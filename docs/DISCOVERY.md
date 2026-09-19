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
must not hand out `@kenth` simultaneously. A single **alias registry** serialises
claims. This is acceptable because:

* Claims are rare — one per account, once, ever.
* It is off the hot path entirely. Sending, receiving, and resolving all read
  local replicas.
* If it is down, the only thing that breaks is *claiming a new alias*.
  Degrading to a read-only namespace is a fine failure mode for a feature
  measured in claims per minute.

Alternative if the single authority is unacceptable: shard the namespace by
`hash(alias) mod regions` so each region owns a slice of names. Same
resolution behaviour, no central component, more moving parts. Decide in
[OPEN-QUESTIONS.md](OPEN-QUESTIONS.md).

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

Rate-limit state is per-region and in memory. A scraper can therefore spread
alias attempts across N regions and get N times their intended budget. With a
handful of regions the multiplier is small, but the alias-path budget should be
set to roughly `intended_global / region_count` rather than the full amount, and
the per-target escalation in [ABUSE.md](ABUSE.md) still applies at the owning
region, which sees all of the traffic for its own aliases regardless of where it
entered. Tracked as an open question.

## 2. Groups across regions

A group lives in one region; its members do not have to.

* The **group record and roster** live in the group's home region.
* A member's **account, subscriptions and settings** stay in their own region.
* Joining mints a group-scoped handle **at the member's home region**, so it
  carries the member's prefix and self-routes like any other handle.
* Roster reads are a cross-region read of a small, cacheable list.
* Pinging a teammate uses their group handle — no directory involved.

**The honest caveat:** joining a group hosted elsewhere means your display name
and your group handle are stored in that region. That is real, it is
user-initiated, and it is minimal — but the join screen must say *"this group
is hosted in ap-1"* before you confirm, and it belongs in the data inventory in
[PRIVACY.md](PRIVACY.md). Groups are also the reason a region cannot be treated
as a hard data boundary in the marketing copy; say "your account lives in your
region" rather than "your data never leaves".

## 3. Contacts

For the actual "appreciate my friend in Hong Kong again next week" case, a
directory is overkill. A local contacts list solves it with zero server cost and
zero privacy surface:

* Nickname → handle, stored **on the device**, never on the server.
* Populated by tapping a link, scanning a QR, or from a group roster.
* Encrypted export/import file so a user can move it between their own devices.

Server-side sync would mean storing a social graph, which is the one dataset
this product has so far avoided entirely. Local-first, with a manual export, is
the phase-1 answer. Whether that is good enough with three devices is an open
question — but any sync design must be end-to-end encrypted with a key derived
from the account key, so the server holds an opaque blob and never the graph.

## Summary of what is and is not global

| Data | Scope |
| --- | --- |
| Handles | regional, but self-routing everywhere via prefix |
| `alias → handle` | **globally replicated** (opt-in, public by nature) |
| Account key hash, settings, subscriptions, blocks, counters | home region only |
| Group record + roster | group's home region |
| Group membership (member's side) | member's home region |
| Contacts | the user's device |
| Pings | nowhere — same as always |
