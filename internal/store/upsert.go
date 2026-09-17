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

// upsertOneInTx allocates the next server_seq and runs upsertEnvelopeSQL for one
// envelope inside an already-open write transaction. The caller owns BEGIN/COMMIT.
func upsertOneInTx(ctx context.Context, tx *sql.Tx, userID string, e Envelope) (applied bool, err error) {
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
	return applied, nil
}

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

	applied, err = upsertOneInTx(ctx, tx, userID, e)
	if err != nil {
		return false, err
	}

	if err = tx.Commit(); err != nil {
		return false, fmt.Errorf("commit upsert transaction: %w", err)
	}
	return applied, nil
}

// UpsertMany applies every envelope under last-write-wins inside one
// transaction. Either every comparison ran, or none did.
//
// The HTTP handler must validate wire fields and reject duplicate (id, part)
// pairs before calling this method. An empty slice is a caller error: the
// store does not begin a transaction for it.
//
// Live waiters are not notified here. The handler wakes the registry once
// after Commit, and only when at least one row was stored. Notifying from
// inside the loop would publish a prefix of the batch.
func (s *Store) UpsertMany(ctx context.Context, userID string, envelopes []Envelope) ([]bool, error) {
	if len(envelopes) == 0 {
		return nil, fmt.Errorf("upsert many: empty envelope slice")
	}

	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin upsert transaction: %w", err)
	}

	applied := make([]bool, len(envelopes))
	for i, e := range envelopes {
		var one bool
		one, err = upsertOneInTx(ctx, tx, userID, e)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		applied[i] = one
	}

	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit upsert transaction: %w", err)
	}
	return applied, nil
}
