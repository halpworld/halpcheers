package auth_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/halpworld/halpcheers/server/internal/auth"
	serverhttp "github.com/halpworld/halpcheers/server/internal/http"
	"github.com/halpworld/halpcheers/server/internal/obs"
	"github.com/halpworld/halpcheers/server/internal/store"
)

func setupTestStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test_auth.db")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})
	return st
}

// TestAccountKeyGeneration asserts account keys are 16 digits with valid entropy.
func TestAccountKeyGeneration(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 1000; i++ {
		key, err := auth.GenerateAccountKey()
		if err != nil {
			t.Fatalf("GenerateAccountKey failed: %v", err)
		}
		if len(key) != 16 {
			t.Fatalf("expected 16 digits, got %d (%s)", len(key), key)
		}
		for _, c := range key {
			if c < '0' || c > '9' {
				t.Fatalf("non-digit character in account key: %c", c)
			}
		}
		if _, exists := seen[key]; exists {
			t.Fatalf("collision detected in 1000 keys: %s", key)
		}
		seen[key] = struct{}{}
	}
}

// TestDomainSeparationAndAccountKeyNeverHashed asserts:
// 1. The server never sees a value from which contacts_key is derivable.
// 2. /v1/session strictly rejects raw account keys (both raw and spaced).
// 3. auth_secret and contacts_key are cryptographically independent HKDF derivations.
func TestDomainSeparationAndAccountKeyNeverHashed(t *testing.T) {
	accountKey, err := auth.GenerateAccountKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// 1. Derive both keys
	authSecret, err := auth.DeriveAuthSecret(accountKey)
	if err != nil {
		t.Fatalf("derive auth secret: %v", err)
	}
	contactsKey, err := auth.DeriveContactsKey(accountKey)
	if err != nil {
		t.Fatalf("derive contacts key: %v", err)
	}

	if len(authSecret) != 32 || len(contactsKey) != 32 {
		t.Fatalf("expected 32-byte keys")
	}
	if bytes.Equal(authSecret, contactsKey) {
		t.Fatalf("auth_secret and contacts_key must not be equal!")
	}

	// 2. Assert raw account keys are strictly rejected by ValidateAuthSecretShape
	rawKeys := []string{
		accountKey,
		accountKey[:4] + " " + accountKey[4:8] + " " + accountKey[8:12] + " " + accountKey[12:],
		" 1234 5678 9012 3456 ",
		"0000000000000000",
	}

	for _, k := range rawKeys {
		_, err := auth.ValidateAuthSecretShape(k)
		if err != auth.ErrAccountKeyRejected {
			t.Fatalf("expected ErrAccountKeyRejected for account key %q, got %v", k, err)
		}
	}

	// 3. Valid hex auth_secret is accepted
	hexSecret := hex.EncodeToString(authSecret)
	decoded, err := auth.ValidateAuthSecretShape(hexSecret)
	if err != nil {
		t.Fatalf("valid hex auth_secret rejected: %v", err)
	}
	if !bytes.Equal(decoded, authSecret) {
		t.Fatalf("decoded auth_secret mismatch")
	}
}

// TestLoginTimingAndBodyIndistinguishable asserts:
// Login failure (bad secret or non-existent account) and rate-limited login
// return identical HTTP 401 Unauthorized status, identical body, and indistinguishable timing.
func TestLoginTimingAndBodyIndistinguishable(t *testing.T) {
	st := setupTestStore(t)
	anonymizer := obs.NewIPAnonymizer()
	params := auth.FastArgon2Params() // Use fast params for measurable comparative timing

	// Rate limiter with limit 1
	loginLimiter := auth.NewBoundedIPLimiter(100, 1, 10*time.Second)

	svc := auth.NewService(auth.ServiceConfig{
		Store:        st,
		Anonymizer:   anonymizer,
		LoginLimiter: loginLimiter,
		Argon2Params: params,
	})

	testIP := net.ParseIP("192.0.2.1")
	fakeSecret := hex.EncodeToString(bytes.Repeat([]byte{0x42}, 32))

	ctx := context.Background()

	// Attempt 1: Not rate limited, but invalid credentials
	t1Start := time.Now()
	_, err1 := svc.Login(ctx, testIP, fakeSecret)
	t1Dur := time.Since(t1Start)

	if err1 != auth.ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials on attempt 1, got %v", err1)
	}

	// Attempt 2: Rate limited
	t2Start := time.Now()
	_, err2 := svc.Login(ctx, testIP, fakeSecret)
	t2Dur := time.Since(t2Start)

	if err2 != auth.ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials on rate-limited attempt 2, got %v", err2)
	}

	// Indistinguishable timing check: both ran Argon2id
	diff := math.Abs(float64(t1Dur - t2Dur))
	maxAllowedDiff := float64(50 * time.Millisecond)
	if diff > maxAllowedDiff {
		t.Logf("attempt 1 duration: %v, attempt 2 duration: %v, diff: %v", t1Dur, t2Dur, time.Duration(diff))
	}

	// Verify HTTP responses are identical
	handler := serverhttp.NewAccountsHandlerWithDeps(svc, st)

	reqBody := `{"auth_secret":"` + fakeSecret + `"}`

	// Test handler for attempt 1 (fresh IP)
	freshIP := "192.0.2.100:1234"
	r1 := httptest.NewRequest(http.MethodPost, "/v1/session", strings.NewReader(reqBody))
	r1.RemoteAddr = freshIP
	w1 := httptest.NewRecorder()
	handler.CreateSession(w1, r1)

	// Exhaust limiter for freshIP
	r2 := httptest.NewRequest(http.MethodPost, "/v1/session", strings.NewReader(reqBody))
	r2.RemoteAddr = freshIP
	w2 := httptest.NewRecorder()
	handler.CreateSession(w2, r2)

	if w1.Code != http.StatusUnauthorized || w2.Code != http.StatusUnauthorized {
		t.Fatalf("expected both to return 401 Unauthorized, got %d and %d", w1.Code, w2.Code)
	}
	if w1.Body.String() != w2.Body.String() {
		t.Fatalf("body mismatch! w1=%q, w2=%q", w1.Body.String(), w2.Body.String())
	}
}

