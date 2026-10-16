# Working agreement for parallel agents

Phase 1 is built by several agents working at the same time, mostly without
talking to each other. That works only if the boundaries are explicit, so this
file is the contract: what you may touch, what you must read first, how you land
work, and where decisions go.

Read this once before your first commit on an issue, then read the issue.

## 1. Read order

1. [AGENTS.md](../AGENTS.md) — the nine invariants. Non-negotiable, and several
   of them are easy to violate by accident.
2. Your issue. It names the docs that govern it. They are not background
   reading; they are the specification.
3. [UI.md](UI.md) for anything a human looks at — copy included.
4. [API.md](API.md) for anything on the wire, [ARCHITECTURE.md](ARCHITECTURE.md)
   for shape, budgets and configuration.

If the docs and this file disagree with your instincts, the docs win. If the
docs are wrong, **change the doc in the same PR** and explain why in the PR body
(AGENTS.md, "Before you change the spec"). Silent divergence is the failure mode
this whole project is arranged to prevent.

## 2. Ownership map

One agent owns a path at a time. Do not edit outside your column, even for an
obvious one-line fix — open an issue instead, or say so in your PR body and let
the owner do it.

| Path | Track |
| --- | --- |
| `server/cmd/halpd/`, `server/internal/config/`, `server/internal/core/`, CI, Dockerfiles, `docker-compose.yml` | Foundation |
| `server/internal/store/` (incl. `migrations/`) | Foundation, then Storage |
| `server/internal/http/router.go` | **Foundation only. Frozen after wave 0.** |
| `server/internal/http/<group>.go` | the track that owns that endpoint group |
| `server/internal/auth/` | Accounts |
| `server/internal/auth/pow/` | Proof-of-work |
| `server/internal/region/` | Handles |
| `server/internal/guard/` | Guard |
| `server/internal/coalesce/` | Ingress & coalescing |
| `server/internal/push/` | Web Push |
| `server/internal/stream/` | SSE |
| `server/internal/obs/` | Observability |
| `web/` | Web core, then Web UI (split by directory, named in the issues) |
| `extension/` | Extension |
| `desktop/` | Desktop |
| `docs/` | everyone, append-only in practice — see §6 |

## 3. The contract wave comes first

Nothing parallel starts until the foundation issues land, because they define
the surfaces everyone else compiles against:

* **`server/internal/core/`** — the shared types and the small interfaces the
  tracks implement for each other (`PingJob`, `Digest`, `Policy`, `HandleKind`,
  `Enqueuer`, `Resolver`, `Guard`). Plain types, no logic, no dependencies.
  Once it is merged it is **frozen**: a change needs a comment on the Phase 1
  tracker issue naming every track it breaks, and a PR of its own.
* **`server/internal/http/router.go`** — every phase-1 route from
  [API.md](API.md), mounted to a `notImplemented` stub. Feature tracks replace
  their stub inside their own `http/<group>.go` file. **Nobody edits
  `router.go` again**; this is what stops fifteen agents from rewriting the same
  forty lines.
* **`server/internal/store/migrations/0001_init.sql`** — the complete phase-1
  schema from [API.md](API.md), in one file. It is written whole precisely so
  that parallel tracks never race to claim migration number `0002`.
* **`server/internal/config/`** — the full tunables table from
  [ARCHITECTURE.md](ARCHITECTURE.md), including the startup-only vs
  `SIGHUP`-reloadable split. Every key exists from day one even where its
  consumer does not, so no track has to touch config to add its own.

Later migrations claim their number by commenting on the tracker issue before
the PR opens. First comment wins; the loser renumbers.

## 4. Landing work

* Branch: `agent/<issue-number>-<short-slug>`, cut from `main`.
* One PR per issue. If the issue is too big for one reviewable PR, split it into
  sub-issues rather than opening a 3,000-line PR.
* PR body states: the issue, the invariants the change touches and how it
  honours them, any doc changed and why, and any new dependency with its
  justification (invariant 3).
