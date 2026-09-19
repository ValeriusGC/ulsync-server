# Configuration reference

**Created:** 2026-08-26 12:35:42 +0500  
**Updated:** 2026-09-19 20:06:19 +0300  
**Version:** 11  
**Document type:** reference

The server reads a single YAML file (see `config.example.yaml`). After start, every runtime path, bind address, and timeout comes from that file; there is no environment-variable configuration. When the file named by `-config` is missing, exactly one of `-jwks-url` or `-shared-secret` writes it from embedded defaults and that one flag, then `Load` runs as usual. An existing file is never overwritten: first-run flags are not a rewrite API.

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

## origin

| Field | Type | Default | Purpose |
|---|---|---|---|
| `origin` | string | empty | Application contour for an **authored** store. Sent by clients as the `Ulsync-Origin` header. |

Empty or absent means an **open** store: the first well-formed `Ulsync-Origin` on `GET /v1/sync/hello` is recorded in `server_meta` and becomes the store origin. Mail endpoints never imprint.

When `origin` is set before the first client, the process writes it into `server_meta` at startup. If the database already holds a different origin, the process refuses to listen: that is a configuration error, not a silent overwrite.

The value is not a URL, not a user id, and not `source_id`. Allowed characters: `A–Z`, `a–z`, `0–9`, `.`, `_`, `/`, `-`. Length 1–256. An invalid value in YAML prevents startup.

The operations panel shows both `origin` from this file (not redacted) and the current value from `server_meta`. The panel does not edit either field.

## auth

| Field | Type | Default | Purpose |
|---|---|---|---|
| `jwks_url` | string | Supabase JWKS URL placeholder (see below) | URL of the JSON Web Key Set used to verify bearer tokens. |
| `jwks_file` | string | empty | Path to a static JWKS file. When non-empty, the server reads this file and does not fetch `jwks_url`. |
| `jwks_cache_ttl` | duration | `10m` | How long a successfully loaded key set stays cached before the next refresh. |
| `allowed_algs` | string list | `ES256`, `RS256` | Algorithms the parser will accept. Anything else, including `none` and `HS256`, is rejected unless listed here. |
| `audience` | string list | empty | JWT `aud` values to require. An empty list means audience is not checked. |
| `issuer` | string | empty | JWT `iss` value to require. Empty means issuer is not checked. |
| `dev_hs256_secret` | string | empty | Development-only HMAC secret. Empty in every non-dev deployment. |

The server never issues tokens. It fetches **public** keys (or reads them from `jwks_file`) and extracts `sub` as `user_id`. A private PEM is not a configuration field and is not accepted on the command line: this process verifies tokens, it does not mint them.

`applyDefaults` fills an empty `jwks_url` with the Supabase placeholder **only when** both `dev_hs256_secret` and `jwks_file` are empty. A secret-only or file-only document must not inherit a foreign JWKS URL, or the process would fetch someone else's keys. Existing YAML that has neither secret nor file still receives the placeholder, as before.

Supabase's edge caches the JWKS response for 10 minutes. A newly published signing key may therefore be invisible to this process for up to that long even if `jwks_cache_ttl` is shorter; values under 10 minutes do not make a new Supabase key appear sooner. See [JSON Web Tokens](https://supabase.com/docs/guides/auth/jwts) and [signing keys](https://supabase.com/docs/guides/auth/signing-keys).

An empty `audience` list means "do not check `aud`". The same for an empty `issuer`: `iss` is not required. Non-empty values are matched with `jwt.WithAudience` / `jwt.WithIssuer` and a mismatch is a hard rejection.

### Symmetric development mode

`dev_hs256_secret` exists because a local loop without an identity provider is useful. It is not a production setting, and the name is deliberate so it cannot be mistaken for "the JWT secret".

HS256 is a symmetric algorithm: the key that verifies a token is the same key that can **issue** one. A process that holds this secret can mint a bearer token for any `sub`, including users of someone else's project. Supabase calls HS256 "not recommended for production" and "strongly discourage[s]" verifying tokens with the legacy JWT secret; new projects sign asymmetrically by default as of 1 October 2025 ([JWT signing keys](https://supabase.com/blog/jwt-signing-keys)).

