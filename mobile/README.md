# Halp Mobile Architecture & Phase 4 Evaluation

This directory (`mobile/`) is reserved as an architectural placeholder for **Phase 4 — Mobile**, as specified in [`docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md) and [`docs/ROADMAP.md`](../docs/ROADMAP.md).

Mobile clients are deliberately scheduled last and are explicitly conditional upon empirical operating data and platform viability reviews from Phases 1–3.

---

## 1. Phase 4 Go / No-Go Decision Gate

A native mobile client is **not** an automatic milestone. It is gated on two hard platform viability hurdles:

### Apple App Store Guideline 4.2 (Minimum Functionality)
Apple's App Store Review Guidelines (§4.2) mandate that applications provide substantial, lasting utility beyond a simple website or single-purpose notification receiver:
* Halp is intentionally minimalist: **no user accounts with passwords, no profile pictures, no message text, no replies, no reaction feeds, and no social graph** (`AGENTS.md`).
* A mobile app whose sole function is receiving an anonymous notification ("Someone appreciates you.") and viewing today's integer count risks arbitrary rejection during app review as "insufficient functionality" or a "repackaged web page".

### App Store Push-Spam & Contentless Notification Rules
Both Apple and Google enforce strict guidelines regarding unsolicited notifications:
* Push notifications cannot be sent without user intent or contextual content.
* Halp's anonymous appreciation pings originate from third parties who send to a public handle without the recipient's prior knowledge. Reviewers may classify this as "unsolicited push spam" unless carefully contextualized.

### Progressive Web App (PWA) as Canonical Mobile Baseline
Since iOS 16.4 and Android Chrome 50+, modern mobile browsers support standards-based **Web Push over W3C Push API / VAPID** when installed to the home screen ("Add to Home Screen"):
* The existing `@halp/app` (`web/app/`) runs as a fully compliant, zero-external-origin PWA with offline caching via its Service Worker (`/sw.js`).
* Mobile users receive system-native notifications directly through Web Push without passing through Apple or Google review gates.
* **A native mobile app will only proceed if PWA retention metrics demonstrate a compelling operational need that the web platform cannot satisfy.**

---

## 2. Architectural Framework Evaluation

If the Phase 4 gate passes, two implementation paths are evaluated:

| Architectural Option | Technology Stack | Advantages | Disadvantages |
| :--- | :--- | :--- | :--- |
| **Option A: Tauri v2 Mobile** | Rust + Webview (`@halp/core` + `@halp/app`) | Reuses existing Tauri desktop Rust bridge, key custody (`keyring`), and TypeScript UI components. Single codebase across macOS, Windows, Linux, iOS, and Android. | Webview memory footprint; Apple review scrutiny over bundled web content. |
| **Option B: Thin Native Shells** | Swift / SwiftUI (iOS) & Kotlin / Compose (Android) | Instant cold start, tiny binary size (< 5 MB), native OS secure enclave integration (iOS Keychain / Android Keystore), direct APNs/FCM token hooks. Easiest to pass App Store Guideline 4.2 via platform-native widgets. | Requires maintaining two platform-specific native UI shells alongside the web/desktop clients. |

---

## 3. Backend Push Service Foundation & Invariant 4 Hygiene

The Halp server backend (`server/`) is already architected to support native mobile push transports alongside standard Web Push without requiring breaking database migrations:

### Subscriptions Schema
The `subscriptions` table in SQLite is transport-agnostic:
```sql
CREATE TABLE IF NOT EXISTS subscriptions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id   INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    kind         TEXT    NOT NULL, -- 'webpush' | 'apns' | 'fcm'
    endpoint     TEXT    NOT NULL,
    p256dh       BLOB,
    auth         BLOB,
    created_day  INTEGER NOT NULL,
    last_ok_day  INTEGER
);
```
* `kind: "apns"`: Stores the APNs device token as the endpoint.
* `kind: "fcm"`: Stores the Firebase Cloud Messaging v1 registration token as the endpoint.

### Authoritative Unregistration & Invariant 4 Pruning
Per **AGENTS.md Invariant 4**, push endpoints must be pruned **only on authoritative rejection from push gateways**, never on transient network faults:
1. **Apple Push Notification service (APNs):**
   * Authoritative error: HTTP/2 status `410 Gone` with reason `Unregistered` or `BadDeviceToken`.
   * Transient errors: HTTP/2 status `429 Too Many Requests`, `500`, `503`, or connection dropouts. These are dropped and counted in metrics, never pruned.
2. **Firebase Cloud Messaging (FCM v1):**
   * Authoritative error: HTTP status `404 Not Found` or `410 Gone` with error code `UNREGISTERED`.
   * Transient errors: HTTP status `429`, `500`, `503`, or timeouts.

### Re-registration Race Protection
Pruning enforces strict timestamp inequality:
```sql
DELETE FROM subscriptions
WHERE endpoint = ? AND created_day <= ?;
```
If a mobile user re-installs the app and re-registers the same device token while an asynchronous push dispatch is in flight, the new subscription row receives a newer `created_day`. The stale dispatch failure's prune query safely no-ops, preventing silent unsubscription.

### Invariant 6 (Zero Sender Disclosure)
Under all push transports (Web Push, APNs, FCM):
* Single pings deliver a **payloadless notification** triggering the client's localized copy ("Someone appreciates you.").
* Coalesced digests deliver only the integer count `{"n": count}`.
* No sender account ID, handle, timestamp, or correlation identifier is ever transmitted to Apple, Google, or the recipient's mobile device.
