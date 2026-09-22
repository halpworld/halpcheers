/**
 * Build script for Chrome and Firefox MV3 extension packages.
 * One command builds both packages into dist/chrome and dist/firefox.
 */

import fs from 'node:fs';
import path from 'node:path';
import { execSync } from 'node:child_process';

const ROOT_DIR = path.resolve(import.meta.dirname);
const DIST_DIR = path.join(ROOT_DIR, 'dist');
const BUILD_DIR = path.join(DIST_DIR, 'build');
const CHROME_DIR = path.join(DIST_DIR, 'chrome');
const FIREFOX_DIR = path.join(DIST_DIR, 'firefox');

console.log('1. Compiling TypeScript with tsc...');
execSync('npx tsc', { cwd: ROOT_DIR, stdio: 'inherit' });

// Ensure output directories exist
fs.mkdirSync(CHROME_DIR, { recursive: true });
fs.mkdirSync(FIREFOX_DIR, { recursive: true });

const commonFiles = [
  'popup.html',
  'popup.css',
];

const jsFiles = [
  'background.js',
  'popup.js',
  'shim.js',
];

console.log('2. Assembling Chrome MV3 package...');
fs.copyFileSync(path.join(ROOT_DIR, 'manifest.chrome.json'), path.join(CHROME_DIR, 'manifest.json'));
for (const file of commonFiles) {
  fs.copyFileSync(path.join(ROOT_DIR, 'src', file), path.join(CHROME_DIR, file));
}
for (const file of jsFiles) {
  const srcPath = path.join(BUILD_DIR, file);
  if (fs.existsSync(srcPath)) {
    fs.copyFileSync(srcPath, path.join(CHROME_DIR, file));
  }
}

console.log('3. Assembling Firefox MV3 package...');
fs.copyFileSync(path.join(ROOT_DIR, 'manifest.firefox.json'), path.join(FIREFOX_DIR, 'manifest.json'));
for (const file of commonFiles) {
  fs.copyFileSync(path.join(ROOT_DIR, 'src', file), path.join(FIREFOX_DIR, file));
}
for (const file of jsFiles) {
  const srcPath = path.join(BUILD_DIR, file);
  if (fs.existsSync(srcPath)) {
    fs.copyFileSync(srcPath, path.join(FIREFOX_DIR, file));
  }
}

console.log('4. Verifying zero remote code / CDN URLs...');
const externalRegex = /(https?:\/\/(?!halp\.to)[a-zA-Z0-9.-]+\.[a-zA-Z]{2,})/g;
for (const dir of [CHROME_DIR, FIREFOX_DIR]) {
  const files = fs.readdirSync(dir);
  for (const f of files) {
    const p = path.join(dir, f);
    if (fs.statSync(p).isFile()) {
      const content = fs.readFileSync(p, 'utf-8');
      const matches = content.match(externalRegex);
      if (matches) {
        throw new Error(`Forbidden external origin in ${p}: ${matches.join(', ')}`);
      }
    }
  }
}

console.log('Build complete! Created dist/chrome and dist/firefox');