When this field is non-empty the process logs a warning at startup. The secret itself is never logged. `HS256` is not in the default `allowed_algs`; the operator must list it explicitly for the secret to have any effect. First-run `-shared-secret` writes the operator's string and adds `HS256` to `allowed_algs`. The process never invents `local-dev-only`.

## First-run flags

These flags apply only when the `-config` path does not exist. `-version` and `-healthcheck` neither read nor seed the file.

| Flag | Role |
|---|---|
| `-jwks-url` | Seed `auth.jwks_url` from the operator's IdP. `dev_hs256_secret` stays empty. The URL in the file equals the flag, not the Supabase placeholder. |
| `-shared-secret` | Seed `auth.dev_hs256_secret` from the operator's string (personal cloud, not "turn auth off"). After `Load`, `jwks_url` is empty. `allowed_algs` contains `HS256`. |

Pass exactly one. Neither flag, or both at once, exits 1 and the message names `-jwks-url` and `-shared-secret`; the file is not created. That is XOR on purpose: merging would pick an authority silently (URL wins, or a leftover secret forges tokens). There is no `-jwks-file` here: a blank host has no JWKS file yet; that path is a later YAML edit.

Any seed writes `admin.bind` `127.0.0.1:8081` with an empty `admin.token` (a seeded Ubuntu process is not the Compose container that binds `0.0.0.0:8081`). `storage.driver` is `sqlite`. `storage.path` is the absolute `{directory of -config}/data/ulsync.db` so a later cwd change does not move the database. `server.bind` stays `0.0.0.0:8080`. `origin` is not seeded. The write is atomic (temporary file in the same directory, then `Rename`) with mode `0600`.

## sync

| Field | Type | Default | Purpose |
|---|---|---|---|
| `max_envelopes_per_push` | integer | `500` | Maximum envelopes per push request. |

The default matches the protocol ceiling in `protocol/SPEC.md` §3.1 and the maximum `limit` on pull and `items` on diff. An explicit `1` remains valid for operators who want to cap batches at a single envelope. Values greater than `500` make `Load` fail and the process will not listen, for the same reason an invalid `origin` is rejected at startup. Zero is replaced by the default (`500`).
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

The server refuses to start when `admin.bind` listens on a non-loopback address (for example `0.0.0.0:8081`) while `admin.token` is empty. Without this check, the operations page could be exposed on the network by a configuration mistake. Fix by binding to `127.0.0.1` or setting a non-empty `admin.token`. **Do not expose the panel on a public interface without a non-empty `admin.token`.** The process enforces this at startup; there is no runtime override.

In Docker, published ports reach the container's `eth0`, not container loopback. Binding `admin` to `127.0.0.1:8081` inside the container makes the panel unreachable through `ports:` mapping. Use `admin.bind: "0.0.0.0:8081"` with a non-empty `admin.token`, and keep the host publish as `127.0.0.1:8081:8081` in `compose.yaml` (see README).

The operations listener runs in the same process as sync (see `admin` bind above). It serves `GET /admin` (embedded HTML), `GET /admin/events` (SSE snapshot once per second), and `POST /admin/token-check` (user JWT validation with rejection reasons). The panel is read-only: it never writes configuration or database state.

To reach the panel from another host while keeping loopback on the server, forward the port: `ssh -L 8081:127.0.0.1:8081 user@host`, then open `http://127.0.0.1:8081/admin` locally.

When `admin.token` is non-empty, all three panel routes require `Authorization: Bearer <admin.token>`. When it is empty and the bind is loopback, no header is required.

Browsers cannot send an `Authorization` header on `EventSource`. With a non-empty `admin.token`, use curl with `-H Authorization` for `/admin/events`; the in-browser stream will not authenticate. With an empty token on loopback (including through an SSH tunnel to `127.0.0.1`), the browser stream works without extra headers.

## Reverse proxies and buffering

Nginx buffers proxy responses by default. For `live=sse` that means events sit in the proxy until the buffer fills or the connection closes, then arrive as one chunk — the live feed is no longer live. `proxy_read_timeout` must also exceed `sync.live_poll_timeout` (55s), otherwise Nginx closes a quiet long-poll before the server answers.

This fragment belongs on the host Nginx in front of the published sync port. Round 1 `compose.yaml` does not run a reverse-proxy sidecar.

```nginx
# Canonical Nginx fragment for ulsync live pull (SSE and long-poll).
# Host Nginx in front of the published sync port — not inside the app container.
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
