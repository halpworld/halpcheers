package http

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/halpworld/halpcheers/server/internal/auth"
	"github.com/halpworld/halpcheers/server/internal/auth/pow"
	"github.com/halpworld/halpcheers/server/internal/coalesce"
	"github.com/halpworld/halpcheers/server/internal/config"
	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/guard"
	"github.com/halpworld/halpcheers/server/internal/obs"
	"github.com/halpworld/halpcheers/server/internal/push"
	"github.com/halpworld/halpcheers/server/internal/push/webpush"
	"github.com/halpworld/halpcheers/server/internal/region"
	"github.com/halpworld/halpcheers/server/internal/store"
	"github.com/halpworld/halpcheers/server/internal/stream"
)

// sessionAuthAdapter adapts *auth.SessionStore to the AuthenticateSession interface.
type sessionAuthAdapter struct {
	sessions *auth.SessionStore
}

func (a *sessionAuthAdapter) AuthenticateSession(ctx context.Context, token string) (core.AccountID, bool) {
	if a.sessions == nil {
		return 0, false
	}
	return a.sessions.Lookup(token)
}

type blockCacheKey struct {
	sender core.AccountID
	handle core.Handle
}

// sqliteBlockStore adapts *store.Store to the guard.BlockStore interface
// with a bounded in-memory cache (10,000 entries) so hot-path IsBlocked checks
// execute in O(1) memory after initial lookup.
type sqliteBlockStore struct {
	store *store.Store
	mu    sync.RWMutex
	cache map[blockCacheKey]bool
}

func newSQLiteBlockStore(st *store.Store) *sqliteBlockStore {
	return &sqliteBlockStore{
		store: st,
		cache: make(map[blockCacheKey]bool, 1024),
	}
}

func (s *sqliteBlockStore) RecordBlock(ctx context.Context, sender core.AccountID, handle core.Handle, day core.Day) error {
	if s.store == nil {
		return nil
	}
	err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, txErr := tx.ExecContext(ctx, `
			INSERT INTO blocks (sender_account_id, handle, created_day)
			VALUES (?, ?, ?)
			ON CONFLICT(sender_account_id, handle) DO NOTHING
		`, sender.Int64(), handle.Raw(), day.Int())
		return txErr
	})
	if err == nil {
		s.mu.Lock()
		if len(s.cache) >= 10_000 {
			for k := range s.cache {
				delete(s.cache, k)
				break
			}
		}
		s.cache[blockCacheKey{sender: sender, handle: handle}] = true
		s.mu.Unlock()
	}
	return err
}

func (s *sqliteBlockStore) IsBlocked(ctx context.Context, sender core.AccountID, handle core.Handle) (bool, error) {
	if s.store == nil {
		return false, nil
	}
	key := blockCacheKey{sender: sender, handle: handle}
	s.mu.RLock()
	if blocked, ok := s.cache[key]; ok {
		s.mu.RUnlock()
		return blocked, nil
	}
	s.mu.RUnlock()

	var count int
	err := s.store.ReadDB().QueryRowContext(ctx, `
		SELECT COUNT(1) FROM blocks WHERE sender_account_id = ? AND handle = ?
	`, sender.Int64(), handle.Raw()).Scan(&count)
	if err != nil {
		return false, err
	}
	blocked := count > 0

	s.mu.Lock()
	if len(s.cache) >= 10_000 {
		for k := range s.cache {
			delete(s.cache, k)
			break
		}
	}
	s.cache[key] = blocked
	s.mu.Unlock()

	return blocked, nil
}

