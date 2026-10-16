package http

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/halpworld/halpcheers/server/internal/config"
	"github.com/halpworld/halpcheers/server/internal/core"
	"github.com/halpworld/halpcheers/server/internal/region"
	"github.com/halpworld/halpcheers/server/internal/store"
)

var (
	errGroupNotFound      = errors.New("group_not_found")
	errGroupForbidden     = errors.New("group_forbidden")
	errGroupFull          = errors.New("group_full")
	errMaxGroupsReached   = errors.New("max_groups_reached")
	errAlreadyGroupMember = errors.New("already_member")
	errDisplayNameTaken   = errors.New("display_name_taken")
)

// GroupsHandler handles Phase 2 /v1/groups* routes (docs/GROUPS.md & docs/API.md § Groups).
//
// INVARIANTS:
// - Joining a group mints a group-scoped handle (kind = "group") visible only inside that group.
// - Leaving or being removed burns the group-scoped handle permanently.
// - Roster responses never expose account IDs, personal handles, aliases, or received counts.
// - Minimum group size floor (groups.min_size, default 5) disables group-scoped pings below threshold.
type GroupsHandler struct {
	regionSvc *region.Service
	resolver  *region.Resolver
	store     *store.Store
	auth      SessionAuthenticator
	pow       PoWValidator
	cfg       *config.Config
}

// NewGroupsHandler creates an unconfigured GroupsHandler stub (returns 501).
func NewGroupsHandler() *GroupsHandler {
	return &GroupsHandler{}
}

// NewGroupsHandlerWithDeps creates a GroupsHandler wired with services and config.
func NewGroupsHandlerWithDeps(
	regionSvc *region.Service,
	resolver *region.Resolver,
	st *store.Store,
	auth SessionAuthenticator,
	pow PoWValidator,
	cfg *config.Config,
) *GroupsHandler {
	return &GroupsHandler{
		regionSvc: regionSvc,
		resolver:  resolver,
		store:     st,
		auth:      auth,
		pow:       pow,
		cfg:       cfg,
	}
}

func (h *GroupsHandler) getAccount(r *http.Request) (core.AccountID, bool) {
	token, ok := SessionFromContext(r.Context())
	if !ok || token == "" {
		return 0, false
	}
	if h.auth != nil {
		return h.auth.AuthenticateSession(r.Context(), token)
	}
	return 1001, true
}

func (h *GroupsHandler) minGroupSize() int {
	if h.cfg != nil && h.cfg.Reloadable() != nil && h.cfg.Reloadable().GroupsMinSize >= 2 {
		return h.cfg.Reloadable().GroupsMinSize
	}
	return 5
}

func (h *GroupsHandler) maxGroupMembers() int {
	if h.cfg != nil && h.cfg.Reloadable() != nil && h.cfg.Reloadable().GroupsMaxMembers > 0 {
		return h.cfg.Reloadable().GroupsMaxMembers
	}
	return 500
}

func (h *GroupsHandler) maxGroupsPerAccount() int {
	if h.cfg != nil && h.cfg.Reloadable() != nil && h.cfg.Reloadable().GroupsMaxPerAccount > 0 {
		return h.cfg.Reloadable().GroupsMaxPerAccount
	}
	return 20
}

func generateInviteCode() (string, []byte, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", nil, err
	}
	code := hex.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(code))
	return code, sum[:], nil
}

func hashInviteCode(code string) []byte {
	sum := sha256.Sum256([]byte(strings.TrimSpace(code)))
	return sum[:]
}

func (h *GroupsHandler) mintHandle(r *http.Request) (core.Handle, error) {
	if h.regionSvc != nil {
		return h.regionSvc.MintHandle(r.Context())
	}
	return region.GenerateHandle()
}

