// Package client implements the Halp TUI HTTP/SSE client, HKDF key derivation,
// client-side PoW solver, and 300 ms send latency floor.
//
// INVARIANTS (AGENTS.md & docs/UI.md):
// - Account key NEVER leaves the device. Only auth_secret = HKDF-SHA256(account_key, info="halp/auth/v1") is sent.
// - SendPing enforces the 300 ms perceived latency floor and uniform "Sent." outcome (Invariant 7).
// - Notification copy scales with n ("Someone appreciates you." for n=1, "N people appreciate you." for n>=2).
package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	HKDFInfoAuth       = "halp/auth/v1"
	HKDFInfoContacts   = "halp/contacts/v1"
	HKDFInfoLocalStore = "halp/tui-local-store/v1"

	DifficultyFloor  uint8 = 14
	DifficultySignup uint8 = 21

	MinSendFloor = 300 * time.Millisecond
)

// FormatArrivalCopy renders the exact notification copy band from docs/UI.md §8 and docs/DELIVERY.md.
func FormatArrivalCopy(n int) string {
	if n <= 0 {
		return "No appreciation pings yet today."
	}
	if n == 1 {
		return "Someone appreciates you."
	}
	return fmt.Sprintf("%d people appreciate you.", n)
}

// NormalizeAccountKey strips whitespace and validates a 16-digit account key.
func NormalizeAccountKey(raw string) (string, error) {
	var b strings.Builder
	for _, r := range raw {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '-' {
			continue
		}
		if r < '0' || r > '9' {
			return "", errors.New("account key must contain only 16 digits")
		}
		b.WriteRune(r)
	}
	clean := b.String()
	if len(clean) != 16 {
		return "", errors.New("account key must be exactly 16 digits")
	}
	return clean, nil
}

// FormatAccountKey formats a 16-digit key into groups of four ("6421 8830 5197 4462").
func FormatAccountKey(raw string) string {
	clean, err := NormalizeAccountKey(raw)
	if err != nil {
		return raw
	}
	return fmt.Sprintf("%s %s %s %s", clean[0:4], clean[4:8], clean[8:12], clean[12:16])
}

// GenerateAccountKey generates a cryptographically random 16-digit account key.
func GenerateAccountKey() (string, error) {
	var digits [16]byte
	var buf [1]byte
	for i := 0; i < 16; i++ {
		for {
			if _, err := rand.Read(buf[:]); err != nil {
				return "", err
			}
			if buf[0] < 250 {
				digits[i] = '0' + (buf[0] % 10)
				break
			}
		}
	}
	return string(digits[:]), nil
}

// DeriveAuthSecretHex derives the 64-character hex auth_secret from the 16-digit account key.
// INVARIANT: The raw account key never leaves the device; only this derived secret is sent to POST /v1/session.
func DeriveAuthSecretHex(accountKey string) (string, error) {
	clean, err := NormalizeAccountKey(accountKey)
	if err != nil {
		return "", err
	}
	key, err := hkdf.Key(sha256.New, []byte(clean), nil, HKDFInfoAuth, 32)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(key), nil
}

// DeriveContactsKey derives the 32-byte contacts_key from the 16-digit account key.
// INVARIANT: Never leaves the device.
func DeriveContactsKey(accountKey string) ([]byte, error) {
	clean, err := NormalizeAccountKey(accountKey)
	if err != nil {
		return nil, err
	}
	return hkdf.Key(sha256.New, []byte(clean), nil, HKDFInfoContacts, 32)
}

// SaveAccountKeyEncrypted saves the 16-digit account key encrypted with AES-256-GCM on disk.
func SaveAccountKeyEncrypted(path string, accountKey string) error {
	clean, err := NormalizeAccountKey(accountKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	var salt [16]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return err
	}
	hostSeed, _ := os.Hostname()
	derived, err := hkdf.Key(sha256.New, []byte("halp-tui-"+hostSeed), salt[:], HKDFInfoLocalStore, 32)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(derived)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	sealed := gcm.Seal(nil, nonce[:], []byte(clean), nil)

	out := make([]byte, 0, 16+12+len(sealed))
	out = append(out, salt[:]...)
	out = append(out, nonce[:]...)
	out = append(out, sealed...)
	return os.WriteFile(path, []byte(hex.EncodeToString(out)), 0600)
}

// LoadAccountKeyEncrypted loads and decrypts the 16-digit account key from disk.
func LoadAccountKeyEncrypted(path string) (string, error) {
	rawHex, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	data, err := hex.DecodeString(strings.TrimSpace(string(rawHex)))
	if err != nil || len(data) < 16+12+16 {
		return "", errors.New("corrupted encrypted key file")
	}
	salt := data[0:16]
	nonce := data[16:28]
	ciphertext := data[28:]

	hostSeed, _ := os.Hostname()
	derived, err := hkdf.Key(sha256.New, []byte("halp-tui-"+hostSeed), salt, HKDFInfoLocalStore, 32)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(derived)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return NormalizeAccountKey(string(plain))
}

