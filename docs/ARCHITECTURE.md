# Architecture

## What changed from the first draft, and why

The original spec assumed a Flutter client and APNs/FCM delivery. Targeting web,
browser extensions, desktop and a TUI first changes three things:

1. **No APNs and no FCM in phase 1.** Delivery is Web Push (VAPID) for browsers
   and extensions, and a long-lived SSE stream for desktop and TUI clients.
2. **The server is no longer stateless.** SSE means we hold one open connection
   per online desktop/TUI client. Concurrent connections, not request rate, is
   now the binding constraint on a small box. This is the single biggest cost
   change and it is budgeted below.
3. **Flutter is dropped.** A TypeScript core serves the web app, the extensions
   and (via Tauri) the desktop app from one codebase; the TUI is Go and shares
   the server's client packages. Flutter would have meant a second UI codebase
   that covers none of the phase-1 targets better than this does.

## Shape

```
                         ┌───────────────────────────────────────────┐
  send  ──POST /v1/ping──▶│  Ingress (Go, chi)                        │
                         │   1. auth (bearer)      ~40 µs             │
                         │   2. proof-of-work      ~2 µs              │
                         │   3. token buckets      ~1 µs              │
                         │   4. pair dedupe filter ~1 µs              │
                         │   5. non-blocking enqueue                  │
                         │   ──▶ 202 Accepted in < 3 ms               │
                         └──────────────────┬────────────────────────┘
                                            │ chan pingJob (bounded)
                                            ▼
                         ┌───────────────────────────────────────────┐
                         │  Coalescer                                │
                         │   per-recipient accumulator + time wheel  │
                         │   collapses N pings ──▶ 1 digest          │
                         └──────────────────┬────────────────────────┘
                                            │ chan digest
                                            ▼
                         ┌───────────────────────────────────────────┐
                         │  Dispatch worker pool                     │
                         └────┬──────────────────────────┬───────────┘
                              │                          │
                              ▼                          ▼
                    Web Push (HTTP/2,           SSE fan-out to locally
                    VAPID, keep-alive)          attached desktop/TUI conns
                              │                          │
                              ▼                          ▼
                    Mozilla / Google /          Desktop tray, TUI,
                    Apple push services         OBS overlay
                              │
                              ▼
                    Browser + extension service workers

  SQLite (WAL) ──── accounts, handles, aliases, subscriptions, settings,
                    groups, blocks, aggregate counters.  Never pings.
                    + alias_directory: the one globally replicated table.

  Peer link  ─────── mTLS HTTP/2 to sibling regions: forward pings by handle
                     prefix, pull the alias claim log, read group rosters.
                     Off the hot path except the forward itself.
```

Everything on one box. One Go binary, one SQLite file, no Redis, no message
broker, no ORM.

## The request path in detail

**Ingress** does the minimum work needed to decide whether a ping is allowed,
then gets out of the way. Ordered cheapest-check-first so that a flood is
rejected before it costs anything:

| Step | Cost | Rejects |
| --- | --- | --- |
| Bearer token lookup (in-memory session map) | ~40 µs | unauthenticated senders |
| Proof-of-work verify (1 × SHA-256) | ~2 µs | unpriced floods |
| Sender token bucket (atomic, in-memory) | ~1 µs | one account sending too much |
| Recipient inbound bucket | ~1 µs | one target being hammered |
| Pair dedupe (rotating Bloom filter) | ~1 µs | repeat pings to the same person |
| Non-blocking channel send | ~100 ns | — |

No disk I/O, no network, no locks held across I/O. The handle → account
resolution comes from an in-process LRU over SQLite, so the hot path is pure
memory.

**Never** call a push service from the HTTP handler. The 202 is returned before
any delivery is attempted. This is invariant 2 in `AGENTS.md`.

## Loss policy

Pings are best-effort. This is an accepted, deliberate property, not a bug:

* **Queue full** → drop the job, increment `halp_pings_dropped_total{reason="queue_full"}`,
  still return `202`. The alternative (blocking) breaks the latency contract and
  the alternative (429) tells a well-behaved sender to retry into an overloaded
  box, which makes it worse.
* **Stale jobs** → `pingJob` carries `enqueuedAt`; a worker discards anything
  older than `dispatch.max_age` (default 30 s). Nobody wants yesterday's
  appreciation.
* **Shutdown** → on `SIGTERM` the listener stops accepting, the queue drains with
  a deadline (`shutdown.drain_timeout`, default 10 s), then the process exits.
  Routine deploys should not drop anything.
* **Crash** → the in-flight buffer is lost. Accepted.

Because the coalescer keeps a short-lived per-recipient counter (see
[DELIVERY.md](DELIVERY.md)), an offline recipient does not lose their pings for
the duration of the digest window — the counter *is* the buffer. It holds a
number, never a list of senders, so this does not violate invariant 1.

## Capacity budget

