package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// migrationFS holds SQL files compiled into the binary. A separate migrations
// folder next to the executable is intentionally not supported: the server
// ships as a single deployable file.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// migration is one versioned SQL file discovered from migrationFS.
type migration struct {
	version int    // numeric prefix from filename (NNNN_description.sql)
	name    string // filename for error messages
	sql     string // full file contents
}

// loadMigrations reads embedded *.sql files and sorts them by numeric version
// parsed from the filename prefix (NNNN_description.sql).
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations directory: %w", err)
	}

	var migrations []migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := parseMigrationVersion(entry.Name())
		if err != nil {
			return nil, err
		}
		data, err := migrationFS.ReadFile(path.Join("migrations", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		migrations = append(migrations, migration{
			version: version,
			name:    entry.Name(),
			sql:     string(data),
		})
	}

	sort.Slice(migrations, func(i, j int) bool {
		if migrations[i].version == migrations[j].version {
			return migrations[i].name < migrations[j].name
		}
		return migrations[i].version < migrations[j].version
	})
	return migrations, nil
}

// parseMigrationVersion extracts the leading integer from a migration filename.
func parseMigrationVersion(name string) (int, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, fmt.Errorf("migration filename %q must match NNNN_description.sql", name)
	}
	version, err := strconv.Atoi(prefix)
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("migration filename %q must start with a positive version number", name)
	}
	return version, nil
}

// appliedMigrationVersion returns the highest version recorded in
// schema_migrations, or 0 when the table does not exist yet (fresh database).
func appliedMigrationVersion(ctx context.Context, db *sql.DB) (int, error) {
	var maxVersion sql.NullInt64
	err := db.QueryRowContext(ctx, `
		SELECT MAX(version) FROM schema_migrations
	`).Scan(&maxVersion)
	if err != nil {
		if isMissingTableError(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("query applied migration version: %w", err)
	}
	if !maxVersion.Valid {
		return 0, nil
	}
	return int(maxVersion.Int64), nil
}

// isMissingTableError detects a fresh database before the first migration
// creates schema_migrations.
func isMissingTableError(err error) bool {
	return strings.Contains(err.Error(), "no such table")
}

// applyMigrations runs every embedded migration with a version greater than
// the latest applied version, in ascending version order.
func applyMigrations(ctx context.Context, db *sql.DB) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	if len(migrations) == 0 {
		return fmt.Errorf("no migrations found")
	}

	current, err := appliedMigrationVersion(ctx, db)
	if err != nil {
		return err
	}

	for _, migration := range migrations {
		if migration.version <= current {
			continue
		}
		if err := applyMigration(ctx, db, migration); err != nil {
			return fmt.Errorf("apply migration %q: %w", migration.name, err)
		}
		current = migration.version
	}
	return nil
}

// applyMigration executes one migration file inside a single transaction on the
// writer pool. DDL and the schema_migrations insert commit together; a failed
// migration leaves no half-applied schema.
func applyMigration(ctx context.Context, db *sql.DB, migration migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	// database/sql executes one statement per Exec; migration files may contain several.
	for _, statement := range splitSQLStatements(migration.sql) {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("execute statement: %w", err)
		}
	}

	appliedAt := time.Now().UTC().UnixMilli()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO schema_migrations (version, applied_at_ms) VALUES (?, ?)
	`, migration.version, appliedAt); err != nil {
		return fmt.Errorf("record migration version: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

// splitSQLStatements splits a migration file on semicolons. Our migrations do
// not embed semicolons inside string literals.
func splitSQLStatements(sqlText string) []string {
	parts := strings.Split(sqlText, ";")
	statements := make([]string, 0, len(parts))
	for _, part := range parts {
		statement := strings.TrimSpace(part)
		if statement == "" {
			continue
		}
		statements = append(statements, statement)
	}
	return statements
}
