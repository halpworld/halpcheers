# Contributing to Halp

Thank you for your interest in contributing to Halp!

Halp is an ultra-lightweight, privacy-first notification utility with exactly one mission:
> **Send a person an anonymous "someone appreciates you" ping. Nothing else, ever.**

To keep Halp secure, fast, and mathematically impossible to weaponize, all development follows a **documentation-first, invariant-driven workflow**. Before opening a pull request or proposing changes, please read this guide and [`AGENTS.md`](AGENTS.md).

---

## 1. Documentation-First Architecture

In this repository, **[`docs/`](docs/) is the single source of truth**:
* Every feature, protocol, API endpoint, UI state, and security boundary is specified in [`docs/`](docs/) before code is written.
* **Silent divergence is strictly forbidden.** If your implementation must deviate from a specification in [`docs/`](docs/), update the corresponding document in the **same pull request** and detail your rationale in the PR description.
* All user-facing copy, button states, and screen transitions are specified in [`docs/UI.md`](docs/UI.md). Clients do not invent their own copy or states.

---

## 2. The 9 Non-Negotiable Invariants

Every contribution must preserve the 9 architectural invariants defined in [`AGENTS.md`](AGENTS.md):

1. **No message records:** No `messages`, `pings`, `history`, `events`, or `audit_log` tables in SQLite. No log line or metric label records a ping. The sole disk exception is the day-granular `blocks` table for user safety.
2. **Never make synchronous push calls in the HTTP handler:** `POST /v1/ping/{handle}` validates, enqueues to a bounded channel in ~100 ns, and returns `202 Accepted` in `< 3 ms`. It never awaits push services, disk writes, or DNS lookups.
3. **No heavy frameworks:** Pure Go stdlib + `chi` router + `modernc.org/sqlite` + Prometheus client. Hand-written SQL, no ORMs, no reflection-based DI. Web Push encryption is implemented in-house (`crypto/ecdh`, `crypto/aes`) against RFC 8291/8188.
4. **Token lifecycle hygiene:** Prune subscriptions on authoritative rejection only (`404`/`410`), strictly scoped to `created_day <= send_start_day`. Never prune on transient errors.
5. **Abuse prevention ships with the feature, not after it:** Every new reachability surface lands with its rate limits, token buckets, and revocation paths.
6. **Never reveal the sender:** Anonymity is guaranteed mathematically. Senders are never revealed in UI, API responses, error bodies, GDPR exports, or timing channels.
7. **Enforcement is invisible to the sender:** Valid, rate-limited, blocked, paused, and unknown targets all return byte-identical `202 Accepted` responses with uniform timing (no existence oracles).
8. **Fixed-size allocations for anything keyed by user input:** In-memory LRUs, Bloom filters, sketches, and channels are bounded at startup to prevent memory exhaustion attacks.
9. **No identifiers in logs or metrics:** Handles, account IDs, aliases, IPs, subscription URLs, and user agents are forbidden in log lines, metric labels, and client errors.

### Instant Rejection Triggers

A pull request will be **immediately rejected** if it introduces any of the following:
* A `messages`, `pings`, `events`, or `audit_log` table under any name.
* A push dispatch, database write, or DNS lookup inside the ping HTTP handler.
* An unbounded `map` or collection keyed by handle, alias, or IP.
* A different response body, status code, or measurable timing variance for a rejected ping versus an accepted one.
* An endpoint or helper that answers "does this handle/alias exist?" (including autocomplete or validation helpers).
* An ORM, dependency injection framework, or third-party Web Push client library.
* A log line or metric label containing a handle, alias, account ID, IP, endpoint URL, or user agent.
* An analytics SDK, font CDN, or tracking beacon in any client bundle.
* Retries or disk persistence added to ping delivery (drops under load are expected and counted via metrics).

### Permanent Non-Goals

Do not submit issues or pull requests proposing:
* Text messages, attachments, media, or custom notes.
* Reactions, replies, read receipts, or threaded conversations.
* Profiles, avatars, bios, friend lists, or follower counts.
* Public leaderboards, counters, scoreboards, or activity feeds.
* Sender reveal mechanisms (even optional, paid, or mutual).
* Third-party analytics or commercial advertising SDKs.

---

## 3. Repository Ownership & Frozen Contracts

To support parallel development without merge conflicts, code is organized with strict ownership boundaries:

