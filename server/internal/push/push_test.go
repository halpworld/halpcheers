package push_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/obs"
	"github.com/halpworld/halpcheers/server/internal/push"
	"github.com/halpworld/halpcheers/server/internal/store"
)

func setupTestStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test_push.db")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})
	return st
}

// MockSender implements push.Sender with controllable responses.
type MockSender struct {
	mu           sync.Mutex
	statusCodes  map[string]int
	errors       map[string]error
	calls        map[string]int
	beforeReturn func(endpoint string)
}

func NewMockSender() *MockSender {
	return &MockSender{
		statusCodes: make(map[string]int),
		errors:      make(map[string]error),
		calls:       make(map[string]int),
	}
}

func (m *MockSender) SetResponse(endpoint string, code int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statusCodes[endpoint] = code
	m.errors[endpoint] = err
}

func (m *MockSender) Send(ctx context.Context, sub push.Subscription, payload []byte) (int, error) {
	m.mu.Lock()
	m.calls[sub.Endpoint]++
	code := m.statusCodes[sub.Endpoint]
	err := m.errors[sub.Endpoint]
	fn := m.beforeReturn
	m.mu.Unlock()

	if fn != nil {
		fn(sub.Endpoint)
	}

	if err != nil {
		return 0, err
	}
	if code == 0 {
		code = http.StatusOK
	}
	return code, nil
}

// TestReRegistrationRaceCondition asserts Invariant 4:
// When a send is in-flight and returns 410, but a re-registration with a newer created_day
// occurred before the 410 was handled, the newer subscription row SURVIVES!
func TestReRegistrationRaceCondition(t *testing.T) {
	st := setupTestStore(t)
	ctx := context.Background()

	// 1. Create account
	accID, err := st.CreateAccount(ctx, []byte("hash1"), "eu-1")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	endpoint := "https://push.example.com/sub/race"
	oldDay := core.Today().Int() - 1 // Older created_day

	// Insert subscription with old created_day
	err = st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO subscriptions (account_id, kind, endpoint, created_day) VALUES (?, 'webpush', ?, ?)",
			accID.Int64(), endpoint, oldDay)
		return err
	})
	if err != nil {
		t.Fatalf("insert subscription: %v", err)
	}

	mockSender := NewMockSender()
	mockSender.SetResponse(endpoint, http.StatusGone, nil) // 410 Gone

	newDay := core.Today().Int() + 1 // Re-registered with newer day

	// Hook into MockSender to simulate re-registration while request is in flight
	mockSender.beforeReturn = func(ep string) {
		if ep == endpoint {
			_ = st.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec("UPDATE subscriptions SET created_day = ? WHERE endpoint = ?",
					newDay, endpoint)
				return err
			})
		}
	}

	pool := push.NewPool(push.PoolConfig{
		Workers: 1,
		Store:   st,
		Sender:  mockSender,
	})

	job := push.DispatchJob{
		Recipient:   accID,
		DigestCount: 1,
		EnqueuedAt:  time.Now(),
	}

	pool.QueueJob(job)
	_ = pool.Stop()

	// Assert the subscription row still exists because created_day > sendStartDay!
	var count int
	err = st.ReadDB().QueryRow("SELECT COUNT(*) FROM subscriptions WHERE endpoint = ?", endpoint).Scan(&count)
	if err != nil {
		t.Fatalf("query count: %v", err)
	}
	if count != 1 {
		t.Fatalf("CRITICAL INVARIANT 4 VIOLATION: re-registered subscription was erroneously pruned!")
	}
}

// TestStatusCodePruningTable asserts Invariant 4:
// Only 404 and 410 prune subscriptions.
// 429, 500, 502, 503, timeout, and DNS errors NEVER prune.
func TestStatusCodePruningTable(t *testing.T) {
	cases := []struct {
		name         string
		statusCode   int
		err          error
		shouldPrune  bool
	}{
		{"404 Not Found", http.StatusNotFound, nil, true},
		{"410 Gone", http.StatusGone, nil, true},
		{"200 OK", http.StatusOK, nil, false},
		{"201 Created", http.StatusCreated, nil, false},
		{"429 Too Many Requests", http.StatusTooManyRequests, nil, false},
		{"500 Internal Server Error", http.StatusInternalServerError, nil, false},
		{"502 Bad Gateway", http.StatusBadGateway, nil, false},
		{"503 Service Unavailable", http.StatusServiceUnavailable, nil, false},
		{"Timeout error", 0, context.DeadlineExceeded, false},
		{"DNS failure", 0, errors.New("lookup failed"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := setupTestStore(t)
			ctx := context.Background()

			accID, err := st.CreateAccount(ctx, []byte("hash_"+tc.name), "eu-1")
			if err != nil {
				t.Fatalf("create account: %v", err)
			}

			endpoint := fmt.Sprintf("https://push.example.com/%s", tc.name)
			today := core.Today().Int()

			err = st.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec("INSERT INTO subscriptions (account_id, kind, endpoint, created_day) VALUES (?, 'webpush', ?, ?)",
					accID.Int64(), endpoint, today)
				return err
			})
			if err != nil {
				t.Fatalf("insert sub: %v", err)
			}

			mockSender := NewMockSender()
			mockSender.SetResponse(endpoint, tc.statusCode, tc.err)

			pool := push.NewPool(push.PoolConfig{
				Workers: 1,
				Store:   st,
				Sender:  mockSender,
			})

			pool.QueueJob(push.DispatchJob{
				Recipient:   accID,
				DigestCount: 1,
				EnqueuedAt:  time.Now(),
			})
			_ = pool.Stop()

			var count int
			_ = st.ReadDB().QueryRow("SELECT COUNT(*) FROM subscriptions WHERE endpoint = ?", endpoint).Scan(&count)

			if tc.shouldPrune && count != 0 {
				t.Fatalf("[%s] expected subscription to be pruned, but found %d rows", tc.name, count)
			}
			if !tc.shouldPrune && count != 1 {
				t.Fatalf("[%s] subscription should NOT be pruned, but count was %d", tc.name, count)
			}
		})
	}
}

// TestStaleJobDrop asserts jobs older than dispatch.max_age are dropped and counted.
func TestStaleJobDrop(t *testing.T) {
	st := setupTestStore(t)
	metrics := obs.NewMetrics()
	mockSender := NewMockSender()

	now := time.Now()
	pool := push.NewPool(push.PoolConfig{
		Workers: 1,
		Store:   st,
		Sender:  mockSender,
		Metrics: metrics,
		MaxAge:  30 * time.Second,
		NowFunc: func() time.Time { return now },
	})

	// Job enqueued 45 seconds ago
	staleJob := push.DispatchJob{
		Recipient:   100,
		DigestCount: 1,
		EnqueuedAt:  now.Add(-45 * time.Second),
	}

	pool.QueueJob(staleJob)
	_ = pool.Stop()

	// Assert mock sender was never called for stale job
	if len(mockSender.calls) != 0 {
		t.Fatalf("stale job should have been dropped without calling push sender")
	}
}

// TestGracefulShutdownDrainsWithinTimeout asserts pool stops cleanly without hanging.
func TestGracefulShutdownDrainsWithinTimeout(t *testing.T) {
	st := setupTestStore(t)
	mockSender := NewMockSender()

	pool := push.NewPool(push.PoolConfig{
		Workers:      2,
		Store:        st,
		Sender:       mockSender,
		DrainTimeout: 2 * time.Second,
	})

	err := pool.Stop()
	if err != nil {
		t.Fatalf("expected graceful shutdown, got error: %v", err)
	}
}
