// Reading the journal stream: NDJSON over a chunked HTTP response (ADR
// 0007), read with fetch and a ReadableStream. Chunk boundaries fall
// anywhere, inside a line and inside a multi-byte UTF-8 character
// (https://encoding.spec.whatwg.org/#interface-textdecoder, stream: true).

import { describe, expect, test } from "bun:test";

import { lines } from "../src/ndjson";
import { forAll, int, pick } from "./gen";

/** A body that delivers chunks one read at a time. */
function body(chunks: readonly Uint8Array[]): ReadableStream<Uint8Array> {
  let i = 0;
  return new ReadableStream<Uint8Array>({
    pull(controller) {
      const chunk = chunks[i++];
      if (chunk === undefined) {
        controller.close();
      } else {
        controller.enqueue(chunk);
      }
    },
  });
}

async function collect(chunks: readonly Uint8Array[]): Promise<string[]> {
  const out: string[] = [];
  for await (const l of lines(body(chunks))) {
    out.push(l);
  }
  return out;
}

const utf8 = (s: string) => new TextEncoder().encode(s);

describe("lines", () => {
  test("yields each line without its line end, the last even when unterminated", async () => {
    expect(await collect([utf8('{"id":1}\n{"id":2}\r\n{"id":3}')])).toEqual(['{"id":1}', '{"id":2}', '{"id":3}']);
  });

  test("skips empty and whitespace-only lines", async () => {
    expect(await collect([utf8("\n  \n{\"id\":1}\n\t\r\n\n{\"id\":2}\n")])).toEqual(['{"id":1}', '{"id":2}']);
    expect(await collect([])).toEqual([]);
  });

  test("a character split between chunks arrives whole", async () => {
    const bytes = utf8('{"text":"é€😀"}\n');
    for (let cut = 1; cut < bytes.length; cut++) {
      expect(await collect([bytes.slice(0, cut), bytes.slice(cut)])).toEqual(['{"text":"é€😀"}']);
    }
  });

  test("any chunking of a stream yields its non-blank lines in order", async () => {
    const pieces = ['{"id":1}', '{"text":"é"}', "", "  ", '{"text":"€😀\\n"}', '{"kind":"gap"}'];
    const runs: Promise<void>[] = [];
    forAll(200, (r, seed) => {
      const want = Array.from({ length: int(r, 8) }, () => pick(r, pieces));
      const crlf = int(r, 2) === 0;
      const text = want.join(crlf ? "\r\n" : "\n") + (int(r, 2) === 0 ? "\n" : "");
      const bytes = utf8(text);
      const chunks: Uint8Array[] = [];
      for (let at = 0; at < bytes.length; ) {
        const size = 1 + int(r, 6);
        chunks.push(bytes.slice(at, at + size));
        at += size;
      }
      runs.push(
        collect(chunks).then((got) => {
          expect({ seed, lines: got }).toEqual({ seed, lines: want.filter((l) => l.trim() !== "") });
        }),
      );
    });
    await Promise.all(runs);
  });
});
