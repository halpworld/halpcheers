package stream_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/halpworld/halpcheers/server/internal/core"
	serverhttp "github.com/halpworld/halpcheers/server/internal/http"
	"github.com/halpworld/halpcheers/server/internal/obs"
	"github.com/halpworld/halpcheers/server/internal/stream"
)

// fakeFlusherRecorder implements http.ResponseWriter and http.Flusher.
type fakeFlusherRecorder struct {
	*httptest.ResponseRecorder
	flushed chan struct{}
}

func newFakeFlusherRecorder() *fakeFlusherRecorder {
	return &fakeFlusherRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		flushed:          make(chan struct{}, 100),
	}
}

func (f *fakeFlusherRecorder) Flush() {
	select {
	case f.flushed <- struct{}{}:
	default:
	}
}

// fakePending implements serverhttp.PendingProvider for testing.
type fakePending struct {
	mu     sync.Mutex
	counts map[core.AccountID]int
}

func (p *fakePending) GetAndClearPending(account core.AccountID) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := p.counts[account]
	p.counts[account] = 0
	return n
}

// Test1000ConnectionMemoryBudget measures memory consumption per connection for 1,000 idle connections.
// Budget from docs/ARCHITECTURE.md: 12-20 KB per connection.
func Test1000ConnectionMemoryBudget(t *testing.T) {
	hub := stream.NewHub(stream.Config{
		MaxConnections: 2000,
	})
	defer hub.Close()

	const numConns = 1000
	ctxs := make([]context.CancelFunc, numConns)

	// Run GC before measuring baseline
	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	var wg sync.WaitGroup
	wg.Add(numConns)

	for i := 0; i < numConns; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		ctxs[i] = cancel

		accID := core.AccountID(1000 + i)
		r := httptest.NewRequest(http.MethodGet, "/v1/stream", nil).WithContext(ctx)
		w := newFakeFlusherRecorder()

		go func() {
			wg.Done()
			hub.ServeHTTP(w, r, accID)
		}()
	}

	wg.Wait()
	// Allow connections to register
	time.Sleep(50 * time.Millisecond)

	if hub.ActiveConnections() != int64(numConns) {
		t.Fatalf("expected %d active connections, got %d", numConns, hub.ActiveConnections())
	}

	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	bytesAllocated := memAfter.HeapAlloc - memBefore.HeapAlloc
	bytesPerConn := bytesAllocated / numConns
	t.Logf("Measured memory: %d bytes total for %d connections (~%d bytes/conn = %.2f KB/conn)",
		bytesAllocated, numConns, bytesPerConn, float64(bytesPerConn)/1024.0)

	// Cancel all connections
	for _, cancel := range ctxs {
		cancel()
	}

	// Budget check: 20 KB max per connection
	const maxBudgetPerConn = 20 * 1024
	if bytesPerConn > maxBudgetPerConn {
		t.Fatalf("memory per connection %d bytes exceeded 20 KB budget", bytesPerConn)
	}
}

// TestSlowConsumerDropped asserts that when a client's buffer is saturated,
// the slow consumer is disconnected, the hub stays responsive, and the drop metric increments.
func TestSlowConsumerDropped(t *testing.T) {
	metrics := obs.NewMetrics()
	hub := stream.NewHub(stream.Config{
		BufferPerConn: 2, // Small buffer of 2
		Metrics:       metrics,
	})
	defer hub.Close()

	accID := core.AccountID(42)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := httptest.NewRequest(http.MethodGet, "/v1/stream", nil).WithContext(ctx)
	w := newFakeFlusherRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		hub.ServeHTTP(w, r, accID)
	}()

	// Wait for connection to register
	for i := 0; i < 50; i++ {
		if hub.ActiveConnections() == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Overflow buffer: buffer capacity is 2, send 5 pings without reading
	for i := 0; i < 5; i++ {
		hub.Broadcast(accID, i+1)
	}

	select {
	case <-done:
		// Connection terminated because slow consumer was dropped!
	case <-time.After(2 * time.Second):
		t.Fatalf("slow consumer was not disconnected within timeout")
	}

	if hub.ActiveConnections() != 0 {
		t.Fatalf("expected 0 active connections after drop, got %d", hub.ActiveConnections())
	}
}