* Keep `main` green. `go build ./... && go vet ./... && staticcheck ./... &&
  go test -race ./...` must pass before you ask for review.

## 5. Definition of done

An issue is done when all of these are true, not when the code works:

- [ ] The behaviour matches the doc the issue names, or the doc changed in the
      same PR.
- [ ] Tests exist for the failure directions the docs call out, not only the
      happy path. Every track has at least one test that would catch an
      invariant violation.
- [ ] No identifier reaches a log line, a metric label or an error body
      (invariant 9). Grep your own diff for handle, account id, endpoint URL,
      IP and user agent.
- [ ] Nothing durable records a ping (invariant 1). If your change adds a table,
      a file, or a log with sender **and** recipient in it, it is wrong unless
      it is the `blocks` table.
- [ ] No synchronous network or disk call was added to the `POST /v1/ping`
      handler (invariant 2).
- [ ] Anything keyed by user input is a fixed-size allocation sized from config
      (invariant 8).
- [ ] Any new way to reach a user ships with its rate limit and its revocation
      path (invariant 5).
- [ ] Decisions taken along the way are logged (§6).

## 6. Log every decision

[OPEN-QUESTIONS.md](OPEN-QUESTIONS.md) is the decision log for this project and
there is no second one. No `adr/` directory, no decisions buried in PR threads,
no "we discussed it in the issue".

When you make a call that a future reader would otherwise have to reverse
engineer — a default, a trade-off, a thing you deliberately did *not* build, a
place the docs were ambiguous and you picked — append an entry to the
`## Decided` section, **in the same PR as the code**:

```markdown
### 27. Short question → **the answer, in bold**

Two or three paragraphs: what was decided, what it costs, what the alternative
was and why it lost, and where it landed in the docs.
```

Rules:

* Claim your number by commenting on the Phase 1 tracker issue before you open
  the PR. First comment wins; the loser renumbers on rebase.
* Never delete an entry. Superseded decisions get a follow-up entry that says
  so, because the reasoning is the valuable part.
* If the decision changes a doc, change that doc in the same PR and link the
  entry from it, the way the existing entries do.
* A decision small enough to be obvious still gets logged if you had to think
  about it for more than a minute. The cost of an unnecessary entry is four
  lines; the cost of a missing one is an afternoon.

## 7. Things that will get a PR rejected

Not style notes — these are the ones that cost a rewrite:

* A `messages`, `pings`, `events` or `audit_log` table, under any name.
* A push call, a disk write or a DNS lookup inside the ping handler.
* An unbounded `map[string]...` keyed by handle, alias or IP.
* A different response body, status code or measurable latency for a rejected
  send than for an accepted one.
* An endpoint that answers "does this handle/alias exist?", including as a
  validation helper, an autocomplete, or a 404 that differs from a 200.
* An ORM, a DI container, a reflection-based config or validation library, or a
  Web Push dependency.
* A log line or metric label containing a handle, account id, alias, IP,
  endpoint URL or user agent.
* An analytics SDK, a font CDN or an error-reporting SDK in a client.
* Retries or persistence added to ping delivery to "fix" drops. Drops are the
  spec; count them instead.

## 8. Current implementation state & agent handoff guide

This section is maintained as work progresses so any agent picking up the
repository can orient immediately without re-auditing the tree.

### Completed phases

* **Phase 0 — Skeleton** *(Complete)*: Repo layout, `AGENTS.md`, CI workflows
  (`.github/workflows/ci.yml`, `.github/workflows/release.yml`), `Dockerfile`,
  `docker-compose.yml`, SQLite WAL migrations (`0001_init.sql`), `/healthz` and
  `/metrics`.
