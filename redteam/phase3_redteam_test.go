package redteam

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/halpworld/halpcheers/server/testenv"
	_ "modernc.org/sqlite"
)

// Helper: send a ping to target and return response and body bytes.
func sendPhase3Ping(t *testing.T, env *testenv.Environment, tok string, target string) (*http.Response, []byte) {
	t.Helper()
	powStr := env.SolvePoW(target, 14)
	req, err := http.NewRequest(http.MethodPost, env.BaseURL+"/v1/ping/"+target, nil)
	if err != nil {
		t.Fatalf("failed to create ping request: %v", err)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("X-Halp-PoW", powStr)

	res, err := env.HTTPClient.Do(req)
	if err != nil {
		t.Fatalf("failed to send ping: %v", err)
	}
	body, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil {
		t.Fatalf("failed to read ping response body: %v", err)
	}
	return res, body
}

// Helper: query /v1/pending count for tok.
func getPhase3Pending(t *testing.T, env *testenv.Environment, tok string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, env.BaseURL+"/v1/pending", nil)
	if err != nil {
		t.Fatalf("failed to create pending request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	res, err := env.HTTPClient.Do(req)
	if err != nil {
		t.Fatalf("failed to get pending count: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from pending endpoint, got %d", res.StatusCode)
	}

	var data struct {
		N int `json:"n"`
	}
	if err := json.NewDecoder(res.Body).Decode(&data); err != nil {
		t.Fatalf("failed to decode pending count: %v", err)
	}
	return data.N
}

// Helper: wait for pending count to match expected.
func waitPhase3Pending(t *testing.T, env *testenv.Environment, tok string, expected int, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	lastSeen := 0
	for time.Now().Before(deadline) {
		lastSeen = getPhase3Pending(t, env, tok)
		if lastSeen == expected {
			return lastSeen
		}
		time.Sleep(10 * time.Millisecond)
	}
	return lastSeen
}

// TestOverlayTokenSecrecyAndRevocationIndependence verifies Requirement 1:
// - GET /overlay/{handle} (passing public stream handle instead of secret overlay_token) is rejected with 404.
// - Rotating overlay_token via PATCH /v1/handles/{handle} immediately invalidates the old token (404).
// - Revoking overlay_token via PATCH /v1/handles/{handle} immediately invalidates the active token (404).
// - Pinging the stream handle still returns 202 Accepted and delivers after overlay token revocation.
func TestOverlayTokenSecrecyAndRevocationIndependence(t *testing.T) {
	env, err := testenv.New()
	if err != nil {
		t.Fatalf("testenv.New: %v", err)
	}
	defer env.Close()

	// 1. Create streamer account and sender account
	_, streamerTok, err := env.CreateAccountAndSession(0x81)
	if err != nil {
		t.Fatalf("create streamer account: %v", err)
	}
	_, senderTok, err := env.CreateAccountAndSession(0x82)
	if err != nil {
		t.Fatalf("create sender account: %v", err)
	}

	// 2. Create stream-kind handle via POST /v1/handles
	createBody, _ := json.Marshal(map[string]any{
		"kind":  "stream",
		"label": "Twitch Overlay",
	})
	createReq, err := http.NewRequest(http.MethodPost, env.BaseURL+"/v1/handles", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create handle request: %v", err)
	}
	createReq.Header.Set("Authorization", "Bearer "+streamerTok)

	createRes, err := env.HTTPClient.Do(createReq)
	if err != nil {
		t.Fatalf("create stream handle failed: %v", err)
	}
	defer createRes.Body.Close()
	if createRes.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created from create handle, got %d", createRes.StatusCode)
	}

	var created struct {
		Handle string         `json:"handle"`
		Kind   string         `json:"kind"`
		Policy map[string]any `json:"policy"`
	}
	if err := json.NewDecoder(createRes.Body).Decode(&created); err != nil {
		t.Fatalf("failed to decode create handle response: %v", err)
	}
	streamTarget := created.Handle
	if streamTarget == "" || created.Kind != "stream" {
		t.Fatalf("unexpected stream handle created: %+v", created)
	}

	initialOverlayTok, ok := created.Policy["overlay_token"].(string)
	if !ok || len(initialOverlayTok) != 32 {
		t.Fatalf("expected 32-hex overlay_token in policy, got %q", initialOverlayTok)
	}

	// 3. Overlay Token Secrecy: GET /overlay/{handle} using public stream handle MUST return 404 Not Found
	// docs/SHARING.md: "A URL with a read-only overlay token (not the handle, so it can be revoked without changing the handle)"
	leakReq, _ := http.NewRequest(http.MethodGet, env.BaseURL+"/overlay/"+streamTarget, nil)
	leakRes, err := env.HTTPClient.Do(leakReq)
	if err != nil {
		t.Fatalf("get overlay with handle: %v", err)
	}
	_ = leakRes.Body.Close()
	if leakRes.StatusCode != http.StatusNotFound {
		t.Fatalf("critical secrecy leak: GET /overlay/{handle} returned status %d, expected 404 Not Found", leakRes.StatusCode)
	}

	// 4. Valid Overlay Token: GET /overlay/{token} returns 200 OK
	okReq, _ := http.NewRequest(http.MethodGet, env.BaseURL+"/overlay/"+initialOverlayTok, nil)
	okRes, err := env.HTTPClient.Do(okReq)
	if err != nil {
		t.Fatalf("get overlay with valid token: %v", err)
	}
	_ = okRes.Body.Close()
	if okRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for valid overlay token, got %d", okRes.StatusCode)
	}

	// 5. Rotate Overlay Token via PATCH /v1/handles/{handle}
	rotateBody, _ := json.Marshal(map[string]any{
		"policy": map[string]any{
			"rotate_overlay_token": true,
		},
	})
	patchReq, _ := http.NewRequest(http.MethodPatch, env.BaseURL+"/v1/handles/"+streamTarget, bytes.NewReader(rotateBody))
	patchReq.Header.Set("Authorization", "Bearer "+streamerTok)
	patchRes, err := env.HTTPClient.Do(patchReq)
	if err != nil {
		t.Fatalf("rotate overlay token failed: %v", err)
	}
	defer patchRes.Body.Close()
	if patchRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on PATCH /v1/handles, got %d", patchRes.StatusCode)
	}

	var patchData struct {
		Policy map[string]any `json:"policy"`
	}
	if err := json.NewDecoder(patchRes.Body).Decode(&patchData); err != nil {
		t.Fatalf("failed to decode patch response: %v", err)
	}
	rotatedOverlayTok, ok := patchData.Policy["overlay_token"].(string)
	if !ok || len(rotatedOverlayTok) != 32 {
		t.Fatalf("expected new 32-hex overlay_token after rotation, got %q", rotatedOverlayTok)
	}
	if rotatedOverlayTok == initialOverlayTok {
		t.Fatalf("expected rotated overlay_token to differ from initial token")
	}

	// Immediate Invalidation of Old Token: Old token returns 404
	oldReq, _ := http.NewRequest(http.MethodGet, env.BaseURL+"/overlay/"+initialOverlayTok, nil)
	oldRes, err := env.HTTPClient.Do(oldReq)
	if err != nil {
		t.Fatalf("get overlay with old token: %v", err)
	}
	_ = oldRes.Body.Close()
	if oldRes.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found for rotated old overlay token, got %d", oldRes.StatusCode)
	}

	// New Rotated Token works: returns 200 OK
	newReq, _ := http.NewRequest(http.MethodGet, env.BaseURL+"/overlay/"+rotatedOverlayTok, nil)
	newRes, err := env.HTTPClient.Do(newReq)
	if err != nil {
		t.Fatalf("get overlay with new rotated token: %v", err)
	}
	_ = newRes.Body.Close()
	if newRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for new rotated overlay token, got %d", newRes.StatusCode)
	}

	// 6. Revoke Overlay Token via PATCH /v1/handles/{handle}
	revokeBody, _ := json.Marshal(map[string]any{
		"policy": map[string]any{
			"revoke_overlay_token": true,
		},
	})
	revReq, _ := http.NewRequest(http.MethodPatch, env.BaseURL+"/v1/handles/"+streamTarget, bytes.NewReader(revokeBody))
	revReq.Header.Set("Authorization", "Bearer "+streamerTok)
	revRes, err := env.HTTPClient.Do(revReq)
	if err != nil {
		t.Fatalf("revoke overlay token failed: %v", err)
	}
	_ = revRes.Body.Close()
	if revRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on revoke overlay token, got %d", revRes.StatusCode)
	}

	// Immediate Invalidation: Revoked token returns 404
	revokedReq, _ := http.NewRequest(http.MethodGet, env.BaseURL+"/overlay/"+rotatedOverlayTok, nil)
	revokedRes, err := env.HTTPClient.Do(revokedReq)
	if err != nil {
		t.Fatalf("get overlay with revoked token: %v", err)
	}
	_ = revokedRes.Body.Close()
	if revokedRes.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found for revoked overlay token, got %d", revokedRes.StatusCode)
	}

	// 7. Revocation Independence: Pinging the stream handle STILL works and returns 202 Accepted
	_ = getPhase3Pending(t, env, streamerTok)
	pingRes, _ := sendPhase3Ping(t, env, senderTok, streamTarget)
	if pingRes.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted for ping to stream handle after overlay revocation, got %d", pingRes.StatusCode)
	}

	delivered := waitPhase3Pending(t, env, streamerTok, 1, 200*time.Millisecond)
	if delivered != 1 {
		t.Fatalf("expected 1 delivery to streamer account after overlay revocation, got %d", delivered)
	}
}

