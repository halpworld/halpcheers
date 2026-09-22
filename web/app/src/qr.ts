/**
 * Pure client-side QR code generator in TypeScript.
 * Generates SVG and Canvas/PNG data URLs with zero external dependencies and zero network calls.
 * Implements QR Code Model 2 (Byte mode, Error Correction Level M).
 */

// GF(256) arithmetic for Reed-Solomon error correction
const GF256_EXP = new Uint8Array(512);
const GF256_LOG = new Uint8Array(256);

(() => {
  let x = 1;
  for (let i = 0; i < 255; i++) {
    GF256_EXP[i] = x;
    GF256_EXP[i + 255] = x;
    GF256_LOG[x] = i;
    x <<= 1;
    if (x & 0x100) {
      x ^= 0x11d; // Primitive polynomial x^8 + x^4 + x^3 + x^2 + 1
    }
  }
})();

function gfMul(x: number, y: number): number {
  if (x === 0 || y === 0) return 0;
  return GF256_EXP[GF256_LOG[x] + GF256_LOG[y]];
}

function rsComputePoly(ecCount: number): Uint8Array {
  let poly = new Uint8Array([1]);
  for (let i = 0; i < ecCount; i++) {
    const next = new Uint8Array(poly.length + 1);
    const factor = GF256_EXP[i];
    for (let j = 0; j < poly.length; j++) {
      next[j] ^= gfMul(poly[j], factor);
      next[j + 1] ^= poly[j];
    }
    poly = next;
  }
  return poly;
}

function rsCalculateEc(data: Uint8Array, ecCount: number): Uint8Array {
  const poly = rsComputePoly(ecCount);
  const remainder = new Uint8Array(ecCount);
  for (let i = 0; i < data.length; i++) {
    const factor = data[i] ^ remainder[0];
    remainder.copyWithin(0, 1);
    remainder[ecCount - 1] = 0;
    for (let j = 0; j < ecCount; j++) {
      remainder[j] ^= gfMul(poly[j], factor);
    }
  }
  return remainder;
}

// Capacity table for Byte mode, Error Correction Level M:
// Version 1: 21x21, 16 data bytes, 10 EC bytes
// Version 2: 25x25, 28 data bytes, 16 EC bytes
// Version 3: 29x29, 44 data bytes, 26 EC bytes
// Version 4: 33x33, 64 data bytes, 36 EC bytes (2 blocks of 32 data / 18 EC)
interface VersionInfo {
  version: number;
  size: number;
  dataCapacity: number;
  ecBytesPerBlock: number;
  blocks: number;
}

const VERSIONS: VersionInfo[] = [
  { version: 1, size: 21, dataCapacity: 16, ecBytesPerBlock: 10, blocks: 1 },
  { version: 2, size: 25, dataCapacity: 28, ecBytesPerBlock: 16, blocks: 1 },
  { version: 3, size: 29, dataCapacity: 44, ecBytesPerBlock: 26, blocks: 1 },
  { version: 4, size: 33, dataCapacity: 64, ecBytesPerBlock: 18, blocks: 2 },
];

export class QRCode {
  public size: number;
  public modules: boolean[][];

  constructor(text: string) {
    const enc = new TextEncoder();
    const textBytes = enc.encode(text);

    // Pick smallest version that fits
    // Byte mode overhead: 4 bits mode + 8 bits length = 12 bits (1.5 bytes -> rounded up)
    const neededBytes = textBytes.length + 3;
    const vInfo = VERSIONS.find((v) => v.dataCapacity >= neededBytes) ?? VERSIONS[VERSIONS.length - 1];

    this.size = vInfo.size;
    this.modules = Array.from({ length: this.size }, () => Array(this.size).fill(false));

    this.buildMatrix(textBytes, vInfo);
  }

