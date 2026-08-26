// Package store provides embedded SQLite persistence for the sync server.
//
// A Store opens two connection pools against one database file: a single
// writer serializes mutations inside the process, and multiple readers serve
// concurrent lookups. Schema migrations ship inside the binary and run
// automatically on [Open].
//
// Example:
//
//	db, err := store.Open(ctx, cfg.Storage)
//	if err != nil {
//	    return err
//	}
//	defer db.Close()
//
//	seq, err := db.AllocateSeq(ctx, userID)
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ValeriusGC/ulsync-server/internal/config"

	_ "modernc.org/sqlite"
)

// Store owns the SQLite database file, its schema version, and two connection
// pools. All writes go through writeDB; reads may use readDB in parallel.
//
// Store methods are safe for concurrent use from multiple goroutines.
type Store struct {
	path    string  // configured SQLite file path for Stats and logging
	writeDB *sql.DB // single connection; all mutations serialize here
	readDB  *sql.DB // concurrent readers for Stats and future pull
}

// Stats is a snapshot of database counters and on-disk size. It is intended
// for periodic polling (for example the operations panel in step 07), not for
// per-request use on hot paths.
type Stats struct {
	// Users is the number of rows in the users table (one row per user_id that
	// has ever received a server sequence number).
	Users int64
	// Envelopes is the number of stored envelope rows across all users.
	Envelopes int64
	// Path is the configured SQLite file path (the main .db file, not -wal/-shm).
	Path string
	// SizeBytes is the size of Path on disk at the time Stats was collected.
	SizeBytes int64
}

// Open prepares the database directory, connects writer and reader pools, and
// applies any pending embedded migrations before returning.
//
// Open uses modernc.org/sqlite (pure Go, no CGO). The writer pool is capped
// at one connection so concurrent writers queue inside the process instead of
// receiving SQLITE_BUSY from the file. sql.Open does not create the file until
// the first Ping, so Open always pings both pools.
//
// The caller must call [Store.Close] when the Store is no longer needed.
func Open(ctx context.Context, cfg config.Storage) (*Store, error) {
	if cfg.Driver != "sqlite" {
		return nil, fmt.Errorf("unsupported storage driver %q", cfg.Driver)
	}

	dbPath := cfg.Path
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	writeDSN := writeDSN(dbPath)
	writeDB, err := sql.Open("sqlite", writeDSN)
	if err != nil {
		return nil, fmt.Errorf("open write database: %w", err)
	}
	writeDB.SetMaxOpenConns(1)
	writeDB.SetMaxIdleConns(1)

	readDSN := readDSN(dbPath)
	readDB, err := sql.Open("sqlite", readDSN)
	if err != nil {
		_ = writeDB.Close()
		return nil, fmt.Errorf("open read database: %w", err)
	}
	readDB.SetMaxOpenConns(runtime.NumCPU())

	if err := writeDB.PingContext(ctx); err != nil {
		_ = writeDB.Close()
		_ = readDB.Close()
		return nil, fmt.Errorf("ping write database: %w", err)
	}
	if err := readDB.PingContext(ctx); err != nil {
		_ = writeDB.Close()
		_ = readDB.Close()
		return nil, fmt.Errorf("ping read database: %w", err)
	}

	store := &Store{
		path:    dbPath,
		writeDB: writeDB,
		readDB:  readDB,
	}
	if err := applyMigrations(ctx, writeDB); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

// Close closes the writer and reader pools. It is safe to call on a nil
// receiver field-wise but should be called on a non-nil *Store returned from Open.
func (s *Store) Close() error {
	var firstErr error
	if s.writeDB != nil {
		if err := s.writeDB.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.readDB != nil {
		if err := s.readDB.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// AllocateSeq reserves the next per-user server_seq value in a single
// round trip. The user row is created on first use; there is no separate
// "create user" step.
//
// The sequence number is consumed even when a later envelope write is rejected.
// Gaps in server_seq are allowed and expected by the sync protocol.
//
// AllocateSeq must run on the writer pool. Calling it through the reader pool
// would race under concurrency.
//
// The statement is fixed by the round 1 plan (upsert with RETURNING):
//
//	INSERT INTO users (user_id, next_seq) VALUES (?, 1)
//	ON CONFLICT (user_id) DO UPDATE SET next_seq = next_seq + 1
//	RETURNING next_seq;
func (s *Store) AllocateSeq(ctx context.Context, userID string) (int64, error) {
	var seq int64
	err := s.writeDB.QueryRowContext(ctx, `
		INSERT INTO users (user_id, next_seq) VALUES (?, 1)
		ON CONFLICT (user_id) DO UPDATE SET next_seq = next_seq + 1
		RETURNING next_seq
	`, userID).Scan(&seq)
	if err != nil {
		return 0, fmt.Errorf("allocate sequence for user %q: %w", userID, err)
	}
	return seq, nil
}

// Stats returns user and envelope counts plus the database file size on disk.
//
// Each call issues two COUNT(*) queries and one os.Stat. Under load, calling
// Stats on every HTTP request would become a self-inflicted denial of service.
// Callers should cache or throttle results (the admin panel does this in step 07).
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var stats Stats
	stats.Path = s.path

	if err := s.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&stats.Users); err != nil {
		return Stats{}, fmt.Errorf("count users: %w", err)
	}
	if err := s.readDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM envelopes`).Scan(&stats.Envelopes); err != nil {
		return Stats{}, fmt.Errorf("count envelopes: %w", err)
	}

	fileInfo, err := os.Stat(s.path)
	if err != nil {
		return Stats{}, fmt.Errorf("stat database file: %w", err)
	}
	stats.SizeBytes = fileInfo.Size()
	return stats, nil
}

// writeDSN builds the writer connection string: WAL mode, immediate transaction
// lock (_txlock=immediate), and a single connection cap enforced separately via
// SetMaxOpenConns(1).
func writeDSN(dbPath string) string {
	return fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_txlock=immediate",
		dbPath,
	)
}

// readDSN builds the reader connection string. It omits _txlock because readers
// never begin write transactions.
func readDSN(dbPath string) string {
	return fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)",
		dbPath,
	)
}
