package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const (
	// upsertAllocateSeqSQL reserves the next per-user server_seq inside the same
	// transaction as the envelope write. A separate round trip would allow the
	// number to be issued without a matching row under concurrent pushes.
	upsertAllocateSeqSQL = `
		INSERT INTO users (user_id, next_seq) VALUES (?, 1)
		ON CONFLICT (user_id) DO UPDATE SET next_seq = next_seq + 1
		RETURNING next_seq
	`

	// upsertEnvelopeSQL is fixed by the round 1 plan (§13.5). created_at_ms is
	// intentionally absent from the UPDATE SET list: creation time never changes.
	// The WHERE clause implements three tie-breakers so the outcome does not
	// depend on arrival order. RETURNING answers whether the row was written:
	// no row means the incoming envelope did not win (applied = false).
	upsertEnvelopeSQL = `
		INSERT INTO envelopes (user_id, id, part, entity_type, created_at_ms,
		                       last_edited_at_ms, revision, source_id, flags,
		                       schema_version, server_seq, payload_encoding, payload)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (user_id, id, part) DO UPDATE SET
		  entity_type       = excluded.entity_type,
		  last_edited_at_ms = excluded.last_edited_at_ms,
		  revision          = excluded.revision,
		  source_id         = excluded.source_id,
		  flags             = excluded.flags,
		  schema_version    = excluded.schema_version,
		  server_seq        = excluded.server_seq,
		  payload_encoding  = excluded.payload_encoding,
		  payload           = excluded.payload
		WHERE excluded.last_edited_at_ms > envelopes.last_edited_at_ms
		   OR (excluded.last_edited_at_ms = envelopes.last_edited_at_ms
		       AND (excluded.revision > envelopes.revision
		            OR (excluded.revision = envelopes.revision
		                AND excluded.source_id > envelopes.source_id)))
		RETURNING server_seq
	`
)

// Upsert stores one envelope for the given user under last-write-wins and
// reports whether the incoming envelope won. A false result is not an error:
// it means the stored envelope is not older than the incoming one.
//
// Sequence allocation and the envelope write run in one transaction on the
// writer pool. The per-user counter is incremented before the upsert decides;
// a rejected envelope consumes a sequence number that may never appear on any
// row (gaps are legal per the sync protocol).
func (s *Store) Upsert(ctx context.Context, userID string, e Envelope) (applied bool, err error) {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin upsert transaction: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var seq int64
	if err = tx.QueryRowContext(ctx, upsertAllocateSeqSQL, userID).Scan(&seq); err != nil {
		return false, fmt.Errorf("allocate sequence for user %q: %w", userID, err)
	}

	// applied is derived solely from RETURNING: a separate SELECT "what was stored"
	// would add a round trip and race under concurrent writers for the same user.
	var returnedSeq int64
	err = tx.QueryRowContext(ctx, upsertEnvelopeSQL,
		userID,
		e.ID,
		e.Part,
		e.EntityType,
		e.CreatedAtMS,
		e.LastEditedAtMS,
		e.Revision,
		e.SourceID,
		e.Flags,
		e.SchemaVersion,
		seq,
		e.PayloadEncoding,
		e.Payload,
	).Scan(&returnedSeq)
	switch {
	case err == nil:
		applied = true
	case errors.Is(err, sql.ErrNoRows):
		// The upsert WHERE rejected the incoming row; seq was still consumed above.
		applied = false
		err = nil
	default:
		return false, fmt.Errorf("upsert envelope %q/%q for user %q: %w", e.ID, e.Part, userID, err)
	}

	if err = tx.Commit(); err != nil {
		return false, fmt.Errorf("commit upsert transaction: %w", err)
	}
	return applied, nil
}
