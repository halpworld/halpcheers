# Changelog

All notable changes to Halp will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

---

## [Unreleased]

---

## [0.3.0] - 2026-10-17 — Phase 3: Amplifiers

### Added
* **OBS Browser Source Overlay (`/overlay/{token}`):**
  * Dedicated real-time streaming endpoint for content creators, broadcasters, and streamers.
  * Revocable overlay tokens with an active 2-second revocation watchdog loop.
  * Customizable overlay themes, visual animation triggers, and optional audible chimes.
* **Streamer Handle Presets:**
  * Preset configurations with elevated pair-limit windows (10 pings/24h) and stream-optimized coalescing.
* **Chat Bot Integration (`tui/cmd/halp-bot`):**
  * Thin API client connecting to Twitch and YouTube Live IRCv3 chat interfaces.
  * Allows viewers to trigger anonymous appreciation pings via chat commands while preserving sender anonymity.
* **Public Transparency Report & Warrant Canary:**
  * Published initial transparency statement and bi-annual warrant canary at [`docs/TRANSPARENCY.md`](docs/TRANSPARENCY.md).
  * Formal architectural documentation demonstrating the mathematical impossibility of retroactive data disclosure.
* **Production Edge Hardening:**
  * Hardened Caddy reverse proxy configuration (`Caddyfile`) enforcing Invariant 9 by stripping client IPs, User-Agent headers, and query parameters from access logs.
  * Reverse proxy shields `/metrics` from external inspection while allowing internal Prometheus scraping.
  * Configured `flush_interval -1` to preserve sub-millisecond SSE delivery latency for `/v1/stream` and `/overlay/*`.
  * Multi-stage production `web/Dockerfile` packaging the static web assets with Caddy.
* **Web Client Enhancements:**
  * Full Base64URL VAPID public key decoding in web and PWA clients.
  * Web Push service worker cold-start counter synchronization via `GET /v1/pending`.
* **Mobile Foundations:**
  * Architectural specification and token-pruning feedback pipeline design for Phase 4 APNs and FCM integration (`docs/ROADMAP.md`).

---

## [0.2.0] - 2026-10-16 — Phase 2: Reach

### Added
* **Client-Encrypted Contacts Sync (`GET/PUT/DELETE /v1/contacts`):**
  * Cryptographic split of the 16-digit account key via HKDF-SHA256 (`contacts_key = HKDF(account_key, "halp/contacts/v1")`).
  * Contacts sealed client-side using AES-256-GCM with fresh 12-byte random IVs.
  * 4 KiB (`4096`-byte) boundary padding applied prior to encryption to obscure contact count.
  * Strict 64 KiB ceiling (`contacts.max_bytes`) satisfying Invariant 8.
  * Multi-device synchronization using `If-Match` optimistic concurrency and deterministic CRDT set-union merging where `deleted: true` tombstones authoritatively win.
* **Groups & Team Support (`POST/GET/PATCH/DELETE /v1/groups*`):**
  * Group management with invite links, roster membership, and group-scoped handles.
  * Deanonymization floor: `groups.min_size = 5` disabling handle resolution if active roster falls below 5 members.
  * Membership verification on `POST /v1/ping/{handle}` rejecting non-member sends.
  * Automatic handle burn for group members upon leaving or being removed from a group.
  * Automatic group owner promotion to longest-standing member (`MIN(joined_day)`) upon owner departure.
  * Sender spray prevention: 10 distinct group members per day sender cap enforced via `PairCascade`.
* **Pure Go Terminal Client (`tui/cmd/halp`):**
  * Single static binary compiled with `CGO_ENABLED=0` and zero external C dependencies.
  * Bubble Tea Elm-architecture (`Init`, `Update`, `View`) state machine with Lip Gloss styling conforming to `docs/UI.md`.
  * Real-time SSE streaming with automatic keepalive pinging (`event: ka`) and graceful idle demotion (`event: demote`) fallback to `GET /v1/pending`.
  * CLI send subcommand (`halp send <handle>`) with Hashcash PoW grinding and an enforced 300 ms perceived latency floor.
  * Optional terminal bell alert (`\a`) on incoming appreciation pings.