// TestOverlayZeroExternalOriginsAndCopyBands verifies Requirement 2:
// - GET /overlay/{token} HTML contains zero external URLs, protocols, or CDNs.
// - Uses verbatim copy bands: "Someone appreciates you." and " people appreciate you.".
func TestOverlayZeroExternalOriginsAndCopyBands(t *testing.T) {
	env, err := testenv.New()
	if err != nil {
		t.Fatalf("testenv.New: %v", err)
	}
	defer env.Close()

	_, streamerTok, err := env.CreateAccountAndSession(0x83)
	if err != nil {
		t.Fatalf("create streamer: %v", err)
	}

	createBody, _ := json.Marshal(map[string]any{
		"kind":  "stream",
		"label": "OBS Zero Origins",
	})
	createReq, _ := http.NewRequest(http.MethodPost, env.BaseURL+"/v1/handles", bytes.NewReader(createBody))
	createReq.Header.Set("Authorization", "Bearer "+streamerTok)
	createRes, err := env.HTTPClient.Do(createReq)
	if err != nil {
		t.Fatalf("create handle: %v", err)
	}
	defer createRes.Body.Close()

	var created struct {
		Policy map[string]any `json:"policy"`
	}
	_ = json.NewDecoder(createRes.Body).Decode(&created)
	overlayTok, _ := created.Policy["overlay_token"].(string)
	if len(overlayTok) != 32 {
		t.Fatalf("invalid overlay token: %q", overlayTok)
	}

	// Fetch Overlay HTML
	getReq, _ := http.NewRequest(http.MethodGet, env.BaseURL+"/overlay/"+overlayTok, nil)
	getRes, err := env.HTTPClient.Do(getReq)
	if err != nil {
		t.Fatalf("get overlay html: %v", err)
	}
	defer getRes.Body.Close()
	if getRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from overlay endpoint, got %d", getRes.StatusCode)
	}

	bodyBytes, err := io.ReadAll(getRes.Body)
	if err != nil {
		t.Fatalf("read overlay html body: %v", err)
	}
	htmlContent := string(bodyBytes)

	// 1. Zero External Origins and CDNs Audit:
	// Overlay HTML must be self-contained: no external scripts, styles, or assets.
	forbiddenOrigins := []string{
		"http://",
		"https://",
		"//", // Protocol-relative URLs
		"cdnjs",
		"unpkg",
		"jsdelivr",
		"googleapis",
		"gstatic",
		"bootstrapcdn",
	}

	for _, token := range forbiddenOrigins {
		if strings.Contains(strings.ToLower(htmlContent), token) {
			t.Fatalf("critical origin violation: overlay HTML contains forbidden origin string %q", token)
		}
	}

	// 2. Verbatim Copy Bands Audit:
	// Verify exact verbatim strings per docs/UI.md & Phase 3 specifications.
	bandSingle := "Someone appreciates you."
	bandMulti := " people appreciate you."

	if !strings.Contains(htmlContent, bandSingle) {
		t.Fatalf("overlay HTML missing verbatim singular copy band %q", bandSingle)
	}
	if !strings.Contains(htmlContent, bandMulti) {
		t.Fatalf("overlay HTML missing verbatim plural copy band %q", bandMulti)
	}

	// 3. Verify Styles Query: "meter", "corner", "pop-in"
	styles := []string{"pop-in", "meter", "corner"}
	for _, st := range styles {
		stReq, _ := http.NewRequest(http.MethodGet, env.BaseURL+"/overlay/"+overlayTok+"?style="+st, nil)
		stRes, err := env.HTTPClient.Do(stReq)
		if err != nil {
			t.Fatalf("get style %s: %v", st, err)
		}
		stBody, _ := io.ReadAll(stRes.Body)
		_ = stRes.Body.Close()
		if stRes.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 for style %s, got %d", st, stRes.StatusCode)
		}
		if !strings.Contains(string(stBody), "style-"+st) {
			t.Fatalf("expected overlay HTML to render class style-%s", st)
		}
	}
}

