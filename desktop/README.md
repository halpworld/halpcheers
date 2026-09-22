# @halp/desktop — Tauri v2 Desktop Application

Desktop application for Halp (`to.halp.desktop`), built with [Tauri v2](https://v2.tauri.app/), `@halp/core`, and pure system UI components.

## Features

- **Native System Tray**: System tray icon with real-time arrival counter and quick-send action for default handles.
- **Native OS Notifications**: Native notification integration across macOS, Windows, and Linux adhering strictly to `docs/UI.md` §8 copy bands ($n=1$: "Someone appreciates you.", $n \ge 2$: "$n$ people appreciate you.") with zero body text, zero action buttons, and zero sender revelation.
- **OS Keychain Integration (Decision 40)**:
  - Primary: Native OS secure credential store via the `keyring` crate (macOS Keychain, Windows Credential Manager, Linux Secret Service).
  - Fallback: AES-256-GCM encrypted local storage with key derived via HKDF-SHA256 when hardware keyring is unavailable (e.g. headless CI or container sandboxes). Plaintext storage on disk is strictly forbidden.
- **Long-lived SSE with Idle Demotion (Decision 36)**: Continuous real-time streaming over `GET /v1/stream`. When idle demoted by the server after 15 minutes of inactivity, gracefully transitions to polling `GET /v1/pending` and recovers transparently.
- **Silent Jittered Reconnect**: Exponential backoff with randomized full jitter ensures silent network reconnection without intrusive notification toasts.
- **Strict Latency Floor & Send Uniformity**: Send actions enforce a minimum 300 ms perceived latency floor and exact two-state button behavior (`Sending...` → `Sent.`). Accepted pings, rate-limited drops, and network failures render identically to eliminate timing or status oracles.
- **Zero-Leak Design**: Zero external font requests, zero analytics/telemetry SDKs, zero crash-reporting libraries, and zero external origins.

## Packaging

Tauri v2 generates unsigned distribution bundles for all three major desktop platforms from a single codebase:
- **macOS**: `.dmg`
- **Windows**: `.msi`
- **Linux**: `.AppImage`, `.deb`

### Building

```bash
# Install frontend dependencies
npm install

# Build frontend assets
npm run build

# Run desktop test suite
npm test

# Build Tauri distribution bundles (requires Rust and Tauri CLI)
cargo tauri build
```
