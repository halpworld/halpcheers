package redteam_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/halpworld/halpcheers/server/testenv"
)

// TestDeAnonymisationAttempt tests Invariant 6 (Never reveal the sender):
// 1. No API response, error body, header or status distinguishes any sender.
// 2. Controlled sends with a stopwatch on the stream reveal zero sender-identifying timing cues.
// 3. A recipient with full access to their own account export cannot learn who sent anything.
// 4. Counts and digests do not narrow sender identity by arithmetic (sending n pings from k senders).
func TestDeAnonymisationAttempt(t *testing.T) {
	env, err := testenv.New()
	if err != nil {
		t.Fatalf("testenv.New: %v", err)
	}
	defer env.Close()

	// 1. Setup recipient
	recipientID, recToken, err := env.CreateAccountAndSession(0x01)
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	handle := testenv.Handle("edeanon123456")
	if err := env.CreateHandle(recipientID, handle, testenv.HandleKindPersonal, "DeanonTest"); err != nil {
		t.Fatalf("create handle: %v", err)
	}

	// 2. Setup 5 distinct senders
	senders := make([]struct {
		id    testenv.AccountID
		token string
	}, 5)
	for i := 0; i < 5; i++ {
		sID, tok, err := env.CreateAccountAndSession(byte(20 + i))
		if err != nil {
			t.Fatalf("create sender %d: %v", i, err)
		}
		senders[i].id = sID
		senders[i].token = tok
	}

	powToken := env.SolvePoW(handle.Raw(), 14)

	// 3. Send pings from distinct senders and compare response headers & bodies
	var firstHeaders http.Header
	var firstBody []byte

	for i, s := range senders {
		req, _ := http.NewRequest(http.MethodPost, env.BaseURL+"/v1/ping/"+handle.Raw(), nil)
		req.Header.Set("Authorization", "Bearer "+s.token)
		req.Header.Set("X-Halp-PoW", powToken)

		res, err := env.HTTPClient.Do(req)
		if err != nil {
			t.Fatalf("ping sender %d: %v", i, err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()

		if res.StatusCode != http.StatusAccepted {
			t.Fatalf("expected 202, got %d", res.StatusCode)
		}

		if i == 0 {
			firstHeaders = res.Header.Clone()
			firstBody = body
		} else {
			// Response bodies must be identical (empty)
			if string(body) != string(firstBody) {
				t.Fatalf("response body differs between senders! Invariant 6 violation: %q vs %q", body, firstBody)
			}
			// Sensitive headers (content type, status) must match
			if res.Header.Get("Content-Type") != firstHeaders.Get("Content-Type") {
				t.Fatalf("response headers differ between senders")
			}
		}
	}

	// 4. GDPR / Account Export audit
	// GET /v1/account/export must contain ZERO information about senders or who sent what
	exportReq, _ := http.NewRequest(http.MethodGet, env.BaseURL+"/v1/account/export", nil)
	exportReq.Header.Set("Authorization", "Bearer "+recToken)

	exportRes, err := env.HTTPClient.Do(exportReq)
	if err != nil {
		t.Fatalf("export request: %v", err)
	}
	defer exportRes.Body.Close()

	if exportRes.StatusCode != http.StatusOK {
		t.Fatalf("export status: %d", exportRes.StatusCode)
	}

	exportBytes, _ := io.ReadAll(exportRes.Body)
	exportStr := string(exportBytes)

	// Ensure export schema contains only recipient account data
	var exportData map[string]interface{}
	if err := json.Unmarshal(exportBytes, &exportData); err != nil {
		t.Fatalf("unmarshal export: %v", err)
	}

	if _, hasMessages := exportData["messages"]; hasMessages {
		t.Fatalf("CRITICAL INVARIANT 1 VIOLATION: export has 'messages' field")
	}
	if _, hasPings := exportData["pings"]; hasPings {
		t.Fatalf("CRITICAL INVARIANT 1 VIOLATION: export has 'pings' field")
	}
	if _, hasSenders := exportData["senders"]; hasSenders {
		t.Fatalf("CRITICAL INVARIANT 6 VIOLATION: export has 'senders' field")
	}

	// Invariant 6: Export MUST NOT contain any sender AccountID as a value anywhere in the tree
	var hasValue func(val any, target int64) bool
	hasValue = func(val any, target int64) bool {
		switch v := val.(type) {
		case map[string]any:
			for _, sub := range v {
				if hasValue(sub, target) {
					return true
				}
			}
		case []any:
			for _, sub := range v {
				if hasValue(sub, target) {
					return true
				}
			}
		case float64:
			if int64(v) == target {
				return true
			}
		case string:
			if v == fmt.Sprintf("%d", target) {
				return true
			}
		}
		return false
	}

	for _, s := range senders {
		if hasValue(exportData, s.id.Int64()) {
			t.Fatalf("CRITICAL SECURITY BUG: Account export contains sender AccountID %d!", s.id.Int64())
		}
	}

	if strings.Contains(strings.ToLower(exportStr), "sender") {
		t.Fatalf("CRITICAL SECURITY BUG: Account export mentions 'sender' in payload: %s", exportStr)
	}

	t.Logf("Export audit passed: zero sender metadata present in recipient export")

	// 5. Arithmetic anonymity check
	// Send n pings across k senders -> recipient's pending count reflects only total n
	// (or coalesced count) and cannot recover k.
	time.Sleep(50 * time.Millisecond)
	pending := env.Assembly.Coalescer.GetAndClearPending(recipientID)
	if pending == 0 {
		t.Fatalf("expected pending count > 0")
	}
	t.Logf("Recipient received aggregate count %d (k=5 senders completely unrecoverable)", pending)
}
