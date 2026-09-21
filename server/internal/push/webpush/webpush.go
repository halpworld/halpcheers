package webpush

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"sync"
	"time"
)

const (
	DefaultRecordSize uint32 = 4096
	SaltLength        int    = 16
	P256KeyLength     int    = 65
)

var (
	ErrInvalidSubscriptionKey = errors.New("invalid subscription public key (must be 65-byte uncompressed P-256)")
	ErrInvalidAuthSecret      = errors.New("invalid subscription auth secret (must be 16 bytes)")
	ErrCiphertextTooShort     = errors.New("ciphertext too short for RFC 8188 header")
	ErrInvalidDelimiter       = errors.New("invalid record delimiter in decrypted payload")
	ErrKeyMismatch            = errors.New("key mismatch or authentication tag failure")
)

// CachedSubscriptionSecret caches the ECDH ephemeral keypair and derived IKM for a subscription.
// INVARIANT: The salt MUST remain fresh per message; only the ECDH handshake and IKM are cached.
type CachedSubscriptionSecret struct {
	ASPrivateKey *ecdh.PrivateKey
	ASPublicKey  []byte // 65-byte uncompressed point
	IKM          []byte // 32-byte derived IKM
}

// KeypairCache provides thread-safe in-memory caching of ECDH shared secrets per subscription endpoint.
type KeypairCache struct {
	mu    sync.RWMutex
	items map[string]*CachedSubscriptionSecret
}

// NewKeypairCache creates a new empty KeypairCache.
func NewKeypairCache() *KeypairCache {
	return &KeypairCache{
		items: make(map[string]*CachedSubscriptionSecret),
	}
}

// Get returns the cached secret for an endpoint, if present.
func (c *KeypairCache) Get(endpoint string) (*CachedSubscriptionSecret, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	secret, ok := c.items[endpoint]
	return secret, ok
}

// Put caches a shared secret for an endpoint.
func (c *KeypairCache) Put(endpoint string, secret *CachedSubscriptionSecret) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[endpoint] = secret
}

// Remove invalidates the cached key for an endpoint.
func (c *KeypairCache) Remove(endpoint string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, endpoint)
}

// Options parameters for Web Push dispatch encryption and headers.
type Options struct {
	TTL        int    // Time-To-Live in seconds (default 60)
	Urgency    string // very-low | low | normal | high (default "normal")
	VAPIDKey   *ecdsa.PrivateKey
	Subscriber string // mailto: or url
	Endpoint   string // target subscription endpoint URL
	Cache      *KeypairCache
}

// EncryptedMessage holds the wire payload and HTTP request headers.
type EncryptedMessage struct {
	Payload []byte
	Headers map[string]string
}

// Encrypt prepares a Web Push dispatch per RFC 8291 and RFC 8188.
// If plaintext is empty, a payloadless notification is returned (zero body, no Content-Encoding).
func Encrypt(p256dh []byte, auth []byte, plaintext []byte, opts Options) (*EncryptedMessage, error) {
	headers := make(map[string]string)

	ttl := opts.TTL
	if ttl <= 0 {
		ttl = 60
	}
	headers["TTL"] = fmt.Sprintf("%d", ttl)

	urgency := opts.Urgency
	if urgency == "" {
		urgency = "normal"
	}
	headers["Urgency"] = urgency

	// Sign VAPID header if key is provided
	if opts.VAPIDKey != nil && opts.Endpoint != "" {
		vapidHeader, err := SignVAPID(opts.Endpoint, opts.Subscriber, opts.VAPIDKey, time.Now().Add(12*time.Hour))
		if err != nil {
			return nil, fmt.Errorf("vapid sign error: %w", err)
		}
		headers["Authorization"] = vapidHeader
	}

	// Payloadless send: empty body and no Content-Encoding header
	if len(plaintext) == 0 {
		return &EncryptedMessage{
			Payload: nil,
			Headers: headers,
		}, nil
	}

	if len(p256dh) != P256KeyLength {
		return nil, ErrInvalidSubscriptionKey
	}
	if len(auth) != 16 {
		return nil, ErrInvalidAuthSecret
	}

	uaKey, err := ecdh.P256().NewPublicKey(p256dh)
	if err != nil {
		return nil, fmt.Errorf("invalid p256dh: %w", err)
	}

	// 1. Obtain or generate IKM and server ephemeral key
	var asPublicBytes []byte
	var ikm []byte

	var cached *CachedSubscriptionSecret
	if opts.Cache != nil && opts.Endpoint != "" {
		cached, _ = opts.Cache.Get(opts.Endpoint)
	}

	if cached != nil {
		asPublicBytes = cached.ASPublicKey
		ikm = cached.IKM
	} else {
		asPrivKey, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("failed generating ephemeral key: %w", err)
		}
		asPubKey := asPrivKey.PublicKey()
		asPublicBytes = asPubKey.Bytes()

		sharedSecret, err := asPrivKey.ECDH(uaKey)
		if err != nil {
			return nil, fmt.Errorf("ecdh failed: %w", err)
		}

		ikm, err = deriveIKM(sharedSecret, auth, p256dh, asPublicBytes)
		if err != nil {
			return nil, fmt.Errorf("ikm derivation failed: %w", err)
		}

		if opts.Cache != nil && opts.Endpoint != "" {
			opts.Cache.Put(opts.Endpoint, &CachedSubscriptionSecret{
				ASPrivateKey: asPrivKey,
				ASPublicKey:  asPublicBytes,
				IKM:          ikm,
			})
		}
	}

	// 2. Generate fresh 16-byte random salt per message (NON-NEGOTIABLE)
	salt := make([]byte, SaltLength)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("failed to generate random salt: %w", err)
	}

	// 3. Encrypt payload using RFC 8188 aes128gcm
	body, err := EncryptRFC8188(ikm, salt, asPublicBytes, plaintext, DefaultRecordSize)
	if err != nil {
		return nil, fmt.Errorf("rfc8188 encrypt failed: %w", err)
	}

	headers["Content-Encoding"] = "aes128gcm"
	headers["Content-Type"] = "application/octet-stream"

	return &EncryptedMessage{
		Payload: body,
		Headers: headers,
	}, nil
}

