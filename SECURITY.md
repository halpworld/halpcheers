# Security Policy

Halp is engineered so that user privacy, sender anonymity, and resistance to abuse are **guaranteed by cryptography and architecture**, rather than merely promised by policy.

---

## 1. Supported Versions

Security updates and vulnerability patches are published for the following active releases:

| Version | Supported | Notes |
| --- | --- | --- |
| `0.3.x` | :white_check_mark: | Current active release (Phase 3 Amplifiers) |
| `0.2.x` | :white_check_mark: | Phase 2 Reach (Contacts Sync, Groups, TUI, Desktop) |
| `0.1.x` | :white_check_mark: | Phase 1 Core (Security Hardening, Web Push, Red-Team) |
| `< 0.1.0` | :x: | Pre-release and skeleton builds |

---

## 2. Reporting a Vulnerability

We treat all security disclosures with extreme urgency. If you discover a vulnerability or architectural flaw that compromises user privacy, sender anonymity, or service availability, please report it privately.

### Preferred Reporting Methods

1. **GitHub Security Advisory (Recommended):**
   Submit a private vulnerability disclosure directly through the repository's **Security** tab via GitHub Private Vulnerability Reporting.
2. **Direct Email:**
   If GitHub Security Advisories are unavailable, email **`security@halp.to`**.

### Disclosure Response SLA

* **Acknowledgment:** Within **48 hours** of initial receipt.
* **Triage & Assessment:** Within **5 business days**, including an initial severity determination and reproduction confirmation.
* **Status Updates:** Every **7 business days** until a patch is released.
* **Public Disclosure Window:** Standard **90-day** coordinated vulnerability disclosure window to allow users to upgrade before public advisory publication.

> [!CAUTION]
> **Please do NOT report security vulnerabilities via public GitHub issues, discussions, or pull requests.**

---

## 3. Cryptographic Architecture & Key Hierarchy

Halp enforces strict cryptographic domain separation so that even complete database compromise or rogue server administrators cannot decrypt user contacts or deanonymize senders.

```
                         Client Device (Never Transmitted)
                                   account_key
                          (16-digit decimal, ~53.15 bits)
                                       │
            ┌──────────────────────────┴──────────────────────────┐
            ▼                                                     ▼
       auth_secret                                           contacts_key
HKDF-SHA256(account_key, "halp/auth/v1")              HKDF-SHA256(account_key, "halp/contacts/v1")
            │                                                     │
            │ (Sent over TLS to /v1/session)                      │ (Never leaves client device)
            ▼                                                     ▼
     Argon2id Hash at Rest                                AES-256-GCM Seal
 (t=1, m=64MB, p=4, keyLen=32)                        (Fresh 12-byte random IV)
  Fixed salt: "halp-argon2id-v1"                     Applied after 4 KiB padding
```

### 1. `account_key` (The Device Secret)
* A 16-digit cryptographically random decimal value generated client-side from `crypto/rand` (~53.15 bits of entropy).
* **The account key never leaves the client device.** It is never transmitted across the wire, never stored in URLs, never encoded in shareable QR codes, and never written to server disks.

### 2. `auth_secret` (Authentication Secret)
* Derived client-side as `HKDF-SHA256(account_key, info="halp/auth/v1")`.
* Transmitted over TLS to authenticate session creation (`POST /v1/session`).
* The server stores only the deterministic **Argon2id** hash of `auth_secret` (`t=1, m=64MB, p=4, salt="halp-argon2id-v1"`).
* Even if the server database is leaked, an attacker cannot reverse the Argon2id hash to recover `auth_secret`, nor can `auth_secret` be used to derive `contacts_key`.

### 3. `contacts_key` (Client-Side Contacts Encryption)
* Derived client-side as `HKDF-SHA256(account_key, info="halp/contacts/v1")`.
* **Never leaves the client device.** The server has zero knowledge of `contacts_key`.
* Used to seal the private address book blob with **AES-256-GCM** using a fresh 12-byte random IV per write.
* **4 KiB (`4096`-byte) Boundary Padding:** Before encryption, plaintext contacts JSON is padded to a 4 KiB boundary. This prevents the server from observing contact counts or watching contact count growth.
* Hard storage ceiling of 64 KiB (`contacts.max_bytes`) enforced by the server (Invariant 8).
* Multi-device sync uses `If-Match` optimistic concurrency with deterministic CRDT set-union merging where `deleted: true` tombstones authoritatively win.

### 4. Web Push Encryption (`RFC 8291` / `RFC 8188` / `RFC 8292`)
* Implemented in-house in Go using only standard library packages (`crypto/ecdh`, `crypto/aes`, `crypto/cipher`, `crypto/ecdsa`). Zero third-party cryptographic dependencies.
* ECDH key agreement over the NIST P-256 curve with cached shared secret per subscription endpoint.
* **Fresh Random Salt per Message:** A cryptographically random 16-byte salt is generated afresh for every digest message, strictly preventing AES-GCM nonce reuse under cached shared secrets.
* Single-count pings (`n=1`) are dispatched **payloadless** (zero-byte body), eliminating cryptographic overhead and metadata leakage.
* Push requests are authenticated using RFC 8292 Voluntary Application Server Identification (VAPID) with ES256 signatures.

### 5. Stateless Hashcash Proof-of-Work (PoW)
* Send and signup paths require client-side Hashcash grinding based on HMAC-SHA256 challenge epochs (5-minute epochs with $\pm 1$ skew tolerance).
* Send floor difficulty $d=14$ (~10 ms client compute); signup difficulty $d=21$ (~1.5 s client compute).
* Verification uses stack-allocated buffers and zero heap allocations (~130 ns verification time), neutralizing distributed denial-of-service (DDoS) and botnet amplification.

---

## 4. What Qualifies as a Critical Security Vulnerability

Any issue that violates the 9 Non-Negotiable Invariants of Halp constitutes a security vulnerability of the highest severity:

1. **Sender De-Anonymization (Invariant 6):**
   Any mechanism, timing discrepancy, API response field, UI indicator, or inference attack that enables a recipient or third party to identify who sent an appreciation ping.
2. **Existence Oracles (Invariant 7):**
   Any observable difference in HTTP response code, body, or timing between active, paused, blocked, rate-limited, and nonexistent handles or aliases.
3. **Message History & Logging (Invariants 1 & 9):**
   Any persistent recording of pings, messages, or sender-recipient relationships to disk, or any leakage of user identifiers (`handle`, `alias`, `account_id`, `ip`, `endpoint`, `user_agent`) into server logs or Prometheus metrics.
4. **Memory Exhaustion & Amplification (Invariant 8):**
   Any endpoint that allocates unbounded memory or stores unbounded keys in maps based on untrusted user inputs.
5. **Cryptographic Weaknesses:**
   Any nonce reuse under AES-256-GCM, predictable challenge generation, or side-channel leakage during Hashcash verification or Argon2id hashing.
