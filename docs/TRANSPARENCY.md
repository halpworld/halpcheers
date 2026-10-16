# Public Transparency Report & Warrant Canary

This document sets out Halp's public transparency report, our warrant canary, and the architectural principles that make surveillance and retroactive data disclosure impossible.

Halp is engineered so that privacy is guaranteed by mathematics and system architecture, not merely by policy or promises.

---

## 1. Architectural Impossibility: The Defense Stack

When companies receive subpoenas, national security letters, or law enforcement warrants, they can only produce what they possess. Halp is deliberately constructed so that message history and sender identities **do not exist**.

### Invariant 1: No Message Records

There is no `messages`, `pings`, `history`, `events`, or `audit_log` table anywhere in Halp's SQLite database or server memory:
* When an appreciation ping arrives at `POST /v1/ping/{target}`, it validates proof-of-work and token buckets, enqueues to an in-memory ring buffer, and returns `202 Accepted` in under 3 ms (Invariant 2).
* The coalescer time wheel increments ephemeral counters and schedules delivery.
* Once a push notification or SSE frame is dispatched, **the ping is gone from memory forever**.
* Even under court order or physical seizure of server hardware, Halp cannot produce message history, ping timestamps, or message logs because they are never written to disk or preserved.

### Invariant 6: Never Reveal the Sender

The sender of an appreciation ping is anonymous to both the recipient and the server:
* `POST /v1/ping/{target}` authenticates via an anonymous, unlinked 16-digit key session (`Authorization: Bearer <session>`, provisioned in the background on first visit) combined with client-side Hashcash proof-of-work (PoW). The server never knows a user's real-world identity, email, or IP, and never writes `(sender, recipient)` ping records to disk.
* Bloom filters and Count-Min sketches used for pair-rate-limiting and abuse mitigation reside solely in volatile memory with TTLs. None of them record persistent identifiers.
* Data export under GDPR (`GET /v1/account/export`) contains handles, settings, and client-encrypted contacts. It contains strictly zero sender accounts, sender keys, or sender handles.

### Invariant 9: No Identifiers in Observability

Access logs carry the chi route pattern (e.g. `/v1/ping/{target}`) rather than the concrete handle or alias. No IP addresses, user agents, account IDs, or handles are ever written to server logs or Prometheus metric labels.

### Single EU Region (`eu-1`)

All account data resides exclusively in a single region in the European Union (Germany / Finland) under GDPR jurisdiction. There is no multi-region replication, no peer routing, and no global alias directory (Decision 22).

---

## 2. Warrant Canary

A warrant canary is a regularly published statement confirming that an organization has **not** received secret government subpoenas, national security directives, or gag orders. If this notice is ever removed, fails to update, or is altered, users should assume that confidentiality has been legally compromised.

### Canary Statement

**As of October 2026, Halp certifies the following:**

1. **Zero (0) National Security Letters (NSLs)** have been received.
2. **Zero (0) Foreign Intelligence Surveillance Act (FISA)** court orders or directives have been received.
3. **Zero (0) gag orders** prohibiting disclosure of legal requests or government inquiries have been received.
4. **Zero (0) demands or requests for encryption keys, account keys, backdoors, or altered software builds** have been received.
5. **Zero (0) Title III wiretap orders or pen register / trap and trace orders** have been received.
6. Halp operates 100% open-source software with verifiable, reproducible builds. No user data has ever been diverted to third-party intelligence or surveillance programs.

*Next Scheduled Canary Verification:* April 2027 (published bi-annually).

---

## 3. Law Enforcement Inquiries & Legal Requests

The table below records all legal requests, warrants, subpoenas, and court orders received across all reporting periods.

| Period | Subpoenas | Search Warrants | Court Orders | Emergency Requests | National Security Requests | Accounts Disclosed | Pings / Messages Disclosed |
| :--- | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| **2025 H1** | 0 | 0 | 0 | 0 | 0 | 0 | **0 (Impossible)** |
| **2025 H2** | 0 | 0 | 0 | 0 | 0 | 0 | **0 (Impossible)** |
| **2026 H1** | 0 | 0 | 0 | 0 | 0 | 0 | **0 (Impossible)** |
| **2026 H2 (To date)** | 0 | 0 | 0 | 0 | 0 | 0 | **0 (Impossible)** |

### Disclosure Policy

If served with a valid, legally binding court order issued by a court of competent jurisdiction:
1. **Zero Content Disclosed:** We cannot disclose message text, reasons, or pings, as none are ever collected or stored.
2. **Zero Sender Disclosed:** We cannot disclose who appreciated an account or sent a ping, as sender identity is never recorded.
3. **Limited Account Metadata Only:** The only stored account metadata is the public handle string, the day-granular integer creation date (`created_day`), and delivery settings. Account keys never leave user devices (passwords do not exist).
4. **Challenge to Overreach:** We challenge all overbroad, unconstitutional, or non-jurisdictional demands.

---

## 4. Aggregate Metrics Structure

To ensure public accountability without compromising individual privacy, Halp exposes non-identifying operational counters via `/metrics` (Prometheus) and `obs.GenerateTransparencyReport`:

```
# Core throughput and dispatch metrics (non-identifying)
halp_pings_accepted_total
halp_pings_dropped_total{reason="rate_limited|deduped|blocked|pow_failed|queue_full|stale|unknown_or_paused"}
halp_digests_sent_total{channel="webpush|sse|pending"}
halp_sse_connections
```

These aggregate metrics provide absolute verification of system health and throughput while ensuring that individual senders, recipients, and timing correlations cannot be inspected or reconstructed.
