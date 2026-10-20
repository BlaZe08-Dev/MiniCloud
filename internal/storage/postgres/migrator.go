package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var defaultMigrationFS embed.FS

// DefaultAdvisoryLockID is a constant 64-bit integer derived from "MiniCld" (0x4d696e69436c64)
// used for serializing migration transactions via pg_advisory_xact_lock.
const DefaultAdvisoryLockID int64 = 0x4d696e69436c64

// ErrChecksumMismatch is returned when an already-applied migration has a different
// SHA-256 checksum than the embedded migration file.
var ErrChecksumMismatch = errors.New("migration checksum mismatch: file has been modified after being applied")

// Migration represents a single SQL migration file.
type Migration struct {
	Version  int
	Name     string
	Content  string
	Checksum string
}

// AppliedMigration records a migration execution stored in schema_migrations.
type AppliedMigration struct {
	Version   int
	Name      string
	Checksum  string
	AppliedAt time.Time
}

// Migrator manages the discovery, validation, and forward-only execution of database migrations.
type Migrator struct {
	fs     fs.FS
	lockID int64
}

// MigratorOption configures a Migrator instance.
type MigratorOption func(*Migrator)

// WithFS overrides the filesystem source for migrations (used in tests).
func WithFS(migrationFS fs.FS) MigratorOption {
	return func(m *Migrator) {
		m.fs = migrationFS
	}
}

// WithLockID sets a custom advisory lock ID.
func WithLockID(lockID int64) MigratorOption {
	return func(m *Migrator) {
		m.lockID = lockID
	}
}

// NewMigrator creates a new forward-only migration runner.
func NewMigrator(opts ...MigratorOption) *Migrator {
	m := &Migrator{
		fs:     defaultMigrationFS,
		lockID: DefaultAdvisoryLockID,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// LoadMigrations discovers, parses, and sorts all .sql files in the migrations directory.
func (m *Migrator) LoadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(m.fs, "migrations")
	if err != nil {
		return nil, fmt.Errorf("failed to read migrations directory: %w", err)
	}

	var migrations []Migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		name := entry.Name()
		parts := strings.SplitN(name, "_", 2)
		if len(parts) < 2 {
			return nil, fmt.Errorf("invalid migration filename %q: expected format 000001_name.sql", name)
		}

		version, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("invalid migration version in %q: %w", name, err)
		}

		contentBytes, err := fs.ReadFile(m.fs, filepath.Join("migrations", name))
		if err != nil {
			return nil, fmt.Errorf("failed to read migration file %q: %w", name, err)
		}

		hash := sha256.Sum256(contentBytes)
		checksum := hex.EncodeToString(hash[:])

		migrations = append(migrations, Migration{
			Version:  version,
			Name:     name,
			Content:  string(contentBytes),
			Checksum: checksum,
		})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	return migrations, nil
}

// Migrate executes all pending migrations within a single serialized transaction.
//
// Invariants enforced (ADR-0010 & owner requirements):
// 1. Takes pg_advisory_xact_lock FIRST inside the transaction before any catalog access.
// 2. Creates schema_migrations IF NOT EXISTS inside the locked transaction.
// 3. Verifies SHA-256 checksums of all previously applied migrations (fails loudly on tampering).
// 4. Applies unapplied migrations in ascending version order.
// 5. Unqualified SQL statements execute in the caller's active search_path.
// 6. Returns the count of newly applied migrations (0 if already up to date).
func (m *Migrator) Migrate(ctx context.Context, conn *pgx.Conn) (int, error) {
	migrations, err := m.LoadMigrations()
	if err != nil {
		return 0, err
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to begin migration transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Step 1: Take transaction-level advisory lock first to eliminate catalog race conditions
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1);", m.lockID); err != nil {
		return 0, fmt.Errorf("failed to acquire migration advisory lock: %w", err)
	}

	// Step 2: Ensure schema_migrations table exists (unqualified table operates in search_path)
	createTableSQL := `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			sha256_checksum TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
	`
	if _, err := tx.Exec(ctx, createTableSQL); err != nil {
		return 0, fmt.Errorf("failed to ensure schema_migrations table: %w", err)
	}

	// Step 3: Read previously applied migrations
	rows, err := tx.Query(ctx, "SELECT version, name, sha256_checksum, applied_at FROM schema_migrations ORDER BY version ASC;")
	if err != nil {
		return 0, fmt.Errorf("failed to query applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]AppliedMigration)
	for rows.Next() {
		var a AppliedMigration
		if err := rows.Scan(&a.Version, &a.Name, &a.Checksum, &a.AppliedAt); err != nil {
			return 0, fmt.Errorf("failed to scan applied migration row: %w", err)
		}
		applied[a.Version] = a
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("error reading applied migrations: %w", err)
	}

	// Step 4: Verify checksums of already-applied migrations
	for _, migration := range migrations {
		if prev, ok := applied[migration.Version]; ok {
			if prev.Checksum != migration.Checksum {
				return 0, fmt.Errorf("%w: migration %s (version %d) stored checksum %s does not match current checksum %s",
					ErrChecksumMismatch, migration.Name, migration.Version, prev.Checksum, migration.Checksum)
			}
		}
	}

	// Step 5: Execute unapplied migrations in ascending version order
	appliedCount := 0
	for _, migration := range migrations {
		if _, ok := applied[migration.Version]; ok {
			continue
		}

		if _, err := tx.Exec(ctx, migration.Content); err != nil {
			return appliedCount, fmt.Errorf("failed to execute migration %s: %w", migration.Name, err)
		}

		insertRecordSQL := `
			INSERT INTO schema_migrations (version, name, sha256_checksum, applied_at)
			VALUES ($1, $2, $3, CURRENT_TIMESTAMP);
		`
		if _, err := tx.Exec(ctx, insertRecordSQL, migration.Version, migration.Name, migration.Checksum); err != nil {
			return appliedCount, fmt.Errorf("failed to record migration %s in schema_migrations: %w", migration.Name, err)
		}
		appliedCount++
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("failed to commit migration transaction: %w", err)
	}

	return appliedCount, nil
}
