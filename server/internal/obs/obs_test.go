package obs_test

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/halpworld/halpcheers/server/internal/obs"
)

// TestMetricsServesPhase1Set asserts that /metrics serves the complete Phase 1 metric set.
func TestMetricsServesPhase1Set(t *testing.T) {
	m := obs.NewMetrics()

	m.IncPingsAccepted()
	m.IncPingsDropped(obs.DropReasonRateLimited)
	m.IncPingsDropped(obs.DropReasonQueueFull)
	m.IncDigestsSent()
	m.IncPushFailures(obs.PushFailureCode410)
	m.SetPoWDifficulty(16)
	m.SetSSEConnections(5)
	m.ObserveIngressLatency(500 * time.Microsecond)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	requiredMetrics := []string{
		"halp_pings_accepted_total",
		"halp_pings_dropped_total{reason=\"rate_limited\"}",
		"halp_pings_dropped_total{reason=\"queue_full\"}",
		"halp_digests_sent_total",
		"halp_push_failures_total{code=\"410\"}",
		"halp_pow_difficulty",
		"halp_sse_connections",
		"halp_ingress_latency_seconds",
	}

	for _, name := range requiredMetrics {
		if !strings.Contains(body, name) {
			t.Errorf("missing expected metric in output: %s", name)
		}
	}
}

// TestAccessLogEmitsRoutePatternNotPath asserts that the access log records the route pattern
// and NEVER the raw URL path containing target handles or aliases.
func TestAccessLogEmitsRoutePatternNotPath(t *testing.T) {
	var logBuf bytes.Buffer
	middleware := obs.AccessLogMiddleware(&logBuf)

	r := chi.NewRouter()
	r.Use(middleware)
	r.Post("/v1/ping/{target}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	// Send ping with a realistic Crockford base32 handle
	handle := "e7k4p2m9qx3v"
	req := httptest.NewRequest(http.MethodPost, "/v1/ping/"+handle, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	logOutput := logBuf.String()

	// Invariant 9 Assertions:
	// 1. Must contain the route pattern
	if !strings.Contains(logOutput, "route=/v1/ping/{target}") {
		t.Errorf("expected access log to contain route pattern 'route=/v1/ping/{target}', got: %s", logOutput)
	}

	// 2. Must NEVER contain the handle identifier
	if strings.Contains(logOutput, handle) {
		t.Errorf("INVARIANT 9 VIOLATION: access log leaked handle '%s': %s", handle, logOutput)
	}

	// 3. Must contain method, status, latency_us, size
	for _, expectedToken := range []string{"method=POST", "status=202", "latency_us=", "size="} {
		if !strings.Contains(logOutput, expectedToken) {
			t.Errorf("expected access log to contain %q, got: %s", expectedToken, logOutput)
		}
	}
}

// TestIPAnonymization asserts that raw IPs are hashed in memory with a rotating salt
// and never stored raw.
func TestIPAnonymization(t *testing.T) {
	anon := obs.NewIPAnonymizer()
	ip1 := net.ParseIP("192.0.2.1")
	ip2 := net.ParseIP("192.0.2.2")

	h1 := anon.Anonymize(ip1)
	h1Again := anon.Anonymize(ip1)
	h2 := anon.Anonymize(ip2)

	if h1 != h1Again {
		t.Fatalf("expected deterministic hash within same salt epoch")
	}
	if h1 == h2 {
		t.Fatalf("expected different IPs to produce different hash buckets")
	}

	// Rotate salt
	anon.RotateSalt()
	h1AfterRotation := anon.Anonymize(ip1)
	if h1 == h1AfterRotation {
		t.Fatalf("expected rotated salt to produce different hash bucket for same IP")
	}
}

// TestCIInvariant9CheckFailsOnLeakFixture asserts that the CI AST check correctly catches
// an intentional leak fixture (e.g. log.Printf("ping to %s", handle)) with a helpful Invariant 9 error.
func TestCIInvariant9CheckFailsOnLeakFixture(t *testing.T) {
	leakyFixtureCode := `
package fixture

import (
	"log"
	"github.com/halpworld/halpcheers/server/internal/core"
)

func LeakyHandler(handle core.Handle) {
	log.Printf("ping to %s", handle)
}
`
	violations, err := obs.CheckSource("leaky_fixture.go", leakyFixtureCode)
	if err != nil {
		t.Fatalf("check source failed: %v", err)
	}

	if len(violations) == 0 {
		t.Fatalf("expected Invariant 9 check to FAIL on leaky fixture, but it found 0 violations")
	}

	v := violations[0]
	if !strings.Contains(v.Message, "INVARIANT 9 VIOLATION") {
		t.Errorf("expected violation message to reference INVARIANT 9 VIOLATION, got: %s", v.Message)
	}
	if !strings.Contains(v.Message, "handle") {
		t.Errorf("expected violation message to name the leaked identifier 'handle', got: %s", v.Message)
	}
}

// TestCleanCodebasePassesInvariant9Check asserts that our actual repository source
// contains zero Invariant 9 violations.
func TestCleanCodebasePassesInvariant9Check(t *testing.T) {
	violations, err := obs.CheckInvariant9("../..")
	if err != nil {
		t.Fatalf("check codebase failed: %v", err)
	}

	if len(violations) > 0 {
		for _, v := range violations {
			t.Errorf("unexpected invariant violation in clean codebase: %s", v)
		}
	}
}
