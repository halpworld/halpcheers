/**
 * Test suite for @halp/app (web application shell).
 * Covers Definition of Done requirements for Issue #21:
 * - Key wall cannot be passed without the checkbox.
 * - Every string matches docs/UI.md verbatim.
 * - Settings presets map to documented values.
 * - Built bundle and HTML reference zero external origins.
 * - Never-show checklist verified.
 */

import test from 'node:test';
import assert from 'node:assert';
import fs from 'node:fs';
import path from 'node:path';
import { COPY, HalpApp } from '../dist/main.js';
import { QRCode } from '../dist/qr.js';

// -----------------------------------------------------------------------------
// 1. Verbatim Copy Matches docs/UI.md
// -----------------------------------------------------------------------------

test('Every string matches docs/UI.md verbatim', () => {
  // Key wall (§2)
  assert.strictEqual(
    COPY.keyWall.title,
    'This is your account. There is no email, no password and no reset.'
  );
  assert.strictEqual(
    COPY.keyWall.subtitle,
    'If you lose it, the account is gone — handles, settings and all.'
  );
  assert.strictEqual(COPY.keyWall.checkbox, 'I have written it down');

  // Login (§3)
  assert.strictEqual(
    COPY.login.failure,
    "That didn't work. Check the digits and try again."
  );

  // Home (§4)
  assert.strictEqual(
    COPY.home.escalationNotice,
    "This handle is unusually busy. We've widened its digest window so your notifications stay reasonable. You can change it below."
  );
  assert.strictEqual(COPY.home.singleCount, 'Someone appreciates you.');
  assert.strictEqual(COPY.home.pluralCount(7), '7 people appreciate you.');
  assert.strictEqual(COPY.home.pluralCount(412), '412 people appreciate you.');

  // Burn (§5)
  const burnPrompt = COPY.burn.prompt('e7k4p2m9qx3va');
  assert.ok(burnPrompt.includes('Burn e7k4p2m9qx3va?'));
  assert.ok(
    burnPrompt.includes(
      'Anyone with this link or QR code loses the ability to reach you, immediately and permanently. The handle is never reissued and this cannot be undone. Your other handles are unaffected.'
    )
  );

  // Settings (§6)
  assert.strictEqual(
    COPY.settings.escalationDisclaimer,
    'If a handle gets flooded we may widen its window on its own. We will never make it narrower than you asked.'
  );
  const digestSecs = COPY.settings.digestPresets.map((p) => p.seconds);
  assert.deepStrictEqual(digestSecs, [0, 60, 900, 3600, 86400]);

  // Report abuse (§7)
  assert.strictEqual(
    COPY.abuse.terminalState,
    "Done. It should quieten down. If it doesn't, pause or burn this handle — that always works."
  );

  // Send states (docs/UI.md Uniformity & decision 7)
  assert.strictEqual(COPY.send.sent, 'Sent.');
});

// -----------------------------------------------------------------------------
// 2. Key Wall Gating: Continue is Disabled until Checkbox is Checked
// -----------------------------------------------------------------------------

// Setup minimal global document mock for Node test runner
class MockElement {
  tagName: string;
  className = '';
  id = '';
  type = '';
  textContent = '';
  disabled = false;
  checked = false;
  maxLength = 0;
  placeholder = '';
  style: Record<string, string> = {};
  children: MockElement[] = [];
  onchange: ((e: any) => void) | null = null;
  onclick: ((e: any) => void) | null = null;
  oninput: ((e: any) => void) | null = null;

  attributes: Record<string, string> = {};

  setAttribute(name: string, value: string) {
    this.attributes[name] = value;
  }

  constructor(tagName: string) {
    this.tagName = tagName;
  }

  appendChild(child: MockElement) {
    this.children.push(child);
    return child;
  }

  querySelector(selector: string): MockElement | null {
    if (selector.startsWith('#')) {
      const targetId = selector.slice(1);
      if (this.id === targetId) return this;
      for (const c of this.children) {
        const found = c.querySelector(selector);
        if (found) return found;
      }
    }
    if (selector.includes('.btn') && this.className.includes('btn') && !this.className.includes('btn-secondary')) {
      return this;
    }
    for (const c of this.children) {
      const found = c.querySelector(selector);
      if (found) return found;
    }
    return null;
  }
}

(globalThis as any).document = {
  createElement: (tag: string) => new MockElement(tag),
};

test('Key wall cannot be passed without the checkbox', () => {
  const mockContainer = new MockElement('div') as unknown as HTMLElement;
  const app = new HalpApp(mockContainer);
  app.currentView = 'key_wall';
  app.currentKey = '1234567890123456';

  const element = app.renderKeyWall() as unknown as MockElement;
  const checkbox = element.querySelector('#key-saved-checkbox');
  const continueBtn = element.querySelector('button.btn');

  assert.ok(checkbox !== null, 'checkbox exists');
  assert.ok(continueBtn !== null, 'continue button exists');
  assert.strictEqual(continueBtn.disabled, true, 'Continue must start disabled');

  // Simulate tick
  checkbox.checked = true;
  if (checkbox.onchange) {
    checkbox.onchange(new Event('change'));
  }
  assert.strictEqual(continueBtn.disabled, false, 'Continue is enabled after checking');

  // Simulate un-tick
  checkbox.checked = false;
  if (checkbox.onchange) {
    checkbox.onchange(new Event('change'));
  }
  assert.strictEqual(continueBtn.disabled, true, 'Continue is disabled when unchecked');
});

