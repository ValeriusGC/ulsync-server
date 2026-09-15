package store_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/ValeriusGC/ulsync-server/internal/config"
	"github.com/ValeriusGC/ulsync-server/internal/store"

	_ "modernc.org/sqlite"
)

func TestOpenCreatesDatabaseAndAppliesMigrationsOnce(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ulsync.db")
	cfg := config.Storage{Driver: "sqlite", Path: dbPath}

	ctx := context.Background()
	first, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer first.Close()

	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("database file missing: %v", err)
	}

	assertMigrationCount(t, dbPath, 2)

	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	second, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer second.Close()

	assertMigrationCount(t, dbPath, 2)
}

func TestStatsOnEmptyDatabase(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := config.Storage{Driver: "sqlite", Path: filepath.Join(dir, "ulsync.db")}

	ctx := context.Background()
	s, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.Users != 0 || stats.Envelopes != 0 {
		t.Fatalf("Stats() counts = (%d, %d), want (0, 0)", stats.Users, stats.Envelopes)
	}
	if stats.Path == "" {
		t.Fatal("Stats().Path is empty")
	}
	if stats.SizeBytes <= 0 {
		t.Fatalf("Stats().SizeBytes = %d, want > 0", stats.SizeBytes)
	}
}

func TestStrictSchemaRejectsStringInIntegerColumn(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ulsync.db")
	cfg := config.Storage{Driver: "sqlite", Path: dbPath}

	ctx := context.Background()
	s, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	_, err = db.ExecContext(ctx, `
		INSERT INTO envelopes (
			user_id, id, part, entity_type, created_at_ms, last_edited_at_ms,
			revision, source_id, flags, schema_version, server_seq,
			payload_encoding, payload
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "user-1", "env-1", "full", "note", 1, "not-an-integer", 1, "device-a", 0, 1, 1, "none", []byte{})
	if err == nil {
		t.Fatal("expected STRICT schema to reject string in last_edited_at_ms")
	}
}

func TestAllocateSeqIncrementsForSameUser(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := config.Storage{Driver: "sqlite", Path: filepath.Join(dir, "ulsync.db")}

	ctx := context.Background()
	s, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

	for want := int64(1); want <= 3; want++ {
		seq, err := s.AllocateSeq(ctx, "user-a")
		if err != nil {
			t.Fatalf("AllocateSeq() #%d error = %v", want, err)
		}
		if seq != want {
			t.Fatalf("AllocateSeq() #%d = %d, want %d", want, seq, want)
		}
	}
}

func TestAllocateSeqConcurrentForOneUser(t *testing.T) {
	const workers = 100

	dir := t.TempDir()
	cfg := config.Storage{Driver: "sqlite", Path: filepath.Join(dir, "ulsync.db")}

	ctx := context.Background()
	s, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

	results := make(chan int64, workers)
	for range workers {
		go func() {
			seq, err := s.AllocateSeq(ctx, "user-a")
			if err != nil {
				t.Errorf("AllocateSeq() error = %v", err)
				results <- 0
				return
			}
			results <- seq
		}()
	}

	seen := make(map[int64]struct{}, workers)
	for range workers {
		seq := <-results
		if seq == 0 {
			continue
		}
		if _, exists := seen[seq]; exists {
			t.Fatalf("duplicate sequence number %d", seq)
		}
		seen[seq] = struct{}{}
	}

	if len(seen) != workers {
		t.Fatalf("got %d unique values, want %d", len(seen), workers)
	}
	for want := int64(1); want <= workers; want++ {
		if _, ok := seen[want]; !ok {
			t.Fatalf("missing sequence number %d", want)
		}
	}
}

func TestAllocateSeqIndependentPerUser(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := config.Storage{Driver: "sqlite", Path: filepath.Join(dir, "ulsync.db")}

	ctx := context.Background()
	s, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

	seqA, err := s.AllocateSeq(ctx, "user-a")
	if err != nil {
		t.Fatalf("AllocateSeq(user-a) error = %v", err)
	}
	seqB, err := s.AllocateSeq(ctx, "user-b")
	if err != nil {
		t.Fatalf("AllocateSeq(user-b) error = %v", err)
	}
	if seqA != 1 || seqB != 1 {
		t.Fatalf("first sequence numbers = (%d, %d), want (1, 1)", seqA, seqB)
	}
}

// assertMigrationCount checks schema_migrations row count and version in a db file.
func assertMigrationCount(t *testing.T, dbPath string, want int) {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if count != want {
		t.Fatalf("schema_migrations count = %d, want %d", count, want)
	}

	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("read schema_migrations version: %v", err)
	}
	if version != 2 {
		t.Fatalf("schema_migrations version = %d, want 2", version)
	}
}
