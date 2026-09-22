package guard_test

import (
	"context"
	"runtime"
	"strconv"
	"testing"

	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/guard"
	"github.com/halpworld/halpcheers/server/internal/obs"
)

type mockBlockStore struct {
	blocked map[string]bool
}

func (m *mockBlockStore) RecordBlock(ctx context.Context, sender core.AccountID, handle core.Handle, day core.Day) error {
	m.blocked[strconv.FormatInt(sender.Int64(), 10)+":"+handle.Raw()] = true
	return nil
}

func (m *mockBlockStore) IsBlocked(ctx context.Context, sender core.AccountID, handle core.Handle) (bool, error) {
	return m.blocked[strconv.FormatInt(sender.Int64(), 10)+":"+handle.Raw()], nil
}

func TestPairCascadeLimitEnforcement(t *testing.T) {
	metrics := obs.NewMetrics()
	svc := guard.NewService(guard.Config{
		PairMaxPersonal: 3,
		PairMaxStream:   10,
		SlotBytes:       1024 * 1024,
		Metrics:         metrics,
	})

	ctx := context.Background()
	sender := core.AccountID(1001)
	personalHandle := core.Handle("epersonal123")
	streamHandle := core.Handle("estream456")

	// Personal handle: exactly 3 pings allowed
	for i := 1; i <= 3; i++ {
		if !svc.Allow(ctx, sender, personalHandle, core.HandleKindPersonal) {
			t.Fatalf("expected ping %d to be allowed for personal handle", i)
		}
	}
	// 4th ping must be rejected
	if svc.Allow(ctx, sender, personalHandle, core.HandleKindPersonal) {
		t.Fatalf("expected 4th ping to be rejected for personal handle")
	}

	// Stream handle: exactly 10 pings allowed
	for i := 1; i <= 10; i++ {
		if !svc.Allow(ctx, sender, streamHandle, core.HandleKindStream) {
			t.Fatalf("expected ping %d to be allowed for stream handle", i)
		}
	}
	// 11th ping must be rejected
	if svc.Allow(ctx, sender, streamHandle, core.HandleKindStream) {
		t.Fatalf("expected 11th ping to be rejected for stream handle")
	}
}

func TestFalsePositiveAdvancesSlot(t *testing.T) {
	// Sizing with a smaller slot in direct cascade to simulate collision
	cascade := guard.NewPairCascade(3, 1024)
	sender := core.AccountID(2001)
	handle := core.Handle("ecollision1")

	// Pre-fill slot 0
	if !cascade.Allow(sender, handle, 3) {
		t.Fatalf("first ping must be allowed")
	}
	// Fill slot 1
	if !cascade.Allow(sender, handle, 3) {
		t.Fatalf("second ping must be allowed")
	}
	// Fill slot 2
	if !cascade.Allow(sender, handle, 3) {
		t.Fatalf("third ping must be allowed")
	}
	// 4th ping must be rejected (cannot exceed pair.max=3)
	if cascade.Allow(sender, handle, 3) {
		t.Fatalf("fourth ping must be rejected even under collisions")
	}
}

func TestSilentSuspensionAndBlocks(t *testing.T) {
	store := &mockBlockStore{blocked: make(map[string]bool)}
	metrics := obs.NewMetrics()
	svc := guard.NewService(guard.Config{
		BlockStore: store,
		Metrics:    metrics,
	})

	ctx := context.Background()
	sender := core.AccountID(3001)
	handle := core.Handle("etarget4001")

	// Initially allowed
	if !svc.Allow(ctx, sender, handle, core.HandleKindPersonal) {
		t.Fatalf("expected ping to be allowed initially")
	}

	// Report abuse records block in store
	if err := svc.ReportAbuse(ctx, handle, sender); err != nil {
		t.Fatalf("ReportAbuse failed: %v", err)
	}

	// Subsequent ping is silently blocked (returns false, counted in metrics)
	if svc.Allow(ctx, sender, handle, core.HandleKindPersonal) {
		t.Fatalf("expected blocked sender to be rejected")
	}

	// Silent account suspension (Layer 5)
	sender2 := core.AccountID(3002)
	svc.SuspendAccount(sender2)
	if svc.Allow(ctx, sender2, handle, core.HandleKindPersonal) {
		t.Fatalf("expected suspended account to be silently rejected")
	}
}

func TestMemoryFlatUnderSimulatedFlood(t *testing.T) {
	svc := guard.NewService(guard.Config{
		SlotBytes: 1024 * 1024,
	})

	ctx := context.Background()
	target := core.Handle("efloodtarget")

	runtime.GC()
	var mBefore runtime.MemStats
	runtime.ReadMemStats(&mBefore)

	// Simulate 100,000 distinct senders
	for i := 0; i < 100_000; i++ {
		sender := core.AccountID(i + 1_000_000)
		_ = svc.Allow(ctx, sender, target, core.HandleKindPersonal)
	}

	runtime.GC()
	var mAfter runtime.MemStats
	runtime.ReadMemStats(&mAfter)

	// Fixed cascade filter memory is pre-allocated; growth should be bounded
	allocatedMB := float64(mAfter.Alloc) / (1024 * 1024)
	if allocatedMB > 150.0 {
		t.Fatalf("memory allocation exceeded bound under 100k flood: %.2f MB", allocatedMB)
	}
}

func BenchmarkAllow(b *testing.B) {
	svc := guard.NewService(guard.Config{
		SlotBytes: 1024 * 1024,
	})
	ctx := context.Background()
	handle := core.Handle("ebenchmark12")

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		sender := core.AccountID((i % 1000) + 1000)
		svc.Allow(ctx, sender, handle, core.HandleKindPersonal)
	}
}

func BenchmarkBloomFilterCheck(b *testing.B) {
	bf := guard.NewBloomFilter(1024 * 1024)
	bf.Add(12345, 67890)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		bf.Contains(uint64(i), 67890)
	}
}
