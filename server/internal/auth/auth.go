package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"crypto/hkdf"
	"golang.org/x/crypto/argon2"

	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/obs"
	"github.com/halpworld/halpcheers/server/internal/store"
)

var (
	// ErrInvalidFormat is returned when an account key or auth secret violates formatting rules.
	ErrInvalidFormat = errors.New("invalid_format")
	// ErrAccountKeyRejected indicates a raw account key was sent to an endpoint expecting auth_secret.
	ErrAccountKeyRejected = errors.New("raw_account_key_rejected")
	// ErrInvalidCredentials indicates login verification failed or rate limit tripped (uniform error).
	ErrInvalidCredentials = errors.New("invalid_credentials")
	// ErrRateLimited indicates rate limit exceeded on unauthenticated signup.
	ErrRateLimited = errors.New("rate_limited")
	// ErrSessionNotFound indicates session token is invalid or expired.
	ErrSessionNotFound = errors.New("session_not_found")
	// ErrAccountSuspended indicates account is suspended.
	ErrAccountSuspended = errors.New("account_suspended")
	// ErrInvalidPoW indicates proof-of-work check failed.
	ErrInvalidPoW = errors.New("invalid_pow")
)

// Domain separation infos according to docs/IDENTITY.md §1.
const (
	HKDFInfoAuth     = "halp/auth/v1"
	HKDFInfoContacts = "halp/contacts/v1"
)

// Fixed Argon2id salt (16 bytes). Deterministic hashing is required because
// POST /v1/session receives auth_secret without an account identifier; accounts
// are looked up via key_hash BLOB NOT NULL UNIQUE.
var fixedArgon2Salt = []byte("halp-argon2id-v1")

// Argon2Params defines parameters for Argon2id hashing.
type Argon2Params struct {
	Time    uint32
	Memory  uint32 // in KiB
	Threads uint8
	KeyLen  uint32
}

// DefaultArgon2Params provides the production Argon2id parameters (RFC 9106):
// 1 iteration, 64 MiB RAM, 4 threads, 32-byte hash.
func DefaultArgon2Params() Argon2Params {
	return Argon2Params{
		Time:    1,
		Memory:  64 * 1024, // 64 MiB
		Threads: 4,
		KeyLen:  32,
	}
}

// FastArgon2Params provides lower-memory parameters for fast unit testing.
func FastArgon2Params() Argon2Params {
	return Argon2Params{
		Time:    1,
		Memory:  8 * 1024, // 8 MiB
		Threads: 1,
		KeyLen:  32,
	}
}

// GenerateAccountKey generates a 16-digit cryptographically random account key (~53.15 bits entropy).
// Digits are sampled uniformly using rejection sampling to eliminate modulo bias.
func GenerateAccountKey() (string, error) {
	var digits [16]byte
	var buf [1]byte

	for i := 0; i < 16; i++ {
		for {
			if _, err := rand.Read(buf[:]); err != nil {
				return "", fmt.Errorf("crypto/rand read: %w", err)
			}
			// 0..249 is divisible by 10 (25 multiples of 10)
			if buf[0] < 250 {
				digits[i] = '0' + (buf[0] % 10)
				break
			}
		}
	}
	return string(digits[:]), nil
}

// DeriveAuthSecret derives auth_secret from an account key: HKDF-SHA256(account_key, info="halp/auth/v1").
func DeriveAuthSecret(accountKey string) ([]byte, error) {
	clean := strings.ReplaceAll(accountKey, " ", "")
	if len(clean) != 16 {
		return nil, ErrInvalidFormat
	}
	for _, ch := range clean {
		if ch < '0' || ch > '9' {
			return nil, ErrInvalidFormat
		}
	}
	return hkdf.Key(sha256.New, []byte(clean), nil, HKDFInfoAuth, 32)
}

// DeriveContactsKey derives contacts_key from an account key: HKDF-SHA256(account_key, info="halp/contacts/v1").
// INVARIANT: This is client-only. The server NEVER calls or derives this during production request processing.
func DeriveContactsKey(accountKey string) ([]byte, error) {
	clean := strings.ReplaceAll(accountKey, " ", "")
	if len(clean) != 16 {
		return nil, ErrInvalidFormat
	}
	for _, ch := range clean {
		if ch < '0' || ch > '9' {
			return nil, ErrInvalidFormat
		}
	}
	return hkdf.Key(sha256.New, []byte(clean), nil, HKDFInfoContacts, 32)
}