* **Desktop App Polish (`desktop/`):**
  * Launch-at-login integration via native OS mechanisms, strictly opt-in and never default-on per `docs/UI.md` §9.
  * Dedicated per-handle view with create, pause, and burn actions.
  * Destructive handle burn requires typing the verbatim confirmation phrase (`burn <handle>`).
  * Secure Tauri auto-updater configuration with Ed25519 signature verification.
* **Guard & Block Cache Optimization:**
  * Bounded in-memory guard maps with fixed memory floors.
  * Thread-safe O(1) in-memory block cache for instantaneous abuse checks.

---

## [0.1.0] - 2026-09-22 — Phase 1: The Primitive, Defended

### Added
* **Ingress & Defense Stack (`POST /v1/ping/{handle}`):**
  * Sub-3ms response ceiling returning `202 Accepted` via non-blocking channel enqueuing.
  * Stateless Hashcash proof-of-work (PoW) verification with ±1 epoch clock skew tolerance.
  * IP token buckets and cascaded rotating Bloom filters (24-hour windows) enforcing pair deduplication limits.
  * Uniform timing and byte-identical responses across valid, rate-limited, blocked, paused, and unknown handles (no existence oracles).
* **Identity & Account Management:**
  * Anonymous account creation protected by Hashcash PoW ($d=21$, ~1.5s client grind).
  * 16-digit cryptographically random account keys (~53.15 bits entropy).
  * Client-side HKDF `auth_secret` derivation and server-side Argon2id password hashing.
  * Handle minting with Crockford base32 encoding and region prefix `e` for `eu-1`.
  * Complete GDPR data export (`GET /v1/account/export`) and cascading account deletion (`DELETE /v1/account`).
* **Coalescer & Dispatch Pipeline:**
  * Recipient accumulator and 1-second precision time wheel.
  * Quiet hours and digest escalation policies.
  * In-house RFC 8291 / RFC 8188 Web Push dispatcher using stdlib `crypto/ecdh` and `crypto/aes`.
  * Authoritative subscription pruning on `404`/`410` responses with `created_day <= send_start_day` race protection.
* **Real-time SSE Hub (`GET /v1/stream`):**
  * Bounded channel buffers (16 frames) and 10,000 active connection ceiling.
  * 25-second heartbeats (`event: ka`) and 15-minute idle demotion to `GET /v1/pending`.
  * Measured heap memory footprint of ~4.56 KB per connected client.
* **Multi-Platform Clients:**
  * Web App PWA (`web/app`) with vanilla TypeScript, zero external CDNs, and client-side SVG QR code generator.
  * Manifest V3 browser extensions for Chrome and Firefox with SSE popup mode and `chrome.alarms` background sync.
  * Tauri v2 native desktop app for macOS, Windows, and Linux with secure OS keychain integration.
* **Verification & Release Pipeline:**
  * Automated load test harness (`loadtest/`) validating sustained 1,200 RPS and 10,000 burst capacity.
  * Automated adversarial red-team suite (`redteam/`) verifying all 9 invariants under race detector in CI.
  * CI/CD cross-platform release workflow (`.github/workflows/release.yml`) with Apple Developer ID signing, macOS notarization, and Windows EV code signing support.

---

## [0.0.1] - 2026-09-01 — Phase 0: Skeleton

### Added
* Initial repository layout, Dockerfile, and `docker-compose.yml`.
* Invariant enforcement contract and architectural rules (`AGENTS.md`).
* Pure Go server daemon (`halpd`) with `modernc.org/sqlite` in WAL mode (`CGO_ENABLED=0`).
* Plain SQL schema migrations applied in sequential order (`0001_init.sql`).
* Frozen contract wave (`server/internal/core/` and `server/internal/http/router.go`).
* Baseline `/healthz` and Prometheus `/metrics` endpoints.
