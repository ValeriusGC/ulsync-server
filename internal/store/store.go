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

// Store owns the SQLite database: schema migration, a single writer and a
// pool of readers.
type Store struct {
	path    string
	writeDB *sql.DB
	readDB  *sql.DB
}

// Stats holds cheap-to-serialize counters for the operations page.
type Stats struct {
	Users     int64
	Envelopes int64
	Path      string
	SizeBytes int64
}

// Open creates the database directory, opens separate writer and reader pools,
// and applies pending schema migrations.
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

// Close releases database connections.
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

// Stats reports counters for the operations page. Each call runs COUNT queries
// against the database; callers should poll on a timer (for example every few
// seconds), not on every HTTP request.
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

func writeDSN(dbPath string) string {
	return fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_txlock=immediate",
		dbPath,
	)
}

func readDSN(dbPath string) string {
	return fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)",
		dbPath,
	)
}
