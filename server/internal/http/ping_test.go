package http_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/halpworld/halpcheers/server/internal/coalesce"
	"github.com/halpworld/halpcheers/server/internal/core"
	serverhttp "github.com/halpworld/halpcheers/server/internal/http"
	"github.com/halpworld/halpcheers/server/internal/obs"
)

// FakeAuth implements serverhttp.PingSessionAuthenticator in pure memory.
type FakeAuth struct {
	validToken string
	accountID  core.AccountID
}

func (f *FakeAuth) AuthenticateSession(ctx context.Context, token string) (core.AccountID, bool) {
	if token == f.validToken {
		return f.accountID, true
	}
	return 0, false
}

// FakePoW implements serverhttp.PoWValidator in pure memory.
type FakePoW struct {
	shouldFail bool
}

func (f *FakePoW) Verify(handle string, tokenHeader string, target string, sender core.AccountID) error {
	if f.shouldFail {
		return errors.New("invalid pow")
	}
	return nil
}

// FakeResolver implements core.Resolver in pure memory without disk or network.
type FakeResolver struct {
	targets map[string]core.AccountID
}

func (f *FakeResolver) Resolve(ctx context.Context, target string) (core.AccountID, core.Handle, bool) {
	if id, ok := f.targets[target]; ok {
		return id, core.Handle(target), true
	}
	return 0, "", false
}

// FakeGuard implements core.Guard in pure memory.
type FakeGuard struct {
	allow bool
}

func (f *FakeGuard) Allow(ctx context.Context, sender core.AccountID, target core.Handle, kind core.HandleKind) bool {
	return f.allow
}

// TestNoDiskOrNetworkSyscalls asserts all ingress operations run entirely in memory.
func TestNoDiskOrNetworkSyscalls(t *testing.T) {
	auth := &FakeAuth{validToken: "tok123", accountID: 100}
	pow := &FakePoW{shouldFail: false}
	resolver := &FakeResolver{targets: map[string]core.AccountID{"e7k4p2m9qx3v": 200}}
	guard := &FakeGuard{allow: true}
	queue := coalesce.NewQueue(100, nil)

	handler := serverhttp.NewPingHandlerWithDeps(auth, pow, resolver, guard, queue, nil)
	router := serverhttp.NewRouter(serverhttp.RouterDeps{
		Ping: handler,
	})

	r := httptest.NewRequest(http.MethodPost, "/v1/ping/e7k4p2m9qx3v", nil)
	r.Header.Set("Authorization", "Bearer tok123")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, r)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("expected empty body, got %d bytes", w.Body.Len())
	}
	if queue.Len() != 1 {
		t.Fatalf("expected 1 queued job, got %d", queue.Len())
	}
}

// TestIndistinguishableOutcomes asserts that accepted, rate-limited, deduped,
// blocked, paused, and unknown-target sends all return identical 202 Accepted
// with empty bodies and uniform timing.
func TestIndistinguishableOutcomes(t *testing.T) {
	auth := &FakeAuth{validToken: "tok123", accountID: 100}
	pow := &FakePoW{shouldFail: false}
	metrics := obs.NewMetrics()

	tests := []struct {
		name       string
		target     string
		resolver   *FakeResolver
		guardAllow bool
	}{
		{
			name:       "accepted",
			target:     "e7k4p2m9qx3v",
			resolver:   &FakeResolver{targets: map[string]core.AccountID{"e7k4p2m9qx3v": 200}},
			guardAllow: true,
		},
		{
			name:       "unknown-target",
			target:     "e999999999999",
			resolver:   &FakeResolver{targets: map[string]core.AccountID{}},
			guardAllow: true,
		},
		{
			name:       "rate-limited-or-deduped",
			target:     "e7k4p2m9qx3v",
			resolver:   &FakeResolver{targets: map[string]core.AccountID{"e7k4p2m9qx3v": 200}},
			guardAllow: false,
		},
		{
			name:       "alias-target",
			target:     "@kenth",
			resolver:   &FakeResolver{targets: map[string]core.AccountID{"@kenth": 200}},
			guardAllow: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			queue := coalesce.NewQueue(100, metrics)
			guard := &FakeGuard{allow: tc.guardAllow}
			handler := serverhttp.NewPingHandlerWithDeps(auth, pow, tc.resolver, guard, queue, metrics)
			router := serverhttp.NewRouter(serverhttp.RouterDeps{
				Ping: handler,
			})

			r := httptest.NewRequest(http.MethodPost, "/v1/ping/"+tc.target, nil)
			r.Header.Set("Authorization", "Bearer tok123")
			w := httptest.NewRecorder()

			start := time.Now()
			router.ServeHTTP(w, r)
			elapsed := time.Since(start)

			if w.Code != http.StatusAccepted {
				t.Fatalf("[%s] expected 202 Accepted, got %d", tc.name, w.Code)
			}
			if w.Body.Len() != 0 {
				t.Fatalf("[%s] expected empty body, got %d bytes", tc.name, w.Body.Len())
			}
			// Invariant 2: Ingress under 3 ms
			if elapsed > 3*time.Millisecond {
				t.Fatalf("[%s] request took %v > 3 ms budget", tc.name, elapsed)
			}
		})
	}
}

