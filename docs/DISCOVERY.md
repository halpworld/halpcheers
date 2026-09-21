# Finding people, globally

Regions are independent by design (see [PRIVACY.md](PRIVACY.md)), which is good
for data protection and would be terrible for a product where your friend is in
Hong Kong and you are in Stockholm. Nobody should ever have to know, or pick, a
region to appreciate someone.

This document is the rule that makes that true: **you never search a region.
There is exactly one global namespace, and everything else self-routes.**

## What already works without any lookup

A handle carries its home region in its first character, so it is
*self-routing*. Any region can accept a ping for any handle:

```
Stockholm user  ──POST /v1/ping/a3k9w7m2qx5t──▶  eu-1 ingress
                                                   │ prefix 'a' ≠ mine
                                                   │ validate, enqueue, 202
                                                   ▼
                                                 ap-1  ──▶  Hong Kong friend
```

No directory, no lookup, no shared state, no cross-region read on the hot path.
The forwarded job is a handle and a count — no sender, no IP, no content.

So the ordinary flow — your friend sends you their link, QR, or badge, you tap
it — is already global and always has been. That is the primary path and the
one most people will use.

## The four ways to find someone

| Path | Needs a lookup? | Cross-region |
| --- | --- | --- |
| Link / QR / badge someone gave you | no — handle self-routes | works |
| A group you are both in | roster read from the group's region | works |
| Your contacts list | no — stored on your device | works |
| `@alias` | **yes — this is the gap** | fixed below |

## 1. The global alias directory

An alias is the only identifier that needs resolving, and therefore the only
thing that needs to be global. So we make exactly that one table global and
nothing else.

**Split the two layers:**

* **Account data** — key hash, subscriptions, settings, blocks, counters —
  never leaves its home region. Not replicated, not readable by another region.
  This is what keeps the privacy analysis short.
* **The directory** — `alias → handle` only. Replicated in full to every
  region, read-only everywhere except at the owner's region.

That is a much smaller step than it sounds, because **an alias is public by
definition**. Its entire purpose is to be findable by strangers. Replicating it
discloses nothing the user has not already chosen to publish, and it is opt-in,
which makes the opt-in the consent point for the replication too. Say so in
plain words at the point of claiming one.

**Size.** One optional alias per account, ~40 bytes per row. Even at ten million
aliases that is a few hundred MB, and realistically it is single-digit MB for a
long time. Every region holds the whole thing in SQLite and serves resolution
locally, in microseconds.

**Replication.** An append-only claim log per region, pulled by peers over the
same mTLS HTTP/2 link used for ping forwarding. Rows are
`(alias, handle, owner_region, seq, tombstone)`. Regions apply peer logs
read-only. Eventual consistency is fine: a freshly claimed alias may take
seconds to be resolvable elsewhere, and the claiming UI says so.

**Uniqueness** is the one thing that needs an authority, because two regions
must not hand out `@kenth` simultaneously. A single **alias registry**
serialises claims. Hashed namespace sharding was the alternative and was
rejected: it removes the central component, but it adds moving parts to every
region and turns adding a region into a live namespace migration.

The registry is acceptable because of what it is *not* on:

* Claims are rare — one per account, once, ever.
* It is off the hot path entirely. Sending, receiving, and resolving all read
  local replicas.
* If it is down, the only thing that breaks is *claiming a new alias*.
  Degrading to a read-only namespace is a fine failure mode for a feature
  measured in claims per minute. The claiming UI says so.
* It is **reconstructible**. Each region keeps its own `aliases` rows as its own
  source of truth, so the global namespace is the union of those tables. The
  registry is an authority for *ordering* claims, not the only copy of them.

### The registry is a separate service

Not a designated primary region — promoting one region would make it special,
which is the thing the rest of this design spends its effort avoiding, and it
would put one region's uptime in front of another region's features.

`halp-registry` is one table, three endpoints, no user-facing surface:

```
POST /registry/v1/claim    { alias, handle, owner_region } → { seq } | conflict
POST /registry/v1/release  { alias, owner_region }         → { seq }   (tombstone)
GET  /registry/v1/log?since={seq}                          → append-only claim log
```

A region authenticates the user, checks the reserved list, then claims on their
behalf. The registry never sees an account, a session, an end-user IP or a ping;
it sees `(alias, handle, owner_region)` and hands back a sequence number.

> **It is never publicly reachable.** mTLS with peer certificates only, no
> public DNS, no unauthenticated read path. A public read endpoint on the
> registry is exactly the alias enumeration oracle deleted below, rebuilt by
> accident on the back door.

The honest cost: a second deployable, in a design that was proud of having one.
It is small enough to run as its own unit on the `eu-1` box until a second
region exists — the point is that it is a separate *service* with its own
interface, not a separate *machine*.

### Resolution is folded into sending

The original draft had `GET /v1/resolve/{alias}`. **Drop it.** A standalone
resolve endpoint is an enumeration oracle: it tells a scraper which aliases
exist, with no cost beyond a GET, and a global namespace makes that worse.

