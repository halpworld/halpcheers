/**
 * Test suite for @halp/core.
 * Covers all requirements in Issue #20 Definition of Done:
 * - HKDF vectors tested against known-answer set.
 * - auth_secret and contacts_key differ.
 * - Spy on fetch across every method asserts account key NEVER leaks to body, URL, header, or query.
 * - PoW solve benchmark and epoch token caching (ten sends grind once).
 * - Reconnect backoff jitter (asserts two clients do not synchronise).
 * - Local syntactic validation.
 */

import test from 'node:test';
import assert from 'node:assert';
import {
  generateAccountKey,
  deriveAuthSecret,
  deriveAuthSecretHex,
  deriveContactsKey,
  bytesToHex,
  storeAccountKey,
  loadAccountKey,
  clearAccountKey,
  PoWEngine,
  checkPoW,
  solvePoWSync,
  getCurrentEpoch,
  DIFFICULTY_FLOOR,
  calculateJitteredBackoff,
  HalpClient,
  isHandleShaped,
  isAliasShaped,
  isAccountKeyShaped,
  formatAccountKey,
  normalizeAccountKey,
  TransportManager,
} from '../dist/index.js';

// -----------------------------------------------------------------------------
// 1. HKDF Known-Answer Vector & Domain Separation Tests
// -----------------------------------------------------------------------------

test('HKDF known-answer vectors match server derivations exactly', async () => {
  const knownKey = '1234567890123456';
  // Generated and verified directly against server/internal/auth (Go stdlib hkdf)
  const expectedAuthSecret = '66a32d811b1ae8e728e60fcc073c1cf9996e1dce4db7ab14de6c2d73742f22b3';
  const expectedContactsKey = '50c7311ecac10d3049591a1904f208c2619f87309111b15375b69f726b00fc01';

  const authSecretBytes = await deriveAuthSecret(knownKey);
  const contactsKeyBytes = await deriveContactsKey(knownKey);

  const authSecretHex = bytesToHex(authSecretBytes);
  const contactsKeyHex = bytesToHex(contactsKeyBytes);

  assert.strictEqual(authSecretHex, expectedAuthSecret, 'auth_secret matches known-answer vector');
  assert.strictEqual(contactsKeyHex, expectedContactsKey, 'contacts_key matches known-answer vector');
  assert.notStrictEqual(authSecretHex, contactsKeyHex, 'auth_secret and contacts_key must differ');
});

test('Domain separation: auth_secret and contacts_key differ for randomly generated keys', async () => {
  for (let i = 0; i < 5; i++) {
    const key = generateAccountKey();
    assert.strictEqual(key.length, 16, 'key is 16 digits');
    const authHex = await deriveAuthSecretHex(key);
    const contactsBytes = await deriveContactsKey(key);
    const contactsHex = bytesToHex(contactsBytes);

    assert.strictEqual(authHex.length, 64);
    assert.strictEqual(contactsHex.length, 64);
    assert.notStrictEqual(authHex, contactsHex, 'keys derived under different info must differ');
  }
});

// -----------------------------------------------------------------------------
// 2. Invariant: Account Key Never Transmitted Across Network (Fetch Spy)
// -----------------------------------------------------------------------------

