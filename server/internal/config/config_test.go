package config_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/halpworld/halpcheers/server/internal/config"
)

// TestDocumentedDefaults asserts every default in docs/ARCHITECTURE.md § Configuration
// is faithfully implemented in the configuration defaults.
func TestDocumentedDefaults(t *testing.T) {
	cfg := config.NewDefault()
	startup := cfg.Startup
	rel := cfg.Reloadable()

	// Startup keys
	if startup.QueueSize != 65536 {
		t.Errorf("queue.size default: got %d, expected 65536", startup.QueueSize)
	}
	if startup.ShutdownDrainTimeout != 10*time.Second {
		t.Errorf("shutdown.drain_timeout default: got %v, expected 10s", startup.ShutdownDrainTimeout)
	}
	if startup.GuardExpectedDailyPings != 1000 {
		t.Errorf("guard.expected_daily_pings default: got %d, expected 1000", startup.GuardExpectedDailyPings)
	}
	if startup.GuardPairWindow != 24*time.Hour {
		t.Errorf("guard.pair.window default: got %v, expected 24h", startup.GuardPairWindow)
	}
	if startup.GuardPairMaxPersonal != 3 {
		t.Errorf("guard.pair.max (personal) default: got %d, expected 3", startup.GuardPairMaxPersonal)
	}
	if startup.GuardPairMaxStream != 10 {
		t.Errorf("guard.pair.max (stream) default: got %d, expected 10", startup.GuardPairMaxStream)
	}
	if startup.GuardPairSlotBytes != 1024*1024 {
		t.Errorf("guard.pair.slot_bytes floor default: got %d, expected 1048576 (1 MiB)", startup.GuardPairSlotBytes)
	}
	if startup.GuardSketchDepth != 4 {
		t.Errorf("guard.sketch.depth default: got %d, expected 4", startup.GuardSketchDepth)
	}
	if startup.GuardSketchWidth != 2048 {
		t.Errorf("guard.sketch.width default: got %d, expected 2048", startup.GuardSketchWidth)
	}

	// Reloadable keys
	if rel.DispatchMaxAge != 30*time.Second {
		t.Errorf("dispatch.max_age default: got %v, expected 30s", rel.DispatchMaxAge)
	}
	if rel.IdleDemoteAfter != 15*time.Minute {
		t.Errorf("idle.demote_after default: got %v, expected 15m", rel.IdleDemoteAfter)
	}
	if rel.GuardSenderHour != 20 {
		t.Errorf("guard.sender.hour default: got %d, expected 20", rel.GuardSenderHour)
	}
	if rel.GuardSenderDay != 100 {
		t.Errorf("guard.sender.day default: got %d, expected 100", rel.GuardSenderDay)
	}
	if rel.PowFloorMs != 10 {
		t.Errorf("pow.floor_ms default: got %d, expected 10", rel.PowFloorMs)
	}
	if rel.PowSignupMs != 1500 {
		t.Errorf("pow.signup_ms default: got %d, expected 1500", rel.PowSignupMs)
	}
	if rel.PowEpoch != 5*time.Minute {
		t.Errorf("pow.epoch default: got %v, expected 5m", rel.PowEpoch)
	}
	if rel.DigestWindowSeconds != 60 {
		t.Errorf("digest.window_s default: got %d, expected 60", rel.DigestWindowSeconds)
	}
	if rel.DigestMaxPerHour != 12 {
		t.Errorf("digest.max_per_hour default: got %d, expected 12", rel.DigestMaxPerHour)
	}
	if rel.GroupsMinSize != 5 {
		t.Errorf("groups.min_size default: got %d, expected 5", rel.GroupsMinSize)
	}
	if rel.GroupsMaxMembers != 500 {
		t.Errorf("groups.max_members default: got %d, expected 500", rel.GroupsMaxMembers)
	}
	if rel.GroupsMaxPerAccount != 20 {
		t.Errorf("groups.max_per_account default: got %d, expected 20", rel.GroupsMaxPerAccount)
	}
	if rel.AliasReleaseMonths != 12 {
		t.Errorf("alias.release_months default: got %d, expected 12", rel.AliasReleaseMonths)
	}
	if rel.ContactsMaxBytes != 64*1024 {
		t.Errorf("contacts.max_bytes default: got %d, expected 65536", rel.ContactsMaxBytes)
	}
	if rel.BlocksTTLDays != 365 {
		t.Errorf("blocks.ttl_days default: got %d, expected 365", rel.BlocksTTLDays)
	}
	if rel.SubscriptionsPruneAfterDays != 180 {
		t.Errorf("subscriptions.prune_after_days default: got %d, expected 180", rel.SubscriptionsPruneAfterDays)
	}
}

