// Package coalesce implements ingress aggregation, flush loops, and digest construction.
//
// CRITICAL INVARIANT WARNING (AGENTS.md Invariant 1):
// No message records.
// The accumulator in this package holds ONLY recipient account IDs and ephemeral counters.
// It does NOT have, CANNOT have, and MUST NEVER be extended with fields for sender identities,
// handles, pings, or audit information. A digest carries only a count N.
package coalesce

import (
	"context"
	"sync"
	"time"

	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/obs"
)

// accumulatorEntry holds purely ephemeral counter state for a recipient.
// INVARIANT 1: No sender account, handle, or ping identifier is stored here.
type accumulatorEntry struct {
	count     int
	firstSeen time.Time
	lastSeen  time.Time
}

// WindowProvider returns the aggregation window for a given recipient account.
type WindowProvider interface {
	WindowForAccount(account core.AccountID) time.Duration
}

// Config defines tunables for the Coalescer.
type Config struct {
	DefaultWindow time.Duration // digest.default_window_s (default 60s)
	MaxAge        time.Duration // dispatch.max_age (default 30s)
	MaxRecipients int           // fixed capacity bound (default 100,000)
	Metrics       *obs.Metrics
	Windows       WindowProvider
}

// Coalescer aggregates incoming ping jobs into time-windowed digests.
type Coalescer struct {
	mu            sync.Mutex
	items         map[core.AccountID]*accumulatorEntry
	defaultWindow time.Duration
	maxAge        time.Duration
	maxRecipients int
	metrics       *obs.Metrics
	windows       WindowProvider
}

// New creates a new Coalescer instance.
func New(cfg Config) *Coalescer {
	window := cfg.DefaultWindow
	if window <= 0 {
		window = 60 * time.Second
	}
	maxAge := cfg.MaxAge
	if maxAge <= 0 {
		maxAge = 30 * time.Second
	}
	maxRecipients := cfg.MaxRecipients
	if maxRecipients <= 0 {
		maxRecipients = 100_000
	}

	return &Coalescer{
		items:         make(map[core.AccountID]*accumulatorEntry),
		defaultWindow: window,
		maxAge:        maxAge,
		maxRecipients: maxRecipients,
		metrics:       cfg.Metrics,
		windows:       cfg.Windows,
	}
}

// Add ingests an in-flight PingJob into the recipient's accumulator.
// If the job is older than maxAge, it is dropped and counted with reason="stale".
// Returns true if ingested, false if dropped.
func (c *Coalescer) Add(job core.PingJob) bool {
	return c.AddAt(job, time.Now())
}

// AddAt ingests a PingJob evaluated against a specific wall-clock time (for testing).
func (c *Coalescer) AddAt(job core.PingJob, now time.Time) bool {
	if !job.EnqueuedAt.IsZero() && now.Sub(job.EnqueuedAt) > c.maxAge {
		if c.metrics != nil {
			c.metrics.IncPingsDropped(obs.DropReasonStale)
		}
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	entry, exists := c.items[job.Recipient]
	if !exists {
		// Bounded capacity check (Invariant 8)
		if len(c.items) >= c.maxRecipients {
			if c.metrics != nil {
				c.metrics.IncPingsDropped(obs.DropReasonQueueFull)
			}
			return false
		}
		c.items[job.Recipient] = &accumulatorEntry{
			count:     1,
			firstSeen: now,
			lastSeen:  now,
		}
		return true
	}

	entry.count++
	entry.lastSeen = now
	return true
}

// GetAndClearPending atomically returns the accumulated count for an account and clears it.
// Used by GET /v1/pending and cold-start SSE clients.
func (c *Coalescer) GetAndClearPending(account core.AccountID) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, exists := c.items[account]
	if !exists || entry.count == 0 {
		return 0
	}
	n := entry.count
	delete(c.items, account)
	return n
}

// Flush evaluates all accumulated entries against the given timestamp and returns
// digests for those whose window has expired.
func (c *Coalescer) Flush(now time.Time) []core.Digest {
	c.mu.Lock()
	defer c.mu.Unlock()

	var digests []core.Digest
	for id, entry := range c.items {
		window := c.defaultWindow
		if c.windows != nil {
			if w := c.windows.WindowForAccount(id); w > 0 {
				window = w
			}
		}

		if now.Sub(entry.firstSeen) >= window {
			digests = append(digests, core.Digest{
				Recipient: id,
				Count:     entry.count,
			})
			delete(c.items, id)
			if c.metrics != nil {
				c.metrics.IncDigestsSent()
			}
		}
	}
	return digests
}

// ForceFlush immediately flushes an account's accumulated count into a Digest if present.
func (c *Coalescer) ForceFlush(account core.AccountID) (core.Digest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, exists := c.items[account]
	if !exists || entry.count == 0 {
		return core.Digest{}, false
	}
	d := core.Digest{
		Recipient: account,
		Count:     entry.count,
	}
	delete(c.items, account)
	if c.metrics != nil {
		c.metrics.IncDigestsSent()
	}
	return d, true
}

// FlushAll immediately extracts all pending entries into Digests.
func (c *Coalescer) FlushAll() []core.Digest {
	c.mu.Lock()
	defer c.mu.Unlock()

	digests := make([]core.Digest, 0, len(c.items))
	for id, entry := range c.items {
		digests = append(digests, core.Digest{
			Recipient: id,
			Count:     entry.count,
		})
		if c.metrics != nil {
			c.metrics.IncDigestsSent()
		}
	}
	c.items = make(map[core.AccountID]*accumulatorEntry)
	return digests
}

// RunFlushLoop starts a periodic background worker draining expired digests until ctx cancels.
func (c *Coalescer) RunFlushLoop(ctx context.Context, interval time.Duration, onDigest func(core.Digest)) {
	if interval <= 0 {
		interval = 1 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			digests := c.Flush(now)
			for _, d := range digests {
				onDigest(d)
			}
		}
	}
}
