package push_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/obs"
	"github.com/halpworld/halpcheers/server/internal/push"
	"github.com/halpworld/halpcheers/server/internal/push/webpush"
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

// TestDefaultSender_Payloadless verifies that DigestCount == 1 sends an empty body,
// no Content-Encoding header, and a valid Authorization header signed with VAPID.
func TestDefaultSender_Payloadless(t *testing.T) {
	var (
		mu           sync.Mutex
		receivedReqs int
		authHeader   string
		contentEnc   string
		receivedBody []byte
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedReqs++
		authHeader = r.Header.Get("Authorization")
		contentEnc = r.Header.Get("Content-Encoding")
		body, _ := io.ReadAll(r.Body)
		receivedBody = body
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	st := setupTestStore(t)
	ctx := context.Background()

	targetURL := ts.URL + "/sub/payloadless"
	acc, err := st.CreateAccount(ctx, []byte("hash_pl"), "eu-1")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	today := core.Today().Int()
	err = st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO subscriptions (account_id, kind, endpoint, created_day) VALUES (?, 'webpush', ?, ?)",
			acc.Int64(), targetURL, today)
		return err
	})
	if err != nil {
		t.Fatalf("insert subscription: %v", err)
	}

	sender := push.NewDefaultSender()
	pool := push.NewPool(push.PoolConfig{
		Workers: 1,
		Store:   st,
		Sender:  sender,
	})

	job := push.DispatchJob{
		Recipient:   acc,
		DigestCount: 1,
		EnqueuedAt:  time.Now(),
	}

	pool.QueueJob(job)
	_ = pool.Stop()

	mu.Lock()
	defer mu.Unlock()

	if receivedReqs != 1 {
		t.Fatalf("expected 1 dispatch request, got %d", receivedReqs)
	}
	if len(receivedBody) != 0 {
		t.Fatalf("expected empty body for payloadless dispatch, got %d bytes", len(receivedBody))
	}
	if contentEnc != "" {
		t.Fatalf("expected no Content-Encoding header for payloadless dispatch, got %q", contentEnc)
	}
	if !strings.HasPrefix(authHeader, "vapid ") {
		t.Fatalf("expected Authorization header starting with 'vapid ', got %q", authHeader)
	}
	sub, _, err := webpush.VerifyVAPID(authHeader, ts.URL)
	if err != nil {
		t.Fatalf("VerifyVAPID failed: %v", err)
	}
	if sub != "mailto:ops@halp.to" {
		t.Fatalf("expected sub 'mailto:ops@halp.to', got %q", sub)
	}
}

// TestDefaultSender_EncryptedDigest verifies that DigestCount == 5 produces an RFC 8291/8188
// encrypted message with Content-Encoding aes128gcm which decrypts to {"n":5}.
func TestDefaultSender_EncryptedDigest(t *testing.T) {
	uaPrivKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ua key: %v", err)
	}
	uaPubKey := uaPrivKey.PublicKey().Bytes()
	authSecret := make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		t.Fatalf("rand auth: %v", err)
	}

	var (
		mu           sync.Mutex
		capturedBody []byte
		capturedEnc  string
		capturedAuth string
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		capturedBody = body
		capturedEnc = r.Header.Get("Content-Encoding")
		capturedAuth = r.Header.Get("Authorization")
		mu.Unlock()

		w.WriteHeader(http.StatusCreated)
	}))
	defer ts.Close()

	st := setupTestStore(t)
	ctx := context.Background()

	targetURL := ts.URL + "/sub/encrypted"
	acc, err := st.CreateAccount(ctx, []byte("hash_enc"), "eu-1")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	today := core.Today().Int()
	err = st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO subscriptions (account_id, kind, endpoint, p256dh, auth, created_day) VALUES (?, 'webpush', ?, ?, ?, ?)",
			acc.Int64(), targetURL, uaPubKey, authSecret, today)
		return err
	})
	if err != nil {
		t.Fatalf("insert subscription: %v", err)
	}

	sender := push.NewDefaultSender()
	pool := push.NewPool(push.PoolConfig{
		Workers: 1,
		Store:   st,
		Sender:  sender,
	})

	job := push.DispatchJob{
		Recipient:   acc,
		DigestCount: 5,
		EnqueuedAt:  time.Now(),
	}

	pool.QueueJob(job)
	_ = pool.Stop()

	mu.Lock()
	defer mu.Unlock()

	if capturedEnc != "aes128gcm" {
		t.Fatalf("expected Content-Encoding 'aes128gcm', got %q", capturedEnc)
	}
	if len(capturedBody) == 0 {
		t.Fatalf("expected non-empty encrypted body")
	}
	if !strings.HasPrefix(capturedAuth, "vapid ") {
		t.Fatalf("expected Authorization header with 'vapid ', got %q", capturedAuth)
	}

	decrypted, err := webpush.Decrypt(uaPrivKey, authSecret, capturedBody)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	expectedPlaintext := `{"n":5}`
	if string(decrypted) != expectedPlaintext {
		t.Fatalf("expected decrypted payload %q, got %q", expectedPlaintext, string(decrypted))
	}
}

