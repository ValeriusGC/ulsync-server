# Open questions

**Created:** 2026-08-26 12:35:42 +0500  
**Updated:** 2026-08-31 21:31:37 +0500  
**Version:** 7  
**Document type:** log

Findings that fall outside the current step are recorded here instead of being fixed opportunistically. Each entry has three parts: what was found, where, and why it is not resolved in this change.

Entries are reviewed at the end of round 1 (step 17).

## `/health` storage field shape

**Found:** SPEC.md defines `"storage":"<path>"` as a string; this server returns an object with `path` and `size_bytes`.

**Where:** `protocol/SPEC.md` health section; `internal/httpapi/server.go` GET `/health`.

**Why not here:** ulsync-protocol is a separate repository; SPEC update is a follow-up PR after step 02 merges, before any client parses `/health`.

## `auth.jwks_file` is not in triad-plan §13.2

**Found:** Step 03 needs a static JWKS file for air-gapped hosts and for the load-test path in step 08, but the round-1 configuration shape in the triad plan lists only `jwks_url`.

**Where:** `config.example.yaml` `auth.jwks_file`; triad plan §13.2.

**Why not here:** The field is additive and does not change any existing key. Patching the triad plan is not this PR.

## `/v1/whoami` is not in the protocol SPEC

**Found:** This server exposes `GET /v1/whoami` so a bearer token can be checked without push/pull. SPEC.md places operations only on `admin.bind` and does not list this route.

**Where:** `protocol/SPEC.md` operations section; this repository `GET /v1/whoami`.

**Why not here:** Editing the protocol submodule is a different repository. Same follow-up as the `/health` storage object: a dedicated `ulsync-protocol` PR.

## Pull `limit` above the configured maximum

**Found:** protocol/SPEC.md §3.2 and §5 reject `limit` outside 1…500 with 400. This server clamps to `sync.pull_limit_max` and returns 200, matching the round-1 step 05 prompt.

**Where:** `protocol/SPEC.md` pull limits; `GET /v1/sync/pull` query parsing.

**Why not here:** The protocol lives in ulsync-protocol. A dedicated SPEC PR should say that a limit above the maximum is truncated, not rejected. This server step does not edit the submodule.

## No cap on live pull connections

**Found:** Live pull holds an HTTP connection per waiting device. There is no configuration field that limits how many waiters the process accepts.

**Where:** `GET /v1/sync/pull?live=sse` and `live=poll`; `internal/live` registry.

**Resolution (step 08):** Measured **31.8 KiB/conn** at 1 000 established SSE and **25.7 KiB/conn** at 10 000 (`holdconns`, sequential points, same server process). Scaling is approximately linear between those points. **No `sync.max_live_connections` YAML cap** — memory is a hosting/provisioning limit, not a correctness defect at these numbers. Revisit if a host cannot raise `ulimit -n` enough for the target idle connection count.

## Docker image size not recorded during step 09 execute

**Found:** README still says image size and architecture are "not measured yet"; acceptance requires values from `docker images` / `docker image inspect`.

**Where:** step 09 execute environment; `docker` binary not installed on the build host.

**Why not here:** Run scenario **C** in `TEMP_09_server_docker.md`, paste Size and Architecture into README **Install in five minutes**, then amend commit 6 or add a follow-up commit before merge.