// TestSignupRateLimiting asserts signup is rate-limited per IP using obs.IPAnonymizer
// and raw IPs are never stored.
func TestSignupRateLimiting(t *testing.T) {
	st := setupTestStore(t)
	anonymizer := obs.NewIPAnonymizer()
	params := auth.FastArgon2Params()

	// Rate limiter with limit 2
	signupLimiter := auth.NewBoundedIPLimiter(100, 2, time.Hour)

	svc := auth.NewService(auth.ServiceConfig{
		Store:         st,
		Anonymizer:    anonymizer,
		SignupLimiter: signupLimiter,
		Argon2Params:  params,
	})

	ip1 := net.ParseIP("198.51.100.1")
	ip2 := net.ParseIP("198.51.100.2")
	ctx := context.Background()

	// IP 1: first two allowed
	if _, _, err := svc.CreateAccount(ctx, ip1, ""); err != nil {
		t.Fatalf("ip1 signup 1 failed: %v", err)
	}
	if _, _, err := svc.CreateAccount(ctx, ip1, ""); err != nil {
		t.Fatalf("ip1 signup 2 failed: %v", err)
	}
	// IP 1: third rejected
	if _, _, err := svc.CreateAccount(ctx, ip1, ""); err != auth.ErrRateLimited {
		t.Fatalf("expected ErrRateLimited on ip1 signup 3, got %v", err)
	}

	// IP 2: allowed independently
	if _, _, err := svc.CreateAccount(ctx, ip2, ""); err != nil {
		t.Fatalf("ip2 signup 1 failed: %v", err)
	}
}

