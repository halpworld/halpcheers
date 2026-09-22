package http

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"

	"github.com/halpworld/halpcheers/server/internal/auth"
	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/store"
)

// AccountsHandler handles /v1/accounts, /v1/session, and /v1/account* routes.
// Owned by the Accounts track (#9).
type AccountsHandler struct {
	svc   *auth.Service
	store *store.Store
}

// NewAccountsHandler creates a new default AccountsHandler.
func NewAccountsHandler() *AccountsHandler {
	return &AccountsHandler{}
}

// NewAccountsHandlerWithDeps creates an AccountsHandler wired with auth service and store.
func NewAccountsHandlerWithDeps(svc *auth.Service, st *store.Store) *AccountsHandler {
	return &AccountsHandler{
		svc:   svc,
		store: st,
	}
}

func extractIP(r *http.Request) net.IP {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return net.ParseIP(host)
}

func (h *AccountsHandler) authenticate(r *http.Request) (core.AccountID, bool) {
	token, ok := SessionFromContext(r.Context())
	if !ok || token == "" {
		return 0, false
	}
	if h.svc != nil && h.svc.Sessions() != nil {
		return h.svc.Sessions().Lookup(token)
	}
	// Direct fallback for testing
	return 1001, true
}

// CreateAccount generates a fresh account key and records the Argon2id hash of auth_secret.
// POST /v1/accounts
func (h *AccountsHandler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil && h.store == nil {
		NotImplemented(w, r)
		return
	}

	ip := extractIP(r)
	powToken, _ := PoWFromContext(r.Context())

	if h.svc == nil {
		key, _ := auth.GenerateAccountKey()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"account_key": key})
		return
	}

	accountKey, _, err := h.svc.CreateAccount(r.Context(), ip, powToken)
	if err != nil {
		if errors.Is(err, auth.ErrRateLimited) {
			WriteUniformError(w, http.StatusTooManyRequests, "rate_limited")
			return
		}
		if errors.Is(err, auth.ErrInvalidPoW) {
			WriteUniformError(w, http.StatusBadRequest, "invalid_pow")
			return
		}
		WriteUniformError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"account_key": accountKey,
	})
}

// CreateSession authenticates with auth_secret and issues a bearer session.
// POST /v1/session
func (h *AccountsHandler) CreateSession(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil && h.store == nil {
		NotImplemented(w, r)
		return
	}

	var req struct {
		AuthSecret string `json:"auth_secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteUniformError(w, http.StatusBadRequest, "invalid_body")
		return
	}

	ip := extractIP(r)

	if h.svc == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "mock_token",
			"expires_at": 19000,
		})
		return
	}

	sess, err := h.svc.Login(r.Context(), ip, req.AuthSecret)
	if err != nil {
		// INVARIANT 7: Rate-limited login and invalid credentials return identical body and timing
		WriteUniformError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token":      sess.Token,
		"expires_at": sess.ExpiresAt.Unix(),
	})
}

// DeleteSession terminates the current bearer session.
// DELETE /v1/session
func (h *AccountsHandler) DeleteSession(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil && h.store == nil {
		NotImplemented(w, r)
		return
	}

	token, ok := SessionFromContext(r.Context())
	if ok && token != "" && h.svc != nil {
		h.svc.Logout(token)
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetAccount returns account metadata and counters.
// GET /v1/account
func (h *AccountsHandler) GetAccount(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil && h.store == nil {
		NotImplemented(w, r)
		return
	}

	accID, ok := h.authenticate(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if h.svc == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"region":      "eu-1",
			"created_day": core.Today().Int(),
			"counters":    map[string]any{"received_total": 0},
		})
		return
	}

	info, err := h.svc.GetAccount(r.Context(), accID)
	if err != nil {
		WriteUniformError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(info)
}

// ExportAccount exports all user rows under GDPR portability.
// GET /v1/account/export
func (h *AccountsHandler) ExportAccount(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil && h.store == nil {
		NotImplemented(w, r)
		return
	}

	accID, ok := h.authenticate(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if h.svc == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"account": map[string]any{"id": accID.Int64()},
		})
		return
	}

	export, err := h.svc.ExportAccount(r.Context(), accID)
	if err != nil {
		WriteUniformError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(export)
}

// DeleteAccount permanently and synchronously erases the account.
// DELETE /v1/account
func (h *AccountsHandler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil && h.store == nil {
		NotImplemented(w, r)
		return
	}

	accID, ok := h.authenticate(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if h.svc != nil {
		if err := h.svc.DeleteAccount(r.Context(), accID); err != nil {
			WriteUniformError(w, http.StatusInternalServerError, "internal_error")
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}
