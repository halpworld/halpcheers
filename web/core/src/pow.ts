/**
 * Hashcash Proof of Work solver and cache for Halp.
 *
 * SPECIFICATION (docs/ABUSE.md, docs/OPEN-QUESTIONS.md #28):
 * - Hashcash equation: SHA-256(handle || epoch || challenge || nonce) has d leading zero bits.
 * - Standard floor: d = 14 (~10 ms compute).
 * - Signup difficulty: d = 21 (~1.5 s compute).
 * - Rotating epochs: 5 minutes (300 s).
 * - Header format: X-Halp-PoW: <epoch>.<nonce>
 * - Solved tokens are cached per epoch so a burst of sends pays once.
 */

import { bytesToHex, hexToBytes } from './key.js';

export const DIFFICULTY_FLOOR = 14;
export const DIFFICULTY_SIGNUP = 21;
export const EPOCH_SECONDS = 300;

export interface PoWToken {
  epoch: number;
  nonce: string;
  headerValue: string;
}

/**
 * Returns the current 5-minute epoch index from a timestamp in milliseconds.
 */
export function getCurrentEpoch(timeMs: number = Date.now()): number {
  return Math.floor(timeMs / 1000 / EPOCH_SECONDS);
}

/**
 * Counts the number of leading zero bits in a 32-byte SHA-256 digest.
 */
export function countLeadingZeroBits(digest: Uint8Array): number {
  let count = 0;
  for (let i = 0; i < digest.length; i++) {
    const byte = digest[i];
    if (byte === 0) {
      count += 8;
    } else {
      count += Math.clz32(byte) - 24;
      break;
    }
  }
  return count;
}

/**
 * Fast synchronous SHA-256 implementation for PoW grinding in JS/Worker threads.
 * Using a dedicated JS implementation allows high-frequency looping without the
 * async Promise overhead of crypto.subtle on every single iteration (~100x faster for grinding).
 */
export class Sha256Grinder {
  private static K = new Uint32Array([
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
  ]);

  static hash(data: Uint8Array): Uint8Array {
    const l = data.length;
    const bitLen = l * 8;
    const kPad = (56 - ((l + 1) % 64) + 64) % 64;
    const totalLen = l + 1 + kPad + 8;
    const padded = new Uint8Array(totalLen);
    padded.set(data);
    padded[l] = 0x80;

    // Append 64-bit length in big-endian
    const view = new DataView(padded.buffer);
    view.setUint32(totalLen - 4, bitLen, false);

    let h0 = 0x6a09e667, h1 = 0xbb67ae85, h2 = 0x3c6ef372, h3 = 0xa54ff53a;
    let h4 = 0x510e527f, h5 = 0x9b05688c, h6 = 0x1f83d9ab, h7 = 0x5be0cd19;

    const w = new Uint32Array(64);

    for (let chunk = 0; chunk < totalLen; chunk += 64) {
      for (let i = 0; i < 16; i++) {
        w[i] = view.getUint32(chunk + i * 4, false);
      }
      for (let i = 16; i < 64; i++) {
        const s0 = ((w[i - 15] >>> 7) | (w[i - 15] << 25)) ^
                   ((w[i - 15] >>> 18) | (w[i - 15] << 14)) ^
                   (w[i - 15] >>> 3);
        const s1 = ((w[i - 2] >>> 17) | (w[i - 2] << 15)) ^
                   ((w[i - 2] >>> 19) | (w[i - 2] << 13)) ^
                   (w[i - 2] >>> 10);
        w[i] = (w[i - 16] + s0 + w[i - 7] + s1) | 0;
      }

      let a = h0, b = h1, c = h2, d = h3, e = h4, f = h5, g = h6, h = h7;

      for (let i = 0; i < 64; i++) {
        const S1 = ((e >>> 6) | (e << 26)) ^ ((e >>> 11) | (e << 21)) ^ ((e >>> 25) | (e << 7));
        const ch = (e & f) ^ (~e & g);
        const temp1 = (h + S1 + ch + Sha256Grinder.K[i] + w[i]) | 0;
        const S0 = ((a >>> 2) | (a << 30)) ^ ((a >>> 13) | (a << 19)) ^ ((a >>> 22) | (a << 10));
        const maj = (a & b) ^ (a & c) ^ (b & c);
        const temp2 = (S0 + maj) | 0;

        h = g;
        g = f;
        f = e;
        e = (d + temp1) | 0;
        d = c;
        c = b;
        b = a;
        a = (temp1 + temp2) | 0;
      }

      h0 = (h0 + a) | 0;
      h1 = (h1 + b) | 0;
      h2 = (h2 + c) | 0;
      h3 = (h3 + d) | 0;
      h4 = (h4 + e) | 0;
      h5 = (h5 + f) | 0;
      h6 = (h6 + g) | 0;
      h7 = (h7 + h) | 0;
    }

    const out = new Uint8Array(32);
    const outView = new DataView(out.buffer);
    outView.setUint32(0, h0, false);
    outView.setUint32(4, h1, false);
    outView.setUint32(8, h2, false);
    outView.setUint32(12, h3, false);
    outView.setUint32(16, h4, false);
    outView.setUint32(20, h5, false);
    outView.setUint32(24, h6, false);
    outView.setUint32(28, h7, false);
    return out;
  }
}

