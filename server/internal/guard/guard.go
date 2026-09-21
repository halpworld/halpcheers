package guard

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"sync"
	"time"

	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/obs"
)

// BlockStore defines the narrow interface for persisting and querying the blocks table.
// INVARIANT 1: blocks is the ONLY permitted disk exception (day-granular, expires after 12 months).
type BlockStore interface {
	RecordBlock(ctx context.Context, sender core.AccountID, handle core.Handle, day core.Day) error
	IsBlocked(ctx context.Context, sender core.AccountID, handle core.Handle) (bool, error)
}

// BloomFilter is a fixed-size in-memory bitset filter.
// Sized from config with a 1 MiB minimum floor per slot (Invariant 8).
type BloomFilter struct {
	bits     []uint64
	numBits  uint64
	numBytes int
}

// NewBloomFilter creates a BloomFilter of the specified size in bytes (clamped to 1 MiB floor).
func NewBloomFilter(sizeBytes int) *BloomFilter {
	if sizeBytes < 1024*1024 {
		sizeBytes = 1024 * 1024
	}
	numBits := uint64(sizeBytes) * 8
	numWords := (numBits + 63) / 64
	return &BloomFilter{
		bits:     make([]uint64, numWords),
		numBits:  numBits,
		numBytes: sizeBytes,
	}
}

// Contains checks if the item is present using 4 independent hash derived indices.
func (bf *BloomFilter) Contains(h1, h2 uint64) bool {
	for i := uint64(0); i < 4; i++ {
		idx := (h1 + i*h2) % bf.numBits
		word := idx / 64
		bit := idx % 64
		if (bf.bits[word] & (1 << bit)) == 0 {
			return false
		}
	}
	return true
}

// Add inserts the item into the filter.
func (bf *BloomFilter) Add(h1, h2 uint64) {
	for i := uint64(0); i < 4; i++ {
		idx := (h1 + i*h2) % bf.numBits
		word := idx / 64
		bit := idx % 64
		bf.bits[word] |= (1 << bit)
	}
}

// PairWindow represents one 24-hour pair-filter window holding a cascade of slots.
type PairWindow struct {
	day      core.Day
	key      [32]byte
	slots    []*BloomFilter
	maxSlots int
	slotSize int
}

func newPairWindow(day core.Day, maxSlots, slotBytes int) *PairWindow {
	var key [32]byte
	rand.Read(key[:])

	slots := make([]*BloomFilter, maxSlots)
	for i := 0; i < maxSlots; i++ {
		slots[i] = NewBloomFilter(slotBytes)
	}

	return &PairWindow{
		day:      day,
		key:      key,
		slots:    slots,
		maxSlots: maxSlots,
		slotSize: slotBytes,
	}
}

// PairCascade manages the 24-hour pair limit cascade across rotating windows.
type PairCascade struct {
	mu       sync.RWMutex
	curr     *PairWindow
	prev     *PairWindow
	maxSlots int
	slotSize int
}

func NewPairCascade(maxSlots, slotBytes int) *PairCascade {
	if maxSlots < 10 {
		maxSlots = 10
	}
	today := core.Today()
	return &PairCascade{
		curr:     newPairWindow(today, maxSlots, slotBytes),
		prev:     newPairWindow(today.AddDays(-1), maxSlots, slotBytes),
		maxSlots: maxSlots,
		slotSize: slotBytes,
	}
}

func (pc *PairCascade) rotateIfNeeded(today core.Day) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if today > pc.curr.day {
		pc.prev = pc.curr
		pc.curr = newPairWindow(today, pc.maxSlots, pc.slotSize)
	}
}

// Allow evaluates the cascade for (sender, handle) up to limit slots.
// INVARIANT: False positives advance a slot, costing quota rather than granting it.
func (pc *PairCascade) Allow(sender core.AccountID, handle core.Handle, limit int) bool {
	today := core.Today()
	pc.rotateIfNeeded(today)

	pc.mu.RLock()
	curr := pc.curr
	pc.mu.RUnlock()

	if limit > curr.maxSlots {
		limit = curr.maxSlots
	}

	h1, h2 := hashPair(curr.key, sender, handle)

	pc.mu.Lock()
	defer pc.mu.Unlock()

	for slot := 0; slot < limit; slot++ {
		if !curr.slots[slot].Contains(h1, h2) {
			// Found first free slot: insert and permit
			curr.slots[slot].Add(h1, h2)
			return true
		}
	}

	// All slots occupied
	return false
}

func hashPair(key [32]byte, sender core.AccountID, handle core.Handle) (uint64, uint64) {
	mac := hmac.New(sha256.New, key[:])
	var sBuf [8]byte
	binary.BigEndian.PutUint64(sBuf[:], uint64(sender.Int64()))
	mac.Write(sBuf[:])
	mac.Write([]byte(handle.Raw()))
	sum := mac.Sum(nil)

	h1 := binary.BigEndian.Uint64(sum[0:8])
	h2 := binary.BigEndian.Uint64(sum[8:16])
	if h2 == 0 {
		h2 = 1
	}
	return h1, h2
}

