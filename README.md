# Halp

**Someone appreciates you.**

Halp is an ultra-lightweight notification utility with exactly one function: let a
person send another person an anonymous appreciation ping. No text, no images, no
reason, no sender identity. One fixed idea, delivered.

* **Zero payload.** The notification content is constant. There is nothing to write.
* **Anonymous to the recipient.** The recipient never learns who appreciated them.
* **No message history.** Nothing is stored about a ping once it is dispatched.
* **Small.** The server is a single Go binary plus one SQLite file, in one EU
  region. It is meant to run comfortably on a $10–20/month VPS.

## Status

**Phase 1 (Core Loop, Security Hardening, Load & Red-Team Suites, Release Pipeline)** and **Phase 2 — Reach (Client-Encrypted Contacts Sync, Groups, Go TUI Client, Browser Extension, and Desktop App Polish)** are fully implemented and verified against all invariants and exit criteria.

The full design and specification live in [`docs/`](docs/):

| Doc | What's in it |
| --- | --- |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | System shape, request path, capacity budget |
| [docs/IDENTITY.md](docs/IDENTITY.md) | Account keys, handles, aliases |
| [docs/ABUSE.md](docs/ABUSE.md) | The defence stack — the most important document here |
| [docs/DELIVERY.md](docs/DELIVERY.md) | Transports, coalescing, digest policy |
| [docs/DISCOVERY.md](docs/DISCOVERY.md) | Finding people, contacts, and why there is no lookup endpoint |
| [docs/GROUPS.md](docs/GROUPS.md) | Teams and group-scoped handles |
| [docs/SHARING.md](docs/SHARING.md) | Links, QR, badges, live-stream overlays |
| [docs/PRIVACY.md](docs/PRIVACY.md) | GDPR posture and data inventory |
| [docs/API.md](docs/API.md) | HTTP surface and SQLite schema |
| [docs/UI.md](docs/UI.md) | UI and UX spec — copy, states, what no screen may show |
| [docs/ROADMAP.md](docs/ROADMAP.md) | Phasing and milestones |
| [docs/AGENT-WORKFLOW.md](docs/AGENT-WORKFLOW.md) | How parallel agents split the work and log decisions |
| [docs/OPEN-QUESTIONS.md](docs/OPEN-QUESTIONS.md) | Decisions made, and the ones still outstanding |

Engineering rules that must not be violated are in [AGENTS.md](AGENTS.md).

## Quickstart & Verification

Halp is written in pure Go (`CGO_ENABLED=0`) with zero external runtime C dependencies.

### Running the Server

```bash
# Start the HTTP server with SQLite (listens on :8080 by default)
go run ./server/cmd/halpd
```

### Running the TUI Client

```bash
# Launch interactive Bubble Tea TUI
go run ./tui/cmd/halp

# Send an anonymous ping via CLI
go run ./tui/cmd/halp send <handle>
```

### Verification & Test Suites

```bash
# Run unit and integration tests across modules with Go race detector
go test -race ./server/... ./tui/... ./redteam/...
# (or within each Go module directory: go test -race ./...)

# Run AST static analyzer enforcing Invariant 9 (zero identifier leaks in logs/formatters)
go run ./server/cmd/check-invariant9 ./server

# Run hostile red-team adversarial suite (verifies all 9 invariants and abuse scenarios)
go test -v -race ./redteam/...

# Execute capacity and throughput load test harness
go run ./loadtest -target http://localhost:8080 -rps 1200
```

## Platforms

Phase 1 and Phase 2 platforms are implemented: **web, browser extensions (Chrome / Firefox), desktop app (macOS / Windows / Linux via Tauri), and terminal (Go TUI)**.

There is no Safari extension: the Push API inside Safari web extensions was the
weakest assumption in the plan, so Safari users get the web app over Safari Web
Push plus the desktop app instead. Mobile is phase 4, pending a feasibility
call — the repository reserves `mobile/` for it.

## Licence

See [LICENSE](LICENSE).
