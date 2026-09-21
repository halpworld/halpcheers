// Package core defines the frozen domain types and narrow interfaces for Halp.
//
// In accordance with docs/AGENT-WORKFLOW.md §3, this package is FROZEN once merged.
// All Wave 1 tracks compile against the types and interfaces defined here.
//
// INVARIANTS ENFORCED HERE:
// - AGENTS.md Invariant 1: No message records. PingJob is strictly ephemeral in memory
//   and must never be serialized or stored durably. Digest contains only a count.
// - AGENTS.md Invariant 2: Non-blocking Enqueuer interface ensures HTTP ping handler
//   returns 202 without blocking on workers or I/O.
// - AGENTS.md Invariant 9: No identifiers in logs or metrics. AccountID, Handle, and
//   Alias implement redacting fmt.Stringer to prevent accidental leakage in formatters.
// - Data rules: All timestamps intended for durable disk storage are day-granular Day integers.
//   No second-precision timestamps reach disk.
//
// Dependencies: stdlib ONLY. No logic, no I/O, no third-party packages.
package core

import (
	"encoding/json"
	"time"
)

// AccountID is the internal unique identifier for a registered account.
//
// Invariant 9: String() returns a redacted placeholder to prevent accidental logging.
// Use Int64() when the numeric identifier is intentionally needed (e.g. SQL queries).
type AccountID int64

// Int64 returns the underlying int64 representation of the AccountID.
func (id AccountID) Int64() int64 {
	return int64(id)
}

// String implements fmt.Stringer with redaction to satisfy Invariant 9.
func (id AccountID) String() string {
	return "[REDACTED_ACCOUNT]"
}

// MarshalJSON serializes the AccountID as an integer in JSON responses.
func (id AccountID) MarshalJSON() ([]byte, error) {
	return json.Marshal(int64(id))
}

// UnmarshalJSON deserializes the AccountID from an integer in JSON responses.
func (id *AccountID) UnmarshalJSON(b []byte) error {
	var v int64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*id = AccountID(v)
	return nil
}

// Handle represents a public, shareable, disposable 13-character Crockford base32 identifier.
// The first character is always the region character ('e' for eu-1).
// Handles are the only recipient address a sender ever sees.
//
// Invariant 9: String() returns a redacted placeholder to prevent accidental logging.
// Use Raw() when the string value is intentionally required (e.g. database keys, JSON wire payload).
type Handle string

// Raw returns the underlying handle string.
func (h Handle) Raw() string {
	return string(h)
}

// String implements fmt.Stringer with redaction to satisfy Invariant 9.
func (h Handle) String() string {
	return "[REDACTED_HANDLE]"
}

// MarshalText serializes the raw handle string for text/JSON encoding.
func (h Handle) MarshalText() ([]byte, error) {
	return []byte(string(h)), nil
}

// UnmarshalText deserializes a handle string from text/JSON.
func (h *Handle) UnmarshalText(b []byte) error {
	*h = Handle(string(b))
	return nil
}

// Alias represents an optional vanity identifier (e.g. "@kenth").
// Aliases resolve internally to a nominated Handle on the send path.
//
// Invariant 9: String() returns a redacted placeholder to prevent accidental logging.
// Use Raw() when the string value is intentionally required.
type Alias string

// Raw returns the underlying alias string.
func (a Alias) Raw() string {
	return string(a)
}

// String implements fmt.Stringer with redaction to satisfy Invariant 9.
func (a Alias) String() string {
	return "[REDACTED_ALIAS]"
}

// MarshalText serializes the raw alias string for text/JSON encoding.
func (a Alias) MarshalText() ([]byte, error) {
	return []byte(string(a)), nil
}

// UnmarshalText deserializes an alias string from text/JSON.
func (a *Alias) UnmarshalText(b []byte) error {
	*a = Alias(string(b))
	return nil
}

// HandleKind categorizes the posting context of a handle.
type HandleKind string

