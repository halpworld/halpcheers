package pow

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"math/bits"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/obs"
)

var (
	ErrInvalidFormat = errors.New("invalid pow header format")
	ErrExpiredEpoch  = errors.New("pow epoch expired or out of tolerance")
	ErrInsufficient  = errors.New("insufficient proof of work difficulty")
)

// DifficultyFeed supplies dynamic difficulty escalations from the Guard layer.
// This preserves the abstraction boundary: PoW does not access Guard internals.
type DifficultyFeed interface {
	Difficulty(target string, sender core.AccountID) uint8
}

// Engine manages challenge rotation, difficulty calculation, and hot-path verification.
type Engine struct {
	secret        []byte
	epochDuration time.Duration
	floorMs       int
	signupMs      int
	feed          DifficultyFeed
	metrics       *obs.Metrics

	// Cached challenge state for fast lookups without HMAC recomputation on hot path.
	mu             sync.RWMutex
	cachedEpoch    uint64
	cachedPrevChal [32]byte
	cachedCurrChal [32]byte
	cachedNextChal [32]byte

	// Current floor difficulty (d leading zero bits)
	floorDifficulty uint8
	signupDiff      uint8
}

// Config holds initialization parameters for the Engine.
type Config struct {
	Secret        []byte
	EpochDuration time.Duration
	FloorMs       int
	SignupMs      int
	Feed          DifficultyFeed
	Metrics       *obs.Metrics
}

// New creates an Engine. If Secret is nil, 32 bytes of secure random are generated.
func New(cfg Config) (*Engine, error) {
	secret := cfg.Secret
	if len(secret) == 0 {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
	}
	epochDuration := cfg.EpochDuration
	if epochDuration <= 0 {
		epochDuration = 5 * time.Minute
	}
	floorMs := cfg.FloorMs
	if floorMs <= 0 {
		floorMs = 10
	}
	signupMs := cfg.SignupMs
	if signupMs <= 0 {
		signupMs = 1500
	}

	e := &Engine{
		secret:          secret,
		epochDuration:   epochDuration,
		floorMs:         floorMs,
		signupMs:        signupMs,
		feed:            cfg.Feed,
		metrics:         cfg.Metrics,
		floorDifficulty: MillisecondsToDifficulty(floorMs),
		signupDiff:      MillisecondsToDifficulty(signupMs),
	}

	e.rotateChallenges(e.currentEpoch(time.Now()))
	if e.metrics != nil {
		e.metrics.SetPoWDifficulty(float64(e.floorDifficulty))
	}
	return e, nil
}

// MillisecondsToDifficulty translates target client computation time in milliseconds
// into leading zero bits d (documented in docs/ABUSE.md calibration table).
func MillisecondsToDifficulty(ms int) uint8 {
	switch {
	case ms <= 3:
		return 12
	case ms <= 7:
		return 13
	case ms <= 15: // ~10 ms default floor
		return 14
	case ms <= 30:
		return 15
	case ms <= 70:
		return 16
	case ms <= 150:
		return 17
	case ms <= 300:
		return 18
	case ms <= 700:
		return 19
	case ms <= 1200:
		return 20
	case ms <= 2500: // ~1.5 s signup difficulty
		return 21
	case ms <= 5000:
		return 22
	case ms <= 10000:
		return 23
	default:
		return 24
	}
}

func (e *Engine) currentEpoch(t time.Time) uint64 {
	return uint64(t.Unix()) / uint64(e.epochDuration.Seconds())
}

// CurrentEpoch returns the active epoch number.
func (e *Engine) CurrentEpoch() uint64 {
	return e.currentEpoch(time.Now())
}

// CurrentChallenge returns the active epoch and its server-seeded challenge.
func (e *Engine) CurrentChallenge() (uint64, [32]byte, uint8) {
	now := time.Now()
	cur := e.currentEpoch(now)
	e.ensureEpoch(cur)

	e.mu.RLock()
	defer e.mu.RUnlock()
	return cur, e.cachedCurrChal, e.floorDifficulty
}

