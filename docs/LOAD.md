# Load model and measured capacity

**Created:** 2026-08-28 18:50:00 +0500  
**Updated:** 2026-08-28 18:50:00 +0500  
**Version:** 1  
**Document type:** reference

## How to reproduce

Full commands are filled in after the first measured run on this machine (commit 7). Until then, the sequence is:

1. Raise the file-descriptor soft limit (`ulimit -n`).
2. Build the server binary and install k6 ≥ 1.2.0.
3. Generate ES256 tokens: `go run ./load/gentokens -n 1000 -out load`.
4. Start the server with `load/config.yaml` (not the operator `config.yaml` HS256 loop).
5. Run smoke passes (`SMOKE=1`) for `steady.js`, `live.js`, and `reconnect.js`.
6. Run acceptance passes without `SMOKE` in the order documented in the operator run plan.
7. Measure per-connection RSS with `holdconns` at two points (1 000 then 10 000), sequentially.
8. Copy numbers from terminal output into the **Measured at 1 000 users** section below.

`TODO_MEASURE`: fill after the run — no invented numbers.

## Machine

| Field | Value |
|---|---|
| Model | not measured |
| CPU cores | not measured |
| RAM | not measured |
| Disk | not measured |
| OS | not measured |
| k6 version | not measured |
| Go version | not measured |
| `ulimit -n` (soft) | not measured |
| `ulimit -n` (hard) | not measured |

## Honesty

The load generator (k6 or `holdconns`) and the server run on the same host and share its CPU, memory, and disk. Reported latency and throughput are therefore pessimistic relative to a dedicated server with remote clients. That is preferable to optimistic numbers from an isolated benchmark.

## Measured at 1 000 users

| Metric | Value |
|---|---|
| Client cycles per second | derived, not measured — 1 000 users × 7 clients / 300 s ≈ 23.3 cycles/s |
| Pull p95 (ms) | not measured |
| Push p95 (ms) | not measured |
| HTTP error rate | not measured |
| RSS empty server (KiB) | not measured |
| RSS after steady (KiB) | not measured |
| `live_connections` peak during `live.js` | not measured |
| Per-connection RSS at 1 000 established | not measured |
| Per-connection RSS at 10 000 established | not measured |
| Linear between 1 000 and 10 000? | not measured |

Planning figure from triad plan §7 (not a measurement): on the order of 30 KiB per idle live connection for capacity planning only.

## Extrapolation to 10 000 × 7

Multiply measured values at 1 000 users by 10 for a first-order estimate at 10 000 users (70 000 client devices in the rare-batch model).

| Scales | Hypothesis |
|---|---|
| Linear | RSS of idle live connections; CPU on read-heavy pull traffic |
| Not linear | SQLite single-writer queue — push latency does not scale linearly with user count |

| Column | Formula |
|---|---|
| Estimate | measured at 1 000 × 10 |
| Provisioning (×2 headroom) | estimate × 2 |

Fill numeric cells after the measured run.

## PostgreSQL switch condition

not measured — replaced with a concrete threshold after the steady-batch run (push p95 vs CPU and disk wait).

## File descriptor limit

70 000 idle live connections require a raised per-process open-file limit. On macOS the default soft limit is often 256 or 10 240; `holdconns -n 10000` fails with "too many open files" before memory is exhausted if the limit is too low.

Before measuring connection memory:

```bash
ulimit -n
ulimit -Hn
ulimit -n 200000
```

If the shell refuses 200 000, use the hard maximum:

```bash
ulimit -n $(ulimit -Hn)
```

Record both soft and hard values in **Machine** after the run.

## What was not measured

- TLS terminated by this process (load runs use plain HTTP on loopback).
- Real mobile network latency, packet loss, or carrier NAT behaviour.
- Multiple tenants on one host.
- 70 000 simultaneous live SSE connections (correctness is checked at 100 × 7; capacity is extrapolated).
- Smoke-run (`SMOKE=1`) latency as "1 000 users" numbers.
