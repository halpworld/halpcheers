package obs

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"net"
	"sync"
	"time"
)

// IPAnonymizer provides in-memory keyed anonymization for IP addresses used strictly
// by unauthenticated rate limiters (login and signup).
//
// INVARIANTS (docs/PRIVACY.md § IP addresses):
// - Raw IP is NEVER written to disk, logged, or emitted in a metric label.
// - Anonymized representation is HMAC(daily_rotating_salt, ip) truncated to uint64.
// - Salt rotates daily in memory and is NEVER persisted to disk, making yesterday's
//   buckets unlinkable to today's even across process restarts or memory snapshots.
type IPAnonymizer struct {
	mu   sync.RWMutex
	salt [32]byte
}

// NewIPAnonymizer creates an IPAnonymizer with a cryptographically random in-memory salt.
func NewIPAnonymizer() *IPAnonymizer {
	a := &IPAnonymizer{}
	a.rotate()
	return a
}

func (a *IPAnonymizer) rotate() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := rand.Read(a.salt[:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
}

// RotateSalt updates the in-memory salt to a fresh random 32-byte secret.
func (a *IPAnonymizer) RotateSalt() {
	a.rotate()
}

// StartDailyRotation starts a background goroutine that rotates the salt every 24 hours.
func (a *IPAnonymizer) StartDailyRotation(stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				a.RotateSalt()
			}
		}
	}()
}

// Anonymize computes HMAC-SHA256(salt, rawIP) and returns the first 8 bytes as a uint64 bucket key.
// The raw IP is processed in memory and discarded immediately.
func (a *IPAnonymizer) Anonymize(ip net.IP) uint64 {
	a.mu.RLock()
	salt := a.salt
	a.mu.RUnlock()

	mac := hmac.New(sha256.New, salt[:])
	mac.Write(ip)
	sum := mac.Sum(nil)

	return binary.BigEndian.Uint64(sum[:8])
}
