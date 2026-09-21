# AGENTS.md — context boundaries and rules

Rules for anyone, human or agent, writing code in this repository. Read
[docs/](docs/) for the reasoning; this file is the short list of things that
must not be violated.

## The product in one line

Send a person an anonymous "someone appreciates you" ping. Nothing else, ever.

## Non-negotiable invariants

**1. No message records.**
There is no `messages`, `pings`, `history`, `events` or `audit_log` table, and
no log line or metric label that records a ping. Once a ping is dispatched, it
is gone.

> *Permitted under this invariant:* ephemeral counters, rate-limit buckets,
> Bloom filter bits, count-min sketches and aggregate metrics, all in memory
> with TTLs and none of them carrying an identifier. Abuse prevention is
> impossible otherwise, and a counter is not a record.
>
> *The one exception on disk:* the `blocks` table stores
> `(sender_account, handle, created_day)`. It is user-initiated, necessary for
> the safety of the person reporting, day-granular, and expires after 12
> months. It is documented in `docs/ABUSE.md` and in the privacy policy. Do not
> add a second exception without the same treatment.

**2. Never make a synchronous push call in the HTTP handler.**
`POST /v1/ping/{handle}` validates, enqueues to a bounded channel, and returns
`202` in under 3 ms. It never awaits a push service, a disk write, or a DNS
lookup.

**3. No heavy frameworks.**
Stdlib plus `chi`, a SQLite driver and a Prometheus client. Hand-written SQL, no
ORM, no reflection-based config/DI/validation. Web Push encryption is
implemented in-house against RFC 8291/8188 with stdlib crypto. A new dependency
on the hot path needs a justification in the PR description.

**4. Token lifecycle hygiene.**
Prune subscriptions on authoritative rejection only (Web Push `404`/`410`;
phase 4 APNs `Unregistered`, FCM `registration-token-not-registered`). Delete by
exact endpoint **and** only if the row predates the failed send, or a device
that re-registered mid-flight gets silently unsubscribed. Never prune on
transient errors.

**5. Abuse prevention ships with the feature, not after it.**
Any change that adds a way to reach a user — a new handle kind, groups,
overlays, badges, an API — lands together with its rate limits and its
revocation path. See `docs/ABUSE.md`.

**6. Never reveal the sender.**
Not in the UI, not in an API response, not in an error message, not behind a
flag, not in a premium tier, not by inference from timing or counts. This is the
product. A feature that leaks it by accident is a bug of the highest severity.

**7. Enforcement is invisible to the sender.**
Rate-limited, deduped, blocked and accepted pings all return an identical `202`
with identical timing. Lookups never distinguish "not found" from "paused".
Feedback is what lets an attacker tune.

**8. Fixed-size allocations for anything keyed by user input.**
Filters, sketches and caches are sized from config at startup. An unbounded map
keyed by handle or IP is an amplification vector, not a defence.

**9. No identifiers in logs or metrics.**
No handle, account ID, alias, IP, endpoint URL or user agent in a log line, a
metric label, or an error surfaced to a client. Access logs carry the route
pattern, not the path.

## Data rules

* Timestamps on disk are **day-granular integers** (`created_day`,
  `last_seen_day`), never second-precision, unless there is a stated reason.
* **The account key never leaves the device.** The client derives
  `auth_secret = HKDF-SHA256(account_key, info="halp/auth/v1")` and sends only
  that; the server stores its Argon2id hash. `contacts_key` is derived the same
  way under a different `info` and is never transmitted. A change that makes the
  server see the account key breaks every client-side encryption claim at once.
  It must also never appear in a URL, QR code, log, metric or share link.
* Account deletion is immediate, synchronous and complete — one local
  transaction, cascading from `accounts`. Handles are never reissued.
* **One region: `eu-1`, in the EU.** No replication, no peer link, no
  `halp-registry`, no `alias_directory`. Nothing we store leaves the EU, which
  is a sentence the privacy policy prints; do not add anything that makes it
  false. The multi-region design is parked in `docs/DISCOVERY.md` and adding a
  second region is a privacy review, not a deployment.
* **Handles reserve a region character anyway** — every handle starts with `e`
  and nothing reads it. Do not "simplify" it away to reclaim 5 bits. Handles are
  public and permanent, so a prefix cannot be retrofitted onto the ones already
  in READMEs and QR codes, and the alternative later is a global
  `handle → region` lookup, which is the existence oracle below rebuilt for
  handles.
* **No existence oracles.** There is no alias resolve endpoint; resolution
  happens inside `POST /v1/ping/{target}` so probing costs the same as sending.
  Do not add a lookup, validity check, autocomplete or "is this handle real?"
  helper, however convenient it would be for the client.
* **If `halp-registry` is ever built**, it serves peer regions over mTLS only:
  no public DNS name, no unauthenticated read path, no browsable log. A public
  read endpoint there is the enumeration oracle above rebuilt on the back door,
  and it would not look like one in review. It does not exist today.

## Losses are acceptable; lying about them is not

Pings are best-effort. Dropping under load is a design decision (`queue_full`,
stale job, crash). Every drop increments a counter. Never add retries or
persistence to "fix" this without changing the spec first.

## Scope discipline

The following are permanent non-goals. Do not implement them, and do not build
infrastructure that only makes sense as a step toward them: messages, replies,
text, images, reactions, profiles, avatars, followers, feeds, leaderboards,
read receipts, sender reveal, ads, third-party analytics SDKs.

## Before you change the spec

`docs/` is the source of truth. If an implementation needs to violate something
here, change the doc in the same PR and say why. Silent divergence between the
docs and the code is how invariant 1 gets lost.
