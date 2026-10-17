package http_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/halpworld/halpcheers/server/internal/core"
	serverhttp "github.com/halpworld/halpcheers/server/internal/http"
	"github.com/halpworld/halpcheers/server/internal/push/webpush"
	"github.com/halpworld/halpcheers/server/internal/store"
)

func setupSubTestStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test_sub.db")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("failed to open store: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})
	return st
}

type fakeAuth struct {
	validToken string
	accountID  core.AccountID
}

func (f *fakeAuth) AuthenticateSession(ctx context.Context, token string) (core.AccountID, bool) {
	if token == f.validToken {
		return f.accountID, true
	}
	return 0, false
}

func TestSubscriptionsCRUD(t *testing.T) {
	st := setupSubTestStore(t)
	ctx := context.Background()

	accID, err := st.CreateAccount(ctx, []byte("hashsub"), "eu-1")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	auth := &fakeAuth{validToken: "tok_sub", accountID: accID}
	handler := serverhttp.NewSubscriptionsHandlerWithDeps(st, auth)
	router := serverhttp.NewRouter(serverhttp.RouterDeps{
		Subscriptions: handler,
	})

	// 1. Create subscription
	body := `{"endpoint":"https://push.example.com/sub/test1","p256dh":"010203","auth":"040506","kind":"webpush"}`
	r1 := httptest.NewRequest(http.MethodPost, "/v1/subscriptions", strings.NewReader(body))
	r1.Header.Set("Authorization", "Bearer tok_sub")
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, r1)

	if w1.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", w1.Code, w1.Body.String())
	}

	var res1 struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(w1.Body).Decode(&res1); err != nil || res1.ID == 0 {
		t.Fatalf("expected valid id, got %v (err: %v)", res1.ID, err)
	}

	// 2. Re-registration updates in place
	r2 := httptest.NewRequest(http.MethodPost, "/v1/subscriptions", strings.NewReader(body))
	r2.Header.Set("Authorization", "Bearer tok_sub")
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, r2)

	if w2.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created on re-registration, got %d", w2.Code)
	}

	// 3. Delete subscription
	delURL := "/v1/subscriptions/" + strconv.FormatInt(res1.ID, 10)
	r3 := httptest.NewRequest(http.MethodDelete, delURL, nil)
	r3.Header.Set("Authorization", "Bearer tok_sub")
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, r3)

	if w3.Code != http.StatusNoContent {
		t.Fatalf("expected 204 No Content on delete, got %d", w3.Code)
	}
}

