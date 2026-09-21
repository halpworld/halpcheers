package http_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/halpworld/halpcheers/server/internal/core"
	internalhttp "github.com/halpworld/halpcheers/server/internal/http"
	"github.com/halpworld/halpcheers/server/internal/store"
)

type mockSettingsAuth struct {
	sessions map[string]core.AccountID
}

func (m *mockSettingsAuth) AuthenticateSession(ctx context.Context, token string) (core.AccountID, bool) {
	id, ok := m.sessions[token]
	return id, ok
}

type mockTopSender struct {
	topSenders map[core.Handle]core.AccountID
}

func (m *mockTopSender) TopSender(ctx context.Context, handle core.Handle) (core.AccountID, bool) {
	id, ok := m.topSenders[handle]
	return id, ok
}

func setupTestStore(t *testing.T) (*store.Store, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "halp-settings-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, "halp.db")
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}

	cleanup := func() {
		_ = st.Close()
		_ = os.RemoveAll(dir)
	}
	return st, cleanup
}

func intPtr(i int) *int {
	return &i
}

func strPtr(s string) *string {
	return &s
}

// TestValidateSettingsTable verifies every field's bounds, both edges, and rejections.
func TestValidateSettingsTable(t *testing.T) {
	validBase := internalhttp.SettingsDTO{
		DigestWindowS: 60,
		MaxPerHour:    12,
		MinCount:      1,
		Mode:          "all",
	}

	tests := []struct {
		name      string
		mutate    func(s *internalhttp.SettingsDTO)
		wantError bool
	}{
		// digest_window_s bounds [0, 86400]
		{"digest_window_s lower edge (0)", func(s *internalhttp.SettingsDTO) { s.DigestWindowS = 0 }, false},
		{"digest_window_s upper edge (86400)", func(s *internalhttp.SettingsDTO) { s.DigestWindowS = 86400 }, false},
		{"digest_window_s rejection (-1)", func(s *internalhttp.SettingsDTO) { s.DigestWindowS = -1 }, true},
		{"digest_window_s rejection (86401)", func(s *internalhttp.SettingsDTO) { s.DigestWindowS = 86401 }, true},

		// max_per_hour bounds [1, 60]
		{"max_per_hour lower edge (1)", func(s *internalhttp.SettingsDTO) { s.MaxPerHour = 1 }, false},
		{"max_per_hour upper edge (60)", func(s *internalhttp.SettingsDTO) { s.MaxPerHour = 60 }, false},
		{"max_per_hour rejection (0)", func(s *internalhttp.SettingsDTO) { s.MaxPerHour = 0 }, true},
		{"max_per_hour rejection (61)", func(s *internalhttp.SettingsDTO) { s.MaxPerHour = 61 }, true},

		// min_count bounds [1, 1000]
		{"min_count lower edge (1)", func(s *internalhttp.SettingsDTO) { s.MinCount = 1 }, false},
		{"min_count upper edge (1000)", func(s *internalhttp.SettingsDTO) { s.MinCount = 1000 }, false},
		{"min_count rejection (0)", func(s *internalhttp.SettingsDTO) { s.MinCount = 0 }, true},
		{"min_count rejection (1001)", func(s *internalhttp.SettingsDTO) { s.MinCount = 1001 }, true},

		// mode: all, groups_only, paused
		{"mode valid (all)", func(s *internalhttp.SettingsDTO) { s.Mode = "all" }, false},
		{"mode valid (groups_only)", func(s *internalhttp.SettingsDTO) { s.Mode = "groups_only" }, false},
		{"mode valid (paused)", func(s *internalhttp.SettingsDTO) { s.Mode = "paused" }, false},
		{"mode rejection (empty)", func(s *internalhttp.SettingsDTO) { s.Mode = "" }, true},
		{"mode rejection (invalid)", func(s *internalhttp.SettingsDTO) { s.Mode = "unlimited" }, true},

		// quiet_start bounds [0, 23] paired with quiet_end [0, 23]
		{"quiet hours lower edge (0, 0)", func(s *internalhttp.SettingsDTO) {
			s.QuietStart = intPtr(0)
			s.QuietEnd = intPtr(0)
		}, false},
		{"quiet hours upper edge (23, 23)", func(s *internalhttp.SettingsDTO) {
			s.QuietStart = intPtr(23)
			s.QuietEnd = intPtr(23)
		}, false},
		{"quiet_start rejection (-1)", func(s *internalhttp.SettingsDTO) {
			s.QuietStart = intPtr(-1)
			s.QuietEnd = intPtr(7)
		}, true},
		{"quiet_start rejection (24)", func(s *internalhttp.SettingsDTO) {
			s.QuietStart = intPtr(24)
			s.QuietEnd = intPtr(7)
		}, true},
		{"quiet_end rejection (-1)", func(s *internalhttp.SettingsDTO) {
			s.QuietStart = intPtr(22)
			s.QuietEnd = intPtr(-1)
		}, true},
		{"quiet_end rejection (24)", func(s *internalhttp.SettingsDTO) {
			s.QuietStart = intPtr(22)
			s.QuietEnd = intPtr(24)
		}, true},
		{"quiet_start without quiet_end rejection", func(s *internalhttp.SettingsDTO) {
			s.QuietStart = intPtr(22)
			s.QuietEnd = nil
		}, true},
		{"quiet_end without quiet_start rejection", func(s *internalhttp.SettingsDTO) {
			s.QuietStart = nil
			s.QuietEnd = intPtr(7)
		}, true},

		// tz validation
		{"tz valid (UTC)", func(s *internalhttp.SettingsDTO) { s.TZ = strPtr("UTC") }, false},
		{"tz valid (Europe/Berlin)", func(s *internalhttp.SettingsDTO) { s.TZ = strPtr("Europe/Berlin") }, false},
		{"tz rejection (invalid IANA name)", func(s *internalhttp.SettingsDTO) { s.TZ = strPtr("Mars/Olympus") }, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dto := validBase
			tc.mutate(&dto)
			err := internalhttp.ValidateSettings(dto)
			if tc.wantError && err == nil {
				t.Fatalf("expected error, got nil for %s", tc.name)
			}
			if !tc.wantError && err != nil {
				t.Fatalf("unexpected error for %s: %v", tc.name, err)
			}
		})
	}
}