* **Phase 1 — The primitive, defended** *(Complete)*:
  * **Server (`server/`)**: Account signup/login/export/delete with Argon2id and
    stateless HMAC-SHA256 PoW (`server/internal/auth/`); handle lifecycle +
    region prefix + bounded 100k LRU cache (`server/internal/region/`);
    `POST /v1/ping/{handle}` sub-3 ms non-blocking ingress (`server/internal/http/ping.go`);
    in-memory guard (`server/internal/guard/`) with rotating pair Bloom cascade,
    count-min sketch, bounded 10k-entry maps (`boundedMap`), and O(1) in-memory
    `sqliteBlockStore` cache; coalescer + 60-slot time wheel
    (`server/internal/coalesce/`); RFC 8291/8188 Web Push encryption + VAPID ES256
    (`server/internal/push/`); SSE hub with 5-conn/account cap and idle demotion
    (`server/internal/stream/`); Prometheus metrics + zero-identifier AST linter
    (`server/cmd/check-invariant9`).
  * **Clients**: `@halp/core` (`web/core/`), Web App + Service Worker (`web/app/`),
    Landing Page (`web/landing/`), Chrome & Firefox MV3 extensions (`extension/`),
    and Tauri v2 Desktop App (`desktop/`).
  * **Verification**: Load test harness (`loadtest/`) and Phase 1 red-team suite
    (`redteam/redteam_test.go`).
