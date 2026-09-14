package store

import (
	"context"
	"testing"
)

func TestStoredRanksFoundAndMissing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)

	present := envelopeWithRanks("present-id", Ranks{
		LastEditedAtMS: 3000,
		Revision:       2,
		SourceID:       "device-a",
	})
	if _, err := s.Upsert(ctx, "user-a", present); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	keys := []Key{
		{ID: "present-id", Part: "full"},
		{ID: "absent-id", Part: "full"},
	}
	got, err := s.StoredRanks(ctx, "user-a", keys)
	if err != nil {
		t.Fatalf("StoredRanks() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("map len = %d, want 1", len(got))
	}
	ranks, ok := got[Key{ID: "present-id", Part: "full"}]
	if !ok {
		t.Fatal("present key missing from map")
	}
	if ranks.LastEditedAtMS != 3000 || ranks.Revision != 2 || ranks.SourceID != "device-a" {
		t.Fatalf("ranks = %+v, want 3000/2/device-a", ranks)
	}
	if _, ok := got[Key{ID: "absent-id", Part: "full"}]; ok {
		t.Fatal("absent key must not appear in map")
	}
}

func TestStoredRanksCrossUserIsolation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)

	env := envelopeWithRanks("shared-id", Ranks{
		LastEditedAtMS: 1000,
		Revision:       1,
		SourceID:       "device-a",
	})
	if _, err := s.Upsert(ctx, "user-a", env); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	got, err := s.StoredRanks(ctx, "user-b", []Key{{ID: "shared-id", Part: "full"}})
	if err != nil {
		t.Fatalf("StoredRanks() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("map len = %d, want 0 for other user", len(got))
	}
}

func TestStoredRanksEmptyKeys(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := openTestStore(t)

	got, err := s.StoredRanks(ctx, "user-a", nil)
	if err != nil {
		t.Fatalf("StoredRanks() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("map len = %d, want empty map without DB access", len(got))
	}
}