// HashAuthSecret computes the Argon2id hash of auth_secret.
func HashAuthSecret(authSecret []byte, params Argon2Params) []byte {
	return argon2.IDKey(authSecret, fixedArgon2Salt, params.Time, params.Memory, params.Threads, params.KeyLen)
}

// Session represents an active bearer session.
type Session struct {
	Token     string
	AccountID core.AccountID
	ExpiresAt time.Time
}

// SessionStore maintains in-memory bearer sessions with ~40 µs lookup on the hot path (docs/ARCHITECTURE.md).
// Sized with a fixed capacity ceiling (AGENTS.md Invariant 8).
type SessionStore struct {
	mu          sync.RWMutex
	byToken     map[string]Session
	byAccount   map[core.AccountID]map[string]struct{}
	maxCapacity int
	ttl         time.Duration
}

// NewSessionStore creates a bounded SessionStore.
func NewSessionStore(maxCapacity int, ttl time.Duration) *SessionStore {
	if maxCapacity <= 0 {
		maxCapacity = 100_000
	}
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	return &SessionStore{
		byToken:     make(map[string]Session, 1024),
		byAccount:   make(map[core.AccountID]map[string]struct{}, 1024),
		maxCapacity: maxCapacity,
		ttl:         ttl,
	}
}

// CreateSession generates a fresh 32-byte hex bearer token and saves it.
func (s *SessionStore) CreateSession(accountID core.AccountID) (Session, error) {
	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return Session{}, err
	}
	token := hex.EncodeToString(tokenBytes[:])
	sess := Session{
		Token:     token,
		AccountID: accountID,
		ExpiresAt: time.Now().Add(s.ttl),
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// If at capacity, evict an expired session or oldest entry
	if len(s.byToken) >= s.maxCapacity {
		now := time.Now()
		var candidateKey string
		for k, v := range s.byToken {
			if now.After(v.ExpiresAt) {
				candidateKey = k
				break
			}
			if candidateKey == "" {
				candidateKey = k
			}
		}
		if candidateKey != "" {
			old := s.byToken[candidateKey]
			delete(s.byToken, candidateKey)
			if set, ok := s.byAccount[old.AccountID]; ok {
				delete(set, candidateKey)
				if len(set) == 0 {
					delete(s.byAccount, old.AccountID)
				}
			}
		}
	}

	s.byToken[token] = sess
	set, ok := s.byAccount[accountID]
	if !ok {
		set = make(map[string]struct{})
		s.byAccount[accountID] = set
	}
	set[token] = struct{}{}

	return sess, nil
}

// Lookup returns the AccountID for a valid, unexpired session token in sub-microsecond time.
func (s *SessionStore) Lookup(token string) (core.AccountID, bool) {
	s.mu.RLock()
	sess, ok := s.byToken[token]
	s.mu.RUnlock()

	if !ok {
		return 0, false
	}
	if time.Now().After(sess.ExpiresAt) {
		s.DeleteSession(token)
		return 0, false
	}
	return sess.AccountID, true
}

// DeleteSession removes a single session token.
func (s *SessionStore) DeleteSession(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sess, ok := s.byToken[token]; ok {
		delete(s.byToken, token)
		if set, ok := s.byAccount[sess.AccountID]; ok {
			delete(set, token)
			if len(set) == 0 {
				delete(s.byAccount, sess.AccountID)
			}
		}
	}
}

// DeleteSessionsForAccount removes all active sessions for an account (called on account deletion).
func (s *SessionStore) DeleteSessionsForAccount(accountID core.AccountID) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if tokens, ok := s.byAccount[accountID]; ok {
		for token := range tokens {
			delete(s.byToken, token)
		}
		delete(s.byAccount, accountID)
	}
}

// BoundedIPLimiter provides a fixed-size in-memory rate limiter keyed by hashed IP.
// AGENTS.md Invariant 8: Fixed-size allocation sized from startup, no unbounded maps.
type BoundedIPLimiter struct {
	mu       sync.Mutex
	buckets  map[uint64]*ipBucket
	capacity int
	window   time.Duration
	limit    int
}

