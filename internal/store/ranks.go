package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// storedRanksSQL selects only the three conflict ranks for one envelope row.
// payload and server_seq are intentionally absent: divergence check compares
// metadata, not contents or cursor position (SPEC §3.4).
const storedRanksSQL = `
SELECT last_edited_at_ms, revision, source_id
FROM envelopes
WHERE user_id = ? AND id = ? AND part = ?
`

// Key identifies one stored row inside a user's store: (id, part). Envelope
// identity on the wire is the same pair (SPEC §1.1); entity_type is not part
// of it and cannot be used to match rows.
type Key struct {
	// ID is the record identifier shared across devices for one user.
	ID string
	// Part names which slice of the record this row holds.
	Part string
}

// StoredRanks reports the conflict ranks of each requested key for one user.
//
// Keys absent from the store are absent from the map: the caller distinguishes
// «missing» from «stale» by presence, not by a sentinel value. Reads go
// through the read pool, so a long request cannot block the single writer.
//
// A single prepared statement is executed once per key inside one read
// transaction. Primary-key lookups are microseconds each; the batch is capped
// at 500 items (SPEC §3.4). Building one IN clause for 500 keys would approach
// SQLite's parameter limit and require chunking without changing the result.
func (s *Store) StoredRanks(ctx context.Context, userID string, keys []Key) (map[Key]Ranks, error) {
	if len(keys) == 0 {
		return map[Key]Ranks{}, nil
	}

	tx, err := s.readDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin stored ranks transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, storedRanksSQL)
	if err != nil {
		return nil, fmt.Errorf("prepare stored ranks query: %w", err)
	}
	defer stmt.Close()

	out := make(map[Key]Ranks, len(keys))
	for _, key := range keys {
		var ranks Ranks
		err := stmt.QueryRowContext(ctx, userID, key.ID, key.Part).Scan(
			&ranks.LastEditedAtMS,
			&ranks.Revision,
			&ranks.SourceID,
		)
		switch {
		case err == nil:
			out[key] = ranks
		case errors.Is(err, sql.ErrNoRows):
			// Absent keys stay out of the map so callers can report «missing».
		default:
			return nil, fmt.Errorf("select ranks for %q/%q user %q: %w", key.ID, key.Part, userID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit stored ranks transaction: %w", err)
	}
	return out, nil
}