| Path | Component | Status |
| --- | --- | --- |
| `server/internal/core/` | Shared core domain types and interfaces | **FROZEN** (Requires RFC to modify) |
| `server/internal/http/router.go` | HTTP route mounting and middleware chain | **FROZEN** (Routes mounted to stubs) |
| `server/internal/store/migrations/` | Sequential SQL migration files | Append-only (`0001_init.sql`, etc.) |
| `server/internal/config/` | Centralized tunables and runtime configuration | Frozen contract |
| `server/internal/auth/` | Accounts, Argon2id auth, sessions | Accounts module |
| `server/internal/auth/pow/` | Stateless Hashcash challenge grinding & verification | PoW module |
| `server/internal/region/` | Handles, aliases, in-process LRU resolver | Handles module |
| `server/internal/guard/` | Bloom cascades, Count-Min sketch, pair rate limits | Guard module |
| `server/internal/coalesce/` | Ingress channel queue, accumulator, digest time wheel | Ingress module |
| `server/internal/push/` | RFC 8291/8188 Web Push encryption & HTTP worker pool | Push module |
| `server/internal/stream/` | Real-time SSE hub, connection ceiling, idle demotion | Stream module |
| `server/internal/obs/` | Metrics registry & Invariant 9 logging helpers | Observability module |
| `tui/` | Bubble Tea Elm-architecture terminal client & CLI | TUI module |
| `web/core/` | TypeScript client SDK, WebCrypto key derivations, PoW worker | Web SDK module |
| `web/app/` | Vanilla TypeScript PWA and service worker | Web App module |
| `extension/` | Manifest V3 browser extension for Chrome & Firefox | Extension module |
| `desktop/` | Tauri v2 desktop application for macOS, Windows, Linux | Desktop module |
| `loadtest/` | Automated capacity benchmark and ECDF timing harness | Load test module |
| `redteam/` | End-to-end adversarial verification suite | Release gate |

---

## 4. Development Environment & Running Tests

### Prerequisites
* **Go**: `1.26+` (`CGO_ENABLED=0`)
* **Node.js**: `22+` (for `web/` and `extension/`)
* **Rust & Cargo**: Latest stable (for `desktop/` Tauri builds)
* **Docker & Compose**: For full-stack container verification

### Go Test Suite & Race Detection

Halp uses a multi-module workspace defined in `go.work`. Run the full test suite across all modules with Go's race detector:

```bash
# Run all Go tests under race detector
go test -race ./server/... ./tui/... ./redteam/... ./loadtest/...
```

### Static Analysis: Invariant 9 AST Checker

To ensure zero identifiers leak into logs or metric labels, an automated AST analyzer runs in CI:

```bash
# Check server code for Invariant 9 violations
go run ./server/cmd/check-invariant9 ./server

# Check TUI code
go run ./server/cmd/check-invariant9 ./tui
```

> [!IMPORTANT]
> **Why `fmt.Sprintf` triggers Invariant 9:**
> The AST analyzer checks all logging, formatting, and string construction calls. Passing identifiers (`handle`, `accountID`, `alias`, `ip`, `endpoint`, `userAgent`) to `fmt.Sprintf` is flagged because formatting calls frequently end up in logs or error traces. Use direct string concatenation or dedicated URL builders instead.

### Red-Team Adversarial Suite

Run the full end-to-end red-team suite that validates all 9 invariants against simulated attacks (harassment botnets, de-anonymization probes, enumeration scans, and resource exhaustion):

```bash
go test -v -race ./redteam/...
```

### Capacity & Latency Load Test

Validate the server's `< 3 ms` ingress latency ceiling and memory limits under load:

```bash
# Sustained 1,200 RPS ingress benchmark
go run ./loadtest -target http://localhost:8080 -rps 1200
```

### Frontend Workspaces

```bash
# Web Core TypeScript SDK
npm --prefix web/core ci
npm --prefix web/core run build
npm --prefix web/core test

# Web App PWA
npm --prefix web/app ci
npm --prefix web/app run build
npm --prefix web/app test

# Browser Extension
npm --prefix extension ci
npm --prefix extension run build
npm --prefix extension test

# Desktop (Tauri)
cd desktop/src-tauri && cargo test
```

---

## 5. Logging Architectural Decisions

All architectural choices, tradeoffs, and protocol conventions are preserved in [`docs/OPEN-QUESTIONS.md`](docs/OPEN-QUESTIONS.md) under `## Decided`.

When introducing a decision or non-trivial design trade-off:
1. Claim a decision number by noting it on the corresponding GitHub issue.
2. In the same pull request as your code, append your decision to the `## Decided` section in [`docs/OPEN-QUESTIONS.md`](docs/OPEN-QUESTIONS.md) using the standard format:
   ```markdown
   ### <N>. Decision Title → **Short Answer in Bold**

   Detailed paragraphs explaining what was decided, what it costs, the alternatives
   considered, why they were rejected, and where the implementation landed.
   ```
3. Never delete existing decisions; if a decision is superseded, append a new entry explaining why.

---

## 6. Pull Request Checklist

Before submitting a PR, verify:
- [ ] Code strictly honors the 9 Non-Negotiable Invariants.
- [ ] `docs/` is updated if any protocol, route, or specification changed.
- [ ] `go test -race ./server/... ./tui/... ./redteam/... ./loadtest/...` passes cleanly.
- [ ] `go run ./server/cmd/check-invariant9 ./server` reports zero violations.
- [ ] No new external dependencies on the hot path without explicit justification.
- [ ] Decisions are logged in [`docs/OPEN-QUESTIONS.md`](docs/OPEN-QUESTIONS.md).
