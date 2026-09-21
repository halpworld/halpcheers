package push

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/obs"
	"github.com/halpworld/halpcheers/server/internal/store"
)

// DispatchJob represents an in-flight dispatch task for a recipient account.
type DispatchJob struct {
	Recipient   core.AccountID
	DigestCount int // 1 for single ping (payloadless default); > 1 for coalesced digest
	EnqueuedAt  time.Time
}

// Subscription represents a registered push endpoint row in SQLite.
type Subscription struct {
	ID         int64
	AccountID  core.AccountID
	Kind       string
	Endpoint   string
	P256DH     []byte
	Auth       []byte
	CreatedDay int64
	LastOKDay  sql.NullInt64
}

// Sender delivers a push notification to a single subscription.
type Sender interface {
	Send(ctx context.Context, sub Subscription, payload []byte) (statusCode int, err error)
}

// DefaultSender provides a production HTTP client for Web Push delivery.
type DefaultSender struct {
	client *http.Client
}

// NewDefaultSender creates a DefaultSender with timeouts and connection pooling.
func NewDefaultSender() *DefaultSender {
	return &DefaultSender{
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        1000,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

func (s *DefaultSender) Send(ctx context.Context, sub Subscription, payload []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("TTL", "60")

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	return resp.StatusCode, nil
}

// PoolConfig configures the dispatch worker pool.
type PoolConfig struct {
	Workers      int           // dispatch.workers (default 4 * vCPU)
	MaxAge       time.Duration // dispatch.max_age (default 30s)
	DrainTimeout time.Duration // shutdown.drain_timeout (default 10s)
	Store        *store.Store
	Sender       Sender
	Metrics      *obs.Metrics
	NowFunc      func() time.Time // clock abstraction for testing
}

// Pool manages a pool of concurrent workers draining the dispatch channel.
type Pool struct {
	workers      int
	maxAge       time.Duration
	drainTimeout time.Duration
	store        *store.Store
	sender       Sender
	metrics      *obs.Metrics
	nowFunc      func() time.Time

	jobCh   chan DispatchJob
	wg      sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
	stopped chan struct{}
}

// NewPool creates and starts a dispatch worker pool.
func NewPool(cfg PoolConfig) *Pool {
	workers := cfg.Workers
	if workers <= 0 {
		workers = 4
	}
	maxAge := cfg.MaxAge
	if maxAge <= 0 {
		maxAge = 30 * time.Second
	}
	drainTimeout := cfg.DrainTimeout
	if drainTimeout <= 0 {
		drainTimeout = 10 * time.Second
	}
	sender := cfg.Sender
	if sender == nil {
		sender = NewDefaultSender()
	}
	nowFunc := cfg.NowFunc
	if nowFunc == nil {
		nowFunc = time.Now
	}

	ctx, cancel := context.WithCancel(context.Background())

	p := &Pool{
		workers:      workers,
		maxAge:       maxAge,
		drainTimeout: drainTimeout,
		store:        cfg.Store,
		sender:       sender,
		metrics:      cfg.Metrics,
		nowFunc:      nowFunc,
		jobCh:        make(chan DispatchJob, 1024),
		ctx:          ctx,
		cancel:       cancel,
		stopped:      make(chan struct{}),
	}

	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go p.workerLoop()
	}

	return p
}

// QueueJob submits a job to the worker pool. Returns false if channel is full or pool is stopped.
func (p *Pool) QueueJob(job DispatchJob) bool {
	select {
	case <-p.ctx.Done():
		return false
	case p.jobCh <- job:
		return true
	default:
		if p.metrics != nil {
			p.metrics.IncPingsDropped(obs.DropReasonQueueFull)
		}
		return false
	}
}

// workerLoop drains dispatch jobs until context cancelled or channel drained.
func (p *Pool) workerLoop() {
	defer p.wg.Done()

	for {
		select {
		case <-p.ctx.Done():
			// Context canceled during drain
			return
		case job, ok := <-p.jobCh:
			if !ok {
				return
			}
			p.processJob(job)
		}
	}
}

// processJob executes dispatch logic, stale check, push call, and exact token pruning.
func (p *Pool) processJob(job DispatchJob) {
	now := p.nowFunc()

	// 1. Stale job check (docs/ARCHITECTURE.md § Loss policy)
	if !job.EnqueuedAt.IsZero() && now.Sub(job.EnqueuedAt) > p.maxAge {
		if p.metrics != nil {
			p.metrics.IncPingsDropped(obs.DropReasonStale)
		}
		return
	}

	if p.store == nil {
		return
	}

	// 2. Fetch subscriptions for recipient
	ctx, cancel := context.WithTimeout(p.ctx, 15*time.Second)
	defer cancel()

	rows, err := p.store.ReadDB().QueryContext(ctx,
		"SELECT id, kind, endpoint, p256dh, auth, created_day FROM subscriptions WHERE account_id = ?",
		job.Recipient.Int64())
	if err != nil {
		return
	}
	defer rows.Close()

	var subs []Subscription
	for rows.Next() {
		var sub Subscription
		sub.AccountID = job.Recipient
		if err := rows.Scan(&sub.ID, &sub.Kind, &sub.Endpoint, &sub.P256DH, &sub.Auth, &sub.CreatedDay); err == nil {
			subs = append(subs, sub)
		}
	}

	// 3. Dispatch to each subscription
	for _, sub := range subs {
		p.dispatchToSub(ctx, sub, job)
	}
}

// dispatchToSub sends a push notification and enforces exact token pruning (Invariant 4).
func (p *Pool) dispatchToSub(ctx context.Context, sub Subscription, job DispatchJob) {
	// CRITICAL: Capture send_start_day BEFORE dispatch request leaves
	// (docs/DELIVERY.md & AGENTS.md Invariant 4)
	sendStartDay := core.Today().Int()

	statusCode, err := p.sender.Send(ctx, sub, nil)

	// INVARIANT 4 (AGENTS.md Invariant 4):
	// Prune on authoritative rejection only: Web Push 404 or 410.
	// Never prune on 429, 500, 502, 503, timeout, or DNS failure.
	// Delete by exact endpoint AND only if the row predates the failed send:
	// DELETE FROM subscriptions WHERE endpoint = ? AND created_day <= ?
	if statusCode == http.StatusNotFound || statusCode == http.StatusGone {
		_ = p.store.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.ExecContext(context.Background(),
				"DELETE FROM subscriptions WHERE endpoint = ? AND created_day <= ?",
				sub.Endpoint, sendStartDay)
			return err
		})
		return
	}

	// Successful send: update last_ok_day
	if err == nil && (statusCode >= 200 && statusCode < 300) {
		_ = p.store.Write(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.ExecContext(context.Background(),
				"UPDATE subscriptions SET last_ok_day = ? WHERE id = ?",
				sendStartDay, sub.ID)
			return err
		})
	}
}

// PruneAncientSubscriptions sweeps rows whose last_ok_day is older than pruneAfterDays.
func (p *Pool) PruneAncientSubscriptions(ctx context.Context, pruneAfterDays int) error {
	if p.store == nil || pruneAfterDays <= 0 {
		return nil
	}
	cutoffDay := core.Today().Int() - int64(pruneAfterDays)
	return p.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"DELETE FROM subscriptions WHERE last_ok_day IS NOT NULL AND last_ok_day < ?",
			cutoffDay)
		return err
	})
}

// Stop gracefully drains the worker pool up to drainTimeout.
func (p *Pool) Stop() error {
	close(p.jobCh)

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(p.drainTimeout):
		// Drain deadline exceeded: force cancel remaining workers
		p.cancel()
		<-done
		return errors.New("drain timeout exceeded")
	}
}