// Assembly manages the full collection of services and background pumps
// wiring the Halp server according to the Wave 1 contracts.
type Assembly struct {
	Auth           *auth.Service
	PoW            *pow.Engine
	Resolver       *region.Resolver
	Region         *region.Service
	Guard          *guard.Service
	Queue          *coalesce.Queue
	Coalescer      *coalesce.Coalescer
	PushPool       *push.Pool
	StreamHub      *stream.Hub
	Metrics        *obs.Metrics
	Store          *store.Store
	VAPIDPublicKey string

	cfg    *config.Config
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// AssembleDependencies initializes and wires all subsystem handlers and services.
func AssembleDependencies(ctx context.Context, cfg *config.Config, st *store.Store, metrics *obs.Metrics) (RouterDeps, *Assembly, error) {
	if metrics == nil {
		metrics = obs.NewMetrics()
	}

	blockStore := newSQLiteBlockStore(st)
	guardCfg := guard.Config{
		BlockStore: blockStore,
		Metrics:    metrics,
	}
	if cfg != nil {
		guardCfg.PairMaxPersonal = cfg.Startup.GuardPairMaxPersonal
		guardCfg.PairMaxSocial = cfg.Startup.GuardPairMaxSocial
		guardCfg.PairMaxGroup = cfg.Startup.GuardPairMaxGroup
		guardCfg.PairMaxStream = cfg.Startup.GuardPairMaxStream
		guardCfg.SlotBytes = cfg.Startup.GuardPairSlotBytes
		guardCfg.SketchWidth = cfg.Startup.GuardSketchWidth
		guardCfg.SketchDepth = cfg.Startup.GuardSketchDepth
		if r := cfg.Reloadable(); r != nil {
			guardCfg.SenderHour = r.GuardSenderHour
			guardCfg.SenderDay = r.GuardSenderDay
		}
	}
	guardSvc := guard.NewService(guardCfg)

	powCfg := pow.Config{
		Feed:    guardSvc,
		Metrics: metrics,
	}
	if cfg != nil {
		if r := cfg.Reloadable(); r != nil {
			powCfg.FloorMs = r.PowFloorMs
			powCfg.SignupMs = r.PowSignupMs
			powCfg.EpochDuration = r.PowEpoch
		}
	}
	powEngine, err := pow.New(powCfg)
	if err != nil {
		return RouterDeps{}, nil, fmt.Errorf("pow init: %w", err)
	}

	authSvc := auth.NewService(auth.ServiceConfig{
		Store:       st,
		Anonymizer:  obs.NewIPAnonymizer(),
		PoWVerifier: powEngine,
	})

	authAdapter := &sessionAuthAdapter{sessions: authSvc.Sessions()}

	resolver := region.NewResolver(st, 10_000)
	regionSvc := region.NewService(st, resolver)

	queueCapacity := 65536
	if cfg != nil && cfg.Startup.QueueSize > 0 {
		queueCapacity = cfg.Startup.QueueSize
	}
	queue := coalesce.NewQueue(queueCapacity, metrics)

	coalesceCfg := coalesce.Config{
		Metrics:       metrics,
		DefaultWindow: 60 * time.Second,
	}
	if cfg != nil {
		if r := cfg.Reloadable(); r != nil {
			if r.DigestWindowSeconds > 0 {
				coalesceCfg.DefaultWindow = time.Duration(r.DigestWindowSeconds) * time.Second
			}
			if r.DispatchMaxAge > 0 {
				coalesceCfg.MaxAge = r.DispatchMaxAge
			}
		}
	}
	coalescer := coalesce.New(coalesceCfg)

	vapidPrivKey, vapidPubB64, err := webpush.LoadOrGenerateVAPIDKey(os.Getenv("HALP_VAPID_PRIVATE_KEY"), os.Getenv("HALP_VAPID_KEY_FILE"))
	if err != nil {
		return RouterDeps{}, nil, fmt.Errorf("vapid key initialization: %w", err)
	}

	subscriber := os.Getenv("HALP_VAPID_SUBSCRIBER")
	if subscriber == "" {
		subscriber = "mailto:ops@halp.to"
	}

	keypairCache := webpush.NewKeypairCache()
	sender := push.NewDefaultSenderWithVAPID(vapidPrivKey, subscriber, keypairCache)

	poolCfg := push.PoolConfig{
		Store:   st,
		Sender:  sender,
		Cache:   keypairCache,
		Metrics: metrics,
	}
	if cfg != nil {
		poolCfg.Workers = cfg.Startup.DispatchWorkers
		poolCfg.DrainTimeout = cfg.Startup.ShutdownDrainTimeout
		if r := cfg.Reloadable(); r != nil {
			poolCfg.MaxAge = r.DispatchMaxAge
		}
	}
	pushPool := push.NewPool(poolCfg)

	streamCfg := stream.Config{
		Metrics: metrics,
	}
	if cfg != nil {
		if r := cfg.Reloadable(); r != nil {
			streamCfg.IdleDemoteAfter = r.IdleDemoteAfter
		}
	}
	streamHub := stream.NewHub(streamCfg)

	accountsH := NewAccountsHandlerWithDeps(authSvc, st)
	handlesH := NewHandlesHandlerWithDeps(regionSvc, resolver, st, authAdapter)
	pingH := NewPingHandlerWithDeps(authAdapter, powEngine, resolver, guardSvc, queue, metrics)
	subsH := NewSubscriptionsHandlerWithDeps(st, authAdapter)
	streamH := NewStreamHandlerWithDeps(streamHub, authAdapter, coalescer)
	settingsH := NewSettingsHandlerWithDeps(st, authAdapter, guardSvc)
	groupsH := NewGroupsHandlerWithDeps(regionSvc, resolver, st, authAdapter, powEngine, cfg)
	contactsH := NewContactsHandlerWithDeps(st, authAdapter, cfg)
	publicH := NewConfiguredPublicHandlerWithDeps(st, streamHub)

	deps := RouterDeps{
		Metrics:       metrics,
		Accounts:      accountsH,
		Handles:       handlesH,
		Ping:          pingH,
		Subscriptions: subsH,
		Stream:        streamH,
		Settings:      settingsH,
		Groups:        groupsH,
		Contacts:      contactsH,
		Public:        publicH,
		MaxBodyBytes:  128 * 1024,
		ReqTimeout:    10 * time.Second,
	}

	if cfg != nil {
		deps.MaxBodyBytes = int64(cfg.Reloadable().ContactsMaxBytes) * 2
		deps.ReqTimeout = cfg.Startup.ShutdownDrainTimeout
		resolver.SetMinGroupSize(cfg.Reloadable().GroupsMinSize)
	}

	assembly := &Assembly{
		Auth:           authSvc,
		PoW:            powEngine,
		Resolver:       resolver,
		Region:         regionSvc,
		Guard:          guardSvc,
		Queue:          queue,
		Coalescer:      coalescer,
		PushPool:       pushPool,
		StreamHub:      streamHub,
		Metrics:        metrics,
		Store:          st,
		VAPIDPublicKey: vapidPubB64,
		cfg:            cfg,
	}

	return deps, assembly, nil
}

// Start launches the background ingress, coalescing, and maintenance pumps.
func (a *Assembly) Start(ctx context.Context) {
	a.ctx, a.cancel = context.WithCancel(ctx)

	// Pump 1: Drain ingress queue -> Coalescer and StreamHub
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		for {
			select {
			case <-a.ctx.Done():
				return
			case job, ok := <-a.Queue.Channel():
				if !ok {
					return
				}
				a.Coalescer.Add(job)
				a.StreamHub.Broadcast(job.Recipient, 1)
			}
		}
	}()

	// Pump 2: Flush coalescer periodically -> PushPool
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-a.ctx.Done():
				return
			case now := <-ticker.C:
				digests := a.Coalescer.Flush(now)
				for _, d := range digests {
					a.PushPool.QueueJob(push.DispatchJob{
						Recipient:   d.Recipient,
						DigestCount: d.Count,
						EnqueuedAt:  now,
					})
				}
			}
		}
	}()

	// Pump 3: Daily maintenance ticker (every 6 hours)
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()

		runMaintenance := func() {
			pruneDays := 180
			blocksTTLDays := 365
			if a.cfg != nil {
				if r := a.cfg.Reloadable(); r != nil {
					if r.SubscriptionsPruneAfterDays > 0 {
						pruneDays = r.SubscriptionsPruneAfterDays
					}
					if r.BlocksTTLDays > 0 {
						blocksTTLDays = r.BlocksTTLDays
					}
				}
			}
			if a.PushPool != nil {
				_ = a.PushPool.PruneAncientSubscriptions(a.ctx, pruneDays)
			}
			if a.Store != nil {
				cutoff := core.Today().Int() - int64(blocksTTLDays)
				_ = a.Store.Write(a.ctx, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(a.ctx, "DELETE FROM blocks WHERE created_day < ?", cutoff)
					return err
				})
			}
		}

		for {
			select {
			case <-a.ctx.Done():
				return
			case <-ticker.C:
				runMaintenance()
			}
		}
	}()
}

// Close cleanly halts background pumps and waits for worker drains.
func (a *Assembly) Close() {
	if a.cancel != nil {
		a.cancel()
	}
	a.wg.Wait()

	// Drain any remaining items from ingress queue
	if a.Queue != nil && a.Coalescer != nil && a.StreamHub != nil {
	drainLoop:
		for {
			select {
			case job, ok := <-a.Queue.Channel():
				if !ok {
					break drainLoop
				}
				a.Coalescer.Add(job)
				a.StreamHub.Broadcast(job.Recipient, 1)
			default:
				break drainLoop
			}
		}
	}

	// Flush all coalesced pending digests to PushPool before stopping it
	if a.Coalescer != nil && a.PushPool != nil {
		now := time.Now()
		for _, d := range a.Coalescer.FlushAll() {
			a.PushPool.QueueJob(push.DispatchJob{
				Recipient:   d.Recipient,
				DigestCount: d.Count,
				EnqueuedAt:  now,
			})
		}
	}

	if a.PushPool != nil {
		_ = a.PushPool.Stop()
	}
}
