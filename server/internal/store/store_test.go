package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/halpworld/halpcheers/server/internal/store"
)

// TestFreshBootAndIdempotentSecondBoot asserts that migrations run cleanly on a fresh database
// and a second boot is an idempotent no-op.
func TestFreshBootAndIdempotentSecondBoot(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "halp.db")

	// First boot
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("first boot failed: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	// Second boot
	st2, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("second boot failed: %v", err)
	}
	defer st2.Close()

	// Verify schema_version exists and has 1 migration
	var count int
	err = st2.ReadDB().QueryRow("SELECT COUNT(*) FROM schema_version").Scan(&count)
	if err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 applied migration, got %d", count)
	}
}

// TestForeignKeysPragmaVerifiedOn asserts that foreign_keys=ON is active on both connections.
func TestForeignKeysPragmaVerifiedOn(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "halp.db")

	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	var fkEnabled int
	err = st.ReadDB().QueryRow("PRAGMA foreign_keys").Scan(&fkEnabled)
	if err != nil {
		t.Fatalf("query foreign_keys pragma: %v", err)
	}
	if fkEnabled != 1 {
		t.Fatalf("expected foreign_keys PRAGMA to be 1 (ON), got %d", fkEnabled)
	}

	err = st.Write(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow("PRAGMA foreign_keys").Scan(&fkEnabled)
	})
	if err != nil {
		t.Fatalf("query write tx foreign_keys pragma: %v", err)
	}
	if fkEnabled != 1 {
		t.Fatalf("expected write tx foreign_keys PRAGMA to be 1 (ON), got %d", fkEnabled)
	}
}