* **Phase 2 — Reach** *(Complete — commit `81293a2`, Decisions #47–#50)*:
  * **Contacts Sync (`server/internal/http/contacts.go`, `web/core/src/contacts.ts`)**:
    `GET/PUT/DELETE /v1/contacts` with `If-Match` optimistic concurrency (`412` on
    conflict), 4 KiB (`4096`-byte) boundary padding before AES-256-GCM encryption
    under `HKDF-SHA256(account_key, info="halp/contacts/v1")`, 64 KiB max ceiling,
    and CRDT set-union conflict merge where `deleted: true` tombstones win.
  * **Groups (`server/internal/http/groups.go`, `server/internal/region/resolver.go`)**:
    All 9 `/v1/groups*` endpoints, `groups.min_size = 5` floor disabling group
    handle resolution below 5 members while returning uniform `202 Accepted`,
    O(1) bounded in-memory `memberCache` and `groupItems` index in `Resolver`,
    automatic handle burn on leave/kick, owner succession to longest-standing
    member, and 10 distinct group members/day sender spray cap via `PairCascade`.
  * **Go TUI Client (`tui/cmd/halp/`)**: Pure Go (`CGO_ENABLED=0`), zero external
    dependencies, Elm-architecture (`Init`/`Update`/`View`) state machine, SSE
    streaming with `event: demote` fallback to `GET /v1/pending`, optional
    terminal bell (`\a`), and `halp send <handle>` with context-aware 300 ms
    latency floor.
  * **Desktop App Polish (`desktop/`)**: Launch-at-login (`get_autostart` /
    `set_autostart`, default `false` per `docs/UI.md` §9), per-handle view with
    verbatim burn prompt, and Tauri updater endpoint configuration.
  * **Verification**: `server/internal/http/phase2_test.go` and
    `redteam/phase2_redteam_test.go`.
* **Phase 3 — Amplifiers** *(Complete — commit `272c9cc`, Decisions #51–#53)*:
  * **OBS Overlay & Streamer Handle Presets (`server/internal/http/handles.go`,
    `server/internal/http/public.go`, `web/app/src/main.ts`)**: Creating a
    `stream` handle (`POST /v1/handles` with `kind: "stream"`) provisions default
    streamer policy (`digest_window_s: 60`, `max_per_hour: 60`, `min_count: 1`,
    32-hex `overlay_token`, `overlay_style: "pop-in"`). `PATCH /v1/handles/{handle}`
    supports `rotate_overlay_token: true` and `revoke_overlay_token: true` without
    burning the public handle. `GET /overlay/{token}` serves zero-external-origin
    transparent OBS Browser Source HTML (`pop-in`, `meter`, `corner`) and live SSE
    (`?stream=1`) with constant-time token comparison and a 2s revocation
    watchdog.
  * **Twitch / YouTube Thin API Chat Bot (`tui/internal/bot/bot.go`,
    `tui/cmd/halp-bot/main.go`)**: Pure Go (`CGO_ENABLED=0`), IRCv3 tag parsing,
    `!halp`/`!appreciate`/`!cheers` commands, 15s channel cooldown and 60s user
    cooldown backed by a bounded 10,000-entry map (Invariant 8), and lock-free
    optional `SendPing` forwarding.
  * **Public Transparency Report (`server/internal/obs/transparency.go`,
    `docs/TRANSPARENCY.md`)**: Aggregate, zero-identifier transparency report
    generator and warrant canary documentation.
  * **Verification**: `server/internal/http/phase3_test.go`, `tui/bot_test.go`,
    and `redteam/phase3_redteam_test.go`.
* **Production Readiness & Edge Hardening** *(Complete — Decision #54)*:
  * **Web Push VAPID & RFC 8291/8188 Dispatch (`server/internal/push/`)**:
    `DefaultSender` signs every Web Push request with ES256 VAPID, sends
    payloadless bodies for `DigestCount == 1`, encrypts `{"n":count}` for
    `DigestCount > 1`, retries once on `429`/`5xx` honouring `Retry-After`,
    bounds `KeypairCache` to `100,000` entries (Invariant 8), and evicts cached
    keys alongside SQLite rows (`created_day <= sendStartDay`) on `404`/`410`
    (Invariant 4).
  * **Full Subsystem Config & Graceful Drain (`server/internal/http/assemble.go`,
    `server/internal/http/middleware.go`, `server/cmd/halpd/main.go`)**: Wires all
    `cfg.Startup.*` and `cfg.Reloadable().*` settings (`guard`, `pow`, `coalesce`,
    `push`, `stream`), persists VAPID keys (`HALP_VAPID_KEY_FILE` with `0600`
    mode), exempts SSE routes from `TimeoutMiddleware` while preserving
    `http.Flusher` through `AccessLogMiddleware`, drains `Queue` +
    `Coalescer.FlushAll()` on `SIGTERM`, and runs a 6-hour maintenance ticker
    for expired `blocks` and ancient `subscriptions`.
  * **Edge Deployment & Runbook (`docker-compose.yml`, `Caddyfile`,
    `halp.toml.example`, `docs/OPERATIONS.md`)**: Loopback-only `127.0.0.1:8080`
    binding on `halpd`, Caddy TLS termination with `flush_interval -1` SSE
    streaming, `/metrics` `403` gate, and log scrubbing of IPs, `User-Agent`, and
    query strings (Invariant 9).

### Guardrails & tooling notes for future agents

1. **Frozen contracts**: `server/internal/core/` and `server/internal/http/router.go`
   remain strictly frozen (`git diff server/internal/core server/internal/http/router.go`
   must be empty).
2. **AST Invariant 9 checker (`server/cmd/check-invariant9`)**: Scans Go ASTs for
   any `fmt.Sprintf` or logger call whose arguments reference an identifier
   matching `handle`, `accountID`, `alias`, `ip`, `endpoint`, or `userAgent`.
   Always use string concatenation (`+`) when building URL paths or UI strings with
   those variable names, and run `go run ./server/cmd/check-invariant9 ./server`
   and `go run ./server/cmd/check-invariant9 ./tui` before committing.
3. **Verification suite**:
   ```bash
   go test -race ./server/... ./tui/... ./redteam/... ./loadtest/...
   go run ./server/cmd/check-invariant9 ./server
   go run ./server/cmd/check-invariant9 ./tui
   ```

### What remains on the roadmap

* **Phase 3 (Parked item)**: Second region (`docs/DISCOVERY.md`, Decision 22) —
  explicitly parked unless cross-region demand and a privacy review warrant
  changing the single-region (`eu-1`) guarantee.
* **Phase 4 — Mobile, if feasible (`mobile/`)**: Conditional on App Store / Play
  Store viability for a contentless appreciation-ping app, choice of Tauri v2
  mobile vs thin native shells, and APNs/FCM push dispatcher + token-pruning
  integration.

