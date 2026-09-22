# @halp/core

The shared TypeScript client library for Halp.

Used across all Halp client surfaces:
* `web/app/` — the web app
* `extension/` — Chrome & Firefox MV3 extensions
* `desktop/` — Tauri v2 desktop application

Zero runtime dependencies beyond Web APIs (`crypto.subtle`, `fetch`, `EventSource`, `Worker`).

---

## 1. Key Derivation & Management (`key.ts`)

Per **AGENTS.md Data Rules** and **docs/IDENTITY.md**:
* The 16-digit account key (`account_key`) never leaves the device.
* `auth_secret = HKDF-SHA256(account_key, info="halp/auth/v1")` (32 bytes) is derived and sent at login (`POST /v1/session`).
* `contacts_key = HKDF-SHA256(account_key, info="halp/contacts/v1")` (32 bytes) is derived locally and never transmitted anywhere.

```typescript
import {
  generateAccountKey,
  deriveAuthSecret,
  deriveAuthSecretHex,
  deriveContactsKey,
  storeAccountKey,
  loadAccountKey,
  clearAccountKey,
} from '@halp/core';

// 1. Generate fresh 16-digit account key
const key = generateAccountKey(); // e.g. "6421883051974462"

// 2. Derive 32-byte auth_secret (hex string for login)
const authSecretHex = await deriveAuthSecretHex(key);

// 3. Derive 32-byte contacts_key (never sent)
const contactsKey = await deriveContactsKey(key);

// 4. Save key in local device storage
await storeAccountKey(key);
```

---

## 2. Hashcash Proof of Work (`pow.ts`)

Implements client-side hashcash over 5-minute epochs per **docs/ABUSE.md**:
* Grinds: `SHA-256(handle || epoch || challenge || nonce)` until leading zero bits $\ge d$.
* Floor difficulty: $d=14$ (~10 ms compute).
* Signup difficulty: $d=21$ (~1.5 s compute).
* Automatically offloaded to a Web Worker to avoid blocking UI rendering.
* Caches solved tokens per epoch so bursts of sends pay the PoW cost only once.

```typescript
import { PoWEngine, DIFFICULTY_FLOOR, DIFFICULTY_SIGNUP, getCurrentEpoch } from '@halp/core';

const pow = new PoWEngine();

// Solve or retrieve cached token
const token = await pow.getOrSolveToken('e7k4p2m9qx3v');
console.log(token.headerValue); // "123456.1042" (used in X-Halp-PoW header)
```

---

## 3. Typed API Client (`client.ts`)

Strongly typed client for all endpoints in **docs/API.md**.

```typescript
import { HalpClient } from '@halp/core';

const client = new HalpClient({ baseUrl: 'https://halp.to' });

// 1. Signup (costs PoW d=21)
const { account_key } = await client.createAccount();

// 2. Login using account key (derives auth_secret, sends only that)
await client.loginWithAccountKey(account_key);

// 3. Send appreciation ping (< 3 ms server response)
await client.sendPing('e7k4p2m9qx3v');

// 4. Manage handles
const handles = await client.listHandles();
const newHandle = await client.createHandle('My GitHub Handle', 'personal');
await client.updateHandle(newHandle.handle, { paused: true });
await client.deleteHandle(newHandle.handle); // Burns permanently

// 5. Settings & Abuse reporting
const settings = await client.getSettings();
await client.updateSettings({ digest_window_s: 900, max_per_hour: 12 });
await client.reportAbuse('e7k4p2m9qx3v');
```

---

## 4. Real-time Transport Manager (`transport.ts`)

Manages real-time incoming appreciation notifications over SSE (`GET /v1/stream`) with automatic jittered backoff and idle demotion fallback to `GET /v1/pending`:

```typescript
import { TransportManager } from '@halp/core';

const transport = new TransportManager({
  client,
  events: {
    onPing: (count) => {
      console.log(`Received ${count} appreciation pings`);
    },
    onStatusChange: (status) => {
      console.log(`Transport status: ${status}`);
    },
  },
});

transport.start();
```

---

## 5. Syntactic Validation (`validation.ts`)

Provides local syntactic validation without server requests (protecting against enumeration oracles):
* `isHandleShaped(handle)`: Validates 13-character Crockford base32 handle starting with `e`.
* `isAliasShaped(alias)`: Validates `@` prefix and alphanumeric/hyphen syntax.
* `isAccountKeyShaped(key)`: Validates 16 decimal digits.
* `formatAccountKey(key)`: Formats into `xxxx xxxx xxxx xxxx`.
