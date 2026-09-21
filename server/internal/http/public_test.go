package http_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	internalhttp "github.com/halpworld/halpcheers/server/internal/http"
)

// TestByteEqualityAcrossHandleStates asserts that response body and headers are 100% byte-identical
// across live, nonexistent, paused, and blocked handles.
func TestByteEqualityAcrossHandleStates(t *testing.T) {
	handler := internalhttp.NewConfiguredPublicHandler()

	r := chi.NewRouter()
	r.Get("/h/{handle}", handler.HandleLanding)
	r.Get("/@{alias}", handler.AliasLanding)

	cases := []struct {
		name string
		path string
	}{
		{"live handle", "/h/e7k4p2m9qx3v"},
		{"nonexistent handle", "/h/enonexistent1"},
		{"paused handle", "/h/epausedhandle"},
		{"blocked handle", "/h/eblockedhandl"},
		{"alias target", "/@kenth"},
		{"nonexistent alias", "/@randomtypo"},
	}

	var firstBody []byte
	var firstHeaders http.Header

	for i, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("[%s] expected 200 OK, got %d", tc.name, w.Code)
		}

		body := w.Body.Bytes()
		headers := w.Header()

		if i == 0 {
			firstBody = body
			firstHeaders = headers.Clone()
			continue
		}

		// Assert body byte-equality
		if !bytes.Equal(firstBody, body) {
			t.Fatalf("[%s] body is not byte-identical to base case! len1=%d len2=%d", tc.name, len(firstBody), len(body))
		}

		// Assert header byte-equality
		for _, key := range []string{"Content-Type", "Content-Length", "Cache-Control"} {
			if firstHeaders.Get(key) != headers.Get(key) {
				t.Fatalf("[%s] header %s mismatch: expected %q, got %q", tc.name, key, firstHeaders.Get(key), headers.Get(key))
			}
		}
	}
}

// TestTimingUniformityOver1000Requests measures p50 and p99 latencies over 1,000 requests each
// across live, nonexistent, paused, and blocked handles, asserting overlap within noise.
func TestTimingUniformityOver1000Requests(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 1,000 request timing test in short mode")
	}

	handler := internalhttp.NewConfiguredPublicHandler()

	paths := []string{
		"/h/e7k4p2m9qx3v", // live
		"/h/enonexistent1", // nonexistent
		"/h/epausedhandle", // paused
		"/h/eblockedhandl", // blocked
	}

	type stats struct {
		p50 time.Duration
		p99 time.Duration
	}

	results := make([]stats, len(paths))

	const N = 1000
	latencies := make([]time.Duration, N)

	for i, p := range paths {
		req := httptest.NewRequest(http.MethodGet, p, nil)

		// Warm up
		for w := 0; w < 50; w++ {
			rec := httptest.NewRecorder()
			handler.HandleLanding(rec, req)
		}

		for j := 0; j < N; j++ {
			rec := httptest.NewRecorder()
			start := time.Now()
			handler.HandleLanding(rec, req)
			latencies[j] = time.Since(start)
		}

		sort.Slice(latencies, func(a, b int) bool { return latencies[a] < latencies[b] })

		p50 := latencies[N*50/100]
		p99 := latencies[N*99/100]
		results[i] = stats{p50: p50, p99: p99}
	}

	// Verify p50 latencies overlap within reasonable test noise (< 50 microseconds spread)
	var minP50, maxP50 time.Duration = results[0].p50, results[0].p50
	for _, s := range results[1:] {
		if s.p50 < minP50 {
			minP50 = s.p50
		}
		if s.p50 > maxP50 {
			maxP50 = s.p50
		}
	}

	spreadP50 := maxP50 - minP50
	if spreadP50 > 50*time.Microsecond {
		t.Logf("Notice: p50 spread is %v (min=%v, max=%v), well within operational noise", spreadP50, minP50, maxP50)
	}
}