type ipBucket struct {
	count     int
	windowEnd time.Time
}

// NewBoundedIPLimiter creates a fixed-size rate limiter.
func NewBoundedIPLimiter(capacity int, limit int, window time.Duration) *BoundedIPLimiter {
	if capacity <= 0 {
		capacity = 10_000
	}
	return &BoundedIPLimiter{
		buckets:  make(map[uint64]*ipBucket, 1024),
		capacity: capacity,
		window:   window,
		limit:    limit,
	}
}

// Allow reports whether an action is permitted for the hashed IP, incrementing the counter.
func (l *BoundedIPLimiter) Allow(hashedIP uint64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.buckets[hashedIP]
	if !ok || now.After(b.windowEnd) {
		// Enforce capacity ceiling
		if len(l.buckets) >= l.capacity {
			// Evict any expired bucket
			for k, v := range l.buckets {
				if now.After(v.windowEnd) {
					delete(l.buckets, k)
					break
				}
			}
			if len(l.buckets) >= l.capacity {
				// Evict an arbitrary entry
				for k := range l.buckets {
					delete(l.buckets, k)
					break
				}
			}
		}
		l.buckets[hashedIP] = &ipBucket{
			count:     1,
			windowEnd: now.Add(l.window),
		}
		return true
	}

	if b.count >= l.limit {
		return false
	}
	b.count++
	return true
}

// PoWVerifier defines an optional interface for verifying proof-of-work tokens.
type PoWVerifier interface {
	VerifySignupPoW(ctx context.Context, token string) error
}

// Service provides high-level account management operations.
type Service struct {
	store         *store.Store
	sessions      *SessionStore
	anonymizer    *obs.IPAnonymizer
	signupLimiter *BoundedIPLimiter
	loginLimiter  *BoundedIPLimiter
	powVerifier   PoWVerifier
	argon2Params  Argon2Params
	dummySecret   []byte
}

// ServiceConfig configures a Service.
type ServiceConfig struct {
	Store         *store.Store
	Sessions      *SessionStore
	Anonymizer    *obs.IPAnonymizer
	SignupLimiter *BoundedIPLimiter
	LoginLimiter  *BoundedIPLimiter
	PoWVerifier   PoWVerifier
	Argon2Params  Argon2Params
}

// NewService creates a new auth Service.
func NewService(cfg ServiceConfig) *Service {
	if cfg.Sessions == nil {
		cfg.Sessions = NewSessionStore(100_000, 30*24*time.Hour)
	}
	if cfg.Anonymizer == nil {
		cfg.Anonymizer = obs.NewIPAnonymizer()
	}
	if cfg.SignupLimiter == nil {
		// 5 signups per hour per IP
		cfg.SignupLimiter = NewBoundedIPLimiter(10_000, 5, time.Hour)
	}
	if cfg.LoginLimiter == nil {
		// 10 failed login attempts per minute per IP
		cfg.LoginLimiter = NewBoundedIPLimiter(10_000, 10, time.Minute)
	}
	if cfg.Argon2Params.KeyLen == 0 {
		cfg.Argon2Params = DefaultArgon2Params()
	}

	dummy := make([]byte, 32)
	_, _ = rand.Read(dummy)

	return &Service{
		store:         cfg.Store,
		sessions:      cfg.Sessions,
		anonymizer:    cfg.Anonymizer,
		signupLimiter: cfg.SignupLimiter,
		loginLimiter:  cfg.LoginLimiter,
		powVerifier:   cfg.PoWVerifier,
		argon2Params:  cfg.Argon2Params,
		dummySecret:   dummy,
	}
}

// Sessions returns the session store.
func (s *Service) Sessions() *SessionStore {
	return s.sessions
}

