package store

import (
	"context"
	"testing"
)

func TestWinsAgreesWithUpsert(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		stored   Ranks
		incoming Ranks
		wantWins bool
	}{
		{
			name:     "greater_time",
			stored:   Ranks{LastEditedAtMS: 1000, Revision: 1, SourceID: "device-a"},
			incoming: Ranks{LastEditedAtMS: 2000, Revision: 1, SourceID: "device-a"},
			wantWins: true,
		},
		{
			name:     "lesser_time",
			stored:   Ranks{LastEditedAtMS: 2000, Revision: 1, SourceID: "device-a"},
			incoming: Ranks{LastEditedAtMS: 1000, Revision: 1, SourceID: "device-a"},
			wantWins: false,
		},
		{
			name:     "equal_time_greater_revision",
			stored:   Ranks{LastEditedAtMS: 2000, Revision: 1, SourceID: "device-a"},
			incoming: Ranks{LastEditedAtMS: 2000, Revision: 2, SourceID: "device-a"},
			wantWins: true,
		},
		{
			name:     "equal_time_revision_greater_source_id",
			stored:   Ranks{LastEditedAtMS: 2000, Revision: 1, SourceID: "device-a"},
			incoming: Ranks{LastEditedAtMS: 2000, Revision: 1, SourceID: "device-b"},
			wantWins: true,
		},
		{
			name:     "equal_time_revision_lesser_source_id",
			stored:   Ranks{LastEditedAtMS: 2000, Revision: 1, SourceID: "device-b"},
			incoming: Ranks{LastEditedAtMS: 2000, Revision: 1, SourceID: "device-a"},
			wantWins: false,
		},
		{
			name:     "full_tie",
			stored:   Ranks{LastEditedAtMS: 2000, Revision: 1, SourceID: "device-a"},
			incoming: Ranks{LastEditedAtMS: 2000, Revision: 1, SourceID: "device-a"},
			wantWins: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			s := openTestStore(t)
			keyID := "conflict-key-" + tc.name

			storedEnv := envelopeWithRanks(keyID, tc.stored)
			if _, err := s.Upsert(ctx, "user-a", storedEnv); err != nil {
				t.Fatalf("stored Upsert() error = %v", err)
			}

			gotWins := Wins(tc.incoming, tc.stored)
			if gotWins != tc.wantWins {
				t.Fatalf("Wins() = %v, want %v", gotWins, tc.wantWins)
			}

			incomingEnv := envelopeWithRanks(keyID, tc.incoming)
			gotApplied, err := s.Upsert(ctx, "user-a", incomingEnv)
			if err != nil {
				t.Fatalf("incoming Upsert() error = %v", err)
			}
			if gotApplied != gotWins {
				t.Fatalf("Upsert() applied = %v, Wins() = %v, want equal", gotApplied, gotWins)
			}
		})
	}
}

// envelopeWithRanks builds a minimal Envelope for conflict tests. Only the three
// ranks and the key fields matter; payload bytes are opaque placeholders.
func envelopeWithRanks(id string, ranks Ranks) Envelope {
	return Envelope{
		ID:              id,
		Part:            "full",
		EntityType:      "counter_operation",
		CreatedAtMS:     ranks.LastEditedAtMS,
		LastEditedAtMS:  ranks.LastEditedAtMS,
		Revision:        ranks.Revision,
		SourceID:        ranks.SourceID,
		Flags:           0,
		SchemaVersion:   1,
		PayloadEncoding: "json",
		Payload:         []byte(`{}`),
	}
}