  private buildMatrix(textBytes: Uint8Array, vInfo: VersionInfo): void {
    const isReserved = Array.from({ length: this.size }, () => Array(this.size).fill(false));

    // 1. Finder patterns (top-left, top-right, bottom-left)
    this.drawFinderPattern(0, 0, isReserved);
    this.drawFinderPattern(this.size - 7, 0, isReserved);
    this.drawFinderPattern(0, this.size - 7, isReserved);

    // 2. Timing patterns
    for (let i = 8; i < this.size - 8; i++) {
      const val = i % 2 === 0;
      this.modules[6][i] = val;
      isReserved[6][i] = true;
      this.modules[i][6] = val;
      isReserved[i][6] = true;
    }

    // 3. Dark module (4 * version + 9, 8)
    const darkRow = 4 * vInfo.version + 9;
    this.modules[darkRow][8] = true;
    isReserved[darkRow][8] = true;

    // 4. Reserve format info areas
    for (let i = 0; i < 9; i++) {
      if (i !== 6) {
        isReserved[8][i] = true;
        isReserved[i][8] = true;
      }
    }
    for (let i = 0; i < 8; i++) {
      isReserved[8][this.size - 1 - i] = true;
      isReserved[this.size - 1 - i][8] = true;
    }

    // 5. Encode data codewords
    const totalDataCap = vInfo.dataCapacity;
    const dataBits: number[] = [];

    // Mode: Byte (0100)
    dataBits.push(0, 1, 0, 0);

    // Character count (8 bits for versions 1-9)
    for (let i = 7; i >= 0; i--) {
      dataBits.push((textBytes.length >> i) & 1);
    }

    // Text data
    for (let b = 0; b < textBytes.length; b++) {
      for (let i = 7; i >= 0; i--) {
        dataBits.push((textBytes[b] >> i) & 1);
      }
    }

    // Terminator (up to 4 zeros)
    const termZeros = Math.min(4, totalDataCap * 8 - dataBits.length);
    for (let i = 0; i < termZeros; i++) {
      dataBits.push(0);
    }

    // Pad to byte boundary
    while (dataBits.length % 8 !== 0) {
      dataBits.push(0);
    }

    // Convert bits to byte codewords
    const dataCodewords: number[] = [];
    for (let i = 0; i < dataBits.length; i += 8) {
      let byteVal = 0;
      for (let b = 0; b < 8; b++) {
        byteVal = (byteVal << 1) | dataBits[i + b];
      }
      dataCodewords.push(byteVal);
    }

    // Pad bytes (0xEC, 0x11) until full capacity
    const padBytes = [0xec, 0x11];
    let padIdx = 0;
    while (dataCodewords.length < totalDataCap) {
      dataCodewords.push(padBytes[padIdx % 2]);
      padIdx++;
    }

    // Error correction codewords
    const rawData = new Uint8Array(dataCodewords);
    const ecCodewords = rsCalculateEc(rawData, vInfo.ecBytesPerBlock);

    // Interleaved final codeword stream
    const finalCodewords = new Uint8Array(rawData.length + ecCodewords.length);
    finalCodewords.set(rawData, 0);
    finalCodewords.set(ecCodewords, rawData.length);

    // 6. Place data bits in matrix (columns right to left, serpentine order)
    let bitIdx = 0;
    const totalBits = finalCodewords.length * 8;

    let up = true;
    for (let right = this.size - 1; right > 0; right -= 2) {
      if (right === 6) right--; // Skip vertical timing pattern column

      for (let vert = 0; vert < this.size; vert++) {
        const row = up ? this.size - 1 - vert : vert;

        for (let c = 0; c < 2; c++) {
          const col = right - c;
          if (!isReserved[row][col]) {
            let bit = false;
            if (bitIdx < totalBits) {
              const byteI = Math.floor(bitIdx / 8);
              const bitI = 7 - (bitIdx % 8);
              bit = ((finalCodewords[byteI] >> bitI) & 1) === 1;
              bitIdx++;
            }

            // Apply Mask 0: (row + col) % 2 === 0
            const mask = (row + col) % 2 === 0;
            this.modules[row][col] = bit !== mask;
          }
        }
      }
      up = !up;
    }

    // 7. Apply Format Information for ECC Level M, Mask 0
    // Format pattern 101010000010010 for Level M, Mask 0 (after 0x5412 XOR)
    const formatBits = 0b101010000010010;
    for (let i = 0; i < 15; i++) {
      const bit = ((formatBits >> (14 - i)) & 1) === 1;

      // Around top-left finder
      if (i < 6) {
        this.modules[8][i] = bit;
      } else if (i === 6) {
        this.modules[8][7] = bit;
      } else if (i === 7) {
        this.modules[8][8] = bit;
      } else if (i === 8) {
        this.modules[7][8] = bit;
      } else {
        this.modules[14 - i][8] = bit;
      }

      // Along edges (top-right and bottom-left)
      if (i < 8) {
        this.modules[this.size - 1 - i][8] = bit;
      } else {
        this.modules[8][this.size - 15 + i] = bit;
      }
    }
  }

  private drawFinderPattern(row: number, col: number, isReserved: boolean[][]): void {
    for (let r = -1; r <= 7; r++) {
      for (let c = -1; c <= 7; c++) {
        const curR = row + r;
        const curC = col + c;
        if (curR >= 0 && curR < this.size && curC >= 0 && curC < this.size) {
          isReserved[curR][curC] = true;
          // Separator boundary
          if (r === -1 || r === 7 || c === -1 || c === 7) {
            this.modules[curR][curC] = false;
          } else if (r === 0 || r === 6 || c === 0 || c === 6 || (r >= 2 && r <= 4 && c >= 2 && c <= 4)) {
            this.modules[curR][curC] = true;
          } else {
            this.modules[curR][curC] = false;
          }
        }
      }
    }
  }

  /**
   * Generates a scalable SVG string with a quiet zone.
   */
  toSVG(margin: number = 4): string {
    const totalSize = this.size + margin * 2;
    let path = '';

    for (let r = 0; r < this.size; r++) {
      for (let c = 0; c < this.size; c++) {
        if (this.modules[r][c]) {
          path += `M${c + margin},${r + margin}h1v1h-1z `;
        }
      }
    }

    return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 ${totalSize} ${totalSize}" shape-rendering="crispEdges" role="img" aria-label="QR Code">
  <rect width="100%" height="100%" fill="#ffffff"/>
  <path d="${path.trim()}" fill="#000000"/>
</svg>`;
  }

  /**
   * Renders the QR code to an HTML Canvas element.
   */
  toCanvas(canvas: HTMLCanvasElement, scale: number = 8, margin: number = 4): void {
    const totalSize = (this.size + margin * 2) * scale;
    canvas.width = totalSize;
    canvas.height = totalSize;
    const ctx = canvas.getContext('2d');
    if (!ctx) return;

    ctx.fillStyle = '#ffffff';
    ctx.fillRect(0, 0, totalSize, totalSize);

    ctx.fillStyle = '#000000';
    for (let r = 0; r < this.size; r++) {
      for (let c = 0; c < this.size; c++) {
        if (this.modules[r][c]) {
          ctx.fillRect((c + margin) * scale, (r + margin) * scale, scale, scale);
        }
      }
    }
  }

  /**
   * Returns a PNG data URL (in browser environments where HTMLCanvasElement exists).
   */
  toDataURL(scale: number = 8, margin: number = 4): string {
    if (typeof document === 'undefined') {
      return '';
    }
    const canvas = document.createElement('canvas');
    this.toCanvas(canvas, scale, margin);
    return canvas.toDataURL('image/png');
  }
}
