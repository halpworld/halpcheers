package http_test

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/halpworld/halpcheers/server/internal/auth/pow"
	"github.com/halpworld/halpcheers/server/internal/coalesce"
	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/guard"
	internalhttp "github.com/halpworld/halpcheers/server/internal/http"
	"github.com/halpworld/halpcheers/server/internal/obs"
	"github.com/halpworld/halpcheers/server/internal/region"
	"github.com/halpworld/halpcheers/server/internal/stream"
)

func TestStreamerHandleCreationAndPresets(t *testing.T) {
	st, cleanup := setupTestStore(t)
	defer cleanup()

	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO accounts (id, key_hash, created_day, last_seen_day) VALUES (1001, 'hash1001', 20000, 20000)")
		return err
	})
	if err != nil {
		t.Fatalf("failed to seed account: %v", err)
	}

	auth := &mockSettingsAuth{
		sessions: map[string]core.AccountID{
			"tok-1001": 1001,
		},
	}
	resolver := region.NewResolver(st, 10_000)
	regionSvc := region.NewService(st, resolver)
	handlesH := internalhttp.NewHandlesHandlerWithDeps(regionSvc, resolver, st, auth)

	r := internalhttp.NewRouter(internalhttp.RouterDeps{
		Handles: handlesH,
	})

	// 1. Create a stream handle
	createReqBody, _ := json.Marshal(map[string]any{
		"kind":  "stream",
		"label": "Twitch Stream",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/handles", bytes.NewReader(createReqBody))
	req.Header.Set("Authorization", "Bearer tok-1001")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", w.Code, w.Body.String())
	}

	var createResp struct {
		Handle string         `json:"handle"`
		Label  string         `json:"label"`
		Kind   string         `json:"kind"`
		Policy map[string]any `json:"policy"`
	}
	if err := json.NewDecoder(w.Body).Decode(&createResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if createResp.Kind != "stream" {
		t.Errorf("expected kind 'stream', got %q", createResp.Kind)
	}
	if createResp.Label != "Twitch Stream" {
		t.Errorf("expected label 'Twitch Stream', got %q", createResp.Label)
	}
	if createResp.Policy == nil {
		t.Fatal("expected policy in create response, got nil")
	}

	tokVal, ok := createResp.Policy["overlay_token"].(string)
	if !ok || len(tokVal) != 32 {
		t.Fatalf("expected 32-hex overlay_token, got %v", createResp.Policy["overlay_token"])
	}
	if createResp.Policy["overlay_style"] != "pop-in" {
		t.Errorf("expected default overlay_style 'pop-in', got %v", createResp.Policy["overlay_style"])
	}
	if int(createResp.Policy["digest_window_s"].(float64)) != 60 {
		t.Errorf("expected digest_window_s 60, got %v", createResp.Policy["digest_window_s"])
	}

	// 2. List handles and verify policy is included
	listReq := httptest.NewRequest(http.MethodGet, "/v1/handles", nil)
	listReq.Header.Set("Authorization", "Bearer tok-1001")
	wList := httptest.NewRecorder()
	r.ServeHTTP(wList, listReq)

	if wList.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", wList.Code)
	}

	var listResp struct {
		Handles []struct {
			Handle string         `json:"handle"`
			Kind   string         `json:"kind"`
			Policy map[string]any `json:"policy"`
		} `json:"handles"`
	}
	if err := json.NewDecoder(wList.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listResp.Handles) != 1 {
		t.Fatalf("expected 1 handle, got %d", len(listResp.Handles))
	}
	listedPolicy := listResp.Handles[0].Policy
	if listedPolicy == nil || listedPolicy["overlay_token"] != tokVal {
		t.Fatalf("listed policy mismatch: got %+v", listedPolicy)
	}
}