These are budgets to design and benchmark against, not measurements, and not
launch expectations. The 100M/day figure is a ceiling to avoid designing
ourselves out of.

**Ingress.** 100M pings/day ≈ 1,200 avg RPS with bursts to 10k. Trivial for Go:
the per-request work above is a few microseconds plus TLS. Budget one vCPU.

**Web Push crypto is the CPU risk.** RFC 8291 encryption is per-subscription
(ECDH P-256 + HKDF + AES-GCM), so identical content still costs a full key
agreement per recipient device. At a few thousand deliveries/sec that is real
work on 2–4 vCPU.

> **Mitigation: send payloadless pushes by default.** A push message with no body
> needs no encryption at all. The service worker already knows the only string it
> will ever display. We only attach an (encrypted) body when the digest count
> matters — i.e. `n > 1` — and even then it is `{"n":42}`. Coalescing therefore
> cuts *both* notification spam and crypto cost, and most deliveries end up free.
>
> Whether an ECDH keypair may be reused across messages to the same subscription
> (which would let us cache the shared secret) needs to be checked against
> RFC 8291 before relying on it. Tracked in [OPEN-QUESTIONS.md](OPEN-QUESTIONS.md).

**SSE connections are the memory risk.** Each open connection costs a goroutine
stack plus read/write buffers — budget ~12–20 KB with tuned buffers. That puts a
4 GB box somewhere around 50–100k concurrent desktop/TUI clients before memory,
not CPU, stops us. Also budget file descriptors (`ulimit -n`) and conntrack.
Mitigations, in order of preference:

1. Most users are browser users on Web Push and hold no connection at all.
2. Idle TUI/desktop clients back off to a long-poll after `idle.demote_after`.
3. Beyond one box, shard by handle region prefix onto a second box. Handles
   carry their region (see [IDENTITY.md](IDENTITY.md)) so routing needs no
   shared state.

**SQLite.** Reads are served from an in-process LRU and the OS page cache; writes
are rare (registration, handle churn, settings). WAL mode, `synchronous=NORMAL`,
one writer goroutine, `busy_timeout` set. A single indexed lookup from page cache
beats a Redis round-trip, which is why there is no Redis.

**Bloom filter.** Sized from a configured `expected_daily_pings` at ~2 bytes per
entry for a ≤0.1% false-positive rate, double-buffered across two 24 h windows.
At launch scale that is single-digit MB; at the 100M/day ceiling it is a few
hundred MB of fixed allocation. Over capacity it degrades gracefully — the
false-positive rate rises and a few legitimate pings are silently deduped, which
for this product is acceptable. Alert on the estimated fill ratio.

## Observability without records

Invariant 1 forbids per-message rows. It does **not** forbid aggregate counters
with no identity attached, and abuse defence is impossible without them.
Permitted: Prometheus counters/histograms (`pings_accepted_total`,
`pings_dropped_total{reason}`, `digests_sent_total`, `push_failures_total{code}`,
`pow_difficulty`, `sse_connections`, ingress latency histogram) and in-memory
TTL'd rate-limit state. Forbidden: anything that records *that a specific ping
happened between two specific parties*, in a database, a log line, or a metric
label. No handle, account ID, or IP address may appear as a metric label.

Access logs: status, method, route pattern, latency, size. No path parameters
(they contain handles), no query strings, no IPs, no user agents.

## Repository layout

```text
halp/
├── server/                  # Go backend — the only thing that must be deployed
│   ├── cmd/halpd/           # main
│   ├── internal/
│   │   ├── http/            # chi routes, handlers, middleware
│   │   ├── auth/            # account keys, sessions, proof-of-work
│   │   ├── guard/           # token buckets, bloom dedupe, anomaly detection
│   │   ├── coalesce/        # accumulator + time wheel
│   │   ├── push/            # RFC 8291 Web Push (stdlib crypto, no deps)
│   │   ├── stream/          # SSE hub
│   │   ├── store/           # SQLite, hand-written SQL, no ORM
│   │   ├── region/          # handle prefix routing + peer forwarding
│   │   └── directory/       # global alias replica + claim log sync
│   ├── go.mod
│   └── Dockerfile
├── web/                     # TypeScript core: web app + shared UI/logic
├── extension/               # MV3 (Chrome, Firefox) + Safari web-extension wrap
├── desktop/                 # Tauri shell — tray, native notifications
├── tui/                     # Go, Bubble Tea, single static binary
├── mobile/                  # placeholder, phase 2
├── docs/
├── docker-compose.yml
└── AGENTS.md
```

## Dependency policy

Go server: stdlib plus `chi`, `modernc.org/sqlite` or `mattn/go-sqlite3`, and a
Prometheus client. Web Push encryption is implemented in-house against
RFC 8291/8188 using `crypto/ecdh`, `crypto/hkdf` and `crypto/aes` — it is roughly
200 lines and avoids a supply-chain dependency on the delivery path. No ORM, no
reflection-heavy config or DI frameworks, no message broker.