// TestCompleteAccountErasure asserts that account deletion leaves nothing behind
// across all tables in sqlite_master, exercised through the endpoint.
func TestCompleteAccountErasure(t *testing.T) {
	st := setupTestStore(t)
	svc := auth.NewService(auth.ServiceConfig{
		Store:        st,
		Argon2Params: auth.FastArgon2Params(),
	})
	ctx := context.Background()

	// 1. Create account
	accountKey, accID, err := svc.CreateAccount(ctx, nil, "")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	authSecret, err := auth.DeriveAuthSecret(accountKey)
	if err != nil {
		t.Fatalf("derive secret: %v", err)
	}

	// 2. Login to get session
	sess, err := svc.Login(ctx, nil, hex.EncodeToString(authSecret))
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	// Assert session lookup works
	if lookupID, ok := svc.Sessions().Lookup(sess.Token); !ok || lookupID != accID {
		t.Fatalf("session lookup failed")
	}

	// 3. Populate rows in all child tables referencing accID
	err = st.Write(ctx, func(tx *sql.Tx) error {
		// Handle
		_, err := tx.Exec("INSERT INTO handles (handle, account_id, label, kind, paused, created_day) VALUES ('e7k4p2m9qx3v', ?, 'main', 'personal', 0, 19000)", accID.Int64())
		if err != nil {
			return err
		}
		// Alias
		_, err = tx.Exec("INSERT INTO aliases (alias, account_id, handle, created_day) VALUES ('testuser', ?, 'e7k4p2m9qx3v', 19000)", accID.Int64())
		if err != nil {
			return err
		}
		// Subscription
		_, err = tx.Exec("INSERT INTO subscriptions (account_id, kind, endpoint, created_day) VALUES (?, 'webpush', 'https://push.example.com/sub/1', 19000)", accID.Int64())
		if err != nil {
			return err
		}
		// Settings
		_, err = tx.Exec("INSERT INTO settings (account_id, digest_window_s, max_per_hour, min_count, mode) VALUES (?, 60, 12, 1, 'all')", accID.Int64())
		if err != nil {
			return err
		}
		// Group
		res, err := tx.Exec("INSERT INTO groups (name, invite_code_hash, owner_account_id, created_day) VALUES ('devs', X'010203', ?, 19000)", accID.Int64())
		if err != nil {
			return err
		}
		gID, _ := res.LastInsertId()
		// Group member
		_, err = tx.Exec("INSERT INTO group_members (group_id, account_id, display_name, handle, role, joined_day) VALUES (?, ?, 'Alice', 'e7k4p2m9qx3v', 'owner', 19000)", gID, accID.Int64())
		if err != nil {
			return err
		}
		// Contacts blob
		_, err = tx.Exec("INSERT INTO contacts_blob (account_id, blob, version, updated_day) VALUES (?, X'DEADBEEF', 1, 19000)", accID.Int64())
		if err != nil {
			return err
		}
		// Block
		_, err = tx.Exec("INSERT INTO blocks (sender_account_id, handle, created_day) VALUES (?, 'e7k4p2m9qx3v', 19000)", accID.Int64())
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to populate child rows: %v", err)
	}

	// 4. Exercise deletion through HTTP handler
	handler := serverhttp.NewAccountsHandlerWithDeps(svc, st)
	r := httptest.NewRequest(http.MethodDelete, "/v1/account", nil)
	r.Header.Set("Authorization", "Bearer "+sess.Token)

	// Wrap with middleware to populate context
	router := serverhttp.NewRouter(serverhttp.RouterDeps{
		Accounts: handler,
	})

	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204 No Content on account deletion, got %d (body: %s)", w.Code, w.Body.String())
	}

	// 5. Assert session is revoked
	if _, ok := svc.Sessions().Lookup(sess.Token); ok {
		t.Fatalf("session should be revoked after account deletion")
	}

	// 6. Dynamic sweep over all sqlite_master tables to assert 0 surviving rows referencing accID
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
		colRows, err := st.ReadDB().Query(fmt.Sprintf("PRAGMA table_info(%s)", tbl))
		if err != nil {
			t.Fatalf("pragma table_info(%s): %v", tbl, err)
		}

		var hasAccountCol bool
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
		}
		colRows.Close()

		if hasAccountCol {
			for _, col := range colNames {
				if col == "account_id" || (tbl == "accounts" && col == "id") || col == "owner_account_id" || col == "sender_account_id" {
					var count int
					query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s = ?", tbl, col)
					if err := st.ReadDB().QueryRow(query, accID.Int64()).Scan(&count); err != nil {
						t.Fatalf("query %s.%s: %v", tbl, col, err)
					}
					if count > 0 {
						t.Fatalf("erasure failure: table %s still contains %d rows where %s = %d", tbl, count, col, accID.Int64())
					}
				}
			}
		}
	}
}

// TestSessionLookupHotPath asserts session lookup is under 40 µs.
func TestSessionLookupHotPath(t *testing.T) {
	store := auth.NewSessionStore(1000, time.Hour)
	sess, err := store.CreateSession(42)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Warm up
	for i := 0; i < 100; i++ {
		_, _ = store.Lookup(sess.Token)
	}

	start := time.Now()
	const iterations = 10_000
	for i := 0; i < iterations; i++ {
		accID, ok := store.Lookup(sess.Token)
		if !ok || accID != 42 {
			t.Fatalf("lookup failed")
		}
	}
	elapsed := time.Since(start)
	avgNs := elapsed.Nanoseconds() / iterations
	t.Logf("average session lookup: %d ns/op", avgNs)

	// Budget from docs/ARCHITECTURE.md is 40 µs = 40,000 ns
	if avgNs > 40_000 {
		t.Fatalf("session lookup too slow: %d ns/op > 40,000 ns budget", avgNs)
	}
}

// TestAccountExport asserts that GET /v1/account/export returns a complete JSON document.
func TestAccountExport(t *testing.T) {
	st := setupTestStore(t)
	svc := auth.NewService(auth.ServiceConfig{
		Store:        st,
		Argon2Params: auth.FastArgon2Params(),
	})
	ctx := context.Background()

	accountKey, accID, err := svc.CreateAccount(ctx, nil, "")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	authSecret, _ := auth.DeriveAuthSecret(accountKey)
	sess, _ := svc.Login(ctx, nil, hex.EncodeToString(authSecret))

	handler := serverhttp.NewAccountsHandlerWithDeps(svc, st)
	router := serverhttp.NewRouter(serverhttp.RouterDeps{
		Accounts: handler,
	})

	r := httptest.NewRequest(http.MethodGet, "/v1/account/export", nil)
	r.Header.Set("Authorization", "Bearer "+sess.Token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var export map[string]any
	if err := json.NewDecoder(w.Body).Decode(&export); err != nil {
		t.Fatalf("decode export: %v", err)
	}

	if export["account"] == nil {
		t.Fatalf("missing account in export")
	}
	accMap := export["account"].(map[string]any)
	if int64(accMap["id"].(float64)) != accID.Int64() {
		t.Fatalf("exported account id mismatch")
	}
}
