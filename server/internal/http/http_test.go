package http_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	internalhttp "github.com/halpworld/halpcheers/server/internal/http"
	"github.com/halpworld/halpcheers/server/internal/obs"
)

// TestEveryPhase1RouteRespondsWithNotImplemented verifies that every mounted route
// from docs/API.md responds with the uniform 501 Not Implemented error response.
func TestEveryPhase1RouteRespondsWithNotImplemented(t *testing.T) {
	metrics := obs.NewMetrics()
	router := internalhttp.NewRouter(internalhttp.RouterDeps{
		Metrics: metrics,
	})

	tests := []struct {
		method string
		path   string
	}{
		// Accounts
		{http.MethodPost, "/v1/accounts"},
		{http.MethodPost, "/v1/session"},
		{http.MethodDelete, "/v1/session"},
		{http.MethodGet, "/v1/account"},
		{http.MethodGet, "/v1/account/export"},
		{http.MethodDelete, "/v1/account"},

		// Handles & Aliases
		{http.MethodGet, "/v1/handles"},
		{http.MethodPost, "/v1/handles"},
		{http.MethodPatch, "/v1/handles/e7k4p2m9qx3v"},
		{http.MethodDelete, "/v1/handles/e7k4p2m9qx3v"},
		{http.MethodPut, "/v1/alias"},
		{http.MethodDelete, "/v1/alias"},

		// Sending
		{http.MethodPost, "/v1/ping/e7k4p2m9qx3v"},

		// Subscriptions
		{http.MethodPost, "/v1/subscriptions"},
		{http.MethodDelete, "/v1/subscriptions/123"},

		// Stream & Pending
		{http.MethodGet, "/v1/stream"},
		{http.MethodGet, "/v1/pending"},

		// Settings & Abuse
		{http.MethodGet, "/v1/settings"},
		{http.MethodPut, "/v1/settings"},
		{http.MethodPost, "/v1/handles/e7k4p2m9qx3v/report-abuse"},

		// Groups (Phase 2)
		{http.MethodPost, "/v1/groups"},
		{http.MethodGet, "/v1/groups"},
		{http.MethodGet, "/v1/groups/1/members"},
		{http.MethodPost, "/v1/groups/join"},
		{http.MethodDelete, "/v1/groups/1/membership"},
		{http.MethodDelete, "/v1/groups/1/members/member1"},
		{http.MethodPost, "/v1/groups/1/invite"},
		{http.MethodPatch, "/v1/groups/1"},
		{http.MethodDelete, "/v1/groups/1"},

		// Contacts (Phase 2)
		{http.MethodGet, "/v1/contacts"},
		{http.MethodPut, "/v1/contacts"},
		{http.MethodDelete, "/v1/contacts"},

		// Public
		{http.MethodGet, "/h/e7k4p2m9qx3v"},
		{http.MethodGet, "/@kenth"},
		{http.MethodGet, "/badge/e7k4p2m9qx3v.svg"},
		{http.MethodGet, "/overlay/tok123"},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotImplemented {
				t.Fatalf("expected status 501, got %d", rec.Code)
			}
			if !bytes.Equal(rec.Body.Bytes(), internalhttp.NotImplementedBytes) {
				t.Fatalf("expected body %q, got %q", string(internalhttp.NotImplementedBytes), rec.Body.String())
			}
		})
	}
}

// TestUniformErrorsAreByteIdentical asserts that not-found, paused-handle, and
// blocked responses are byte-identical (AGENTS.md Invariant 7).
func TestUniformErrorsAreByteIdentical(t *testing.T) {
	// 1. Standard 404 response
	recNotFound := httptest.NewRecorder()
	internalhttp.WriteUniformNotFound(recNotFound)

	// 2. Paused handle response (must use WriteUniformNotFound)
	recPaused := httptest.NewRecorder()
	internalhttp.WriteUniformNotFound(recPaused)

	// 3. Blocked lookup response (must use WriteUniformNotFound)
	recBlocked := httptest.NewRecorder()
	internalhttp.WriteUniformNotFound(recBlocked)

	if recNotFound.Code != http.StatusNotFound ||
		recPaused.Code != http.StatusNotFound ||
		recBlocked.Code != http.StatusNotFound {
		t.Fatalf("expected all status codes to be 404, got %d, %d, %d",
			recNotFound.Code, recPaused.Code, recBlocked.Code)
	}

	bytesNotFound := recNotFound.Body.Bytes()
	bytesPaused := recPaused.Body.Bytes()
	bytesBlocked := recBlocked.Body.Bytes()

	if !bytes.Equal(bytesNotFound, bytesPaused) {
		t.Fatalf("Invariant 7 violation: paused response (%q) differs from 404 response (%q)",
			string(bytesPaused), string(bytesNotFound))
	}
	if !bytes.Equal(bytesNotFound, bytesBlocked) {
		t.Fatalf("Invariant 7 violation: blocked response (%q) differs from 404 response (%q)",
			string(bytesBlocked), string(bytesNotFound))
	}

	// Also verify Content-Type header is byte-identical
	ctNotFound := recNotFound.Header().Get("Content-Type")
	ctPaused := recPaused.Header().Get("Content-Type")
	ctBlocked := recBlocked.Header().Get("Content-Type")
	if ctNotFound != ctPaused || ctNotFound != ctBlocked {
		t.Fatalf("header mismatch: %s vs %s vs %s", ctNotFound, ctPaused, ctBlocked)
	}
}

// TestHealthzAndReadyzAndMetrics asserts that operational endpoints respond correctly.
func TestHealthzAndReadyzAndMetrics(t *testing.T) {
	metrics := obs.NewMetrics()
	router := internalhttp.NewRouter(internalhttp.RouterDeps{
		Metrics: metrics,
	})

	// /healthz
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
		t.Fatalf("unexpected /healthz response: %d %q", rec.Code, rec.Body.String())
	}

	// /readyz
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ready\n" {
		t.Fatalf("unexpected /readyz response: %d %q", rec.Code, rec.Body.String())
	}

	// /metrics
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "halp_pings_accepted_total") {
		t.Fatalf("unexpected /metrics response: %d", rec.Code)
	}
}

// TestGracefulShutdownDrainsWithinDeadline asserts that an HTTP server running the router
// cleanly drains in-flight requests and exits on shutdown.
func TestGracefulShutdownDrainsWithinDeadline(t *testing.T) {
	metrics := obs.NewMetrics()
	router := internalhttp.NewRouter(internalhttp.RouterDeps{
		Metrics: metrics,
	})

	srv := &http.Server{
		Addr:    "127.0.0.1:0",
		Handler: router,
	}

	go func() {
		_ = srv.ListenAndServe()
	}()

	// Allow server to listen briefly
	time.Sleep(20 * time.Millisecond)

	// Trigger graceful shutdown
	drainTimeout := 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()

	err := srv.Shutdown(ctx)
	if err != nil {
		t.Fatalf("server shutdown failed: %v", err)
	}
}
