package http

import (
	"context"
	"database/sql"
	"fmt"
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

// sqliteBlockStore adapts *store.Store to the guard.BlockStore interface.
type sqliteBlockStore struct {
	store *store.Store
}

func (s *sqliteBlockStore) RecordBlock(ctx context.Context, sender core.AccountID, handle core.Handle, day core.Day) error {
	if s.store == nil {
		return nil
	}
	return s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO blocks (sender_account_id, handle, created_day)
			VALUES (?, ?, ?)
			ON CONFLICT(sender_account_id, handle) DO NOTHING
		`, sender.Int64(), handle.Raw(), day.Int())
		return err
	})
}

func (s *sqliteBlockStore) IsBlocked(ctx context.Context, sender core.AccountID, handle core.Handle) (bool, error) {
	if s.store == nil {
		return false, nil
	}
	var count int
	err := s.store.ReadDB().QueryRowContext(ctx, `
		SELECT COUNT(1) FROM blocks WHERE sender_account_id = ? AND handle = ?
	`, sender.Int64(), handle.Raw()).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// Assembly manages the full collection of services and background pumps
// wiring the Halp server according to the Wave 1 contracts.
type Assembly struct {
	Auth      *auth.Service
	PoW       *pow.Engine
	Resolver  *region.Resolver
	Region    *region.Service
	Guard     *guard.Service
	Queue     *coalesce.Queue
	Coalescer *coalesce.Coalescer
	PushPool  *push.Pool
	StreamHub *stream.Hub
	Metrics   *obs.Metrics
	Store     *store.Store

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// AssembleDependencies initializes and wires all subsystem handlers and services.
func AssembleDependencies(ctx context.Context, cfg *config.Config, st *store.Store, metrics *obs.Metrics) (RouterDeps, *Assembly, error) {
	if metrics == nil {
		metrics = obs.NewMetrics()
	}

	blockStore := &sqliteBlockStore{store: st}
	guardSvc := guard.NewService(guard.Config{
		BlockStore: blockStore,
		Metrics:    metrics,
	})

	powEngine, err := pow.New(pow.Config{
		Feed:    guardSvc,
		Metrics: metrics,
	})
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

	queue := coalesce.NewQueue(65536, metrics)
	coalescer := coalesce.New(coalesce.Config{
		Metrics:       metrics,
		DefaultWindow: 60 * time.Second,
	})

	pushPool := push.NewPool(push.PoolConfig{
		Store:   st,
		Metrics: metrics,
	})

	streamHub := stream.NewHub(stream.Config{
		Metrics: metrics,
	})

	accountsH := NewAccountsHandlerWithDeps(authSvc, st)
	handlesH := NewHandlesHandlerWithDeps(regionSvc, resolver, st, authAdapter)
	pingH := NewPingHandlerWithDeps(authAdapter, powEngine, resolver, guardSvc, queue, metrics)
	subsH := NewSubscriptionsHandlerWithDeps(st, authAdapter)
	streamH := NewStreamHandlerWithDeps(streamHub, authAdapter, coalescer)
	settingsH := NewSettingsHandlerWithDeps(st, authAdapter, guardSvc)
	publicH := NewConfiguredPublicHandler()

	deps := RouterDeps{
		Metrics:       metrics,
		Accounts:      accountsH,
		Handles:       handlesH,
		Ping:          pingH,
		Subscriptions: subsH,
		Stream:        streamH,
		Settings:      settingsH,
		Public:        publicH,
		MaxBodyBytes:  64 * 1024,
		ReqTimeout:    10 * time.Second,
	}

	if cfg != nil {
		deps.MaxBodyBytes = int64(cfg.Reloadable().ContactsMaxBytes)
		deps.ReqTimeout = cfg.Startup.ShutdownDrainTimeout
	}

	assembly := &Assembly{
		Auth:      authSvc,
		PoW:       powEngine,
		Resolver:  resolver,
		Region:    regionSvc,
		Guard:     guardSvc,
		Queue:     queue,
		Coalescer: coalescer,
		PushPool:  pushPool,
		StreamHub: streamHub,
		Metrics:   metrics,
		Store:     st,
	}

	return deps, assembly, nil
}

// Start launches the background ingress and coalescing pumps.
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
}

// Close cleanly halts background pumps and waits for worker drains.
func (a *Assembly) Close() {
	if a.cancel != nil {
		a.cancel()
	}
	a.wg.Wait()
	if a.PushPool != nil {
		_ = a.PushPool.Stop()
	}
}
