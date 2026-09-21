package webpush_test

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"testing"
	"time"

	"github.com/halpworld/halpcheers/server/internal/push/webpush"
)

// TestRFC8188Section31 verifies the exact test vector from RFC 8188 §3.1.
func TestRFC8188Section31(t *testing.T) {
	// From RFC 8188 §3.1:
	// IKM (base64url) = "yqdlZ-tYemfogSmv7Ws5PQ"
	// Salt (base64url) = "I1BsxtFttlv3u_Oo94xnmw"
	// RS = 4096
	// KeyID = "" (empty, idlen = 0)
	// Plaintext = "I am the walrus"
	ikm, err := base64.RawURLEncoding.DecodeString("yqdlZ-tYemfogSmv7Ws5PQ")
	if err != nil {
		t.Fatalf("decode ikm failed: %v", err)
	}
	salt, err := base64.RawURLEncoding.DecodeString("I1BsxtFttlv3u_Oo94xnmw")
	if err != nil {
		t.Fatalf("decode salt failed: %v", err)
	}
	plaintext := []byte("I am the walrus")

	encrypted, err := webpush.EncryptRFC8188(ikm, salt, nil, plaintext, 4096)
	if err != nil {
		t.Fatalf("EncryptRFC8188 failed: %v", err)
	}

	expectedB64 := "I1BsxtFttlv3u_Oo94xnmwAAEAAA-NAVub2qFgBEuQKRapoZu-IxkIva3MEB1PD-ly8Thjg"
	expected, err := base64.RawURLEncoding.DecodeString(expectedB64)
	if err != nil {
		t.Fatalf("decode expected failed: %v", err)
	}

	if hex.EncodeToString(encrypted) != hex.EncodeToString(expected) {
		t.Fatalf("RFC 8188 §3.1 mismatch:\nGot:      %x\nExpected: %x", encrypted, expected)
	}

	// Decrypt round-trip
	decrypted, err := webpush.DecryptRFC8188(ikm, encrypted)
	if err != nil {
		t.Fatalf("DecryptRFC8188 failed: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Fatalf("Decrypted mismatch: got %q, expected %q", decrypted, plaintext)
	}
}

// TestRFC8291RoundTrip tests complete end-to-end encrypt and decrypt round trip.
func TestRFC8291RoundTrip(t *testing.T) {
	// Generate subscriber keypair and auth secret
	uaPrivKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate subscriber key: %v", err)
	}
	uaPubKey := uaPrivKey.PublicKey().Bytes()

	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatalf("failed to generate auth secret: %v", err)
	}

	plaintext := []byte("someone appreciates you")

	msg, err := webpush.Encrypt(uaPubKey, auth, plaintext, webpush.Options{
		TTL:      60,
		Urgency:  "high",
		Endpoint: "https://push.example.com/sub/123",
	})
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	if msg.Headers["Content-Encoding"] != "aes128gcm" {
		t.Errorf("expected Content-Encoding aes128gcm, got %s", msg.Headers["Content-Encoding"])
	}
	if msg.Headers["TTL"] != "60" {
		t.Errorf("expected TTL 60, got %s", msg.Headers["TTL"])
	}
	if msg.Headers["Urgency"] != "high" {
		t.Errorf("expected Urgency high, got %s", msg.Headers["Urgency"])
	}

	decrypted, err := webpush.Decrypt(uaPrivKey, auth, msg.Payload)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Fatalf("round trip mismatch: got %q, expected %q", decrypted, plaintext)
	}
}

// TestSaltUniqueness asserts 1,000 distinct salts and 1,000 distinct ciphertexts for same inputs.
func TestSaltUniqueness(t *testing.T) {
	uaPrivKey, _ := ecdh.P256().GenerateKey(rand.Reader)
	uaPubKey := uaPrivKey.PublicKey().Bytes()
	auth := make([]byte, 16)
	rand.Read(auth)

	cache := webpush.NewKeypairCache()
	plaintext := []byte("testing salt uniqueness across 1000 sends")
	endpoint := "https://push.example.com/unique-salt"

	seenSalts := make(map[string]struct{}, 1000)
	seenCiphertexts := make(map[string]struct{}, 1000)

	for i := 0; i < 1000; i++ {
		msg, err := webpush.Encrypt(uaPubKey, auth, plaintext, webpush.Options{
			Endpoint: endpoint,
			Cache:    cache,
		})
		if err != nil {
			t.Fatalf("Encrypt iteration %d failed: %v", i, err)
		}

		salt := string(msg.Payload[:webpush.SaltLength])
		if _, exists := seenSalts[salt]; exists {
			t.Fatalf("FATAL: duplicate salt observed at iteration %d (AES-GCM catastrophe)", i)
		}
		seenSalts[salt] = struct{}{}

		ct := string(msg.Payload)
		if _, exists := seenCiphertexts[ct]; exists {
			t.Fatalf("FATAL: duplicate ciphertext observed at iteration %d", i)
		}
		seenCiphertexts[ct] = struct{}{}
	}

	if len(seenSalts) != 1000 {
		t.Fatalf("expected 1,000 distinct salts, got %d", len(seenSalts))
	}
	if len(seenCiphertexts) != 1000 {
		t.Fatalf("expected 1,000 distinct ciphertexts, got %d", len(seenCiphertexts))
	}
}

