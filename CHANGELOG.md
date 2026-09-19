# Changelog

**Created:** 2026-08-26 12:35:42 +0500  
**Updated:** 2026-09-19 21:01:53 +0300  
**Version:** 15  
**Document type:** changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- One-line install: POSIX `scripts/install.sh` downloads the linux static binary for the host arch, seeds `$HOME/.ulsync` with exactly one of `--jwks-url` or `--shared-secret`, and starts the process in the background. A second run does not overwrite YAML. There is no systemd unit, no macOS binary, and the Dart package is not published.

- GitHub Actions release workflow on `v*` tags: static linux `amd64` and `arm64` binaries (`CGO_ENABLED=0`) plus `install.sh`, published with `gh release create`. The job fails when `scripts/install.sh` is absent so a tag cannot ship two binaries without the installer.

- First-run seed: when `-config` is missing, exactly one of `-jwks-url` or `-shared-secret` writes the YAML from embedded defaults, then the process starts. An existing file is never overwritten. URL seed leaves `dev_hs256_secret` empty; secret seed leaves `jwks_url` empty after `Load` and lists `HS256`. The process does not invent `local-dev-only` and does not accept a private PEM.
- `applyDefaults` no longer fills the Supabase `jwks_url` placeholder when `dev_hs256_secret` or `jwks_file` is set, so a secret-only file does not fetch a foreign host.

- `POST /v1/sync/push` applies up to 500 envelopes in one store transaction. Default `sync.max_envelopes_per_push` is 500; values above 500 refuse startup. A client that still sends one envelope remains compatible.
- `store.UpsertMany` wraps the existing per-row upsert SQL in a single `BEGIN`…`COMMIT`. The push handler validates the whole batch and rejects duplicate `(id, part)` keys before writing; live waiters wake once after commit when at least one row was stored.

- Store origin: `server_meta` remembers which application contour owns the database; `GET /v1/sync/hello` imprints an open store; optional `origin:` in configuration pins an authored store at startup.
- `Ulsync-Origin` middleware on `/v1/sync/*` refuses a foreign application before mail runs. Legacy clients without the header still work on an open store after imprint.
- Operations panel shows configured `origin` and the live value from `server_meta` (read-only).
- `POST /v1/sync/diff`: read-only divergence check returning `missing` and `stale` lists by the three conflict ranks of SPEC §2 (protocol submodule bumped to the step-18 contract).
- `store.Wins` expresses the same last-write-wins rule as `upsertEnvelopeSQL`; a table-driven test pins the two implementations together without changing the write path.

## [0.1.0] - 2026-08-31

### Added

- Go server skeleton: single-file YAML configuration, `GET /health`, graceful shutdown, CI on Go 1.26, and `ulsync-protocol` git submodule.
- SQLite storage with embedded migrations, separate writer and reader pools, and per-user sequence allocation.
- JWT bearer verification against a JWKS (`RS256` and `ES256`), optional `aud`/`iss`, static `auth.jwks_file` for hosts without outbound internet, and `GET /v1/whoami`.
- `POST /v1/sync/push`: single-envelope last-write-wins upsert with transactional sequence allocation; push responses omit `server_seq`.
- `GET /v1/sync/pull` returns envelopes after a cursor for the authenticated user; `next_cursor` comes from returned rows; per-user isolation is enforced in the query.
- Live pull: `live=poll` holds the request until a change or `sync.live_poll_timeout`; `live=sse` streams `envelope` and `cursor` events with a `: ping` heartbeat.
- Operations page on `admin.bind`: embedded read-only UI, live SSE snapshot, `POST /admin/token-check`, and secret redaction on the configuration type.
- k6 load scenarios (steady batch, live wakeups, thundering herd), ES256 token generator, connection holder, and `docs/LOAD.md` with measured capacity numbers.
- Multi-stage Docker image (`gcr.io/distroless/static-debian12`, non-root UID 65532), `compose.yaml` with named volume and host loopback admin publish, `-healthcheck` flag for distroless `HEALTHCHECK`, CI image build on every pull request, and five-minute installation README.