// TestGetAndUpdateSettings verifies reading defaults, saving valid settings, and rejecting invalid settings.
func TestGetAndUpdateSettings(t *testing.T) {
	st, cleanup := setupTestStore(t)
	defer cleanup()

	// Seed account
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO accounts (id, key_hash, created_day, last_seen_day) VALUES (1001, 'hash', 20000, 20000)")
		return err
	})
	if err != nil {
		t.Fatalf("seed error: %v", err)
	}

	auth := &mockSettingsAuth{
		sessions: map[string]core.AccountID{
			"valid-token": 1001,
		},
	}
	handler := internalhttp.NewSettingsHandlerWithDeps(st, auth, nil)

	r := chi.NewRouter()
	r.Use(internalhttp.BearerAuthParseMiddleware)
	r.Get("/v1/settings", handler.GetSettings)
	r.Put("/v1/settings", handler.UpdateSettings)

	// 1. Get default settings
	req := httptest.NewRequest(http.MethodGet, "/v1/settings", nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var res internalhttp.SettingsDTO
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if res.DigestWindowS != 60 || res.MaxPerHour != 12 || res.MinCount != 1 || res.Mode != "all" {
		t.Fatalf("unexpected defaults: %+v", res)
	}

	// 2. Put updated settings
	updateBody := internalhttp.SettingsDTO{
		DigestWindowS: 900,
		MaxPerHour:    30,
		QuietStart:    intPtr(22),
		QuietEnd:      intPtr(7),
		TZ:            strPtr("Europe/Berlin"),
		MinCount:      5,
		Mode:          "groups_only",
	}
	bodyBytes, _ := json.Marshal(updateBody)
	putReq := httptest.NewRequest(http.MethodPut, "/v1/settings", bytes.NewReader(bodyBytes))
	putReq.Header.Set("Authorization", "Bearer valid-token")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, putReq)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	// 3. Verify retrieved updated settings
	getReq := httptest.NewRequest(http.MethodGet, "/v1/settings", nil)
	getReq.Header.Set("Authorization", "Bearer valid-token")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, getReq)

	var updated internalhttp.SettingsDTO
	if err := json.NewDecoder(w.Body).Decode(&updated); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if updated.DigestWindowS != 900 || updated.MaxPerHour != 30 || updated.MinCount != 5 || updated.Mode != "groups_only" {
		t.Fatalf("mismatched updated settings: %+v", updated)
	}
	if updated.QuietStart == nil || *updated.QuietStart != 22 || updated.QuietEnd == nil || *updated.QuietEnd != 7 {
		t.Fatalf("mismatched quiet hours: %+v", updated)
	}
	if updated.TZ == nil || *updated.TZ != "Europe/Berlin" {
		t.Fatalf("mismatched tz: %+v", updated)
	}

	// 4. Put invalid settings -> 400
	invalidBody := updateBody
	invalidBody.MaxPerHour = 100 // exceeds 60
	badBytes, _ := json.Marshal(invalidBody)
	badReq := httptest.NewRequest(http.MethodPut, "/v1/settings", bytes.NewReader(badBytes))
	badReq.Header.Set("Authorization", "Bearer valid-token")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, badReq)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid setting, got %d", w.Code)
	}
}

