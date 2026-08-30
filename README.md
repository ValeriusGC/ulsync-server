# ulsync-server

**Created:** 2026-08-26 12:35:42 +0500  
**Updated:** 2026-08-28 18:50:00 +0500  
**Version:** 8  
**Document type:** readme

Go sync server for the [ulsync](https://github.com/ValeriusGC/ulsync-protocol) protocol. Round 1 delivers push, pull, and live feed against a local SQLite store.

The wire contract lives in the `protocol/` git submodule (`SPEC.md` and golden fixtures). This repository implements the server side only.

## Requirements

- Go 1.26 or later

## Build

```bash
go build -ldflags "-X main.version=$(git describe --tags --always --dirty)" -o ulsync-server ./cmd/ulsync-server
```

Without `-ldflags`, the reported version is `dev`.

## Run

```bash
cp config.example.yaml config.yaml
./ulsync-server -config config.yaml
```

Flags:

- `-config` — path to the YAML configuration file (default `./config.yaml`)
- `-version` — print the build version and exit

Logging uses structured JSON on stdout at info level.

## Health check

With the default `server.bind` (`0.0.0.0:8080`):

```bash
curl -sS localhost:8080/health
```

Expected shape (values vary):

```json
{"version":"dev","started_at":"2026-08-26T07:35:42Z","storage":{"path":"./data/ulsync.db","size_bytes":4096}}
```

No authentication is required. The response reports the database file path and size on disk; it does not expose secrets or database contents.

## Authentication

This server does not issue tokens, register users, or refresh sessions. It only verifies a bearer token that some other identity provider already signed.

Configure either:

- `auth.jwks_url` — the JSON Web Key Set (JWKS) URL (Supabase publishes one at `/auth/v1/.well-known/jwks.json`), or
- `auth.jwks_file` — a static JWKS file, for hosts without outbound internet. When set, the URL is not fetched.

`GET /v1/whoami` is the cheapest way to confirm a token is accepted. It stays in the API because operators use it when a client cannot sync. The operations panel checks user JWTs through `POST /admin/token-check` instead of calling whoami over HTTP. Whoami is not a debug leftover.

```bash
curl -sS -H "Authorization: Bearer $TOKEN" localhost:8080/v1/whoami
```

Expected shape:

```json
{"user_id":"<the token sub claim>"}
```

Missing, malformed, or rejected tokens return `401` with body `{"error":"unauthorized"}` and header `WWW-Authenticate: Bearer`. The reason is written to the process log, not to the client. `GET /health` stays public so Docker and process supervisors can probe liveness without a token.

## Push

`POST /v1/sync/push` accepts exactly one envelope and reports whether the server stored it under last-write-wins rules. The response names `id`, `part`, and `applied` only; `server_seq` is omitted so the client cursor moves only from pull results.

Round 1 accepts a single envelope per request. Send the whole envelope from the protocol fixture, including base64 `payload`:

```bash
TOKEN=<your bearer token>
ENV=$(cat protocol/fixtures/envelope/minimal.json)
curl -sS -X POST localhost:8080/v1/sync/push \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"envelopes\":[$ENV]}"
```

Expected first response (values match the fixture):

```json
{"results":[{"id":"3f2504e0-4f89-11d3-9a0c-0305e82c3301","part":"full","applied":true}]}
```

Sending the same body again returns HTTP `200` with `"applied":false`. That is success, not a conflict: the server already holds an envelope that is not inferior to the one just sent. Two envelopes in one request return `413`.

## Pull

`GET /v1/sync/pull` returns this user's envelopes with `server_seq` greater than `since`, in ascending order, and a `next_cursor` for the next request. An empty page is the normal stop of the catch-up loop, not an error. Pull responses include `server_seq`; push responses do not — the client cursor moves only from pull.

Seed two envelopes first (a second `id`, or both pages with `limit=1` would be empty after the first):

```bash
TOKEN=<your bearer token>
ENV1=$(cat protocol/fixtures/envelope/minimal.json)
curl -sS -X POST localhost:8080/v1/sync/push \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"envelopes\":[$ENV1]}"
ENV2=$(sed 's/3f2504e0-4f89-11d3-9a0c-0305e82c3301/3f2504e0-4f89-11d3-9a0c-0305e82c3302/' protocol/fixtures/envelope/minimal.json)
curl -sS -X POST localhost:8080/v1/sync/push \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"envelopes\":[$ENV2]}"
```

Then walk with `limit=1`. Substitute the **returned** `next_cursor`; do not add `limit` in your head. Sequence gaps are legal, so arithmetic would skip rows. On a database that already had rows the numbers differ — use the `next_cursor` field, not this example.

```bash
curl -sS -H "Authorization: Bearer $TOKEN" 'localhost:8080/v1/sync/pull?since=0&limit=1'
```

Expected first page on a fresh store (`next_cursor` equals the page's `server_seq`):

```json
{"envelopes":[{"id":"3f2504e0-4f89-11d3-9a0c-0305e82c3301","part":"full","entity_type":"counter_operation","created_at_ms":1756100000000,"last_edited_at_ms":1756100000000,"revision":1,"source_id":"device-a","flags":0,"schema_version":1,"payload_encoding":"json","payload":"eyJ0eXBlIjoiaW5jcmVtZW50In0=","server_seq":1}],"next_cursor":1}
```

```bash
curl -sS -H "Authorization: Bearer $TOKEN" 'localhost:8080/v1/sync/pull?since=1&limit=1'
```

Expected second page:

```json
{"envelopes":[{"id":"3f2504e0-4f89-11d3-9a0c-0305e82c3302","part":"full","entity_type":"counter_operation","created_at_ms":1756100000000,"last_edited_at_ms":1756100000000,"revision":1,"source_id":"device-a","flags":0,"schema_version":1,"payload_encoding":"json","payload":"eyJ0eXBlIjoiaW5jcmVtZW50In0=","server_seq":2}],"next_cursor":2}
```

```bash
curl -sS -H "Authorization: Bearer $TOKEN" 'localhost:8080/v1/sync/pull?since=2&limit=1'
```

Expected empty page (`next_cursor` equals the submitted `since`, not zero):

```json
{"envelopes":[],"next_cursor":2}
```

Omit `limit` to use `sync.pull_limit_default` (100). A `limit` above `sync.pull_limit_max` is truncated to that cap and still returns `200`. Unknown query parameters other than `live` are ignored. `live` selects immediate, long-poll, or SSE delivery; see [Live updates](#live-updates).

## Live updates

Device B learns that device A wrote a row without polling on a timer and without a refresh button. It holds `GET /v1/sync/pull` open; the server writes into that request when **this** `user_id` gets a new row. The URL is the same as Pull; only the `live` query parameter changes the delivery.

The server does not issue tokens. `$TOKEN` is the same bearer string as in [Pull](#pull).

### Immediate (no `live`)

Omit `live`, or pass an empty value. The handler answers at once, possibly with an empty page. Catch-up is the same loop as [Pull](#pull): substitute the returned `next_cursor` until a page is empty.

```bash
curl -sS -H "Authorization: Bearer $TOKEN" 'localhost:8080/v1/sync/pull?since=0&limit=10'
```

### Long poll (`live=poll`)

If the page after `since` is empty, the server holds the request until a write for this user commits or `sync.live_poll_timeout` elapses (55 seconds by default, under the common 60-second proxy idle limit). The JSON shape is the same as an immediate pull.

Two terminals. In the first, wait on a cursor that has no rows yet:

```bash
time curl -sS -H "Authorization: Bearer $TOKEN" 'localhost:8080/v1/sync/pull?since=999&live=poll'
```

In the second, push a fixture as in [Push](#push). The first curl must return in a fraction of a second after the push, not after 55 seconds, with the new envelope in `envelopes`.

If nothing is pushed, the first curl still returns HTTP `200` after about 55 seconds: `"envelopes":[]` and `next_cursor` equal to the submitted `since` (here 999). The wait comes from `sync.live_poll_timeout` in the configuration file, not from a constant in the binary.

### Server-Sent Events (`live=sse`)

Use `curl -N`. Without `-N`, curl buffers the stream itself and it looks as if "nothing arrives" — that is curl, not the server.

```bash
curl -N -sS -H "Authorization: Bearer $TOKEN" 'localhost:8080/v1/sync/pull?since=0&live=sse'
```

The response headers are `Content-Type: text/event-stream`, `Cache-Control: no-cache`, and `X-Accel-Buffering: no` (so Nginx does not accumulate the stream and dump it at the end). The body starts with whatever already exists after `since`: one `event: envelope` per row, then `event: cursor`. While the connection is silent, the server writes the SSE comment `: ping` every `sync.live_heartbeat` (15 seconds by default) so a mobile carrier NAT does not drop the TCP session.

Leave that curl running. In another terminal, push a fixture. An `envelope` event and a `cursor` event must appear in the first terminal within milliseconds, not after the next ping.

The bearer token is checked when the connection **opens**. The server does not close the stream when the token's `exp` elapses. Reopening with a fresh token is the client's job (the Dart package, step 13). Checking `exp` inside the loop would make sync go silent at token expiry with nothing in the log that looks like an error.

Any other `live` value (including `SSE` or `Poll`) is `400` with body `{"error":"invalid parameter","param":"live"}`.

## Operations page

The process listens on a second address (`admin.bind`, default `127.0.0.1:8081`) for a read-only operations panel. Sync traffic stays on `server.bind`; firewall rules for sync do not automatically expose the panel.

Open the panel on the local machine:

```bash
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8081/admin
```

Expected: HTTP `200` with `Content-Type: text/html`.

The default bind is loopback-only so the panel is not reachable from other hosts without an explicit tunnel or a non-loopback bind plus `admin.token`. To view the panel from another machine, forward the port over SSH:

```bash
ssh -L 8081:127.0.0.1:8081 user@host
```

Then open `http://127.0.0.1:8081/admin` in a browser on your laptop. The tunnel terminates on the server's loopback listener.

The panel is read-only: it shows process metrics, storage counters, and a redacted copy of the configuration. It never edits YAML or database rows. `POST /admin/token-check` only validates a user JWT and reports the subject; it does not change server state.

Two different secrets appear in operator workflows:

- **`admin.token`** — a shared password from YAML that protects the panel routes (`/admin`, `/admin/events`, `/admin/token-check`) when set. It is **not** a JWT. Send it as `Authorization: Bearer <admin.token>` on curl requests when the field is non-empty.
- **User JWT** — the same bearer token clients use on `/v1/*`. Paste it into the token-check form or POST it to `/admin/token-check` as JSON `{"token":"…"}`.

Browsers cannot attach custom headers to `EventSource`, so the in-page snapshot stream works without `admin.token` only when the panel bind is loopback and `admin.token` is empty (including over an SSH tunnel to `127.0.0.1`). When `admin.token` is set, use curl with `-H Authorization` for `/admin/events`; the HTML stream will receive `401` without a header.

Every value on the page (version, database path, counters, configuration) arrives from the SSE snapshot stream once per second. Nothing is baked into the HTML markup, so changing the configuration file and restarting changes the page without rebuilding the binary.

Snapshot stream:

```bash
curl -N -sS http://127.0.0.1:8081/admin/events | head -c 400; echo
```

Expected: `Content-Type: text/event-stream` and a JSON `data:` frame containing `version`, `storage`, `config`, and `live_connections`.

User token check (replace `$TOKEN` with a JWT your identity provider signed, or a development HS256 token minted with `auth.dev_hs256_secret`):

```bash
curl -sS -X POST http://127.0.0.1:8081/admin/token-check \
  -H 'Content-Type: application/json' \
  -d "{\"token\":\"$TOKEN\"}"; echo
```

Expected for a valid token: `{"valid":true,"subject":"<sub claim>"}`. Expected for garbage: `{"valid":false,"reason":"…"}` with a non-empty reason.

## Storage

The server uses a single SQLite database file configured by `storage.path`. One writer connection serializes all writes inside the process; readers use a separate pool. Schema migrations run automatically on startup when the store opens.

## Configuration

See [docs/CONFIG.md](docs/CONFIG.md) and `config.example.yaml`. All bind addresses, paths, and timeouts are read from the configuration file.

## Load testing

Load runs use `load/config.yaml` and ES256 bearer tokens from `go run ./load/gentokens -n 1000 -out load`. They do **not** use the operator `config.yaml` HS256 development loop or `dev_hs256_secret`.

The database file for load runs is `./data/ulsync-load.db`, separate from the operator store. Generated `load/jwks.json` and `load/tokens.json` are gitignored.

k6 scenarios (`load/steady.js`, `load/live.js`, `load/reconnect.js`) model 1 000 product users with seven clients each. Per-connection memory is measured with `load/holdconns`, not k6.

`live.js` requires a custom k6 binary with the community `xk6-sse` extension. Build it once:

```bash
go install go.k6.io/xk6/cmd/xk6@latest
xk6 build --with github.com/phymbert/xk6-sse@37cc472 -o load/k6-sse
```

Use `./load/k6-sse run load/live.js` (stock k6 v2.2.0 auto-resolution for `k6/x/sse` failed on this machine).

Full reproduction commands, measured numbers, and extrapolation to 10 000 users are in [docs/LOAD.md](docs/LOAD.md).

## Development

```bash
gofmt -l . && go vet ./... && go test ./...
```

## License

Apache-2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).