// TestDefaultSender_Retry verifies that DefaultSender performs a single retry
// on 503 or 429 status codes, honoring Retry-After, and does not retry on non-retryable codes.
func TestDefaultSender_Retry(t *testing.T) {
	t.Run("503 retry succeeds", func(t *testing.T) {
		var attempts int
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts++
			if attempts == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer ts.Close()

		sender := push.NewDefaultSender()
		testSub := push.Subscription{
			Endpoint: ts.URL + "/retry-503",
		}

		status, err := sender.Send(context.Background(), testSub, nil)
		if err != nil {
			t.Fatalf("Send failed: %v", err)
		}
		if status != http.StatusOK {
			t.Fatalf("expected status 200 after retry, got %d", status)
		}
		if attempts != 2 {
			t.Fatalf("expected 2 attempts, got %d", attempts)
		}
	})

	t.Run("429 retry succeeds", func(t *testing.T) {
		var attempts int
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts++
			if attempts == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer ts.Close()

		sender := push.NewDefaultSender()
		testSub := push.Subscription{
			Endpoint: ts.URL + "/retry-429",
		}

		status, err := sender.Send(context.Background(), testSub, nil)
		if err != nil {
			t.Fatalf("Send failed: %v", err)
		}
		if status != http.StatusOK {
			t.Fatalf("expected status 200 after retry, got %d", status)
		}
		if attempts != 2 {
			t.Fatalf("expected 2 attempts, got %d", attempts)
		}
	})

	t.Run("404 does not retry", func(t *testing.T) {
		var attempts int
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts++
			w.WriteHeader(http.StatusNotFound)
		}))
		defer ts.Close()

		sender := push.NewDefaultSender()
		testSub := push.Subscription{
			Endpoint: ts.URL + "/retry-404",
		}

		status, err := sender.Send(context.Background(), testSub, nil)
		if err != nil {
			t.Fatalf("Send failed: %v", err)
		}
		if status != http.StatusNotFound {
			t.Fatalf("expected status 404, got %d", status)
		}
		if attempts != 1 {
			t.Fatalf("expected exactly 1 attempt for 404, got %d", attempts)
		}
	})
}

// TestCacheInvalidationOnGone verifies that on 410 Gone, any cached keypair for that endpoint is invalidated.
func TestCacheInvalidationOnGone(t *testing.T) {
	cache := webpush.NewKeypairCache()
	targetURL := "https://push.example.com/test-gone-cache"
	cache.Put(targetURL, &webpush.CachedSubscriptionSecret{})

	if _, ok := cache.Get(targetURL); !ok {
		t.Fatalf("expected item to be in cache before test")
	}

	st := setupTestStore(t)
	ctx := context.Background()

	acc, err := st.CreateAccount(ctx, []byte("hash_gone"), "eu-1")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	today := core.Today().Int()
	_ = st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO subscriptions (account_id, kind, endpoint, created_day) VALUES (?, 'webpush', ?, ?)",
			acc.Int64(), targetURL, today)
		return err
	})

	mockSender := NewMockSender()
	mockSender.SetResponse(targetURL, http.StatusGone, nil)

	pool := push.NewPool(push.PoolConfig{
		Workers: 1,
		Store:   st,
		Sender:  mockSender,
		Cache:   cache,
	})

	pool.QueueJob(push.DispatchJob{
		Recipient:   acc,
		DigestCount: 1,
		EnqueuedAt:  time.Now(),
	})
	_ = pool.Stop()

	if _, ok := cache.Get(targetURL); ok {
		t.Fatalf("expected cache entry to be invalidated on 410 Gone")
	}
}
