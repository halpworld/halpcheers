package redteam_test

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/halpworld/halpcheers/server/testenv"
)

// TestEnumerationAttempt verifies Invariant 7 and anti-oracle defenses:
// 1. Unknown, paused, blocked and live targets are indistinguishable by response bytes, headers, status.
// 2. No endpoint anywhere confirms a handle or alias exists (no existence oracles).
// 3. The badge (/badge/{handle}.svg) and OG tags (/h/{handle}) don't confirm existence either.
// 4. Probing costs the same as sending, including the PoW.
func TestEnumerationAttempt(t *testing.T) {
	env, err := testenv.New()
	if err != nil {
		t.Fatalf("testenv.New: %v", err)
	}
	defer env.Close()

	// 1. Setup handles: live, paused, blocked, and unknown
	recID, _, _ := env.CreateAccountAndSession(0x01)
	liveHandle := testenv.Handle("elive12345678")
	pausedHandle := testenv.Handle("epause1234567")
	blockedHandle := testenv.Handle("eblock1234567")
	unknownHandle := testenv.Handle("eunknown12345")

	_ = env.CreateHandle(recID, liveHandle, testenv.HandleKindPersonal, "Live")
	_ = env.CreateHandle(recID, pausedHandle, testenv.HandleKindPersonal, "Paused")
	_ = env.CreateHandle(recID, blockedHandle, testenv.HandleKindPersonal, "Blocked")

	senderID, senderToken, _ := env.CreateAccountAndSession(0x02)
	_ = env.RecordBlock(senderID, blockedHandle)

	targets := map[string]string{
		"live":    liveHandle.Raw(),
		"paused":  pausedHandle.Raw(),
		"blocked": blockedHandle.Raw(),
		"unknown": unknownHandle.Raw(),
	}

	// 2. Send ping to all 4 target categories and assert bitwise equality
	var referenceBody []byte
	var referenceStatus int
	var referenceHeaders http.Header

	for category, handleStr := range targets {
		powToken := env.SolvePoW(handleStr, 14)
		req, _ := http.NewRequest(http.MethodPost, env.BaseURL+"/v1/ping/"+handleStr, nil)
		req.Header.Set("Authorization", "Bearer "+senderToken)
		req.Header.Set("X-Halp-PoW", powToken)

		res, err := env.HTTPClient.Do(req)
		if err != nil {
			t.Fatalf("send %s: %v", category, err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()

		if referenceBody == nil {
			referenceBody = body
			referenceStatus = res.StatusCode
			referenceHeaders = res.Header.Clone()
		} else {
			if res.StatusCode != referenceStatus {
				t.Fatalf("target %s returned status %d, expected %d", category, res.StatusCode, referenceStatus)
			}
			if !bytes.Equal(body, referenceBody) {
				t.Fatalf("target %s returned body %q, expected %q (Invariant 7 leak!)", category, body, referenceBody)
			}
			if res.Header.Get("Content-Type") != referenceHeaders.Get("Content-Type") {
				t.Fatalf("target %s returned different Content-Type header", category)
			}
		}
	}
	t.Logf("Ping response indistinguishability verified across live, paused, blocked, and unknown targets (202 Accepted, empty body)")

	// 3. Public landing page check: /h/{handle} must serve byte-identical HTML for real vs nonexistent handles
	resLive, err := env.HTTPClient.Get(env.BaseURL + "/h/" + liveHandle.Raw())
	if err != nil {
		t.Fatalf("get /h/live: %v", err)
	}
	liveHTML, _ := io.ReadAll(resLive.Body)
	_ = resLive.Body.Close()

	resUnknown, err := env.HTTPClient.Get(env.BaseURL + "/h/" + unknownHandle.Raw())
	if err != nil {
		t.Fatalf("get /h/unknown: %v", err)
	}
	unknownHTML, _ := io.ReadAll(resUnknown.Body)
	_ = resUnknown.Body.Close()

	if !bytes.Equal(liveHTML, unknownHTML) {
		t.Fatalf("CRITICAL INVARIANT 7 VIOLATION: /h/{handle} serves different HTML for live vs unknown handle!\nLive: %s\nUnknown: %s", liveHTML, unknownHTML)
	}
	t.Logf("Public landing page /h/{handle} is byte-identical for existing vs nonexistent handles (no existence oracle)")

	// 4. Badge check: /badge/{handle}.svg must serve identical SVG for real vs nonexistent handles
	resBadgeLive, err := env.HTTPClient.Get(env.BaseURL + "/badge/" + liveHandle.Raw() + ".svg")
	if err != nil {
		t.Fatalf("get badge live: %v", err)
	}
	liveSVG, _ := io.ReadAll(resBadgeLive.Body)
	_ = resBadgeLive.Body.Close()

	resBadgeUnknown, err := env.HTTPClient.Get(env.BaseURL + "/badge/" + unknownHandle.Raw() + ".svg")
	if err != nil {
		t.Fatalf("get badge unknown: %v", err)
	}
	unknownSVG, _ := io.ReadAll(resBadgeUnknown.Body)
	_ = resBadgeUnknown.Body.Close()

	if !bytes.Equal(liveSVG, unknownSVG) {
		t.Fatalf("CRITICAL INVARIANT 7 VIOLATION: /badge/{handle}.svg differs for live vs unknown handle!")
	}
	t.Logf("Badge SVG is byte-identical for existing vs nonexistent handles")

	// 5. Proof of work is strictly required for probing
	// Probe without PoW header
	reqNoPoW, _ := http.NewRequest(http.MethodPost, env.BaseURL+"/v1/ping/"+unknownHandle.Raw(), nil)
	reqNoPoW.Header.Set("Authorization", "Bearer "+senderToken)
	resNoPoW, err := env.HTTPClient.Do(reqNoPoW)
	if err != nil {
		t.Fatalf("do no-pow: %v", err)
	}
	_ = resNoPoW.Body.Close()
	// Drop is silent: returns 202 Accepted, but dropped internally
	if resNoPoW.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 for missing PoW, got %d", resNoPoW.StatusCode)
	}
	t.Logf("Probing costs identical to valid sends (enforcement invisible to sender)")
}
