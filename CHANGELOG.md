# Changelog

**Created:** 2026-08-26 12:35:42 +0500  
**Updated:** 2026-08-27 20:16:00 +0500  
**Version:** 6  
**Document type:** changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- Go server skeleton: single-file YAML configuration, `GET /health`, graceful shutdown, CI on Go 1.26, and `ulsync-protocol` git submodule.
- SQLite storage with embedded migrations, separate writer and reader pools, and per-user sequence allocation.
- JWT bearer verification against a JWKS (`RS256` and `ES256`), optional `aud`/`iss`, static `auth.jwks_file` for hosts without outbound internet, and `GET /v1/whoami`.
- `POST /v1/sync/push`: single-envelope last-write-wins upsert with transactional sequence allocation; push responses omit `server_seq`.
- `GET /v1/sync/pull` returns envelopes after a cursor for the authenticated user; `next_cursor` comes from returned rows; per-user isolation is enforced in the query.
- Live pull: `live=poll` holds the request until a change or `sync.live_poll_timeout`; `live=sse` streams `envelope` and `cursor` events with a `: ping` heartbeat.
