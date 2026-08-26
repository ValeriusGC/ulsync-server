# Configuration reference

**Created:** 2026-08-26 12:35:42 +0500  
**Updated:** 2026-08-26 12:35:42 +0500  
**Version:** 1  
**Document type:** reference

The server reads a single YAML file (see `config.example.yaml`). Every runtime path, bind address, and timeout comes from this file; nothing is hard-coded in the binary.

## server

| Field | Type | Default | Purpose |
|---|---|---|---|
| `bind` | string | `0.0.0.0:8080` | Listen address for `/health` and `/v1/*`. |
| `read_header_timeout` | duration | `5s` | Closes connections that do not send headers in time. |
| `idle_timeout` | duration | `120s` | Closes idle keep-alive connections. |
| `max_body_bytes` | integer | `1048576` | Maximum request body size (1 MiB). |

## storage

| Field | Type | Default | Purpose |
|---|---|---|---|
| `driver` | string | `sqlite` | Storage backend name (only `sqlite` in round 1). |
| `path` | string | `./data/ulsync.db` | SQLite file path; shown in `/health` until the store is wired (step 02). |

## auth

| Field | Type | Default | Purpose |
|---|---|---|---|
| `jwks_url` | string | Supabase JWKS URL placeholder | URL to fetch JSON Web Key Set for JWT verification. |
| `jwks_cache_ttl` | duration | `10m` | How long fetched JWKS keys stay cached. |
| `allowed_algs` | string list | `ES256`, `RS256` | Accepted JWT signing algorithms. |
| `audience` | string list | empty | Optional JWT `aud` claim values to require. |
| `issuer` | string | empty | Optional JWT `iss` claim value to require. |
| `dev_hs256_secret` | string | empty | Development-only shared secret for HS256 (step 03). |

## sync

| Field | Type | Default | Purpose |
|---|---|---|---|
| `max_envelopes_per_push` | integer | `1` | Maximum envelopes per push request (round 1: exactly one). |
| `pull_limit_default` | integer | `100` | Default page size for pull when `limit` is omitted. |
| `pull_limit_max` | integer | `500` | Hard cap for pull `limit`. |
| `live_poll_timeout` | duration | `55s` | Long-poll wait when `live=poll`. |
| `live_heartbeat` | duration | `15s` | SSE comment interval to keep connections alive. |

## admin

| Field | Type | Default | Purpose |
|---|---|---|---|
| `bind` | string | `127.0.0.1:8081` | Listen address for the operations panel (step 07). |
| `token` | string | empty | Shared secret required to access `/admin` when not on loopback. |

## Admin bind safety

The server refuses to start when `admin.bind` listens on a non-loopback address (for example `0.0.0.0:8081`) while `admin.token` is empty. Without this check, the operations page could be exposed on the network by a configuration mistake long before step 07 adds the listener. Fix by binding to `127.0.0.1` or setting a non-empty `admin.token`.
