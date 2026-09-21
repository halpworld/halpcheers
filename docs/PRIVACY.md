# Privacy and data protection

Engineering posture, not legal advice. Have counsel review the policy text and
the DPA chain before launch — particularly the push-service transfers below.

**Hosting is one region, `eu-1`, in the EU**
([decision 22](OPEN-QUESTIONS.md#decided)). Additional regions are possible
later and would change this document materially; what is written below
describes what we actually run.

## Why this is easy for us

Data minimisation is not a compliance exercise bolted on afterwards; it is the
architecture. There is no email, no password, no phone number, no profile, no
message content, and no message history. The hardest GDPR questions are about
data we have chosen not to have.

## Data inventory

Everything the service stores, and why.

| Data | Where | Retention | Basis |
| --- | --- | --- | --- |
| Account key hash (Argon2id) | SQLite | life of account | contract |
| Account region, `created_day`, `last_seen_day` | SQLite | life of account | contract |
| Handles (+ label, paused flag, policy) | SQLite | until burned | contract |
| Alias | SQLite | until released | contract — opt-in, public by nature |
| Push subscriptions (endpoint URL, p256dh, auth) | SQLite | until pruned / 180 d idle | contract |
| Delivery settings | SQLite | life of account | contract |
| Group membership + display name | SQLite | until leave/removal | contract |
| Contacts blob (opaque ciphertext, size class, `updated_day`) | SQLite | life of account | contract — client-encrypted, unreadable to us |
| Blocks `(sender, handle, created_day)` | SQLite | 12 months | legitimate interest — safety |
| Lifetime received counter | SQLite | life of account | contract — aggregate integer, not exposed publicly in phase 1 |
| Rate-limit buckets, Bloom bits, sketches | memory only | minutes to 48 h | legitimate interest — security |
| Aggregate metrics (no identifiers) | memory / Prometheus | operational | legitimate interest |

**Not stored, anywhere, ever:** pings, sender↔recipient pairs (except a
user-initiated block), message content, IP addresses at rest, user agents,
access-log path parameters, precise account timestamps.

**The contacts blob is worth being precise about**, because "we can't read it"
is a claim that has to survive scrutiny. The account key never leaves the
device; the client derives `auth_secret` for login and `contacts_key` for
encryption by HKDF domain separation, and only the first is ever sent. The blob
is AES-256-GCM under `contacts_key` and padded to a 4 KiB boundary before
sealing. What we do learn: that an account has one, which size class it falls
in, and the day it last changed. Not who is in it, and not how many.

**Timestamps are day-granular** (`created_day`, `last_seen_day` as integer days)
rather than second-precision. Second-precision timestamps across a handful of
tables are a surprisingly good correlation fingerprint; days are enough for
retention and hygiene and much weaker as an identifier.

## What crosses a border, and why

**Nothing we store does.** There is one region, in the EU, and no replication,
no peer link and no registry ([decision 22](OPEN-QUESTIONS.md#decided)). Every
row in the inventory above lives in one SQLite file in the EU, which removes
the two documented exceptions the multi-region draft carried — a globally
replicated alias directory and foreign-region group roster rows — along with
the cross-border erasure reconciliation they required.

That makes one sentence printable that was not before: **your account data
stays in the EU.** It is worth using, and it is worth guarding — the moment a
second region exists it stops being true and the whole analysis comes back.
[DISCOVERY.md](DISCOVERY.md) keeps that design parked so the cost is visible
before anyone signs up for it.

**The honest footnote, which must travel with the sentence:** delivering a
notification necessarily involves your browser's push service, and those are
not ours and mostly not in the EU. See the next section. "Your account data
stays in the EU" is accurate; "your data never leaves the EU" is not, and must
not be printed.

## Push subscription endpoints are the sensitive bit

A Web Push endpoint is a stable, unique, third-party-issued URL tied to one
browser profile. It is personal data, and using it means:

* Disclosing that delivering notifications necessarily involves Mozilla, Google
  or Apple push infrastructure, and that those parties see the endpoint and the
  timing of deliveries even though they never see who appreciated whom.
* A transfer analysis for the non-EU push services. This is unavoidable for any
  web-push product; it must be named in the policy rather than glossed over.
* Deleting subscriptions promptly on logout, unsubscribe and account deletion.

## IP addresses

Needed transiently for signup, login and unauthenticated rate limiting. Rules:

* Never written to disk. Never in a log line. Never a metric label.
* Held in memory only, as `HMAC(daily_rotating_salt, ip)` truncated, with a TTL
  no longer than the rate-limit window.
* The salt rotates daily and is never persisted, so yesterday's buckets are
  unlinkable to today's even in a memory dump.

## Data subject rights, with no email address

The account key is the only identifier, which makes this both simple and
unusual:

* **Access / portability** — one button in the client exports the account's
  complete row set as JSON. It is small enough to render on screen.
* **Erasure** — one button. Deletes the account, all handles, alias,
  subscriptions, settings, group memberships, the contacts blob and blocks,
  immediately and synchronously. Nothing to anonymise because there is nothing pseudonymous
  left behind. Handles are never reissued.
* **Erasure is one local transaction**, which is the largest single benefit of
  running one region. Foreign-key cascades from `accounts` remove handles,
  alias, subscriptions, settings, group rows, the contacts blob and blocks in
  one go, and there is no peer to notify, no tombstone to replicate and no
  partitioned region to reconcile with on reconnect. Cross-border erasure was
  the most likely compliance failure in the multi-region draft; it no longer
  exists, and it comes back with the second region.
* **Rectification** — everything is user-editable in the client.
* **We cannot service a request from someone who has lost their key**, because
  we have no way to identify them and no way to authenticate the claim. This is
  a consequence of collecting nothing, it must be stated at signup in plain
  language, and the policy must say it too.
* **Inactive accounts** are deleted after 24 months of no `last_seen_day`
  update, after in-app warnings while the account is still reachable.

## Single region

Each deployment is a standalone Go binary with its own SQLite file, and there
is exactly one of them, in the EU. No user data is replicated anywhere, so
there is no transfer of EU user data to analyse beyond the push services below.

Users do not pick a region and are not asked about one. Handles still carry a
region character (always `e`) for the reason set out in
[IDENTITY.md](IDENTITY.md); it reveals nothing while there is one region.

Adding a US or APAC region later is a genuine change to this document, not a
deployment detail: it would create transfers, reintroduce cross-border erasure,
and require the disclosures in [DISCOVERY.md](DISCOVERY.md). Treat it as a
privacy review, not an ops ticket.

## Other obligations to plan for

* **Age.** A minimum age in the ToS, and a decision on whether to do anything
  beyond that. With no profile and no content the risk surface is small, but
  "anonymous messages to minors" is a phrase that attracts attention regardless
  of how benign the message is. Get an opinion.
* **Breach posture.** Worst-case compromise leaks handles and push endpoints —
  bad, but no content, no history, no contact details, and no social graph
  beyond group rosters. Contacts blobs would leak as ciphertext we hold no key
  for, and the blast radius is one region. Document that in the incident plan; it is genuinely
  reassuring and worth being able to say quickly.
* **Transparency report.** Trivial to produce and good for trust: we have no
  ping data to hand over, and we can say so with numbers.
* **Store / platform review.** Browser extension stores review for spam and
  messaging abuse. Ship with the abuse controls visible in the UI at submission
  time, not promised in the listing. The same applies double to the phase-2
  mobile apps — a contentless unsolicited-message app sits squarely in the blast
  radius of Apple's minimum-functionality guideline and both stores' push-spam
  rules.