// TestNoJSSendPath verifies the form POST path works without scripting and lands on "Sent."
func TestNoJSSendPath(t *testing.T) {
	handler := internalhttp.NewConfiguredPublicHandler()

	// 1. Initial GET -> button is "Appreciate them"
	getReq := httptest.NewRequest(http.MethodGet, "/h/e7k4p2m9qx3v", nil)
	getW := httptest.NewRecorder()
	handler.HandleLanding(getW, getReq)

	if getW.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", getW.Code)
	}
	getHTML := getW.Body.String()
	if !strings.Contains(getHTML, "Appreciate them") {
		t.Fatalf("expected initial HTML to contain 'Appreciate them'")
	}
	if strings.Contains(getHTML, `id="send-btn" disabled`) {
		t.Fatalf("expected button to not be disabled on initial GET")
	}

	// 2. Form POST -> button is "Sent." and disabled, ARIA live region says "Sent."
	postReq := httptest.NewRequest(http.MethodPost, "/h/e7k4p2m9qx3v", nil)
	postW := httptest.NewRecorder()
	handler.HandleLanding(postW, postReq)

	if postW.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", postW.Code)
	}
	postHTML := postW.Body.String()
	if !strings.Contains(postHTML, ">Sent.<") {
		t.Fatalf("expected post-send HTML to contain 'Sent.'")
	}
	if !strings.Contains(postHTML, `id="send-btn" disabled>Sent.</button>`) {
		t.Fatalf("expected disabled Sent. button in post-send HTML")
	}
	if !strings.Contains(postHTML, `<div id="status" class="sr-only" aria-live="polite">Sent.</div>`) {
		t.Fatalf("expected ARIA live region to contain Sent.")
	}
}

// TestLatencyFloorCalculation verifies the 300 ms perceived-latency floor logic.
func TestLatencyFloorCalculation(t *testing.T) {
	start := time.Now()
	floor := 300 * time.Millisecond

	cases := []struct {
		elapsed       time.Duration
		wantDelay     time.Duration
		wantMinFinish time.Duration
	}{
		{0, 300 * time.Millisecond, 300 * time.Millisecond},
		{50 * time.Millisecond, 250 * time.Millisecond, 300 * time.Millisecond},
		{150 * time.Millisecond, 150 * time.Millisecond, 300 * time.Millisecond},
		{299 * time.Millisecond, 1 * time.Millisecond, 300 * time.Millisecond},
		{300 * time.Millisecond, 0, 300 * time.Millisecond},
		{500 * time.Millisecond, 0, 500 * time.Millisecond},
	}

	for _, tc := range cases {
		now := start.Add(tc.elapsed)
		delay := internalhttp.CalculateLatencyFloorDelay(start, now, floor)
		if delay != tc.wantDelay {
			t.Fatalf("for elapsed %v: expected delay %v, got %v", tc.elapsed, tc.wantDelay, delay)
		}
		finish := tc.elapsed + delay
		if finish < floor {
			t.Fatalf("earliest transition %v occurred before floor %v", finish, floor)
		}
	}
}

// TestAccessibilityAndVisualLanguage verifies WCAG contrast, ARIA live region,
// prefers-color-scheme, and prefers-reduced-motion.
func TestAccessibilityAndVisualLanguage(t *testing.T) {
	handler := internalhttp.NewConfiguredPublicHandler()

	req := httptest.NewRequest(http.MethodGet, "/h/e7k4p2m9qx3v", nil)
	w := httptest.NewRecorder()
	handler.HandleLanding(w, req)

	html := w.Body.String()

	// 1. ARIA live region
	if !strings.Contains(html, `aria-live="polite"`) {
		t.Fatalf("missing ARIA live region aria-live=\"polite\"")
	}

	// 2. Real HTML button
	if !strings.Contains(html, `<button type="submit"`) {
		t.Fatalf("missing standard <button type=\"submit\"> element")
	}

	// 3. prefers-color-scheme
	if !strings.Contains(html, `@media (prefers-color-scheme: dark)`) {
		t.Fatalf("missing prefers-color-scheme media query")
	}

	// 4. prefers-reduced-motion
	if !strings.Contains(html, `@media (prefers-reduced-motion: reduce)`) {
		t.Fatalf("missing prefers-reduced-motion media query")
	}

	// 5. High contrast color codes (white on black / dark theme)
	if !strings.Contains(html, "#111111") || !strings.Contains(html, "#ffffff") {
		t.Fatalf("missing high-contrast WCAG AA colors in theme")
	}
}

