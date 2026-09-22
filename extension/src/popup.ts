/**
 * Extension popup controller.
 *
 * SPECIFICATION (docs/UI.md §10, docs/UI.md ## Uniformity is a UI requirement):
 * - Send button: exactly two states ("Sending...", "Sent.") and 300 ms floor.
 * - Displays today's arrival count.
 * - Quick send to saved handles.
 * - Account key never exposed to page scripts.
 */

import { HalpClient } from '@halp/core';
import { extensionApi } from './shim.js';

const MIN_LATENCY_FLOOR_MS = 300;
const DEFAULT_ORIGIN = 'https://halp.to';

export class PopupController {
  private client: HalpClient;
  private savedHandles: string[] = [];

  constructor(client?: HalpClient) {
    this.client = client ?? new HalpClient({ baseUrl: DEFAULT_ORIGIN });
  }

  async init(): Promise<void> {
    const key = await extensionApi.storage.get('halp_account_key');
    if (key) {
      try {
        await this.client.loginWithAccountKey(key);
      } catch {
        // Fallback or retry
      }
    }

    await this.refreshArrivals();
    await this.loadSavedHandles();
    this.bindEvents();
  }

  async refreshArrivals(): Promise<number> {
    const box = document.getElementById('arrival-count');
    try {
      const res = await this.client.getPending();
      const n = res?.n ?? 0;
      if (box) {
        if (n === 0) {
          box.textContent = 'No appreciation pings yet today.';
        } else if (n === 1) {
          box.textContent = 'Someone appreciates you.';
        } else {
          box.textContent = `${n} people appreciate you.`;
        }
      }
      return n;
    } catch {
      if (box) box.textContent = 'Unable to load arrivals.';
      return 0;
    }
  }

  async loadSavedHandles(): Promise<void> {
    const raw = await extensionApi.storage.get('halp_saved_handles');
    if (raw) {
      try {
        this.savedHandles = JSON.parse(raw);
      } catch {
        this.savedHandles = [];
      }
    }
    this.renderSavedHandles();
  }

  renderSavedHandles(): void {
    const list = document.getElementById('saved-list');
    if (!list) return;
    list.innerHTML = '';

    for (const h of this.savedHandles) {
      const row = document.createElement('div');
      row.className = 'handle-row';

      const tag = document.createElement('span');
      tag.textContent = h;

      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'quick-send-btn';
      btn.textContent = 'Send';
      btn.onclick = () => this.sendToTarget(h, btn);

      row.appendChild(tag);
      row.appendChild(btn);
      list.appendChild(row);
    }
  }

  async sendToTarget(target: string, btn: HTMLButtonElement): Promise<void> {
    const live = document.getElementById('status');
    btn.disabled = true;
    btn.textContent = 'Sending...';

    const start = performance.now();

    try {
      await this.client.sendPing(target);
      const elapsed = performance.now() - start;
      const delay = Math.max(0, MIN_LATENCY_FLOOR_MS - elapsed);

      setTimeout(() => {
        btn.textContent = 'Sent.';
        if (live) live.textContent = 'Sent.';

        // Save handle to quick send list if new
        if (!this.savedHandles.includes(target)) {
          this.savedHandles.unshift(target);
          if (this.savedHandles.length > 5) this.savedHandles.pop();
          extensionApi.storage.set('halp_saved_handles', JSON.stringify(this.savedHandles));
          this.renderSavedHandles();
        }

        setTimeout(() => {
          btn.disabled = false;
          btn.textContent = 'Send';
        }, 2000);
      }, delay);
    } catch {
      const elapsed = performance.now() - start;
      const delay = Math.max(0, MIN_LATENCY_FLOOR_MS - elapsed);

      setTimeout(() => {
        btn.disabled = false;
        btn.textContent = 'Send';
        if (live) live.textContent = 'Something went wrong. Try again.';
      }, delay);
    }
  }

  bindEvents(): void {
    const form = document.getElementById('send-form') as HTMLFormElement | null;
    const input = document.getElementById('target-input') as HTMLInputElement | null;
    const sendBtn = document.getElementById('send-btn') as HTMLButtonElement | null;

    if (form && input && sendBtn) {
      form.onsubmit = (e) => {
        e.preventDefault();
        const target = input.value.trim();
        if (target) {
          this.sendToTarget(target, sendBtn).then(() => {
            input.value = '';
          });
        }
      };
    }
  }
}

if (typeof document !== 'undefined') {
  document.addEventListener('DOMContentLoaded', () => {
    const controller = new PopupController();
    controller.init();
  });
}
