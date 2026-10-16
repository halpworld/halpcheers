/**
 * Client-side encrypted Contacts Sync for Halp (Phase 2 — Reach).
 *
 * SPECIFICATION (docs/DISCOVERY.md § Contacts, docs/API.md § Contacts, docs/OPEN-QUESTIONS.md #14):
 * - Key derivation: contacts_key = HKDF-SHA256(account_key, info="halp/contacts/v1").
 *   Neither account_key nor contacts_key EVER leaves the device.
 * - Encryption: AES-256-GCM with fresh 12-byte random nonce per write.
 * - Padding: Plaintext is padded to a 4 KiB (4096-byte) boundary BEFORE sealing so size
 *   reveals only a size class, never an exact contact count.
 * - Ceiling: contacts.max_bytes = 64 KiB (65536 bytes).
 * - Conflict resolution: Each entry is { handle, nickname, added_day, deleted }.
 *   Merging is a set union where deletion wins. Stale If-Match (412) re-fetches, merges, and retries.
 */

import { deriveContactsKey } from './key.js';
import { HalpClient, HalpApiError } from './client.js';

export const CONTACTS_PAD_BLOCK_BYTES = 4096;
export const CONTACTS_MAX_PLAINTEXT_BYTES = 64 * 1024;
export const AES_GCM_IV_BYTES = 12;

export interface ContactEntry {
  handle: string;
  nickname: string;
  added_day: number;
  deleted?: boolean;
}

/**
 * Returns the current UTC day number (days since Unix epoch).
 */
export function currentDayNumber(): number {
  return Math.floor(Date.now() / 86400000);
}

/**
 * Encodes a Uint8Array to standard Base64 string.
 */
export function bytesToBase64(bytes: Uint8Array): string {
  let binary = '';
  for (let i = 0; i < bytes.byteLength; i++) {
    binary += String.fromCharCode(bytes[i]);
  }
  return btoa(binary);
}

/**
 * Decodes a Base64 string to Uint8Array.
 */
export function base64ToBytes(b64: string): Uint8Array {
  const clean = b64.trim();
  const binary = atob(clean);
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    out[i] = binary.charCodeAt(i);
  }
  return out;
}

/**
 * Pads serialized payload bytes to a 4 KiB (4096-byte) boundary with a 4-byte big-endian length prefix.
 */
export function padContactsPlaintext(payload: Uint8Array): Uint8Array {
  const totalUnpadded = 4 + payload.byteLength;
  if (totalUnpadded > CONTACTS_MAX_PLAINTEXT_BYTES) {
    throw new Error('contacts_blob_exceeds_max_bytes');
  }
  const blocks = Math.max(1, Math.ceil(totalUnpadded / CONTACTS_PAD_BLOCK_BYTES));
  const paddedLen = blocks * CONTACTS_PAD_BLOCK_BYTES;
  if (paddedLen > CONTACTS_MAX_PLAINTEXT_BYTES) {
    throw new Error('contacts_blob_exceeds_max_bytes');
  }

  const padded = new Uint8Array(paddedLen);
  const view = new DataView(padded.buffer);
  view.setUint32(0, payload.byteLength, false);
  padded.set(payload, 4);
  return padded;
}

/**
 * Unpads a 4 KiB-aligned plaintext buffer back to its original payload bytes.
 */
export function unpadContactsPlaintext(padded: Uint8Array): Uint8Array {
  if (padded.byteLength < 4 || padded.byteLength % CONTACTS_PAD_BLOCK_BYTES !== 0) {
    throw new Error('invalid_padded_contacts_plaintext');
  }
  const view = new DataView(padded.buffer, padded.byteOffset, padded.byteLength);
  const payloadLen = view.getUint32(0, false);
  if (payloadLen > padded.byteLength - 4) {
    throw new Error('corrupted_contacts_length_prefix');
  }
  return padded.subarray(4, 4 + payloadLen);
}

/**
 * Merges two contact entry lists using a CRDT-style set union where deletion wins
 * (docs/DISCOVERY.md § The blob & docs/OPEN-QUESTIONS.md #14).
 */
export function mergeContacts(local: ContactEntry[], remote: ContactEntry[]): ContactEntry[] {
  const byHandle = new Map<string, ContactEntry>();

  const upsert = (entry: ContactEntry) => {
    const key = entry.handle.trim();
    if (!key) return;

    const existing = byHandle.get(key);
    if (!existing) {
      byHandle.set(key, {
        handle: key,
        nickname: entry.deleted ? '' : entry.nickname,
        added_day: entry.added_day || currentDayNumber(),
        ...(entry.deleted ? { deleted: true } : {}),
      });
      return;
    }

    const existingDay = existing.added_day || currentDayNumber();
    const entryDay = entry.added_day || currentDayNumber();

    let isDeleted: boolean;
    let addedDay: number;
    let nickname: string;

    if (entryDay > existingDay) {
      isDeleted = Boolean(entry.deleted);
      addedDay = entryDay;
      nickname = isDeleted ? '' : entry.nickname || existing.nickname;
    } else if (existingDay > entryDay) {
      isDeleted = Boolean(existing.deleted);
      addedDay = existingDay;
      nickname = isDeleted ? '' : existing.nickname || entry.nickname;
    } else {
      // Same day: deletion always wins in the set union
      isDeleted = Boolean(existing.deleted || entry.deleted);
      addedDay = existingDay;
      nickname = isDeleted ? '' : entry.nickname || existing.nickname;
    }

    byHandle.set(key, {
      handle: key,
      nickname,
      added_day: addedDay,
      ...(isDeleted ? { deleted: true } : {}),
    });
  };

  for (const item of remote) {
    upsert(item);
  }
  for (const item of local) {
    upsert(item);
  }

  return Array.from(byHandle.values()).sort((a, b) => a.handle.localeCompare(b.handle));
}