// CreateGroup creates a new group, generates an invite code, and mints the owner's group handle.
// POST /v1/groups
func (h *GroupsHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	if h.store == nil && h.regionSvc == nil {
		NotImplemented(w, r)
		return
	}

	accountID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Note        string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteUniformError(w, http.StatusBadRequest, "invalid_body")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 64 {
		WriteUniformError(w, http.StatusBadRequest, "invalid_group_name")
		return
	}

	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = "owner"
	}
	if len(displayName) > 32 || region.IsReservedAlias(displayName) {
		WriteUniformError(w, http.StatusBadRequest, "invalid_display_name")
		return
	}

	inviteCode, inviteHash, err := generateInviteCode()
	if err != nil {
		WriteUniformError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	ownerHandle, err := h.mintHandle(r)
	if err != nil {
		WriteUniformError(w, http.StatusInternalServerError, "mint_failed")
		return
	}

	today := core.Today().Int()
	maxPerAcc := h.maxGroupsPerAccount()
	minSize := h.minGroupSize()
	var groupID int64

	if h.store != nil {
		err = h.store.Write(r.Context(), func(tx *sql.Tx) error {
			var curGroups int
			if qErr := tx.QueryRowContext(r.Context(),
				"SELECT COUNT(1) FROM group_members WHERE account_id = ?",
				accountID.Int64(),
			).Scan(&curGroups); qErr != nil {
				return qErr
			}
			if curGroups >= maxPerAcc {
				return errMaxGroupsReached
			}

			res, execErr := tx.ExecContext(r.Context(), `
				INSERT INTO groups (name, invite_code_hash, owner_account_id, created_day)
				VALUES (?, ?, ?, ?)
			`, name, inviteHash, accountID.Int64(), today)
			if execErr != nil {
				return execErr
			}
			groupID, execErr = res.LastInsertId()
			if execErr != nil {
				return execErr
			}

			if _, execErr = tx.ExecContext(r.Context(), `
				INSERT INTO handles (handle, account_id, label, kind, group_id, paused, created_day)
				VALUES (?, ?, ?, 'group', ?, 0, ?)
			`, ownerHandle.Raw(), accountID.Int64(), name, groupID, today); execErr != nil {
				return execErr
			}

			var noteVal any
			if strings.TrimSpace(req.Note) != "" {
				noteVal = strings.TrimSpace(req.Note)
			}
			if _, execErr = tx.ExecContext(r.Context(), `
				INSERT INTO group_members (group_id, account_id, display_name, note, handle, role, joined_day)
				VALUES (?, ?, ?, ?, ?, 'owner', ?)
			`, groupID, accountID.Int64(), displayName, noteVal, ownerHandle.Raw(), today); execErr != nil {
				return execErr
			}

			return nil
		})
		if err != nil {
			if errors.Is(err, errMaxGroupsReached) {
				WriteUniformError(w, http.StatusBadRequest, "max_groups_reached")
				return
			}
			WriteUniformError(w, http.StatusInternalServerError, "internal_error")
			return
		}
	}

	if h.resolver != nil {
		h.resolver.SetMinGroupSize(minSize)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"group_id":    groupID,
		"name":        name,
		"invite_code": inviteCode,
		"my_handle":   ownerHandle.Raw(),
		"min_size":    minSize,
		"active":      1 >= minSize,
	})
}

// ListGroups lists all groups the authenticated account belongs to.
// GET /v1/groups
func (h *GroupsHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	if h.store == nil && h.regionSvc == nil {
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

	minSize := h.minGroupSize()
	rows, err := h.store.ReadDB().QueryContext(r.Context(), `
		SELECT g.id, g.name, g.created_day,
		       gm.display_name, gm.handle, gm.role,
		       (SELECT COUNT(1) FROM group_members m2 WHERE m2.group_id = g.id) AS member_count
		FROM group_members gm
		JOIN groups g ON gm.group_id = g.id
		WHERE gm.account_id = ?
		ORDER BY g.created_day DESC, g.id DESC
	`, accountID.Int64())
	if err != nil {
		WriteUniformError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	defer rows.Close()

	type groupItemDTO struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Role        string `json:"role"`
		DisplayName string `json:"display_name"`
		MyHandle    string `json:"my_handle"`
		MemberCount int    `json:"member_count"`
		MinSize     int    `json:"min_size"`
		Active      bool   `json:"active"`
		CreatedDay  int64  `json:"created_day"`
	}

	groups := make([]groupItemDTO, 0)
	for rows.Next() {
		var item groupItemDTO
		if err := rows.Scan(
			&item.ID,
			&item.Name,
			&item.CreatedDay,
			&item.DisplayName,
			&item.MyHandle,
			&item.Role,
			&item.MemberCount,
		); err != nil {
			WriteUniformError(w, http.StatusInternalServerError, "internal_error")
			return
		}
		item.MinSize = minSize
		item.Active = item.MemberCount >= minSize
		groups = append(groups, item)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"groups": groups})
}

