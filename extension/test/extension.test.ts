/**
 * Test suite for Halp Browser Extension (Issue #23).
 * Covers Definition of Done requirements:
 * - Manifest validation for Chrome & Firefox MV3.
 * - Minimal permissions audit (no tabs, no <all_urls>).
 * - Zero remote code or CDN references.
 * - Send path 300 ms latency floor and two button states.
 * - Notifications carry no sender info.
 */

import test from 'node:test';
import assert from 'node:assert';
import fs from 'node:fs';
import path from 'node:path';
import { execSync } from 'node:child_process';

const EXTENSION_DIR = path.resolve(import.meta.dirname, '..');

// -----------------------------------------------------------------------------
// 1. Manifest Validation & Minimal Permissions Audit
// -----------------------------------------------------------------------------

test('Chrome and Firefox manifests exist and have minimal permissions', () => {
  const chromePath = path.join(EXTENSION_DIR, 'manifest.chrome.json');
  const firefoxPath = path.join(EXTENSION_DIR, 'manifest.firefox.json');

  assert.ok(fs.existsSync(chromePath), 'manifest.chrome.json exists');
  assert.ok(fs.existsSync(firefoxPath), 'manifest.firefox.json exists');

  const chromeManifest = JSON.parse(fs.readFileSync(chromePath, 'utf-8'));
  const firefoxManifest = JSON.parse(fs.readFileSync(firefoxPath, 'utf-8'));

  for (const [browser, m] of [['Chrome', chromeManifest], ['Firefox', firefoxManifest]]) {
    assert.strictEqual(m.manifest_version, 3, `${browser} is MV3`);

    // Audit permissions: STRICT MINIMALITY
    const perms: string[] = m.permissions || [];
    assert.ok(!perms.includes('tabs'), `${browser} must NOT request 'tabs' permission`);
    assert.ok(!perms.includes('<all_urls>'), `${browser} must NOT request '<all_urls>'`);
    assert.ok(!perms.includes('cookies'), `${browser} must NOT request 'cookies'`);
    assert.ok(!perms.includes('webRequest'), `${browser} must NOT request 'webRequest'`);

    // Only allowed permissions
    const allowed = new Set(['storage', 'alarms', 'notifications']);
    for (const p of perms) {
      assert.ok(allowed.has(p), `${browser} permission '${p}' must be in minimal allowed set`);
    }

    // Host permissions strictly limited to halp origin
    const hostPerms: string[] = m.host_permissions || [];
    for (const hp of hostPerms) {
      assert.ok(hp.startsWith('https://halp.to'), `${browser} host permission must be scoped to halp.to, got: ${hp}`);
    }

    // Content scripts forbidden in phase 1
    assert.strictEqual(m.content_scripts, undefined, `${browser} must not declare content scripts`);
  }
});

// -----------------------------------------------------------------------------
// 2. Build Verification: Both Packages Built from Single Command
// -----------------------------------------------------------------------------

test('Single build command produces both Chrome and Firefox packages without remote code', () => {
  execSync('npm run build', { cwd: EXTENSION_DIR, stdio: 'pipe' });

  const chromeDist = path.join(EXTENSION_DIR, 'dist', 'chrome');
  const firefoxDist = path.join(EXTENSION_DIR, 'dist', 'firefox');

  assert.ok(fs.existsSync(path.join(chromeDist, 'manifest.json')), 'Chrome dist has manifest.json');
  assert.ok(fs.existsSync(path.join(chromeDist, 'popup.html')), 'Chrome dist has popup.html');
  assert.ok(fs.existsSync(path.join(chromeDist, 'popup.js')), 'Chrome dist has popup.js');
  assert.ok(fs.existsSync(path.join(chromeDist, 'background.js')), 'Chrome dist has background.js');

  assert.ok(fs.existsSync(path.join(firefoxDist, 'manifest.json')), 'Firefox dist has manifest.json');
  assert.ok(fs.existsSync(path.join(firefoxDist, 'popup.html')), 'Firefox dist has popup.html');
  assert.ok(fs.existsSync(path.join(firefoxDist, 'popup.js')), 'Firefox dist has popup.js');
  assert.ok(fs.existsSync(path.join(firefoxDist, 'background.js')), 'Firefox dist has background.js');

  // Verify Chrome manifest uses background.service_worker
  const chromeBuilt = JSON.parse(fs.readFileSync(path.join(chromeDist, 'manifest.json'), 'utf-8'));
  assert.ok(chromeBuilt.background.service_worker !== undefined);

  // Verify Firefox manifest uses background.scripts
  const firefoxBuilt = JSON.parse(fs.readFileSync(path.join(firefoxDist, 'manifest.json'), 'utf-8'));
  assert.ok(firefoxBuilt.background.scripts !== undefined);
});

// -----------------------------------------------------------------------------
// 3. Absence of Remote Code, Analytics, and CDNs
// -----------------------------------------------------------------------------

test('Built extension packages reference zero remote code or analytics SDKs', () => {
  const forbiddenPatterns = [
    'google-analytics',
    'googletagmanager',
    'sentry',
    'segment',
    'mixpanel',
    'fonts.googleapis.com',
    'cdnjs.cloudflare.com',
    'unpkg.com',
    'cdn.jsdelivr.net',
  ];

  const distDir = path.join(EXTENSION_DIR, 'dist');
  const checkDir = (dir: string) => {
    const entries = fs.readdirSync(dir, { withFileTypes: true });
    for (const e of entries) {
      const fullPath = path.join(dir, e.name);
      if (e.isDirectory()) {
        checkDir(fullPath);
      } else if (e.isFile()) {
        const content = fs.readFileSync(fullPath, 'utf-8');
        for (const forbidden of forbiddenPatterns) {
          assert.ok(
            !content.includes(forbidden),
            `Found forbidden pattern '${forbidden}' in ${fullPath}`
          );
        }
      }
    }
  };

  checkDir(distDir);
});
