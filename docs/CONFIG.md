# Configuration reference

**Created:** 2026-08-26 12:35:42 +0500  
**Updated:** 2026-08-27 20:16:00 +0500  
**Version:** 6  
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
| `path` | string | `./data/ulsync.db` | SQLite database file path. |

The server creates the parent directory and the database file on first start. Schema migrations run automatically when the store opens; no manual migration step is required.

SQLite runs in WAL (write-ahead log) journal mode. Besides the main file at `storage.path`, the server creates companion files alongside it:

- `<path>-wal` — uncommitted changes waiting to be checkpointed into the main file
- `<path>-shm` — shared-memory index for WAL readers

When copying or moving the database, copy all three files together while the server is stopped. Copying only the main `.db` file can leave recent writes in the WAL and produce a database that looks empty or stale.

## auth

| Field | Type | Default | Purpose |
|---|---|---|---|
| `jwks_url` | string | Supabase JWKS URL placeholder | URL of the JSON Web Key Set used to verify bearer tokens. |
| `jwks_file` | string | empty | Path to a static JWKS file. When non-empty, the server reads this file and does not fetch `jwks_url`. |
| `jwks_cache_ttl` | duration | `10m` | How long a successfully loaded key set stays cached before the next refresh. |
| `allowed_algs` | string list | `ES256`, `RS256` | Algorithms the parser will accept. Anything else, including `none` and `HS256`, is rejected unless listed here. |
| `audience` | string list | empty | JWT `aud` values to require. An empty list means audience is not checked. |
| `issuer` | string | empty | JWT `iss` value to require. Empty means issuer is not checked. |
| `dev_hs256_secret` | string | empty | Development-only HMAC secret. Empty in every non-dev deployment. |

The server never issues tokens. It fetches **public** keys (or reads them from `jwks_file`) and extracts `sub` as `user_id`.

Supabase's edge caches the JWKS response for 10 minutes. A newly published signing key may therefore be invisible to this process for up to that long even if `jwks_cache_ttl` is shorter; values under 10 minutes do not make a new Supabase key appear sooner. See [JSON Web Tokens](https://supabase.com/docs/guides/auth/jwts) and [signing keys](https://supabase.com/docs/guides/auth/signing-keys).

An empty `audience` list means "do not check `aud`". The same for an empty `issuer`: `iss` is not required. Non-empty values are matched with `jwt.WithAudience` / `jwt.WithIssuer` and a mismatch is a hard rejection.

### Symmetric development mode

`dev_hs256_secret` exists because a local loop without an identity provider is useful. It is not a production setting, and the name is deliberate so it cannot be mistaken for "the JWT secret".

HS256 is a symmetric algorithm: the key that verifies a token is the same key that can **issue** one. A process that holds this secret can mint a bearer token for any `sub`, including users of someone else's project. Supabase calls HS256 "not recommended for production" and "strongly discourage[s]" verifying tokens with the legacy JWT secret; new projects sign asymmetrically by default as of 1 October 2025 ([JWT signing keys](https://supabase.com/blog/jwt-signing-keys)).

When this field is non-empty the process logs a warning at startup. The secret itself is never logged. `HS256` is not in the default `allowed_algs`; the operator must list it explicitly for the secret to have any effect.

## sync

| Field | Type | Default | Purpose |
|---|---|---|---|
| `max_envelopes_per_push` | integer | `1` | Maximum envelopes per push request. |

Round 1 fixes this value at `1` on purpose. The handler, tests, and operator docs all assume a single envelope so conflict resolution and sequence allocation stay easy to reason about. Batching several envelopes in one HTTP request is round 2; the configuration key exists now so the limit is not hard-coded, but raising it above `1` in round 1 would violate the wire contract in `protocol/SPEC.md` §3.1.
| `pull_limit_default` | integer | `100` | Default page size for pull when `limit` is omitted. |
| `pull_limit_max` | integer | `500` | Hard cap for pull `limit`. Values above this cap are truncated to it and are not rejected. |
| `live_poll_timeout` | duration | `55s` | Long-poll wait when `live=poll`. |
| `live_heartbeat` | duration | `15s` | SSE comment interval to keep connections alive. |

## admin

| Field | Type | Default | Purpose |
|---|---|---|---|
| `bind` | string | `127.0.0.1:8081` | Listen address for the operations panel (step 07). |
| `token` | string | empty | Shared secret required to access `/admin` when not on loopback. |

## Admin bind safety

The server refuses to start when `admin.bind` listens on a non-loopback address (for example `0.0.0.0:8081`) while `admin.token` is empty. Without this check, the operations page could be exposed on the network by a configuration mistake long before step 07 adds the listener. Fix by binding to `127.0.0.1` or setting a non-empty `admin.token`.

## Reverse proxies and buffering

Nginx buffers proxy responses by default. For `live=sse` that means events sit in the proxy until the buffer fills or the connection closes, then arrive as one chunk — the live feed is no longer live. `proxy_read_timeout` must also exceed `sync.live_poll_timeout` (55s), otherwise Nginx closes a quiet long-poll before the server answers.

This fragment is canonical for the repository. Step 09 compose files must include these directives, not a paraphrase:

```nginx
# Canonical Nginx fragment for ulsync live pull (SSE and long-poll).
# Step 09 compose files must include these directives, not a paraphrase.
location /v1/ {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header Connection "";
    proxy_set_header Authorization $http_authorization;
    proxy_buffering off;
    proxy_cache off;
    gzip off;
    # Exceeds sync.live_poll_timeout (55s) and the 15s SSE heartbeat.
    proxy_read_timeout 120s;
}
```

Caddy streams `text/event-stream` without extra directives. A minimal reverse proxy is enough:

```caddy
reverse_proxy localhost:8080
```
