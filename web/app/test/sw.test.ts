/**
 * Test suite for Halp Service Worker (Issue #22).
 * Covers Definition of Done requirements:
 * - Push event with no data renders the fixed notification.
 * - Push event with {"n": N} renders each of the three copy bands correctly.
 * - pushsubscriptionchange re-subscribes and re-registers against a mock.
 * - Permission requested after user gesture, never on load.
 * - Cold-start path falls back to /v1/pending.
 * - Notification click focuses or opens app.
 */

import test from 'node:test';
import assert from 'node:assert';
import {
  getNotificationTitle,
  processPushEvent,
  processSubscriptionChange,
  processNotificationClick,
} from '../dist/sw.js';
import { ServiceWorkerManager } from '../dist/sw-register.js';
import { HalpClient } from '@halp/core';

// -----------------------------------------------------------------------------
// 1. Push Event with No Data (Payloadless Default)
// -----------------------------------------------------------------------------

test('Push event with no data renders the fixed notification ("Someone appreciates you.")', async () => {
  let shownTitle = '';
  let shownOptions: NotificationOptions | undefined;

  const mockShow = async (title: string, opts?: NotificationOptions) => {
    shownTitle = title;
    shownOptions = opts;
  };

  const title = await processPushEvent(null, mockShow);

  assert.strictEqual(title, 'Someone appreciates you.');
  assert.strictEqual(shownTitle, 'Someone appreciates you.');
  assert.strictEqual(shownOptions?.tag, 'halp-appreciation');
  assert.strictEqual(shownOptions?.body, undefined, 'Must carry NO body text');
});

// -----------------------------------------------------------------------------
// 2. Push Event with Scaled Counts (Three Copy Bands)
// -----------------------------------------------------------------------------

test('Push event with {"n": N} renders each of the three copy bands correctly', async () => {
  const bands = [
    { n: 1, expected: 'Someone appreciates you.' },
    { n: 7, expected: '7 people appreciate you.' },
    { n: 20, expected: '20 people appreciate you.' },
    { n: 21, expected: '21 people appreciate you.' },
    { n: 412, expected: '412 people appreciate you.' },
  ];

  for (const { n, expected } of bands) {
    let shownTitle = '';
    const mockShow = async (title: string) => { shownTitle = title; };
    const mockData: PushMessageDataLike = {
      json: () => ({ n }),
      text: () => JSON.stringify({ n }),
    };

    const title = await processPushEvent(mockData, mockShow);
    assert.strictEqual(title, expected, `Band n=${n} rendered correctly`);
    assert.strictEqual(shownTitle, expected);
  }
});

// -----------------------------------------------------------------------------
// 3. Cold-Start Path Falls Back to /v1/pending
// -----------------------------------------------------------------------------

test('Cold start without push payload falls back to /v1/pending for count', async () => {
  let shownTitle = '';
  const mockShow = async (title: string) => { shownTitle = title; };

  let fallbackCalled = false;
  const mockFallbackFetcher = async () => {
    fallbackCalled = true;
    return { n: 12 };
  };

  const title = await processPushEvent(null, mockShow, mockFallbackFetcher);

  assert.strictEqual(fallbackCalled, true, 'Fallback /v1/pending was called on cold start');
  assert.strictEqual(title, '12 people appreciate you.');
  assert.strictEqual(shownTitle, '12 people appreciate you.');
});

// -----------------------------------------------------------------------------
// 4. pushsubscriptionchange Re-subscribes and Re-registers
// -----------------------------------------------------------------------------

test('pushsubscriptionchange re-subscribes and re-registers against mock', async () => {
  let registeredEndpoint = '';
  let registeredP256dh = '';
  let registeredAuth = '';

  const mockRegister = async (sub: { endpoint: string; p256dh: string; auth: string }) => {
    registeredEndpoint = sub.endpoint;
    registeredP256dh = sub.p256dh;
    registeredAuth = sub.auth;
  };

  const dummyP256dh = new Uint8Array([1, 2, 3, 4]).buffer;
  const dummyAuth = new Uint8Array([5, 6, 7, 8]).buffer;

  const mockRegistration = {
    pushManager: {
      subscribe: async (opts: any) => {
        assert.strictEqual(opts.userVisibleOnly, true);
        assert.strictEqual(opts.applicationServerKey, 'test-vapid-key');
        return {
          endpoint: 'https://push.browser.test/v1/new-sub',
          getKey: (name: string) => (name === 'p256dh' ? dummyP256dh : dummyAuth),
        };
      },
    },
  } as unknown as ServiceWorkerRegistration;

  await processSubscriptionChange(mockRegistration, 'test-vapid-key', mockRegister);

  assert.strictEqual(registeredEndpoint, 'https://push.browser.test/v1/new-sub');
  assert.ok(registeredP256dh.length > 0, 'p256dh encoded and sent');
  assert.ok(registeredAuth.length > 0, 'auth encoded and sent');
});

// -----------------------------------------------------------------------------
// 5. Notification Click Focuses Existing Tab or Opens App
// -----------------------------------------------------------------------------

test('Notification click focuses existing app tab if open', async () => {
  let focused = false;
  const mockClients = {
    matchAll: async () => [
      {
        url: 'https://halp.to/',
        focus: async () => { focused = true; },
      },
    ],
    openWindow: async () => null,
  } as unknown as Clients;

  await processNotificationClick(mockClients, '/');
  assert.strictEqual(focused, true, 'Focused existing window');
});

test('Notification click opens new window if no tab is open', async () => {
  let openedUrl = '';
  const mockClients = {
    matchAll: async () => [],
    openWindow: async (url: string) => { openedUrl = url; return null; },
  } as unknown as Clients;

  await processNotificationClick(mockClients, '/');
  assert.strictEqual(openedUrl, '/', 'Opened window at root');
});

// -----------------------------------------------------------------------------
// 6. User Gesture Enforcement: Never Requests Permission on Page Load
// -----------------------------------------------------------------------------

test('ServiceWorkerManager registers worker on load WITHOUT requesting permission', async () => {
  let registerCalled = false;
  let permissionPromptCalled = false;

  Object.defineProperty(globalThis, 'navigator', {
    value: {
      serviceWorker: {
        register: async () => {
          registerCalled = true;
          return {} as any;
        },
      },
    },
    configurable: true,
    writable: true,
  });

  (globalThis as any).window = {
    Notification: {
      requestPermission: async () => {
        permissionPromptCalled = true;
        return 'granted';
      },
    },
  };

  const manager = new ServiceWorkerManager(new HalpClient());
  await manager.registerWorker();

  assert.strictEqual(registerCalled, true, 'Worker script registered');
  assert.strictEqual(permissionPromptCalled, false, 'Permission request NEVER called on background registration');
});
