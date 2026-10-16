package http

import (
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/halpworld/halpcheers/server/internal/config"
	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/store"
)

const (
	// ContactsPadBlockBytes is the mandatory 4 KiB padding boundary before AES-256-GCM sealing
	// (docs/DISCOVERY.md § The blob & docs/OPEN-QUESTIONS.md #14).
	ContactsPadBlockBytes = 4096
	// aesGCMOverheadBytes is 12-byte nonce + 16-byte authentication tag.
	aesGCMOverheadBytes = 28
	// defaultContactsMaxBytes is the default 64 KiB ceiling on contacts blobs.
	defaultContactsMaxBytes = 64 * 1024
)

var (
	errContactsVersionConflict = errors.New("contacts_version_conflict")
)

// ContactsHandler handles Phase 2 /v1/contacts routes (docs/API.md § Contacts & docs/DISCOVERY.md).
//
// INVARIANTS:
// - The server stores an opaque AES-256-GCM ciphertext sealed under contacts_key, which never leaves the device.
// - Plaintext is padded to a 4 KiB boundary before sealing so size reveals only a size class, never a contact count.
// - Fixed ceiling contacts.max_bytes (default 64 KiB) enforced per Invariant 8.
// - Optimistic concurrency enforced via If-Match: <version>.
type ContactsHandler struct {
	store *store.Store
	auth  SessionAuthenticator
	cfg   *config.Config
}

// NewContactsHandler creates an unconfigured ContactsHandler stub (returns 501).
func NewContactsHandler() *ContactsHandler {
	return &ContactsHandler{}
}

// NewContactsHandlerWithDeps creates a ContactsHandler wired with storage, auth, and config.
func NewContactsHandlerWithDeps(st *store.Store, auth SessionAuthenticator, cfg *config.Config) *ContactsHandler {
	return &ContactsHandler{
		store: st,
		auth:  auth,
		cfg:   cfg,
	}
}

func (h *ContactsHandler) getAccount(r *http.Request) (core.AccountID, bool) {
	token, ok := SessionFromContext(r.Context())
	if !ok || token == "" {
		return 0, false
	}
	if h.auth != nil {
		return h.auth.AuthenticateSession(r.Context(), token)
	}
	return 1001, true
}

func (h *ContactsHandler) maxBytes() int {
	if h.cfg != nil && h.cfg.Reloadable() != nil && h.cfg.Reloadable().ContactsMaxBytes > 0 {
		return h.cfg.Reloadable().ContactsMaxBytes
	}
	return defaultContactsMaxBytes
}

// decodeContactsBlob decodes a base64 or hex encoded ciphertext blob and validates
// the 4 KiB padding boundary and size ceiling (docs/DISCOVERY.md § The blob).
func decodeContactsBlob(encoded string, maxBytes int) ([]byte, error) {
	trimmed := strings.TrimSpace(encoded)
	if trimmed == "" {
		return nil, errors.New("empty_blob")
	}

	var raw []byte
	var err error

	// Try standard base64, raw base64, URL-safe base64, then hex fallback
	raw, err = base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(trimmed)
	}
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(trimmed)
	}
	if err != nil {
		raw, err = hex.DecodeString(trimmed)
	}
	if err != nil || len(raw) == 0 {
		return nil, errors.New("invalid_blob_encoding")
	}

	// Validate 4 KiB boundary padding:
	// Either the plaintext before AES-GCM sealing (len - 28 bytes for 12B IV + 16B tag) is a positive
	// multiple of 4096 up to maxBytes, OR the total blob length itself is a multiple of 4096 up to maxBytes.
	plaintextLen := len(raw) - aesGCMOverheadBytes
	validPlaintextPad := plaintextLen >= ContactsPadBlockBytes &&
		plaintextLen <= maxBytes &&
		plaintextLen%ContactsPadBlockBytes == 0
	validTotalPad := len(raw) >= ContactsPadBlockBytes &&
		len(raw) <= maxBytes &&
		len(raw)%ContactsPadBlockBytes == 0

	if !validPlaintextPad && !validTotalPad {
		if len(raw) > maxBytes+aesGCMOverheadBytes {
			return nil, errors.New("blob_too_large")
		}
		return nil, errors.New("invalid_blob_padding")
	}

	return raw, nil
}

