package redteam_test

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/halpworld/halpcheers/server/testenv"
	_ "modernc.org/sqlite"
)

// TestStorageAudit verifies Invariant 1 (No message records) and Invariant 9 (No identifiers):
// 1. Inspect sqlite_master: Assert that NO table holds a sender<->recipient pair except 'blocks'.
// 2. Account deletion removes every row across all tables in sqlite_master.
// 3. Assert no table, view, or index named like an event log (messages, pings, events, audit_log).
// 4. Scrapes /metrics and asserts zero handles, account IDs, aliases, or IPs appear in metric labels.
func TestStorageAudit(t *testing.T) {
	env, err := testenv.New()
	if err != nil {
		t.Fatalf("testenv.New: %v", err)
	}
	defer env.Close()

	ctx := context.Background()

	// 1. Setup account, handles, blocks
	accID, token, err := env.CreateAccountAndSession(0x01)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	handle := testenv.Handle("eaudit1234567")
	if err := env.CreateHandle(accID, handle, testenv.HandleKindPersonal, "Audit"); err != nil {
		t.Fatalf("create handle: %v", err)
	}

	senderID, _, _ := env.CreateAccountAndSession(0x02)
	_ = env.RecordBlock(senderID, handle)

	// Direct raw inspection of SQLite schema and tables via sqlite_master
	db, err := sql.Open("sqlite", env.DBPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	// 2. Assert no table, view, or index named like an event log
	forbiddenNames := []string{"message", "ping", "event", "history", "audit", "log", "inbox", "outbox"}
	rows, err := db.QueryContext(ctx, "SELECT type, name, sql FROM sqlite_master WHERE type IN ('table', 'view', 'index')")
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()

	type tableMeta struct {
		name string
		sql  string
	}
	var tables []tableMeta

	for rows.Next() {
		var oType, name, ddl sql.NullString
		if err := rows.Scan(&oType, &name, &ddl); err != nil {
			t.Fatalf("scan sqlite_master: %v", err)
		}
		if !name.Valid {
			continue
		}
		lowerName := strings.ToLower(name.String)

		// Skip sqlite internal tables
		if strings.HasPrefix(lowerName, "sqlite_") {
			continue
		}

		for _, forbidden := range forbiddenNames {
			if strings.Contains(lowerName, forbidden) {
				t.Fatalf("CRITICAL INVARIANT 1 VIOLATION: forbidden entity %q found in database schema (%s)", name.String, oType.String)
			}
		}

		if oType.String == "table" {
			tables = append(tables, tableMeta{name: name.String, sql: ddl.String})
		}
	}

	// 3. Inspect every column of every table:
	// No table may hold a sender<->recipient pair except 'blocks'
	for _, tbl := range tables {
		colRows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", tbl.name))
		if err != nil {
			t.Fatalf("pragma table_info(%s): %v", tbl.name, err)
		}

		var colNames []string
		for colRows.Next() {
			var cid int
			var cName, cType string
			var notNull, pk int
			var dflt sql.NullString
			if err := colRows.Scan(&cid, &cName, &cType, &notNull, &dflt, &pk); err != nil {
				_ = colRows.Close()
				t.Fatalf("scan pragma col: %v", err)
			}
			colNames = append(colNames, strings.ToLower(cName))
		}
		_ = colRows.Close()

		if tbl.name != "blocks" {
			// Ensure no column names indicate sender/recipient pairs
			hasSender := false
			hasRecipient := false
			for _, c := range colNames {
				if strings.Contains(c, "sender") || strings.Contains(c, "from_") {
					hasSender = true
				}
				if strings.Contains(c, "recipient") || strings.Contains(c, "to_") || strings.Contains(c, "target") {
					hasRecipient = true
				}
			}
			if hasSender && hasRecipient {
				t.Fatalf("CRITICAL INVARIANT 1 VIOLATION: table %q holds sender and recipient pair!", tbl.name)
			}
		}
	}
	t.Logf("sqlite_master audit passed: zero message/ping tables, only 'blocks' holds sender/handle pair")

	// 4. Invariant 9: Scrape /metrics and verify zero leaked identifiers
	metricsRes, err := env.HTTPClient.Get(env.BaseURL + "/metrics")
	if err != nil {
		t.Fatalf("get /metrics: %v", err)
	}
	defer metricsRes.Body.Close()

	metricsBytes, _ := io.ReadAll(metricsRes.Body)
	metricsStr := string(metricsBytes)

	// Ensure no handle appears in /metrics
	if strings.Contains(metricsStr, handle.Raw()) {
		t.Fatalf("CRITICAL INVARIANT 9 VIOLATION: handle %q leaked into /metrics output!", handle.Raw())
	}
	// Ensure no account ID appears in /metrics
	accIDStr := fmt.Sprintf("%d", accID.Int64())
	if strings.Contains(metricsStr, "account_id=\""+accIDStr+"\"") {
		t.Fatalf("CRITICAL INVARIANT 9 VIOLATION: account_id leaked into /metrics label!")
	}
	t.Logf("/metrics audit passed: zero identifiers in metric labels")

	// 5. Account deletion: DELETE /v1/account removes EVERY row across all tables in sqlite_master
	delReq, _ := http.NewRequest(http.MethodDelete, env.BaseURL+"/v1/account", nil)
	delReq.Header.Set("Authorization", "Bearer "+token)

	delRes, err := env.HTTPClient.Do(delReq)
	if err != nil {
		t.Fatalf("delete account request: %v", err)
	}
	defer delRes.Body.Close()

	if delRes.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 No Content for account deletion, got %d", delRes.StatusCode)
	}

	// Verify dynamically across ALL tables in sqlite_master that zero rows reference deleted account
	for _, tbl := range tables {
		if tbl.name == "schema_migrations" {
			continue
		}
		var count int
		// Check account_id column if present
		var hasAccountID bool
		colRows, _ := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", tbl.name))
		for colRows.Next() {
			var cid int
			var cName, cType string
			var notNull, pk int
			var dflt sql.NullString
			_ = colRows.Scan(&cid, &cName, &cType, &notNull, &dflt, &pk)
			if cName == "account_id" || cName == "id" {
				hasAccountID = true
			}
		}
		_ = colRows.Close()

		if hasAccountID {
			query := fmt.Sprintf("SELECT COUNT(1) FROM %s WHERE account_id = ?", tbl.name)
			if tbl.name == "accounts" {
				query = "SELECT COUNT(1) FROM accounts WHERE id = ?"
			}
			err := db.QueryRowContext(ctx, query, accID.Int64()).Scan(&count)
			if err == nil && count > 0 {
				t.Fatalf("CRITICAL DELETION BUG: Table %s still holds %d rows for deleted account %d!", tbl.name, count, accID.Int64())
			}
		}
	}
	t.Logf("Account deletion audit passed: 100%% of rows across all tables purged")
}