/**
 * Checks whether a given nonce solves the hashcash challenge.
 * Evaluates: SHA-256(handle || epoch || challenge || nonce).
 */
export function checkPoW(
  handle: string,
  epoch: number,
  challenge: Uint8Array,
  nonce: string,
  targetDifficulty: number
): boolean {
  const enc = new TextEncoder();
  const handleBytes = enc.encode(handle);
  const epochBytes = enc.encode(epoch.toString());
  const nonceBytes = enc.encode(nonce);

  const totalLen = handleBytes.length + epochBytes.length + challenge.length + nonceBytes.length;
  const buf = new Uint8Array(totalLen);
  let pos = 0;
  buf.set(handleBytes, pos); pos += handleBytes.length;
  buf.set(epochBytes, pos); pos += epochBytes.length;
  buf.set(challenge, pos); pos += challenge.length;
  buf.set(nonceBytes, pos);

  const digest = Sha256Grinder.hash(buf);
  return countLeadingZeroBits(digest) >= targetDifficulty;
}

/**
 * Synchronously solves a hashcash challenge.
 */
export function solvePoWSync(
  handle: string,
  epoch: number,
  challenge: Uint8Array,
  targetDifficulty: number,
  maxIterations: number = 10_000_000
): { nonce: string; iterations: number } | null {
  const enc = new TextEncoder();
  const handleBytes = enc.encode(handle);
  const epochBytes = enc.encode(epoch.toString());

  const prefixLen = handleBytes.length + epochBytes.length + challenge.length;
  // Maximum decimal representation of maxIterations is 20 chars
  const buf = new Uint8Array(prefixLen + 20);
  let pos = 0;
  buf.set(handleBytes, pos); pos += handleBytes.length;
  buf.set(epochBytes, pos); pos += epochBytes.length;
  buf.set(challenge, pos); pos += challenge.length;

  for (let nonceVal = 0; nonceVal < maxIterations; nonceVal++) {
    const nonceStr = nonceVal.toString();
    const nonceBytes = enc.encode(nonceStr);
    buf.set(nonceBytes, prefixLen);
    const sub = buf.subarray(0, prefixLen + nonceBytes.length);

    const digest = Sha256Grinder.hash(sub);
    if (countLeadingZeroBits(digest) >= targetDifficulty) {
      return { nonce: nonceStr, iterations: nonceVal + 1 };
    }
  }

  return null;
}

/**
 * Worker script code for background grinding.
 */