// GetContacts retrieves the account's encrypted contacts blob and version.
// GET /v1/contacts -> { version, blob, updated_day }
func (h *ContactsHandler) GetContacts(w http.ResponseWriter, r *http.Request) {
	if h.store == nil && h.auth == nil {
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

	var blob []byte
	var version int64
	var updatedDay int64

	err := h.store.ReadDB().QueryRowContext(r.Context(),
		"SELECT blob, version, updated_day FROM contacts_blob WHERE account_id = ?",
		accountID.Int64(),
	).Scan(&blob, &version, &updatedDay)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("ETag", `"0"`)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": 0,
				"blob":    "",
			})
			return
		}
		WriteUniformError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, version))
	_ = json.NewEncoder(w).Encode(map[string]any{
		"version":     version,
		"blob":        base64.StdEncoding.EncodeToString(blob),
		"updated_day": updatedDay,
	})
}

// UpdateContacts stores a new encrypted contacts blob using optimistic concurrency (If-Match: <version>).
// PUT /v1/contacts
func (h *ContactsHandler) UpdateContacts(w http.ResponseWriter, r *http.Request) {
	if h.store == nil && h.auth == nil {
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

	ifMatchRaw := strings.TrimSpace(r.Header.Get("If-Match"))
	if ifMatchRaw == "" {
		WriteUniformError(w, http.StatusBadRequest, "missing_if_match")
		return
	}
	ifMatchClean := strings.Trim(ifMatchRaw, `"`)
	expectedVersion, err := strconv.ParseInt(ifMatchClean, 10, 64)
	if err != nil || expectedVersion < 0 {
		WriteUniformError(w, http.StatusBadRequest, "invalid_if_match")
		return
	}

	var req struct {
		Blob string `json:"blob"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteUniformError(w, http.StatusBadRequest, "invalid_body")
		return
	}

	rawBlob, err := decodeContactsBlob(req.Blob, h.maxBytes())
	if err != nil {
		WriteUniformError(w, http.StatusBadRequest, err.Error())
		return
	}

	today := core.Today().Int()
	var newVersion int64

	err = h.store.Write(r.Context(), func(tx *sql.Tx) error {
		var currentVersion int64
		qErr := tx.QueryRowContext(r.Context(),
			"SELECT version FROM contacts_blob WHERE account_id = ?",
			accountID.Int64(),
		).Scan(&currentVersion)

		if qErr != nil {
			if errors.Is(qErr, sql.ErrNoRows) {
				currentVersion = 0
			} else {
				return qErr
			}
		}

		if currentVersion != expectedVersion {
			return errContactsVersionConflict
		}

		newVersion = currentVersion + 1
		_, execErr := tx.ExecContext(r.Context(), `
			INSERT INTO contacts_blob (account_id, blob, version, updated_day)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(account_id) DO UPDATE SET
				blob = excluded.blob,
				version = excluded.version,
				updated_day = excluded.updated_day
		`, accountID.Int64(), rawBlob, newVersion, today)
		return execErr
	})

	if err != nil {
		if errors.Is(err, errContactsVersionConflict) {
			WriteUniformError(w, http.StatusPreconditionFailed, "version_conflict")
			return
		}
		WriteUniformError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, newVersion))
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"version":     newVersion,
		"updated_day": today,
	})
}

// DeleteContacts removes the account's encrypted contacts blob.
// DELETE /v1/contacts
func (h *ContactsHandler) DeleteContacts(w http.ResponseWriter, r *http.Request) {
	if h.store == nil && h.auth == nil {
		NotImplemented(w, r)
		return
	}

	accountID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if h.store != nil {
		if err := h.store.Write(r.Context(), func(tx *sql.Tx) error {
			_, err := tx.ExecContext(r.Context(), "DELETE FROM contacts_blob WHERE account_id = ?", accountID.Int64())
			return err
		}); err != nil {
			WriteUniformError(w, http.StatusInternalServerError, "internal_error")
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}
