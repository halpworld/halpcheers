package http

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/stream"
)

// StreamAuthenticator resolves incoming session tokens for StreamHandler.
type StreamAuthenticator interface {
	AuthenticateSession(ctx context.Context, token string) (core.AccountID, bool)
}

// PendingProvider reads and clears accumulated counts for GET /v1/pending.
type PendingProvider interface {
	GetAndClearPending(account core.AccountID) int
}

// StreamHandler handles /v1/stream and /v1/pending.
// Owned by the SSE track (#17).
type StreamHandler struct {
	hub     *stream.Hub
	auth    StreamAuthenticator
	pending PendingProvider
}

// NewStreamHandler creates an unconfigured StreamHandler stub.
func NewStreamHandler() *StreamHandler {
	return &StreamHandler{}
}

// NewStreamHandlerWithDeps creates a StreamHandler with hub, auth, and pending provider.
func NewStreamHandlerWithDeps(hub *stream.Hub, auth StreamAuthenticator, pending PendingProvider) *StreamHandler {
	return &StreamHandler{
		hub:     hub,
		auth:    auth,
		pending: pending,
	}
}

func (h *StreamHandler) getAccount(r *http.Request) (core.AccountID, bool) {
	token, ok := SessionFromContext(r.Context())
	if !ok || token == "" {
		return 0, false
	}
	if h.auth != nil {
		return h.auth.AuthenticateSession(r.Context(), token)
	}
	return 1001, true
}

// Stream establishes a persistent Server-Sent Events connection.
// GET /v1/stream
func (h *StreamHandler) Stream(w http.ResponseWriter, r *http.Request) {
	if h.hub == nil {
		NotImplemented(w, r)
		return
	}

	accID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	h.hub.ServeHTTP(w, r, accID)
}

// Pending returns and clears the accumulated coalesced count.
// GET /v1/pending
func (h *StreamHandler) Pending(w http.ResponseWriter, r *http.Request) {
	if h.hub == nil && h.pending == nil {
		NotImplemented(w, r)
		return
	}

	accID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	count := 0
	if h.pending != nil {
		count = h.pending.GetAndClearPending(accID)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]int{"n": count})
}
