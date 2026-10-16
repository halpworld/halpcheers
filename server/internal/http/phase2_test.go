package http_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/halpworld/halpcheers/server/internal/coalesce"
	"github.com/halpworld/halpcheers/server/internal/config"
	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/guard"
	internalhttp "github.com/halpworld/halpcheers/server/internal/http"
	"github.com/halpworld/halpcheers/server/internal/obs"
	"github.com/halpworld/halpcheers/server/internal/region"
)

func TestContactsSyncLifecycleAndConcurrency(t *testing.T) {
	st, cleanup := setupTestStore(t)
	defer cleanup()

	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO accounts (id, key_hash, created_day, last_seen_day) VALUES (1001, 'hash1', 20000, 20000)")
		return err
	})
	if err != nil {
		t.Fatalf("seed error: %v", err)
	}

	auth := &mockSettingsAuth{
		sessions: map[string]core.AccountID{
			"tok-1001": 1001,
		},
	}
	cfg := config.NewDefault()
	handler := internalhttp.NewContactsHandlerWithDeps(st, auth, cfg)

	r := internalhttp.NewRouter(internalhttp.RouterDeps{
		Contacts:     handler,
		MaxBodyBytes: 128 * 1024,
	})

	// 1. Initial GET returns version 0 and empty blob
	req := httptest.NewRequest(http.MethodGet, "/v1/contacts", nil)
	req.Header.Set("Authorization", "Bearer tok-1001")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on initial GET, got %d", w.Code)
	}
	var getRes struct {
		Version int64  `json:"version"`
		Blob    string `json:"blob"`
	}
	if err := json.NewDecoder(w.Body).Decode(&getRes); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if getRes.Version != 0 || getRes.Blob != "" {
		t.Fatalf("expected version=0 and empty blob, got %+v", getRes)
	}

	// 2. Reject unpadded blob (not 4 KiB boundary)
	badBlob := base64.StdEncoding.EncodeToString(make([]byte, 500))
	bodyBytes, _ := json.Marshal(map[string]string{"blob": badBlob})
	putReq := httptest.NewRequest(http.MethodPut, "/v1/contacts", bytes.NewReader(bodyBytes))
	putReq.Header.Set("Authorization", "Bearer tok-1001")
	putReq.Header.Set("If-Match", "0")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, putReq)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unpadded blob, got %d", w.Code)
	}

	// 3. Accept valid 4 KiB-padded AES-GCM blob (4096 plaintext + 28 GCM overhead = 4124 bytes)
	validRaw := make([]byte, 4096+28)
	for i := range validRaw {
		validRaw[i] = byte(i % 251)
	}
	validEncoded := base64.StdEncoding.EncodeToString(validRaw)
	bodyBytes, _ = json.Marshal(map[string]string{"blob": validEncoded})
	putReq = httptest.NewRequest(http.MethodPut, "/v1/contacts", bytes.NewReader(bodyBytes))
	putReq.Header.Set("Authorization", "Bearer tok-1001")
	putReq.Header.Set("If-Match", "0")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, putReq)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on first PUT, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Reject stale If-Match: 0 with 412 Precondition Failed
	putReqStale := httptest.NewRequest(http.MethodPut, "/v1/contacts", bytes.NewReader(bodyBytes))
	putReqStale.Header.Set("Authorization", "Bearer tok-1001")
	putReqStale.Header.Set("If-Match", "0")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, putReqStale)
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("expected 412 Precondition Failed on stale If-Match, got %d", w.Code)
	}

	// 5. Succeed with If-Match: 1 -> increments to version 2
	putReq2 := httptest.NewRequest(http.MethodPut, "/v1/contacts", bytes.NewReader(bodyBytes))
	putReq2.Header.Set("Authorization", "Bearer tok-1001")
	putReq2.Header.Set("If-Match", "1")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, putReq2)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on second PUT, got %d", w.Code)
	}

	// 6. GET returns version 2 and identical ciphertext
	req = httptest.NewRequest(http.MethodGet, "/v1/contacts", nil)
	req.Header.Set("Authorization", "Bearer tok-1001")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if err := json.NewDecoder(w.Body).Decode(&getRes); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if getRes.Version != 2 || getRes.Blob != validEncoded {
		t.Fatalf("expected version 2 and matching blob, got version=%d", getRes.Version)
	}

	// 7. DELETE removes blob
	delReq := httptest.NewRequest(http.MethodDelete, "/v1/contacts", nil)
	delReq.Header.Set("Authorization", "Bearer tok-1001")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, delReq)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on DELETE, got %d", w.Code)
	}
}