// deriveIKM computes IKM per RFC 8291 Section 3.2.
func deriveIKM(sharedSecret, auth, uaPublic, asPublic []byte) ([]byte, error) {
	// PRK = HKDF-Extract(salt=auth, IKM=sharedSecret)
	prk, err := hkdf.Extract(sha256.New, sharedSecret, auth)
	if err != nil {
		return nil, err
	}

	// info = "WebPush: info\x00" || ua_public || as_public
	var info bytes.Buffer
	info.WriteString("WebPush: info\x00")
	info.Write(uaPublic)
	info.Write(asPublic)

	// IKM = HKDF-Expand(PRK, info, 32)
	return hkdf.Expand(sha256.New, prk, info.String(), 32)
}

// EncryptRFC8188 encrypts plaintext according to RFC 8188 Section 2.
func EncryptRFC8188(ikm, salt, keyID, plaintext []byte, rs uint32) ([]byte, error) {
	if len(salt) != SaltLength {
		return nil, errors.New("salt must be 16 bytes")
	}
	if rs < 18 {
		return nil, errors.New("rs must be at least 18 bytes")
	}

	// PRK = HKDF-Extract(salt, ikm)
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}

	// CEK = HKDF-Expand(PRK, "Content-Encoding: aes128gcm\x00", 16)
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}

	// Nonce = HKDF-Expand(PRK, "Content-Encoding: nonce\x00", 12)
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// Build RFC 8188 Header:
	// salt (16) || rs (4) || idlen (1) || keyid (len)
	var header bytes.Buffer
	header.Write(salt)

	var rsBytes [4]byte
	binary.BigEndian.PutUint32(rsBytes[:], rs)
	header.Write(rsBytes[:])

	header.WriteByte(byte(len(keyID)))
	header.Write(keyID)

	// Single record: plaintext || 0x02
	record := make([]byte, len(plaintext)+1)
	copy(record, plaintext)
	record[len(plaintext)] = 0x02

	// Encrypt record with nonce ^ seq (seq=0, so nonce unchanged)
	ciphertext := gcm.Seal(nil, nonce, record, nil)

	header.Write(ciphertext)
	return header.Bytes(), nil
}

// DecryptRFC8188 decrypts an RFC 8188 message given the ikm.
func DecryptRFC8188(ikm, data []byte) ([]byte, error) {
	if len(data) < SaltLength+4+1 {
		return nil, ErrCiphertextTooShort
	}

	salt := data[:SaltLength]
	rs := binary.BigEndian.Uint32(data[SaltLength : SaltLength+4])
	idlen := int(data[SaltLength+4])

	headerLen := SaltLength + 4 + 1 + idlen
	if len(data) < headerLen {
		return nil, ErrCiphertextTooShort
	}

	ciphertext := data[headerLen:]

	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}

	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}

	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	plainWithDelimiter, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrKeyMismatch
	}

	if len(plainWithDelimiter) == 0 {
		return nil, ErrInvalidDelimiter
	}

	// Verify delimiter byte 0x02 (final record)
	delimiter := plainWithDelimiter[len(plainWithDelimiter)-1]
	if delimiter != 0x02 {
		return nil, ErrInvalidDelimiter
	}

	_ = rs
	return plainWithDelimiter[:len(plainWithDelimiter)-1], nil
}

