/**
 * Halp Desktop Application Controller.
 *
 * SPECIFICATION (docs/UI.md §9, docs/DELIVERY.md, docs/OPEN-QUESTIONS.md #40):
 * - Key wall gated by checkbox confirmation on first run.
 * - Keychain storage via DesktopBridge (Tauri IPC).
 * - Real-time receiving over SSE with automatic fallback to polling /v1/pending on idle demotion.
 * - Jittered exponential backoff for silent network recovery.
 * - Native OS notifications matching UI.md §8 copy bands (zero body, zero sender).
 * - System tray integration with arrival counter.
 * - Quick send with 300 ms floor and two-state button ("Sending...", "Sent.").
 */

import {
  HalpClient,
  TransportManager,
  TransportStatus,
  generateAccountKey,
  deriveAuthSecretHex,
  normalizeAccountKey,
} from '@halp/core';
import { DesktopBridge, createDesktopBridge } from './bridge.js';

const MIN_LATENCY_FLOOR_MS = 300;
const DEFAULT_ORIGIN = 'https://halp.to';

export class DesktopApp {
  private bridge: DesktopBridge;
  private client: HalpClient;
  private transport: TransportManager | null = null;
  private accountKey: string | null = null;
  private authSecret: string | null = null;
  private todayCount = 0;
  private defaultHandle = '';

  constructor(bridge?: DesktopBridge, client?: HalpClient) {
    this.bridge = bridge ?? createDesktopBridge();
    this.client = client ?? new HalpClient({ baseUrl: DEFAULT_ORIGIN });
  }

  async init(): Promise<void> {
    const existingKey = await this.bridge.getAccountKey();

    if (existingKey) {
      const cleanKey = normalizeAccountKey(existingKey);
      await this.startHomeView(cleanKey);
    } else {
      this.showKeyWall();
    }

    this.bindWindowEvents();
  }

  private showKeyWall(): void {
    const keyWall = document.getElementById('key-wall');
    const homeView = document.getElementById('home-view');
    const keyDisplay = document.getElementById('key-display');
    const savedCheck = document.getElementById('key-saved-check') as HTMLInputElement | null;
    const continueBtn = document.getElementById('key-continue-btn') as HTMLButtonElement | null;

    if (keyWall) keyWall.style.display = 'flex';
    if (homeView) homeView.style.display = 'none';

    // Generate fresh 16-digit account key
    const freshKey = generateAccountKey();
    if (keyDisplay) {
      keyDisplay.textContent = freshKey.replace(/(\d{4})(?=\d)/g, '$1 ');
    }

    if (savedCheck && continueBtn) {
      savedCheck.checked = false;
      continueBtn.disabled = true;

      savedCheck.onchange = () => {
        continueBtn.disabled = !savedCheck.checked;
      };

      continueBtn.onclick = async () => {
        await this.bridge.saveAccountKey(freshKey);
        await this.startHomeView(freshKey);
      };
    }
  }

  async startHomeView(key: string): Promise<void> {
    this.accountKey = key;
    this.authSecret = await deriveAuthSecretHex(key);

    const keyWall = document.getElementById('key-wall');
    const homeView = document.getElementById('home-view');

    if (keyWall) keyWall.style.display = 'none';
    if (homeView) homeView.style.display = 'flex';

    this.bindHomeEvents();
    this.startTransport();
  }

  private bindHomeEvents(): void {
    const sendForm = document.getElementById('send-form') as HTMLFormElement | null;
    const sendBtn = document.getElementById('send-btn') as HTMLButtonElement | null;
    const targetInput = document.getElementById('target-input') as HTMLInputElement | null;
    const clearKeyBtn = document.getElementById('clear-key-btn') as HTMLButtonElement | null;
    const copyHandleBtn = document.getElementById('copy-handle-btn') as HTMLButtonElement | null;

    if (sendForm && sendBtn && targetInput) {
      sendForm.onsubmit = async (e) => {
        e.preventDefault();
        const target = targetInput.value.trim();
        if (!target) return;

        await this.handleSend(target, sendBtn, targetInput);
      };
    }

    if (clearKeyBtn) {
      clearKeyBtn.onclick = async () => {
        if (confirm('Log out from this device? Make sure you have your 16-digit key saved.')) {
          this.stopTransport();
          await this.bridge.clearAccountKey();
          this.accountKey = null;
          this.authSecret = null;
          this.showKeyWall();
        }
      };
    }

    if (copyHandleBtn) {
      copyHandleBtn.onclick = async () => {
        const handleDisplay = document.getElementById('handle-display');
        const text = handleDisplay?.textContent || '';
        if (text && text !== 'Loading...') {
          await navigator.clipboard.writeText(`https://halp.to/${text}`);
          copyHandleBtn.textContent = 'Copied!';
          setTimeout(() => {
            copyHandleBtn.textContent = 'Copy Link';
          }, 1500);
        }
      };
    }
  }

