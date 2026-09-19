# HTTP API and storage schema

Draft. Shapes over exact wire formats. All responses JSON except the badge and
the landing page.

## Conventions

* `Authorization: Bearer <session>` for everything except signup, login and the
  public pages.
* `X-Halp-PoW: <epoch>.<nonce>` on every mutating request. See
  [ABUSE.md](ABUSE.md).
* Rate-limited and blocked sends return the **same** `202` as accepted ones.
  Never leak enforcement state to a sender.
* Errors are uniform in shape and timing; lookups never distinguish
  "not found" from "paused" or "blocked".

## Endpoints

### Account

```
POST   /v1/accounts              → { account_key }         PoW ~1-2 s, per-IP limited
POST   /v1/session               { account_key } → { token, expires_at }
DELETE /v1/session
GET    /v1/account               → region, created_day, counters
GET    /v1/account/export        → full JSON export (GDPR access/portability)
DELETE /v1/account               → immediate, synchronous, irreversible
```

### Handles and alias

```
GET    /v1/handles               → [ { handle, label, kind, paused, policy, counters } ]
POST   /v1/handles               { label, kind } → { handle }
PATCH  /v1/handles/{handle}      { label?, paused?, policy? }
DELETE /v1/handles/{handle}      burn — permanent, never reissued
PUT    /v1/alias                 { alias, handle }
DELETE /v1/alias
GET    /v1/resolve/{alias}       → { handle }   heavily rate-limited, uniform errors
```

### Sending

```
POST   /v1/ping/{handle}         → 202 Accepted, empty body, < 3 ms
```

The only hot endpoint. No body. No idempotency key — the pair filter already
collapses repeats.

### Receiving

```
POST   /v1/subscriptions         { endpoint, p256dh, auth, kind } → { id }
DELETE /v1/subscriptions/{id}
GET    /v1/stream                SSE: event "ping" { n }, event "ka" heartbeat
GET    /v1/pending               → { n }   coalesced count, for SW/cold start
GET    /v1/settings
PUT    /v1/settings              { digest_window_s, max_per_hour, quiet_*, tz, min_count, mode }
POST   /v1/handles/{handle}/report-abuse
```

### Groups

```
POST   /v1/groups                { name } → { group_id, invite_code }
GET    /v1/groups                → groups I'm in
GET    /v1/groups/{id}/members   → [ { display_name, note, handle } ]
POST   /v1/groups/join           { invite_code, display_name } → { group_id, my_handle }
DELETE /v1/groups/{id}/membership            leave — burns my group handle
DELETE /v1/groups/{id}/members/{member}      remove — owner/admin only
POST   /v1/groups/{id}/invite    rotate invite code
PATCH  /v1/groups/{id}           { name?, policy? }
DELETE /v1/groups/{id}
```

### Public

```
GET    /h/{handle}               landing page, works without JS, no owner info
GET    /badge/{handle}.svg       shields-style badge, long cache
GET    /overlay/{token}          SSE overlay for OBS (phase 3, revocable token)
GET    /healthz  /readyz  /metrics
```

## SQLite schema (draft)

WAL mode, `synchronous=NORMAL`, `foreign_keys=ON`, single writer goroutine,
`busy_timeout` set. Hand-written SQL, no ORM.

```sql
CREATE TABLE accounts (
  id            INTEGER PRIMARY KEY,
  key_hash      BLOB NOT NULL UNIQUE,   -- Argon2id
  region        TEXT NOT NULL,
  created_day   INTEGER NOT NULL,       -- days since epoch, NOT a timestamp
  last_seen_day INTEGER NOT NULL,
  send_tier     INTEGER NOT NULL DEFAULT 0,
  suspended     INTEGER NOT NULL DEFAULT 0,
  recv_total    INTEGER NOT NULL DEFAULT 0   -- aggregate only, for the badge
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

CREATE TABLE groups (
  id               INTEGER PRIMARY KEY,
  name             TEXT NOT NULL,
  invite_code_hash BLOB NOT NULL,
  owner_account_id INTEGER NOT NULL REFERENCES accounts(id),
  policy_json      TEXT,
  created_day      INTEGER NOT NULL
);

CREATE TABLE group_members (
  group_id     INTEGER NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  account_id   INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  display_name TEXT NOT NULL,
  note         TEXT,
  handle       TEXT NOT NULL REFERENCES handles(handle) ON DELETE CASCADE,
  role         TEXT NOT NULL DEFAULT 'member',
  joined_day   INTEGER NOT NULL,
  PRIMARY KEY (group_id, account_id)
);

-- The one deliberate exception to "no sender/recipient pairs on disk".
-- User-initiated, safety-necessary, day-granular, expires. See ABUSE.md.
CREATE TABLE blocks (
  sender_account_id INTEGER NOT NULL,
  handle            TEXT NOT NULL REFERENCES handles(handle) ON DELETE CASCADE,
  created_day       INTEGER NOT NULL,
  PRIMARY KEY (sender_account_id, handle)
);
```

There is no `messages` table, no `pings` table, no `events` table, and no
`audit_log`. Adding one is a spec violation, not a feature.
