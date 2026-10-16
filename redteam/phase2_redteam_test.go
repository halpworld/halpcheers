package redteam

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/halpworld/halpcheers/server/testenv"
	_ "modernc.org/sqlite"
)

// Helper: send a ping request to target and return response and body bytes.
func sendRedteamPing(t *testing.T, env *testenv.Environment, tok string, target string) (*http.Response, []byte) {
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

// Helper: query /v1/pending count for the account associated with tok.
func fetchPendingCount(t *testing.T, env *testenv.Environment, tok string) int {
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

// Helper: wait for pending count to match expected, or timeout.
func waitForPendingCount(t *testing.T, env *testenv.Environment, tok string, expected int, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	lastSeen := 0
	for time.Now().Before(deadline) {
		lastSeen = fetchPendingCount(t, env, tok)
		if lastSeen == expected {
			return lastSeen
		}
		time.Sleep(10 * time.Millisecond)
	}
	return lastSeen
}

// Helper: create a group and return (groupID, inviteCode, ownerHandle).
func createTestGroup(t *testing.T, env *testenv.Environment, tok string, name string, dName string) (int64, string, string) {
	t.Helper()
	bodyBytes, _ := json.Marshal(map[string]string{
		"name":         name,
		"display_name": dName,
		"note":         "redteam-note",
	})
	req, err := http.NewRequest(http.MethodPost, env.BaseURL+"/v1/groups", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("failed to create group request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	res, err := env.HTTPClient.Do(req)
	if err != nil {
		t.Fatalf("create group failed: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 201 Created from create group, got %d: %s", res.StatusCode, string(respBody))
	}

	var created struct {
		GroupID    int64  `json:"group_id"`
		InviteCode string `json:"invite_code"`
		MyHandle   string `json:"my_handle"`
		MinSize    int    `json:"min_size"`
		Active     bool   `json:"active"`
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatalf("failed to decode create group response: %v", err)
	}
	return created.GroupID, created.InviteCode, created.MyHandle
}

// Helper: join a group and return member group handle.
func joinTestGroup(t *testing.T, env *testenv.Environment, tok string, invCode string, dName string) string {
	t.Helper()
	bodyBytes, _ := json.Marshal(map[string]string{
		"invite_code":  invCode,
		"display_name": dName,
		"note":         "redteam-join",
	})
	req, err := http.NewRequest(http.MethodPost, env.BaseURL+"/v1/groups/join", bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("failed to create join group request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	res, err := env.HTTPClient.Do(req)
	if err != nil {
		t.Fatalf("join group failed: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(res.Body)
		t.Fatalf("expected 200 OK from join group, got %d: %s", res.StatusCode, string(respBody))
	}

	var joined struct {
		GroupID  int64  `json:"group_id"`
		MyHandle string `json:"my_handle"`
		MinSize  int    `json:"min_size"`
		Active   bool   `json:"active"`
	}
	if err := json.NewDecoder(res.Body).Decode(&joined); err != nil {
		t.Fatalf("failed to decode join group response: %v", err)
	}
	return joined.MyHandle
}

// TestContactsPaddingAndConcurrencyOracle verifies Requirement 1:
// - PUT /v1/contacts rejects unpadded blobs (e.g. 100 bytes, 4095 bytes) with 400.
// - PUT /v1/contacts rejects oversized blobs (> 64 KiB) with 400.
// - Enforces If-Match optimistic concurrency (412 on stale version, increments version on match).
// - Stores/returns opaque ciphertext without leaking plaintext to disk or response.
func TestContactsPaddingAndConcurrencyOracle(t *testing.T) {
	env, err := testenv.New()
	if err != nil {
		t.Fatalf("testenv.New: %v", err)
	}
	defer env.Close()

	_, tok, err := env.CreateAccountAndSession(0x31)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	// 1. Initial GET /v1/contacts returns version 0 and empty blob
	getReq, _ := http.NewRequest(http.MethodGet, env.BaseURL+"/v1/contacts", nil)
	getReq.Header.Set("Authorization", "Bearer "+tok)
	getRes, err := env.HTTPClient.Do(getReq)
	if err != nil {
		t.Fatalf("initial get contacts: %v", err)
	}
	if getRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on initial GET /v1/contacts, got %d", getRes.StatusCode)
	}
	var initData struct {
		Version int64  `json:"version"`
		Blob    string `json:"blob"`
	}
	_ = json.NewDecoder(getRes.Body).Decode(&initData)
	_ = getRes.Body.Close()
	if initData.Version != 0 || initData.Blob != "" {
		t.Fatalf("expected initial contacts version=0 and empty blob, got version=%d blob=%q", initData.Version, initData.Blob)
	}

	// 2. Reject unpadded blobs with 400 Bad Request
	unpaddedSizes := []int{1, 100, 500, 4095, 4097, 8191}
	for _, sz := range unpaddedSizes {
		raw := make([]byte, sz)
		for i := range raw {
			raw[i] = byte(i % 255)
		}
		encoded := base64.StdEncoding.EncodeToString(raw)
		body, _ := json.Marshal(map[string]string{"blob": encoded})

		putReq, _ := http.NewRequest(http.MethodPut, env.BaseURL+"/v1/contacts", bytes.NewReader(body))
		putReq.Header.Set("Authorization", "Bearer "+tok)
		putReq.Header.Set("If-Match", "0")

		putRes, err := env.HTTPClient.Do(putReq)
		if err != nil {
			t.Fatalf("put unpadded size %d: %v", sz, err)
		}
		_ = putRes.Body.Close()
		if putRes.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for unpadded blob of %d bytes, got %d", sz, putRes.StatusCode)
		}
	}

	// Also reject empty or malformed blob payloads
	malformedBodies := []string{
		`{"blob":""}`,
		`{"blob":"not-valid-base64-content!@#$"}`,
		`{}`,
	}
	for _, mb := range malformedBodies {
		putReq, _ := http.NewRequest(http.MethodPut, env.BaseURL+"/v1/contacts", strings.NewReader(mb))
		putReq.Header.Set("Authorization", "Bearer "+tok)
		putReq.Header.Set("If-Match", "0")

		putRes, err := env.HTTPClient.Do(putReq)
		if err != nil {
			t.Fatalf("put malformed body: %v", err)
		}
		_ = putRes.Body.Close()
		if putRes.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for malformed body %s, got %d", mb, putRes.StatusCode)
		}
	}

	// 3. Reject oversized blobs (> 64 KiB ceiling) with 400 Bad Request
	oversizedSizes := []int{
		65 * 1024,                  // 65 KiB unpadded
		68 * 1024,                  // 68 KiB unpadded
		64*1024 + 4096,             // 68 KiB 4 KiB-padded
		64*1024 + 4096 + 28,        // 68 KiB padded + GCM overhead
		128 * 1024,                 // 128 KiB
	}
	for _, sz := range oversizedSizes {
		raw := make([]byte, sz)
		encoded := base64.StdEncoding.EncodeToString(raw)
		body, _ := json.Marshal(map[string]string{"blob": encoded})

		putReq, _ := http.NewRequest(http.MethodPut, env.BaseURL+"/v1/contacts", bytes.NewReader(body))
		putReq.Header.Set("Authorization", "Bearer "+tok)
		putReq.Header.Set("If-Match", "0")

		putRes, err := env.HTTPClient.Do(putReq)
		if err != nil {
			t.Fatalf("put oversized size %d: %v", sz, err)
		}
		_ = putRes.Body.Close()
		if putRes.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for oversized blob of %d bytes, got %d", sz, putRes.StatusCode)
		}
	}

	// 4. Concurrency checks: missing If-Match or invalid If-Match returns 400
	validCiphertextRaw := make([]byte, 4096+28)
	secretPlaintextMarker := "SECRET_PLAINTEXT_CONTACT_CARD_ALICE_BOB"
	for i := range validCiphertextRaw {
		validCiphertextRaw[i] = byte((i * 37) % 256)
	}
	validEncodedBlob := base64.StdEncoding.EncodeToString(validCiphertextRaw)
	validBody, _ := json.Marshal(map[string]string{"blob": validEncodedBlob})

	// Missing If-Match
	reqNoMatch, _ := http.NewRequest(http.MethodPut, env.BaseURL+"/v1/contacts", bytes.NewReader(validBody))
	reqNoMatch.Header.Set("Authorization", "Bearer "+tok)
	resNoMatch, err := env.HTTPClient.Do(reqNoMatch)
	if err != nil {
		t.Fatalf("put without If-Match: %v", err)
	}
	_ = resNoMatch.Body.Close()
	if resNoMatch.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for missing If-Match, got %d", resNoMatch.StatusCode)
	}

	// Invalid If-Match
	reqBadMatch, _ := http.NewRequest(http.MethodPut, env.BaseURL+"/v1/contacts", bytes.NewReader(validBody))
	reqBadMatch.Header.Set("Authorization", "Bearer "+tok)
	reqBadMatch.Header.Set("If-Match", `"invalid"`)
	resBadMatch, err := env.HTTPClient.Do(reqBadMatch)
	if err != nil {
		t.Fatalf("put with invalid If-Match: %v", err)
	}
	_ = resBadMatch.Body.Close()
	if resBadMatch.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for invalid If-Match, got %d", resBadMatch.StatusCode)
	}

	// 5. Enforce 412 Precondition Failed on stale If-Match version
	reqStale, _ := http.NewRequest(http.MethodPut, env.BaseURL+"/v1/contacts", bytes.NewReader(validBody))
	reqStale.Header.Set("Authorization", "Bearer "+tok)
	reqStale.Header.Set("If-Match", "1") // Current version is 0
	resStale, err := env.HTTPClient.Do(reqStale)
	if err != nil {
		t.Fatalf("put with stale If-Match: %v", err)
	}
	_ = resStale.Body.Close()
	if resStale.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412 Precondition Failed for stale If-Match, got %d", resStale.StatusCode)
	}

	// 6. Successful PUT with matching If-Match: 0 -> increments version to 1
	reqOk, _ := http.NewRequest(http.MethodPut, env.BaseURL+"/v1/contacts", bytes.NewReader(validBody))
	reqOk.Header.Set("Authorization", "Bearer "+tok)
	reqOk.Header.Set("If-Match", "0")
	resOk, err := env.HTTPClient.Do(reqOk)
	if err != nil {
		t.Fatalf("put with matching If-Match: %v", err)
	}
	if resOk.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resOk.Body)
		t.Fatalf("expected 200 OK on first valid PUT, got %d: %s", resOk.StatusCode, string(bodyBytes))
	}
	var putResData struct {
		Version int64 `json:"version"`
	}
	_ = json.NewDecoder(resOk.Body).Decode(&putResData)
	_ = resOk.Body.Close()
	if putResData.Version != 1 {
		t.Fatalf("expected version 1 after first PUT, got %d", putResData.Version)
	}

	// Stale If-Match: 0 now fails with 412 Precondition Failed
	reqConflict, _ := http.NewRequest(http.MethodPut, env.BaseURL+"/v1/contacts", bytes.NewReader(validBody))
	reqConflict.Header.Set("Authorization", "Bearer "+tok)
	reqConflict.Header.Set("If-Match", "0")
	resConflict, err := env.HTTPClient.Do(reqConflict)
	if err != nil {
		t.Fatalf("put conflict check: %v", err)
	}
	_ = resConflict.Body.Close()
	if resConflict.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("expected 412 Precondition Failed when reusing If-Match: 0, got %d", resConflict.StatusCode)
	}

	// Update to version 2 using matching If-Match: 1
	validCiphertext2 := make([]byte, 8192)
	for i := range validCiphertext2 {
		validCiphertext2[i] = byte((i * 13) % 256)
	}
	validEncodedBlob2 := base64.StdEncoding.EncodeToString(validCiphertext2)
	validBody2, _ := json.Marshal(map[string]string{"blob": validEncodedBlob2})

	reqOk2, _ := http.NewRequest(http.MethodPut, env.BaseURL+"/v1/contacts", bytes.NewReader(validBody2))
	reqOk2.Header.Set("Authorization", "Bearer "+tok)
	reqOk2.Header.Set("If-Match", "1")
	resOk2, err := env.HTTPClient.Do(reqOk2)
	if err != nil {
		t.Fatalf("put second version: %v", err)
	}
	if resOk2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on second PUT, got %d", resOk2.StatusCode)
	}
	_ = resOk2.Body.Close()

	// 7. Verify GET /v1/contacts returns opaque ciphertext identical to PUT blob
	getReq2, _ := http.NewRequest(http.MethodGet, env.BaseURL+"/v1/contacts", nil)
	getReq2.Header.Set("Authorization", "Bearer "+tok)
	getRes2, err := env.HTTPClient.Do(getReq2)
	if err != nil {
		t.Fatalf("get contacts v2: %v", err)
	}
	defer getRes2.Body.Close()
	if getRes2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from GET /v1/contacts, got %d", getRes2.StatusCode)
	}
	var getResult struct {
		Version int64  `json:"version"`
		Blob    string `json:"blob"`
	}
	if err := json.NewDecoder(getRes2.Body).Decode(&getResult); err != nil {
		t.Fatalf("failed to decode get response: %v", err)
	}
	if getResult.Version != 2 {
		t.Fatalf("expected version 2 on GET, got %d", getResult.Version)
	}
	if getResult.Blob != validEncodedBlob2 {
		t.Fatalf("ciphertext blob mismatch between PUT and GET")
	}

	// 8. Verify SQLite directly: Assert blob in database matches ciphertext and no plaintext leak
	db, err := sql.Open("sqlite", env.DBPath)
	if err != nil {
		t.Fatalf("failed to open sqlite DB: %v", err)
	}
	defer db.Close()

	var dbBlob []byte
	var dbVersion int64
	err = db.QueryRowContext(context.Background(),
		"SELECT blob, version FROM contacts_blob LIMIT 1",
	).Scan(&dbBlob, &dbVersion)
	if err != nil {
		t.Fatalf("failed to query contacts_blob: %v", err)
	}
	if dbVersion != 2 {
		t.Fatalf("expected db version 2, got %d", dbVersion)
	}
	if !bytes.Equal(dbBlob, validCiphertext2) {
		t.Fatalf("stored database blob bytes do not match expected ciphertext")
	}
	if bytes.Contains(dbBlob, []byte(secretPlaintextMarker)) {
		t.Fatalf("critical privacy leak: plaintext marker found in contacts_blob table")
	}

	// 9. DELETE /v1/contacts removes blob and returns 204 No Content
	delReq, _ := http.NewRequest(http.MethodDelete, env.BaseURL+"/v1/contacts", nil)
	delReq.Header.Set("Authorization", "Bearer "+tok)
	delRes, err := env.HTTPClient.Do(delReq)
	if err != nil {
		t.Fatalf("delete contacts: %v", err)
	}
	_ = delRes.Body.Close()
	if delRes.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 No Content on DELETE /v1/contacts, got %d", delRes.StatusCode)
	}

	// Subsequent GET returns version 0 and empty blob
	getReq3, _ := http.NewRequest(http.MethodGet, env.BaseURL+"/v1/contacts", nil)
	getReq3.Header.Set("Authorization", "Bearer "+tok)
	getRes3, err := env.HTTPClient.Do(getReq3)
	if err != nil {
		t.Fatalf("get contacts after delete: %v", err)
	}
	defer getRes3.Body.Close()
	var delResult struct {
		Version int64  `json:"version"`
		Blob    string `json:"blob"`
	}
	_ = json.NewDecoder(getRes3.Body).Decode(&delResult)
	if delResult.Version != 0 || delResult.Blob != "" {
		t.Fatalf("expected version 0 and empty blob after delete, got %+v", delResult)
	}
}

