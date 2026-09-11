# Round 1 verification result

**Created:** 2026-09-11 09:59:46 +0300  
**Updated:** 2026-09-11 09:59:46 +0300  
**Version:** 1  
**Document type:** verification result

**Round closed: yes.**

Operator evidence is the filled table in `flutter-senior-prep/plan_triad/handoffs/TEMP_17_verify.md` section 5 (version 13, updated 2026-09-11). This file does not invent passes. Product Go was not changed in step 17. Full `steady.js` was not re-run.

## Eight claims

| # | Claim | Result | How verified |
|---|---|---|---|
| 1 | Two devices, one account: increment on A appears on B without a refresh; after the server is stopped and started, the apps reopen live without a process restart | pass | TEMP_17_verify §3.11–3.12. Server host and device identifiers were left as blank placeholders in section 5. |
| 2 | Two users do not see each other's data | pass | TEMP_17_verify §3.14 (second account on device B) and `go test -race ./internal/httpapi -run TestPullIsolatesUsersIncludingSameID` |
| 3 | Foreign signature, expired token, empty `sub`, algorithm outside the allow-list: each request is 401 with one body | pass | TEMP_17_verify §4.3 (four `curl` calls to `GET /v1/whoami` on local Docker) |
| 4 | `non_utf8_payload.json` pull returns payload bytes `fffe0041` | pass | TEMP_17_verify §4.4 |
| 5 | Repeat push returns `applied: false` and HTTP 200, `server_seq` does not grow; the app send queue does not loop | pass | TEMP_17_verify §4.5 (server `curl`) and §3.13 (app logs on the Ubuntu host run) |
| 6 | Equal `last_edited_at_ms` and `revision`, both arrival orders: one winner (`device-b`) | pass | TEMP_17_verify §4.6 (two `curl` orders on local Docker) |
| 7 | README five-minute Docker path (no Go) reaches `/health`; Hands-on check reaches whoami, push, pull, and `live=sse` | pass | TEMP_17_verify §4.1 and §4.7. `live=poll` is not in the README Hands-on check and does not affect this claim (§4.7.6). |
| 8 | Operations page is password-protected; process refuses to start if `admin.bind` is exposed without `admin.token`; `docs/LOAD.md` has measured numbers and a numeric PostgreSQL threshold | pass | TEMP_17_verify §4.8 |

## Where this was checked

- **Claims 1, 2, and 5 (app queue):** TEMP_17_verify §3. Procedure is Docker Compose on an Ubuntu host that is not the development Mac, plus two Android device contours. The section 5 table does not record the host address or device identifiers.
- **Claims 3–8 (except the app half of 5):** TEMP_17_verify §4. Development Mac, local Docker Desktop, `localhost:8080` and `127.0.0.1:8081`.
- **Device count in the round check:** two Android contours (TEMP_17_verify §3.10–3.14). More than two devices were not run.
- **Load numbers** are not from this step. They are the step 08 record in [docs/LOAD.md](LOAD.md). Machine table there: Apple M1 Pro, 10 cores, 16 GiB, macOS 26.5.2 (Build 25F84), k6 v2.2.0 via `load/k6-sse`, go1.26.4 `darwin/arm64`. Honesty section: generator and server shared that host.
- **Server tree recorded here:** `main` at `9c3c1b9` (this branch's tip before the step 17 docs commits), Go module `go 1.26` in `go.mod`, changelog `[0.1.0]`. README **Install in five minutes** records image size 13.1MB and architecture arm64. This step did not re-run `docker images`.

## Measured numbers

Cited from [docs/LOAD.md](LOAD.md) (not re-measured in step 17):

- HTTP request rate (steady): 27.75 req/s
- Pull p95: 1.033 ms
- Push p95: 1.341 ms
- HTTP error rate: 0.00%
- Per-connection RSS at 1 000 established SSE: 31.8 KiB
- Per-connection RSS at 10 000 established SSE: 25.7 KiB
- PostgreSQL switch: move the writer when push p95 exceeds **200 ms** on the same rare-batch shape (`docs/LOAD.md` section **PostgreSQL switch condition**)

## What was not tested

Required list for this result (step 17 prompt), none of these were run as a round-check claim:

- Server-side encryption / TLS terminated by this process
- Real mobile carrier (latency, loss, NAT)
- More than two devices
- Multi-day soak
- Multi-tenant on one host

Load-file gaps already listed in `docs/LOAD.md` **What was not measured** (70 000 simultaneous live SSE, `iostat`, smoke-run numbers treated as 1 000-user figures) stay as written there.

Out of assertion 1 by TEMP_17_verify §3.12: start the apps while the server is down, then bring the server up. That path is not a pass/fail cell for claim 1.

## Defects found

- **No `fix(` commits and no CHANGELOG `Fixed` section** on `main` in this repository. This step does not invent product-bug PR links.
- **SPEC / config debts** remain open in [docs/OPEN_QUESTIONS.md](OPEN_QUESTIONS.md): `/health` `storage` object vs string; `GET /v1/whoami` absent from SPEC; pull `limit` clamp vs SPEC 400; `auth.jwks_file` vs triad-plan §13.2. Not edited in this step. SPEC debts are pointed at `ulsync-protocol`.
- **Docker image size** (open question from step 09 execute): README already records 13.1MB / arm64 as of `9c3c1b9` (`feat(docker): … (#18 / #19)`). This step closes the log entry; it does not re-measure.
- **Live connection cap:** already **Resolution (step 08)** in the open-questions log (31.8 / 25.7 KiB/conn; no YAML cap). Meaning unchanged in this step.
- **Application finding, 2026-09-10** (TEMP_17_verify §3.12; `counter_schmounter/docs/OPEN_QUESTIONS.md`): live does not open if the first `syncOnce` at app start failed (apps launched while the server was down). Not in assertion 1. Not fixed in this repository or in this PR. Product code is out of step 17.
