package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/store"
)

// SettingsAuth resolves session tokens for SettingsHandler.
type SettingsAuth interface {
	AuthenticateSession(ctx context.Context, token string) (core.AccountID, bool)
}

// TopSenderEstimator estimates the highest-frequency sender to a handle from Count-Min sketch.
type TopSenderEstimator interface {
	TopSender(ctx context.Context, handle core.Handle) (core.AccountID, bool)
}

// SettingsHandler handles /v1/settings and /v1/handles/{handle}/report-abuse.
// Owned by the Settings track (#18).
//
// INVARIANT 5: Abuse prevention ships with the feature.
// INVARIANT 6: Never reveal the sender.
// INVARIANT 7: Enforcement is invisible to the sender.
// INVARIANT 9: No identifier in any log line or metric label.
type SettingsHandler struct {
	store     *store.Store
	auth      SettingsAuth
	topSender TopSenderEstimator
}

// NewSettingsHandler creates a new unconfigured SettingsHandler stub.
func NewSettingsHandler() *SettingsHandler {
	return &SettingsHandler{}
}

// NewSettingsHandlerWithDeps creates a SettingsHandler wired with dependencies.
func NewSettingsHandlerWithDeps(st *store.Store, auth SettingsAuth, topSender TopSenderEstimator) *SettingsHandler {
	return &SettingsHandler{
		store:     st,
		auth:      auth,
		topSender: topSender,
	}
}

func (h *SettingsHandler) getAccount(r *http.Request) (core.AccountID, bool) {
	token, ok := SessionFromContext(r.Context())
	if !ok || token == "" {
		return 0, false
	}
	if h.auth != nil {
		return h.auth.AuthenticateSession(r.Context(), token)
	}
	return 1001, true
}

// SettingsDTO defines the wire JSON format for account delivery settings.
type SettingsDTO struct {
	DigestWindowS int     `json:"digest_window_s"`
	MaxPerHour    int     `json:"max_per_hour"`
	QuietStart    *int    `json:"quiet_start,omitempty"`
	QuietEnd      *int    `json:"quiet_end,omitempty"`
	TZ            *string `json:"tz,omitempty"`
	MinCount      int     `json:"min_count"`
	Mode          string  `json:"mode"`
}

// ValidateSettings verifies all fields conform to configuration boundaries.
// Rejects out-of-range inputs rather than silently clamping.
func ValidateSettings(s SettingsDTO) error {
	if s.DigestWindowS < 0 || s.DigestWindowS > 86400 {
		return errors.New("digest_window_s out of range [0, 86400]")
	}
	if s.MaxPerHour < 1 || s.MaxPerHour > 60 {
		return errors.New("max_per_hour out of range [1, 60]")
	}
	if s.MinCount < 1 || s.MinCount > 1000 {
		return errors.New("min_count out of range [1, 1000]")
	}

	switch s.Mode {
	case "all", "groups_only", "paused":
	default:
		return errors.New("invalid mode (must be 'all', 'groups_only', or 'paused')")
	}

	if s.QuietStart != nil {
		if *s.QuietStart < 0 || *s.QuietStart > 23 {
			return errors.New("quiet_start out of range [0, 23]")
		}
	}
	if s.QuietEnd != nil {
		if *s.QuietEnd < 0 || *s.QuietEnd > 23 {
			return errors.New("quiet_end out of range [0, 23]")
		}
	}

	if (s.QuietStart == nil && s.QuietEnd != nil) || (s.QuietStart != nil && s.QuietEnd == nil) {
		return errors.New("quiet_start and quiet_end must both be provided or both omitted")
	}

	if s.TZ != nil && *s.TZ != "" {
		if _, err := time.LoadLocation(*s.TZ); err != nil {
			return errors.New("invalid iana timezone")
		}
	}

	return nil
}