// TestGroupMinSizeAndNonMemberUniformity verifies Requirement 2:
// - Pinging a group handle when group has < 5 members (groups.min_size = 5)
// - Pinging a group handle as an unauthenticated or non-member sender
// - Exceeding the 10 distinct group members/day spray cap
// All return an identical 202 Accepted and zero enqueued deliveries (Invariant 7 uniformity).
func TestGroupMinSizeAndNonMemberUniformity(t *testing.T) {
	env, err := testenv.New()
	if err != nil {
		t.Fatalf("testenv.New: %v", err)
	}
	defer env.Close()

	// Seed 14 distinct accounts
	// Account 1: Group Owner
	// Accounts 2..12: Group Members (11 members)
	// Account 13: Non-member sender
	tokens := make([]string, 14)
	for i := 1; i <= 13; i++ {
		_, tok, err := env.CreateAccountAndSession(byte(40 + i))
		if err != nil {
			t.Fatalf("create account %d: %v", i, err)
		}
		tokens[i] = tok
	}

	ownerToken := tokens[1]
	member2Token := tokens[2]
	nonMemberToken := tokens[13]

	// 1. Owner creates group "#redteam-squad"
	gID, invCode, ownerTarget := createTestGroup(t, env, ownerToken, "redteam-squad", "Owner")
	if gID <= 0 || ownerTarget == "" {
		t.Fatalf("invalid group created: id=%d target=%s", gID, ownerTarget)
	}

	memberTargets := make([]string, 13)

	// 2. Members 2, 3, 4 join (total 4 members in group: Owner + 3 < 5 min_size)
	memberTargets[2] = joinTestGroup(t, env, member2Token, invCode, "Member2")
	memberTargets[3] = joinTestGroup(t, env, tokens[3], invCode, "Member3")
	memberTargets[4] = joinTestGroup(t, env, tokens[4], invCode, "Member4")

	// Drain any initial pending digests for owner
	_ = fetchPendingCount(t, env, ownerToken)

	// -------------------------------------------------------------------------
	// Scenario 1: Group has < 5 members (4 members). Member 2 pings owner group handle.
	// -------------------------------------------------------------------------
	resBelowMin, bodyBelowMin := sendRedteamPing(t, env, member2Token, ownerTarget)
	if resBelowMin.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted on below-min-size group ping, got %d", resBelowMin.StatusCode)
	}

	// Zero enqueued deliveries: wait and assert pending count is 0
	time.Sleep(50 * time.Millisecond)
	if n := fetchPendingCount(t, env, ownerToken); n != 0 {
		t.Fatalf("expected 0 deliveries for below min_size group, got %d", n)
	}

	// -------------------------------------------------------------------------
	// Member 5 joins -> group now reaches min_size = 5 (active = true)!
	// -------------------------------------------------------------------------
	memberTargets[5] = joinTestGroup(t, env, tokens[5], invCode, "Member5")

	// Verify that an authorized member ping against the active group DOES deliver!
	resActive, _ := sendRedteamPing(t, env, member2Token, ownerTarget)
	if resActive.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted for active group member ping, got %d", resActive.StatusCode)
	}
	received := waitForPendingCount(t, env, ownerToken, 1, 200*time.Millisecond)
	if received != 1 {
		t.Fatalf("expected 1 enqueued delivery for active group ping, got %d", received)
	}

	// -------------------------------------------------------------------------
	// Scenario 2: Non-member sender pings active group handle.
	// Account 13 is authenticated, but NOT a member of this group.
	// -------------------------------------------------------------------------
	resNonMember, bodyNonMember := sendRedteamPing(t, env, nonMemberToken, ownerTarget)
	if resNonMember.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted for non-member ping to group, got %d", resNonMember.StatusCode)
	}

	// Zero enqueued deliveries to owner
	time.Sleep(50 * time.Millisecond)
	if n := fetchPendingCount(t, env, ownerToken); n != 0 {
		t.Fatalf("expected 0 deliveries for non-member ping to group, got %d", n)
	}

	// Unauthenticated sender check: missing Authorization token returns 401 Unauthorized
	resNoAuth, _ := sendRedteamPing(t, env, "", ownerTarget)
	if resNoAuth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for unauthenticated ping, got %d", resNoAuth.StatusCode)
	}

	// -------------------------------------------------------------------------
	// Scenario 3: Exceeding 10 distinct group members/day spray cap.
	// Add Members 6..12 to the group so the group has 12 members total.
	// -------------------------------------------------------------------------
	for i := 6; i <= 12; i++ {
		memberTargets[i] = joinTestGroup(t, env, tokens[i], invCode, "Member"+strconv.Itoa(i))
	}

	// Member 2 pings 10 distinct group members (Owner + Members 3..11):
	// Target 1: Owner
	// Targets 2..10: Members 3..11 (total 10 distinct members)
	targetsToPing := []string{ownerTarget}
	for i := 3; i <= 11; i++ {
		targetsToPing = append(targetsToPing, memberTargets[i])
	}
	if len(targetsToPing) != 10 {
		t.Fatalf("expected 10 distinct targets, got %d", len(targetsToPing))
	}

	for idx, tVal := range targetsToPing {
		rSpray, _ := sendRedteamPing(t, env, member2Token, tVal)
		if rSpray.StatusCode != http.StatusAccepted {
			t.Fatalf("expected 202 for distinct member %d ping, got %d", idx+1, rSpray.StatusCode)
		}
	}

	// Clear any pending count for Member 12
	_ = fetchPendingCount(t, env, tokens[12])

	// 11th distinct group member pinged by Member 2 is Member 12:
	// This exceeds the 10 distinct group members/24h spray cap!
	resSprayExceeded, bodySprayExceeded := sendRedteamPing(t, env, member2Token, memberTargets[12])
	if resSprayExceeded.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted on spray cap exceeded ping, got %d", resSprayExceeded.StatusCode)
	}

	// Zero enqueued deliveries to Member 12: suppressed by guard spray cap!
	time.Sleep(50 * time.Millisecond)
	if n := fetchPendingCount(t, env, tokens[12]); n != 0 {
		t.Fatalf("expected 0 deliveries for spray-capped ping, got %d", n)
	}

	// -------------------------------------------------------------------------
	// Invariant 7 Anti-Oracle Uniformity Assertions:
	// Response status, headers, and body must be strictly identical across all 3 non-delivery scenarios:
	// 1) Below min_size (< 5)
	// 2) Non-member sender
	// 3) Spray cap exceeded (> 10 distinct/day)
	// -------------------------------------------------------------------------
	if resBelowMin.StatusCode != http.StatusAccepted ||
		resNonMember.StatusCode != http.StatusAccepted ||
		resSprayExceeded.StatusCode != http.StatusAccepted {
		t.Fatalf("status codes differ between suppression cases")
	}

	if !bytes.Equal(bodyBelowMin, bodyNonMember) {
		t.Fatalf("Invariant 7 violation: body for below-min-size differs from non-member: %q vs %q", bodyBelowMin, bodyNonMember)
	}
	if !bytes.Equal(bodyNonMember, bodySprayExceeded) {
		t.Fatalf("Invariant 7 violation: body for non-member differs from spray-capped: %q vs %q", bodyNonMember, bodySprayExceeded)
	}

	if resBelowMin.Header.Get("Content-Type") != resNonMember.Header.Get("Content-Type") ||
		resNonMember.Header.Get("Content-Type") != resSprayExceeded.Header.Get("Content-Type") {
		t.Fatalf("Invariant 7 violation: Content-Type header differs between suppression cases")
	}
}

