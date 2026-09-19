# Roadmap

Ordering principle: **the abuse controls ship with the first send, not after.**
Every phase below is gated on that. The overlay, the badges and the groups are
all amplifiers, and an amplifier on top of an undefended primitive is how this
category of product dies.

## Phase 0 — Skeleton

* Repo layout, `AGENTS.md`, CI (build, vet, staticcheck, race tests), Dockerfile,
  `docker-compose.yml`.
* Go server that boots, opens SQLite in WAL, serves `/healthz` and `/metrics`.
* Schema migrations (plain SQL files, applied in order, no framework).

## Phase 1 — The primitive, defended

The minimum shippable thing, and it is not small, because "defended" is the
product.

* Accounts: signup with PoW, account key, Argon2id, sessions, export, delete.
* Handles: create, label, pause, burn. Region prefix. LRU over SQLite.
* `POST /v1/ping/{handle}` → 202 with the full ingress chain: auth, PoW,
  token buckets, pair Bloom filter, bounded non-blocking enqueue.
* Coalescer: accumulator + time wheel, digest policy, quiet hours, automatic
  escalation.
* Web Push dispatch (payloadless default, encrypted `{n}` for digests),
  subscription pruning with the re-registration race handled.
* SSE hub with heartbeats, backoff and idle demotion.
* Anomaly detection, adaptive PoW, report-abuse → top-sender mute.
* Metrics and the no-identifier logging rules.
* **Clients:** web app (send + receive + settings), Chrome and Firefox
  extensions, landing page, QR.
* **Load test before anything else ships.** Sustained 1,200 RPS and a 10k burst
  on the target VPS; measure ingress p99, Web Push CPU, SSE memory per
  connection. The capacity budget in [ARCHITECTURE.md](ARCHITECTURE.md) is
  arithmetic until this exists.

**Exit criteria:** a hostile person with a botnet and a published handle cannot
make the recipient's day worse than one digest notification, and cannot make the
box fall over.

## Phase 2 — Reach

* Desktop app (Tauri): tray, native notifications, autostart, per-handle view.
* TUI (Bubble Tea): single static binary, SSE, `halp send <handle>`.
* Safari support — resolve the extension-vs-web-push question first.
* Aliases.
* Groups: invites, roster, group-scoped handles, admin, minimum group size.
* Badges (SVG + optional public counter), rich link previews.

## Phase 3 — Amplifiers

* OBS overlay with revocable tokens; streamer handle presets.
* Twitch/YouTube chat bot as a thin API client.
* Second region, if warranted, exercising the prefix routing path.
* Public transparency report.

## Phase 4 — Mobile, if feasible

Deliberately last and explicitly conditional. Decide with real data from phases
1–3, and settle first:

* App-store viability for a contentless unsolicited-notification app (Apple 4.2
  minimum functionality; both stores' push-spam rules). This is a go/no-go, not
  a detail.
* Whether to extend Tauri v2 to mobile or write thin native shells.
* APNs and FCM integration, quotas, and the (very different) token-pruning
  feedback paths.

`mobile/` exists as a placeholder until then.

## Non-goals, permanently

Messages, replies, reactions, profiles, avatars, followers, feeds,
leaderboards, read receipts, "who appreciated me", premium de-anonymisation,
ads, analytics SDKs.