test('INVARIANT: No request body, URL, header, or query string contains the account key across any method', async () => {
  const secretAccountKey = '9876543210987654';

  const requestsIntercepted: Array<{ url: string; method?: string; headers: Record<string, string>; body?: string }> = [];

  const mockFetch: typeof fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
    const urlStr = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
    const headersObj: Record<string, string> = {};
    if (init?.headers) {
      if (init.headers instanceof Headers) {
        init.headers.forEach((v, k) => { headersObj[k] = v; });
      } else if (Array.isArray(init.headers)) {
        for (const [k, v] of init.headers) { headersObj[k] = v; }
      } else {
        Object.assign(headersObj, init.headers);
      }
    }
    requestsIntercepted.push({
      url: urlStr,
      method: init?.method,
      headers: headersObj,
      body: init?.body ? String(init.body) : undefined,
    });

    // Mock responses
    if (urlStr.includes('/v1/accounts')) {
      return new Response(JSON.stringify({ account_key: '1111222233334444' }), { status: 201, headers: { 'Content-Type': 'application/json' } });
    }
    if (urlStr.includes('/v1/session')) {
      return new Response(JSON.stringify({ token: 'test_session_token', expires_at: 1999999999 }), { status: 200, headers: { 'Content-Type': 'application/json' } });
    }
    if (urlStr.includes('/v1/handles') && init?.method === 'GET') {
      return new Response(JSON.stringify({ handles: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } });
    }
    if (urlStr.includes('/v1/handles') && init?.method === 'POST') {
      return new Response(JSON.stringify({ handle: 'e7k4p2m9qx3v', kind: 'personal', paused: false, created_day: 20000 }), { status: 201, headers: { 'Content-Type': 'application/json' } });
    }
    if (urlStr.includes('/v1/ping/')) {
      return new Response(null, { status: 202 });
    }
    if (urlStr.includes('/v1/subscriptions')) {
      return new Response(JSON.stringify({ id: 42 }), { status: 201, headers: { 'Content-Type': 'application/json' } });
    }
    if (urlStr.includes('/v1/settings') && init?.method === 'GET') {
      return new Response(JSON.stringify({ digest_window_s: 60, max_per_hour: 12, min_count: 1, mode: 'all' }), { status: 200, headers: { 'Content-Type': 'application/json' } });
    }
    if (urlStr.includes('/v1/pending')) {
      return new Response(JSON.stringify({ n: 5 }), { status: 200, headers: { 'Content-Type': 'application/json' } });
    }
    if (urlStr.includes('/report-abuse')) {
      return new Response(JSON.stringify({ status: 'ok' }), { status: 200, headers: { 'Content-Type': 'application/json' } });
    }
    return new Response(JSON.stringify({ status: 'ok' }), { status: 200, headers: { 'Content-Type': 'application/json' } });
  };

  const client = new HalpClient({
    baseUrl: 'https://api.halp.test',
    fetchFn: mockFetch,
  });

  // Call every method
  await client.loginWithAccountKey(secretAccountKey);
  await client.getAccount();
  await client.listHandles();
  const created = await client.createHandle('Work', 'personal');
  await client.updateHandle(created.handle, { label: 'Renamed' });
  await client.sendPing('e7k4p2m9qx3v');
  await client.createSubscription({ endpoint: 'https://push.test/sub', p256dh: 'dummy', auth: 'dummy' });
  await client.getPending();
  await client.getSettings();
  await client.updateSettings({ digest_window_s: 60 });
  await client.reportAbuse('e7k4p2m9qx3v');
  await client.exportAccount();
  await client.deleteSession();

  // Audit intercepted requests
  assert.ok(requestsIntercepted.length >= 10, 'intercepted all method requests');

  for (const req of requestsIntercepted) {
    // 1. Check URL
    assert.ok(!req.url.includes(secretAccountKey), `Account key leaked in URL: ${req.url}`);

    // 2. Check Body
    if (req.body) {
      assert.ok(!req.body.includes(secretAccountKey), `Account key leaked in body: ${req.body}`);
    }

    // 3. Check Headers
    for (const [k, v] of Object.entries(req.headers)) {
      assert.ok(!v.includes(secretAccountKey), `Account key leaked in header ${k}: ${v}`);
    }
  }
});

// -----------------------------------------------------------------------------
// 3. PoW Solver, Difficulty Calibration & Epoch Caching
// -----------------------------------------------------------------------------

test('PoW solver solves challenge within expected iteration and time budget', async () => {
  const handle = 'e7k4p2m9qx3v';
  const epoch = 123456;
  const challenge = new Uint8Array(32);
  challenge[0] = 0x12; challenge[31] = 0x34;

  const start = performance.now();
  const res = solvePoWSync(handle, epoch, challenge, DIFFICULTY_FLOOR);
  const elapsed = performance.now() - start;

  assert.ok(res !== null, 'PoW solved successfully');
  assert.ok(checkPoW(handle, epoch, challenge, res.nonce, DIFFICULTY_FLOOR), 'valid solution');
  // Floor difficulty (d=14) expected ~10 ms, assert completed well under 1 second
  assert.ok(elapsed < 1000, `PoW floor solved in ${elapsed.toFixed(1)} ms (< 1000 ms budget)`);
});