Instead, `POST /v1/ping/{target}` accepts a handle **or** an alias and resolves
internally. The alias path then inherits proof-of-work, the sender's token
buckets and the pair filter automatically — you cannot probe the namespace more
cheaply than you can send, and sending is already the most expensive thing in
the system.

Consequence to accept: the client cannot tell the user "that alias doesn't
exist". Sending to a typo looks exactly like sending to a real person. That is
the correct trade for a guessable public namespace, and it is the same uniform
`202` that invariant 7 already requires everywhere else.

### Abuse note specific to going global

Rate-limit state is per-region and in memory, which raises the obvious question
of whether an attacker gets N× their budget by spreading across N regions. For
the limits that protect users, no:

* A **sender account's** budget lives at that account's home region, and an
  account has exactly one home region.
* An **alias's** and a **handle's** inbound budgets live at the owner's region,
  which sees all of the traffic for its own targets regardless of where it
  entered the system. The per-target PoW escalation in [ABUSE.md](ABUSE.md)
  applies there too.

What does multiply is the IP-keyed brakes on signup and login, which is a
different problem with a different answer — see question 13 in
[OPEN-QUESTIONS.md](OPEN-QUESTIONS.md).

## 2. Groups across regions

A group lives in one region; its members do not have to.

* The **group record and roster** live in the group's home region.
* A member's **account, subscriptions and settings** stay in their own region.
* Joining mints a group-scoped handle **at the member's home region**, so it
  carries the member's prefix and self-routes like any other handle.
* Roster reads are a cross-region read of a small, cacheable list.
* Pinging a teammate uses their group handle — no directory involved.
* The `groups.min_size` deanonymisation floor counts **members, wherever they
  are**. No mechanism is needed for that: the group's home region already holds
  the full roster including foreign-region members, so the count is local.

**The honest caveat:** joining a group hosted elsewhere means your display name
and your group handle are stored in that region. That is real, it is
user-initiated, and it is minimal — but the join screen must say *"this group
is hosted in ap-1"* before you confirm, and it belongs in the data inventory in
[PRIVACY.md](PRIVACY.md). Groups are also the reason a region cannot be treated
as a hard data boundary in the marketing copy; say "your account lives in your
region" rather than "your data never leaves".

## 3. Contacts

For the actual "appreciate my friend in Hong Kong again next week" case, a
directory is overkill. A contacts list solves it:

* Nickname → handle, held **on the device**.
* Populated by tapping a link, scanning a QR, or from a group roster.

Phase 1 is local-only with an encrypted export file. That stops being good
enough the moment someone has the web app and the desktop app, which is now
phase 1, so sync follows in phase 2 — as a blob the server stores and cannot
read.

### Key derivation, which is the part that makes it true

A key derived from the account key is worthless if the server ever sees the
account key, and today `POST /v1/session` sends it. So the account key stops
leaving the device, and two independent values are derived from it client-side:

```
account_key  (16 digits, device only)
  ├─ auth_secret   = HKDF-SHA256(account_key, info="halp/auth/v1")      → sent, Argon2id-hashed server-side
  └─ contacts_key  = HKDF-SHA256(account_key, info="halp/contacts/v1")  → never leaves the device
```

Domain separation means a server that logged every login body would learn
`auth_secret` — enough to impersonate, useless for decryption. Modest for auth
on its own; the whole point for contacts.

### The blob

One row per account, in the account's home region only. Not replicated, so it
does not become a third cross-border exception.

* AES-256-GCM under `contacts_key`, fresh nonce per write.
* **Padded to a 4 KiB boundary before sealing**, so size reveals a class rather
  than a contact count the server could watch grow.
* Fixed ceiling `contacts.max_bytes`, default 64 KiB — invariant 8 covers this
  as much as it covers a Bloom filter. ~1,500 contacts at ~40 bytes each.
* `GET /v1/contacts` → `{ version, blob }`, `PUT /v1/contacts` with
  `If-Match: <version>`.

**Conflicts matter here**, because three devices is the entire reason the
feature exists. Last-writer-wins on a whole blob silently eats an offline
device's additions. Instead each entry is
`{ handle, nickname, added_day, deleted }`, and merging is a set union where
deletion wins. A stale `If-Match` is rejected; the client re-fetches, merges,
retries. No library, no vector clocks, nothing lost.

**What the server still learns:** that an account has a contacts blob, its size
class, and the day it last changed. Not who is in it, not how many. Disclosed in
[PRIVACY.md](PRIVACY.md) rather than glossed over.

**Lose the account key, lose the contacts** — consistent with there being no
recovery anywhere else here.

This stays an address book and does not drift toward the non-goals: it is
readable only by its owner, there is no "who has me in their contacts", no
mutual-contact notion and no suggestions. The server cannot read the blob, so it
could not power any of that even if we forgot ourselves.

## Summary of what is and is not global

| Data | Scope |
| --- | --- |
| Handles | regional, but self-routing everywhere via prefix |
| `alias → handle` | **globally replicated** (opt-in, public by nature) |
| Account key hash, settings, subscriptions, blocks, counters | home region only |
| Group record + roster | group's home region |
| Group membership (member's side) | member's home region |
| Contacts | the user's device, plus an opaque blob in the home region the server cannot read |
| Pings | nowhere — same as always |
