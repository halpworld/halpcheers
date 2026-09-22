package region_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/region"
	"github.com/halpworld/halpcheers/server/internal/store"
)

func setupTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_region.db")
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})
	return st
}

func TestMintHandleEntropyAndPrefix(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		h, err := region.GenerateHandle()
		if err != nil {
			t.Fatalf("GenerateHandle failed: %v", err)
		}
		raw := h.Raw()
		if len(raw) != region.HandleLength {
			t.Fatalf("expected handle length %d, got %d for %s", region.HandleLength, len(raw), raw)
		}
		if !strings.HasPrefix(raw, region.RegionPrefix) {
			t.Fatalf("expected region prefix %s, got %s", region.RegionPrefix, raw)
		}

		// Verify Crockford base32 characters
		for _, char := range raw[1:] {
			if !strings.ContainsRune(region.CrockfordAlphabet, char) {
				t.Fatalf("invalid character %c in Crockford base32 handle %s", char, raw)
			}
		}

		// Verify uniqueness across samples
		if _, exists := seen[raw]; exists {
			t.Fatalf("unexpected collision in random handle sample: %s", raw)
		}
		seen[raw] = struct{}{}
	}
}

func TestBurnedHandleNeverReissued(t *testing.T) {
	st := setupTestStore(t)
	res := region.NewResolver(st, 100)
	svc := region.NewService(st, res)

	ctx := context.Background()
	// Create an account
	err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO accounts (id, key_hash, created_day, last_seen_day) VALUES (1, X'01020304', 1, 1)")
		return err
	})
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	h, err := svc.MintHandle(ctx)
	if err != nil {
		t.Fatalf("MintHandle failed: %v", err)
	}

	// Insert into DB
	err = st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO handles (handle, account_id, kind, created_day) VALUES (?, 1, 'personal', 1)", h.Raw())
		return err
	})
	if err != nil {
		t.Fatalf("insert handle failed: %v", err)
	}

	// Burn the handle
	if err := svc.BurnHandle(ctx, 1, h); err != nil {
		t.Fatalf("BurnHandle failed: %v", err)
	}

	if !svc.IsBurned(h) {
		t.Fatalf("expected handle to be marked burned")
	}

	// Resolver must reject burned handle
	_, _, ok := res.Resolve(ctx, h.Raw())
	if ok {
		t.Fatalf("resolver must reject burned handle")
	}
}

func TestResolverUniformityAcrossStates(t *testing.T) {
	st := setupTestStore(t)
	res := region.NewResolver(st, 100)
	svc := region.NewService(st, res)
	ctx := context.Background()

	// Create test account
	err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO accounts (id, key_hash, created_day, last_seen_day) VALUES (10, X'11223344', 1, 1)")
		return err
	})
	if err != nil {
		t.Fatalf("failed creating account: %v", err)
	}

	liveHandle := core.Handle("elive12345678")
	pausedHandle := core.Handle("epaused123456")
	burnedHandle := core.Handle("eburned123456")
	missingHandle := core.Handle("emissing12345")

	err = st.Write(ctx, func(tx *sql.Tx) error {
		_, _ = tx.ExecContext(ctx, "INSERT INTO handles (handle, account_id, kind, paused, created_day) VALUES (?, 10, 'personal', 0, 1)", liveHandle.Raw())
		_, _ = tx.ExecContext(ctx, "INSERT INTO handles (handle, account_id, kind, paused, created_day) VALUES (?, 10, 'personal', 1, 1)", pausedHandle.Raw())
		_, _ = tx.ExecContext(ctx, "INSERT INTO handles (handle, account_id, kind, paused, created_day) VALUES (?, 10, 'personal', 0, 1)", burnedHandle.Raw())
		return nil
	})
	if err != nil {
		t.Fatalf("setup handles failed: %v", err)
	}

	// Burn burnedHandle
	_ = svc.BurnHandle(ctx, 10, burnedHandle)

	// Live resolves ok=true
	acc, h, ok := res.Resolve(ctx, liveHandle.Raw())
	if !ok || acc != 10 || h != liveHandle {
		t.Fatalf("expected live handle to resolve successfully")
	}

	// Paused resolves ok=false
	_, _, ok = res.Resolve(ctx, pausedHandle.Raw())
	if ok {
		t.Fatalf("expected paused handle to return ok=false")
	}

	// Burned resolves ok=false
	_, _, ok = res.Resolve(ctx, burnedHandle.Raw())
	if ok {
		t.Fatalf("expected burned handle to return ok=false")
	}

	// Nonexistent resolves ok=false
	_, _, ok = res.Resolve(ctx, missingHandle.Raw())
	if ok {
		t.Fatalf("expected missing handle to return ok=false")
	}
}

func TestResolverLRUFixedCapacityBound(t *testing.T) {
	st := setupTestStore(t)
	capacity := 10
	res := region.NewResolver(st, capacity)
	ctx := context.Background()

	// Populate beyond capacity
	for i := 0; i < 50; i++ {
		fakeHandle := core.Handle("ehandle" + strings.Repeat("a", i%10) + string(rune('0'+(i%10))))
		res.Resolve(ctx, fakeHandle.Raw())
	}

	if res.Len() > capacity {
		t.Fatalf("LRU size %d exceeded capacity %d (AGENTS.md Invariant 8)", res.Len(), capacity)
	}
}

func TestReservedAliases(t *testing.T) {
	for name := range region.ReservedAliases {
		if !region.IsReservedAlias(name) {
			t.Errorf("expected %s to be recognized as reserved", name)
		}
		if !region.IsReservedAlias("@" + name) {
			t.Errorf("expected @%s to be recognized as reserved", name)
		}
		if !region.IsReservedAlias(strings.ToUpper(name)) {
			t.Errorf("expected uppercase %s to be recognized as reserved", name)
		}
	}

	if region.IsReservedAlias("mycustomalias") {
		t.Errorf("unreserved alias flagged as reserved")
	}
}