const (
	// HandleKindPersonal is for private 1:1 sharing (default pair limit: 3/24h).
	HandleKindPersonal HandleKind = "personal"
	// HandleKindSocial is for public profiles / READMEs (default pair limit: 3/24h).
	HandleKindSocial HandleKind = "social"
	// HandleKindStream is for live streaming / broadcasts (default pair limit: 10/24h).
	HandleKindStream HandleKind = "stream"
	// HandleKindGroup is auto-created when joining a group (default pair limit: 3/24h).
	HandleKindGroup HandleKind = "group"
)

// IsValid reports whether the HandleKind is one of the recognized kinds.
func (k HandleKind) IsValid() bool {
	switch k {
	case HandleKindPersonal, HandleKindSocial, HandleKindStream, HandleKindGroup:
		return true
	default:
		return false
	}
}

// String returns the string representation of the HandleKind.
func (k HandleKind) String() string {
	return string(k)
}

// PingJob represents an in-flight ping job enqueued in memory for delivery.
//
// CRITICAL INVARIANT WARNING (AGENTS.md Invariant 1 & 9):
// PingJob is STRICTLY EPHEMERAL and exists ONLY in-memory within the bounded dispatch queue.
// It must NEVER be written to SQLite, persisted to disk, serialized into any database row,
// recorded in an audit log, or included in any metric label.
//
// EnqueuedAt is a real-time timestamp used exclusively in-memory by dispatch workers to
// discard stale jobs older than dispatch.max_age (docs/ARCHITECTURE.md § Loss policy).
// It is never written to durable storage.
type PingJob struct {
	// Recipient is the account ID of the receiving party.
	Recipient AccountID
	// SenderAccount is the account ID of the authenticated sender, needed by Guard for rate limiting.
	SenderAccount AccountID
	// EnqueuedAt is the wall-clock time the job was pushed into the in-memory queue.
	EnqueuedAt time.Time
}

// Digest represents a coalesced notification payload dispatched to a recipient.
//
// Invariant 1: It carries ONLY the recipient account and a count N.
// The client application renders the notification text based on N.
// The server never sends or records who contributed to this count.
type Digest struct {
	// Recipient is the account ID of the receiver.
	Recipient AccountID `json:"-"`
	// Count is the number of coalesced appreciation pings (N).
	Count int `json:"n"`
}

// PolicyMode defines the delivery filtering mode for an account or handle.
type PolicyMode string

const (
	// PolicyModeAll delivers pings from all valid senders.
	PolicyModeAll PolicyMode = "all"
	// PolicyModeGroupsOnly delivers pings only from shared group members.
	PolicyModeGroupsOnly PolicyMode = "groups_only"
	// PolicyModePaused pauses all delivery; incoming pings are discarded.
	PolicyModePaused PolicyMode = "paused"
)

// Policy represents the six delivery settings defined in docs/DELIVERY.md.
type Policy struct {
	// DigestWindowSeconds is the coalescing window duration (0, 60, 900, 3600, 86400). Default: 60.
	DigestWindowSeconds int `json:"digest_window_s"`
	// MaxPerHour is the maximum number of notifications delivered per hour (1-60). Default: 12.
	MaxPerHour int `json:"max_per_hour"`
	// QuietStart is the local quiet hour start (0-23), or nil if disabled.
	QuietStart *int `json:"quiet_start,omitempty"`
	// QuietEnd is the local quiet hour end (0-23), or nil if disabled.
	QuietEnd *int `json:"quiet_end,omitempty"`
	// Timezone is the IANA timezone string used to compute quiet hours.
	Timezone string `json:"tz,omitempty"`
	// MinCount is the minimum count required to trigger a digest. Default: 1.
	MinCount int `json:"min_count"`
	// Mode controls delivery filtering: "all", "groups_only", or "paused". Default: "all".
	Mode PolicyMode `json:"mode"`
}

// DefaultPolicy returns a Policy populated with the documented default settings.
func DefaultPolicy() Policy {
	return Policy{
		DigestWindowSeconds: 60,
		MaxPerHour:          12,
		QuietStart:          nil,
		QuietEnd:            nil,
		Timezone:            "UTC",
		MinCount:            1,
		Mode:                PolicyModeAll,
	}
}