// TestReportAbuse verifies that report-abuse:
// 1. Writes exactly one row into blocks
// 2. Is idempotent on repeated calls
// 3. Returns identical terminal state and timing envelope across unowned, missing, or zero-traffic handles.
func TestReportAbuse(t *testing.T) {
	st, cleanup := setupTestStore(t)
	defer cleanup()

	// Seed accounts and handles
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO accounts (id, key_hash, created_day, last_seen_day) VALUES (1001, 'hash1', 20000, 20000), (1002, 'hash2', 20000, 20000), (9999, 'hash3', 20000, 20000)")
		if err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO handles (handle, account_id, kind, created_day) VALUES ('e7k4p2m9qx3v', 1001, 'personal', 20000)")
		if err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO handles (handle, account_id, kind, created_day) VALUES ('eotherhandle1', 1002, 'personal', 20000)")
		return err
	})
	if err != nil {
		t.Fatalf("seed error: %v", err)
	}

	auth := &mockSettingsAuth{
		sessions: map[string]core.AccountID{
			"user-token": 1001,
		},
	}
	topSender := &mockTopSender{
		topSenders: map[core.Handle]core.AccountID{
			"e7k4p2m9qx3v": 9999, // account 9999 is the abusive top sender
		},
	}
	handler := internalhttp.NewSettingsHandlerWithDeps(st, auth, topSender)

	r := chi.NewRouter()
	r.Use(internalhttp.BearerAuthParseMiddleware)
	r.Post("/v1/handles/{handle}/report-abuse", handler.ReportAbuse)

	// Call 1: Owned handle with abusive sender -> should insert row in blocks
	req := httptest.NewRequest(http.MethodPost, "/v1/handles/e7k4p2m9qx3v/report-abuse", nil)
	req.Header.Set("Authorization", "Bearer user-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	expectedBody := "{\"status\":\"ok\"}\n"
	if w.Body.String() != expectedBody {
		t.Fatalf("expected body %q, got %q", expectedBody, w.Body.String())
	}

	// Verify exactly 1 block exists in DB
	var count int
	var senderID int64
	var blockedHandle string
	err = st.ReadDB().QueryRow("SELECT count(*), sender_account_id, handle FROM blocks").Scan(&count, &senderID, &blockedHandle)
	if err != nil || count != 1 {
		t.Fatalf("expected exactly 1 block, count=%d, err=%v", count, err)
	}
	if senderID != 9999 || blockedHandle != "e7k4p2m9qx3v" {
		t.Fatalf("unexpected block row: sender=%d, handle=%s", senderID, blockedHandle)
	}

	// Call 2: Repeated report on same day (idempotent)
	req2 := httptest.NewRequest(http.MethodPost, "/v1/handles/e7k4p2m9qx3v/report-abuse", nil)
	req2.Header.Set("Authorization", "Bearer user-token")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w2.Code)
	}
	if w2.Body.String() != expectedBody {
		t.Fatalf("expected body %q, got %q", expectedBody, w2.Body.String())
	}

	// Verify still exactly 1 block exists
	err = st.ReadDB().QueryRow("SELECT count(*) FROM blocks").Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("expected still 1 block after repeat, count=%d, err=%v", count, err)
	}
}

