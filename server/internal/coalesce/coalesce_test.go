package coalesce_test

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/halpworld/halpcheers/server/internal/coalesce"
	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/obs"
)

// TestInvariant1NoSenderIdentityInAccumulator verifies that the internal structures
// of the coalescer cannot hold sender identities or message records (AGENTS.md Invariant 1).
func TestInvariant1NoSenderIdentityInAccumulator(t *testing.T) {
	c := coalesce.New(coalesce.Config{})
	val := reflect.ValueOf(c).Elem()

	// Inspect the items field type
	itemsField := val.FieldByName("items")
	if !itemsField.IsValid() {
		t.Fatalf("coalescer must contain items map")
	}

	mapType := itemsField.Type()
	if mapType.Key() != reflect.TypeOf(core.AccountID(0)) {
		t.Fatalf("accumulator map key must be core.AccountID")
	}

	elemType := mapType.Elem()
	if elemType.Kind() == reflect.Pointer {
		elemType = elemType.Elem()
	}

	// Verify fields of accumulator entry
	for i := 0; i < elemType.NumField(); i++ {
		f := elemType.Field(i)
		name := f.Name
		if name == "Sender" || name == "SenderAccount" || name == "Handle" || name == "Message" || name == "History" {
			t.Fatalf("CRITICAL INVARIANT 1 VIOLATION: accumulator entry contains forbidden field %s", name)
		}
	}
}

// TestCoalescingFlushCycle verifies that incoming jobs accumulate counts and emit accurate digests.
func TestCoalescingFlushCycle(t *testing.T) {
	metrics := obs.NewMetrics()
	c := coalesce.New(coalesce.Config{
		DefaultWindow: 60 * time.Second,
		Metrics:       metrics,
	})

	now := time.Now()
	recipient1 := core.AccountID(1001)
	recipient2 := core.AccountID(1002)

	// Add 3 pings for recipient1 and 1 for recipient2
	c.AddAt(core.PingJob{Recipient: recipient1, EnqueuedAt: now}, now)
	c.AddAt(core.PingJob{Recipient: recipient1, EnqueuedAt: now}, now.Add(5*time.Second))
	c.AddAt(core.PingJob{Recipient: recipient1, EnqueuedAt: now}, now.Add(10*time.Second))
	c.AddAt(core.PingJob{Recipient: recipient2, EnqueuedAt: now}, now)

	// Flush before window expires should return nothing
	digests := c.Flush(now.Add(30 * time.Second))
	if len(digests) != 0 {
		t.Fatalf("expected 0 digests before window expires, got %d", len(digests))
	}

	// Flush after window expires should emit digests with accurate counts
	digests = c.Flush(now.Add(61 * time.Second))
	if len(digests) != 2 {
		t.Fatalf("expected 2 digests after window, got %d", len(digests))
	}

	counts := make(map[core.AccountID]int)
	for _, d := range digests {
		counts[d.Recipient] = d.Count
	}

	if counts[recipient1] != 3 {
		t.Errorf("recipient1 count: expected 3, got %d", counts[recipient1])
	}
	if counts[recipient2] != 1 {
		t.Errorf("recipient2 count: expected 1, got %d", counts[recipient2])
	}

	// Subsequent flush should be empty (accumulator cleared)
	digests = c.Flush(now.Add(70 * time.Second))
	if len(digests) != 0 {
		t.Fatalf("expected empty accumulator after flush, got %d digests", len(digests))
	}
}

// TestStaleJobDrop verifies that jobs exceeding dispatch.max_age are dropped and counted.
func TestStaleJobDrop(t *testing.T) {
	metrics := obs.NewMetrics()
	c := coalesce.New(coalesce.Config{
		MaxAge:  30 * time.Second,
		Metrics: metrics,
	})

	now := time.Now()
	recipient := core.AccountID(2001)

	// Fresh job should be accepted
	freshJob := core.PingJob{
		Recipient:  recipient,
		EnqueuedAt: now.Add(-10 * time.Second),
	}
	if !c.AddAt(freshJob, now) {
		t.Fatalf("expected fresh job to be accepted")
	}

	// Stale job (40s old > 30s max age) must be dropped
	staleJob := core.PingJob{
		Recipient:  recipient,
		EnqueuedAt: now.Add(-40 * time.Second),
	}
	if c.AddAt(staleJob, now) {
		t.Fatalf("expected stale job to be dropped")
	}

	// Verify accumulator only recorded the 1 fresh job
	digests := c.FlushAll()
	if len(digests) != 1 || digests[0].Count != 1 {
		t.Fatalf("expected 1 digest with count 1, got %+v", digests)
	}
}

// TestGetAndClearPending verifies the /v1/pending endpoint semantics.
func TestGetAndClearPending(t *testing.T) {
	c := coalesce.New(coalesce.Config{})
	now := time.Now()
	account := core.AccountID(3001)

	c.AddAt(core.PingJob{Recipient: account, EnqueuedAt: now}, now)
	c.AddAt(core.PingJob{Recipient: account, EnqueuedAt: now}, now)
	c.AddAt(core.PingJob{Recipient: account, EnqueuedAt: now}, now)

	// First call returns 3
	pending := c.GetAndClearPending(account)
	if pending != 3 {
		t.Fatalf("expected pending 3, got %d", pending)
	}

	// Second call returns 0
	second := c.GetAndClearPending(account)
	if second != 0 {
		t.Fatalf("expected pending 0 on subsequent call, got %d", second)
	}
}

// TestBoundedCapacity verifies that the accumulator does not grow beyond its max capacity (Invariant 8).
func TestBoundedCapacity(t *testing.T) {
	metrics := obs.NewMetrics()
	c := coalesce.New(coalesce.Config{
		MaxRecipients: 5,
		Metrics:       metrics,
	})
	now := time.Now()

	for i := 1; i <= 5; i++ {
		accepted := c.AddAt(core.PingJob{Recipient: core.AccountID(i), EnqueuedAt: now}, now)
		if !accepted {
			t.Fatalf("expected recipient %d to be accepted within capacity", i)
		}
	}

	// 6th recipient should be rejected due to capacity limit
	overflow := c.AddAt(core.PingJob{Recipient: core.AccountID(6), EnqueuedAt: now}, now)
	if overflow {
		t.Fatalf("expected 6th recipient to be rejected under bounded capacity")
	}
}

// TestConcurrentFlushAndAdd ensures thread safety under race detector.
func TestConcurrentFlushAndAdd(t *testing.T) {
	c := coalesce.New(coalesce.Config{
		DefaultWindow: 5 * time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	var digestCount int
	var countMu sync.Mutex

	// Flush loop
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.RunFlushLoop(ctx, 2*time.Millisecond, func(d core.Digest) {
			countMu.Lock()
			digestCount += d.Count
			countMu.Unlock()
		})
	}()

	// Ingress workers
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				rec := core.AccountID((i % 5) + 1)
				c.Add(core.PingJob{Recipient: rec, EnqueuedAt: time.Now()})
				time.Sleep(1 * time.Millisecond)
			}
		}(w)
	}

	wg.Wait()
}