func TestBrowserBase64URLSubscriptionKeyDecodingAndKindValidation(t *testing.T) {
	st := setupSubTestStore(t)
	ctx := context.Background()

	accID, err := st.CreateAccount(ctx, []byte("hashsub_b64"), "eu-1")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	auth := &fakeAuth{validToken: "tok_sub_b64", accountID: accID}
	handler := serverhttp.NewSubscriptionsHandlerWithDeps(st, auth)
	router := serverhttp.NewRouter(serverhttp.RouterDeps{
		Subscriptions: handler,
	})

	// 1. Generate real 65-byte uncompressed P-256 public key and 16-byte auth secret
	uaPrivKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ecdh key: %v", err)
	}
	uaPubBytes := uaPrivKey.PublicKey().Bytes()
	if len(uaPubBytes) != 65 {
		t.Fatalf("expected 65-byte P-256 pubkey, got %d", len(uaPubBytes))
	}

	authBytes := make([]byte, 16)
	if _, err := rand.Read(authBytes); err != nil {
		t.Fatalf("generate auth secret: %v", err)
	}

	// Browser PushSubscription.toJSON() produces base64url strings without padding
	p256dhB64 := base64.RawURLEncoding.EncodeToString(uaPubBytes)
	authB64 := base64.RawURLEncoding.EncodeToString(authBytes)

	// 2. Test valid kind "webpush" with browser base64url keys
	targetURL := "https://push.example.com/browser/1"
	bodyMap := map[string]any{
		"endpoint": targetURL,
		"p256dh":   p256dhB64,
		"auth":     authB64,
		"kind":     "webpush",
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	req1 := httptest.NewRequest(http.MethodPost, "/v1/subscriptions", bytes.NewReader(bodyBytes))
	req1.Header.Set("Authorization", "Bearer tok_sub_b64")
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)

	if w1.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", w1.Code, w1.Body.String())
	}

	// Verify exact raw bytes stored in SQLite
	var (
		storedKind   string
		storedP256DH []byte
		storedAuth   []byte
	)
	err = st.ReadDB().QueryRowContext(ctx,
		"SELECT kind, p256dh, auth FROM subscriptions WHERE endpoint = ?", targetURL).Scan(&storedKind, &storedP256DH, &storedAuth)
	if err != nil {
		t.Fatalf("query subscription: %v", err)
	}

	if storedKind != "webpush" {
		t.Fatalf("expected kind 'webpush', got %q", storedKind)
	}
	if len(storedP256DH) != 65 || !bytes.Equal(storedP256DH, uaPubBytes) {
		t.Fatalf("stored p256dh mismatch: len=%d, expected 65 bytes matching uaPubBytes", len(storedP256DH))
	}
	if len(storedAuth) != 16 || !bytes.Equal(storedAuth, authBytes) {
		t.Fatalf("stored auth mismatch: len=%d, expected 16 bytes matching authBytes", len(storedAuth))
	}

	// Verify end-to-end encryption works with webpush.Encrypt using stored keys
	enc, err := webpush.Encrypt(storedP256DH, storedAuth, []byte("cheers!"), webpush.Options{
		Endpoint: targetURL,
	})
	if err != nil {
		t.Fatalf("webpush.Encrypt failed with decoded stored keys: %v", err)
	}
	decrypted, err := webpush.Decrypt(uaPrivKey, storedAuth, enc.Payload)
	if err != nil {
		t.Fatalf("webpush.Decrypt failed: %v", err)
	}
	if string(decrypted) != "cheers!" {
		t.Fatalf("expected decrypted 'cheers!', got %q", string(decrypted))
	}

	// 3. Test valid kinds: "apns" and "fcm"
	for _, validKind := range []string{"apns", "fcm"} {
		validURL := "https://push.example.com/" + validKind + "/1"
		bBytes, _ := json.Marshal(map[string]any{
			"endpoint": validURL,
			"kind":     validKind,
		})
		r := httptest.NewRequest(http.MethodPost, "/v1/subscriptions", bytes.NewReader(bBytes))
		r.Header.Set("Authorization", "Bearer tok_sub_b64")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201 Created for valid kind %q, got %d", validKind, w.Code)
		}
	}

	// 4. Test empty kind defaults to "webpush"
	defaultURL := "https://push.example.com/default/1"
	bBytes, _ := json.Marshal(map[string]any{
		"endpoint": defaultURL,
	})
	rDefault := httptest.NewRequest(http.MethodPost, "/v1/subscriptions", bytes.NewReader(bBytes))
	rDefault.Header.Set("Authorization", "Bearer tok_sub_b64")
	wDefault := httptest.NewRecorder()
	router.ServeHTTP(wDefault, rDefault)
	if wDefault.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for empty kind, got %d", wDefault.Code)
	}
	var defKind string
	_ = st.ReadDB().QueryRowContext(ctx, "SELECT kind FROM subscriptions WHERE endpoint = ?", defaultURL).Scan(&defKind)
	if defKind != "webpush" {
		t.Fatalf("expected default kind 'webpush', got %q", defKind)
	}

	// 5. Test invalid kind returns 400 invalid_kind
	invalidURL := "https://push.example.com/invalid/1"
	bBad, _ := json.Marshal(map[string]any{
		"endpoint": invalidURL,
		"kind":     "windowsphone",
	})
	rBad := httptest.NewRequest(http.MethodPost, "/v1/subscriptions", bytes.NewReader(bBad))
	rBad.Header.Set("Authorization", "Bearer tok_sub_b64")
	wBad := httptest.NewRecorder()
	router.ServeHTTP(wBad, rBad)
	if wBad.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for invalid kind, got %d: %s", wBad.Code, wBad.Body.String())
	}
	if !strings.Contains(wBad.Body.String(), "invalid_kind") {
		t.Fatalf("expected 'invalid_kind' error in response, got %s", wBad.Body.String())
	}
}