// TestGroupLeaveKickHandleBurnPermanence verifies Requirement 3:
// - When a member leaves or is removed from a group, their group-scoped handle is permanently burned.
// - Burned handles never resolve again, even if group has >= 5 members.
// - Pinging a burned handle returns uniform 202 Accepted with zero enqueued deliveries.
func TestGroupLeaveKickHandleBurnPermanence(t *testing.T) {
	env, err := testenv.New()
	if err != nil {
		t.Fatalf("testenv.New: %v", err)
	}
	defer env.Close()

	// Seed 8 accounts: Owner + Members 2..8
	tokens := make([]string, 9)
	for i := 1; i <= 8; i++ {
		_, tok, err := env.CreateAccountAndSession(byte(70 + i))
		if err != nil {
			t.Fatalf("create account %d: %v", i, err)
		}
		tokens[i] = tok
	}

	ownerToken := tokens[1]
	member2Token := tokens[2]
	member5Token := tokens[5]
	member6Token := tokens[6]

	// 1. Create group and join members so group has 7 members (> min_size 5)
	gID, invCode, _ := createTestGroup(t, env, ownerToken, "burn-squad", "Owner")
	gidStr := strconv.FormatInt(gID, 10)

	_ = joinTestGroup(t, env, member2Token, invCode, "Member2")
	_ = joinTestGroup(t, env, tokens[3], invCode, "Member3")
	_ = joinTestGroup(t, env, tokens[4], invCode, "Member4")
	m5Target := joinTestGroup(t, env, member5Token, invCode, "Member5")
	m6Target := joinTestGroup(t, env, member6Token, invCode, "Member6")
	_ = joinTestGroup(t, env, tokens[7], invCode, "Member7")

	// Verify Member 6 is currently reachable while active in group
	_ = fetchPendingCount(t, env, member6Token)
	rPre, _ := sendRedteamPing(t, env, member2Token, m6Target)
	if rPre.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted on pre-leave ping, got %d", rPre.StatusCode)
	}
	if n := waitForPendingCount(t, env, member6Token, 1, 200*time.Millisecond); n != 1 {
		t.Fatalf("expected 1 delivery to active member 6 before leaving, got %d", n)
	}

	// -------------------------------------------------------------------------
	// Member 6 Leaves Group: DELETE /v1/groups/{id}/membership
	// -------------------------------------------------------------------------
	leaveReq, err := http.NewRequest(http.MethodDelete, env.BaseURL+"/v1/groups/"+gidStr+"/membership", nil)
	if err != nil {
		t.Fatalf("create leave request: %v", err)
	}
	leaveReq.Header.Set("Authorization", "Bearer "+member6Token)
	leaveRes, err := env.HTTPClient.Do(leaveReq)
	if err != nil {
		t.Fatalf("leave group failed: %v", err)
	}
	_ = leaveRes.Body.Close()
	if leaveRes.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 No Content on leave group, got %d", leaveRes.StatusCode)
	}

	// Group still has 6 members (Owner, M2, M3, M4, M5, M7) which is >= 5 min_size!
	// 2. Direct SQLite schema inspection: Assert Member 6 handle is permanently burned
	db, err := sql.Open("sqlite", env.DBPath)
	if err != nil {
		t.Fatalf("failed to open sqlite DB: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	var handleCount int
	err = db.QueryRowContext(ctx, "SELECT COUNT(1) FROM handles WHERE handle = ?", m6Target).Scan(&handleCount)
	if err != nil || handleCount != 0 {
		t.Fatalf("expected handle %s to be deleted from handles table, count=%d err=%v", m6Target, handleCount, err)
	}

	var memberCount int
	err = db.QueryRowContext(ctx, "SELECT COUNT(1) FROM group_members WHERE handle = ?", m6Target).Scan(&memberCount)
	if err != nil || memberCount != 0 {
		t.Fatalf("expected member entry for %s to be deleted from group_members, count=%d", m6Target, memberCount)
	}

	if env.Assembly != nil && env.Assembly.Region != nil {
		if !env.Assembly.Region.IsBurned(testenv.Handle(m6Target)) {
			t.Fatalf("expected region service to mark handle %s as burned", m6Target)
		}
	}

	// 3. Pinging Member 6's burned handle: returns 202 Accepted (Invariant 7) but zero deliveries!
	_ = fetchPendingCount(t, env, member6Token)
	rBurned, _ := sendRedteamPing(t, env, member2Token, m6Target)
	if rBurned.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted on burned handle ping, got %d", rBurned.StatusCode)
	}
	time.Sleep(50 * time.Millisecond)
	if n := fetchPendingCount(t, env, member6Token); n != 0 {
		t.Fatalf("expected 0 deliveries for burned handle ping, got %d", n)
	}

	// 4. Member 6 re-joins the group (receives a NEW handle)
	m6NewTarget := joinTestGroup(t, env, member6Token, invCode, "Member6Rejoined")
	if m6NewTarget == m6Target {
		t.Fatalf("critical error: new handle %s re-used burned handle %s", m6NewTarget, m6Target)
	}

	// Ping the OLD burned handle again -> MUST still be burned and never resolve!
	_ = fetchPendingCount(t, env, member6Token)
	rOldBurned, _ := sendRedteamPing(t, env, member2Token, m6Target)
	if rOldBurned.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted on old burned handle ping, got %d", rOldBurned.StatusCode)
	}
	time.Sleep(50 * time.Millisecond)
	if n := fetchPendingCount(t, env, member6Token); n != 0 {
		t.Fatalf("expected 0 deliveries for old burned handle ping after re-join, got %d", n)
	}

	// Ping the NEW handle -> delivery succeeds!
	rNew, _ := sendRedteamPing(t, env, member2Token, m6NewTarget)
	if rNew.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted on new handle ping, got %d", rNew.StatusCode)
	}
	if n := waitForPendingCount(t, env, member6Token, 1, 200*time.Millisecond); n != 1 {
		t.Fatalf("expected 1 delivery to member 6 on new handle, got %d", n)
	}

	// -------------------------------------------------------------------------
	// Owner Kicks / Removes Member 5: DELETE /v1/groups/{id}/members/{member}
	// -------------------------------------------------------------------------
	kickReq, err := http.NewRequest(http.MethodDelete, env.BaseURL+"/v1/groups/"+gidStr+"/members/"+m5Target, nil)
	if err != nil {
		t.Fatalf("create kick request: %v", err)
	}
	kickReq.Header.Set("Authorization", "Bearer "+ownerToken)
	kickRes, err := env.HTTPClient.Do(kickReq)
	if err != nil {
		t.Fatalf("kick member failed: %v", err)
	}
	_ = kickRes.Body.Close()
	if kickRes.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 No Content on remove member, got %d", kickRes.StatusCode)
	}

	// Verify Member 5 handle is permanently burned
	var handleCount5 int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(1) FROM handles WHERE handle = ?", m5Target).Scan(&handleCount5)
	if handleCount5 != 0 {
		t.Fatalf("expected kicked handle %s to be deleted from handles table", m5Target)
	}

	if env.Assembly != nil && env.Assembly.Region != nil {
		if !env.Assembly.Region.IsBurned(testenv.Handle(m5Target)) {
			t.Fatalf("expected region service to mark kicked handle %s as burned", m5Target)
		}
	}

	// Pinging Member 5's kicked handle: returns 202 Accepted and zero deliveries!
	_ = fetchPendingCount(t, env, member5Token)
	rKicked, _ := sendRedteamPing(t, env, member2Token, m5Target)
	if rKicked.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted on kicked handle ping, got %d", rKicked.StatusCode)
	}
	time.Sleep(50 * time.Millisecond)
	if n := fetchPendingCount(t, env, member5Token); n != 0 {
		t.Fatalf("expected 0 deliveries for kicked burned handle ping, got %d", n)
	}
}