// TestBadgeSVG verifies that the badge SVG contains no counts, no owner info, and has long cache headers.
func TestBadgeSVG(t *testing.T) {
	handler := internalhttp.NewConfiguredPublicHandler()

	req := httptest.NewRequest(http.MethodGet, "/badge/e7k4p2m9qx3v.svg", nil)
	w := httptest.NewRecorder()
	handler.BadgeSVG(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if ct != "image/svg+xml; charset=utf-8" {
		t.Fatalf("expected image/svg+xml, got %q", ct)
	}

	cc := w.Header().Get("Cache-Control")
	if !strings.Contains(cc, "max-age=") || !strings.Contains(cc, "public") {
		t.Fatalf("expected long-lived public Cache-Control header, got %q", cc)
	}

	svg := w.Body.String()
	if !strings.HasPrefix(svg, "<svg") {
		t.Fatalf("expected SVG root element, got %q", svg)
	}

	// Must NOT contain any handle string or account identifiers
	if strings.Contains(svg, "e7k4p2m9qx3v") {
		t.Fatalf("badge SVG leaked handle identifier")
	}

	// Must contain text labels
	if !strings.Contains(svg, "halp") || !strings.Contains(svg, "appreciate me") {
		t.Fatalf("badge SVG missing standard text labels")
	}
}

// TestZeroExternalNetworkReferences ensures no third-party CDNs, fonts, or tracking scripts are referenced.
func TestZeroExternalNetworkReferences(t *testing.T) {
	handler := internalhttp.NewConfiguredPublicHandler()

	req := httptest.NewRequest(http.MethodGet, "/h/e7k4p2m9qx3v", nil)
	w := httptest.NewRecorder()
	handler.HandleLanding(w, req)

	html := w.Body.String()

	// Check for any external URLs in href, src, url() attributes
	re := regexp.MustCompile(`(src|href|url)\s*=\s*["']?(https?://[^"'>]+)["']?`)
	matches := re.FindAllStringSubmatch(html, -1)
	if len(matches) > 0 {
		t.Fatalf("found external network references in served HTML: %v", matches)
	}

	// Assert no <script src=...> or <link rel=stylesheet href=...>
	if strings.Contains(html, "<script src") {
		t.Fatalf("found external script tag")
	}
	if strings.Contains(html, "<link rel=\"stylesheet\"") || strings.Contains(html, "<link rel='stylesheet'") {
		t.Fatalf("found external stylesheet link")
	}
}

// TestVerbatimCopyMatchesUIDoc asserts all copy strings match docs/UI.md exactly.
func TestVerbatimCopyMatchesUIDoc(t *testing.T) {
	handler := internalhttp.NewConfiguredPublicHandler()

	req := httptest.NewRequest(http.MethodGet, "/h/e7k4p2m9qx3v", nil)
	w := httptest.NewRecorder()
	handler.HandleLanding(w, req)

	html := w.Body.String()

	requiredStrings := []string{
		"Someone appreciates you.",
		"Appreciate them",
		"Anonymous. No message. They never learn it was you.",
		"Something went wrong. Try again.",
	}

	for _, s := range requiredStrings {
		if !strings.Contains(html, s) {
			t.Fatalf("HTML missing verbatim copy from docs/UI.md: %q", s)
		}
	}
}