func TestOverlayHTMLRendering(t *testing.T) {
	st, cleanup := setupTestStore(t)
	defer cleanup()

	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO accounts (id, key_hash, created_day, last_seen_day) VALUES (2001, 'hash2001', 20000, 20000)")
		if err != nil {
			return err
		}
		policy := `{"digest_window_s":60,"max_per_hour":60,"min_count":1,"overlay_token":"1234567890abcdef1234567890abcdef","overlay_style":"pop-in"}`
		_, err = tx.Exec("INSERT INTO handles (handle, account_id, label, kind, paused, policy_json, created_day) VALUES ('e2001stream0001', 2001, 'Live', 'stream', 0, ?, 20000)", policy)
		return err
	})
	if err != nil {
		t.Fatalf("failed to seed handle: %v", err)
	}

	metrics := obs.NewMetrics()
	streamHub := stream.NewHub(stream.Config{Metrics: metrics})
	publicH := internalhttp.NewConfiguredPublicHandlerWithDeps(st, streamHub)

	r := internalhttp.NewRouter(internalhttp.RouterDeps{
		Public: publicH,
	})

	token := "1234567890abcdef1234567890abcdef"

	// 1. Default HTML fetch
	req := httptest.NewRequest(http.MethodGet, "/overlay/"+token, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Errorf("expected text/html, got %q", w.Header().Get("Content-Type"))
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("expected Cache-Control no-store, got %q", w.Header().Get("Cache-Control"))
	}

	body := w.Body.String()
	if !strings.Contains(body, "background: transparent !important") {
		t.Error("body must include transparent background")
	}
	if !strings.Contains(body, "prefers-reduced-motion") {
		t.Error("body must include prefers-reduced-motion support")
	}
	if !strings.Contains(body, "style-pop-in") {
		t.Error("expected default pop-in style class in body")
	}
	if !strings.Contains(body, "Someone appreciates you.") {
		t.Error("expected singular appreciation copy in script")
	}
	if !strings.Contains(body, "people appreciate you.") {
		t.Error("expected plural appreciation copy in script")
	}
	if !strings.Contains(body, "?stream=1") {
		t.Error("expected script to connect to ?stream=1")
	}

	// Verify zero external origins
	if strings.Contains(body, "http://") || strings.Contains(body, "https://") {
		t.Error("overlay page must contain zero external URLs or origins")
	}

	// 2. Query param ?style=meter
	reqMeter := httptest.NewRequest(http.MethodGet, "/overlay/"+token+"?style=meter", nil)
	wMeter := httptest.NewRecorder()
	r.ServeHTTP(wMeter, reqMeter)
	if !strings.Contains(wMeter.Body.String(), "style-meter") || !strings.Contains(wMeter.Body.String(), "meter-track") {
		t.Error("expected meter style and track markup")
	}

	// 3. Query param ?style=corner
	reqCorner := httptest.NewRequest(http.MethodGet, "/overlay/"+token+"?style=corner", nil)
	wCorner := httptest.NewRecorder()
	r.ServeHTTP(wCorner, reqCorner)
	if !strings.Contains(wCorner.Body.String(), "style-corner") {
		t.Error("expected corner style markup")
	}

	// 4. Invalid token format (<32 chars) -> 404
	reqBad := httptest.NewRequest(http.MethodGet, "/overlay/shorttoken", nil)
	wBad := httptest.NewRecorder()
	r.ServeHTTP(wBad, reqBad)
	if wBad.Code != http.StatusNotFound {
		t.Errorf("expected 404 for malformed token, got %d", wBad.Code)
	}

	// 5. Non-existent 32-hex token -> 404
	reqNonExistent := httptest.NewRequest(http.MethodGet, "/overlay/abcdef0123456789abcdef0123456789", nil)
	wNonExistent := httptest.NewRecorder()
	r.ServeHTTP(wNonExistent, reqNonExistent)
	if wNonExistent.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown token, got %d", wNonExistent.Code)
	}
}