// SolvePoW grinds a nonce such that SHA-256(handle || epoch(8B BE) || challenge(32B) || nonce(8B BE))
// has at least difficulty leading zero bits.
func SolvePoW(handle string, epoch int64, challenge [32]byte, difficulty uint8) (uint64, string) {
	hBytes := []byte(handle)
	buf := make([]byte, len(hBytes)+8+32+8)
	copy(buf[0:len(hBytes)], hBytes)
	binary.BigEndian.PutUint64(buf[len(hBytes):len(hBytes)+8], uint64(epoch))
	copy(buf[len(hBytes)+8:len(hBytes)+40], challenge[:])
	nonceOffset := len(hBytes) + 40

	var nonce uint64
	for {
		binary.BigEndian.PutUint64(buf[nonceOffset:nonceOffset+8], nonce)
		sum := sha256.Sum256(buf)
		if hasLeadingZeros(sum, difficulty) {
			return nonce, fmt.Sprintf("%d.%d", epoch, nonce)
		}
		nonce++
	}
}

func hasLeadingZeros(hash [32]byte, d uint8) bool {
	if d == 0 {
		return true
	}
	fullBytes := int(d / 8)
	remBits := d % 8
	for i := 0; i < fullBytes; i++ {
		if hash[i] != 0 {
			return false
		}
	}
	if remBits > 0 {
		if uint8(bits.LeadingZeros8(hash[fullBytes])) < remBits {
			return false
		}
	}
	return true
}

// CalculateJitteredBackoff computes exponential backoff with full jitter in [0.5*exp, 1.5*exp].
func CalculateJitteredBackoff(attempt int, base, maxDelay time.Duration) time.Duration {
	if base <= 0 {
		base = 1 * time.Second
	}
	if maxDelay <= 0 {
		maxDelay = 30 * time.Second
	}
	shift := attempt
	if shift > 10 {
		shift = 10
	}
	exp := float64(base) * math.Pow(2, float64(shift))
	if exp > float64(maxDelay) {
		exp = float64(maxDelay)
	}
	var rnd [2]byte
	_, _ = rand.Read(rnd[:])
	unit := float64(binary.BigEndian.Uint16(rnd[:])) / 65535.0
	// Jitter between 0.5x and 1.5x
	jittered := exp * (0.5 + unit)
	if jittered > float64(maxDelay) {
		jittered = float64(maxDelay)
	}
	return time.Duration(jittered)
}

// HandleDTO represents a handle returned by the server.
type HandleDTO struct {
	Handle     string `json:"handle"`
	Label      string `json:"label,omitempty"`
	Kind       string `json:"kind"`
	Paused     bool   `json:"paused"`
	CreatedDay int64  `json:"created_day"`
}

// Client is the Halp TUI API and SSE client.
type Client struct {
	baseURL    string
	httpClient *http.Client
	mu         sync.RWMutex
	token      string
	powCache   map[string]string
}

// New creates a new TUI Client targeting baseURL.
func New(baseURL string, httpClient *http.Client) *Client {
	clean := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if clean == "" {
		clean = "https://halp.to"
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{
		baseURL:    clean,
		httpClient: httpClient,
		powCache:   make(map[string]string),
	}
}

// SetSessionToken sets the bearer session token.
func (c *Client) SetSessionToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = token
}

// SessionToken returns the current bearer session token.
func (c *Client) SessionToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

func (c *Client) getOrSolvePoW(target string, difficulty uint8) string {
	epoch := time.Now().Unix() / 300
	cacheKey := fmt.Sprintf("%d:%s:%d", epoch, target, difficulty)

	c.mu.RLock()
	if tok, ok := c.powCache[cacheKey]; ok {
		c.mu.RUnlock()
		return tok
	}
	c.mu.RUnlock()

	var challenge [32]byte
	_, headerVal := SolvePoW(target, epoch, challenge, difficulty)

	c.mu.Lock()
	c.powCache[cacheKey] = headerVal
	c.mu.Unlock()
	return headerVal
}

// LoginWithAccountKey derives auth_secret from the 16-digit account key and creates a session.
// INVARIANT: The 16-digit account key is NEVER transmitted over the network.
func (c *Client) LoginWithAccountKey(ctx context.Context, accountKey string) error {
	authSecretHex, err := DeriveAuthSecretHex(accountKey)
	if err != nil {
		return err
	}
	bodyBytes, _ := json.Marshal(map[string]string{
		"auth_secret": authSecretHex,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/session", bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return errors.New("That didn't work. Check the digits and try again.")
	}
	var res struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return err
	}
	c.SetSessionToken(res.Token)
	return nil
}

