/**
 * Key derivation and device storage for Halp.
 *
 * DATA RULES (AGENTS.md):
 * The account key never leaves the device.
 * auth_secret  = HKDF-SHA256(account_key, info="halp/auth/v1") -> sent to server, Argon2id-hashed server-side
 * contacts_key = HKDF-SHA256(account_key, info="halp/contacts/v1") -> never transmitted anywhere
 */

import { normalizeAccountKey } from './validation.js';

export const HKDF_INFO_AUTH = new TextEncoder().encode('halp/auth/v1');
export const HKDF_INFO_CONTACTS = new TextEncoder().encode('halp/contacts/v1');
export const STORAGE_KEY_ACCOUNT = 'halp_account_key';

/**
 * Converts a Uint8Array or ArrayBuffer into a lowercase hex string.
 */
export function bytesToHex(bytes: Uint8Array | ArrayBuffer): string {
  const arr = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
  let hex = '';
  for (let i = 0; i < arr.length; i++) {
    hex += arr[i].toString(16).padStart(2, '0');
  }
  return hex;
}

/**
 * Converts a hex string into a Uint8Array.
 */
export function hexToBytes(hex: string): Uint8Array {
  const clean = hex.trim();
  if (clean.length % 2 !== 0) {
    throw new Error('Invalid hex string length');
  }
  const bytes = new Uint8Array(clean.length / 2);
  for (let i = 0; i < clean.length; i += 2) {
    bytes[i / 2] = parseInt(clean.substring(i, i + 2), 16);
  }
  return bytes;
}

/**
 * Generates a fresh 16-digit account key with cryptographic randomness.
 */
export function generateAccountKey(): string {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  let digits = '';
  for (let i = 0; i < 16; i++) {
    digits += (bytes[i] % 10).toString();
  }
  return digits;
}

/**
 * Derives a 32-byte secret using HKDF-SHA256 with the specified domain info string.
 */
async function deriveHkdfKey(accountKey: string, info: Uint8Array): Promise<Uint8Array> {
  const cleanDigits = normalizeAccountKey(accountKey);
  const enc = new TextEncoder();
  const rawKey = enc.encode(cleanDigits);

  const baseKey = await crypto.subtle.importKey(
    'raw',
    rawKey,
    'HKDF',
    false,
    ['deriveBits']
  );

  const derivedBits = await crypto.subtle.deriveBits(
    {
      name: 'HKDF',
      hash: 'SHA-256',
      salt: new Uint8Array(0), // RFC 5869: empty salt defaults to hash-length zeros
      info: info as unknown as BufferSource,
    },
    baseKey,
    256 // 32 bytes * 8 bits
  );

  return new Uint8Array(derivedBits);
}

/**
 * Derives auth_secret: HKDF-SHA256(account_key, info="halp/auth/v1").
 * Returns 32 bytes.
 */
export async function deriveAuthSecret(accountKey: string): Promise<Uint8Array> {
  return deriveHkdfKey(accountKey, HKDF_INFO_AUTH);
}

/**
 * Derives auth_secret and returns it as a 64-character lowercase hex string.
 * This is the value sent to POST /v1/session.
 */
export async function deriveAuthSecretHex(accountKey: string): Promise<string> {
  const secret = await deriveAuthSecret(accountKey);
  return bytesToHex(secret);
}

/**
 * Derives contacts_key: HKDF-SHA256(account_key, info="halp/contacts/v1").
 * Returns 32 bytes.
 *
 * INVARIANT: This key is NEVER transmitted across the network or sent to the server.
 */
export async function deriveContactsKey(accountKey: string): Promise<Uint8Array> {
  return deriveHkdfKey(accountKey, HKDF_INFO_CONTACTS);
}

/**
 * Persistent device storage abstraction.
 */
export interface KeyStorage {
  getItem(key: string): string | null | Promise<string | null>;
  setItem(key: string, value: string): void | Promise<void>;
  removeItem(key: string): void | Promise<void>;
}

/**
 * Returns localStorage if in a browser environment, otherwise null.
 */
function getDefaultStorage(): KeyStorage | null {
  if (typeof window !== 'undefined' && window.localStorage) {
    return window.localStorage;
  }
  return null;
}

/**
 * Stores the account key in local storage.
 */
export async function storeAccountKey(accountKey: string, storage?: KeyStorage | null): Promise<void> {
  const clean = normalizeAccountKey(accountKey);
  const s = storage ?? getDefaultStorage();
  if (s) {
    await s.setItem(STORAGE_KEY_ACCOUNT, clean);
  }
}

/**
 * Loads the account key from local storage.
 */
export async function loadAccountKey(storage?: KeyStorage | null): Promise<string | null> {
  const s = storage ?? getDefaultStorage();
  if (s) {
    const val = await s.getItem(STORAGE_KEY_ACCOUNT);
    if (val && isAccountKeyShaped(val)) {
      return val;
    }
  }
  return null;
}

/**
 * Removes the account key from local storage.
 */
export async function clearAccountKey(storage?: KeyStorage | null): Promise<void> {
  const s = storage ?? getDefaultStorage();
  if (s) {
    await s.removeItem(STORAGE_KEY_ACCOUNT);
  }
}

function isAccountKeyShaped(str: string): boolean {
  return /^\d{16}$/.test(str.replace(/\s+/g, ''));
}
