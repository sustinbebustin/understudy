// Reference implementations of the S3 flexible-checksum algorithms the suite
// can compute locally, so tests compare understudy against independent code.
// Values are the base64 of the big-endian digest, as sent in x-amz-checksum-*.

import { createHash } from "node:crypto";

export type ChecksumAlgorithm = "CRC32" | "CRC32C" | "CRC64NVME" | "SHA1" | "SHA256";

function toBase64(bytes: Uint8Array): string {
  return Buffer.from(bytes).toString("base64");
}

function uint32Bytes(value: number): Uint8Array {
  const out = new Uint8Array(4);
  new DataView(out.buffer).setUint32(0, value >>> 0, false);
  return out;
}

function crcTable32(polynomial: number): Uint32Array {
  const table = new Uint32Array(256);
  for (let n = 0; n < 256; n += 1) {
    let c = n;
    for (let k = 0; k < 8; k += 1) c = c & 1 ? (c >>> 1) ^ polynomial : c >>> 1;
    table[n] = c >>> 0;
  }
  return table;
}

const CRC32_TABLE = crcTable32(0xedb88320);
const CRC32C_TABLE = crcTable32(0x82f63b78);

function crc32WithTable(table: Uint32Array, data: Uint8Array): number {
  let crc = 0xffffffff;
  for (const byte of data) crc = (table[(crc ^ byte) & 0xff] ?? 0) ^ (crc >>> 8);
  return (crc ^ 0xffffffff) >>> 0;
}

const CRC64_POLYNOMIAL = 0x9a6c9329ac4bc9b5n;
const CRC64_TABLE: bigint[] = (() => {
  const table: bigint[] = [];
  for (let n = 0n; n < 256n; n += 1n) {
    let c = n;
    for (let k = 0; k < 8; k += 1) c = c & 1n ? (c >> 1n) ^ CRC64_POLYNOMIAL : c >> 1n;
    table.push(c);
  }
  return table;
})();

function crc64nvme(data: Uint8Array): Uint8Array {
  const mask = 0xffffffffffffffffn;
  let crc = mask;
  for (const byte of data) crc = (CRC64_TABLE[Number((crc ^ BigInt(byte)) & 0xffn)] ?? 0n) ^ (crc >> 8n);
  crc = (crc ^ mask) & mask;
  const out = new Uint8Array(8);
  new DataView(out.buffer).setBigUint64(0, crc, false);
  return out;
}

/** The raw digest bytes of data under algorithm. */
export function digest(algorithm: ChecksumAlgorithm, data: Uint8Array): Uint8Array {
  switch (algorithm) {
    case "CRC32":
      return uint32Bytes(crc32WithTable(CRC32_TABLE, data));
    case "CRC32C":
      return uint32Bytes(crc32WithTable(CRC32C_TABLE, data));
    case "CRC64NVME":
      return crc64nvme(data);
    case "SHA1":
      return createHash("sha1").update(data).digest();
    case "SHA256":
      return createHash("sha256").update(data).digest();
  }
}

/** The base64 checksum value of data under algorithm. */
export function checksum(algorithm: ChecksumAlgorithm, data: Uint8Array | string): string {
  const bytes = typeof data === "string" ? new TextEncoder().encode(data) : data;
  return toBase64(digest(algorithm, bytes));
}

/** The composite multipart checksum: the algorithm over the concatenated raw part digests, then "-<parts>". */
export function compositeChecksum(algorithm: ChecksumAlgorithm, parts: Uint8Array[]): string {
  const raw = parts.map((part) => digest(algorithm, part));
  const joined = new Uint8Array(raw.reduce((sum, d) => sum + d.length, 0));
  let offset = 0;
  for (const d of raw) {
    joined.set(d, offset);
    offset += d.length;
  }
  return `${toBase64(digest(algorithm, joined))}-${parts.length}`;
}

/** The multipart ETag: MD5 of the concatenated raw part MD5s, then "-<parts>". */
export function multipartEtag(parts: Uint8Array[]): string {
  const joined = Buffer.concat(parts.map((part) => createHash("md5").update(part).digest()));
  return `${createHash("md5").update(joined).digest("hex")}-${parts.length}`;
}

export function md5Hex(data: Uint8Array | string): string {
  return createHash("md5").update(data).digest("hex");
}

/** Deterministic pseudo-random bytes, so large bodies are reproducible and incompressible. */
export function patternBytes(length: number, seed = 1): Uint8Array {
  const out = new Uint8Array(length);
  let state = seed >>> 0 || 1;
  for (let i = 0; i < length; i += 1) {
    state ^= state << 13;
    state >>>= 0;
    state ^= state >>> 17;
    state ^= state << 5;
    state >>>= 0;
    out[i] = state & 0xff;
  }
  return out;
}