// TestOneMiBFloorEnforced asserts that the 1 MiB floor per cascade slot per window
// is strictly maintained regardless of how tiny expected_daily_pings is set.
func TestOneMiBFloorEnforced(t *testing.T) {
	tinyInputs := []int{0, 1, 10, 100, 1000, 500000}
	for _, in := range tinyInputs {
		bytes := config.CalculateSlotBytes(in)
		if bytes < config.MinFilterSlotBytes {
			t.Errorf("for expected pings %d, calculated %d bytes, below 1 MiB floor %d", in, bytes, config.MinFilterSlotBytes)
		}
	}

	// For a massive input (e.g. 100M pings/day), slot bytes should scale above 1 MiB
	hugeInput := 100_000_000
	hugeBytes := config.CalculateSlotBytes(hugeInput)
	if hugeBytes <= config.MinFilterSlotBytes {
		t.Errorf("expected huge input to scale above floor, got %d", hugeBytes)
	}
}

// TestSIGHUPReloadUnderRace exercises concurrent hot-path reads while reloads occur,
// asserting race-freedom under go test -race.
func TestSIGHUPReloadUnderRace(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "halp.toml")

	initialContent := `
[queue]
size = 65536

[dispatch]
max_age = "30s"

[digest]
window_s = 60
max_per_hour = 12
`
	if err := os.WriteFile(confPath, []byte(initialContent), 0600); err != nil {
		t.Fatalf("write initial config: %v", err)
	}

	cfg, err := config.Load(confPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Hot-path readers
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					rel := cfg.Reloadable()
					if rel.DigestWindowSeconds <= 0 {
						t.Errorf("unexpected invalid window: %d", rel.DigestWindowSeconds)
					}
				}
			}
		}()
	}

	// Perform multiple concurrent reloads
	for step := 1; step <= 5; step++ {
		newContent := `
[queue]
size = 65536

[dispatch]
max_age = "45s"

[digest]
window_s = 90
max_per_hour = 20
`
		if err := os.WriteFile(confPath, []byte(newContent), 0600); err != nil {
			t.Fatalf("write updated config: %v", err)
		}
		reloaded, err := cfg.Reload()
		if err != nil {
			t.Fatalf("reload failed: %v", err)
		}
		if !reloaded {
			t.Fatalf("expected successful reload")
		}

		rel := cfg.Reloadable()
		if rel.DigestWindowSeconds != 90 || rel.DigestMaxPerHour != 20 || rel.DispatchMaxAge != 45*time.Second {
			t.Fatalf("reloaded fields mismatch: %+v", rel)
		}
	}

	close(stop)
	wg.Wait()
}

// TestRejectStartupOnlyKeyChange asserts that modifying a startup-only key
// causes cfg.Reload() to fail with ErrStartupKeyChanged and leaves current config intact.
func TestRejectStartupOnlyKeyChange(t *testing.T) {
	dir := t.TempDir()
	confPath := filepath.Join(dir, "halp.toml")

	initialContent := `
[queue]
size = 65536

[digest]
window_s = 60
`
	if err := os.WriteFile(confPath, []byte(initialContent), 0600); err != nil {
		t.Fatalf("write initial config: %v", err)
	}

	cfg, err := config.Load(confPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	// Attempt to change startup-only key queue.size from 65536 to 32768
	modifiedContent := `
[queue]
size = 32768

[digest]
window_s = 120
`
	if err := os.WriteFile(confPath, []byte(modifiedContent), 0600); err != nil {
		t.Fatalf("write modified config: %v", err)
	}

	reloaded, err := cfg.Reload()
	if err == nil || err != config.ErrStartupKeyChanged {
		t.Fatalf("expected ErrStartupKeyChanged, got reloaded=%v, err=%v", reloaded, err)
	}

	// Assert reloadable configuration was NOT updated due to startup key violation
	if cfg.Reloadable().DigestWindowSeconds != 60 {
		t.Fatalf("expected digest window to remain 60, got %d", cfg.Reloadable().DigestWindowSeconds)
	}
}
