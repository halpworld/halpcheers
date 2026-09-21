# Finding people

**We run one region: `eu-1`, in the EU.** That is
[decision 22](OPEN-QUESTIONS.md#decided) and it deletes most of what this
document used to contain. What remains is the part that was never about
regions: how you find a person, and why there is no lookup endpoint.

The multi-region design is not thrown away — it is parked at the bottom, under
[when a second region exists](#when-a-second-region-exists), because the
reasoning is expensive to rediscover and one decision made now still has to
live with it.

## The four ways to find someone

| Path | Needs a lookup? |
| --- | --- |
| Link / QR / badge someone gave you | no — the handle is the address |
| A group you are both in | roster read |
| Your contacts list | no — on your device |
| `@alias` | resolved inside the send path, never on its own |

The ordinary flow — your friend sends you their link, QR, or badge, you tap it
— needs no directory at all. That is the primary path and the one most people
will use.

## Aliases

An alias is the only identifier that needs resolving. With one region that is
a `SELECT` against one table with the alias as its primary key: uniqueness is
whatever SQLite's `PRIMARY KEY` gives us, which is all of it. No directory, no
replication, no claim log, no registry.

### Resolution is folded into sending

The original draft had `GET /v1/resolve/{alias}`. **It stays deleted**, and
this has nothing to do with how many regions we run. A standalone resolve
endpoint is an enumeration oracle: it tells a scraper which aliases exist, for
the cost of a GET, against a namespace people deliberately choose to be
guessable.

Instead, `POST /v1/ping/{target}` accepts a handle **or** an alias and resolves
internally. The alias path then inherits proof-of-work, the sender's token
buckets and the pair filter automatically — you cannot probe the namespace more
cheaply than you can send, and sending is already the most expensive thing in
the system.

Consequence to accept: the client cannot tell the user "that alias doesn't
exist". Sending to a typo looks exactly like sending to a real person. That is
the correct trade for a guessable public namespace, and it is the same uniform
`202` that invariant 7 already requires everywhere else.

## Groups

A group, its roster and its members all live in `eu-1`. Joining mints a
group-scoped handle; pinging a teammate uses that handle and needs no
directory. The `groups.min_size` deanonymisation floor counts every member on
the roster ([decision 15](OPEN-QUESTIONS.md#decided)), which is now a local
`COUNT(*)`. See [GROUPS.md](GROUPS.md).

## Contacts

For the actual "appreciate my friend again next week" case, a directory is
overkill. A contacts list solves it:

* Nickname → handle, held **on the device**.
* Populated by tapping a link, scanning a QR, or from a group roster.

Phase 1 is local-only with an encrypted export file. That stops being good
enough the moment someone has the web app and the desktop app, which is now
phase 1, so sync follows in phase 2 — as a blob the server stores and cannot
read.

### Key derivation, which is the part that makes it true

A key derived from the account key is worthless if the server ever sees the
account key, and the first draft's `POST /v1/session` sent it. So the account
key stops leaving the device, and two independent values are derived from it
client-side:

```
account_key  (16 digits, device only)
  ├─ auth_secret   = HKDF-SHA256(account_key, info="halp/auth/v1")      → sent, Argon2id-hashed server-side
  └─ contacts_key  = HKDF-SHA256(account_key, info="halp/contacts/v1")  → never leaves the device
```

Domain separation means a server that logged every login body would learn
`auth_secret` — enough to impersonate, useless for decryption. Modest for auth
on its own; the whole point for contacts.

### The blob

One row per account.

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

## When a second region exists

Everything below is designed and **not built**. It belongs to phase 3 at the
earliest, alongside the second region itself, and nothing in phase 1 or 2
should carry machinery for it.

With one exception, which is the point of this section.

### The one thing we pay for now: the handle prefix

**Handles keep a region character, from the first handle ever minted.** All of
them start with `e`. This is the only piece of multi-region design that phase 1
implements, and it is not optional, because it is the only one that cannot be
added later.

A handle is public and permanent. It goes on a GitHub README, into a QR code
printed on a sticker, into a badge cached by someone's CDN. If phase 1 mints
prefix-less handles and a second region arrives in phase 3, there are two
options and both are bad: break every handle in existence, or add a global
`handle → region` lookup — which is the enumeration oracle deleted above,
rebuilt for handles instead of aliases.

The cost of keeping it is already paid: 13 characters of Crockford base32,
1 for the region and 12 carrying 60 bits, which is the budget
[IDENTITY.md](IDENTITY.md) has always quoted. Reserving it costs nothing today
and buys the whole self-routing property later.

`accounts.region` stays in the schema for the same reason — one TEXT column,
constant at `eu-1`, and the thing the prefix is derived from.

### The rest, sketched

* **Self-routing.** Any region can accept a ping for any handle: if the prefix
  is not mine, validate, enqueue, forward over an mTLS HTTP/2 peer link, return
  `202` locally. The forwarded job is a handle and a count — no sender, no IP,
  no content.
* **The alias directory** replicates globally, as an append-only claim log per
  region pulled by peers. It would be the only globally replicated data in the
  system, and it is defensible only because an alias is public by definition
  and opt-in, which makes the opt-in the consent point for the replication too.
* **Uniqueness needs an authority**, because two regions must not hand out
  `@kenth` at once. That is a central registry
  ([decision 11](OPEN-QUESTIONS.md#decided)) running as its own small service
  rather than a promoted primary region
  ([decision 12](OPEN-QUESTIONS.md#decided)), mTLS peer-only, with no public
  DNS name and no unauthenticated read path — a public read endpoint there
  would be the enumeration oracle rebuilt on the back door.
  **Bootstrapping it is easy precisely because we waited:** the whole namespace
  is one region's `aliases` table, so the registry is seeded by replaying it.
* **Groups would span regions**, splitting the roster from the member's own
  side, and the join screen would have to say *"this group is hosted in ap-1"*.
* **Erasure would have to cross the border** — an alias tombstone into the
  replication log and roster rows dropped at each group's region. This is the
  most likely compliance failure in the multi-region design, and the
  reconciliation test should be written before the second region exists, using
  two local instances.
* **The marketing copy changes.** With one EU region, "your account data stays
  in the EU" is accurate. The moment there are two, it becomes "your account
  lives in your region", and a group hosted elsewhere makes even that need a
  footnote.

Rate limits do not need anything added here: a recipient's limits run where the
recipient's account lives and a sender's where the sender's does, so neither
multiplies across regions. Only the IP-keyed signup and login brakes do, and the
answer is to accept it, because proof-of-work is charged per attempt and so
costs N× across N regions ([decision 13](OPEN-QUESTIONS.md#decided)).

## Summary of where things live

| Data | Scope |
| --- | --- |
| Everything | `eu-1` |
| Pings | nowhere — same as always |
| Contacts | the user's device, plus an opaque blob in `eu-1` the server cannot read |

That table is the entire benefit of this decision, and it is why the privacy
analysis in [PRIVACY.md](PRIVACY.md) is now one paragraph instead of a section.
