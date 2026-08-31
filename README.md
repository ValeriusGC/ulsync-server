# ulsync-server

**Created:** 2026-08-26 12:35:42 +0500  
**Updated:** 2026-08-31 21:31:37 +0500  
**Version:** 10  
**Document type:** readme

## What this is

Go sync server for the [ulsync](https://github.com/ValeriusGC/ulsync-protocol) protocol. Round 1 delivers push, pull, and live feed against a local SQLite store.

The wire contract lives in the `protocol/` git submodule (`SPEC.md` and golden fixtures). This repository implements the server side only.

## Install in five minutes

You need Docker and Docker Compose V2 on the host. Go is not required for this path.

Create `config.yaml` in the repository root (the file is gitignored):

```yaml
server:
  bind: "0.0.0.0:8080"
  read_header_timeout: "5s"
  idle_timeout: "120s"
  max_body_bytes: 1048576

storage:
  driver: "sqlite"
  path: "/data/ulsync.db"

auth:
  jwks_url: "https://<project>.supabase.co/auth/v1/.well-known/jwks.json"
  jwks_file: ""
  jwks_cache_ttl: "10m"
  allowed_algs: ["ES256", "RS256"]
  audience: []
  issuer: ""
  dev_hs256_secret: ""

sync:
  max_envelopes_per_push: 1
  pull_limit_default: 100
  pull_limit_max: 500
  live_poll_timeout: "55s"
  live_heartbeat: "15s"

admin:
  bind: "0.0.0.0:8081"
  token: "change-me"
```

Start the server:

```bash
docker compose up -d
```

Check health (wait a few seconds after the first start):

```bash
curl -sS localhost:8080/health; echo
```

Expected shape (values vary except the database path):

```json
{"version":"…","started_at":"…","storage":{"path":"/data/ulsync.db","size_bytes":4096}}
```

The `storage.path` field must be `"/data/ulsync.db"`. No authentication is required on `/health`.

Image size: not measured yet — run scenario **C** in `TEMP_09_server_docker.md` after `docker build` and paste the `docker images` output here before merge.

Architecture: not measured yet — run `docker image inspect ulsync-server:test --format '{{.Architecture}}'` after build and paste here before merge.

The runtime image is `gcr.io/distroless/static-debian12` because the binary is static Go (`CGO_ENABLED=0`): there is no shell and no curl, so container health uses the `-healthcheck` program flag instead of a probe binary.

## Configuration

See [docs/CONFIG.md](docs/CONFIG.md) and `config.example.yaml` for every field.

For production JWT verification, set `auth.jwks_url` to your Supabase project's JWKS endpoint:

`https://<project>.supabase.co/auth/v1/.well-known/jwks.json`

Replace `<project>` with your project reference from the Supabase dashboard (Settings → API).

**Docker and the operations panel.** Inside a container, published ports reach the process on the container's network interface (`0.0.0.0`), not on container loopback (`127.0.0.1`). If `admin.bind` is `127.0.0.1:8081` inside the container, Docker's port mapping cannot reach the listener. Use `admin.bind: "0.0.0.0:8081"` and a non-empty `admin.token`, then publish the panel on the **host** as `127.0.0.1:8081:8081` in `compose.yaml` so the panel stays off the LAN.

## Hands-on check

The five-minute install above does not need Go or signed JWTs. The commands below exercise push, pull, and live delivery end-to-end. They require:

- this repository cloned with `git submodule update --init --recursive` (for `protocol/fixtures/`);
- Go 1.26+ on the host (to mint a development token);
- the same `config.yaml` as in [Install in five minutes](#install-in-five-minutes), **plus** HS256 for local signing.

Add to the `auth:` block in `config.yaml`:

```yaml
  allowed_algs: ["ES256", "RS256", "HS256"]
  dev_hs256_secret: "local-dev-only"
```

Restart compose after editing:

```bash
docker compose up -d
```

Mint a bearer token (run from the repository root; needs `go.mod`):

```bash
cat >/tmp/mint_dev_jwt.go <<'EOF'
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	sub := "alice"
	if len(os.Args) > 1 {
		sub = os.Args[1]
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   sub,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
	})
	s, err := t.SignedString([]byte("local-dev-only"))
	if err != nil {
		panic(err)
	}
	fmt.Print(s)
}
EOF
export TOKEN=$(go run /tmp/mint_dev_jwt.go)
```

Whoami:

```bash
curl -sS -H "Authorization: Bearer $TOKEN" localhost:8080/v1/whoami
```

Expected: `{"user_id":"alice"}`.

Push one envelope:

```bash
ENV=$(cat protocol/fixtures/envelope/minimal.json)
curl -sS -X POST localhost:8080/v1/sync/push \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"envelopes\":[$ENV]}"
```

Expected first response:

```json
{"results":[{"id":"3f2504e0-4f89-11d3-9a0c-0305e82c3301","part":"full","applied":true}]}
```

Pull:

```bash
curl -sS -H "Authorization: Bearer $TOKEN" 'localhost:8080/v1/sync/pull?since=0&limit=10'; echo
```

Expected: the envelope above in `envelopes`, with `next_cursor` equal to its `server_seq`.

Live SSE (two terminals). Terminal 1:

```bash
curl -N -sS -H "Authorization: Bearer $TOKEN" 'localhost:8080/v1/sync/pull?since=0&live=sse'
```

Terminal 2 — push a **new** id so `applied` is true:

```bash
export TOKEN=$(go run /tmp/mint_dev_jwt.go)
ENV2=$(sed 's/3f2504e0-4f89-11d3-9a0c-0305e82c3301/3f2504e0-4f89-11d3-9a0c-0305e82c3302/' protocol/fixtures/envelope/minimal.json)
curl -sS -X POST localhost:8080/v1/sync/push \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"envelopes\":[$ENV2]}"
```

Terminal 1 must show `event: envelope` and `event: cursor` within milliseconds, not after the ~15 s heartbeat.

## Operations page

Sync traffic stays on port 8080. The operations panel listens on `admin.bind` (8081 by default).

On the host machine with Docker Compose:

```bash
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8081/admin
```

Expected: HTTP `401` with body `{"error":"unauthorized"}` when `admin.token` is set (as in the five-minute YAML). With the panel password:

```bash
curl -sS -o /dev/null -w '%{http_code}\n' -H 'Authorization: Bearer change-me' http://127.0.0.1:8081/admin
```

Expected: HTTP `200` with `Content-Type: text/html`.

`compose.yaml` publishes the panel as `127.0.0.1:8081:8081` on the host so it is not reachable from other machines on the LAN.

To view the panel from another machine, forward the port over SSH:

```bash
ssh -L 8081:127.0.0.1:8081 user@host
```

Then open `http://127.0.0.1:8081/admin` in a browser on your laptop.

The server refuses to start when `admin.bind` listens on a non-loopback address while `admin.token` is empty. Without this check, exposing the panel on `0.0.0.0` would bypass firewall intent.

Two different secrets:

- **`admin.token`** — shared password from YAML for `/admin` routes. Not a JWT. Send `Authorization: Bearer <admin.token>`.
- **User JWT** — bearer token for `/v1/*` and `POST /admin/token-check`.

Browsers cannot attach custom headers to `EventSource`. When `admin.token` is set, the in-page snapshot stream receives `401` without a header; use curl with `-H Authorization` for `/admin/events`.

The panel is read-only: metrics, storage counters, and redacted configuration. It never edits YAML or database rows.

## TLS and reverse proxies

The cheapest way to add TLS is to terminate encryption on the host with Caddy or Nginx in front of the published sync port.

Caddy on the host:

```
sync.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Nginx must disable response buffering for `live=sse` and long-poll, or the live feed arrives as one chunk. The canonical fragment and explanation are in [docs/CONFIG.md](docs/CONFIG.md) under **Reverse proxies and buffering**. Do not copy the `location /v1/` block here — that file is the single source of truth.

## Operating system limits

Each idle live connection holds a file descriptor. Check the process limit before large deployments:

```bash
ulimit -n
```

Raise it in the service manager or shell profile if the value is below your expected connection count. See [docs/LOAD.md](docs/LOAD.md) section **File descriptor limit** for planning numbers.

## Data durability

In Docker, the SQLite database lives at `/data/ulsync.db` inside the container, on the named volume `ulsync-data` declared in `compose.yaml`.

SQLite runs in WAL mode. Besides the main file, expect companion files alongside it:

- `ulsync.db-wal` — uncommitted changes waiting to be checkpointed
- `ulsync.db-shm` — shared-memory index for WAL readers

Do not copy only the `.db` file while the server is running. The image has no `sqlite3` CLI; stop the container and copy the whole data directory:

```bash
cd /Users/vvk/AndroidStudioProjects/r/ulsync-server
docker compose stop
mkdir -p ./backup-ulsync
docker compose cp ulsync-server:/data/. ./backup-ulsync/
docker compose start
```

On bare metal with `sqlite3` installed, prefer a consistent backup:

```bash
sqlite3 ulsync.db ".backup copy.db"
```

`docker compose down` without `-v` keeps the named volume. `docker compose down -v` destroys the database.

## Upgrading

Rebuild and restart with the same `config.yaml` and volume:

```bash
docker compose up -d --build
```

Embedded migrations run automatically on startup. Do not delete the `ulsync-data` volume during a routine upgrade.

## Capacity

Measured at 1 000 users (seven clients each): pull p95 **1.033** ms, push p95 **1.341** ms, HTTP error rate **0.00%**, per-connection RSS **31.8** KiB at 1 000 established connections and **25.7** KiB at 10 000. Full reproduction and extrapolation are in [docs/LOAD.md](docs/LOAD.md).

## What the server does not do

- It does not issue login tokens or manage user accounts.
- It does not inspect or interpret envelope payloads.
- It does not resolve application-level conflicts beyond last-write-wins on the envelope tuple.
- Round 1 does not support batch push, tombstones/deletion, or `part` values other than `full`.

## Build from source

### Requirements

- Go 1.26 or later

### Build

```bash
go build -ldflags "-X main.version=$(git describe --tags --always --dirty)" -o ulsync-server ./cmd/ulsync-server
```

Without `-ldflags`, the reported version is `dev`.

### Run

```bash
cp config.example.yaml config.yaml
./ulsync-server -config config.yaml
```

Flags:

- `-config` — path to the YAML configuration file (default `./config.yaml`)
- `-version` — print the build version and exit
- `-healthcheck` — `GET http://127.0.0.1:8080/health` and exit 0 only on HTTP 200 (used by Docker `HEALTHCHECK`; see `Dockerfile`)

Logging uses structured JSON on stdout at info level.

### Health check

```bash
curl -sS localhost:8080/health
```

Expected shape (values vary):

```json
{"version":"dev","started_at":"2026-08-26T07:35:42Z","storage":{"path":"./data/ulsync.db","size_bytes":4096}}
```

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
