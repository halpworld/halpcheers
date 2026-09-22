/**
 * Service Worker for Halp Web Push.
 *
 * SPECIFICATION (docs/DELIVERY.md, docs/UI.md §8, AGENTS.md):
 * - Zero payload by default: renders "Someone appreciates you." with NO body, NO buttons, NO sender line.
 * - Payload with {"n": N}: renders scaled title from docs/UI.md §8 (1 / 2–20 / 21+).
 * - Cold start: falls back to querying GET /v1/pending if waking with no payload.
 * - pushsubscriptionchange: re-subscribes with applicationServerKey and calls POST /v1/subscriptions.
 * - notificationclick: focuses existing tab or opens window to home view.
 * - INVARIANT 6: Never reveal sender (no sender names, avatars, handles, or action buttons).
 */

export function getNotificationTitle(count: number): string {
  if (count <= 1) {
    return 'Someone appreciates you.';
  }
  return `${count} people appreciate you.`;
}

// Global declaration for Service Worker scope
declare const self: any;

export interface PushMessageDataLike {
  json(): any;
  text(): string;
}

export interface PushEventLike {
  data?: PushMessageDataLike | null;
  waitUntil(promise: Promise<any>): void;
}

export interface PushSubscriptionChangeLike {
  oldSubscription?: PushSubscription | null;
  newSubscription?: PushSubscription | null;
  waitUntil(promise: Promise<any>): void;
}

export interface NotificationEventLike {
  notification: {
    close(): void;
  };
  waitUntil(promise: Promise<any>): void;
}

/**
 * Core push event processor. Separated for testability without a live browser SW runtime.
 */
export async function processPushEvent(
  eventData: PushMessageDataLike | null | undefined,
  showNotification: (title: string, options?: NotificationOptions) => Promise<void>,
  pendingFallbackFetcher?: () => Promise<{ n: number }>
): Promise<string> {
  let title = 'Someone appreciates you.';

  if (eventData) {
    try {
      const parsed = eventData.json();
      if (parsed && typeof parsed.n === 'number') {
        title = getNotificationTitle(parsed.n);
      }
    } catch {
      // Fallback for non-JSON text or corrupt payload
      title = 'Someone appreciates you.';
    }
  } else if (pendingFallbackFetcher) {
    // Cold start with no payload: synchronize count from /v1/pending
    try {
      const res = await pendingFallbackFetcher();
      if (res && typeof res.n === 'number' && res.n > 0) {
        title = getNotificationTitle(res.n);
      }
    } catch {
      title = 'Someone appreciates you.';
    }
  }

  // Strictly no body, no sender, no action buttons
  await showNotification(title, {
    tag: 'halp-appreciation',
    renotify: true,
  } as any);

  return title;
}

/**
 * Handles pushsubscriptionchange event to re-subscribe and register endpoint.
 */
export async function processSubscriptionChange(
  registration: any,
  applicationServerKey: string,
  registerEndpoint: (sub: { endpoint: string; p256dh: string; auth: string }) => Promise<void>
): Promise<void> {
  const newSub = await registration.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey,
  });

  const rawP256dh = newSub.getKey('p256dh');
  const rawAuth = newSub.getKey('auth');

  if (rawP256dh && rawAuth) {
    const p256dh = btoa(String.fromCharCode(...new Uint8Array(rawP256dh)));
    const auth = btoa(String.fromCharCode(...new Uint8Array(rawAuth)));
    await registerEndpoint({
      endpoint: newSub.endpoint,
      p256dh,
      auth,
    });
  }
}

/**
 * Handles notification clicks: focuses an open app window or opens a new one.
 */
export async function processNotificationClick(
  clients: any,
  appUrl: string = '/'
): Promise<void> {
  const windowClients = await clients.matchAll({
    type: 'window',
    includeUncontrolled: true,
  });

  for (const client of windowClients) {
    if (client.url.includes(appUrl) && 'focus' in client) {
      await client.focus();
      return;
    }
  }

  if (clients.openWindow) {
    await clients.openWindow(appUrl);
  }
}

// Attach event listeners when running inside actual Service Worker context
if (typeof self !== 'undefined' && 'addEventListener' in self && typeof (self as any).skipWaiting === 'function') {
  self.addEventListener('install', () => {
    (self as any).skipWaiting();
  });

  self.addEventListener('activate', (e: any) => {
    e.waitUntil((self as any).clients.claim());
  });

  self.addEventListener('push', (e: any) => {
    const fetchPending = async () => {
      const res = await fetch('/v1/pending');
      return res.json();
    };

    e.waitUntil(
      processPushEvent(
        e.data,
        (title, opts) => self.registration.showNotification(title, opts),
        fetchPending
      )
    );
  });

  self.addEventListener('pushsubscriptionchange', (e: any) => {
    const registerEndpoint = async (sub: { endpoint: string; p256dh: string; auth: string }) => {
      await fetch('/v1/subscriptions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(sub),
      });
    };

    e.waitUntil(
      processSubscriptionChange(
        self.registration,
        'halp-vapid-key',
        registerEndpoint
      )
    );
  });

  self.addEventListener('notificationclick', (e: any) => {
    e.notification.close();
    e.waitUntil(processNotificationClick((self as any).clients, '/'));
  });
}