test('Epoch token caching: ten sends in one epoch grind exactly once', async () => {
  const engine = new PoWEngine();
  const target = 'e7k4p2m9qx3v';
  const epoch = getCurrentEpoch();
  const challenge = new Uint8Array(32);

  assert.strictEqual(engine.getGrindCount(), 0);

  // Send 1: Grinds and caches
  const t1 = await engine.getOrSolveToken(target, epoch, challenge, DIFFICULTY_FLOOR);
  assert.strictEqual(engine.getGrindCount(), 1);

  // Sends 2 through 10: Must hit cache with 0 additional grinds
  for (let i = 2; i <= 10; i++) {
    const t = await engine.getOrSolveToken(target, epoch, challenge, DIFFICULTY_FLOOR);
    assert.strictEqual(t.headerValue, t1.headerValue);
    assert.strictEqual(engine.getGrindCount(), 1, `send ${i} must use cached token without re-grinding`);
  }

  engine.terminate();
});

// -----------------------------------------------------------------------------
// 4. Jittered Reconnect Backoff
// -----------------------------------------------------------------------------

test('Reconnect backoff is jittered: two clients with identical attempts do not synchronize', () => {
  const delaysClientA: number[] = [];
  const delaysClientB: number[] = [];

  for (let attempt = 0; attempt < 10; attempt++) {
    const delayA = calculateJitteredBackoff(attempt, 1000, 30000);
    const delayB = calculateJitteredBackoff(attempt, 1000, 30000);
    delaysClientA.push(delayA);
    delaysClientB.push(delayB);
  }

  // Verify that delays vary and are not identical across all attempts
  let identicalCount = 0;
  for (let i = 0; i < delaysClientA.length; i++) {
    if (delaysClientA[i] === delaysClientB[i]) {
      identicalCount++;
    }
  }

  assert.ok(identicalCount < delaysClientA.length, 'delays must not synchronize');
});

// -----------------------------------------------------------------------------
// 5. Syntactic Validation
// -----------------------------------------------------------------------------

test('Local syntactic validation accepts valid formats and rejects malformed inputs', () => {
  // Handles (1 prefix + 12 chars = 13 characters)
  assert.strictEqual(isHandleShaped('e7k4p2m9qx3va'), true);
  assert.strictEqual(isHandleShaped('e0123456789ab'), true);
  assert.strictEqual(isHandleShaped('a7k4p2m9qx3va'), false, 'must start with e');
  assert.strictEqual(isHandleShaped('e7k4p2m9qx3'), false, 'must be 13 characters');
  assert.strictEqual(isHandleShaped('e7k4p2m9qx3vi'), false, 'cannot contain i');

  // Aliases
  assert.strictEqual(isAliasShaped('@kenth'), true);
  assert.strictEqual(isAliasShaped('@team-lead_42'), true);
  assert.strictEqual(isAliasShaped('kenth'), false, 'must start with @');
  assert.strictEqual(isAliasShaped('@'), false, 'too short');

  // Account keys
  assert.strictEqual(isAccountKeyShaped('6421883051974462'), true);
  assert.strictEqual(isAccountKeyShaped('6421 8830 5197 4462'), true);
  assert.strictEqual(isAccountKeyShaped('123'), false);
  assert.strictEqual(formatAccountKey('6421883051974462'), '6421 8830 5197 4462');
  assert.strictEqual(normalizeAccountKey('6421 8830 5197 4462'), '6421883051974462');
});

// -----------------------------------------------------------------------------
// 6. Device Key Storage
// -----------------------------------------------------------------------------

test('Key storage abstraction stores, loads, and clears account key', async () => {
  const memoryStore = new Map<string, string>();
  const mockStorage = {
    getItem: (k: string) => memoryStore.get(k) ?? null,
    setItem: (k: string, v: string) => { memoryStore.set(k, v); },
    removeItem: (k: string) => { memoryStore.delete(k); },
  };

  const key = '1234567890123456';
  await storeAccountKey(key, mockStorage);
  const loaded = await loadAccountKey(mockStorage);
  assert.strictEqual(loaded, key);

  await clearAccountKey(mockStorage);
  const cleared = await loadAccountKey(mockStorage);
  assert.strictEqual(cleared, null);
});
