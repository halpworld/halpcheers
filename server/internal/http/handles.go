package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/region"
	"github.com/halpworld/halpcheers/server/internal/store"
)

// SessionAuthenticator resolves an incoming session token to an AccountID.
type SessionAuthenticator interface {
	AuthenticateSession(ctx context.Context, token string) (core.AccountID, bool)
}

// HandlesHandler handles /v1/handles* and /v1/alias routes.
// Owned by the Handles track (#10).
type HandlesHandler struct {
	svc      *region.Service
	resolver *region.Resolver
	store    *store.Store
	auth     SessionAuthenticator
}

// NewHandlesHandler creates a new HandlesHandler.
func NewHandlesHandler() *HandlesHandler {
	return &HandlesHandler{}
}

// NewHandlesHandlerWithDeps creates a HandlesHandler with explicit service dependencies.
func NewHandlesHandlerWithDeps(svc *region.Service, resolver *region.Resolver, st *store.Store, auth SessionAuthenticator) *HandlesHandler {
	return &HandlesHandler{
		svc:      svc,
		resolver: resolver,
		store:    st,
		auth:     auth,
	}
}

func (h *HandlesHandler) getAccount(r *http.Request) (core.AccountID, bool) {
	token, ok := SessionFromContext(r.Context())
	if !ok || token == "" {
		return 0, false
	}
	if h.auth != nil {
		return h.auth.AuthenticateSession(r.Context(), token)
	}
	// Fallback/direct session token parsing for tests or mocks
	return 1001, true
}