// TestCachedKeypairProducesDifferentCiphertext verifies that reusing a cached shared secret
// still produces fresh, different ciphertexts on every single call.
func TestCachedKeypairProducesDifferentCiphertext(t *testing.T) {
	uaPrivKey, _ := ecdh.P256().GenerateKey(rand.Reader)
	uaPubKey := uaPrivKey.PublicKey().Bytes()
	auth := make([]byte, 16)
	rand.Read(auth)

	cache := webpush.NewKeypairCache()
	endpoint := "https://push.example.com/cache-test"
	plaintext := []byte("{\"n\":5}")

	msg1, err := webpush.Encrypt(uaPubKey, auth, plaintext, webpush.Options{
		Endpoint: endpoint,
		Cache:    cache,
	})
	if err != nil {
		t.Fatalf("first encrypt failed: %v", err)
	}

	msg2, err := webpush.Encrypt(uaPubKey, auth, plaintext, webpush.Options{
		Endpoint: endpoint,
		Cache:    cache,
	})
	if err != nil {
		t.Fatalf("second encrypt failed: %v", err)
	}

	if string(msg1.Payload) == string(msg2.Payload) {
		t.Fatalf("expected cached keypair to yield distinct ciphertexts, but they were identical")
	}

	// Verify both decrypt properly
	d1, err := webpush.Decrypt(uaPrivKey, auth, msg1.Payload)
	if err != nil || string(d1) != string(plaintext) {
		t.Fatalf("failed decrypting msg1: %v", err)
	}
	d2, err := webpush.Decrypt(uaPrivKey, auth, msg2.Payload)
	if err != nil || string(d2) != string(plaintext) {
		t.Fatalf("failed decrypting msg2: %v", err)
	}
}

// TestVAPIDSigningAndVerification tests RFC 8292 VAPID token generation and verification.
func TestVAPIDSigningAndVerification(t *testing.T) {
	vapidPrivKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate vapid key: %v", err)
	}

	endpoint := "https://fcm.googleapis.com/fcm/send/abc-123"
	subscriber := "mailto:ops@halp.local"

	header, err := webpush.SignVAPID(endpoint, subscriber, vapidPrivKey, time.Now().Add(1*time.Hour))
	if err != nil {
		t.Fatalf("SignVAPID failed: %v", err)
	}

	sub, exp, err := webpush.VerifyVAPID(header, "https://fcm.googleapis.com")
	if err != nil {
		t.Fatalf("VerifyVAPID failed: %v", err)
	}
	if sub != subscriber {
		t.Errorf("sub mismatch: got %s, expected %s", sub, subscriber)
	}
	if exp <= time.Now().Unix() {
		t.Errorf("exp is in the past: %d", exp)
	}
}

// TestPayloadlessSend asserts zero-length body and no Content-Encoding header.
func TestPayloadlessSend(t *testing.T) {
	vapidKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	endpoint := "https://push.example.com/sub/payloadless"

	msg, err := webpush.Encrypt(nil, nil, nil, webpush.Options{
		Endpoint:   endpoint,
		VAPIDKey:   vapidKey,
		Subscriber: "mailto:admin@halp.local",
	})
	if err != nil {
		t.Fatalf("Encrypt failed for payloadless send: %v", err)
	}

	if len(msg.Payload) != 0 {
		t.Fatalf("expected 0-byte payload, got %d bytes", len(msg.Payload))
	}
	if _, exists := msg.Headers["Content-Encoding"]; exists {
		t.Fatalf("payloadless message must not contain Content-Encoding header")
	}
	if msg.Headers["Authorization"] == "" {
		t.Fatalf("payloadless message must carry Authorization header")
	}
	if msg.Headers["TTL"] == "" {
		t.Fatalf("payloadless message must carry TTL header")
	}
}

// TestMalformedKeyInputsEdgeCases verifies error returns and zero panics.
func TestMalformedKeyInputsEdgeCases(t *testing.T) {
	auth := make([]byte, 16)
	validP256 := make([]byte, 65)
	validP256[0] = 0x04 // uncompressed prefix

	// Short key
	if _, err := webpush.Encrypt([]byte{1, 2, 3}, auth, []byte("data"), webpush.Options{}); err == nil {
		t.Errorf("expected error on short p256dh key")
	}

	// Bad auth length
	if _, err := webpush.Encrypt(validP256, []byte{1, 2, 3}, []byte("data"), webpush.Options{}); err == nil {
		t.Errorf("expected error on invalid auth length")
	}

	// Decrypt garbage
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	if _, err := webpush.Decrypt(uaPriv, auth, []byte{1, 2, 3}); err == nil {
		t.Errorf("expected error decrypting garbage data")
	}
}

func BenchmarkEncryptWithKeyCache(b *testing.B) {
	uaPrivKey, _ := ecdh.P256().GenerateKey(rand.Reader)
	uaPubKey := uaPrivKey.PublicKey().Bytes()
	auth := make([]byte, 16)
	rand.Read(auth)

	cache := webpush.NewKeypairCache()
	endpoint := "https://push.example.com/bench"
	plaintext := []byte("{\"n\":1}")

	// Pre-warm cache
	webpush.Encrypt(uaPubKey, auth, plaintext, webpush.Options{
		Endpoint: endpoint,
		Cache:    cache,
	})

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := webpush.Encrypt(uaPubKey, auth, plaintext, webpush.Options{
			Endpoint: endpoint,
			Cache:    cache,
		})
		if err != nil {
			b.Fatalf("Encrypt failed: %v", err)
		}
	}
}

func BenchmarkEncryptWithoutKeyCache(b *testing.B) {
	uaPrivKey, _ := ecdh.P256().GenerateKey(rand.Reader)
	uaPubKey := uaPrivKey.PublicKey().Bytes()
	auth := make([]byte, 16)
	rand.Read(auth)

	endpoint := "https://push.example.com/bench"
	plaintext := []byte("{\"n\":1}")

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := webpush.Encrypt(uaPubKey, auth, plaintext, webpush.Options{
			Endpoint: endpoint,
			Cache:    nil,
		})
		if err != nil {
			b.Fatalf("Encrypt failed: %v", err)
		}
	}
}