// TokenBucket implements a thread-safe token bucket with time-based replenishment.
type TokenBucket struct {
	mu         sync.Mutex
	capacity   float64
	tokens     float64
	refillRate float64 // tokens per second
	lastRefill time.Time
}

func NewTokenBucket(capacity int, refillPer time.Duration) *TokenBucket {
	capF := float64(capacity)
	rate := capF / refillPer.Seconds()
	return &TokenBucket{
		capacity:   capF,
		tokens:     capF,
		refillRate: rate,
		lastRefill: time.Now(),
	}
}

func (tb *TokenBucket) Allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tb.lastRefill = now

	tb.tokens += elapsed * tb.refillRate
	if tb.tokens > tb.capacity {
		tb.tokens = tb.capacity
	}

	if tb.tokens >= 1.0 {
		tb.tokens -= 1.0
		return true
	}
	return false
}

// CountMinSketch implements a fixed-size top-sender estimator per handle (Layer 3).
type CountMinSketch struct {
	mu    sync.RWMutex
	width int
	depth int
	table [][]uint32
}

func NewCountMinSketch(width, depth int) *CountMinSketch {
	if width < 512 {
		width = 2048
	}
	if depth < 2 {
		depth = 4
	}
	table := make([][]uint32, depth)
	for i := range table {
		table[i] = make([]uint32, width)
	}
	return &CountMinSketch{
		width: width,
		depth: depth,
		table: table,
	}
}

func (cms *CountMinSketch) Add(handle core.Handle, sender core.AccountID) {
	cms.mu.Lock()
	defer cms.mu.Unlock()

	h1, h2 := hashPair([32]byte{0x42}, sender, handle)
	for d := 0; d < cms.depth; d++ {
		idx := (h1 + uint64(d)*h2) % uint64(cms.width)
		cms.table[d][idx]++
	}
}

func (cms *CountMinSketch) Estimate(handle core.Handle, sender core.AccountID) uint32 {
	cms.mu.RLock()
	defer cms.mu.RUnlock()

	h1, h2 := hashPair([32]byte{0x42}, sender, handle)
	var minVal uint32 = ^uint32(0)
	for d := 0; d < cms.depth; d++ {
		idx := (h1 + uint64(d)*h2) % uint64(cms.width)
		v := cms.table[d][idx]
		if v < minVal {
			minVal = v
		}
	}
	return minVal
}

// EWMA implements an exponential moving average counter for Layer 4 anomaly detection.
type EWMA struct {
	mu       sync.Mutex
	rate     float64
	tau      float64 // decay time constant in seconds
	lastTick time.Time
}

func NewEWMA(halfLife time.Duration) *EWMA {
	tau := halfLife.Seconds() / 0.693147
	return &EWMA{
		tau:      tau,
		lastTick: time.Now(),
	}
}

func (e *EWMA) Add(n float64) {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now()
	dt := now.Sub(e.lastTick).Seconds()
	e.lastTick = now

	decay := 1.0 / (1.0 + dt/e.tau)
	e.rate = e.rate*decay + n*(1.0-decay)
}

func (e *EWMA) Rate() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rate
}

// Service is the primary Guard implementation satisfying core.Guard and pow.DifficultyFeed.
type Service struct {
	cascade       *PairCascade
	sketch        *CountMinSketch
	blockStore    BlockStore
	metrics       *obs.Metrics
	globalEWMA    *EWMA
	targetEWMAs   sync.Map // handle string -> *EWMA
	senderBuckets sync.Map // AccountID -> *TokenBucket
	targetBuckets sync.Map // handle string -> *TokenBucket
	suspended     sync.Map // AccountID -> bool

	pairMaxPersonal int
	pairMaxSocial   int
	pairMaxGroup    int
	pairMaxStream   int
	senderHour      int
	senderDay       int
}

// Config defines tunables for the Guard service.
type Config struct {
	PairMaxPersonal int
	PairMaxSocial   int
	PairMaxGroup    int
	PairMaxStream   int
	SlotBytes       int
	SketchWidth     int
	SketchDepth     int
	SenderHour      int
	SenderDay       int
	BlockStore      BlockStore
	Metrics         *obs.Metrics
}