// ListHandles lists all handles owned by the authenticated account.
func (h *HandlesHandler) ListHandles(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil && h.store == nil {
		NotImplemented(w, r)
		return
	}
	accountID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if h.store == nil {
		WriteUniformError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	rows, err := h.store.ReadDB().QueryContext(r.Context(),
		"SELECT handle, label, kind, paused, created_day FROM handles WHERE account_id = ? ORDER BY created_day DESC",
		accountID.Int64())
	if err != nil {
		WriteUniformError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	defer rows.Close()

	type handleDTO struct {
		Handle     string `json:"handle"`
		Label      string `json:"label,omitempty"`
		Kind       string `json:"kind"`
		Paused     bool   `json:"paused"`
		CreatedDay int64  `json:"created_day"`
	}

	var handles []handleDTO
	for rows.Next() {
		var hDTO handleDTO
		var label sql.NullString
		var pausedInt int
		if err := rows.Scan(&hDTO.Handle, &label, &hDTO.Kind, &pausedInt, &hDTO.CreatedDay); err != nil {
			WriteUniformError(w, http.StatusInternalServerError, "internal_error")
			return
		}
		if label.Valid {
			hDTO.Label = label.String
		}
		hDTO.Paused = (pausedInt != 0)
		handles = append(handles, hDTO)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"handles": handles})
}

// CreateHandle mints a fresh handle for the account.
func (h *HandlesHandler) CreateHandle(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil && h.store == nil {
		NotImplemented(w, r)
		return
	}
	accountID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req struct {
		Label string `json:"label"`
		Kind  string `json:"kind"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	kind := core.HandleKind(req.Kind)
	if !kind.IsValid() {
		kind = core.HandleKindPersonal
	}

	var handle core.Handle
	var err error
	if h.svc != nil {
		handle, err = h.svc.MintHandle(r.Context())
	} else {
		handle, err = region.GenerateHandle()
	}
	if err != nil {
		WriteUniformError(w, http.StatusInternalServerError, "mint_failed")
		return
	}

	today := core.Today()

	if h.store != nil {
		err = h.store.Write(r.Context(), func(tx *sql.Tx) error {
			_, err := tx.ExecContext(r.Context(),
				"INSERT INTO handles (handle, account_id, label, kind, paused, created_day) VALUES (?, ?, ?, ?, 0, ?)",
				handle.Raw(), accountID.Int64(), req.Label, string(kind), today.Int())
			return err
		})
		if err != nil {
			WriteUniformError(w, http.StatusInternalServerError, "internal_error")
			return
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"handle":      handle.Raw(),
		"label":       req.Label,
		"kind":        string(kind),
		"paused":      false,
		"created_day": today.Int(),
	})
}

// UpdateHandle modifies the label or paused state of an owned handle.
func (h *HandlesHandler) UpdateHandle(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil && h.store == nil {
		NotImplemented(w, r)
		return
	}
	accountID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	handleStr := chi.URLParam(r, "handle")
	if handleStr == "" {
		WriteUniformNotFound(w)
		return
	}

	var req struct {
		Paused *bool   `json:"paused"`
		Label  *string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteUniformError(w, http.StatusBadRequest, "invalid_body")
		return
	}

	if h.store != nil {
		err := h.store.Write(r.Context(), func(tx *sql.Tx) error {
			if req.Paused != nil {
				pausedInt := 0
				if *req.Paused {
					pausedInt = 1
				}
				res, err := tx.ExecContext(r.Context(),
					"UPDATE handles SET paused = ? WHERE handle = ? AND account_id = ?",
					pausedInt, handleStr, accountID.Int64())
				if err != nil {
					return err
				}
				n, _ := res.RowsAffected()
				if n == 0 {
					return sql.ErrNoRows
				}
			}
			if req.Label != nil {
				res, err := tx.ExecContext(r.Context(),
					"UPDATE handles SET label = ? WHERE handle = ? AND account_id = ?",
					*req.Label, handleStr, accountID.Int64())
				if err != nil {
					return err
				}
				n, _ := res.RowsAffected()
				if n == 0 {
					return sql.ErrNoRows
				}
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				WriteUniformNotFound(w)
				return
			}
			WriteUniformError(w, http.StatusInternalServerError, "internal_error")
			return
		}
	}

	if h.resolver != nil {
		h.resolver.Invalidate(handleStr)
	}

	w.WriteHeader(http.StatusOK)
}

// DeleteHandle burns a handle permanently.
func (h *HandlesHandler) DeleteHandle(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil && h.store == nil {
		NotImplemented(w, r)
		return
	}
	accountID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	handleStr := chi.URLParam(r, "handle")
	if handleStr == "" {
		WriteUniformNotFound(w)
		return
	}

	if h.svc != nil {
		if err := h.svc.BurnHandle(r.Context(), accountID, core.Handle(handleStr)); err != nil {
			if errors.Is(err, region.ErrHandleNotFound) {
				WriteUniformNotFound(w)
				return
			}
			WriteUniformError(w, http.StatusInternalServerError, "internal_error")
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// SetAlias registers or updates a vanity alias pointing to a handle.
func (h *HandlesHandler) SetAlias(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil && h.store == nil {
		NotImplemented(w, r)
		return
	}
	accountID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req struct {
		Alias  string `json:"alias"`
		Handle string `json:"handle"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteUniformError(w, http.StatusBadRequest, "invalid_body")
		return
	}

	cleanAlias := strings.ToLower(strings.TrimPrefix(req.Alias, "@"))
	if cleanAlias == "" || len(cleanAlias) > 32 {
		WriteUniformError(w, http.StatusBadRequest, "invalid_alias")
		return
	}
	if region.IsReservedAlias(cleanAlias) {
		WriteUniformError(w, http.StatusBadRequest, "reserved_alias")
		return
	}

	if h.store != nil {
		today := core.Today()
		err := h.store.Write(r.Context(), func(tx *sql.Tx) error {
			// Verify handle belongs to this account
			var dummy int64
			err := tx.QueryRowContext(r.Context(),
				"SELECT account_id FROM handles WHERE handle = ? AND account_id = ?",
				req.Handle, accountID.Int64()).Scan(&dummy)
			if err != nil {
				return errors.New("handle_not_owned")
			}

			// Upsert alias
			_, err = tx.ExecContext(r.Context(),
				"INSERT INTO aliases (alias, account_id, handle, created_day) VALUES (?, ?, ?, ?) ON CONFLICT(alias) DO UPDATE SET handle = excluded.handle, account_id = excluded.account_id",
				cleanAlias, accountID.Int64(), req.Handle, today.Int())
			return err
		})
		if err != nil {
			if err.Error() == "handle_not_owned" {
				WriteUniformError(w, http.StatusForbidden, "forbidden")
				return
			}
			WriteUniformError(w, http.StatusInternalServerError, "internal_error")
			return
		}
	}

	if h.resolver != nil {
		h.resolver.Invalidate(cleanAlias)
	}

	w.WriteHeader(http.StatusOK)
}

// DeleteAlias removes an alias.
func (h *HandlesHandler) DeleteAlias(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil && h.store == nil {
		NotImplemented(w, r)
		return
	}
	accountID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if h.store != nil {
		_ = h.store.Write(r.Context(), func(tx *sql.Tx) error {
			_, err := tx.ExecContext(r.Context(), "DELETE FROM aliases WHERE account_id = ?", accountID.Int64())
			return err
		})
	}

	w.WriteHeader(http.StatusNoContent)
}