func TestGroupsLifecycleMinSizeAndReachability(t *testing.T) {
	st, cleanup := setupTestStore(t)
	defer cleanup()

	// Seed 6 accounts (1001..1006)
	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		for i := 1; i <= 6; i++ {
			id := 1000 + i
			if _, err := tx.Exec(
				"INSERT INTO accounts (id, key_hash, created_day, last_seen_day) VALUES (?, ?, 20000, 20000)",
				id, fmt.Sprintf("hash-%d", id),
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed accounts error: %v", err)
	}

	auth := &mockSettingsAuth{
		sessions: map[string]core.AccountID{
			"tok-1": 1001,
			"tok-2": 1002,
			"tok-3": 1003,
			"tok-4": 1004,
			"tok-5": 1005,
			"tok-6": 1006,
		},
	}

	cfg := config.NewDefault()
	resolver := region.NewResolver(st, 1000)
	regionSvc := region.NewService(st, resolver)
	metrics := obs.NewMetrics()
	guardSvc := guard.NewService(guard.Config{Metrics: metrics})
	queue := coalesce.NewQueue(1024, metrics)

	groupsH := internalhttp.NewGroupsHandlerWithDeps(regionSvc, resolver, st, auth, nil, cfg)
	pingH := internalhttp.NewPingHandlerWithDeps(auth, nil, resolver, guardSvc, queue, metrics)

	r := internalhttp.NewRouter(internalhttp.RouterDeps{
		Groups: groupsH,
		Ping:   pingH,
	})

	// 1. Account 1001 creates group "#acme-dev"
	createBody, _ := json.Marshal(map[string]string{
		"name":         "acme-dev",
		"display_name": "Kenth",
		"note":         "backend",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/groups", bytes.NewReader(createBody))
	req.Header.Set("Authorization", "Bearer tok-1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", w.Code, w.Body.String())
	}

	var created struct {
		GroupID    int64  `json:"group_id"`
		InviteCode string `json:"invite_code"`
		MyHandle   string `json:"my_handle"`
		MinSize    int    `json:"min_size"`
		Active     bool   `json:"active"`
	}
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if created.GroupID <= 0 || created.InviteCode == "" || len(created.MyHandle) != 13 {
		t.Fatalf("unexpected created group response: %+v", created)
	}
	if created.Active {
		t.Fatalf("expected group with 1 member to be inactive below min_size=5")
	}

	// 2. Join accounts 2, 3, 4 (total 4 members < 5 min_size)
	for i := 2; i <= 4; i++ {
		joinBody, _ := json.Marshal(map[string]string{
			"invite_code":  created.InviteCode,
			"display_name": fmt.Sprintf("Member%d", i),
		})
		jReq := httptest.NewRequest(http.MethodPost, "/v1/groups/join", bytes.NewReader(joinBody))
		jReq.Header.Set("Authorization", fmt.Sprintf("Bearer tok-%d", i))
		jw := httptest.NewRecorder()
		r.ServeHTTP(jw, jReq)
		if jw.Code != http.StatusOK {
			t.Fatalf("member %d join failed with %d: %s", i, jw.Code, jw.Body.String())
		}
	}

	// 3. Ping to owner's group handle while group has 4 members (< min_size 5)
	// Must return 202 Accepted (Invariant 7) but NOT enqueue any job!
	pReq := httptest.NewRequest(http.MethodPost, "/v1/ping/"+created.MyHandle, nil)
	pReq.Header.Set("Authorization", "Bearer tok-2")
	pw := httptest.NewRecorder()
	r.ServeHTTP(pw, pReq)
	if pw.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted on below-min-size group ping, got %d", pw.Code)
	}
	select {
	case <-queue.Channel():
		t.Fatalf("expected ping to group handle below min_size=5 to be suppressed")
	default:
	}

	// 4. Member 5 joins -> group reaches min_size = 5!
	join5Body, _ := json.Marshal(map[string]string{
		"invite_code":  created.InviteCode,
		"display_name": "Member5",
	})
	jReq5 := httptest.NewRequest(http.MethodPost, "/v1/groups/join", bytes.NewReader(join5Body))
	jReq5.Header.Set("Authorization", "Bearer tok-5")
	jw5 := httptest.NewRecorder()
	r.ServeHTTP(jw5, jReq5)
	if jw5.Code != http.StatusOK {
		t.Fatalf("member 5 join failed: %d", jw5.Code)
	}

	// 5. Non-member (Account 6) tries to ping owner's group handle -> returns 202 but suppressed!
	pReqNonMember := httptest.NewRequest(http.MethodPost, "/v1/ping/"+created.MyHandle, nil)
	pReqNonMember.Header.Set("Authorization", "Bearer tok-6")
	pwNM := httptest.NewRecorder()
	r.ServeHTTP(pwNM, pReqNonMember)
	if pwNM.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted for non-member, got %d", pwNM.Code)
	}
	select {
	case <-queue.Channel():
		t.Fatalf("expected non-member ping to group-scoped handle to be suppressed")
	default:
	}

	// 6. Group member (Account 2) pings owner's group handle -> accepted and enqueued!
	pReqMember := httptest.NewRequest(http.MethodPost, "/v1/ping/"+created.MyHandle, nil)
	pReqMember.Header.Set("Authorization", "Bearer tok-2")
	pwM := httptest.NewRecorder()
	r.ServeHTTP(pwM, pReqMember)
	if pwM.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted for group member, got %d", pwM.Code)
	}
	select {
	case job := <-queue.Channel():
		if job.Recipient != 1001 {
			t.Fatalf("expected recipient 1001, got %v", job.Recipient)
		}
	default:
		t.Fatalf("expected group member ping to active group (>= 5 members) to be enqueued")
	}

	// 7. Verify roster response never leaks account_id or personal handles (docs/GROUPS.md § Roster)
	rosterReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/groups/%d/members", created.GroupID), nil)
	rosterReq.Header.Set("Authorization", "Bearer tok-2")
	rw := httptest.NewRecorder()
	r.ServeHTTP(rw, rosterReq)
	if rw.Code != http.StatusOK {
		t.Fatalf("expected 200 on roster read, got %d", rw.Code)
	}
	rawRoster := rw.Body.String()
	if bytes.Contains([]byte(rawRoster), []byte("account_id")) || bytes.Contains([]byte(rawRoster), []byte("1001")) {
		t.Fatalf("roster leaked account_id: %s", rawRoster)
	}

	// 8. Member 5 leaves -> their group handle is burned and group drops back to 4 members (< min_size 5)
	leaveReq := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/v1/groups/%d/membership", created.GroupID), nil)
	leaveReq.Header.Set("Authorization", "Bearer tok-5")
	lw := httptest.NewRecorder()
	r.ServeHTTP(lw, leaveReq)
	if lw.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on leave group, got %d", lw.Code)
	}
}
