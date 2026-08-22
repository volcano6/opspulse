// Package storage provides SQLite database persistence and schema migrations.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/volcano6/opspulse/internal/config"
	"github.com/volcano6/opspulse/internal/storage/migrations"
	_ "modernc.org/sqlite" // Pure Go SQLite driver
)

// DB represents a managed SQLite database instance.
type DB struct {
	conn *sql.DB
	path string
	mu   sync.RWMutex
}

// Open initializes and opens a SQLite database at the specified path.
func Open(dbPath string) (*DB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create database directory %q: %w", dir, err)
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", dbPath)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database at %q: %w", dbPath, err)
	}

	// Single writer / connection pooling configuration for SQLite
	conn.SetMaxOpenConns(1)

	db := &DB{
		conn: conn,
		path: dbPath,
	}

	// Auto-run migrations on open
	if err := db.Migrate(context.Background()); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("failed to apply migrations: %w", err)
	}

	return db, nil
}

// OpenDefault opens the default SQLite database under $XDG_DATA_HOME/opspulse/opspulse.db.
func OpenDefault() (*DB, error) {
	dbPath := filepath.Join(config.DataDir(), "opspulse.db")
	return Open(dbPath)
}

// Conn returns the underlying *sql.DB connection.
func (d *DB) Conn() *sql.DB {
	return d.conn
}

// Path returns the filesystem path of the database file.
func (d *DB) Path() string {
	return d.path
}

// Close closes the underlying database connection.
func (d *DB) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn != nil {
		return d.conn.Close()
	}
	return nil
}

// Migrate applies all pending embedded SQL migrations in sequential order.
func (d *DB) Migrate(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	// 1. Ensure schema_migrations table exists
	createMigrationTableSQL := `
	CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := d.conn.ExecContext(ctx, createMigrationTableSQL); err != nil {
		return fmt.Errorf("failed to create schema_migrations table: %w", err)
	}

	// 2. Discover and sort migration files
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("failed to read embedded migrations: %w", err)
	}

	var migrationFiles []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			migrationFiles = append(migrationFiles, entry.Name())
		}
	}
	sort.Strings(migrationFiles)

	// 3. Apply each migration in a transaction
	for _, file := range migrationFiles {
		versionStr := strings.Split(file, "_")[0]
		version, err := strconv.Atoi(versionStr)
		if err != nil {
			continue // Skip non-versioned SQL files
		}

		// Check if already applied
		var exists bool
		checkQuery := "SELECT 1 FROM schema_migrations WHERE version = ?"
		err = d.conn.QueryRowContext(ctx, checkQuery, version).Scan(&exists)
		if err == nil && exists {
			continue // Already applied
		} else if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("failed to check migration version %d: %w", version, err)
		}

		content, err := migrations.FS.ReadFile(file)
		if err != nil {
			return fmt.Errorf("failed to read migration file %q: %w", file, err)
		}

		tx, err := d.conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to begin migration transaction for %q: %w", file, err)
		}

		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to execute migration %q: %w", file, err)
		}

		recordSQL := "INSERT INTO schema_migrations (version, applied_at) VALUES (?, CURRENT_TIMESTAMP)"
		if _, err := tx.ExecContext(ctx, recordSQL, version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to record migration version %d: %w", version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("failed to commit migration transaction for %q: %w", file, err)
		}
	}

	return nil
}