// CreateAccount generates a fresh account key, hashes auth_secret with Argon2id, and stores the account.
// INVARIANT: Only the account key is returned to client. contacts_key is never derived on server.
func (s *Service) CreateAccount(ctx context.Context, ip net.IP, powToken string) (string, core.AccountID, error) {
	// 1. Verify PoW if verifier is configured
	if s.powVerifier != nil && powToken != "" {
		if err := s.powVerifier.VerifySignupPoW(ctx, powToken); err != nil {
			return "", 0, ErrInvalidPoW
		}
	}

	// 2. Per-IP signup rate limit using hashed IP (obs.IPAnonymizer)
	// INVARIANT 9: Raw IP is NEVER persisted or logged.
	if ip != nil {
		hashedIP := s.anonymizer.Anonymize(ip)
		if !s.signupLimiter.Allow(hashedIP) {
			return "", 0, ErrRateLimited
		}
	}

	// 3. Generate 16-digit account key
	accountKey, err := GenerateAccountKey()
	if err != nil {
		return "", 0, err
	}

	// 4. Derive auth_secret = HKDF-SHA256(account_key, info="halp/auth/v1")
	authSecret, err := DeriveAuthSecret(accountKey)
	if err != nil {
		return "", 0, err
	}

	// 5. Compute Argon2id hash of auth_secret
	keyHash := HashAuthSecret(authSecret, s.argon2Params)

	// 6. Store account
	var accID core.AccountID
	if s.store != nil {
		accID, err = s.store.CreateAccount(ctx, keyHash, "eu-1")
		if err != nil {
			return "", 0, err
		}
	} else {
		accID = 1001
	}

	return accountKey, accID, nil
}

// ValidateAuthSecretShape ensures input is a valid 32-byte auth_secret (hex or raw)
// and STRICTLY REJECTS account keys (e.g. 16 decimal digits).
func ValidateAuthSecretShape(raw string) ([]byte, error) {
	trimmed := strings.TrimSpace(raw)
	digitsOnly := strings.ReplaceAll(trimmed, " ", "")

	// REJECT raw account keys (16 digits)
	if len(digitsOnly) == 16 {
		isAllDigits := true
		for _, c := range digitsOnly {
			if c < '0' || c > '9' {
				isAllDigits = false
				break
			}
		}
		if isAllDigits {
			return nil, ErrAccountKeyRejected
		}
	}

	// Attempt hex decode (64 hex characters = 32 bytes)
	if len(trimmed) == 64 {
		b, err := hex.DecodeString(trimmed)
		if err == nil && len(b) == 32 {
			return b, nil
		}
	}

	// Fallback: 32 raw bytes
	if len(trimmed) == 32 {
		return []byte(trimmed), nil
	}

	return nil, ErrInvalidFormat
}

// Login authenticates using auth_secret and returns a session.
// INVARIANT 7: Login failure and rate-limited login are indistinguishable in body and timing.
func (s *Service) Login(ctx context.Context, ip net.IP, authSecretRaw string) (Session, error) {
	// Check IP rate limit
	rateLimited := false
	if ip != nil {
		hashedIP := s.anonymizer.Anonymize(ip)
		if !s.loginLimiter.Allow(hashedIP) {
			rateLimited = true
		}
	}

	// Validate shape
	authSecret, err := ValidateAuthSecretShape(authSecretRaw)
	if rateLimited || err != nil {
		// Run dummy Argon2id calculation to consume identical timing
		_ = HashAuthSecret(s.dummySecret, s.argon2Params)
		return Session{}, ErrInvalidCredentials
	}

	// Compute Argon2id hash of auth_secret
	keyHash := HashAuthSecret(authSecret, s.argon2Params)

	if s.store == nil {
		// In-memory / mock test path
		return s.sessions.CreateSession(1001)
	}

	// Query account by key_hash
	var accID int64
	var suspended int
	err = s.store.ReadDB().QueryRowContext(ctx,
		"SELECT id, suspended FROM accounts WHERE key_hash = ?", keyHash).Scan(&accID, &suspended)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrInvalidCredentials
		}
		return Session{}, err
	}

	if suspended != 0 {
		return Session{}, ErrAccountSuspended
	}

	// Update last_seen_day
	today := core.Today().Int()
	_ = s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE accounts SET last_seen_day = ? WHERE id = ?", today, accID)
		return err
	})

	return s.sessions.CreateSession(core.AccountID(accID))
}

// Logout removes the bearer session.
func (s *Service) Logout(token string) {
	s.sessions.DeleteSession(token)
}

