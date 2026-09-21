package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/halpworld/halpcheers/server/internal/core"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// ErrChecksumMismatch is returned when an existing migration file has a different checksum than recorded.
var ErrChecksumMismatch = fmt.Errorf("migration checksum mismatch")

// Migrate applies all pending migrations in order from migrations/*.sql.
// It tracks applied migrations in the schema_version table and refuses to start on checksum mismatch.
func Migrate(ctx context.Context, db *sql.DB) error {
	// Create schema_version table if it doesn't exist
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_version (
			version     INTEGER PRIMARY KEY,
			name        TEXT NOT NULL,
			checksum    TEXT NOT NULL,
			applied_day INTEGER NOT NULL
		);
	`)
	if err != nil {
		return fmt.Errorf("create schema_version table: %w", err)
	}

	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var filenames []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			filenames = append(filenames, e.Name())
		}
	}
	sort.Strings(filenames)

	for _, name := range filenames {
		parts := strings.SplitN(name, "_", 2)
		if len(parts) < 2 {
			continue
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil {
			return fmt.Errorf("invalid migration version in filename %s: %w", name, err)
		}

		content, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration file %s: %w", name, err)
		}

		h := sha256.Sum256(content)
		currentChecksum := hex.EncodeToString(h[:])

		var recordedChecksum string
		err = db.QueryRowContext(ctx, "SELECT checksum FROM schema_version WHERE version = ?", version).Scan(&recordedChecksum)
		if err == nil {
			// Migration already applied - verify checksum
			if recordedChecksum != currentChecksum {
				return fmt.Errorf("%w for version %04d (%s): recorded %s, current %s",
					ErrChecksumMismatch, version, name, recordedChecksum, currentChecksum)
			}
			continue
		} else if err != sql.ErrNoRows {
			return fmt.Errorf("check applied migration %d: %w", version, err)
		}

		// Apply new migration inside a transaction
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration tx: %w", err)
		}

		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("execute migration %s: %w", name, err)
		}

		today := core.Today().Int()
		_, err = tx.ExecContext(ctx,
			"INSERT INTO schema_version (version, name, checksum, applied_day) VALUES (?, ?, ?, ?)",
			version, name, currentChecksum, today)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}

	return nil
}
