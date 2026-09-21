// Package config implements the complete tunables table for Halp.
//
// In accordance with docs/AGENT-WORKFLOW.md §3 and docs/ARCHITECTURE.md § Configuration:
// - All configuration keys exist from day one, including those whose consumers are Wave 1 tracks.
// - Hand-written TOML parsing without reflection frameworks (AGENTS.md Invariant 3).
// - Startup-only vs SIGHUP-reloadable split:
//     * Startup-only keys size fixed allocations (queue, workers, bloom filter slots, sketch)
//       and cannot be resized under load (AGENTS.md Invariant 8). Changes on SIGHUP are rejected.
//     * Reloadable keys (thresholds, windows, timeouts) are atomically hot-swapped via atomic.Pointer.
// - Sizing rule: guard.expected_daily_pings derives filter slot size with a hard 1 MiB floor
//   per slot per window (docs/ABUSE.md § Sizing rule).
package config

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// ErrStartupKeyChanged is returned when a config reload detects modifications to startup-only keys.
var ErrStartupKeyChanged = errors.New("cannot change startup-only configuration key on reload (restart required)")

const (
	// MinFilterSlotBytes is the hard 1 MiB floor per cascade slot per window (docs/ABUSE.md § Sizing rule).
	MinFilterSlotBytes = 1024 * 1024 // 1 MiB = 1,048,576 bytes
)

// StartupConfig holds settings that size fixed allocations or dictate process-level bindings.
// These are fixed at boot and CANNOT be changed at runtime (AGENTS.md Invariant 8).
type StartupConfig struct {
	// HTTPAddr is the bind address for the HTTP listener (e.g. ":8080").
	HTTPAddr string
	// DBPath is the path to the SQLite database file (e.g. "halp.db").
	DBPath string
	// QueueSize is the capacity of the bounded in-memory ping channel. Default: 65536.
	QueueSize int
	// DispatchWorkers is the number of delivery workers. Default: 4 * runtime.NumCPU().
	DispatchWorkers int
	// ShutdownDrainTimeout is the deadline for draining the queue on SIGTERM. Default: 10s.
	ShutdownDrainTimeout time.Duration

	// GuardExpectedDailyPings sizes the pair-filter cascade and count-min sketch. Default: 1000.
	GuardExpectedDailyPings int
	// GuardPairWindow is the rotation interval for the pair filter windows. Default: 24h.
	GuardPairWindow time.Duration
	// GuardPairMaxPersonal is the maximum daily pings per sender-recipient pair for personal handles. Default: 3.
	GuardPairMaxPersonal int
	// GuardPairMaxSocial is the maximum daily pings per pair for social handles. Default: 3.
	GuardPairMaxSocial int
	// GuardPairMaxGroup is the maximum daily pings per pair for group handles. Default: 3.
	GuardPairMaxGroup int
	// GuardPairMaxStream is the maximum daily pings per pair for stream handles. Default: 10.
	GuardPairMaxStream int
	// GuardPairSlotBytes is the computed byte allocation per cascade slot per window, enforcing the 1 MiB floor.
	GuardPairSlotBytes int

	// GuardSketchWidth is the width of the top-sender count-min sketch.
	GuardSketchWidth int
	// GuardSketchDepth is the depth of the top-sender count-min sketch. Default: 4.
	GuardSketchDepth int
}