// ListMembers returns the group roster for a member of the group.
// GET /v1/groups/{id}/members
//
// INVARIANT (docs/GROUPS.md § Roster):
// Members see ONLY display_name, optional note, role, and group-scoped handle.
// Never account_id, personal handles, aliases, or received counts.
func (h *GroupsHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	if h.store == nil && h.regionSvc == nil {
		NotImplemented(w, r)
		return
	}

	accountID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	groupID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || groupID <= 0 || h.store == nil {
		WriteUniformNotFound(w)
		return
	}

	// Verify caller is a member of this group
	var callerRole string
	err = h.store.ReadDB().QueryRowContext(r.Context(),
		"SELECT role FROM group_members WHERE group_id = ? AND account_id = ?",
		groupID, accountID.Int64(),
	).Scan(&callerRole)
	if err != nil {
		WriteUniformNotFound(w)
		return
	}

	rows, err := h.store.ReadDB().QueryContext(r.Context(), `
		SELECT display_name, note, handle, role
		FROM group_members
		WHERE group_id = ?
		ORDER BY joined_day ASC, display_name ASC
	`, groupID)
	if err != nil {
		WriteUniformError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	defer rows.Close()

	type memberDTO struct {
		DisplayName string `json:"display_name"`
		Note        string `json:"note,omitempty"`
		Handle      string `json:"handle"`
		Role        string `json:"role"`
	}

	members := make([]memberDTO, 0)
	for rows.Next() {
		var m memberDTO
		var note sql.NullString
		if err := rows.Scan(&m.DisplayName, &note, &m.Handle, &m.Role); err != nil {
			WriteUniformError(w, http.StatusInternalServerError, "internal_error")
			return
		}
		if note.Valid {
			m.Note = note.String
		}
		members = append(members, m)
	}

	minSize := h.minGroupSize()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"members":      members,
		"member_count": len(members),
		"min_size":     minSize,
		"active":       len(members) >= minSize,
	})
}