// SignupDifficulty returns the difficulty required for account registration.
func (e *Engine) SignupDifficulty() uint8 {
	return e.signupDiff
}

// deriveChallenge derives HMAC-SHA256(secret, epoch).
func deriveChallenge(secret []byte, epoch uint64) [32]byte {
	var epochBuf [8]byte
	epochBuf[0] = byte(epoch >> 56)
	epochBuf[1] = byte(epoch >> 48)
	epochBuf[2] = byte(epoch >> 40)
	epochBuf[3] = byte(epoch >> 32)
	epochBuf[4] = byte(epoch >> 24)
	epochBuf[5] = byte(epoch >> 16)
	epochBuf[6] = byte(epoch >> 8)
	epochBuf[7] = byte(epoch)

	mac := hmac.New(sha256.New, secret)
	mac.Write(epochBuf[:])
	var res [32]byte
	copy(res[:], mac.Sum(nil))
	return res
}

func (e *Engine) ensureEpoch(cur uint64) {
	e.mu.RLock()
	if e.cachedEpoch == cur {
		e.mu.RUnlock()
		return
	}
	e.mu.RUnlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cachedEpoch != cur {
		e.rotateChallenges(cur)
	}
}

func (e *Engine) rotateChallenges(cur uint64) {
	e.cachedEpoch = cur
	if cur > 0 {
		e.cachedPrevChal = deriveChallenge(e.secret, cur-1)
	} else {
		e.cachedPrevChal = [32]byte{}
	}
	e.cachedCurrChal = deriveChallenge(e.secret, cur)
	e.cachedNextChal = deriveChallenge(e.secret, cur+1)
}

// getChallenge returns the cached challenge for epoch if within tolerance [cur-1, cur+1].
func (e *Engine) getChallenge(epoch uint64, cur uint64) ([32]byte, bool) {
	e.ensureEpoch(cur)

	e.mu.RLock()
	defer e.mu.RUnlock()

	switch epoch {
	case cur:
		return e.cachedCurrChal, true
	case cur - 1:
		return e.cachedPrevChal, true
	case cur + 1:
		return e.cachedNextChal, true
	default:
		return [32]byte{}, false
	}
}

// Difficulty returns the required difficulty for a target and sender.
func (e *Engine) Difficulty(target string, sender core.AccountID) uint8 {
	diff := e.floorDifficulty
	if e.feed != nil {
		feedDiff := e.feed.Difficulty(target, sender)
		if feedDiff > diff {
			diff = feedDiff
		}
	}
	return diff
}

// ParseToken parses the wire format header "X-Halp-PoW: <epoch>.<nonce>".
func ParseToken(headerVal string) (epoch uint64, nonce string, err error) {
	dot := strings.IndexByte(headerVal, '.')
	if dot <= 0 || dot == len(headerVal)-1 {
		return 0, "", ErrInvalidFormat
	}
	epochStr := headerVal[:dot]
	nonce = headerVal[dot+1:]

	epoch, err = strconv.ParseUint(epochStr, 10, 64)
	if err != nil {
		return 0, "", ErrInvalidFormat
	}
	return epoch, nonce, nil
}

// Verify evaluates SHA-256(handle || epoch || challenge || nonce) on the hot path.
// INVARIANT: Allocation-free, O(1), ~2 µs budget.
func (e *Engine) Verify(handle string, tokenHeader string, target string, sender core.AccountID) error {
	epoch, nonce, err := ParseToken(tokenHeader)
	if err != nil {
		if e.metrics != nil {
			e.metrics.IncPingsDropped(obs.DropReasonInvalidPoW)
		}
		return err
	}

	cur := e.CurrentEpoch()
	challenge, ok := e.getChallenge(epoch, cur)
	if !ok {
		if e.metrics != nil {
			e.metrics.IncPingsDropped(obs.DropReasonInvalidPoW)
		}
		return ErrExpiredEpoch
	}

	requiredDiff := e.Difficulty(target, sender)
	if !CheckPoW(handle, epoch, challenge, nonce, requiredDiff) {
		if e.metrics != nil {
			e.metrics.IncPingsDropped(obs.DropReasonInvalidPoW)
		}
		return ErrInsufficient
	}

	return nil
}