// ReloadableConfig holds thresholds, rate limits, and time windows that can safely be
// reloaded at runtime via SIGHUP without reallocation or reinitialization.
type ReloadableConfig struct {
	// DispatchMaxAge is the expiration cutoff for stale ping jobs. Default: 30s.
	DispatchMaxAge time.Duration
	// IdleDemoteAfter is the inactivity timeout before SSE demotes to long-poll. Default: 15m.
	IdleDemoteAfter time.Duration

	// GuardSenderHour is the rate limit bucket for an account per hour. Default: 20.
	GuardSenderHour int
	// GuardSenderDay is the rate limit bucket for an account per day. Default: 100.
	GuardSenderDay int

	// PowFloorMs is the target client CPU work time in ms under normal load. Default: 10.
	PowFloorMs int
	// PowSignupMs is the target client CPU work time in ms for account signup. Default: 1500.
	PowSignupMs int
	// PowEpoch is the challenge rotation interval. Default: 5m.
	PowEpoch time.Duration

	// DigestWindowSeconds is the default coalescing window in seconds. Default: 60.
	DigestWindowSeconds int
	// DigestMaxPerHour is the maximum number of digest notifications per hour. Default: 12.
	DigestMaxPerHour int

	// GroupsMinSize is the minimum group size to avoid deanonymization. Default: 5.
	GroupsMinSize int
	// GroupsMaxMembers is the ceiling on members in a group. Default: 500.
	GroupsMaxMembers int
	// GroupsMaxPerAccount is the maximum groups an account can join. Default: 20.
	GroupsMaxPerAccount int

	// AliasReleaseMonths is the inactivity period after which an alias is released. Default: 12.
	AliasReleaseMonths int
	// ContactsMaxBytes is the hard ceiling on the opaque contacts blob. Default: 64 KiB (65536).
	ContactsMaxBytes int
	// BlocksTTLDays is the retention lifespan for durable blocks. Default: 365.
	BlocksTTLDays int
	// SubscriptionsPruneAfterDays is the idle lifespan before pruning dead subscriptions. Default: 180.
	SubscriptionsPruneAfterDays int
}

// Config wraps the static startup configuration and an atomic pointer to the current reloadable configuration.
type Config struct {
	Startup    StartupConfig
	reloadable atomic.Pointer[ReloadableConfig]
	filePath   string
}

// Reloadable returns a snapshot pointer to the current reloadable configuration.
// This is lock-free and allocation-free on the hot path.
func (c *Config) Reloadable() *ReloadableConfig {
	return c.reloadable.Load()
}

// CalculateSlotBytes derives the filter size from expected daily pings and enforces the 1 MiB floor.
func CalculateSlotBytes(expectedDailyPings int) int {
	// ~2 bytes per entry for <= 0.1% false-positive rate
	calculated := expectedDailyPings * 2
	if calculated < MinFilterSlotBytes {
		return MinFilterSlotBytes
	}
	return calculated
}

// CalculateSketchWidth computes the count-min sketch width from expected daily pings.
func CalculateSketchWidth(expectedDailyPings int) int {
	w := expectedDailyPings * 2
	if w < 2048 {
		return 2048
	}
	return w
}

// DefaultStartupConfig returns the documented default startup settings.
func DefaultStartupConfig() StartupConfig {
	workers := 4 * runtime.NumCPU()
	if workers < 4 {
		workers = 4
	}
	expectedPings := 1000
	return StartupConfig{
		HTTPAddr:                ":8080",
		DBPath:                  "halp.db",
		QueueSize:               65536,
		DispatchWorkers:         workers,
		ShutdownDrainTimeout:    10 * time.Second,
		GuardExpectedDailyPings: expectedPings,
		GuardPairWindow:         24 * time.Hour,
		GuardPairMaxPersonal:    3,
		GuardPairMaxSocial:      3,
		GuardPairMaxGroup:       3,
		GuardPairMaxStream:      10,
		GuardPairSlotBytes:      CalculateSlotBytes(expectedPings),
		GuardSketchWidth:        CalculateSketchWidth(expectedPings),
		GuardSketchDepth:        4,
	}
}

// DefaultReloadableConfig returns the documented default reloadable settings.
func DefaultReloadableConfig() ReloadableConfig {
	return ReloadableConfig{
		DispatchMaxAge:              30 * time.Second,
		IdleDemoteAfter:             15 * time.Minute,
		GuardSenderHour:             20,
		GuardSenderDay:              100,
		PowFloorMs:                  10,
		PowSignupMs:                 1500,
		PowEpoch:                    5 * time.Minute,
		DigestWindowSeconds:         60,
		DigestMaxPerHour:            12,
		GroupsMinSize:               5,
		GroupsMaxMembers:            500,
		GroupsMaxPerAccount:         20,
		AliasReleaseMonths:          12,
		ContactsMaxBytes:            64 * 1024, // 64 KiB = 65536
		BlocksTTLDays:               365,
		SubscriptionsPruneAfterDays: 180,
	}
}

