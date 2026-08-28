# Load model and measured capacity

**Created:** 2026-08-28 18:50:00 +0500  
**Updated:** 2026-08-28 19:42:00 +0500  
**Version:** 2  
**Document type:** reference

## How to reproduce

All commands from the repository root `/Users/vvk/AndroidStudioProjects/r/ulsync-server`.

```bash
cd /Users/vvk/AndroidStudioProjects/r/ulsync-server
git checkout round-1/16-08-load
go build -o ulsync-server ./cmd/ulsync-server

# k6 v2.2+ with xk6-sse (auto-resolution fails on v2.2.0 as of this run)
go install go.k6.io/xk6/cmd/xk6@latest
xk6 build --with github.com/phymbert/xk6-sse@37cc472 -o load/k6-sse

ulimit -n
ulimit -Hn
ulimit -n 200000

go run ./load/gentokens -n 1000 -out load

rm -f ./data/ulsync-load.db ./data/ulsync-load.db-wal ./data/ulsync-load.db-shm
./ulsync-server -config load/config.yaml > load/server.log 2>&1 &
SRV=$!
sleep 1

# Smoke harness (not recorded in Measured at 1 000)
./load/k6-sse run -e SMOKE=1 load/steady.js
./load/k6-sse run -e SMOKE=1 load/live.js
./load/k6-sse run -e SMOKE=1 load/reconnect.js

# Acceptance (order matters)
# Fresh DB before live correctness
kill $SRV
rm -f ./data/ulsync-load.db ./data/ulsync-load.db-wal ./data/ulsync-load.db-shm
./ulsync-server -config load/config.yaml > load/server.log 2>&1 &
SRV=$!
sleep 1

./load/k6-sse run load/live.js

./load/k6-sse run --out json=/tmp/steady-full-metrics.json load/steady.js

grep -ci 'SQLITE_BUSY' load/server.log
grep -ci 'database is locked' load/server.log
grep -ci 'SQLITE_LOCKED' load/server.log

# Connection memory — sequential points, not simultaneous
ps -o rss= -p $SRV   # baseline KiB after live_connections=0
go run ./load/holdconns -n 1000 -url http://127.0.0.1:8080 -tokens load/tokens.json
# Ctrl+C after established=1000, wait live_connections=0
ps -o rss= -p $SRV
go run ./load/holdconns -n 10000 -url http://127.0.0.1:8080 -tokens load/tokens.json
# Ctrl+C after established=10000

./load/k6-sse run load/reconnect.js
kill -0 $SRV && echo server_alive
```

Use `./load/k6-sse` (not stock `k6`) for `live.js` until k6 v2 resolves `k6/x/sse` automatically.

## Machine

| Field | Value |
|---|---|
| Model | Apple M1 Pro |
| CPU cores | 10 |
| RAM | 16 GiB (17179869184 bytes) |
| Disk | APFS, 461 GiB volume, 95% used at measurement time |
| OS | macOS 26.5.2 (Build 25F84) |
| k6 version | v2.2.0 via `load/k6-sse` (xk6 build with `github.com/phymbert/xk6-sse@37cc472`) |
| Go version | go1.26.4 darwin/arm64 |
| `ulimit -n` (soft) | 1048575 |
| `ulimit -n` (hard) | unlimited |

## Honesty

The load generator (k6 or `holdconns`) and the server run on the same host and share its CPU, memory, and disk. Reported latency and throughput are therefore pessimistic relative to a dedicated server with remote clients. That is preferable to optimistic numbers from an isolated benchmark.

## Measured at 1 000 users

| Metric | Value |
|---|---|
| Client cycles per second | derived, not measured — 1 000 users × 7 clients / 300 s ≈ 23.3 cycles/s |
| Measured HTTP request rate (steady) | 27.75 req/s (`http_reqs` / 630 s wall time) |
| Pull p95 (ms) | 1.033 (`http_req_duration{name:pull}` from `/tmp/steady-full-metrics.json`) |
| Push p95 (ms) | 1.341 (`http_req_duration{name:push}` from same export) |
| HTTP error rate | 0.00% (`http_req_failed` over steady run) |
| RSS after steady (KiB) | 30 064 (`ps -o rss= -p $SRV` immediately after first 10 m steady) |
| `live_connections` peak during `live.js` | 700 (100 users × 7 SSE) |
| Per-connection RSS at 1 000 established (KiB) | 31.8 — (`67 232 − 35 440`) / 1 000 |
| Per-connection RSS at 10 000 established (KiB) | 25.7 — (`328 080 − 70 976`) / 10 000 |
| Linear between 1 000 and 10 000? | Approximately yes (25.7 vs 31.8 KiB/conn; same order of magnitude) |

Planning figure from triad plan §7 (not a measurement): on the order of 30 KiB per idle live connection for capacity planning only.

Thundering herd (`reconnect.js`, 7 000 VU): pull `p(95)` = 235.51 ms — recorded for visibility, not a pass/fail threshold. All seed envelopes found; server process survived.

## Extrapolation to 10 000 × 7

Multiply measured values at 1 000 users by 10 for a first-order estimate at 10 000 users (70 000 client devices in the rare-batch model).

| Scales | Hypothesis |
|---|---|
| Linear | RSS of idle live connections; CPU on read-heavy pull traffic |
| Not linear | SQLite single-writer queue — push latency does not scale linearly with user count |

| Metric | Estimate (×10) | Provisioning (×2 headroom) |
|---|---|---|
| Idle live RSS at 70 000 SSE | 70 000 × 25.7 KiB ≈ 1.8 GiB | ≈ 3.6 GiB |
| Rare-batch pull p95 | ≈ 10 ms (linear guess from 1 ms class) | plan 20 ms budget |
| Rare-batch push p95 | ≈ 13 ms | plan 26 ms budget |

Extrapolation is arithmetic, not a second measurement.

## PostgreSQL switch condition

On this machine at the rare-batch profile (~23 derived cycles/s, measured ~28 HTTP req/s), push `p95` = **1.34 ms** with **0%** HTTP errors and no `SQLITE_BUSY` in the log. CPU was not saturated (single-process Go on 10-core M1 Pro).

**Stay on SQLite** while this profile holds. **Move the writer to PostgreSQL** when, on the same workload shape (1 000 users × 7 clients, 5-minute cycle, ES256 verify), **push `p95` exceeds 200 ms** for a full 10-minute steady run **and** process CPU remains below 50% of one core equivalent — indicating queue/disk wait rather than CPU verify cost. Threshold uses `max(2 × measured push p95, 200 ms)` → **200 ms** dominates at 1.34 ms measured.

## File descriptor limit

70 000 idle live connections require a raised per-process open-file limit. On this MacBook the soft limit was already **1 048 575**; `holdconns` opened **10 000** SSE streams without `too many open files`.

On macOS with a low default (often 256 or 10 240):

```bash
ulimit -n
ulimit -Hn
ulimit -n 200000
# if refused:
ulimit -n $(ulimit -Hn)
```

Record both soft and hard values in **Machine** before claiming a connection count.

## What was not measured

- TLS terminated by this process (load runs use plain HTTP on loopback).
- Real mobile network latency, packet loss, or carrier NAT behaviour.
- Multiple tenants on one host.
- 70 000 simultaneous live SSE connections (correctness checked at 100 × 7; capacity extrapolated).
- Smoke-run (`SMOKE=1`) latency as "1 000 users" numbers.
- `iostat` / disk wait percentages (PostgreSQL threshold uses push p95 + CPU observation only).