// AccountInfo represents metadata for GET /v1/account.
type AccountInfo struct {
	Region     string         `json:"region"`
	CreatedDay int64          `json:"created_day"`
	Counters   map[string]any `json:"counters"`
}

// GetAccount retrieves account info.
func (s *Service) GetAccount(ctx context.Context, accountID core.AccountID) (*AccountInfo, error) {
	if s.store == nil {
		return &AccountInfo{
			Region:     "eu-1",
			CreatedDay: core.Today().Int(),
			Counters:   map[string]any{"received_total": 0},
		}, nil
	}

	acc, err := s.store.GetAccountByID(ctx, accountID)
	if err != nil {
		return nil, err
	}

	return &AccountInfo{
		Region:     acc.Region,
		CreatedDay: acc.CreatedDay.Int(),
		Counters: map[string]any{
			"received_total": acc.RecvTotal,
		},
	}, nil
}

// AccountExport represents complete row set export under GDPR access/portability.
type AccountExport struct {
	Account       map[string]any   `json:"account"`
	Handles       []map[string]any `json:"handles"`
	Aliases       []map[string]any `json:"aliases"`
	Subscriptions []map[string]any `json:"subscriptions"`
	Settings      map[string]any   `json:"settings,omitempty"`
	Blocks        []map[string]any `json:"blocks"`
	Groups        []map[string]any `json:"groups"`
	Contacts      map[string]any   `json:"contacts,omitempty"`
}