// NewDefault returns a valid Config populated entirely with documented defaults.
func NewDefault() *Config {
	c := &Config{
		Startup:  DefaultStartupConfig(),
		filePath: "",
	}
	rel := DefaultReloadableConfig()
	c.reloadable.Store(&rel)
	return c
}

// Load reads a TOML configuration file from path.
// If path is empty, it returns default configuration.
func Load(path string) (*Config, error) {
	cfg := NewDefault()
	if path == "" {
		return cfg, nil
	}

	cfg.filePath = path
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config file: %w", err)
	}
	defer f.Close()

	if err := cfg.parseFrom(f); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return cfg, nil
}

// Validate ensures startup values are non-zero and within valid bounds.
func (c *Config) Validate() error {
	if c.Startup.QueueSize <= 0 {
		return fmt.Errorf("queue.size must be > 0, got %d", c.Startup.QueueSize)
	}
	if c.Startup.DispatchWorkers <= 0 {
		return fmt.Errorf("dispatch.workers must be > 0, got %d", c.Startup.DispatchWorkers)
	}
	if c.Startup.ShutdownDrainTimeout <= 0 {
		return fmt.Errorf("shutdown.drain_timeout must be > 0, got %v", c.Startup.ShutdownDrainTimeout)
	}
	if c.Startup.GuardExpectedDailyPings <= 0 {
		return fmt.Errorf("guard.expected_daily_pings must be > 0, got %d", c.Startup.GuardExpectedDailyPings)
	}
	if c.Startup.GuardPairWindow <= 0 {
		return fmt.Errorf("guard.pair.window must be > 0, got %v", c.Startup.GuardPairWindow)
	}
	if c.Startup.GuardPairMaxPersonal <= 0 {
		return fmt.Errorf("guard.pair.max (personal) must be > 0, got %d", c.Startup.GuardPairMaxPersonal)
	}
	if c.Startup.GuardPairMaxStream <= 0 {
		return fmt.Errorf("guard.pair.max (stream) must be > 0, got %d", c.Startup.GuardPairMaxStream)
	}
	if c.Startup.GuardPairSlotBytes < MinFilterSlotBytes {
		return fmt.Errorf("guard slot bytes %d is below 1 MiB floor %d", c.Startup.GuardPairSlotBytes, MinFilterSlotBytes)
	}

	rel := c.Reloadable()
	if rel.DispatchMaxAge <= 0 {
		return fmt.Errorf("dispatch.max_age must be > 0, got %v", rel.DispatchMaxAge)
	}
	if rel.IdleDemoteAfter <= 0 {
		return fmt.Errorf("idle.demote_after must be > 0, got %v", rel.IdleDemoteAfter)
	}
	if rel.GuardSenderHour <= 0 || rel.GuardSenderDay <= 0 {
		return fmt.Errorf("guard.sender limits must be > 0, got hour=%d, day=%d", rel.GuardSenderHour, rel.GuardSenderDay)
	}
	if rel.PowFloorMs < 0 || rel.PowSignupMs < 0 {
		return fmt.Errorf("pow timings must be >= 0")
	}
	if rel.DigestMaxPerHour <= 0 || rel.DigestMaxPerHour > 60 {
		return fmt.Errorf("digest.max_per_hour must be between 1 and 60, got %d", rel.DigestMaxPerHour)
	}
	if rel.GroupsMinSize < 2 {
		return fmt.Errorf("groups.min_size must be >= 2, got %d", rel.GroupsMinSize)
	}
	if rel.ContactsMaxBytes <= 0 {
		return fmt.Errorf("contacts.max_bytes must be > 0, got %d", rel.ContactsMaxBytes)
	}
	return nil
}

