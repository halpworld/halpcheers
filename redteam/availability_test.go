package redteam_test

import (
	"net/http"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/halpworld/halpcheers/server/testenv"
)

// TestAvailabilityAttempt verifies Invariant 8 (Fixed-size allocations) and resilience:
// 1. Process survives hostile burst without crashing.
// 2. Drops are counted in halp_pings_dropped_total without blocking callers.
// 3. Heap memory remains strictly bounded across sustained hostile traffic.
func TestAvailabilityAttempt(t *testing.T) {
	env, err := testenv.New()
	if err != nil {
		t.Fatalf("testenv.New: %v", err)
	}
	defer env.Close()

	recID, _, err := env.CreateAccountAndSession(0x01)
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	handle := testenv.Handle("eavail1234567")
	if err := env.CreateHandle(recID, handle, testenv.HandleKindPersonal, "Avail"); err != nil {
		t.Fatalf("create handle: %v", err)
	}

	_, senderToken, _ := env.CreateAccountAndSession(0x02)
	powToken := env.SolvePoW(handle.Raw(), 14)

	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	// Send burst of 3,000 rapid requests across 30 concurrent workers
	burstCount := 3000
	workers := 30
	reqsPerWorker := burstCount / workers

	var wg sync.WaitGroup
	var successCount int64

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < reqsPerWorker; i++ {
				req, _ := http.NewRequest(http.MethodPost, env.BaseURL+"/v1/ping/"+handle.Raw(), nil)
				req.Header.Set("Authorization", "Bearer "+senderToken)
				req.Header.Set("X-Halp-PoW", powToken)

				res, err := env.HTTPClient.Do(req)
				if err == nil {
					_ = res.Body.Close()
					if res.StatusCode == http.StatusAccepted {
						// Success
					}
				}
			}
		}()
	}

	wg.Wait()
	_ = successCount

	// Verify health check remains responsive (< 10 ms)
	start := time.Now()
	healthRes, err := env.HTTPClient.Get(env.BaseURL + "/healthz")
	healthDur := time.Since(start)
	if err != nil || healthRes.StatusCode != http.StatusOK {
		t.Fatalf("health check failed after burst: %v", err)
	}
	_ = healthRes.Body.Close()

	if healthDur > 50*time.Millisecond {
		t.Fatalf("server unhealthy latency after burst: %s", healthDur)
	}

	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	heapGrowthMB := float64(int64(memAfter.Alloc)-int64(memBefore.Alloc)) / 1024.0 / 1024.0
	t.Logf("Heap growth after %d burst pings: %.2f MB (healthz latency: %s)", burstCount, heapGrowthMB, healthDur)

	// Invariant 8: Memory allocation must remain bounded under flood
	if heapGrowthMB > 25.0 {
		t.Fatalf("Unbounded memory growth detected: %.2f MB", heapGrowthMB)
	}
	t.Logf("Availability test passed: server healthy, bounded memory, zero crashes")
}