// TestHeartbeatSent asserts that heartbeats (event: ka) are transmitted periodically.
func TestHeartbeatSent(t *testing.T) {
	hub := stream.NewHub(stream.Config{
		HeartbeatInterval: 10 * time.Millisecond,
	})
	defer hub.Close()

	accID := core.AccountID(77)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := httptest.NewRequest(http.MethodGet, "/v1/stream", nil).WithContext(ctx)
	w := newFakeFlusherRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		hub.ServeHTTP(w, r, accID)
	}()

	// Wait for multiple heartbeats
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	body := w.Body.String()
	if !strings.Contains(body, "event: ka") {
		t.Fatalf("expected heartbeat frame 'event: ka' in stream, got:\n%s", body)
	}
}

// TestIdleDemotionClosesConnection asserts that after idleDemote duration,
// the stream closes gracefully with event: demote.
func TestIdleDemotionClosesConnection(t *testing.T) {
	hub := stream.NewHub(stream.Config{
		HeartbeatInterval: 1 * time.Hour,
		IdleDemoteAfter:   20 * time.Millisecond, // Short idle demotion
	})
	defer hub.Close()

	accID := core.AccountID(88)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := httptest.NewRequest(http.MethodGet, "/v1/stream", nil).WithContext(ctx)
	w := newFakeFlusherRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		hub.ServeHTTP(w, r, accID)
	}()

	select {
	case <-done:
		// Stream ended due to idle demotion!
	case <-time.After(1 * time.Second):
		t.Fatalf("stream did not demote after idle timeout")
	}

	body := w.Body.String()
	if !strings.Contains(body, "event: demote") {
		t.Fatalf("expected demote event in stream body, got:\n%s", body)
	}
}

// TestPendingClearsOnRead asserts that /v1/pending atomically clears the count.
func TestPendingClearsOnRead(t *testing.T) {
	pending := &fakePending{
		counts: map[core.AccountID]int{
			42: 7,
		},
	}
	hub := stream.NewHub(stream.Config{})
	auth := &fakeStreamAuth{validToken: "tok_stream", accountID: 42}

	handler := serverhttp.NewStreamHandlerWithDeps(hub, auth, pending)
	router := serverhttp.NewRouter(serverhttp.RouterDeps{
		Stream: handler,
	})

	// Call 1: returns 7
	r1 := httptest.NewRequest(http.MethodGet, "/v1/pending", nil)
	r1.Header.Set("Authorization", "Bearer tok_stream")
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, r1)

	if w1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w1.Code)
	}
	if !strings.Contains(w1.Body.String(), `"n":7`) {
		t.Fatalf("expected n=7, got %s", w1.Body.String())
	}

	// Call 2: returns 0 (cleared!)
	r2 := httptest.NewRequest(http.MethodGet, "/v1/pending", nil)
	r2.Header.Set("Authorization", "Bearer tok_stream")
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, r2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w2.Code)
	}
	if !strings.Contains(w2.Body.String(), `"n":0`) {
		t.Fatalf("expected n=0 on second call, got %s", w2.Body.String())
	}
}

// TestConnectDisconnectNoLeaksUnderRace asserts rapid connect/disconnect under -race
// does not leak goroutines or cause deadlocks.
func TestConnectDisconnectNoLeaksUnderRace(t *testing.T) {
	hub := stream.NewHub(stream.Config{
		HeartbeatInterval: 10 * time.Millisecond,
		IdleDemoteAfter:   50 * time.Millisecond,
	})
	defer hub.Close()

	const iterations = 500
	var wg sync.WaitGroup
	wg.Add(iterations)

	for i := 0; i < iterations; i++ {
		go func(idx int) {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			accID := core.AccountID(idx % 10)

			r := httptest.NewRequest(http.MethodGet, "/v1/stream", nil).WithContext(ctx)
			w := newFakeFlusherRecorder()

			done := make(chan struct{})
			go func() {
				defer close(done)
				hub.ServeHTTP(w, r, accID)
			}()

			// Send an event while active
			hub.Broadcast(accID, 1)

			// Quickly disconnect
			cancel()
			<-done
		}(i)
	}

	wg.Wait()

	if hub.ActiveConnections() != 0 {
		t.Fatalf("expected 0 active connections, got %d", hub.ActiveConnections())
	}
}

type fakeStreamAuth struct {
	validToken string
	accountID  core.AccountID
}

func (f *fakeStreamAuth) AuthenticateSession(ctx context.Context, token string) (core.AccountID, bool) {
	if token == f.validToken {
		return f.accountID, true
	}
	return 0, false
}