// Reload re-reads the config file, validates changes, and atomically updates reloadable fields.
// If any startup-only key changed, the reload is rejected with a warning log.
func (c *Config) Reload() (bool, error) {
	if c.filePath == "" {
		return false, nil
	}

	f, err := os.Open(c.filePath)
	if err != nil {
		return false, fmt.Errorf("re-open config file: %w", err)
	}
	defer f.Close()

	fresh := NewDefault()
	fresh.filePath = c.filePath
	if err := fresh.parseFrom(f); err != nil {
		return false, fmt.Errorf("parse reloaded config: %w", err)
	}
	if err := fresh.Validate(); err != nil {
		return false, fmt.Errorf("validate reloaded config: %w", err)
	}

	// Compare startup-only keys
	if fresh.Startup.QueueSize != c.Startup.QueueSize ||
		fresh.Startup.DispatchWorkers != c.Startup.DispatchWorkers ||
		fresh.Startup.ShutdownDrainTimeout != c.Startup.ShutdownDrainTimeout ||
		fresh.Startup.GuardExpectedDailyPings != c.Startup.GuardExpectedDailyPings ||
		fresh.Startup.GuardPairWindow != c.Startup.GuardPairWindow ||
		fresh.Startup.GuardPairMaxPersonal != c.Startup.GuardPairMaxPersonal ||
		fresh.Startup.GuardPairMaxSocial != c.Startup.GuardPairMaxSocial ||
		fresh.Startup.GuardPairMaxGroup != c.Startup.GuardPairMaxGroup ||
		fresh.Startup.GuardPairMaxStream != c.Startup.GuardPairMaxStream ||
		fresh.Startup.GuardSketchWidth != c.Startup.GuardSketchWidth ||
		fresh.Startup.GuardSketchDepth != c.Startup.GuardSketchDepth ||
		fresh.Startup.HTTPAddr != c.Startup.HTTPAddr ||
		fresh.Startup.DBPath != c.Startup.DBPath {
		log.Printf("WARNING: config reload rejected: startup-only key changed (requires process restart)")
		return false, ErrStartupKeyChanged
	}

	// Atomically swap reloadable configuration
	newRel := fresh.Reloadable()
	c.reloadable.Store(newRel)
	log.Printf("config reloaded successfully on SIGHUP")
	return true, nil
}

// ListenSIGHUP starts a background goroutine listening for SIGHUP to trigger atomic reloads.
func (c *Config) ListenSIGHUP(ctx context.Context) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-sigCh:
				log.Println("received SIGHUP, reloading configuration")
				if _, err := c.Reload(); err != nil {
					log.Printf("config reload failed: %v", err)
				}
			}
		}
	}()
}

// parseFrom parses a simple key-value / section-based TOML file by hand without reflection.
func (c *Config) parseFrom(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	currentSection := ""
	lineNum := 0

	rel := *c.Reloadable()

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Section header: [section] or [section.subsection]
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}

		// Key-value pair
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("line %d: invalid format %q", lineNum, line)
		}
		rawKey := strings.TrimSpace(parts[0])
		rawVal := strings.TrimSpace(parts[1])

		// Strip inline comments if value isn't quoted
		if !strings.HasPrefix(rawVal, `"`) {
			if idx := strings.Index(rawVal, "#"); idx != -1 {
				rawVal = strings.TrimSpace(rawVal[:idx])
			}
		} else if endQuote := strings.LastIndex(rawVal, `"`); endQuote > 0 {
			rawVal = strings.TrimSpace(rawVal[:endQuote+1])
		}

		// Combine section and key
		fullKey := rawKey
		if currentSection != "" && !strings.Contains(rawKey, ".") {
			fullKey = currentSection + "." + rawKey
		}

		if err := c.applyKey(fullKey, rawVal, &rel); err != nil {
			return fmt.Errorf("line %d (%s): %w", lineNum, fullKey, err)
		}
	}

	// Recalculate derived sizing based on parsed expected daily pings
	c.Startup.GuardPairSlotBytes = CalculateSlotBytes(c.Startup.GuardExpectedDailyPings)
	c.Startup.GuardSketchWidth = CalculateSketchWidth(c.Startup.GuardExpectedDailyPings)

	c.reloadable.Store(&rel)
	return scanner.Err()
}

