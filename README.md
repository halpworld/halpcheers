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

Design phase. No code yet. The full plan lives in [`docs/`](docs/), and
Phase 1 is broken into parallel tracks in the issue tracker — see
[docs/AGENT-WORKFLOW.md](docs/AGENT-WORKFLOW.md).

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

## Platforms

Phase 1 targets **web, browser extensions (Chrome / Firefox) and the desktop
app (macOS / Windows / Linux)**. The TUI follows in phase 2.

There is no Safari extension: the Push API inside Safari web extensions was the
weakest assumption in the plan, so Safari users get the web app over Safari Web
Push plus the desktop app instead. Mobile is phase 4, pending a feasibility
call — the repository reserves `mobile/` for it.

## Licence

See [LICENSE](LICENSE).
