# Abuse prevention

This is the document that decides whether Halp survives contact with the public.
An anonymous, unsolicited, contentless ping addressed by a published identifier
is, at the protocol level, indistinguishable from a doorbell-ditch tool. Yo,
Sarahah, Secret and YikYak all died of exactly this. Everything here is a
phase-1 requirement, not hardening to add later.

Design constraint: **every defence must be O(1), in memory, and allocation-free
on the hot path.** Nothing here touches disk or the network on a normal ping.

## The invariant, restated

`AGENTS.md` invariant 1 forbids message records. It does not forbid counters.
The distinction the whole defence rests on:

* **Allowed** — a number in memory with a TTL: "account A has sent 7 pings this
  hour", "handle H received 412 pings this minute", a Bloom bit that means
  "maybe A→H already happened today".
* **Forbidden** — anything durable that says *ping X went from A to H at time T*,
  in SQLite, in a log, or in a metric label.

A Bloom filter is a good fit for this beyond its speed: it is one-way and lossy,
so it cannot be read back to enumerate who pinged whom, even by us, even under
subpoena. Privacy and abuse control point the same direction here.

## Layer 0 — Proof of work

Every ping carries a hashcash-style token. The client finds a nonce such that
`SHA-256(handle ‖ epoch ‖ challenge ‖ nonce)` has `d` leading zero bits. The
server verifies with a single hash: ~2 µs.

This is the layer that keeps server cost low while making floods expensive. It
is asymmetric by construction — the sender burns CPU, we burn microseconds.

* **Difficulty is adaptive, three ways.** A global floor (normal: ~10 ms of
  client work, invisible). A per-target escalation when a handle's EWMA inbound
  rate spikes. A per-sender escalation when an account is behaving oddly. Under
  attack, difficulty for the offending path rises to seconds while everyone
  else's stays at 10 ms.
* **Challenges are server-seeded and time-bucketed** (rotating secret, 5-minute
  epochs) so tokens cannot be precomputed far in advance or replayed after
  expiry. Replay inside an epoch is caught by the pair filter and the buckets.
* Signup PoW is heavier (~1–2 s) because it is once per lifetime, and it is the
  main cost imposed on Sybil farms.

### Difficulty calibration

The required number of leading zero bits `d` corresponds to expected hash count $2^d$.
Measured on modern mobile and desktop client runtimes (~1.5–2.5M SHA-256 hashes/sec):

| Leading zero bits `d` | Expected hashes | Expected client time | Purpose / Tier |
|---|---|---|---|
| 12 | 4,096 | ~2–3 ms | Fast low-power fallback |
| 13 | 8,192 | ~4–6 ms | Transition tier |
| **14** | **16,384** | **~10 ms** | **Standard global floor (`pow.floor_ms`)** |
| 15 | 32,768 | ~20 ms | Minor target elevation |
| 16 | 65,536 | ~40 ms | Target EWMA warning |
| 17 | 131,072 | ~80 ms | Moderate target elevation |
| 18 | 262,144 | ~150 ms | Severe target elevation |
| 19 | 524,288 | ~300 ms | Heavy throttle |
| 20 | 1,048,576 | ~600 ms | Sybil brake lower bound |
| **21** | **2,097,152** | **~1,400 ms (~1.5 s)** | **Signup brake (`pow.signup_ms`)** |
| 22 | 4,194,304 | ~2.8 s | Aggressive flood deterrence |
| 23 | 8,388,608 | ~5.5 s | Severe flood mitigation |
| 24 | 16,777,216 | ~11 s | Attack isolation maximum ceiling |


## Layer 1 — Rate limits

All token buckets, all in memory, all with TTL eviction. Numbers are starting
defaults and are runtime-configurable.

| Limit | Default | Purpose |
| --- | --- | --- |
| Per sender account | 20/hour, 100/day | one person cannot be a firehose |
| **Per (sender, recipient) pair** | **3 per 24 h**, 10 for `stream` handles | the important one |
| Per handle, inbound | configurable cap on *deliveries*/hour | protects the recipient |
| Per alias, inbound | stricter than the handle behind it | aliases are guessable |
| Per IP, signup | leaky bucket + PoW | Sybil brake |
| Per IP, login attempts | leaky bucket + PoW + uniform errors | credential guessing |
| Per group, member→member | the pair limit, scoped to the group handle, plus a group-wide cap | groups are a directory |

**The pair limit is the heart of it.** A small number of pings per person per
day makes the signal mean something and makes a single-source flood impossible
by construction.

The first draft set it to exactly 1 per 24 h. That was too tight — a five-person
team could send you at most five pings a day, and a conference QR code wants a
different rule from a personal handle — so it is now a configured maximum per
window, defaulting per handle kind:

