# Privacy, data protection and regions

Engineering posture, not legal advice. Have counsel review the policy text and
the DPA chain before launch — particularly the push-service transfers below.

Hosting is EU-first, with the possibility of additional regions later.

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
| Alias | SQLite | until released | contract |
| Push subscriptions (endpoint URL, p256dh, auth) | SQLite | until pruned / 180 d idle | contract |
| Delivery settings | SQLite | life of account | contract |
| Group membership + display name | SQLite | until leave/removal | contract |
| Blocks `(sender, handle, created_day)` | SQLite | 12 months | legitimate interest — safety |
| Lifetime received counter | SQLite | life of account | contract |
| Rate-limit buckets, Bloom bits, sketches | memory only | minutes to 48 h | legitimate interest — security |
| Aggregate metrics (no identifiers) | memory / Prometheus | operational | legitimate interest |

**Not stored, anywhere, ever:** pings, sender↔recipient pairs (except a
user-initiated block), message content, IP addresses at rest, user agents,
access-log path parameters, precise account timestamps.

**Timestamps are day-granular** (`created_day`, `last_seen_day` as integer days)
rather than second-precision. Second-precision timestamps across a handful of
tables are a surprisingly good correlation fingerprint; days are enough for
retention and hygiene and much weaker as an identifier.

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
  subscriptions, settings, group memberships and blocks, immediately and
  synchronously. Nothing to anonymise because there is nothing pseudonymous
  left behind. Handles are never reissued.
* **Rectification** — everything is user-editable in the client.
* **We cannot service a request from someone who has lost their key**, because
  we have no way to identify them and no way to authenticate the claim. This is
  a consequence of collecting nothing, it must be stated at signup in plain
  language, and the policy must say it too.
* **Inactive accounts** are deleted after 24 months of no `last_seen_day`
  update, after in-app warnings while the account is still reachable.

## Multi-region

**Regional independence, not replication.** Each region is a standalone
deployment with its own SQLite file. No user data is replicated across borders
at rest, so adding a US or APAC region does not create a transfer of EU user
data.

Cross-region pings carry a handle and nothing else — no sender, no IP, no
content — forwarded over mTLS to the owning region. That payload contains no
personal data about the sender and only a pseudonymous identifier for the
recipient, which keeps the cross-border story trivial.

Users pick a region at signup (default nearest, overridable, and it should be
possible to deliberately choose EU from anywhere). The handle prefix makes the
region visible, which is honest: you can tell where your data lives by looking
at your own handle.

## Other obligations to plan for

* **Age.** A minimum age in the ToS, and a decision on whether to do anything
  beyond that. With no profile and no content the risk surface is small, but
  "anonymous messages to minors" is a phrase that attracts attention regardless
  of how benign the message is. Get an opinion.
* **Breach posture.** Worst-case compromise leaks handles and push endpoints —
  bad, but no content, no history, no contact details, and no social graph
  beyond group rosters. Document that in the incident plan; it is genuinely
  reassuring and worth being able to say quickly.
* **Transparency report.** Trivial to produce and good for trust: we have no
  ping data to hand over, and we can say so with numbers.
* **Store / platform review.** Browser extension stores review for spam and
  messaging abuse. Ship with the abuse controls visible in the UI at submission
  time, not promised in the listing. The same applies double to the phase-2
  mobile apps — a contentless unsolicited-message app sits squarely in the blast
  radius of Apple's minimum-functionality guideline and both stores' push-spam
  rules.