func TestOverlaySSEStreamLiveDigest(t *testing.T) {
	st, cleanup := setupTestStore(t)
	defer cleanup()

	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO accounts (id, key_hash, created_day, last_seen_day) VALUES (3001, 'hash3001', 20000, 20000)")
		if err != nil {
			return err
		}
		policy := `{"digest_window_s":60,"max_per_hour":60,"min_count":1,"overlay_token":"fedcba9876543210fedcba9876543210","overlay_style":"pop-in"}`
		_, err = tx.Exec("INSERT INTO handles (handle, account_id, label, kind, paused, policy_json, created_day) VALUES ('e3001stream0001', 3001, 'Live', 'stream', 0, ?, 20000)", policy)
		return err
	})
	if err != nil {
		t.Fatalf("failed to seed handle: %v", err)
	}

	metrics := obs.NewMetrics()
	streamHub := stream.NewHub(stream.Config{
		HeartbeatInterval: 15 * time.Second,
		Metrics:           metrics,
	})
	defer streamHub.Close()

	publicH := internalhttp.NewConfiguredPublicHandlerWithDeps(st, streamHub)
	r := internalhttp.NewRouter(internalhttp.RouterDeps{
		Public: publicH,
	})

	ts := httptest.NewServer(r)
	defer ts.Close()

	token := "fedcba9876543210fedcba9876543210"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	streamURL := ts.URL + "/overlay/" + token + "?stream=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	client := ts.Client()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("SSE connection error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %s", resp.Header.Get("Content-Type"))
	}

	reader := bufio.NewReader(resp.Body)

	// Read initial lines (should contain ": ok" comment)
	firstLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("failed to read initial SSE line: %v", err)
	}
	if !strings.Contains(firstLine, ": ok") {
		t.Logf("first line was: %q (expected comment : ok)", firstLine)
	}

	// Broadcast digest to account 3001
	time.Sleep(50 * time.Millisecond)
	delivered := streamHub.Broadcast(3001, 1)
	if !delivered {
		t.Fatal("expected broadcast to be delivered to active connection")
	}

	// Read next lines until event: ping and data: {"n":1} are seen
	foundPing := false
	foundData := false
	for i := 0; i < 10; i++ {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatalf("error reading SSE stream: %v", readErr)
		}
		lineTrimmed := strings.TrimSpace(line)
		if lineTrimmed == "event: ping" {
			foundPing = true
		}
		if lineTrimmed == `data: {"n":1}` {
			foundData = true
			break
		}
	}

	if !foundPing || !foundData {
		t.Fatalf("did not receive expected ping event: ping=%v data=%v", foundPing, foundData)
	}

	// Broadcast another digest with n=42
	delivered = streamHub.Broadcast(3001, 42)
	if !delivered {
		t.Fatal("expected second broadcast to be delivered")
	}

	foundData42 := false
	for i := 0; i < 10; i++ {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			break
		}
		if strings.TrimSpace(line) == `data: {"n":42}` {
			foundData42 = true
			break
		}
	}
	if !foundData42 {
		t.Fatal("did not receive expected ping data for n=42")
	}
}

