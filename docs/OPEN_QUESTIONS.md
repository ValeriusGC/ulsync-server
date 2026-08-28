# Open questions

**Created:** 2026-08-26 12:35:42 +0500  
**Updated:** 2026-08-27 20:16:00 +0500  
**Version:** 5  
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

**Why not here:** The cap depends on memory and the process file-descriptor limit. Step 08 load numbers decide whether a config field is needed. Adding a guess now would be a fake limit.