// JoinGroup joins a group using an invite code and mints a group-scoped handle.
// POST /v1/groups/join
func (h *GroupsHandler) JoinGroup(w http.ResponseWriter, r *http.Request) {
	if h.store == nil && h.regionSvc == nil {
		NotImplemented(w, r)
		return
	}

	accountID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Verify PoW if configured and header present (docs/GROUPS.md § Joining)
	if h.pow != nil {
		if powHeader, hasPoW := PoWFromContext(r.Context()); hasPoW && powHeader != "" {
			if err := h.pow.Verify("group-join", powHeader, "group-join", accountID); err != nil {
				WriteUniformError(w, http.StatusBadRequest, "invalid_pow")
				return
			}
		}
	}

	var req struct {
		InviteCode  string `json:"invite_code"`
		DisplayName string `json:"display_name"`
		Note        string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteUniformError(w, http.StatusBadRequest, "invalid_body")
		return
	}

	inviteCode := strings.TrimSpace(req.InviteCode)
	displayName := strings.TrimSpace(req.DisplayName)
	if inviteCode == "" || displayName == "" || len(displayName) > 32 {
		WriteUniformError(w, http.StatusBadRequest, "invalid_join_params")
		return
	}
	if region.IsReservedAlias(displayName) {
		WriteUniformError(w, http.StatusBadRequest, "reserved_display_name")
		return
	}

	myHandle, err := h.mintHandle(r)
	if err != nil {
		WriteUniformError(w, http.StatusInternalServerError, "mint_failed")
		return
	}

	inviteHash := hashInviteCode(inviteCode)
	today := core.Today().Int()
	maxPerAcc := h.maxGroupsPerAccount()
	maxMembers := h.maxGroupMembers()
	minSize := h.minGroupSize()
	var groupID int64

	err = h.store.Write(r.Context(), func(tx *sql.Tx) error {
		var groupName string
		qErr := tx.QueryRowContext(r.Context(),
			"SELECT id, name FROM groups WHERE invite_code_hash = ?",
			inviteHash,
		).Scan(&groupID, &groupName)
		if qErr != nil {
			if errors.Is(qErr, sql.ErrNoRows) {
				return errGroupNotFound
			}
			return qErr
		}

		// Check if already a member
		var existing int
		_ = tx.QueryRowContext(r.Context(),
			"SELECT COUNT(1) FROM group_members WHERE group_id = ? AND account_id = ?",
			groupID, accountID.Int64(),
		).Scan(&existing)
		if existing > 0 {
			return errAlreadyGroupMember
		}

		// Enforce groups.max_per_account
		var accGroups int
		if qErr = tx.QueryRowContext(r.Context(),
			"SELECT COUNT(1) FROM group_members WHERE account_id = ?",
			accountID.Int64(),
		).Scan(&accGroups); qErr != nil {
			return qErr
		}
		if accGroups >= maxPerAcc {
			return errMaxGroupsReached
		}

		// Enforce groups.max_members
		var memberCount int
		if qErr = tx.QueryRowContext(r.Context(),
			"SELECT COUNT(1) FROM group_members WHERE group_id = ?",
			groupID,
		).Scan(&memberCount); qErr != nil {
			return qErr
		}
		if memberCount >= maxMembers {
			return errGroupFull
		}

		// Enforce per-group display_name uniqueness
		var nameCount int
		if qErr = tx.QueryRowContext(r.Context(),
			"SELECT COUNT(1) FROM group_members WHERE group_id = ? AND LOWER(display_name) = LOWER(?)",
			groupID, displayName,
		).Scan(&nameCount); qErr != nil {
			return qErr
		}
		if nameCount > 0 {
			return errDisplayNameTaken
		}

		if _, execErr := tx.ExecContext(r.Context(), `
			INSERT INTO handles (handle, account_id, label, kind, group_id, paused, created_day)
			VALUES (?, ?, ?, 'group', ?, 0, ?)
		`, myHandle.Raw(), accountID.Int64(), groupName, groupID, today); execErr != nil {
			return execErr
		}

		var noteVal any
		if strings.TrimSpace(req.Note) != "" {
			noteVal = strings.TrimSpace(req.Note)
		}
		if _, execErr := tx.ExecContext(r.Context(), `
			INSERT INTO group_members (group_id, account_id, display_name, note, handle, role, joined_day)
			VALUES (?, ?, ?, ?, ?, 'member', ?)
		`, groupID, accountID.Int64(), displayName, noteVal, myHandle.Raw(), today); execErr != nil {
			return execErr
		}

		return nil
	})

	if err != nil {
		switch {
		case errors.Is(err, errGroupNotFound):
			WriteUniformNotFound(w)
		case errors.Is(err, errAlreadyGroupMember), errors.Is(err, errDisplayNameTaken):
			WriteUniformError(w, http.StatusConflict, err.Error())
		case errors.Is(err, errMaxGroupsReached), errors.Is(err, errGroupFull):
			WriteUniformError(w, http.StatusBadRequest, err.Error())
		default:
			WriteUniformError(w, http.StatusInternalServerError, "internal_error")
		}
		return
	}

	if h.resolver != nil {
		h.resolver.SetMinGroupSize(minSize)
		h.resolver.InvalidateGroup(groupID)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"group_id":  groupID,
		"my_handle": myHandle.Raw(),
	})
}