/**
 * Encrypts a list of ContactEntry items into a 4 KiB-padded AES-256-GCM Base64 blob
 * using contacts_key derived from accountKey.
 */
export async function encryptContactsBlob(
  entries: ContactEntry[],
  accountKey: string
): Promise<string> {
  const rawKey = await deriveContactsKey(accountKey);
  const cryptoKey = await crypto.subtle.importKey(
    'raw',
    rawKey as unknown as BufferSource,
    { name: 'AES-GCM' },
    false,
    ['encrypt']
  );

  const jsonBytes = new TextEncoder().encode(JSON.stringify({ entries }));
  const paddedPlaintext = padContactsPlaintext(jsonBytes);

  // Fresh 12-byte random IV for every write (never reuse nonce under same key)
  const iv = new Uint8Array(AES_GCM_IV_BYTES);
  crypto.getRandomValues(iv);

  const ciphertextBuf = await crypto.subtle.encrypt(
    { name: 'AES-GCM', iv, tagLength: 128 },
    cryptoKey,
    paddedPlaintext as unknown as BufferSource
  );
  const ciphertext = new Uint8Array(ciphertextBuf);

  const combined = new Uint8Array(iv.byteLength + ciphertext.byteLength);
  combined.set(iv, 0);
  combined.set(ciphertext, iv.byteLength);

  return bytesToBase64(combined);
}

/**
 * Decrypts a Base64 contacts blob using contacts_key derived from accountKey.
 */
export async function decryptContactsBlob(
  blobBase64: string,
  accountKey: string
): Promise<ContactEntry[]> {
  if (!blobBase64 || !blobBase64.trim()) {
    return [];
  }

  const combined = base64ToBytes(blobBase64);
  if (combined.byteLength < AES_GCM_IV_BYTES + CONTACTS_PAD_BLOCK_BYTES + 16) {
    throw new Error('contacts_blob_too_short');
  }

  const iv = combined.subarray(0, AES_GCM_IV_BYTES);
  const ciphertext = combined.subarray(AES_GCM_IV_BYTES);

  const rawKey = await deriveContactsKey(accountKey);
  const cryptoKey = await crypto.subtle.importKey(
    'raw',
    rawKey as unknown as BufferSource,
    { name: 'AES-GCM' },
    false,
    ['decrypt']
  );

  const paddedBuf = await crypto.subtle.decrypt(
    { name: 'AES-GCM', iv: iv as unknown as BufferSource, tagLength: 128 },
    cryptoKey,
    ciphertext as unknown as BufferSource
  );

  const unpadded = unpadContactsPlaintext(new Uint8Array(paddedBuf));
  const parsed = JSON.parse(new TextDecoder().decode(unpadded)) as { entries?: ContactEntry[] };
  return Array.isArray(parsed.entries) ? parsed.entries : [];
}

/**
 * Synchronizes local contacts with the server using optimistic concurrency (If-Match)
 * and automatic re-fetch + set-union merge on 412 Precondition Failed conflicts.
 */
export async function syncContacts(
  client: HalpClient,
  accountKey: string,
  localEntries: ContactEntry[],
  maxRetries: number = 3
): Promise<{ version: number; entries: ContactEntry[] }> {
  let workingEntries = [...localEntries];

  for (let attempt = 0; attempt <= maxRetries; attempt++) {
    const remote = await client.getContacts();
    const remoteVersion = remote.version ?? 0;
    let remoteEntries: ContactEntry[] = [];

    if (remote.blob && remote.blob.trim() !== '') {
      remoteEntries = await decryptContactsBlob(remote.blob, accountKey);
    }

    workingEntries = mergeContacts(workingEntries, remoteEntries);
    const sealedBlob = await encryptContactsBlob(workingEntries, accountKey);

    try {
      const updated = await client.updateContacts(sealedBlob, remoteVersion);
      return {
        version: updated?.version ?? remoteVersion + 1,
        entries: workingEntries,
      };
    } catch (err) {
      if (
        err instanceof HalpApiError &&
        (err.status === 412 || err.status === 409) &&
        attempt < maxRetries
      ) {
        // Stale If-Match: loop around to re-fetch, merge, and retry
        continue;
      }
      throw err;
    }
  }

  throw new Error('contacts_sync_conflict_retries_exhausted');
}
