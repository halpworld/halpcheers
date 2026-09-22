package http_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/halpworld/halpcheers/server/internal/core"
	serverhttp "github.com/halpworld/halpcheers/server/internal/http"
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
	delURL := fmt.Sprintf("/v1/subscriptions/%d", res1.ID)
	r3 := httptest.NewRequest(http.MethodDelete, delURL, nil)
	r3.Header.Set("Authorization", "Bearer tok_sub")
	w3 := httptest.NewRecorder()
	router.ServeHTTP(w3, r3)

	if w3.Code != http.StatusNoContent {
		t.Fatalf("expected 204 No Content on delete, got %d", w3.Code)
	}
}