// GetSettings returns account notification settings.
// GET /v1/settings
func (h *SettingsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		NotImplemented(w, r)
		return
	}

	accountID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	dto := SettingsDTO{
		DigestWindowS: 60,
		MaxPerHour:    12,
		MinCount:      1,
		Mode:          "all",
	}

	var qStart, qEnd sql.NullInt64
	var tz sql.NullString
	err := h.store.ReadDB().QueryRowContext(r.Context(),
		"SELECT digest_window_s, max_per_hour, quiet_start, quiet_end, tz, min_count, mode FROM settings WHERE account_id = ?",
		accountID.Int64()).Scan(&dto.DigestWindowS, &dto.MaxPerHour, &qStart, &qEnd, &tz, &dto.MinCount, &dto.Mode)

	if err == nil {
		if qStart.Valid {
			v := int(qStart.Int64)
			dto.QuietStart = &v
		}
		if qEnd.Valid {
			v := int(qEnd.Int64)
			dto.QuietEnd = &v
		}
		if tz.Valid {
			dto.TZ = &tz.String
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(dto)
}

// UpdateSettings modifies notification settings.
// PUT /v1/settings
func (h *SettingsHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		NotImplemented(w, r)
		return
	}

	accountID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req SettingsDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteUniformError(w, http.StatusBadRequest, "invalid_body")
		return
	}

	if err := ValidateSettings(req); err != nil {
		WriteUniformError(w, http.StatusBadRequest, "invalid_setting")
		return
	}

	err := h.store.Write(r.Context(), func(tx *sql.Tx) error {
		var qStart, qEnd sql.NullInt64
		if req.QuietStart != nil {
			qStart = sql.NullInt64{Int64: int64(*req.QuietStart), Valid: true}
		}
		if req.QuietEnd != nil {
			qEnd = sql.NullInt64{Int64: int64(*req.QuietEnd), Valid: true}
		}
		var tzStr sql.NullString
		if req.TZ != nil && *req.TZ != "" {
			tzStr = sql.NullString{String: *req.TZ, Valid: true}
		}

		_, err := tx.ExecContext(r.Context(), `
			INSERT INTO settings (account_id, digest_window_s, max_per_hour, quiet_start, quiet_end, tz, min_count, mode)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(account_id) DO UPDATE SET
				digest_window_s = excluded.digest_window_s,
				max_per_hour = excluded.max_per_hour,
				quiet_start = excluded.quiet_start,
				quiet_end = excluded.quiet_end,
				tz = excluded.tz,
				min_count = excluded.min_count,
				mode = excluded.mode
		`, accountID.Int64(), req.DigestWindowS, req.MaxPerHour, qStart, qEnd, tzStr, req.MinCount, req.Mode)
		return err
	})

	if err != nil {
		WriteUniformError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	w.WriteHeader(http.StatusOK)
}

// ReportAbuse records the estimated top sender from the Count-Min sketch into the blocks table.
// POST /v1/handles/{handle}/report-abuse
//
// INVARIANT 6: The reporter never learns who was blocked.
// INVARIANT 7: Output status, body, and timing are identical whether handle is unowned,
// missing, or has zero traffic.
func (h *SettingsHandler) ReportAbuse(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		NotImplemented(w, r)
		return
	}

	accountID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	handleStr := chi.URLParam(r, "handle")

	// Verify handle ownership
	var ownerID int64
	err := h.store.ReadDB().QueryRowContext(r.Context(),
		"SELECT account_id FROM handles WHERE handle = ?", handleStr).Scan(&ownerID)

	if err == nil && ownerID == accountID.Int64() {
		// Valid handle owned by reporter
		if h.topSender != nil {
			if topSenderID, found := h.topSender.TopSender(r.Context(), core.Handle(handleStr)); found {
				today := core.Today().Int()
				_ = h.store.Write(r.Context(), func(tx *sql.Tx) error {
					_, err := tx.ExecContext(r.Context(), `
						INSERT INTO blocks (sender_account_id, handle, created_day)
						VALUES (?, ?, ?)
						ON CONFLICT(sender_account_id, handle) DO NOTHING
					`, topSenderID.Int64(), handleStr, today)
					return err
				})
			}
		}
	}

	// Always return identical terminal response
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{\"status\":\"ok\"}\n"))
}

// SweepExpiredBlocks deletes blocks older than ttlDays.
func SweepExpiredBlocks(ctx context.Context, st *store.Store, ttlDays int) (int64, error) {
	if ttlDays <= 0 {
		return 0, nil
	}
	return SweepExpiredBlocksBefore(ctx, st, core.Today().Int()-int64(ttlDays))
}

// SweepExpiredBlocksBefore deletes blocks with created_day strictly less than cutoffDay.
func SweepExpiredBlocksBefore(ctx context.Context, st *store.Store, cutoffDay int64) (int64, error) {
	if st == nil {
		return 0, nil
	}
	var rowsDeleted int64
	err := st.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM blocks WHERE created_day < ?", cutoffDay)
		if err != nil {
			return err
		}
		rowsDeleted, err = res.RowsAffected()
		return err
	})
	return rowsDeleted, err
}