func (c *Config) applyKey(key, val string, rel *ReloadableConfig) error {
	cleanVal := strings.Trim(val, `"`)

	switch key {
	// Startup-only keys
	case "http.addr":
		c.Startup.HTTPAddr = cleanVal
	case "db.path":
		c.Startup.DBPath = cleanVal
	case "queue.size":
		v, err := strconv.Atoi(cleanVal)
		if err != nil {
			return err
		}
		c.Startup.QueueSize = v
	case "dispatch.workers":
		v, err := strconv.Atoi(cleanVal)
		if err != nil {
			return err
		}
		c.Startup.DispatchWorkers = v
	case "shutdown.drain_timeout":
		d, err := time.ParseDuration(cleanVal)
		if err != nil {
			return err
		}
		c.Startup.ShutdownDrainTimeout = d
	case "guard.expected_daily_pings":
		v, err := strconv.Atoi(cleanVal)
		if err != nil {
			return err
		}
		c.Startup.GuardExpectedDailyPings = v
	case "guard.pair.window":
		d, err := time.ParseDuration(cleanVal)
		if err != nil {
			return err
		}
		c.Startup.GuardPairWindow = d
	case "guard.pair.max":
		v, err := strconv.Atoi(cleanVal)
		if err != nil {
			return err
		}
		c.Startup.GuardPairMaxPersonal = v
		c.Startup.GuardPairMaxSocial = v
		c.Startup.GuardPairMaxGroup = v
	case "guard.pair.max_stream":
		v, err := strconv.Atoi(cleanVal)
		if err != nil {
			return err
		}
		c.Startup.GuardPairMaxStream = v
	case "guard.sketch.width":
		v, err := strconv.Atoi(cleanVal)
		if err != nil {
			return err
		}
		c.Startup.GuardSketchWidth = v
	case "guard.sketch.depth":
		v, err := strconv.Atoi(cleanVal)
		if err != nil {
			return err
		}
		c.Startup.GuardSketchDepth = v

	// Reloadable keys
	case "dispatch.max_age":
		d, err := time.ParseDuration(cleanVal)
		if err != nil {
			return err
		}
		rel.DispatchMaxAge = d
	case "idle.demote_after":
		d, err := time.ParseDuration(cleanVal)
		if err != nil {
			return err
		}
		rel.IdleDemoteAfter = d
	case "guard.sender.hour":
		v, err := strconv.Atoi(cleanVal)
		if err != nil {
			return err
		}
		rel.GuardSenderHour = v
	case "guard.sender.day":
		v, err := strconv.Atoi(cleanVal)
		if err != nil {
			return err
		}
		rel.GuardSenderDay = v
	case "pow.floor_ms":
		v, err := strconv.Atoi(cleanVal)
		if err != nil {
			return err
		}
		rel.PowFloorMs = v
	case "pow.signup_ms":
		v, err := strconv.Atoi(cleanVal)
		if err != nil {
			return err
		}
		rel.PowSignupMs = v
	case "pow.epoch":
		d, err := time.ParseDuration(cleanVal)
		if err != nil {
			return err
		}
		rel.PowEpoch = d
	case "digest.window_s":
		v, err := strconv.Atoi(cleanVal)
		if err != nil {
			return err
		}
		rel.DigestWindowSeconds = v
	case "digest.max_per_hour":
		v, err := strconv.Atoi(cleanVal)
		if err != nil {
			return err
		}
		rel.DigestMaxPerHour = v
	case "groups.min_size":
		v, err := strconv.Atoi(cleanVal)
		if err != nil {
			return err
		}
		rel.GroupsMinSize = v
	case "groups.max_members":
		v, err := strconv.Atoi(cleanVal)
		if err != nil {
			return err
		}
		rel.GroupsMaxMembers = v
	case "groups.max_per_account":
		v, err := strconv.Atoi(cleanVal)
		if err != nil {
			return err
		}
		rel.GroupsMaxPerAccount = v
	case "alias.release_months":
		v, err := strconv.Atoi(cleanVal)
		if err != nil {
			return err
		}
		rel.AliasReleaseMonths = v
	case "contacts.max_bytes":
		v, err := strconv.Atoi(cleanVal)
		if err != nil {
			return err
		}
		rel.ContactsMaxBytes = v
	case "blocks.ttl_days":
		v, err := strconv.Atoi(cleanVal)
		if err != nil {
			return err
		}
		rel.BlocksTTLDays = v
	case "subscriptions.prune_after_days":
		v, err := strconv.Atoi(cleanVal)
		if err != nil {
			return err
		}
		rel.SubscriptionsPruneAfterDays = v

	default:
		// Unknown key - log or ignore safely
		log.Printf("ignoring unrecognized config key: %s", key)
	}

	return nil
}