| Handle kind | `guard.pair.max` | Window |
| --- | --- | --- |
| `personal` | 3 | 24 h |
| `social` | 3 | 24 h |
| `group` | 3 | 24 h |
| `stream` | 10 | 24 h |

**Counting to three changes the structure.** A Bloom filter answers "seen or
not" and cannot count. The implementation is a **cascade of `pair.max` filters
per window** over `HMAC(rotating_daily_key, sender_id ‖ handle)`: check slot 1,
and if the pair is already there check slot 2, and so on; insert into the first
free slot and accept; reject when every slot is occupied. Two 24 h windows
rotate as before, each filter sized for ≤0.1% false positives. Lookup stays O(1)
with a constant of at most `pair.max` hashes, and `pair.max = 1` is exactly the
original design.

The failure direction is unchanged and it is the safe one: a false positive
advances a slot, so a sender can only ever get *fewer* pings than their quota,
never more. A legitimate ping is silently dropped — acceptable for this product,
and a reason to keep the filters generously sized and alerted on fill ratio.

Rejections return `202` too. A spammer should not be able to tell a delivered
ping from a dropped one; feedback is what lets an attacker tune. Real senders
never see a rejection because real senders never hit these limits. Internally
the outcome is a metric, not a response.

## Layer 2 — Recipient controls

The recipient cannot see who pinged them, so every control is about the channel,
not the person:

* **Pause** a handle or alias — instant, reversible, no notifications delivered
  and none accumulated.
* **Burn** a handle — permanent, immediate 404, never reissued. The real block.
* **Quiet hours** and **digest window** — server-side, so pings accumulate into
  a count instead of arriving at 3 am. See [DELIVERY.md](DELIVERY.md).
* **Inbound cap** — a hard ceiling on notifications per hour regardless of how
  many people are being nice.
* **"This handle is being abused"** — one button, described below.

## Layer 3 — Muting a source you cannot see

The hard problem: the recipient wants the harassment to stop but cannot name the
sender, because the whole product is that they cannot.

Mechanism: the server keeps a small, in-memory, TTL'd count-min sketch of top
senders per handle for the current window. When the recipient reports abuse, the
server takes the heaviest senders into that handle in that window and writes a
persistent `(sender_account, handle)` block. Nothing is shown to the recipient —
they press a button and the noise stops.

This writes a sender↔recipient pair to disk, which is a **deliberate, narrow
exception** to invariant 1. It is justified because it is user-initiated,
strictly necessary for the safety of the person reporting, capped in volume, and
carries no timestamp beyond a day-granularity `created_day`. It is documented as
an exception in `AGENTS.md` and in the privacy policy rather than smuggled in.
Blocks expire after 12 months and are deletable by the recipient.

## Layer 4 — Anomaly response

Per-handle and global EWMA counters, checked on the hot path as a single
comparison:

* Handle inbound rate jumps ≫ its own baseline → raise that handle's PoW
  difficulty, widen its digest window automatically, and tell the owner in the
  next digest ("this is unusually busy; we've slowed it down").
* Global accept rate crosses a ceiling → raise the global PoW floor.
* Signup rate from one IP/ASN spikes → raise signup difficulty for that range.
* Distribution of senders to one handle collapses toward a few accounts →
  treat as a flood even if each account is under its own limit.

All automatic, all reversible, none requiring a human at 3 am.

## Layer 5 — Account hygiene

* New accounts have a lower send ceiling for their first 24 h.
* An account whose pings are overwhelmingly to handles that later report abuse
  gets its ceiling cut and its PoW raised, then suspended.
* Suspension is silent to the sender: their pings return `202` and go nowhere.
  A spammer who knows they are blocked makes a new account; one who thinks it is
  working does not.

## Sizing rule

Ban lists, sketches and filters must be **fixed-size allocations chosen at
startup from config**, never unbounded maps keyed by user input. An abuse
defence that OOMs the box under attack is an amplification vector, not a
defence.

Launch sizing is driven by `guard.expected_daily_pings = 1000`. At ~2 bytes per
entry that is kilobytes per filter, so the allocation carries a **floor of 1 MiB
per cascade slot per window** whatever the config says: it is cheap enough to
over-provision by two orders of magnitude, and the floor means a mistyped config
value is harmless rather than silently producing a filter that is full on day
one. Total pair-filter footprint at launch: `2 windows × pair.max slots × 1 MiB`
≈ 6 MiB. See the tunables table in [ARCHITECTURE.md](ARCHITECTURE.md).

## What we deliberately do not build

* No reporting queue for humans to triage — there is no content to triage.
* No reputation score shown to anyone.
* No "who pinged me" reveal, ever, under any premium tier. The moment that
  exists, the product is a different and much worse one.
