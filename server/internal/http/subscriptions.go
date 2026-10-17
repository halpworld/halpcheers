package http

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/store"
)

// SubscriptionsAuth resolves bearer session tokens for SubscriptionsHandler.
type SubscriptionsAuth interface {
	AuthenticateSession(ctx context.Context, token string) (core.AccountID, bool)
}

// SubscriptionsHandler handles /v1/subscriptions*.
// Owned by the Dispatch track (#16).
type SubscriptionsHandler struct {
	store *store.Store
	auth  SubscriptionsAuth
}

// NewSubscriptionsHandler creates a new unconfigured SubscriptionsHandler stub.
func NewSubscriptionsHandler() *SubscriptionsHandler {
	return &SubscriptionsHandler{}
}

// NewSubscriptionsHandlerWithDeps creates a SubscriptionsHandler with storage and auth dependencies.
func NewSubscriptionsHandlerWithDeps(st *store.Store, auth SubscriptionsAuth) *SubscriptionsHandler {
	return &SubscriptionsHandler{
		store: st,
		auth:  auth,
	}
}

func (h *SubscriptionsHandler) getAccount(r *http.Request) (core.AccountID, bool) {
	token, ok := SessionFromContext(r.Context())
	if !ok || token == "" {
		return 0, false
	}
	if h.auth != nil {
		return h.auth.AuthenticateSession(r.Context(), token)
	}
	return 1001, true
}

func decodeSubKey(s string) []byte {
	if s == "" {
		return nil
	}
	if len(s)%2 == 0 {
		if b, err := hex.DecodeString(s); err == nil && len(b) > 0 {
			return b
		}
	}
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil && len(b) > 0 {
		return b
	}
	if b, err := base64.URLEncoding.DecodeString(s); err == nil && len(b) > 0 {
		return b
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil && len(b) > 0 {
		return b
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) > 0 {
		return b
	}
	return []byte(s)
}

// CreateSubscription registers or updates a push subscription.
// POST /v1/subscriptions
func (h *SubscriptionsHandler) CreateSubscription(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		NotImplemented(w, r)
		return
	}

	accID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req struct {
		Endpoint string `json:"endpoint"`
		P256DH   string `json:"p256dh"`
		Auth     string `json:"auth"`
		Kind     string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteUniformError(w, http.StatusBadRequest, "invalid_body")
		return
	}

	if req.Endpoint == "" {
		WriteUniformError(w, http.StatusBadRequest, "missing_endpoint")
		return
	}
	if req.Kind == "" {
		req.Kind = "webpush"
	}
	if req.Kind != "webpush" && req.Kind != "apns" && req.Kind != "fcm" {
		WriteUniformError(w, http.StatusBadRequest, "invalid_kind")
		return
	}

	p256dhBytes := decodeSubKey(req.P256DH)
	authBytes := decodeSubKey(req.Auth)

	today := core.Today().Int()
	var subID int64

	err := h.store.Write(r.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(r.Context(), `
			INSERT INTO subscriptions (account_id, kind, endpoint, p256dh, auth, created_day)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(endpoint) DO UPDATE SET
				account_id = excluded.account_id,
				kind = excluded.kind,
				p256dh = excluded.p256dh,
				auth = excluded.auth,
				created_day = excluded.created_day
		`, accID.Int64(), req.Kind, req.Endpoint, p256dhBytes, authBytes, today)
		if err != nil {
			return err
		}

		return tx.QueryRowContext(r.Context(),
			"SELECT id FROM subscriptions WHERE endpoint = ?", req.Endpoint).Scan(&subID)
	})

	if err != nil {
		WriteUniformError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": subID,
	})
}

// DeleteSubscription removes a push subscription.
// DELETE /v1/subscriptions/{id}
func (h *SubscriptionsHandler) DeleteSubscription(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		NotImplemented(w, r)
		return
	}

	accID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	idStr := chi.URLParam(r, "id")
	subID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		WriteUniformNotFound(w)
		return
	}

	_ = h.store.Write(r.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(r.Context(),
			"DELETE FROM subscriptions WHERE id = ? AND account_id = ?",
			subID, accID.Int64())
		return err
	})

	w.WriteHeader(http.StatusNoContent)
}
