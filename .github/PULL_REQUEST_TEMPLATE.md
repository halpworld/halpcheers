## Summary & Linked Issue

Closes #<!-- Link to issue here: e.g. Closes #123 -->

<!-- Brief 2-3 sentence summary of the change, the rationale, and the track ownership. -->

---

## Track & Scope Boundaries

- [ ] **Track Ownership**: The changes are strictly confined to the directory/track owned by this issue per [`docs/AGENT-WORKFLOW.md`](docs/AGENT-WORKFLOW.md) §2.
- [ ] **Frozen Contracts**: `server/internal/core/` and `server/internal/http/router.go` are completely untouched (`git diff --exit-code server/internal/core server/internal/http/router.go` returns 0).

---

## Documentation & Decisions

- [ ] **Specification Parity**: The implementation matches the governing doc in [`docs/`](docs/) or the doc is updated in this same PR with rationale provided.
- [ ] **Decision Logged**: If this change involved an architectural, protocol, or cryptographic trade-off, a numbered decision has been claimed and appended to [`docs/OPEN-QUESTIONS.md`](docs/OPEN-QUESTIONS.md) §Decided.
- [ ] **Copy & UX Compliance**: Any user-facing copy matches [`docs/UI.md`](docs/UI.md) verbatim.

---

## Dependencies (Invariant 3)

- [ ] **No Heavy Frameworks**: No ORM, reflection-based DI/validation, or external Web Push libraries.
- [ ] **Dependency Justification**: Did this PR add any new dependency?
  - *If Yes, provide justification for hot-path / security necessity:*

---

## Invariant Compliance Checklist

- [ ] **Invariant 1: No message records.**
  - Zero `messages`, `pings`, `history`, `events`, or `audit_log` tables/files.
  - Zero log lines or metric labels recording a ping or sender/recipient pair.
  - Only exception: user-initiated `blocks` table (expires in 12 months).
- [ ] **Invariant 2: Never make a synchronous push call in HTTP handler.**
  - `POST /v1/ping/{handle}` validates, enqueues to bounded queue, and returns `202 Accepted` in < 3 ms.
  - Zero push services, disk writes, or DNS queries awaited in the ping handler.
- [ ] **Invariant 3: No heavy frameworks.**
  - Minimal standard dependencies (`chi`, `modernc.org/sqlite`, `client_golang`). In-house RFC 8291/8188 Web Push encryption with stdlib crypto.
- [ ] **Invariant 4: Token lifecycle hygiene.**
  - Push subscriptions pruned *only* on authoritative rejection (`404` / `410`).
  - Scoped by exact endpoint **and** `created_day <= send_start_day` to protect mid-flight re-registrations.
  - Never pruned on transient errors (`429`, `5xx`, network drops).
- [ ] **Invariant 5: Abuse prevention ships with the feature.**
  - New reach mechanisms ship with rate limits, Bloom filter cascades, and revocation paths.
- [ ] **Invariant 6: Never reveal the sender.**
  - Zero sender identities in UI, API responses, error bodies, feature flags, or timing/count inference.
- [ ] **Invariant 7: Enforcement is invisible to the sender.**
  - Accepted, rate-limited, deduped, and blocked sends return uniform `202 Accepted` with identical timing.
  - Zero existence oracles (`/h/{handle}` and `/@{alias}` serve byte-identical markup and headers).
- [ ] **Invariant 8: Fixed-size allocations for anything keyed by user input.**
  - All maps, caches, filters, and sketches are bounded from configuration at startup.
- [ ] **Invariant 9: No identifiers in logs or metrics.**
  - Zero handles, account IDs, aliases, IPs, endpoint URLs, or user agents in logs or metric labels.
  - AST checker passes: `go run ./server/cmd/check-invariant9 ./server` and `./tui`.

---

## Verification & Testing

- [ ] `go test -race ./server/... ./tui/... ./redteam/... ./loadtest/...` passes.
- [ ] `npm test` passes across `web/core`, `web/app`, `extension`, and `desktop`.
- [ ] Zero external origins, CDNs, or telemetry/analytics SDKs verified in client bundles.
- [ ] Negative failure directions and edge cases covered with regression tests.