  async handleSend(
    target: string,
    btn: HTMLButtonElement,
    input?: HTMLInputElement
  ): Promise<void> {
    const originalText = 'Send';
    btn.disabled = true;
    btn.textContent = 'Sending...';

    const startTime = Date.now();

    try {
      await this.client.sendPing(target);
    } catch {
      // Invariant 7: Enforcement is invisible to the sender.
      // Drops, rate limits, blocks and accepted pings all return identical UI state.
    } finally {
      const elapsed = Date.now() - startTime;
      if (elapsed < MIN_LATENCY_FLOOR_MS) {
        await new Promise((r) => setTimeout(r, MIN_LATENCY_FLOOR_MS - elapsed));
      }

      btn.textContent = 'Sent.';
      this.announceAria('Appreciation sent.');

      if (input) {
        input.value = '';
      }

      setTimeout(() => {
        btn.textContent = originalText;
        btn.disabled = false;
      }, 2000);
    }
  }

  startTransport(): void {
    if (this.transport) {
      this.transport.close();
    }

    this.transport = new TransportManager({
      client: this.client,
      events: {
        onPing: (count) => this.handleArrival(count),
        onStatusChange: (status) => this.handleStatusChange(status),
        onError: () => {},
      },
    });

    this.transport.start();
  }

  stopTransport(): void {
    if (this.transport) {
      this.transport.close();
      this.transport = null;
    }
  }

  handleArrival(count: number): void {
    this.todayCount = count;

    // Update UI counter
    const todayCountEl = document.getElementById('today-count');
    const todayCaptionEl = document.getElementById('today-caption');

    if (todayCountEl) {
      todayCountEl.textContent = count.toString();
    }

    if (todayCaptionEl) {
      todayCaptionEl.textContent =
        count === 1
          ? 'Someone appreciates you today.'
          : `${count} people appreciate you today.`;
    }

    // Trigger native OS notification (docs/UI.md §8)
    this.bridge.showNativeNotification(count).catch(() => {});

    // Update system tray count
    this.bridge.updateTrayCount(count).catch(() => {});
  }

  handleStatusChange(status: TransportStatus): void {
    const dot = document.getElementById('connection-dot');
    const text = document.getElementById('connection-text');

    if (!dot || !text) return;

    dot.className = 'connection-dot';

    switch (status) {
      case 'connected':
        dot.classList.add('connected');
        text.textContent = 'Connected';
        break;
      case 'polling':
        dot.classList.add('polling');
        text.textContent = 'Idle (polling)';
        break;
      case 'connecting':
        dot.classList.add('reconnecting');
        text.textContent = 'Connecting...';
        break;
      case 'disconnected':
      case 'closed':
        text.textContent = 'Disconnected';
        break;
    }
  }

  private announceAria(msg: string): void {
    const el = document.getElementById('aria-announcer');
    if (el) {
      el.textContent = msg;
    }
  }

  private bindWindowEvents(): void {
    if (typeof window !== 'undefined') {
      window.addEventListener('trigger-send-default', () => {
        if (this.defaultHandle) {
          const sendBtn = document.getElementById('send-btn') as HTMLButtonElement | null;
          if (sendBtn && !sendBtn.disabled) {
            this.handleSend(this.defaultHandle, sendBtn);
          }
        }
      });
    }
  }
}

// Auto-bootstrap in browser environment
if (typeof window !== 'undefined' && document.getElementById('app')) {
  const app = new DesktopApp();
  app.init().catch(console.error);
}