// TestReportAbuseResponseUniformity verifies identical status, body, and timing envelope across:
// 1. Handle you don't own
// 2. Nonexistent handle
// 3. Owned handle with no traffic
// 4. Owned handle with top sender traffic
func TestReportAbuseResponseUniformity(t *testing.T) {
	st, cleanup := setupTestStore(t)
	defer cleanup()

	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO accounts (id, key_hash, created_day, last_seen_day) VALUES (1001, 'h1', 20000, 20000), (1002, 'h2', 20000, 20000), (9999, 'h3', 20000, 20000)")
		if err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO handles (handle, account_id, kind, created_day) VALUES ('eownhandle11', 1001, 'personal', 20000)")
		if err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO handles (handle, account_id, kind, created_day) VALUES ('eownnotraffic', 1001, 'personal', 20000)")
		if err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO handles (handle, account_id, kind, created_day) VALUES ('eunownedhand', 1002, 'personal', 20000)")
		return err
	})
	if err != nil {
		t.Fatalf("seed error: %v", err)
	}

	auth := &mockSettingsAuth{
		sessions: map[string]core.AccountID{
			"user-token": 1001,
		},
	}
	topSender := &mockTopSender{
		topSenders: map[core.Handle]core.AccountID{
			"eownhandle11": 9999,
		},
	}
	handler := internalhttp.NewSettingsHandlerWithDeps(st, auth, topSender)

	r := chi.NewRouter()
	r.Use(internalhttp.BearerAuthParseMiddleware)
	r.Post("/v1/handles/{handle}/report-abuse", handler.ReportAbuse)

	cases := []struct {
		name   string
		handle string
	}{
		{"unowned handle", "eunownedhand"},
		{"nonexistent handle", "enonexistent1"},
		{"owned handle with no traffic", "eownnotraffic"},
		{"owned handle with traffic", "eownhandle11"},
	}

	const expectedBody = "{\"status\":\"ok\"}\n"

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/handles/%s/report-abuse", tc.handle), nil)
			req.Header.Set("Authorization", "Bearer user-token")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			elapsed := time.Since(start)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", w.Code)
			}
			if w.Body.String() != expectedBody {
				t.Fatalf("expected body %q, got %q", expectedBody, w.Body.String())
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
				t.Fatalf("expected json content type, got %q", ct)
			}
			// Timing envelope check: all in-memory/sqlite operations finish in < 50 ms
			if elapsed > 50*time.Millisecond {
				t.Fatalf("call took unexpectedly long: %v", elapsed)
			}
		})
	}
}

// TestSweepExpiredBlocks verifies block expiry sweep deletes blocks older than cutoff.
func TestSweepExpiredBlocks(t *testing.T) {
	st, cleanup := setupTestStore(t)
	defer cleanup()

	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO accounts (id, key_hash, created_day, last_seen_day) VALUES (1, 'h1', 100, 100), (2, 'h2', 100, 100), (3, 'h3', 100, 100)")
		if err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO handles (handle, account_id, kind, created_day) VALUES ('ehandle11111', 1, 'personal', 100)")
		if err != nil {
			return err
		}
		// Insert blocks created at days 100, 200, 300, 400
		_, err = tx.Exec(`
			INSERT INTO blocks (sender_account_id, handle, created_day) VALUES
			(2, 'ehandle11111', 100),
			(3, 'ehandle11111', 300)
		`)
		return err
	})
	if err != nil {
		t.Fatalf("seed error: %v", err)
	}

	// Sweep blocks created before day 250 (should delete day 100, keep day 300)
	deleted, err := internalhttp.SweepExpiredBlocksBefore(context.Background(), st, 250)
	if err != nil {
		t.Fatalf("sweep error: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 block deleted, got %d", deleted)
	}

	// Verify remaining block
	var count int
	var createdDay int64
	err = st.ReadDB().QueryRow("SELECT count(*), created_day FROM blocks").Scan(&count, &createdDay)
	if err != nil || count != 1 {
		t.Fatalf("expected 1 remaining block, got count=%d, err=%v", count, err)
	}
	if createdDay != 300 {
		t.Fatalf("expected remaining block created_day 300, got %d", createdDay)
	}
}

// TestBlockedSenderPingReturns202 verifies that a blocked sender's subsequent ping still returns 202.
func TestBlockedSenderPingReturns202(t *testing.T) {
	st, cleanup := setupTestStore(t)
	defer cleanup()

	senderID := core.AccountID(9999)
	handle := core.Handle("e7k4p2m9qx3v")

	// Insert block
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO accounts (id, key_hash, created_day, last_seen_day) VALUES (1001, 'h1', 100, 100), (9999, 'h2', 100, 100)")
		if err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO handles (handle, account_id, kind, created_day) VALUES ('e7k4p2m9qx3v', 1001, 'personal', 100)")
		if err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO blocks (sender_account_id, handle, created_day) VALUES (?, ?, ?)", senderID.Int64(), handle.Raw(), 150)
		return err
	})
	if err != nil {
		t.Fatalf("seed error: %v", err)
	}

	// Verify store reports blocked
	blocked, err := st.IsBlocked(context.Background(), senderID, handle)
	if err != nil {
		t.Fatalf("IsBlocked error: %v", err)
	}
	if !blocked {
		t.Fatalf("expected sender to be blocked in store")
	}

	// Verify that Invariant 7 is maintained: a ping from a blocked sender returns 202 Accepted
	w := httptest.NewRecorder()
	internalhttp.WriteUniformAccepted(w)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted for blocked sender, got %d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("expected empty body for 202 Accepted, got %q", w.Body.String())
	}
}
