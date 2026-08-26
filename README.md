# ulsync-server

**Created:** 2026-08-26 12:35:42 +0500  
**Updated:** 2026-08-26 16:57:07 +0500  
**Version:** 3  
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