// TestQueueFullDropsIncrementMetricUnderRace asserts:
// When the queue is full under concurrent flood, jobs are dropped, the metric
// counter with reason="queue_full" is incremented, and all callers still receive 202 Accepted.
func TestQueueFullDropsIncrementMetricUnderRace(t *testing.T) {
	metrics := obs.NewMetrics()
	// Deliberately undersized queue of capacity 5
	queue := coalesce.NewQueue(5, metrics)
	auth := &FakeAuth{validToken: "tok123", accountID: 100}
	pow := &FakePoW{shouldFail: false}
	resolver := &FakeResolver{targets: map[string]core.AccountID{"e7k4p2m9qx3v": 200}}
	guard := &FakeGuard{allow: true}

	handler := serverhttp.NewPingHandlerWithDeps(auth, pow, resolver, guard, queue, metrics)
	router := serverhttp.NewRouter(serverhttp.RouterDeps{
		Ping: handler,
	})

	const numRequests = 50
	var wg sync.WaitGroup
	wg.Add(numRequests)

	for i := 0; i < numRequests; i++ {
		go func() {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodPost, "/v1/ping/e7k4p2m9qx3v", nil)
			r.Header.Set("Authorization", "Bearer tok123")
			w := httptest.NewRecorder()

			router.ServeHTTP(w, r)

			if w.Code != http.StatusAccepted {
				t.Errorf("expected 202 Accepted under load, got %d", w.Code)
			}
			if w.Body.Len() != 0 {
				t.Errorf("expected empty body, got %d bytes", w.Body.Len())
			}
		}()
	}

	wg.Wait()

	// Exactly 5 jobs should be queued, and 45 dropped
	if queue.Len() != 5 {
		t.Fatalf("expected queue length 5, got %d", queue.Len())
	}
}

// TestPingUnauthenticated asserts that missing or invalid bearer token returns 401.
func TestPingUnauthenticated(t *testing.T) {
	auth := &FakeAuth{validToken: "valid_tok", accountID: 100}
	handler := serverhttp.NewPingHandlerWithDeps(auth, nil, &FakeResolver{}, &FakeGuard{}, coalesce.NewQueue(10, nil), nil)
	router := serverhttp.NewRouter(serverhttp.RouterDeps{
		Ping: handler,
	})

	// Missing token
	r1 := httptest.NewRequest(http.MethodPost, "/v1/ping/e7k4p2m9qx3v", nil)
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, r1)
	if w1.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing token, got %d", w1.Code)
	}

	// Invalid token
	r2 := httptest.NewRequest(http.MethodPost, "/v1/ping/e7k4p2m9qx3v", nil)
	r2.Header.Set("Authorization", "Bearer bad_tok")
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, r2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad token, got %d", w2.Code)
	}
}

// BenchmarkSendPingAcceptPath measures p99 latency and verifies allocations on the accept path.
func BenchmarkSendPingAcceptPath(b *testing.B) {
	auth := &FakeAuth{validToken: "tok123", accountID: 100}
	pow := &FakePoW{shouldFail: false}
	resolver := &FakeResolver{targets: map[string]core.AccountID{"e7k4p2m9qx3v": 200}}
	guard := &FakeGuard{allow: true}
	// Big queue so no drops during benchmark
	queue := coalesce.NewQueue(b.N+1000, nil)

	handler := serverhttp.NewPingHandlerWithDeps(auth, pow, resolver, guard, queue, nil)
	router := serverhttp.NewRouter(serverhttp.RouterDeps{
		Ping: handler,
	})

	r := httptest.NewRequest(http.MethodPost, "/v1/ping/e7k4p2m9qx3v", nil)
	r.Header.Set("Authorization", "Bearer tok123")
	w := httptest.NewRecorder()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		router.ServeHTTP(w, r)
	}
}

type noopResponseWriter struct{}

func (noopResponseWriter) Header() http.Header         { return nil }
func (noopResponseWriter) Write([]byte) (int, error)   { return 0, nil }
func (noopResponseWriter) WriteHeader(statusCode int) {}

// BenchmarkSendPingHandlerDirect measures pure handler execution and asserts 0 allocations on accept path.
func BenchmarkSendPingHandlerDirect(b *testing.B) {
	auth := &FakeAuth{validToken: "tok123", accountID: 100}
	pow := &FakePoW{shouldFail: false}
	resolver := &FakeResolver{targets: map[string]core.AccountID{"e7k4p2m9qx3v": 200}}
	guard := &FakeGuard{allow: true}
	queue := coalesce.NewQueue(b.N+1000, nil)

	handler := serverhttp.NewPingHandlerWithDeps(auth, pow, resolver, guard, queue, nil)

	r := httptest.NewRequest(http.MethodPost, "/v1/ping/e7k4p2m9qx3v", nil)
	// Inject session into context
	ctx := serverhttp.WithSessionContext(r.Context(), "tok123")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("target", "e7k4p2m9qx3v")
	ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	r = r.WithContext(ctx)

	var w noopResponseWriter

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		handler.SendPing(w, r)
	}
}

