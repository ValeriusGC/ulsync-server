# Changelog

**Created:** 2026-08-26 12:35:42 +0500  
**Updated:** 2026-09-20 18:00:03 +0300  
**Version:** 23  
**Document type:** changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- As root on systemd, `install.sh` for `$HOME/.ulsync/<name>` writes `ulsync-<name>.service` and `enable --now`. Reboot keeps the store. A repeat one-liner against a pid-file job adopts the unit. No root: background + pid, and the result card says so. `--prefix /tmp/...` does not install a unit.
- `install.sh --uninstall --prefix NAME` stops that store (unit and pid). `--purge` deletes the directory. Same `curl | sh` as install. A product prefix also gets `ulsync-<name>-uninstall` on disk (`/usr/local/bin` as root, otherwise `$HOME/.ulsync/`), so teardown does not need GitHub.
- After a successful start `install.sh` prints a plan on stderr before the binary runs (store, mail, panel, whether systemd will enable) and a result card after `/health` (phones URL, firewall, reboot/unit, stop/wipe). Stdout stays the health URL. `curl | sh` cannot prompt; omitted `--listen` still names `0.0.0.0:8080` / `127.0.0.1:8081`. README shows a sample root card.

### Fixed

- `install.sh` no longer copies or downloads onto a running `$PREFIX/ulsync-server`. A second one-liner against a live `/health` for that prefix exits 0. Linux otherwise returns ETXTBSY (`Text file busy`) and the Hands repeat-install gate fails.
- `install.sh` treats `GET /health` as this prefix only when `storage.path` sits under `$PREFIX`, and `wait_health` requires the started pid to still be alive. A neighbor on 8080 no longer makes a colliding install exit 0.
- A busy mail bind is named before spawn when another HTTP already answers, or after a failed start if the log says `address already in use`. The message tells the operator to pass `--listen HOST:PORT`.

### Added

- One host, several stores: first-run `-listen` / `-admin-listen` (and `install.sh --listen` / `--admin-listen`) write `server.bind` and `admin.bind`. `--prefix` is the directory. A live `/health` on 8080 is not treated as success for a different prefix. Empty flags keep `0.0.0.0:8080` and `127.0.0.1:8081`. Changing a port later is a YAML edit and a restart.

- One-line install: POSIX `scripts/install.sh` downloads the linux static binary for the host arch, seeds `$HOME/.ulsync` with exactly one of `--jwks-url` or `--shared-secret`, and starts the process. A second run does not overwrite YAML. There is no macOS binary, and the Dart package is not published.

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

## [0.1.1] - 2026-09-20

### Changed

- `install.sh --prefix notes` is `$HOME/.ulsync/notes`. `$HOME/.ulsync` is the parent of stores, never a store. Omit `--prefix` and the store is `$HOME/.ulsync/default`. README one-liners name the store so a second app is the same command with another name and `--listen`.

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