// Decrypt decrypts an RFC 8291 Web Push message using the recipient's private key and auth secret.
func Decrypt(uaPrivKey *ecdh.PrivateKey, auth []byte, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < SaltLength+4+1+P256KeyLength {
		return nil, ErrCiphertextTooShort
	}

	asPublicBytes := ciphertext[SaltLength+4+1 : SaltLength+4+1+P256KeyLength]
	asPubKey, err := ecdh.P256().NewPublicKey(asPublicBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid ephemeral public key in header: %w", err)
	}

	sharedSecret, err := uaPrivKey.ECDH(asPubKey)
	if err != nil {
		return nil, fmt.Errorf("ecdh failed: %w", err)
	}

	uaPublicBytes := uaPrivKey.PublicKey().Bytes()
	ikm, err := deriveIKM(sharedSecret, auth, uaPublicBytes, asPublicBytes)
	if err != nil {
		return nil, fmt.Errorf("ikm derivation failed: %w", err)
	}

	return DecryptRFC8188(ikm, ciphertext)
}

// SignVAPID generates an RFC 8292 Authorization header string.
func SignVAPID(endpoint string, subscriber string, privKey *ecdsa.PrivateKey, expiresAt time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid endpoint URL: %w", err)
	}
	audience := fmt.Sprintf("%s://%s", u.Scheme, u.Host)

	headerJSON := `{"typ":"JWT","alg":"ES256"}`
	headerB64 := base64.RawURLEncoding.EncodeToString([]byte(headerJSON))

	claims := map[string]any{
		"aud": audience,
		"exp": expiresAt.Unix(),
		"sub": subscriber,
	}
	claimsBytes, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsBytes)

	signingInput := headerB64 + "." + claimsB64
	hash := sha256.Sum256([]byte(signingInput))

	r, s, err := ecdsa.Sign(rand.Reader, privKey, hash[:])
	if err != nil {
		return "", fmt.Errorf("ecdsa sign failed: %w", err)
	}

	// RFC 7515 §A.3.1: ES256 signature is r || s zero-padded to 32 bytes each
	var sigBytes [64]byte
	rBytes := r.Bytes()
	sBytes := s.Bytes()
	copy(sigBytes[32-len(rBytes):32], rBytes)
	copy(sigBytes[64-len(sBytes):64], sBytes)

	jwt := signingInput + "." + base64.RawURLEncoding.EncodeToString(sigBytes[:])

	// Export uncompressed public key: 0x04 || X || Y using non-deprecated ecdh API
	ecdhPub, err := privKey.PublicKey.ECDH()
	if err != nil {
		return "", fmt.Errorf("failed converting public key to ecdh: %w", err)
	}
	pubBytes := ecdhPub.Bytes()
	pubB64 := base64.RawURLEncoding.EncodeToString(pubBytes)

	return fmt.Sprintf("vapid t=%s, k=%s", jwt, pubB64), nil
}

// VerifyVAPID verifies an RFC 8292 Authorization header for testing.
func VerifyVAPID(authHeader string, expectedAudience string) (sub string, exp int64, err error) {
	if len(authHeader) < 6 || authHeader[:6] != "vapid " {
		return "", 0, errors.New("missing vapid prefix")
	}

	var jwtStr, keyB64 string
	parts := bytes.Split([]byte(authHeader[6:]), []byte(", "))
	for _, p := range parts {
		if bytes.HasPrefix(p, []byte("t=")) {
			jwtStr = string(p[2:])
		} else if bytes.HasPrefix(p, []byte("k=")) {
			keyB64 = string(p[2:])
		}
	}

	if jwtStr == "" || keyB64 == "" {
		return "", 0, errors.New("invalid vapid header components")
	}

	pubBytes, err := base64.RawURLEncoding.DecodeString(keyB64)
	if err != nil {
		return "", 0, fmt.Errorf("failed to decode pubkey: %w", err)
	}
	pubKey, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pubBytes)
	if err != nil {
		return "", 0, fmt.Errorf("invalid uncompressed P-256 public key: %w", err)
	}

	jwtParts := bytes.Split([]byte(jwtStr), []byte("."))
	if len(jwtParts) != 3 {
		return "", 0, errors.New("invalid jwt token parts")
	}

	signingInput := string(jwtParts[0]) + "." + string(jwtParts[1])
	hash := sha256.Sum256([]byte(signingInput))

	sigBytes, err := base64.RawURLEncoding.DecodeString(string(jwtParts[2]))
	if err != nil || len(sigBytes) != 64 {
		return "", 0, errors.New("invalid signature encoding")
	}

	r := new(big.Int).SetBytes(sigBytes[:32])
	s := new(big.Int).SetBytes(sigBytes[32:])

	if !ecdsa.Verify(pubKey, hash[:], r, s) {
		return "", 0, errors.New("signature verification failed")
	}

	claimsBytes, err := base64.RawURLEncoding.DecodeString(string(jwtParts[1]))
	if err != nil {
		return "", 0, err
	}
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return "", 0, err
	}

	if expectedAudience != "" && claims.Aud != expectedAudience {
		return "", 0, fmt.Errorf("audience mismatch: got %s, expected %s", claims.Aud, expectedAudience)
	}

	return claims.Sub, claims.Exp, nil
}
