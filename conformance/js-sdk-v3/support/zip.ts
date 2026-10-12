// A minimal zip writer for Lambda deployment packages: deflated entries, no
// extra fields, enough for any unzip implementation.

import { deflateRawSync } from "node:zlib";
import { digest } from "./checksums";

function crc32(data: Uint8Array): number {
  return new DataView(digest("CRC32", data).buffer).getUint32(0, false);
}

/** A zip archive holding files, a map of entry name to text content. */
export function zip(files: Record<string, string>): Uint8Array {
  const encoder = new TextEncoder();
  const chunks: Uint8Array[] = [];
  const directory: Uint8Array[] = [];
  let offset = 0;

  for (const [name, text] of Object.entries(files)) {
    const nameBytes = encoder.encode(name);
    const raw = encoder.encode(text);
    const packed = deflateRawSync(raw);
    const crc = crc32(raw);

    const local = new Uint8Array(30 + nameBytes.length);
    const lv = new DataView(local.buffer);
    lv.setUint32(0, 0x04034b50, true);
    lv.setUint16(4, 20, true);
    lv.setUint16(8, 8, true);
    lv.setUint32(14, crc, true);
    lv.setUint32(18, packed.length, true);
    lv.setUint32(22, raw.length, true);
    lv.setUint16(26, nameBytes.length, true);
    local.set(nameBytes, 30);

    const entry = new Uint8Array(46 + nameBytes.length);
    const ev = new DataView(entry.buffer);
    ev.setUint32(0, 0x02014b50, true);
    ev.setUint16(4, 20, true);
    ev.setUint16(6, 20, true);
    ev.setUint16(10, 8, true);
    ev.setUint32(16, crc, true);
    ev.setUint32(20, packed.length, true);
    ev.setUint32(24, raw.length, true);
    ev.setUint16(28, nameBytes.length, true);
    ev.setUint32(42, offset, true);
    entry.set(nameBytes, 46);

    chunks.push(local, packed);
    directory.push(entry);
    offset += local.length + packed.length;
  }

  const directorySize = directory.reduce((sum, entry) => sum + entry.length, 0);
  const end = new Uint8Array(22);
  const dv = new DataView(end.buffer);
  dv.setUint32(0, 0x06054b50, true);
  dv.setUint16(8, directory.length, true);
  dv.setUint16(10, directory.length, true);
  dv.setUint32(12, directorySize, true);
  dv.setUint32(16, offset, true);

  return Buffer.concat([...chunks, ...directory, end]);
}
