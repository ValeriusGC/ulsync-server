# Open questions

**Created:** 2026-08-26 12:35:42 +0500  
**Updated:** 2026-08-26 14:40:04 +0500  
**Version:** 2  
**Document type:** log

Findings that fall outside the current step are recorded here instead of being fixed opportunistically. Each entry has three parts: what was found, where, and why it is not resolved in this change.

Entries are reviewed at the end of round 1 (step 17).

## `/health` storage field shape

**Found:** SPEC.md defines `"storage":"<path>"` as a string; this server returns an object with `path` and `size_bytes`.

**Where:** `protocol/SPEC.md` health section; `internal/httpapi/server.go` GET `/health`.

**Why not here:** ulsync-protocol is a separate repository; SPEC update is a follow-up PR after step 02 merges, before any client parses `/health`.