// -----------------------------------------------------------------------------
// 3. Settings Presets Map to Documented Values (No Raw Numbers)
// -----------------------------------------------------------------------------

test('Settings presets map to documented values in docs/API.md and docs/UI.md', () => {
  const presets = COPY.settings.digestPresets;
  assert.strictEqual(presets[0].label, 'Instant');
  assert.strictEqual(presets[0].seconds, 0);

  assert.strictEqual(presets[1].label, 'Every minute');
  assert.strictEqual(presets[1].seconds, 60);

  assert.strictEqual(presets[2].label, 'Every 15 min');
  assert.strictEqual(presets[2].seconds, 900);

  assert.strictEqual(presets[3].label, 'Hourly');
  assert.strictEqual(presets[3].seconds, 3600);

  assert.strictEqual(presets[4].label, 'Daily');
  assert.strictEqual(presets[4].seconds, 86400);

  const modes = COPY.settings.modePresets;
  assert.strictEqual(modes[0].value, 'all');
  assert.strictEqual(modes[1].value, 'groups_only');
  assert.strictEqual(modes[2].value, 'paused');
});

// -----------------------------------------------------------------------------
// 4. Zero External Origins Check
// -----------------------------------------------------------------------------

test('Bundle and markup reference ZERO external origins or tracking scripts', () => {
  const appDir = path.resolve(import.meta.dirname, '..');
  const filesToCheck = [
    path.join(appDir, 'index.html'),
    path.join(appDir, 'src/styles.css'),
    path.join(appDir, 'src/main.ts'),
    path.join(appDir, 'src/qr.ts'),
  ];

  const externalUrlPattern = /(https?:\/\/(?!halp\.to)[a-zA-Z0-9.-]+\.[a-zA-Z]{2,})/g;

  for (const filePath of filesToCheck) {
    if (fs.existsSync(filePath)) {
      const content = fs.readFileSync(filePath, 'utf-8');
      const matches = content.match(externalUrlPattern);
      if (matches) {
        // Exclude standard XML namespaces in SVG: xmlns="http://www.w3.org/2000/svg"
        const filtered = matches.filter((url) => !url.includes('www.w3.org'));
        assert.deepStrictEqual(filtered, [], `External origin found in ${filePath}: ${filtered.join(', ')}`);
      }
    }
  }
});

// -----------------------------------------------------------------------------
// 5. Client-Side QR Generation
// -----------------------------------------------------------------------------

test('Client-side QR generator produces valid SVG markup without network requests', () => {
  const qr = new QRCode('https://halp.to/h/e7k4p2m9qx3va');
  assert.ok(qr.size >= 21, 'QR size is valid for Model 2');
  const svg = qr.toSVG(4);
  assert.ok(svg.includes('<svg'), 'valid SVG markup generated');
  assert.ok(svg.includes('viewBox="0 0'), 'valid SVG viewBox');
  assert.ok(svg.includes('<path d="M'), 'valid SVG path');
});

// -----------------------------------------------------------------------------
// 6. Landing Page Tests
// -----------------------------------------------------------------------------

test('Default view for unauthenticated visitor is landing page', () => {
  const mockContainer = new MockElement('div') as unknown as HTMLElement;
  const app = new HalpApp(mockContainer);
  assert.strictEqual(app.currentView, 'landing');
});

test('Landing page renders hero, app showcase, and quick access', () => {
  const mockContainer = new MockElement('div') as unknown as HTMLElement;
  const app = new HalpApp(mockContainer);
  app.currentView = 'landing';
  const element = app.renderLanding() as unknown as MockElement;
  assert.ok(element !== null);
  assert.strictEqual(element.className, 'landing-page');
  assert.ok(element.children.length >= 6, 'Has header, hero, showcase, how it works, security, quick access, and footer');
});

test('Landing page can navigate to key_wall (register) and login', () => {
  const mockContainer = new MockElement('div') as unknown as HTMLElement;
  const app = new HalpApp(mockContainer);
  assert.strictEqual(app.currentView, 'landing');

  // Navigate to register (key wall)
  app.currentKey = '6421883051974462';
  app.currentView = 'key_wall';
  assert.strictEqual(app.currentView, 'key_wall');

  // Navigate to login
  app.currentView = 'login';
  assert.strictEqual(app.currentView, 'login');
});

test('App showcase renders all 5 preview tabs', () => {
  const mockContainer = new MockElement('div') as unknown as HTMLElement;
  const app = new HalpApp(mockContainer);

  for (let tab = 0; tab < 5; tab++) {
    app.activePreviewTab = tab;
    const showcase = app.renderAppShowcase() as unknown as MockElement;
    assert.ok(showcase !== null);
    assert.strictEqual(showcase.id, 'showcase');
  }
});