// LeaveGroup removes the authenticated user from the group and permanently burns their group-scoped handle.
// DELETE /v1/groups/{id}/membership
func (h *GroupsHandler) LeaveGroup(w http.ResponseWriter, r *http.Request) {
	if h.store == nil && h.regionSvc == nil {
		NotImplemented(w, r)
		return
	}

	accountID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	groupID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || groupID <= 0 || h.store == nil {
		WriteUniformNotFound(w)
		return
	}

	var burnedHandle string

	err = h.store.Write(r.Context(), func(tx *sql.Tx) error {
		var role string
		qErr := tx.QueryRowContext(r.Context(),
			"SELECT handle, role FROM group_members WHERE group_id = ? AND account_id = ?",
			groupID, accountID.Int64(),
		).Scan(&burnedHandle, &role)
		if qErr != nil {
			if errors.Is(qErr, sql.ErrNoRows) {
				return errGroupNotFound
			}
			return qErr
		}

		// If owner leaves, promote the longest-tenured admin (or member), or delete the group if empty
		if role == "owner" {
			var nextOwnerID int64
			var nextOwnerHandle string
			pErr := tx.QueryRowContext(r.Context(), `
				SELECT account_id, handle
				FROM group_members
				WHERE group_id = ? AND account_id != ?
				ORDER BY CASE WHEN role = 'admin' THEN 0 ELSE 1 END, joined_day ASC, rowid ASC
				LIMIT 1
			`, groupID, accountID.Int64()).Scan(&nextOwnerID, &nextOwnerHandle)

			if pErr == nil {
				if _, execErr := tx.ExecContext(r.Context(),
					"UPDATE group_members SET role = 'owner' WHERE group_id = ? AND handle = ?",
					groupID, nextOwnerHandle,
				); execErr != nil {
					return execErr
				}
				if _, execErr := tx.ExecContext(r.Context(),
					"UPDATE groups SET owner_account_id = ? WHERE id = ?",
					nextOwnerID, groupID,
				); execErr != nil {
					return execErr
				}
			} else if errors.Is(pErr, sql.ErrNoRows) {
				// Last member leaving: delete group
				if _, execErr := tx.ExecContext(r.Context(),
					"DELETE FROM handles WHERE handle = ?",
					burnedHandle,
				); execErr != nil {
					return execErr
				}
				_, execErr := tx.ExecContext(r.Context(), "DELETE FROM groups WHERE id = ?", groupID)
				return execErr
			} else {
				return pErr
			}
		}

		// Burn the departing member's group-scoped handle (cascades to group_members)
		_, execErr := tx.ExecContext(r.Context(),
			"DELETE FROM handles WHERE handle = ?",
			burnedHandle,
		)
		return execErr
	})

	if err != nil {
		if errors.Is(err, errGroupNotFound) {
			WriteUniformNotFound(w)
			return
		}
		WriteUniformError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	if burnedHandle != "" {
		if h.regionSvc != nil {
			h.regionSvc.RecordBurned(core.Handle(burnedHandle))
		}
		if h.resolver != nil {
			h.resolver.Invalidate(burnedHandle)
			h.resolver.InvalidateGroup(groupID)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// RemoveMember removes a member (by handle or display name) and burns their group handle.
// DELETE /v1/groups/{id}/members/{member}
// Owner or Admin only.
func (h *GroupsHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	if h.store == nil && h.regionSvc == nil {
		NotImplemented(w, r)
		return
	}

	accountID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	groupID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	memberParam := strings.TrimSpace(chi.URLParam(r, "member"))
	if err != nil || groupID <= 0 || memberParam == "" || h.store == nil {
		WriteUniformNotFound(w)
		return
	}

	var burnedHandle string

	err = h.store.Write(r.Context(), func(tx *sql.Tx) error {
		var callerRole string
		qErr := tx.QueryRowContext(r.Context(),
			"SELECT role FROM group_members WHERE group_id = ? AND account_id = ?",
			groupID, accountID.Int64(),
		).Scan(&callerRole)
		if qErr != nil {
			return errGroupNotFound
		}
		if callerRole != "owner" && callerRole != "admin" {
			return errGroupForbidden
		}

		var targetRole string
		qErr = tx.QueryRowContext(r.Context(), `
			SELECT handle, role FROM group_members
			WHERE group_id = ? AND (handle = ? OR display_name = ?)
		`, groupID, memberParam, memberParam).Scan(&burnedHandle, &targetRole)
		if qErr != nil {
			return errGroupNotFound
		}

		// Nobody can remove the owner via RemoveMember
		if targetRole == "owner" {
			return errGroupForbidden
		}

		// Burn the member's group handle (ON DELETE CASCADE removes group_members row)
		_, execErr := tx.ExecContext(r.Context(),
			"DELETE FROM handles WHERE handle = ?",
			burnedHandle,
		)
		return execErr
	})

	if err != nil {
		if errors.Is(err, errGroupNotFound) {
			WriteUniformNotFound(w)
			return
		}
		if errors.Is(err, errGroupForbidden) {
			WriteUniformError(w, http.StatusForbidden, "forbidden")
			return
		}
		WriteUniformError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	if burnedHandle != "" {
		if h.regionSvc != nil {
			h.regionSvc.RecordBurned(core.Handle(burnedHandle))
		}
		if h.resolver != nil {
			h.resolver.Invalidate(burnedHandle)
			h.resolver.InvalidateGroup(groupID)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// RotateInvite generates and stores a new invite code for the group.
// POST /v1/groups/{id}/invite
// Owner or Admin only.
func (h *GroupsHandler) RotateInvite(w http.ResponseWriter, r *http.Request) {
	if h.store == nil && h.regionSvc == nil {
		NotImplemented(w, r)
		return
	}

	accountID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	groupID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || groupID <= 0 || h.store == nil {
		WriteUniformNotFound(w)
		return
	}

	newCode, newHash, err := generateInviteCode()
	if err != nil {
		WriteUniformError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	err = h.store.Write(r.Context(), func(tx *sql.Tx) error {
		var callerRole string
		qErr := tx.QueryRowContext(r.Context(),
			"SELECT role FROM group_members WHERE group_id = ? AND account_id = ?",
			groupID, accountID.Int64(),
		).Scan(&callerRole)
		if qErr != nil {
			return errGroupNotFound
		}
		if callerRole != "owner" && callerRole != "admin" {
			return errGroupForbidden
		}

		_, execErr := tx.ExecContext(r.Context(),
			"UPDATE groups SET invite_code_hash = ? WHERE id = ?",
			newHash, groupID,
		)
		return execErr
	})

	if err != nil {
		if errors.Is(err, errGroupNotFound) {
			WriteUniformNotFound(w)
			return
		}
		if errors.Is(err, errGroupForbidden) {
			WriteUniformError(w, http.StatusForbidden, "forbidden")
			return
		}
		WriteUniformError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"invite_code": newCode,
	})
}

// UpdateGroup updates group name, policy, member role, or ownership transfer.
// PATCH /v1/groups/{id}
func (h *GroupsHandler) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	if h.store == nil && h.regionSvc == nil {
		NotImplemented(w, r)
		return
	}

	accountID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	groupID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || groupID <= 0 || h.store == nil {
		WriteUniformNotFound(w)
		return
	}

	var req struct {
		Name                *string        `json:"name"`
		Policy              map[string]any `json:"policy"`
		MemberHandle        *string        `json:"member_handle"`
		Role                *string        `json:"role"`
		TransferOwnerHandle *string        `json:"transfer_owner_handle"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteUniformError(w, http.StatusBadRequest, "invalid_body")
		return
	}

	err = h.store.Write(r.Context(), func(tx *sql.Tx) error {
		var callerRole string
		qErr := tx.QueryRowContext(r.Context(),
			"SELECT role FROM group_members WHERE group_id = ? AND account_id = ?",
			groupID, accountID.Int64(),
		).Scan(&callerRole)
		if qErr != nil {
			return errGroupNotFound
		}
		if callerRole != "owner" && callerRole != "admin" {
			return errGroupForbidden
		}

		if req.Name != nil {
			cleanName := strings.TrimSpace(*req.Name)
			if cleanName == "" || len(cleanName) > 64 {
				return errors.New("invalid_name")
			}
			if _, execErr := tx.ExecContext(r.Context(),
				"UPDATE groups SET name = ? WHERE id = ?",
				cleanName, groupID,
			); execErr != nil {
				return execErr
			}
		}

		if req.Policy != nil {
			polBytes, mErr := json.Marshal(req.Policy)
			if mErr != nil {
				return mErr
			}
			if _, execErr := tx.ExecContext(r.Context(),
				"UPDATE groups SET policy_json = ? WHERE id = ?",
				string(polBytes), groupID,
			); execErr != nil {
				return execErr
			}
		}

		if req.MemberHandle != nil && req.Role != nil {
			if callerRole != "owner" {
				return errGroupForbidden
			}
			newRole := *req.Role
			if newRole != "admin" && newRole != "member" {
				return errors.New("invalid_role")
			}
			if _, execErr := tx.ExecContext(r.Context(),
				"UPDATE group_members SET role = ? WHERE group_id = ? AND handle = ? AND role != 'owner'",
				newRole, groupID, *req.MemberHandle,
			); execErr != nil {
				return execErr
			}
		}

		if req.TransferOwnerHandle != nil {
			if callerRole != "owner" {
				return errGroupForbidden
			}
			var newOwnerAccID int64
			if qErr := tx.QueryRowContext(r.Context(),
				"SELECT account_id FROM group_members WHERE group_id = ? AND handle = ?",
				groupID, *req.TransferOwnerHandle,
			).Scan(&newOwnerAccID); qErr != nil {
				return errGroupNotFound
			}
			if _, execErr := tx.ExecContext(r.Context(),
				"UPDATE group_members SET role = 'admin' WHERE group_id = ? AND account_id = ?",
				groupID, accountID.Int64(),
			); execErr != nil {
				return execErr
			}
			if _, execErr := tx.ExecContext(r.Context(),
				"UPDATE group_members SET role = 'owner' WHERE group_id = ? AND handle = ?",
				groupID, *req.TransferOwnerHandle,
			); execErr != nil {
				return execErr
			}
			if _, execErr := tx.ExecContext(r.Context(),
				"UPDATE groups SET owner_account_id = ? WHERE id = ?",
				newOwnerAccID, groupID,
			); execErr != nil {
				return execErr
			}
		}

		return nil
	})

	if err != nil {
		switch {
		case errors.Is(err, errGroupNotFound):
			WriteUniformNotFound(w)
		case errors.Is(err, errGroupForbidden):
			WriteUniformError(w, http.StatusForbidden, "forbidden")
		default:
			WriteUniformError(w, http.StatusBadRequest, err.Error())
		}
		return
	}

	w.WriteHeader(http.StatusOK)
}

// DeleteGroup deletes the group and burns all group-scoped handles.
// DELETE /v1/groups/{id}
// Owner only.
func (h *GroupsHandler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	if h.store == nil && h.regionSvc == nil {
		NotImplemented(w, r)
		return
	}

	accountID, ok := h.getAccount(r)
	if !ok {
		WriteUniformError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	groupID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || groupID <= 0 || h.store == nil {
		WriteUniformNotFound(w)
		return
	}

	var burnedHandles []string

	err = h.store.Write(r.Context(), func(tx *sql.Tx) error {
		var ownerID int64
		qErr := tx.QueryRowContext(r.Context(),
			"SELECT owner_account_id FROM groups WHERE id = ?",
			groupID,
		).Scan(&ownerID)
		if qErr != nil {
			if errors.Is(qErr, sql.ErrNoRows) {
				return errGroupNotFound
			}
			return qErr
		}
		if ownerID != accountID.Int64() {
			return errGroupForbidden
		}

		rows, qErr := tx.QueryContext(r.Context(),
			"SELECT handle FROM handles WHERE group_id = ?",
			groupID,
		)
		if qErr == nil {
			defer rows.Close()
			for rows.Next() {
				var hStr string
				if err := rows.Scan(&hStr); err == nil {
					burnedHandles = append(burnedHandles, hStr)
				}
			}
		}

		// Delete group (ON DELETE CASCADE removes group handles and group_members)
		_, execErr := tx.ExecContext(r.Context(), "DELETE FROM groups WHERE id = ?", groupID)
		return execErr
	})

	if err != nil {
		if errors.Is(err, errGroupNotFound) {
			WriteUniformNotFound(w)
			return
		}
		if errors.Is(err, errGroupForbidden) {
			WriteUniformError(w, http.StatusForbidden, "forbidden")
			return
		}
		WriteUniformError(w, http.StatusInternalServerError, "internal_error")
		return
	}

	for _, bh := range burnedHandles {
		if h.regionSvc != nil {
			h.regionSvc.RecordBurned(core.Handle(bh))
		}
		if h.resolver != nil {
			h.resolver.Invalidate(bh)
		}
	}
	if h.resolver != nil {
		h.resolver.InvalidateGroup(groupID)
	}

	w.WriteHeader(http.StatusNoContent)
}
