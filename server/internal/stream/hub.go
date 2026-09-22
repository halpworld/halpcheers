package stream

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/obs"
)

// Event represents a single Server-Sent Event frame.
type Event struct {
	Name string
	Data string
}

type clientConn struct {
	ch      chan Event
	account core.AccountID
	done    chan struct{}
	closeMu sync.Mutex
	closed  bool
}

func (c *clientConn) Close() {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.done)
	}
}

// Config configures the SSE Hub.
type Config struct {
	HeartbeatInterval time.Duration // default 25s
	IdleDemoteAfter   time.Duration // default 15m
	BufferPerConn     int           // default 16
	MaxConnections    int           // default 10,000 (AGENTS.md Invariant 8)
	Metrics           *obs.Metrics
	NowFunc           func() time.Time
}

// Hub coordinates active SSE streaming connections by account ID.
//
// INVARIANT 8 (AGENTS.md Invariant 8 & docs/ARCHITECTURE.md):
// Fixed-size allocations per connection. Sized from config at startup.
// Slow consumers are pruned immediately when their fixed-capacity channel is full,
// guaranteeing that no per-connection buffer ever grows and never blocks the hub.
//
// INVARIANT 9:
// Connection metrics are purely numerical counts and gauges; no account IDs
// reach log lines or metric labels.
type Hub struct {
	mu             sync.RWMutex
	conns          map[core.AccountID]map[*clientConn]struct{}
	heartbeat      time.Duration
	idleDemote     time.Duration
	bufferSize     int
	maxConnections int
	metrics        *obs.Metrics
	nowFunc        func() time.Time
	activeConns    atomic.Int64
	closed         chan struct{}
}

// NewHub creates a new SSE Hub.
func NewHub(cfg Config) *Hub {
	heartbeat := cfg.HeartbeatInterval
	if heartbeat <= 0 {
		heartbeat = 25 * time.Second
	}
	idleDemote := cfg.IdleDemoteAfter
	if idleDemote <= 0 {
		idleDemote = 15 * time.Minute
	}
	bufferSize := cfg.BufferPerConn
	if bufferSize <= 0 {
		bufferSize = 16
	}
	maxConns := cfg.MaxConnections
	if maxConns <= 0 {
		maxConns = 10_000
	}
	nowFunc := cfg.NowFunc
	if nowFunc == nil {
		nowFunc = time.Now
	}

	return &Hub{
		conns:          make(map[core.AccountID]map[*clientConn]struct{}),
		heartbeat:      heartbeat,
		idleDemote:     idleDemote,
		bufferSize:     bufferSize,
		maxConnections: maxConns,
		metrics:        cfg.Metrics,
		nowFunc:        nowFunc,
		closed:         make(chan struct{}),
	}
}

// ActiveConnections returns the current number of active SSE streams.
func (h *Hub) ActiveConnections() int64 {
	return h.activeConns.Load()
}

// register adds a client connection to the hub. Returns false if maxConnections reached.
func (h *Hub) register(conn *clientConn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if int(h.activeConns.Load()) >= h.maxConnections {
		return false
	}

	set, ok := h.conns[conn.account]
	if !ok {
		set = make(map[*clientConn]struct{})
		h.conns[conn.account] = set
	}
	set[conn] = struct{}{}
	h.activeConns.Add(1)

	if h.metrics != nil {
		h.metrics.SetSSEConnections(float64(h.activeConns.Load()))
	}
	return true
}

// unregister removes a client connection from the hub.
func (h *Hub) unregister(conn *clientConn) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if set, ok := h.conns[conn.account]; ok {
		delete(set, conn)
		if len(set) == 0 {
			delete(h.conns, conn.account)
		}
		h.activeConns.Add(-1)
		if h.metrics != nil {
			h.metrics.SetSSEConnections(float64(h.activeConns.Load()))
		}
	}
}

// Broadcast sends a ping event to all active streams for an account.
// Returns true if delivered to at least one connection.
// If a consumer's channel is full, the slow consumer is immediately dropped.
func (h *Hub) Broadcast(account core.AccountID, count int) bool {
	h.mu.RLock()
	set, ok := h.conns[account]
	if !ok || len(set) == 0 {
		h.mu.RUnlock()
		return false
	}

	evt := Event{
		Name: "ping",
		Data: fmt.Sprintf("{\"n\":%d}", count),
	}

	var slowConns []*clientConn
	delivered := false

	for conn := range set {
		select {
		case conn.ch <- evt:
			delivered = true
		default:
			// Channel full: slow consumer!
			slowConns = append(slowConns, conn)
		}
	}
	h.mu.RUnlock()

	for _, conn := range slowConns {
		conn.Close()
		if h.metrics != nil {
			h.metrics.IncPingsDropped(obs.DropReasonQueueFull)
		}
	}

	return delivered
}

// ServeHTTP handles an incoming SSE client connection.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request, account core.AccountID) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	conn := &clientConn{
		ch:      make(chan Event, h.bufferSize),
		account: account,
		done:    make(chan struct{}),
	}

	if !h.register(conn) {
		http.Error(w, "Connection limit reached", http.StatusServiceUnavailable)
		return
	}
	defer h.unregister(conn)

	// SSE Headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Initial reconnect retry jitter
	_, _ = fmt.Fprintf(w, "retry: 5000\n\n")
	flusher.Flush()

	heartbeatTicker := time.NewTicker(h.heartbeat)
	defer heartbeatTicker.Stop()

	idleTimer := time.NewTimer(h.idleDemote)
	defer idleTimer.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-conn.done:
			return
		case <-h.closed:
			return
		case <-idleTimer.C:
			// Idle demotion: close stream so client falls back to /v1/pending polling
			_, _ = fmt.Fprintf(w, "event: demote\ndata: {\"reason\":\"idle\"}\n\n")
			flusher.Flush()
			return
		case evt := <-conn.ch:
			// Reset idle timer on active ping reception
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(h.idleDemote)

			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Name, evt.Data)
			flusher.Flush()
		case <-heartbeatTicker.C:
			_, _ = fmt.Fprintf(w, "event: ka\ndata: {}\n\n")
			flusher.Flush()
		}
	}
}

// Close terminates all active SSE streams.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	select {
	case <-h.closed:
		return
	default:
		close(h.closed)
	}

	for _, set := range h.conns {
		for conn := range set {
			conn.Close()
		}
	}
	h.conns = make(map[core.AccountID]map[*clientConn]struct{})
}
