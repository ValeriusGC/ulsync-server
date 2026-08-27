package store

import (
	"context"
	"fmt"
)

// sinceSQL selects one user's envelopes after a cursor. user_id in WHERE is
// isolation, not a speed hint: omitting it would leak another user's rows.
// ORDER BY is explicit because a query plan is not a sort guarantee.
const sinceSQL = `
SELECT id, part, entity_type, created_at_ms, last_edited_at_ms, revision,
       source_id, flags, schema_version, server_seq, payload_encoding, payload
FROM envelopes
WHERE user_id = ? AND server_seq > ?
ORDER BY server_seq
LIMIT ?;
`

// Since returns envelopes of one user with a sequence number greater than the
// cursor, ordered by that number. The second result is the cursor to use next.
// An empty result is not an error: the returned cursor equals the input since.
func (s *Store) Since(ctx context.Context, userID string, since int64, limit int) ([]Envelope, int64, error) {
	rows, err := s.readDB.QueryContext(ctx, sinceSQL, userID, since, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("select envelopes since %d for user %q: %w", since, userID, err)
	}
	defer rows.Close()

	out := make([]Envelope, 0, limit)
	for rows.Next() {
		var env Envelope
		if err := rows.Scan(
			&env.ID,
			&env.Part,
			&env.EntityType,
			&env.CreatedAtMS,
			&env.LastEditedAtMS,
			&env.Revision,
			&env.SourceID,
			&env.Flags,
			&env.SchemaVersion,
			&env.ServerSeq,
			&env.PayloadEncoding,
			&env.Payload,
		); err != nil {
			return nil, 0, fmt.Errorf("scan envelope since %d for user %q: %w", since, userID, err)
		}
		out = append(out, env)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate envelopes since %d for user %q: %w", since, userID, err)
	}

	next := since
	if len(out) > 0 {
		next = out[len(out)-1].ServerSeq
	}
	return out, next, nil
}
