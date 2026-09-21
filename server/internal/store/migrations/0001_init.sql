-- Phase 1 Complete SQLite Schema
-- In accordance with docs/API.md § SQLite schema and AGENTS.md:
-- - Invariant 1: There is NO messages table, NO pings table, NO events table, and NO audit_log.
-- - Data Rules: Timestamps on disk are day-granular integers (created_day, last_seen_day, etc.), never second-precision.
-- - Erasure: ON DELETE CASCADE on accounts(id) guarantees immediate, complete cascading deletion.

CREATE TABLE accounts (
  id            INTEGER PRIMARY KEY,
  key_hash      BLOB NOT NULL UNIQUE,   -- Argon2id of auth_secret, NOT of the account key
  region        TEXT NOT NULL DEFAULT 'eu-1',  -- constant for now; handle prefix derives from it
  created_day   INTEGER NOT NULL,       -- days since epoch, NOT a timestamp
  last_seen_day INTEGER NOT NULL,
  send_tier     INTEGER NOT NULL DEFAULT 0,
  suspended     INTEGER NOT NULL DEFAULT 0,
  recv_total    INTEGER NOT NULL DEFAULT 0   -- aggregate only; counted from day one
);

CREATE TABLE groups (
  id               INTEGER PRIMARY KEY,
  name             TEXT NOT NULL,
  invite_code_hash BLOB NOT NULL,
  owner_account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  policy_json      TEXT,
  created_day      INTEGER NOT NULL
);

CREATE TABLE handles (
  handle      TEXT PRIMARY KEY,
  account_id  INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  label       TEXT,
  kind        TEXT NOT NULL,            -- personal | social | stream | group
  group_id    INTEGER REFERENCES groups(id) ON DELETE CASCADE,
  paused      INTEGER NOT NULL DEFAULT 0,
  policy_json TEXT,                     -- per-handle override, NULL = inherit
  created_day INTEGER NOT NULL
);
CREATE INDEX handles_by_account ON handles(account_id);

-- One region, so the PRIMARY KEY is the whole of global uniqueness.
CREATE TABLE aliases (
  alias       TEXT PRIMARY KEY,
  account_id  INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  handle      TEXT NOT NULL REFERENCES handles(handle) ON DELETE CASCADE,
  created_day INTEGER NOT NULL
);

CREATE TABLE subscriptions (
  id          INTEGER PRIMARY KEY,
  account_id  INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  kind        TEXT NOT NULL,            -- webpush | apns | fcm
  endpoint    TEXT NOT NULL UNIQUE,
  p256dh      BLOB,
  auth        BLOB,
  created_day INTEGER NOT NULL,
  last_ok_day INTEGER
);
CREATE INDEX subs_by_account ON subscriptions(account_id);

CREATE TABLE settings (
  account_id      INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  digest_window_s INTEGER NOT NULL DEFAULT 60,
  max_per_hour    INTEGER NOT NULL DEFAULT 12,
  quiet_start     INTEGER, quiet_end INTEGER, tz TEXT,
  min_count       INTEGER NOT NULL DEFAULT 1,
  mode            TEXT NOT NULL DEFAULT 'all'
);

-- One table, because one region.
CREATE TABLE group_members (
  group_id     INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  account_id   INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  display_name TEXT NOT NULL,
  note         TEXT,
  handle       TEXT NOT NULL REFERENCES handles(handle) ON DELETE CASCADE,
  role         TEXT NOT NULL DEFAULT 'member',
  joined_day   INTEGER NOT NULL,
  PRIMARY KEY (group_id, handle)
);
CREATE INDEX group_members_by_account ON group_members(account_id);

-- Opaque to the server: sealed with contacts_key, which never leaves the device.
CREATE TABLE contacts_blob (
  account_id  INTEGER PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  blob        BLOB NOT NULL,          -- AES-256-GCM, padded to a 4 KiB boundary
  version     INTEGER NOT NULL,       -- optimistic concurrency, matched by If-Match
  updated_day INTEGER NOT NULL
);

-- The one deliberate exception to "no sender/recipient pairs on disk".
-- User-initiated, safety-necessary, day-granular, expires. See ABUSE.md.
CREATE TABLE blocks (
  sender_account_id INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  handle            TEXT NOT NULL REFERENCES handles(handle) ON DELETE CASCADE,
  created_day       INTEGER NOT NULL,
  PRIMARY KEY (sender_account_id, handle)
);