func TestOverlayTokenRotationAndRevocation(t *testing.T) {
	st, cleanup := setupTestStore(t)
	defer cleanup()

	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO accounts (id, key_hash, created_day, last_seen_day) VALUES (4001, 'hash4001', 20000, 20000)")
		return err
	})
	if err != nil {
		t.Fatalf("failed to seed account: %v", err)
	}

	auth := &mockSettingsAuth{
		sessions: map[string]core.AccountID{
			"tok-4001": 4001,
		},
	}

	metrics := obs.NewMetrics()
	streamHub := stream.NewHub(stream.Config{Metrics: metrics})
	defer streamHub.Close()

	resolver := region.NewResolver(st, 10_000)
	regionSvc := region.NewService(st, resolver)
	handlesH := internalhttp.NewHandlesHandlerWithDeps(regionSvc, resolver, st, auth)
	publicH := internalhttp.NewConfiguredPublicHandlerWithDeps(st, streamHub)

	queue := coalesce.NewQueue(1024, metrics)
	powEngine, _ := pow.New(pow.Config{Metrics: metrics})
	guardSvc := guard.NewService(guard.Config{Metrics: metrics})
	pingH := internalhttp.NewPingHandlerWithDeps(auth, powEngine, resolver, guardSvc, queue, metrics)

	r := internalhttp.NewRouter(internalhttp.RouterDeps{
		Handles: handlesH,
		Public:  publicH,
		Ping:    pingH,
	})

	// 1. Create stream handle
	createReqBody, _ := json.Marshal(map[string]any{
		"kind":  "stream",
		"label": "Gamer Stream",
	})
	reqCreate := httptest.NewRequest(http.MethodPost, "/v1/handles", bytes.NewReader(createReqBody))
	reqCreate.Header.Set("Authorization", "Bearer tok-4001")
	wCreate := httptest.NewRecorder()
	r.ServeHTTP(wCreate, reqCreate)
	if wCreate.Code != http.StatusCreated {
		t.Fatalf("create handle failed: %d", wCreate.Code)
	}

	var created struct {
		Handle string         `json:"handle"`
		Policy map[string]any `json:"policy"`
	}
	_ = json.NewDecoder(wCreate.Body).Decode(&created)
	initialToken := created.Policy["overlay_token"].(string)
	handleIdent := created.Handle

	// Verify overlay serves 200 OK for initial token
	reqOverlay1 := httptest.NewRequest(http.MethodGet, "/overlay/"+initialToken, nil)
	wOverlay1 := httptest.NewRecorder()
	r.ServeHTTP(wOverlay1, reqOverlay1)
	if wOverlay1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on initial overlay token, got %d", wOverlay1.Code)
	}

	// 2. Rotate overlay token
	rotateBody, _ := json.Marshal(map[string]any{
		"policy": map[string]any{
			"rotate_overlay_token": true,
		},
	})
	reqRotate := httptest.NewRequest(http.MethodPatch, "/v1/handles/"+handleIdent, bytes.NewReader(rotateBody))
	reqRotate.Header.Set("Authorization", "Bearer tok-4001")
	wRotate := httptest.NewRecorder()
	r.ServeHTTP(wRotate, reqRotate)

	if wRotate.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on rotate, got %d: %s", wRotate.Code, wRotate.Body.String())
	}

	var rotateResp struct {
		Handle string         `json:"handle"`
		Policy map[string]any `json:"policy"`
	}
	_ = json.NewDecoder(wRotate.Body).Decode(&rotateResp)
	rotatedToken, ok := rotateResp.Policy["overlay_token"].(string)
	if !ok || len(rotatedToken) != 32 {
		t.Fatalf("expected new 32-hex token, got %v", rotateResp.Policy["overlay_token"])
	}
	if rotatedToken == initialToken {
		t.Fatal("expected rotated token to be different from initial token")
	}
	if _, exists := rotateResp.Policy["rotate_overlay_token"]; exists {
		t.Fatal("rotate_overlay_token transient key should not remain in policy")
	}

	// Old token must return 404
	reqOld := httptest.NewRequest(http.MethodGet, "/overlay/"+initialToken, nil)
	wOld := httptest.NewRecorder()
	r.ServeHTTP(wOld, reqOld)
	if wOld.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for rotated-out old token, got %d", wOld.Code)
	}

	// New token must return 200 OK
	reqNew := httptest.NewRequest(http.MethodGet, "/overlay/"+rotatedToken, nil)
	wNew := httptest.NewRecorder()
	r.ServeHTTP(wNew, reqNew)
	if wNew.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for new rotated token, got %d", wNew.Code)
	}

	// 3. Revoke overlay token
	revokeBody, _ := json.Marshal(map[string]any{
		"policy": map[string]any{
			"revoke_overlay_token": true,
		},
	})
	reqRevoke := httptest.NewRequest(http.MethodPatch, "/v1/handles/"+handleIdent, bytes.NewReader(revokeBody))
	reqRevoke.Header.Set("Authorization", "Bearer tok-4001")
	wRevoke := httptest.NewRecorder()
	r.ServeHTTP(wRevoke, reqRevoke)

	if wRevoke.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on revoke, got %d: %s", wRevoke.Code, wRevoke.Body.String())
	}

	var revokeResp struct {
		Handle string         `json:"handle"`
		Policy map[string]any `json:"policy"`
	}
	_ = json.NewDecoder(wRevoke.Body).Decode(&revokeResp)
	if _, exists := revokeResp.Policy["overlay_token"]; exists {
		t.Fatalf("expected overlay_token to be deleted from policy, got %v", revokeResp.Policy["overlay_token"])
	}
	if _, exists := revokeResp.Policy["revoke_overlay_token"]; exists {
		t.Fatal("revoke_overlay_token transient key should not remain in policy")
	}

	// Revoked token must return 404
	reqRevoked := httptest.NewRequest(http.MethodGet, "/overlay/"+rotatedToken, nil)
	wRevoked := httptest.NewRecorder()
	r.ServeHTTP(wRevoked, reqRevoked)
	if wRevoked.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for revoked token, got %d", wRevoked.Code)
	}

	// 4. Invariant: Handle itself must still accept pings!
	reqPing := httptest.NewRequest(http.MethodPost, "/v1/ping/"+handleIdent, bytes.NewReader([]byte("{}")))
	reqPing.Header.Set("Authorization", "Bearer tok-4001")
	wPing := httptest.NewRecorder()
	r.ServeHTTP(wPing, reqPing)
	if wPing.Code != http.StatusAccepted {
		t.Fatalf("expected handle to still accept pings with 202 Accepted, got %d: %s", wPing.Code, wPing.Body.String())
	}
}

