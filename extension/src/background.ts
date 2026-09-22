/**
 * MV3 Background Service Worker / Script for Chrome & Firefox.
 *
 * SPECIFICATION (docs/UI.md §10, docs/DELIVERY.md, docs/OPEN-QUESTIONS.md #39):
 * - Alarms poll /v1/pending in background to synchronize toolbar badge.
 * - Shows notification on arrival without sender info.
 * - Account key is stored in storage.local, never exposed to page scripts.
 */

import { extensionApi } from './shim.js';

declare const chrome: any;
declare const browser: any;

const ALARM_NAME = 'halp-sync-pending';
const DEFAULT_ORIGIN = 'https://halp.to';

export async function syncPendingArrivals(origin: string = DEFAULT_ORIGIN): Promise<number> {
  const token = await extensionApi.storage.get('halp_session_token');
  if (!token) {
    return 0;
  }

  try {
    const res = await fetch(`${origin}/v1/pending`, {
      headers: {
        'Authorization': `Bearer ${token}`,
        'Accept': 'application/json',
      },
    });

    if (res.ok) {
      const data = await res.json();
      const count = typeof data.n === 'number' ? data.n : 0;
      if (count > 0) {
        extensionApi.setBadge(count > 99 ? '99+' : count.toString());
        const title = count === 1 ? 'Someone appreciates you.' : `${count} people appreciate you.`;
        extensionApi.showNotification(title);
      } else {
        extensionApi.setBadge('');
      }
      return count;
    }
  } catch {
    // Silent failure on offline/network drops
  }
  return 0;
}

// Setup alarms
const alarms = typeof browser !== 'undefined' ? browser.alarms : typeof chrome !== 'undefined' ? chrome.alarms : null;
if (alarms) {
  alarms.create(ALARM_NAME, { periodInMinutes: 15 });
  alarms.onAlarm.addListener((alarm: any) => {
    if (alarm.name === ALARM_NAME) {
      syncPendingArrivals();
    }
  });
}