// CreateAccount registers a new account and returns the 16-digit account key once.
func (c *Client) CreateAccount(ctx context.Context, powDifficulty uint8) (string, error) {
	if powDifficulty == 0 {
		powDifficulty = DifficultySignup
	}
	powToken := c.getOrSolvePoW("signup", powDifficulty)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/accounts", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Halp-PoW", powToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", errors.New("Something went wrong. Try again.")
	}
	var res struct {
		AccountKey string `json:"account_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", err
	}
	return res.AccountKey, nil
}

// SendPing sends an appreciation ping to target (handle or @alias) while enforcing the 300 ms
// perceived latency floor (docs/UI.md § Uniformity).
//
// INVARIANT 7: Accepted, rate-limited, deduped, blocked, paused, and unknown targets all
// hold the 300 ms floor and return nil ("Sent."). Only a genuine network transport failure returns an error.
func (c *Client) SendPing(ctx context.Context, target string) error {
	start := time.Now()
	defer func() {
		elapsed := time.Since(start)
		if elapsed < MinSendFloor {
			select {
			case <-time.After(MinSendFloor - elapsed):
			case <-ctx.Done():
			}
		}
	}()

	cleanTarget := strings.TrimSpace(target)
	if cleanTarget == "" {
		return nil
	}

	powToken := c.getOrSolvePoW(cleanTarget, DifficultyFloor)
	endpoint := c.baseURL + "/v1/ping/" + url.PathEscape(cleanTarget)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return errors.New("Something went wrong. Try again.")
	}
	req.Header.Set("X-Halp-PoW", powToken)
	if tok := c.SessionToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return errors.New("Something went wrong. Try again.")
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	// Per docs/UI.md § Uniformity: show "Sent." on completion of the POST regardless of what the POST returned.
	return nil
}

// GetPending fetches the coalesced pending ping count from GET /v1/pending.
func (c *Client) GetPending(ctx context.Context) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/pending", nil)
	if err != nil {
		return 0, err
	}
	if tok := c.SessionToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status %d", resp.StatusCode)
	}
	var res struct {
		N int `json:"n"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return 0, err
	}
	return res.N, nil
}

// ListHandles lists the authenticated account's handles.
func (c *Client) ListHandles(ctx context.Context) ([]HandleDTO, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/handles", nil)
	if err != nil {
		return nil, err
	}
	if tok := c.SessionToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var res struct {
		Handles []HandleDTO `json:"handles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	return res.Handles, nil
}

// CreateHandle creates a new handle.
func (c *Client) CreateHandle(ctx context.Context, label, kind string) (HandleDTO, error) {
	bodyBytes, _ := json.Marshal(map[string]string{"label": label, "kind": kind})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/handles", bytes.NewReader(bodyBytes))
	if err != nil {
		return HandleDTO{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := c.SessionToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return HandleDTO{}, err
	}
	defer resp.Body.Close()
	var dto HandleDTO
	_ = json.NewDecoder(resp.Body).Decode(&dto)
	return dto, nil
}

// BurnHandle permanently deletes a handle.
func (c *Client) BurnHandle(ctx context.Context, handle string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/v1/handles/"+url.PathEscape(handle), nil)
	if err != nil {
		return err
	}
	if tok := c.SessionToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// StreamEvent represents an event from GET /v1/stream.
type StreamEvent struct {
	Type  string // "ping", "ka", "demote"
	Count int
}

// StreamSSE connects to GET /v1/stream and emits events to onEvent.
// If the server sends `event: demote` (after idle.demote_after), it automatically queries GET /v1/pending.
func (c *Client) StreamSSE(ctx context.Context, onEvent func(StreamEvent)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if tok := c.SessionToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	// Use a client without a short request timeout for long-lived SSE
	sseClient := &http.Client{
		Transport: c.httpClient.Transport,
	}
	resp, err := sseClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sse status %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	var currentEvent string
	var currentData string

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if currentEvent != "" {
				switch currentEvent {
				case "ping":
					var payload struct {
						N int `json:"n"`
					}
					if err := json.Unmarshal([]byte(currentData), &payload); err == nil && payload.N > 0 {
						onEvent(StreamEvent{Type: "ping", Count: payload.N})
					} else {
						onEvent(StreamEvent{Type: "ping", Count: 1})
					}
				case "ka":
					onEvent(StreamEvent{Type: "ka"})
				case "demote":
					onEvent(StreamEvent{Type: "demote"})
					if n, pErr := c.GetPending(ctx); pErr == nil && n > 0 {
						onEvent(StreamEvent{Type: "ping", Count: n})
					}
					return nil
				}
			}
			currentEvent = ""
			currentData = ""
			continue
		}
		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			currentData = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}

	return scanner.Err()
}
