/**
 * Service Worker registration helper.
 *
 * SPECIFICATION (Issue #22, docs/DELIVERY.md):
 * - Permission is requested strictly after a user gesture, never on first paint/load.
 * - Subscribes with the VAPID applicationServerKey.
 * - Registers the subscription via POST /v1/subscriptions.
 */

import { HalpClient } from '@halp/core';

export interface RegistrationResult {
  subscribed: boolean;
  subscriptionId?: number;
}

export class ServiceWorkerManager {
  private client: HalpClient;
  private swPath: string;

  constructor(client: HalpClient, swPath: string = '/sw.js') {
    this.client = client;
    this.swPath = swPath;
  }

  /**
   * Registers the service worker script in the background without requesting permission yet.
   */
  async registerWorker(): Promise<ServiceWorkerRegistration | null> {
    if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) {
      return null;
    }
    return navigator.serviceWorker.register(this.swPath);
  }

  /**
   * Requests push notification permissions strictly in response to an explicit user gesture.
   */
  async enableNotificationsWithUserGesture(vapidPublicKey: string): Promise<RegistrationResult> {
    if (typeof window === 'undefined' || !('Notification' in window) || !('serviceWorker' in navigator)) {
      return { subscribed: false };
    }

    const permission = await Notification.requestPermission();
    if (permission !== 'granted') {
      return { subscribed: false };
    }

    const reg = await navigator.serviceWorker.ready;
    let sub = await reg.pushManager.getSubscription();

    if (!sub) {
      sub = await reg.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: vapidPublicKey,
      });
    }

    const rawP256dh = sub.getKey('p256dh');
    const rawAuth = sub.getKey('auth');

    if (rawP256dh && rawAuth) {
      const p256dh = btoa(String.fromCharCode(...new Uint8Array(rawP256dh)));
      const auth = btoa(String.fromCharCode(...new Uint8Array(rawAuth)));

      const res = await this.client.createSubscription({
        endpoint: sub.endpoint,
        p256dh,
        auth,
        kind: 'webpush',
      });

      return {
        subscribed: true,
        subscriptionId: res.id,
      };
    }

    return { subscribed: false };
  }
}
