# ulsync-server

**Created:** 2026-08-26 12:35:42 +0500  
**Updated:** 2026-08-27 13:46:10 +0500  
**Version:** 5  
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

`GET /v1/whoami` is the cheapest way to confirm a token is accepted. It stays in the API because operators use it when a client cannot sync, and the operations panel (step 07) will call it as a token check. It is not a debug leftover.

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

Omit `limit` to use `sync.pull_limit_default` (100). A `limit` above `sync.pull_limit_max` is truncated to that cap and still returns `200`. Unknown query parameters are ignored.

## Storage

The server uses a single SQLite database file configured by `storage.path`. One writer connection serializes all writes inside the process; readers use a separate pool. Schema migrations run automatically on startup when the store opens.

## Configuration

See [docs/CONFIG.md](docs/CONFIG.md) and `config.example.yaml`. All bind addresses, paths, and timeouts are read from the configuration file.

## Development

```bash
gofmt -l . && go vet ./... && go test ./...
```

## License

Apache-2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).