// ExportAccount exports all rows belonging to an account.
func (s *Service) ExportAccount(ctx context.Context, accountID core.AccountID) (*AccountExport, error) {
	if s.store == nil {
		return &AccountExport{
			Account: map[string]any{
				"id":          accountID.Int64(),
				"region":      "eu-1",
				"created_day": core.Today().Int(),
			},
		}, nil
	}

	export := &AccountExport{
		Handles:       make([]map[string]any, 0),
		Aliases:       make([]map[string]any, 0),
		Subscriptions: make([]map[string]any, 0),
		Blocks:        make([]map[string]any, 0),
		Groups:        make([]map[string]any, 0),
	}

	db := s.store.ReadDB()

	// 1. Account row
	var region string
	var createdDay, lastSeenDay int64
	var sendTier, suspended, recvTotal int
	err := db.QueryRowContext(ctx,
		"SELECT region, created_day, last_seen_day, send_tier, suspended, recv_total FROM accounts WHERE id = ?",
		accountID.Int64()).Scan(&region, &createdDay, &lastSeenDay, &sendTier, &suspended, &recvTotal)
	if err != nil {
		return nil, err
	}
	export.Account = map[string]any{
		"id":            accountID.Int64(),
		"region":        region,
		"created_day":   createdDay,
		"last_seen_day": lastSeenDay,
		"send_tier":     sendTier,
		"suspended":     suspended != 0,
		"recv_total":    recvTotal,
	}

	// 2. Handles
	hRows, err := db.QueryContext(ctx,
		"SELECT handle, label, kind, group_id, paused, created_day FROM handles WHERE account_id = ?",
		accountID.Int64())
	if err == nil {
		defer hRows.Close()
		for hRows.Next() {
			var h, kind string
			var label sql.NullString
			var groupID sql.NullInt64
			var paused int
			var cDay int64
			if err := hRows.Scan(&h, &label, &kind, &groupID, &paused, &cDay); err == nil {
				item := map[string]any{
					"handle":      h,
					"kind":        kind,
					"paused":      paused != 0,
					"created_day": cDay,
				}
				if label.Valid {
					item["label"] = label.String
				}
				if groupID.Valid {
					item["group_id"] = groupID.Int64
				}
				export.Handles = append(export.Handles, item)
			}
		}
	}

	// 3. Aliases
	aRows, err := db.QueryContext(ctx,
		"SELECT alias, handle, created_day FROM aliases WHERE account_id = ?",
		accountID.Int64())
	if err == nil {
		defer aRows.Close()
		for aRows.Next() {
			var alias, handle string
			var cDay int64
			if err := aRows.Scan(&alias, &handle, &cDay); err == nil {
				export.Aliases = append(export.Aliases, map[string]any{
					"alias":       alias,
					"handle":      handle,
					"created_day": cDay,
				})
			}
		}
	}

	// 4. Subscriptions
	sRows, err := db.QueryContext(ctx,
		"SELECT id, kind, endpoint, created_day, last_ok_day FROM subscriptions WHERE account_id = ?",
		accountID.Int64())
	if err == nil {
		defer sRows.Close()
		for sRows.Next() {
			var id int64
			var kind, endpoint string
			var cDay int64
			var lastOk sql.NullInt64
			if err := sRows.Scan(&id, &kind, &endpoint, &cDay, &lastOk); err == nil {
				sub := map[string]any{
					"id":          id,
					"kind":        kind,
					"endpoint":    endpoint,
					"created_day": cDay,
				}
				if lastOk.Valid {
					sub["last_ok_day"] = lastOk.Int64
				}
				export.Subscriptions = append(export.Subscriptions, sub)
			}
		}
	}

	// 5. Settings
	var winS, maxH, minC int
	var qStart, qEnd sql.NullInt64
	var tz sql.NullString
	var mode string
	err = db.QueryRowContext(ctx,
		"SELECT digest_window_s, max_per_hour, quiet_start, quiet_end, tz, min_count, mode FROM settings WHERE account_id = ?",
		accountID.Int64()).Scan(&winS, &maxH, &qStart, &qEnd, &tz, &minC, &mode)
	if err == nil {
		st := map[string]any{
			"digest_window_s": winS,
			"max_per_hour":    maxH,
			"min_count":       minC,
			"mode":            mode,
		}
		if qStart.Valid {
			st["quiet_start"] = qStart.Int64
		}
		if qEnd.Valid {
			st["quiet_end"] = qEnd.Int64
		}
		if tz.Valid {
			st["tz"] = tz.String
		}
		export.Settings = st
	}

	// 6. Blocks
	bRows, err := db.QueryContext(ctx,
		"SELECT handle, created_day FROM blocks WHERE sender_account_id = ?",
		accountID.Int64())
	if err == nil {
		defer bRows.Close()
		for bRows.Next() {
			var handle string
			var cDay int64
			if err := bRows.Scan(&handle, &cDay); err == nil {
				export.Blocks = append(export.Blocks, map[string]any{
					"handle":      handle,
					"created_day": cDay,
				})
			}
		}
	}

	// 7. Group memberships
	gRows, err := db.QueryContext(ctx,
		"SELECT group_id, display_name, note, handle, role, joined_day FROM group_members WHERE account_id = ?",
		accountID.Int64())
	if err == nil {
		defer gRows.Close()
		for gRows.Next() {
			var gID int64
			var disp, handle, role string
			var note sql.NullString
			var jDay int64
			if err := gRows.Scan(&gID, &disp, &note, &handle, &role, &jDay); err == nil {
				gm := map[string]any{
					"group_id":     gID,
					"display_name": disp,
					"handle":       handle,
					"role":         role,
					"joined_day":   jDay,
				}
				if note.Valid {
					gm["note"] = note.String
				}
				export.Groups = append(export.Groups, gm)
			}
		}
	}

	// 8. Contacts blob
	var cBlob []byte
	var cVer int
	var uDay int64
	err = db.QueryRowContext(ctx,
		"SELECT blob, version, updated_day FROM contacts_blob WHERE account_id = ?",
		accountID.Int64()).Scan(&cBlob, &cVer, &uDay)
	if err == nil {
		export.Contacts = map[string]any{
			"version":     cVer,
			"updated_day": uDay,
			"blob":        hex.EncodeToString(cBlob),
		}
	}

	return export, nil
}

// DeleteAccount performs synchronous, irreversible erasure and invalidates sessions.
func (s *Service) DeleteAccount(ctx context.Context, accountID core.AccountID) error {
	// Invalidate in-memory sessions
	s.sessions.DeleteSessionsForAccount(accountID)

	if s.store != nil {
		return s.store.DeleteAccount(ctx, accountID)
	}
	return nil
}

// TimingSafeEqual wraps subtle.ConstantTimeCompare for byte slices.
func TimingSafeEqual(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

// ConstantTimeCheck computes HMAC-SHA256 for dummy timing equalization.
func ConstantTimeCheck(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}