const WORKER_CODE = `
self.onmessage = function(e) {
  const { id, handle, epoch, challengeHex, difficulty, maxIterations } = e.data;
  
  // Inline solver logic
  const challenge = new Uint8Array(challengeHex.match(/.{1,2}/g).map(byte => parseInt(byte, 16)));
  
  function countLeadingZeroBits(digest) {
    let count = 0;
    for (let i = 0; i < digest.length; i++) {
      const byte = digest[i];
      if (byte === 0) {
        count += 8;
      } else {
        count += Math.clz32(byte) - 24;
        break;
      }
    }
    return count;
  }
  
  // Sha256Grinder inline
  ${Sha256Grinder.toString()}
  
  const enc = new TextEncoder();
  const handleBytes = enc.encode(handle);
  const epochBytes = enc.encode(epoch.toString());
  const prefixLen = handleBytes.length + epochBytes.length + challenge.length;
  const buf = new Uint8Array(prefixLen + 20);
  let pos = 0;
  buf.set(handleBytes, pos); pos += handleBytes.length;
  buf.set(epochBytes, pos); pos += epochBytes.length;
  buf.set(challenge, pos); pos += challenge.length;
  
  for (let nonceVal = 0; nonceVal < maxIterations; nonceVal++) {
    const nonceStr = nonceVal.toString();
    const nonceBytes = enc.encode(nonceStr);
    buf.set(nonceBytes, prefixLen);
    const sub = buf.subarray(0, prefixLen + nonceBytes.length);
    const digest = Sha256Grinder.hash(sub);
    if (countLeadingZeroBits(digest) >= difficulty) {
      self.postMessage({ id, success: true, nonce: nonceStr, iterations: nonceVal + 1 });
      return;
    }
  }
  self.postMessage({ id, success: false });
};
`;

/**
 * Manages PoW solving with Web Worker execution, in-memory caching per epoch,
 * and seamless fallback when Workers are not supported.
 */
export class PoWEngine {
  private cache = new Map<string, PoWToken>();
  private worker: Worker | null = null;
  private nextRequestId = 1;
  private pendingRequests = new Map<number, (res: { nonce: string; iterations: number } | null) => void>();
  private grindCount = 0;

  constructor() {
    this.initWorker();
  }

  private initWorker(): void {
    if (typeof Worker !== 'undefined' && typeof Blob !== 'undefined' && typeof URL !== 'undefined') {
      try {
        const blob = new Blob([WORKER_CODE], { type: 'application/javascript' });
        this.worker = new Worker(URL.createObjectURL(blob));
        this.worker.onmessage = (e) => {
          const { id, success, nonce, iterations } = e.data;
          const resolve = this.pendingRequests.get(id);
          if (resolve) {
            this.pendingRequests.delete(id);
            resolve(success ? { nonce, iterations } : null);
          }
        };
      } catch {
        // Fallback to sync solver if Worker construction is blocked by CSP or environment
        this.worker = null;
      }
    }
  }

  /**
   * Returns how many actual grinds have been executed (for test assertions).
   */
  getGrindCount(): number {
    return this.grindCount;
  }

  /**
   * Solves or returns a cached PoW token for the given target, epoch, and difficulty.
   */
  async getOrSolveToken(
    target: string,
    epoch: number = getCurrentEpoch(),
    challenge: Uint8Array = new Uint8Array(32),
    difficulty: number = DIFFICULTY_FLOOR
  ): Promise<PoWToken> {
    const cacheKey = `${epoch}:${target}:${difficulty}`;
    const cached = this.cache.get(cacheKey);
    if (cached) {
      return cached;
    }

    this.grindCount++;
    let result: { nonce: string; iterations: number } | null = null;

    if (this.worker) {
      result = await new Promise((resolve) => {
        const id = this.nextRequestId++;
        this.pendingRequests.set(id, resolve);
        this.worker!.postMessage({
          id,
          handle: target,
          epoch,
          challengeHex: bytesToHex(challenge),
          difficulty,
          maxIterations: 10_000_000,
        });
      });
    }

    // Fallback to sync grinder if no worker or worker failed
    if (!result) {
      result = solvePoWSync(target, epoch, challenge, difficulty);
    }

    if (!result) {
      throw new Error(`Failed to solve PoW within iteration budget for difficulty ${difficulty}`);
    }

    const token: PoWToken = {
      epoch,
      nonce: result.nonce,
      headerValue: `${epoch}.${result.nonce}`,
    };

    // Cache the solved token for this epoch and target
    this.cache.set(cacheKey, token);
    return token;
  }

  /**
   * Cleans up expired cache entries older than 2 epochs.
   */
  purgeOldEpochs(currentEpoch: number = getCurrentEpoch()): void {
    for (const key of this.cache.keys()) {
      const epochStr = key.split(':')[0];
      const epoch = parseInt(epochStr, 10);
      if (epoch < currentEpoch - 1) {
        this.cache.delete(key);
      }
    }
  }

  /**
   * Terminates background worker resources.
   */
  terminate(): void {
    if (this.worker) {
      this.worker.terminate();
      this.worker = null;
    }
  }
}