// TestAccountDeletionWipesEverything asserts that deleting an account completely
// cascades and wipes every trace of it from ALL tables in sqlite_master.
func TestAccountDeletionWipesEverything(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "halp.db")

	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	// Create account A
	accIDA, err := st.CreateAccount(ctx, []byte("hash_secret_a"), "eu-1")
	if err != nil {
		t.Fatalf("create account A: %v", err)
	}

	// Create account B (as a sender / counterparty)
	accIDB, err := st.CreateAccount(ctx, []byte("hash_secret_b"), "eu-1")
	if err != nil {
		t.Fatalf("create account B: %v", err)
	}

	// Populate data across all schema tables related to account A
	err = st.Write(ctx, func(tx *sql.Tx) error {
		// 1. Group owned by A
		res, err := tx.Exec("INSERT INTO groups (name, invite_code_hash, owner_account_id, created_day) VALUES ('devs', 'hash', ?, 19000)", accIDA.Int64())
		if err != nil {
			return fmt.Errorf("insert group: %w", err)
		}
		grpID, _ := res.LastInsertId()

		// 2. Handle for A
		_, err = tx.Exec("INSERT INTO handles (handle, account_id, label, kind, created_day) VALUES ('e7k4p2m9qx3v', ?, 'github', 'personal', 19000)", accIDA.Int64())
		if err != nil {
			return fmt.Errorf("insert handle: %w", err)
		}

		// 3. Alias for A
		_, err = tx.Exec("INSERT INTO aliases (alias, account_id, handle, created_day) VALUES ('@kenth', ?, 'e7k4p2m9qx3v', 19000)", accIDA.Int64())
		if err != nil {
			return fmt.Errorf("insert alias: %w", err)
		}

		// 4. Subscriptions for A
		_, err = tx.Exec("INSERT INTO subscriptions (account_id, kind, endpoint, created_day) VALUES (?, 'webpush', 'https://push.example.com/sub/1', 19000)", accIDA.Int64())
		if err != nil {
			return fmt.Errorf("insert subscription: %w", err)
		}

		// 5. Settings for A
		_, err = tx.Exec("INSERT INTO settings (account_id, digest_window_s, max_per_hour, min_count, mode) VALUES (?, 60, 12, 1, 'all')", accIDA.Int64())
		if err != nil {
			return fmt.Errorf("insert settings: %w", err)
		}

		// 6. Group member for A
		_, err = tx.Exec("INSERT INTO group_members (group_id, account_id, display_name, handle, role, joined_day) VALUES (?, ?, 'Kenth', 'e7k4p2m9qx3v', 'owner', 19000)", grpID, accIDA.Int64())
		if err != nil {
			return fmt.Errorf("insert group_member: %w", err)
		}

		// 7. Contacts blob for A
		_, err = tx.Exec("INSERT INTO contacts_blob (account_id, blob, version, updated_day) VALUES (?, X'01020304', 1, 19000)", accIDA.Int64())
		if err != nil {
			return fmt.Errorf("insert contacts_blob: %w", err)
		}

		// 8. Block created by A targeting some other handle
		_, err = tx.Exec("INSERT INTO handles (handle, account_id, label, kind, created_day) VALUES ('e999999999999', ?, 'other', 'personal', 19000)", accIDB.Int64())
		if err != nil {
			return fmt.Errorf("insert other handle: %w", err)
		}
		_, err = tx.Exec("INSERT INTO blocks (sender_account_id, handle, created_day) VALUES (?, 'e999999999999', 19000)", accIDA.Int64())
		if err != nil {
			return fmt.Errorf("insert block: %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("populate tables: %v", err)
	}

	// Delete Account A
	if err := st.DeleteAccount(ctx, accIDA); err != nil {
		t.Fatalf("delete account A: %v", err)
	}

	// Dynamic loop over all tables in sqlite_master to assert ZERO surviving rows referencing Account A
	rows, err := st.ReadDB().Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name != 'schema_version'")
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		tables = append(tables, name)
	}

	for _, tbl := range tables {
		// Check table columns to see which identify accounts or handles
		colRows, err := st.ReadDB().Query(fmt.Sprintf("PRAGMA table_info(%s)", tbl))
		if err != nil {
			t.Fatalf("pragma table_info(%s): %v", tbl, err)
		}

		var hasAccountCol, hasHandleCol bool
		var colNames []string
		for colRows.Next() {
			var cid, notnull, pk int
			var cname, ctype string
			var dflt *string
			_ = colRows.Scan(&cid, &cname, &ctype, &notnull, &dflt, &pk)
			colNames = append(colNames, cname)
			if cname == "account_id" || cname == "id" || cname == "owner_account_id" || cname == "sender_account_id" {
				hasAccountCol = true
			}
			if cname == "handle" {
				hasHandleCol = true
			}
		}
		colRows.Close()

		if hasAccountCol {
			for _, col := range colNames {
				if col == "account_id" || (tbl == "accounts" && col == "id") || col == "owner_account_id" || col == "sender_account_id" {
					var count int
					query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s = ?", tbl, col)
					err := st.ReadDB().QueryRow(query, accIDA.Int64()).Scan(&count)
					if err != nil {
						t.Fatalf("query %s.%s: %v", tbl, col, err)
					}
					if count > 0 {
						t.Fatalf("erasure failure: table %s still has %d rows where %s = %d", tbl, count, col, accIDA.Int64())
					}
				}
			}
		}

		if hasHandleCol {
			var count int
			err := st.ReadDB().QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE handle = 'e7k4p2m9qx3v'", tbl)).Scan(&count)
			if err != nil {
				t.Fatalf("query %s for handle: %v", tbl, err)
			}
			if count > 0 {
				t.Fatalf("erasure failure: table %s still has %d rows with deleted handle 'e7k4p2m9qx3v'", tbl, count)
			}
		}
	}
}

// TestConcurrentWritesSingleWriter asserts that concurrent writes pass cleanly under -race
// without lock collisions or SQLITE_BUSY.
func TestConcurrentWritesSingleWriter(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "halp.db")

	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	var wg sync.WaitGroup
	workers := 16
	opsPerWorker := 20

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < opsPerWorker; j++ {
				secret := fmt.Sprintf("secret_%d_%d", workerID, j)
				_, err := st.CreateAccount(ctx, []byte(secret), "eu-1")
				if err != nil {
					t.Errorf("worker %d write %d failed: %v", workerID, j, err)
				}
			}
		}(i)
	}

	wg.Wait()

	var totalAccounts int
	err = st.ReadDB().QueryRow("SELECT COUNT(*) FROM accounts").Scan(&totalAccounts)
	if err != nil {
		t.Fatalf("count accounts: %v", err)
	}
	expected := workers * opsPerWorker
	if totalAccounts != expected {
		t.Fatalf("expected %d accounts, got %d", expected, totalAccounts)
	}
}

// TestNoSecondPrecisionTimestamps asserts that no column in any schema table
// uses second-precision timestamps or timestamp column types.
func TestNoSecondPrecisionTimestamps(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "halp.db")

	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	rows, err := st.ReadDB().Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name != 'schema_version'")
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		tables = append(tables, name)
	}

	for _, tbl := range tables {
		colRows, err := st.ReadDB().Query(fmt.Sprintf("PRAGMA table_info(%s)", tbl))
		if err != nil {
			t.Fatalf("pragma table_info(%s): %v", tbl, err)
		}
		for colRows.Next() {
			var cid, notnull, pk int
			var cname, ctype string
			var dflt *string
			_ = colRows.Scan(&cid, &cname, &ctype, &notnull, &dflt, &pk)

			lowerCol := strings.ToLower(cname)
			lowerType := strings.ToLower(ctype)

			// Check for forbidden time column names or types
			if strings.Contains(lowerCol, "time") || strings.Contains(lowerCol, "timestamp") {
				t.Errorf("table %s contains forbidden timestamp column name: %s", tbl, cname)
			}
			if strings.Contains(lowerType, "time") || strings.Contains(lowerType, "timestamp") || strings.Contains(lowerType, "datetime") {
				t.Errorf("table %s column %s has forbidden timestamp type: %s", tbl, cname, ctype)
			}
		}
		colRows.Close()
	}
}