// TestTransparencyReportZeroIdentifierAudit verifies Requirement 3:
// - obs.GenerateTransparencyReport reports MessageRecordsStored == 0 and SenderIdentitiesStored == 0.
// - Its JSON serialization contains zero handle strings, account keys, or personal identifiers even after
//   accounts, groups, handles, and pings are created and executed.
func TestTransparencyReportZeroIdentifierAudit(t *testing.T) {
	env, err := testenv.New()
	if err != nil {
		t.Fatalf("testenv.New: %v", err)
	}
	defer env.Close()

	ctx := context.Background()

	// 1. Seed accounts, personal handle, stream handle, groups, and pings
	acc1, tok1, err := env.CreateAccountAndSession(0x91)
	if err != nil {
		t.Fatalf("create account 1: %v", err)
	}
	_, tok2, err := env.CreateAccountAndSession(0x92)
	if err != nil {
		t.Fatalf("create account 2: %v", err)
	}
	_, tok3, err := env.CreateAccountAndSession(0x93)
	if err != nil {
		t.Fatalf("create account 3: %v", err)
	}

	persTarget := testenv.Handle("epersonal9191")
	if err := env.CreateHandle(acc1, persTarget, testenv.HandleKindPersonal, "SecretPersonal"); err != nil {
		t.Fatalf("create personal handle: %v", err)
	}

	streamTarget := testenv.Handle("estream919191")
	if err := env.CreateHandle(acc1, streamTarget, testenv.HandleKindStream, "StreamerHandle"); err != nil {
		t.Fatalf("create stream handle: %v", err)
	}

	// Create group and members
	_, invCode, ownerGroupTarget := createTestGroup(t, env, tok1, "transparency-group", "Owner")
	m2Target := joinTestGroup(t, env, tok2, invCode, "Member2")
	_ = joinTestGroup(t, env, tok3, invCode, "Member3")

	// Send pings across handles
	_, _ = sendPhase3Ping(t, env, tok2, persTarget.Raw())
	_, _ = sendPhase3Ping(t, env, tok3, streamTarget.Raw())
	_, _ = sendPhase3Ping(t, env, tok2, ownerGroupTarget)

	// Collect all identifiers created during the session
	sensitiveStrings := []string{
		persTarget.Raw(),
		streamTarget.Raw(),
		ownerGroupTarget,
		m2Target,
		invCode,
		tok1,
		tok2,
		tok3,
		"transparency-group",
		"SecretPersonal",
		"StreamerHandle",
	}

	// 2. Open SQLite directly and call GenerateTransparencyReport
	db, err := sql.Open("sqlite", env.DBPath)
	if err != nil {
		t.Fatalf("failed to open sqlite DB: %v", err)
	}
	defer db.Close()

	report, err := testenv.GenerateTransparencyReport(ctx, db)
	if err != nil {
		t.Fatalf("GenerateTransparencyReport failed: %v", err)
	}
	if report == nil {
		t.Fatalf("expected non-nil transparency report")
	}

	// Also verify method on Environment
	reportFromEnv, err := env.GenerateTransparencyReport(ctx)
	if err != nil {
		t.Fatalf("env.GenerateTransparencyReport failed: %v", err)
	}
	if reportFromEnv == nil {
		t.Fatalf("expected non-nil report from env")
	}

	// 3. Invariants 1 & 6 Architectural Impossibility Assertions:
	// Message records stored MUST be 0.
	// Sender identities stored MUST be 0.
	if report.MessageRecordsStored != 0 {
		t.Fatalf("CRITICAL INVARIANT 1 VIOLATION: transparency report shows MessageRecordsStored=%d, expected 0", report.MessageRecordsStored)
	}
	if report.SenderIdentitiesStored != 0 {
		t.Fatalf("CRITICAL INVARIANT 6 VIOLATION: transparency report shows SenderIdentitiesStored=%d, expected 0", report.SenderIdentitiesStored)
	}

	if report.ActiveAccountsCount < 3 {
		t.Fatalf("expected at least 3 active accounts, got %d", report.ActiveAccountsCount)
	}
	if report.ActiveHandlesCount < 2 {
		t.Fatalf("expected at least 2 active handles, got %d", report.ActiveHandlesCount)
	}
	if report.ActiveGroupsCount < 1 {
		t.Fatalf("expected at least 1 active group, got %d", report.ActiveGroupsCount)
	}

	// 4. JSON Serialization Zero-Identifier Audit:
	// Ensure JSON contains ZERO identifiers, handles, keys, tokens, or names.
	reportJSON, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("failed to marshal transparency report to JSON: %v", err)
	}
	jsonStr := string(reportJSON)

	for _, secret := range sensitiveStrings {
		if secret == "" {
			continue
		}
		if strings.Contains(jsonStr, secret) {
			t.Fatalf("CRITICAL INVARIANT 9 LEAK: transparency report JSON contains sensitive identifier %q: %s", secret, jsonStr)
		}
	}

	// Verify required structure keys
	requiredKeys := []string{
		`"region"`,
		`"message_records_stored"`,
		`"sender_identities_stored"`,
		`"active_accounts_count"`,
		`"active_handles_count"`,
		`"active_groups_count"`,
		`"generated_day"`,
	}
	for _, k := range requiredKeys {
		if !strings.Contains(jsonStr, k) {
			t.Fatalf("transparency report JSON missing required key %s: %s", k, jsonStr)
		}
	}
}