// CheckPoW computes SHA-256(handle || epoch || challenge || nonce) and checks leading zero bits.
// Strictly stack allocated and zero heap allocations.
func CheckPoW(handle string, epoch uint64, challenge [32]byte, nonce string, targetDifficulty uint8) bool {
	var buf [256]byte
	n := 0

	n += copy(buf[n:], handle)
	n += appendUint(buf[n:], epoch)
	n += copy(buf[n:], challenge[:])
	n += copy(buf[n:], nonce)

	digest := sha256.Sum256(buf[:n])
	return countLeadingZeroBits(&digest) >= targetDifficulty
}

// SolvePoW solves a hashcash challenge by finding a valid nonce. Used by clients and tests.
func SolvePoW(handle string, epoch uint64, challenge [32]byte, targetDifficulty uint8, maxIterations uint64) (string, bool) {
	var buf [256]byte
	baseN := 0
	baseN += copy(buf[baseN:], handle)
	baseN += appendUint(buf[baseN:], epoch)
	baseN += copy(buf[baseN:], challenge[:])

	for nonceVal := uint64(0); nonceVal < maxIterations; nonceVal++ {
		nonceN := appendUint(buf[baseN:], nonceVal)
		digest := sha256.Sum256(buf[:baseN+nonceN])
		if countLeadingZeroBits(&digest) >= targetDifficulty {
			var nonceBytes [20]byte
			n := appendUint(nonceBytes[:], nonceVal)
			return string(nonceBytes[:n]), true
		}
	}
	return "", false
}

func countLeadingZeroBits(d *[32]byte) uint8 {
	var count uint8
	for _, b := range d {
		if b == 0 {
			count += 8
		} else {
			count += uint8(bits.LeadingZeros8(b))
			break
		}
	}
	return count
}

func appendUint(dst []byte, v uint64) int {
	if v == 0 {
		dst[0] = '0'
		return 1
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + (v % 10))
		v /= 10
	}
	return copy(dst, b[i:])
}

// DynamicFeed provides a simple thread-safe DifficultyFeed implementation for testing or orchestration.
type DynamicFeed struct {
	escalations sync.Map
	globalDiff  atomic.Uint32
}

func NewDynamicFeed(initialGlobal uint8) *DynamicFeed {
	df := &DynamicFeed{}
	df.globalDiff.Store(uint32(initialGlobal))
	return df
}

func (df *DynamicFeed) SetTargetDifficulty(target string, diff uint8) {
	df.escalations.Store("t:"+target, diff)
}

func (df *DynamicFeed) SetSenderDifficulty(sender core.AccountID, diff uint8) {
	df.escalations.Store("s:"+strconv.FormatInt(sender.Int64(), 10), diff)
}

func (df *DynamicFeed) SetGlobalDifficulty(diff uint8) {
	df.globalDiff.Store(uint32(diff))
}

func (df *DynamicFeed) Difficulty(target string, sender core.AccountID) uint8 {
	global := uint8(df.globalDiff.Load())
	maxDiff := global

	if val, ok := df.escalations.Load("t:" + target); ok {
		if d := val.(uint8); d > maxDiff {
			maxDiff = d
		}
	}
	if val, ok := df.escalations.Load("s:" + strconv.FormatInt(sender.Int64(), 10)); ok {
		if d := val.(uint8); d > maxDiff {
			maxDiff = d
		}
	}
	return maxDiff
}