// SubscriptionKind identifies the push transport protocol.
type SubscriptionKind string

const (
	// SubscriptionKindWebPush identifies a standard RFC 8291 Web Push subscription.
	SubscriptionKindWebPush SubscriptionKind = "webpush"
	// SubscriptionKindAPNs identifies Apple Push Notification service (phase 4 placeholder).
	SubscriptionKindAPNs SubscriptionKind = "apns"
	// SubscriptionKindFCM identifies Firebase Cloud Messaging (phase 4 placeholder).
	SubscriptionKindFCM SubscriptionKind = "fcm"
)

// Subscription represents a registered push delivery endpoint for an account.
type Subscription struct {
	ID         int64            `json:"id"`
	AccountID  AccountID        `json:"account_id"`
	Kind       SubscriptionKind `json:"kind"`
	Endpoint   string           `json:"endpoint"`
	P256DH     []byte           `json:"p256dh,omitempty"`
	Auth       []byte           `json:"auth,omitempty"`
	CreatedDay Day              `json:"created_day"`
	LastOKDay  *Day             `json:"last_ok_day,omitempty"`
}

// Account represents a registered user account record in the database.
type Account struct {
	ID          AccountID `json:"id"`
	KeyHash     []byte    `json:"-"`
	Region      string    `json:"region"`
	CreatedDay  Day       `json:"created_day"`
	LastSeenDay Day       `json:"last_seen_day"`
	SendTier    int       `json:"send_tier"`
	Suspended   bool      `json:"suspended"`
	RecvTotal   int64     `json:"recv_total"`
}

// HandleRecord represents a handle registered to an account.
type HandleRecord struct {
	Handle     Handle     `json:"handle"`
	AccountID  AccountID  `json:"account_id"`
	Label      string     `json:"label,omitempty"`
	Kind       HandleKind `json:"kind"`
	GroupID    *int64     `json:"group_id,omitempty"`
	Paused     bool       `json:"paused"`
	PolicyJSON string     `json:"policy_json,omitempty"`
	CreatedDay Day        `json:"created_day"`
}

// AliasRecord represents a vanity alias mapped to a handle.
type AliasRecord struct {
	Alias      Alias     `json:"alias"`
	AccountID  AccountID `json:"account_id"`
	Handle     Handle    `json:"handle"`
	CreatedDay Day       `json:"created_day"`
}

// Group represents a private group entity.
type Group struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	InviteCodeHash []byte    `json:"-"`
	OwnerAccountID AccountID `json:"owner_account_id"`
	PolicyJSON     string    `json:"policy_json,omitempty"`
	CreatedDay     Day       `json:"created_day"`
}

// GroupMember represents an account's membership in a group.
type GroupMember struct {
	GroupID     int64     `json:"group_id"`
	AccountID   AccountID `json:"account_id"`
	DisplayName string    `json:"display_name"`
	Note        string    `json:"note,omitempty"`
	Handle      Handle    `json:"handle"`
	Role        string    `json:"role"`
	JoinedDay   Day       `json:"joined_day"`
}

// ContactsBlob holds the client-encrypted contacts list stored opaquely on the server.
// The server cannot decrypt this blob (docs/IDENTITY.md § 1).
type ContactsBlob struct {
	AccountID  AccountID `json:"account_id"`
	Blob       []byte    `json:"blob"`
	Version    int64     `json:"version"`
	UpdatedDay Day       `json:"updated_day"`
}

// Block represents a safety block against incoming pings to a handle.
//
// This is the sole on-disk pair exception to Invariant 1, authorized under
// AGENTS.md Invariant 1 and docs/ABUSE.md § Layer 3. It stores only day-granular CreatedDay
// and expires after blocks.ttl_days.
type Block struct {
	SenderAccountID AccountID `json:"sender_account_id"`
	Handle          Handle    `json:"handle"`
	CreatedDay      Day       `json:"created_day"`
}
