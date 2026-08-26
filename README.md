# ulsync-server

**Created:** 2026-08-26 12:35:42 +0500  
**Updated:** 2026-08-26 12:35:42 +0500  
**Version:** 1  
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
{"version":"dev","started_at":"2026-08-26T07:35:42Z","storage":"./data/ulsync.db"}
```

No authentication is required. The response includes the configured storage path only; no secrets or database contents.

## Configuration

See [docs/CONFIG.md](docs/CONFIG.md) and `config.example.yaml`. All bind addresses, paths, and timeouts are read from the configuration file.

## Development

```bash
gofmt -l . && go vet ./... && go test ./...
```

## License

Apache-2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).