// NewService creates a fully initialized Guard service.
func NewService(cfg Config) *Service {
	slotBytes := cfg.SlotBytes
	if slotBytes < 1024*1024 {
		slotBytes = 1024 * 1024
	}

	pPersonal := cfg.PairMaxPersonal
	if pPersonal <= 0 {
		pPersonal = 3
	}
	pSocial := cfg.PairMaxSocial
	if pSocial <= 0 {
		pSocial = 3
	}
	pGroup := cfg.PairMaxGroup
	if pGroup <= 0 {
		pGroup = 3
	}
	pStream := cfg.PairMaxStream
	if pStream <= 0 {
		pStream = 10
	}
	sHour := cfg.SenderHour
	if sHour <= 0 {
		sHour = 20
	}
	sDay := cfg.SenderDay
	if sDay <= 0 {
		sDay = 100
	}

	return &Service{
		cascade:         NewPairCascade(pStream, slotBytes),
		sketch:          NewCountMinSketch(cfg.SketchWidth, cfg.SketchDepth),
		blockStore:      cfg.BlockStore,
		metrics:         cfg.Metrics,
		globalEWMA:      NewEWMA(60 * time.Second),
		pairMaxPersonal: pPersonal,
		pairMaxSocial:   pSocial,
		pairMaxGroup:    pGroup,
		pairMaxStream:   pStream,
		senderHour:      sHour,
		senderDay:       sDay,
	}
}

// SuspendAccount silently suspends an account (Layer 5). Pings will return 202 and go nowhere.
func (s *Service) SuspendAccount(id core.AccountID) {
	s.suspended.Store(id, true)
}

// IsSuspended reports if an account is suspended.
func (s *Service) IsSuspended(id core.AccountID) bool {
	v, ok := s.suspended.Load(id)
	return ok && v.(bool)
}

// PairLimitForKind returns the configured pair limit for a handle kind.
func (s *Service) PairLimitForKind(kind core.HandleKind) int {
	switch kind {
	case core.HandleKindStream:
		return s.pairMaxStream
	case core.HandleKindSocial:
		return s.pairMaxSocial
	case core.HandleKindGroup:
		return s.pairMaxGroup
	default:
		return s.pairMaxPersonal
	}
}

// Allow evaluates ingress rate limits and abuse prevention rules on the hot path.
// Satisfies core.Guard interface.
func (s *Service) Allow(ctx context.Context, sender core.AccountID, target core.Handle, targetKind core.HandleKind) bool {
	// 1. Silent suspension check (Layer 5)
	if s.IsSuspended(sender) {
		if s.metrics != nil {
			s.metrics.IncPingsDropped(obs.DropReasonBlocked)
		}
		return false
	}

	// 2. Persistent block check (Layer 3 exception)
	if s.blockStore != nil {
		blocked, _ := s.blockStore.IsBlocked(ctx, sender, target)
		if blocked {
			if s.metrics != nil {
				s.metrics.IncPingsDropped(obs.DropReasonBlocked)
			}
			return false
		}
	}

	// 3. Sender rate limit bucket (Layer 1: 20/hour default)
	sbVal, _ := s.senderBuckets.LoadOrStore(sender, NewTokenBucket(s.senderHour, 1*time.Hour))
	if !sbVal.(*TokenBucket).Allow() {
		if s.metrics != nil {
			s.metrics.IncPingsDropped(obs.DropReasonRateLimited)
		}
		return false
	}

	// 4. Target inbound rate limit bucket (Layer 1: configurable cap on deliveries/hour)
	tbVal, _ := s.targetBuckets.LoadOrStore(target.Raw(), NewTokenBucket(1000, 1*time.Hour))
	if !tbVal.(*TokenBucket).Allow() {
		if s.metrics != nil {
			s.metrics.IncPingsDropped(obs.DropReasonRateLimited)
		}
		return false
	}

	// 5. Pair-limit cascade (Layer 1: 3/24h or 10/24h)
	pairLimit := s.PairLimitForKind(targetKind)
	if !s.cascade.Allow(sender, target, pairLimit) {
		if s.metrics != nil {
			s.metrics.IncPingsDropped(obs.DropReasonDeduped)
		}
		return false
	}

	// Record telemetry & anomaly metrics
	s.globalEWMA.Add(1.0)
	ewmaVal, _ := s.targetEWMAs.LoadOrStore(target.Raw(), NewEWMA(60*time.Second))
	ewmaVal.(*EWMA).Add(1.0)
	s.sketch.Add(target, sender)

	return true
}

// Difficulty implements pow.DifficultyFeed seam. Raises PoW on EWMA spike.
func (s *Service) Difficulty(target string, sender core.AccountID) uint8 {
	var diff uint8 = 14

	if v, ok := s.targetEWMAs.Load(target); ok {
		rate := v.(*EWMA).Rate()
		if rate > 50.0 {
			diff = 20
		} else if rate > 20.0 {
			diff = 18
		} else if rate > 5.0 {
			diff = 16
		}
	}

	return diff
}

// ReportAbuse records the top sender into blocks table (Layer 3).
func (s *Service) ReportAbuse(ctx context.Context, handle core.Handle, sender core.AccountID) error {
	if s.blockStore != nil {
		return s.blockStore.RecordBlock(ctx, sender, handle, core.Today())
	}
	return nil
}