func TestTransparencyReport(t *testing.T) {
	st, cleanup := setupTestStore(t)
	defer cleanup()

	err := st.Write(context.Background(), func(tx *sql.Tx) error {
		// Accounts: 2 active, 1 suspended
		_, err := tx.Exec("INSERT INTO accounts (id, key_hash, created_day, last_seen_day, suspended) VALUES (5001, 'h1', 20000, 20000, 0)")
		if err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO accounts (id, key_hash, created_day, last_seen_day, suspended) VALUES (5002, 'h2', 20000, 20000, 0)")
		if err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO accounts (id, key_hash, created_day, last_seen_day, suspended) VALUES (5003, 'h3', 20000, 20000, 1)")
		if err != nil {
			return err
		}

		// Handles: 2 unpaused, 1 paused
		_, err = tx.Exec("INSERT INTO handles (handle, account_id, kind, paused, created_day) VALUES ('e5001p001', 5001, 'personal', 0, 20000)")
		if err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO handles (handle, account_id, kind, paused, created_day) VALUES ('e5002s001', 5002, 'stream', 0, 20000)")
		if err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO handles (handle, account_id, kind, paused, created_day) VALUES ('e5002s002', 5002, 'social', 1, 20000)")
		if err != nil {
			return err
		}

		// Groups: 1 group
		_, err = tx.Exec("INSERT INTO groups (id, name, invite_code_hash, owner_account_id, created_day) VALUES (1, 'Test Group', 'hashgrp', 5001, 20000)")
		return err
	})
	if err != nil {
		t.Fatalf("failed to seed transparency data: %v", err)
	}

	report, err := obs.GenerateTransparencyReport(context.Background(), st.ReadDB())
	if err != nil {
		t.Fatalf("failed to generate transparency report: %v", err)
	}

	if report.Region != "eu-1" {
		t.Errorf("expected Region 'eu-1', got %q", report.Region)
	}
	if report.MessageRecordsStored != 0 {
		t.Errorf("expected MessageRecordsStored 0, got %d", report.MessageRecordsStored)
	}
	if report.SenderIdentitiesStored != 0 {
		t.Errorf("expected SenderIdentitiesStored 0, got %d", report.SenderIdentitiesStored)
	}
	if report.ActiveAccountsCount != 2 {
		t.Errorf("expected ActiveAccountsCount 2, got %d", report.ActiveAccountsCount)
	}
	if report.ActiveHandlesCount != 2 {
		t.Errorf("expected ActiveHandlesCount 2, got %d", report.ActiveHandlesCount)
	}
	if report.ActiveGroupsCount != 1 {
		t.Errorf("expected ActiveGroupsCount 1, got %d", report.ActiveGroupsCount)
	}
	if report.GeneratedDay != core.Today() {
		t.Errorf("expected GeneratedDay %v, got %v", core.Today(), report.GeneratedDay)
	}
}
