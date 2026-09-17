package store

import (
	"context"
	"fmt"
	"testing"
)

func TestUpsertMany100DistinctKeys(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)

	envelopes := make([]Envelope, 100)
	for i := range envelopes {
		envelopes[i] = Envelope{
			ID:              fmt.Sprintf("id-%03d", i),
			Part:            "full",
			EntityType:      "counter_operation",
			CreatedAtMS:     1000,
			LastEditedAtMS:  1000,
			Revision:        1,
			SourceID:        "device-a",
			Flags:           0,
			SchemaVersion:   1,
			PayloadEncoding: "json",
			Payload:         []byte(`{"n":1}`),
		}
	}

	applied, err := s.UpsertMany(ctx, "alice", envelopes)
	if err != nil {
		t.Fatalf("UpsertMany() error = %v", err)
	}
	if len(applied) != 100 {
		t.Fatalf("applied len = %d, want 100", len(applied))
	}
	for i, ok := range applied {
		if !ok {
			t.Fatalf("applied[%d] = false, want true", i)
		}
	}

	var count int
	if err := s.readDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM envelopes WHERE user_id = ?
	`, "alice").Scan(&count); err != nil {
		t.Fatalf("count envelopes: %v", err)
	}
	if count != 100 {
		t.Fatalf("envelope count = %d, want 100", count)
	}

	var maxSeq int64
	if err := s.readDB.QueryRowContext(ctx, `
		SELECT MAX(server_seq) FROM envelopes WHERE user_id = ?
	`, "alice").Scan(&maxSeq); err != nil {
		t.Fatalf("max server_seq: %v", err)
	}
	if maxSeq != 100 {
		t.Fatalf("max(server_seq) = %d, want 100", maxSeq)
	}
}

func TestUpsertManyEmptySlice(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	_, err := s.UpsertMany(context.Background(), "alice", nil)
	if err == nil {
		t.Fatal("UpsertMany(nil) expected error")
	}
}
