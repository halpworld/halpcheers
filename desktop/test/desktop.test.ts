import { test, describe, before, after, beforeEach } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';

import { DesktopApp } from '../dist/app.js';
import { MockDesktopBridge } from '../dist/bridge.js';
import { HalpClient } from '@halp/core';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const desktopRoot = path.resolve(__dirname, '..');

describe('Desktop App (Tauri v2) DoD Verification', () => {
  describe('1. Packaging & Build Configuration (tauri.conf.json)', () => {
    const configPath = path.join(desktopRoot, 'src-tauri', 'tauri.conf.json');
    const config = JSON.parse(fs.readFileSync(configPath, 'utf8'));

    test('declares all four target bundles: dmg, msi, appimage, deb', () => {
      const targets = config.bundle?.targets;
      assert.ok(Array.isArray(targets), 'bundle.targets must be an array');
      assert.ok(targets.includes('dmg'), 'missing dmg target for macOS');
      assert.ok(targets.includes('msi'), 'missing msi target for Windows');
      assert.ok(targets.includes('appimage'), 'missing appimage target for Linux');
      assert.ok(targets.includes('deb'), 'missing deb target for Debian/Ubuntu Linux');
    });

    test('enforces strict Content Security Policy with zero remote scripts or CDNs', () => {
      const csp = config.app?.security?.csp;
      assert.ok(csp, 'CSP must be configured');
      assert.match(csp, /default-src 'self'/);
      assert.match(csp, /connect-src 'self' https:\/\/halp\.to/);
      assert.doesNotMatch(csp, /https:\/\/fonts\.googleapis\.com/);
      assert.doesNotMatch(csp, /https:\/\/cdn\./);
      assert.doesNotMatch(csp, /analytics/);
    });

    test('configures system tray icon', () => {
      assert.ok(config.app?.trayIcon, 'tray icon must be defined in app.trayIcon');
    });
  });

  describe('2. Cargo Dependencies & Native Crates (Cargo.toml)', () => {
    const cargoPath = path.join(desktopRoot, 'src-tauri', 'Cargo.toml');
    const cargoContent = fs.readFileSync(cargoPath, 'utf8');

    test('includes keyring crate for OS credential store integration', () => {
      assert.match(cargoContent, /keyring\s*=\s*"/);
    });

    test('includes AES-256-GCM and HKDF for encrypted local fallback (Decision 40)', () => {
      assert.match(cargoContent, /aes-gcm\s*=\s*"/);
      assert.match(cargoContent, /hkdf\s*=\s*"/);
      assert.match(cargoContent, /sha2\s*=\s*"/);
    });

    test('includes tauri-plugin-notification for native OS notifications', () => {
      assert.match(cargoContent, /tauri-plugin-notification\s*=\s*"/);
    });
  });

  describe('3. Native Notification Copy Bands (Rust & Docs UI.md §8)', () => {
    const notifPath = path.join(desktopRoot, 'src-tauri', 'src', 'notification.rs');
    const notifCode = fs.readFileSync(notifPath, 'utf8');

    test('notification implementation contains UI.md §8 copy bands verbatim', () => {
      assert.ok(notifCode.includes('Someone appreciates you.'));
      assert.ok(notifCode.includes('people appreciate you.'));
      // Verifies zero body text parameter
      assert.doesNotMatch(notifCode, /\.body\(/);
    });
  });

  describe('4. Zero Telemetry, Analytics, and Crash Reporting Audit', () => {
    test('strictly no telemetry, tracking, or crash-reporting SDKs across desktop files', () => {
      const forbiddenTokens = [
        'sentry',
        'telemetry',
        'google-analytics',
        'mixpanel',
        'datadog',
        'bugsnag',
        'amplitude',
        'segment',
        'posthog',
      ];

      function scanDir(dir: string): void {
        const entries = fs.readdirSync(dir, { withFileTypes: true });
        for (const entry of entries) {
          if (entry.name === 'node_modules' || entry.name === 'dist' || entry.name === '.git' || entry.name === 'test') {
            continue;
          }
          const fullPath = path.join(dir, entry.name);
          if (entry.isDirectory()) {
            scanDir(fullPath);
          } else if (entry.isFile() && (entry.name.endsWith('.ts') || entry.name.endsWith('.rs') || entry.name.endsWith('.json'))) {
            const content = fs.readFileSync(fullPath, 'utf8').toLowerCase();
            for (const token of forbiddenTokens) {
              assert.ok(
                !content.includes(token),
                `Found forbidden telemetry token "${token}" in ${path.relative(desktopRoot, fullPath)}`
              );
            }
          }
        }
      }

      scanDir(desktopRoot);
    });
  });

  describe('5. Desktop Controller & Key Wall Gating', () => {
    let bridge: MockDesktopBridge;
    let originalDocument: any;
    let domElements: Record<string, any>;

    before(() => {
      originalDocument = (globalThis as any).document;
    });

    after(() => {
      (globalThis as any).document = originalDocument;
    });

    beforeEach(() => {
      bridge = new MockDesktopBridge();
      domElements = {
        'key-wall': { style: { display: 'none' } },
        'home-view': { style: { display: 'none' } },
        'key-display': { textContent: '' },
        'key-saved-check': { checked: false, onchange: null as any },
        'key-continue-btn': { disabled: true, onclick: null as any },
        'today-count': { textContent: '0' },
        'today-caption': { textContent: '' },
        'target-input': { value: '' },
        'send-btn': { disabled: false, textContent: 'Send' },
        'handle-display': { textContent: 'e0123456789ab' },
        'clear-key-btn': { onclick: null as any },
        'copy-handle-btn': { onclick: null as any },
        'connection-dot': { className: '', classList: { add: (c: string) => { domElements['connection-dot'].className += ` ${c}`; } } },
        'connection-text': { textContent: '' },
        'aria-announcer': { textContent: '' },
        'send-form': { onsubmit: null as any },
      };

      (globalThis as any).document = {
        getElementById: (id: string) => domElements[id] || null,
      };
    });

    test('initializes to Key Wall when no account key exists in keychain', async () => {
      const app = new DesktopApp(bridge);
      await app.init();

      assert.equal(domElements['key-wall'].style.display, 'flex');
      assert.equal(domElements['home-view'].style.display, 'none');
      assert.ok(domElements['key-display'].textContent.length >= 19, 'displays formatted 16-digit key');
      assert.equal(domElements['key-continue-btn'].disabled, true, 'Continue is gated before checkbox');

      // Toggling checkbox enables continue
      domElements['key-saved-check'].checked = true;
      domElements['key-saved-check'].onchange();
      assert.equal(domElements['key-continue-btn'].disabled, false, 'Continue is enabled once checked');

      // Clicking continue saves key in keychain and transitions to Home
      await domElements['key-continue-btn'].onclick();
      const savedKey = await bridge.getAccountKey();
      assert.ok(savedKey, 'Account key must be stored in keychain');
      assert.equal(savedKey.length, 16);
      assert.equal(domElements['key-wall'].style.display, 'none');
      assert.equal(domElements['home-view'].style.display, 'flex');
    });

    test('initializes directly to Home View when key exists in keychain', async () => {
      await bridge.saveAccountKey('1234567890123456');
      const app = new DesktopApp(bridge);
      await app.init();

      assert.equal(domElements['key-wall'].style.display, 'none');
      assert.equal(domElements['home-view'].style.display, 'flex');
    });
  });

  describe('6. Send Uniformity & Latency Floor', () => {
    let bridge: MockDesktopBridge;
    let mockClient: HalpClient;

    beforeEach(() => {
      bridge = new MockDesktopBridge();
      mockClient = new HalpClient({ baseUrl: 'https://halp.to' });
    });

    test('enforces >= 300 ms floor and exact two-state button ("Sending...", "Sent.")', async () => {
      const domElements: Record<string, any> = {
        'aria-announcer': { textContent: '' },
      };
      (globalThis as any).document = {
        getElementById: (id: string) => domElements[id] || null,
      };

      // Mock client sendPing with near-zero latency
      mockClient.sendPing = async () => ({ status: 'accepted' });

      const app = new DesktopApp(bridge, mockClient);
      const btn = { disabled: false, textContent: 'Send' } as any;
      const input = { value: 'e0123456789ab' } as any;

      const t0 = Date.now();
      await app.handleSend('e0123456789ab', btn, input);
      const elapsed = Date.now() - t0;

      assert.ok(elapsed >= 280, `Perceived latency floor must be >= 300 ms (was ${elapsed} ms)`);
      assert.equal(btn.textContent, 'Sent.', 'Button must display "Sent." upon completion');
      assert.equal(input.value, '', 'Input must be cleared after sending');
      assert.equal(domElements['aria-announcer'].textContent, 'Appreciation sent.');
    });

    test('enforces identical button state and floor even on network drop/error', async () => {
      const domElements: Record<string, any> = {
        'aria-announcer': { textContent: '' },
      };
      (globalThis as any).document = {
        getElementById: (id: string) => domElements[id] || null,
      };

      // Invariant 7: Network errors/drops must not reveal status to sender
      mockClient.sendPing = async () => {
        throw new Error('Connection failed');
      };

      const app = new DesktopApp(bridge, mockClient);
      const btn = { disabled: false, textContent: 'Send' } as any;
      const input = { value: 'e0123456789ab' } as any;

      const t0 = Date.now();
      await app.handleSend('e0123456789ab', btn, input);
      const elapsed = Date.now() - t0;

      assert.ok(elapsed >= 280, `Latency floor must still hold on errors (was ${elapsed} ms)`);
      assert.equal(btn.textContent, 'Sent.', 'Button must still show "Sent." to prevent inference');
    });
  });

  describe('7. Real-Time Arrival Handling & Notifications', () => {
    let bridge: MockDesktopBridge;

    beforeEach(() => {
      bridge = new MockDesktopBridge();
    });

    test('incoming ping updates DOM, triggers native notification, and updates tray count', () => {
      const domElements: Record<string, any> = {
        'today-count': { textContent: '0' },
        'today-caption': { textContent: '' },
      };
      (globalThis as any).document = {
        getElementById: (id: string) => domElements[id] || null,
      };

      const app = new DesktopApp(bridge);
      app.handleArrival(5);

      assert.equal(domElements['today-count'].textContent, '5');
      assert.equal(domElements['today-caption'].textContent, '5 people appreciate you today.');
      assert.deepEqual(bridge.notificationHistory, [5], 'Dispatched native notification for arrival count 5');
      assert.deepEqual(bridge.trayCountHistory, [5], 'Dispatched tray count update for arrival count 5');
    });
  });
});
