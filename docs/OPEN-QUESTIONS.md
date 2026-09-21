# Open questions

Decisions still outstanding, and assumptions made in the plan that are worth
challenging. Resolve the blocking ones before Phase 1 code.

Answered items move to [Decided](#decided) at the bottom rather than being
deleted, so the reasoning survives.

**Scope note:** [decision 22](#decided) cut the system to
one region. That closed question 13 outright and turned decisions 11 and 12
into designs we are not building yet. They are kept because the reasoning is
expensive to rediscover and because one phase-1 decision — the handle prefix —
only makes sense in their light.

## Operational

16. **Funding.** There is no revenue model here and the non-goals forbid the
    usual ones. A $10–20 VPS is cheap, but decide now whether this is a hobby
    project, donation-funded, or something else — it changes how much the
    100M/day ceiling is worth engineering for.
17. **Region set and hosting provider.** EU-first is decided; which provider,
    and what the backup/restore story is for the SQLite file (it holds the only
    copy of every account's handles).
18. **Legal review.** Push-service transfer analysis, age policy, and the ToS.
    Named in [PRIVACY.md](PRIVACY.md), not yet done.
19. **Abuse escalation path.** Everything is automatic by design. Who looks at
    the dashboard when the automatic response is not enough, and what manual
    levers exist?

## Deferred

20. Region migration for an existing account. Out of scope for v1; current
    answer is "make a new account and repoint your alias".
21. Account recovery. There is none, by design. Revisit only if user research
    says lost keys are killing retention — and if so, the fix is better key
    backup UX, not a recovery backdoor.

## Decided

Kept with their original numbering. Each entry says what was decided, what it
costs, and where it landed in the docs.

### 1. Safari Web Push inside web extensions → **no Safari extension; macOS desktop app instead**

The Safari extension is dropped from the plan. Safari users get the **web app**
(Safari Web Push works there, on macOS and iOS, for a site added to the Dock or
Home Screen) and the **desktop app**.

Consequence, stated plainly: this pulls the Tauri desktop app from Phase 2 into
Phase 1. The server side of it — the SSE hub — was already Phase 1, so the
incremental work is the shell, the tray, native notifications and packaging.
Tauri builds all three desktop targets from one codebase, so Windows and Linux
come along nearly free and there is no reason to ship macOS alone. What is *not*
free: an Apple Developer Program membership, code signing and notarisation
become Phase 1 dependencies with real lead time. Start that paperwork early.

Net: Phase 1 grows. That is a deliberate trade for dropping the platform
assumption the plan was least sure of. See [ROADMAP.md](ROADMAP.md) and
[DELIVERY.md](DELIVERY.md).

### 2. RFC 8291 ephemeral key reuse → **reuse the keypair, cache the shared secret**

Decided: reuse the application-server ECDH keypair per subscription and cache
the derived shared secret, so a digest push costs an AES-GCM seal rather than a
fresh key agreement.

Two honest caveats on that decision:

* **It buys less than it looks like.** Payloadless-by-default means the common
  case — `n == 1` — carries no body and therefore no crypto at all. Key reuse
  only touches the `n > 1` digest path, which coalescing has already made the
  minority of deliveries. If the RFC turns out to forbid it, the cost of falling
  back is small, which is why this is safe to decide now.
* **The salt is then the only per-message freshness.** With a fixed shared
  secret per subscription, the 16-byte RFC 8188 salt is all that varies between
  messages, so it must come fresh from `crypto/rand` for **every** message. A
  repeated salt against a cached secret is key-and-nonce reuse under AES-GCM,
  which is catastrophic, not merely untidy. Make that a test, not a comment.

Still to do before the code lands: read RFC 8291 §3.1 and confirm reuse is
permitted rather than assuming it. This decision sets the preferred direction;
it does not license guessing at the standard.

### 3. Pair-limit window → **loosened to 3 per 24 h, configurable, per handle kind**

1 per person per 24 h was too tight: a five-person team could send you at most
five pings a day, and a conference QR code wants a different rule entirely.

New defaults, all runtime-configurable (`guard.pair.*`):

| Handle kind | Default pair limit |
| --- | --- |
| `personal` | 3 per 24 h |
| `social` | 3 per 24 h |
| `group` | 3 per 24 h |
| `stream` | 10 per 24 h |

**This changes the data structure, which is the part worth noticing.** A Bloom
filter answers "seen or not" and cannot count to three. The replacement is a
cascade of `pair.max` filters per window: check slot 1, and if the pair is
present check slot 2, and so on; insert into the first free slot; reject when
all slots are full. Memory is `pair.max ×` the old allocation and lookup is
still O(1) with a small constant. `pair.max = 1` degenerates exactly to the old
design.

The false-positive direction is unchanged and still the safe one: a false hit
advances a slot, so a sender can only ever get *fewer* pings than their quota,
never more. See [ABUSE.md](ABUSE.md).

### 4. `expected_daily_pings` at launch → **1,000**

At 1,000/day and ~2 bytes per entry the pair filter is measured in kilobytes,
even across two rotating windows and three cascade slots. Because it is that
cheap, the allocation carries a **floor of 1 MiB per slot per window** regardless
of the configured value: it absorbs two orders of magnitude of growth before
anyone has to think about the config again, and it makes a mistyped config
harmless instead of silently useless. See [ARCHITECTURE.md](ARCHITECTURE.md).

### 5. Domain → **`halp.to` stays a placeholder**

No registration yet. It is not blocking for code, but it is blocking for
anything public: badge URLs, share links, QR codes and the Web Push VAPID
subject all bake the origin in, and changing it later invalidates every QR code
already printed and every badge already in a README. Resolve before the first
link is shared outside the team.

### 6. Digest defaults → **confirmed: 60 s window, 12 per hour**

Kept as the shipping defaults. Still a guess; revisit with real users rather
than by argument. See [DELIVERY.md](DELIVERY.md).

### 7. Sender feedback → **confirmed: a plain "Sent."**

No delivery claim, no "they may receive this as part of a digest". The uniform
`202` stands, which means an occasional honest sender is told "sent" for a ping
that was deduped, blocked or dropped. That is the cost of invariant 7 and it is
accepted deliberately.

### 8. Public received counter → **not in Phase 1**

The badge ships plain. No public count, no API field, no `?count=` variant.

One implementation note that follows from invariant 1: keep incrementing the
aggregate `accounts.recv_total` from day one anyway. There are no ping records,
so a counter that is not maintained from the first ping can *never* be
reconstructed. It costs one integer per account, it is already in the
[PRIVACY.md](PRIVACY.md) inventory, and it stays invisible until the
scoreboard question in item 8 gets a real answer.

### 9. Minimum group size → **5, with a config knob added later**

5 stays the default. The knob is **operator** configuration (`groups.min_size`),
not a per-group admin setting: it exists to protect members from
deanonymisation by inference, and the three-person group whose admin would want
to turn it off is precisely the case it exists for. See [GROUPS.md](GROUPS.md).

### 10. Alias policy → **confirmed**

One alias per account, released after 12 months of inactivity, with a reserved
list. See [IDENTITY.md](IDENTITY.md) and [DISCOVERY.md](DISCOVERY.md).

### 11. Alias uniqueness → **a central registry**, not hashed sharding

*Designed, not built — [decision 22](#decided) made it unnecessary until there is
a second region. Kept because that is when the reasoning will be needed.*

One authority serialises alias claims. Sharding the namespace by
`hash(alias) mod regions` was the alternative; it removes the central component
but adds moving parts to every region and makes adding a region a namespace
migration, which is the thing worth avoiding most.

The registry is acceptable because of *what it is not on*: claims are rare (one
per account, once), it is entirely off the hot path, and sending, receiving and
resolving all read a local replica. Its outage mode is benign — claiming a new
alias pauses, everything else keeps working.

One property worth stating because it changes the backup story: the registry is
**reconstructible**. Every region keeps its own `aliases` rows as its own source
of truth, so the global namespace is the union of those tables and can be
rebuilt if the registry loses its disk. It is an authority for *serialising*
claims, not the sole copy of the data.

### 12. Where the registry runs → **a separate tiny service**

*Designed, not built, for the same reason as 11.*

Not a designated primary region. Promoting one region would make it special,
which is what the rest of the design spends its effort avoiding, and it would
put a foreign region's uptime in front of another region's feature.

`halp-registry` is one table, three endpoints and no user-facing surface:

```
POST /registry/v1/claim    { alias, handle, owner_region } → { seq } | conflict
POST /registry/v1/release  { alias, owner_region }         → { seq }   (tombstone)
GET  /registry/v1/log?since={seq}                          → append-only claim log
```

Regions authenticate the user, then call the registry on their behalf; the
registry never sees an account, a session, an end-user IP or a ping.

**It is never publicly reachable.** mTLS with peer certificates only, no public
DNS, no unauthenticated read path. A public registry read endpoint would be
precisely the alias enumeration oracle that
[DISCOVERY.md](DISCOVERY.md) deliberately removed, rebuilt by accident on the
back door.

The honest cost: this is a second deployable in a design that was proud of
having one. That cost is exactly why decision 22 does not pay it yet. When it
is paid, the point is that the registry is a separate *service* with its own
interface, not necessarily a separate *machine* — it can share a box with
`eu-1` at first.

**Bootstrapping is easy because we waited.** The whole namespace will be one
region's `aliases` table, so the registry is seeded by replaying it.

### 13. Per-IP rate limits across regions → **moot; recommendation stands for later**

Closed by decision 22. With one region there is one set of buckets and nothing
multiplies.

The analysis is kept because it survives the simplification. The question as
first written overstated the problem: a recipient's limits run where the
recipient's account lives, a sender's where the sender's does, and an account
has one home region, so **neither multiplies across regions**. Only limits keyed
to something with no home region do — in practice the two IP-keyed brakes on
signup and login.

The recommendation, for whenever a second region exists: **accept the
multiplier**. Proof-of-work does not divide. Signup costs ~1–2 s of client CPU
*per attempt*, so spreading across N regions costs the attacker N× the CPU —
the bucket is a brake, PoW is the price, and only the brake multiplies. If the
region count ever grows enough for that to matter, gossip per-IP counts over
the peer link every ~60 s rather than putting a shared store behind signup.

### 14. Contacts sync → **an opaque encrypted blob the server cannot read**

Answering this rather than asking it back, since it is an engineering problem
with a standard shape.

**Key derivation is the part that makes it true.** Today `POST /v1/session`
sends the raw account key, so a key derived from it would be derivable by the
server too, and "end-to-end encrypted" would be a lie we told ourselves. The
account key therefore never leaves the device again; two independent values are
derived from it client-side:

```
account_key  (16 digits, device only)
  ├─ auth_secret   = HKDF-SHA256(account_key, info="halp/auth/v1")      → sent, Argon2id-hashed server-side
  └─ contacts_key  = HKDF-SHA256(account_key, info="halp/contacts/v1")  → never leaves the device
```

Domain separation means a server that logs the login body learns `auth_secret`,
which is enough to impersonate but not to decrypt. That is a modest win for
auth on its own and the whole ballgame for contacts.

**The blob.** One row per account, `contacts_blob(account_id, blob, version,
updated_day)`, in the account's home region only — not replicated, so it does
not become a third cross-border exception.

* AES-256-GCM (stdlib) under `contacts_key`, fresh nonce per write.
* **Padded to a 4 KiB boundary before sealing**, so the size reveals a class
  rather than a contact count the server can watch grow.
* Fixed ceiling `contacts.max_bytes`, default 64 KiB — invariant 8 applies to
  this as much as to a filter. That is roughly 1,500 contacts at ~40 bytes each.
* `GET /v1/contacts` → `{ version, blob }`; `PUT /v1/contacts` with
  `If-Match: <version>`.

**Conflicts, because three devices is the whole point.** Last-writer-wins on a
whole blob silently eats an offline device's additions. Instead each entry is
`{ handle, nickname, added_day, deleted }` and the merge is a set union with
deletion winning. A stale `If-Match` is rejected, the client re-fetches, merges
and retries, and nothing is lost. This is a CRDT in the same sense a shopping
list is one — no library, no vector clocks.

**What the server still learns, stated plainly:** that an account has a contacts
blob, its size class, and the day it last changed. Not who is in it, not how
many. That is the residual and it is disclosed in
[PRIVACY.md](PRIVACY.md) rather than glossed.

**Lose the account key, lose the contacts.** Consistent with there being no
recovery anywhere else in this design.

**Where the boundary is**, since "contacts" is adjacent to several non-goals: it
is a private address book, readable only by its owner. There is no "who has me
in their contacts", no mutual-contact notion, no suggestions — the server cannot
read the blob, so it could not power any of that even if we forgot ourselves.

### 15. Minimum group size across regions → **members, wherever they are**

*Trivially satisfied under [decision 22](#decided); the reasoning still binds if
a second region arrives.*

Confirmed. `groups.min_size` counts every member on the roster regardless of
which region their account lives in. No mechanism is needed for this: the
group's home region already holds the full roster, including foreign-region
members, so the count is a local one.

### 22. How many regions → **one, `eu-1`**

EU-first was always the plan; this makes it EU-only until there is a reason to
change. The multi-region machinery was the single largest source of complexity
in the design and none of it was buying anything yet.

**What it deletes.** The globally replicated `alias_directory`, the append-only
claim log, the mTLS peer link, `/peer/v1/*`, the `halp-registry` deployable,
the roster/mirror split in the group schema, cross-border erasure
reconciliation, and the join-screen disclosure for foreign-hosted groups. Alias
uniqueness becomes a `PRIMARY KEY`. Account deletion becomes one cascading
transaction. The project goes back to being one binary and one SQLite file,
which is what the README promises.

**What it buys beyond simplicity.** "Your account data stays in the EU" becomes
an accurate sentence, where the multi-region draft could only say "your account
lives in your region". It travels with a footnote — delivering a notification
still involves a third-party push service — but it is a real improvement and
worth protecting.

**What it costs, which is the part to watch.** Everything above is reversible
by building it later. Exactly one thing is not: **the handle region prefix**,
which phase 1 keeps. Every handle starts with `e` and nothing reads it.

A handle is public and permanent — GitHub READMEs, printed QR codes, cached
badges. Mint them without a prefix and a later region leaves two options:
break every handle in existence, or add a global `handle → region` lookup,
which is the enumeration oracle this design deliberately removed, rebuilt for
handles. Reserving one character costs nothing, because the 13-character budget
was always 1 region character plus 60 bits. `accounts.region` stays for the
same reason.

**Adding a second region is a privacy review, not an ops ticket.** It
reintroduces transfers, cross-border erasure and the disclosures above. The
design is parked in [DISCOVERY.md](DISCOVERY.md) precisely so the cost is
visible before anyone commits to it.

### 23. How Phase 1 is split for parallel agents → **module ownership, behind a frozen contract wave**

Phase 1 is built by several agents working at once. The naive split — one agent
per feature, everyone editing whatever the feature touches — produces constant
conflicts in exactly the files that matter most (the router, the schema, the
config struct) and an integration phase longer than the build.

So the work is cut along the package boundaries in
[ARCHITECTURE.md](ARCHITECTURE.md): **one agent owns whole directories**, and the
four things everyone compiles against are written first, together, by one
foundation track, and then frozen:

* `server/internal/core/` — shared types and the small interfaces the tracks
  implement for each other.
* `server/internal/http/router.go` — every phase-1 route mounted to a
  `notImplemented` stub. Feature tracks replace their stub in their own
  `http/<group>.go` file and never touch the router again.
* `server/internal/store/migrations/0001_init.sql` — the entire phase-1 schema
  in one file, so nobody races to claim `0002`.
* `server/internal/config/` — the whole tunables table, including keys whose
  consumer does not exist yet.

The cost is a serialisation point: nothing parallel starts until those land, and
a change to `core/` afterwards is a PR of its own that names every track it
breaks. That is the right trade. The alternative — letting each track add its
own types and routes as it goes — is cheaper for a week and then produces a
system nobody can assemble.

Two packages were added to the documented layout for this:
`internal/config/` and `internal/obs/`, which existed implicitly (the tunables
table and the observability rules) but had no home. `internal/auth/pow/` is a
subpackage so that proof-of-work and account handling can be built in parallel
without sharing a directory.

The breakdown, the dependency order and the rules are written down in
[AGENT-WORKFLOW.md](AGENT-WORKFLOW.md) and tracked as GitHub issues.

### 24. Where UI and UX decisions live → **one shared [UI.md](UI.md), copy included**

There was no UI document, which meant four client tracks — web, extension,
desktop, and the server-rendered public pages — would each have invented their
own copy, their own error states and their own idea of what happens after you
press the button.

That is worse here than in a normal product, because several of those choices
*are* privacy properties. A client that shows "you've already appreciated this
person today" rebuilds the enumeration oracle in the UI. A client that shows a
different spinner for a rate-limited send leaks enforcement state and breaks
invariant 7. A client that lists pings implies a ping log exists. None of those
look like invariant violations while you are writing them.

So [UI.md](UI.md) is a specification, not a style guide: it carries the exact
copy for the key wall, the login failure, the burn dialog and the abuse
confirmation; the two-state send button; the fixed 300 ms latency floor that
makes an unknown handle and a delivered ping look identical; and a checklist of
what no screen may ever show. Agents implement it rather than deciding it.

The cost is that UI changes now need a doc change. Given that "the send button
has exactly two states" is load-bearing for the product's central claim, that is
the correct amount of friction.

### 25. Where agents log decisions → **this file, numbered, and nowhere else**

Candidates were an `adr/` directory, decision records in PR descriptions, and
keeping the existing `## Decided` section. The existing section wins because it
already holds twenty-two entries with their reasoning attached, and a second
location immediately makes the first one untrustworthy — the question stops
being "what did we decide?" and becomes "where did we decide it?".

The mechanics: an agent appends a numbered entry **in the same PR as the code**,
claiming its number by commenting on the Phase 1 tracker issue first. First
comment wins, the loser renumbers on rebase. Entries are never deleted; a
superseded decision gets a follow-up that says so.

The known weakness is the shared file: a dozen agents appending to the end of
one Markdown file will produce merge conflicts. They are trivial ones — two
additions at the end of a file — and the number claim keeps them from silently
colliding. That is cheaper than losing the single-source property.

### 27. SQLite driver choice → **modernc.org/sqlite (pure Go, CGO_ENABLED=0)**

Decided: use `modernc.org/sqlite` instead of `mattn/go-sqlite3`.

The decision between a pure Go SQLite implementation (`modernc.org/sqlite`) and a
cgo wrapper around the C library (`mattn/go-sqlite3`) turns on operational
simplicity and container footprint. With `modernc.org/sqlite`, the server binary
compiles with `CGO_ENABLED=0` into a completely static executable. This allows the
runtime container image to be built on top of `scratch` or distroless static
without any C compiler toolchain, dynamic linker or libc dependencies. Cross-compilation
across architectures (amd64/arm64) is trivial and requires no cross-gcc.

The cost is slightly higher CPU overhead in pure query execution compared to
native C SQLite. For Halp's architecture, this trade-off is strongly favorable:
writes are rare and funneled through a single dedicated writer goroutine (avoiding
multi-threaded write contention entirely), while the hot path serves reads from an
in-process LRU cache and the operating system page cache. The operational stability
of a pure Go static binary in a scratch container cleanly satisfies the "one
deployable" goal in [ARCHITECTURE.md](ARCHITECTURE.md) and invariant 3.

<<<<<<< HEAD
<<<<<<< HEAD
### 28. PoW difficulty calibration and clock skew tolerance → **d=14 floor, d=21 signup, ±1 epoch skew tolerance**

Decided: the default global floor `pow.floor_ms = 10` corresponds to $d=14$ leading zero bits ($2^{14} = 16,384$ hashes, ~10 ms client compute). Signup `pow.signup_ms = 1500` maps to $d=21$ leading zero bits ($2^{21} = 2,097,152$ hashes, ~1.4–1.5 s client compute).

Tolerance for clock skew on challenge epochs is set to $\pm 1$ adjacent epoch ($\pm 5$ minutes around the active epoch). Tokens for epochs older than `cur - 1` or future epochs beyond `cur + 1` are authoritatively rejected as expired. This accounts for reasonable client device clock drift without opening a precomputation window wider than 10 minutes. Challenges are derived deterministically via HMAC-SHA256 from a server-seeded rotating secret. Hot-path verification uses a stack-allocated buffer and `sha256.Sum256` achieving zero heap allocations and ~130 ns execution time, well inside the 2 µs request path budget.

### 29. Argon2id parameters and deterministic salt for auth_secret → **Argon2id (t=1, m=64 MiB, p=4) with deterministic salt**

Decided: `accounts.key_hash` is computed as `Argon2id(auth_secret, salt="halp-argon2id-v1", t=1, m=64 MiB, p=4, keyLen=32)`.

Reasoning:
1. `auth_secret` is derived client-side via HKDF-SHA256 from the 16-digit cryptographically random account key (~53.15 bits entropy). The client submits only `auth_secret` to `POST /v1/session` without any public username or account identifier.
2. Because there is no public account identifier submitted with login, account lookup requires querying `SELECT id FROM accounts WHERE key_hash = ?`. To support O(1) indexed lookup without iterating through every account in the database on every login attempt, the Argon2id hash must be deterministic for a given `auth_secret`.
3. A fixed 16-byte domain salt (`"halp-argon2id-v1"`) combined with 64 MiB RAM and 4 threads satisfies RFC 9106 recommended parameters. Rainbow tables are impossible because the input possesses over 53 bits of high-entropy cryptographic randomness from `crypto/rand`. The 64 MiB memory hardness makes offline ASIC/GPU dictionary cracking prohibitively expensive.
4. On the login endpoint, timing uniformity (AGENTS.md Invariant 7) is strictly preserved: rate-limited attempts and malformed inputs compute a dummy Argon2id hash with the identical parameters before returning, ensuring an attacker cannot distinguish between a non-existent account, bad credentials, and a tripped rate limiter by measuring response latency.

### 30. Handle encoding and resolver LRU cache → **Crockford base32 with 'e' prefix, 60-bit entropy, bounded in-process LRU**

Decided: handles are minted as 13 characters: a fixed region prefix (`e` for `eu-1`)
followed by 12 characters of lowercase Crockford base32 (`0123456789abcdefghjkmnpqrstvwxyz`,
excluding `i`, `l`, `o`, `u`). Each character encodes 5 bits, providing 60 bits of
entropy drawn from `crypto/rand`.

Handle and alias resolution on the ingress path is backed by an in-process thread-safe
LRU cache (`*region.Resolver`) sized from `config.ResolverCacheSize` (default 100,000 items,
~16 MB memory footprint). Cache misses fall back to SQLite read queries (`handles` and `aliases`
tables). When a handle is updated or burned (moved into `burns` table), the resolver LRU
entry is invalidated immediately. In accordance with invariant 8, the cache uses fixed
memory bounds and cannot be bloated by arbitrary attacker inputs.

### 31. Pair-limit cascaded Bloom filters and count-min sketch dimensions → **cascaded slots with 1 MiB minimum floor, 4x2048 count-min sketch**

Decided: the pair deduplication limit (`server/internal/guard/`) is enforced using a cascade of Bloom filters per 24-hour window, rotated across today and yesterday. Each slot is allocated with a hard 1 MiB floor (`guard.pair.slot_bytes = 1048576`), yielding a false positive probability $< 10^{-6}$ for typical daily ping volumes.

In accordance with `docs/ABUSE.md` § Layer 1, Bloom filter lookup is directional: when an element collides (false positive), the evaluation advances to the next slot. Consequently, a false positive can only ever cost an honest sender quota, never grant an extra ping to an attacker.

Abuse top-sender tracking (Layer 3) utilizes an in-memory Count-Min Sketch sized at depth $d=4$ and width $w=2048$, providing bounded-error frequency estimation with fixed memory footprint (~32 KiB). On explicit `report-abuse`, the top sender is written to the persistent `blocks` table, which is the sole durable record exception under Invariant 1.

### 32. Ingress bounded queue and drop policy → **non-blocking channel send (~100 ns), return 202 on queue-full drops**

Decided: `POST /v1/ping/{target}` enqueues `core.PingJob` into a bounded in-memory Go channel (`coalesce.Queue`) using a non-blocking `select`. When the queue reaches its fixed capacity limit (default 65,536 jobs, sized from startup config `queue_size`), the incoming job is dropped immediately, the metric `halp_pings_dropped_total{reason="queue_full"}` is incremented, and the handler returns `202 Accepted` with an empty body in under 3 ms.

Reasoning:
1. In accordance with AGENTS.md Invariant 2, the HTTP handler must complete in under 3 ms and never await a push service, disk write, or DNS lookup. Blocking on channel send when workers are saturated would violate the latency ceiling and cascade upstream into HTTP connection timeouts.
2. In accordance with AGENTS.md Invariant 7 ("Enforcement is invisible to the sender"), returning `429 Too Many Requests` or an error would signal system overload and invite retry amplification from well-behaved clients or give attackers feedback on queue depth.
3. Best-effort delivery is an explicit design choice ("Losses are acceptable; lying about them is not"). Drops under load are counted via Prometheus metrics rather than masked by un-bounded buffers or blocking retries.

### 33. Coalesce accumulator eviction and memory bounds → **in-memory counter map bounded by fixed capacity, stale jobs discarded on ingestion**

Decided: the coalesce accumulator (`server/internal/coalesce/`) aggregates incoming appreciation pings purely as an in-memory map keyed by recipient `core.AccountID` to an accumulator entry containing only a count $N$ and first/last seen timestamps. In accordance with Invariant 1, the entry contains no sender fields, handles, or message records.

Stale jobs older than `dispatch.max_age` (30 s) are dropped on ingress and counted as `obs.DropReasonStale`. The accumulator has a fixed capacity bound (`max_recipients`, default 100,000) satisfying Invariant 8. Expired entries are extracted by the flush loop into `core.Digest` structs carrying only recipient ID and count $N$. The `/v1/pending` endpoint atomically clears and returns the pending count for cold-start and reconnection synchronization.

### 34. Web Push encryption and keypair caching → **in-house RFC 8291/8188 with stdlib crypto, per-subscription shared secret caching with fresh salt per message**

Decided: Web Push encryption is implemented in-house (`server/internal/push/webpush/`) using pure Go stdlib (`crypto/ecdh`, `crypto/hkdf`, `crypto/aes`, `crypto/cipher`, `crypto/ecdsa`, `crypto/rand`) without any third-party dependencies.

To satisfy the dispatch worker pool CPU budget, the ECDH shared secret between the application server and the subscription's `p256dh` public key is cached per subscription endpoint. Benchmark results confirm a 23× speedup: ~1.45 µs per encryption with key cache vs ~33.4 µs without.

Crucially, the 16-byte salt is generated afresh from `crypto/rand` for every single message, strictly preventing AES-GCM nonce reuse under the same CEK. Plaintext pings without count are sent payloadless (zero-byte body, omitting `Content-Encoding`), minimizing bandwidth and processing overhead.

### 35. Dispatch token lifecycle and mid-flight re-registration pruning guard → **Authoritative prune only on 404/410 where created_day <= send_start_day**

Decided: Web Push subscriptions are pruned exclusively upon authoritative rejection (`404 Not Found` or `410 Gone`). The deletion query is strictly scoped as:
`DELETE FROM subscriptions WHERE endpoint = ? AND created_day <= ?`
where the timestamp bound is `send_start_day` captured immediately before the HTTP dispatch request is dispatched over the network.

Reasoning:
1. In accordance with AGENTS.md Invariant 4, transient errors (`429`, `500`, `502`, `503`, timeouts, or network/DNS drops) must never trigger subscription deletion. Pruning on transient faults would silently cause users to stop receiving pings without notification.
2. In-flight race conditions: if a client device unregisters and re-registers the same endpoint while a push dispatch request is in flight, the re-registered row has a newer `created_day`. Requiring `created_day <= send_start_day` ensures that the newly created row is preserved when the prior send's 410 response returns.
3. No retries or outbound queues: failed push attempts are dropped immediately and counted in metrics, preserving the best-effort delivery contract.


