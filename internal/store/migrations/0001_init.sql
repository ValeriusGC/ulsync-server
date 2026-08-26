CREATE TABLE IF NOT EXISTS schema_migrations (
  version       INTEGER NOT NULL PRIMARY KEY,
  applied_at_ms INTEGER NOT NULL
) STRICT;

CREATE TABLE users (
  user_id  TEXT    NOT NULL PRIMARY KEY,
  next_seq INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE envelopes (
  user_id           TEXT    NOT NULL,
  id                TEXT    NOT NULL,
  part              TEXT    NOT NULL,
  entity_type       TEXT    NOT NULL,
  created_at_ms     INTEGER NOT NULL,
  last_edited_at_ms INTEGER NOT NULL,
  revision          INTEGER NOT NULL,
  source_id         TEXT    NOT NULL,
  flags             INTEGER NOT NULL,
  schema_version    INTEGER NOT NULL,
  server_seq        INTEGER NOT NULL,
  payload_encoding  TEXT    NOT NULL,
  payload           BLOB    NOT NULL,
  PRIMARY KEY (user_id, id, part)
) STRICT;

CREATE INDEX envelopes_by_cursor ON envelopes (user_id, server_seq);
