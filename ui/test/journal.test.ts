// Parsing one NDJSON journal line at the boundary, in the form the journal
// writes (internal/journal/ndjson.go; error outcomes as in
// internal/journal/ndjson_test.go): a line that is not an event is reported
// with a reason, never passed on half-read. Unknown kinds parse, so a newer
// server's events render generically; a Gap carries id 0 (ADR 0007).

import { describe, expect, test } from "bun:test";

import { parseEvent } from "../src/journal";
import { event, forAll, int, pick } from "./gen";

const line =
  '{"id":7,"cause":{"parent":3,"trace":1},"links":[4,5],"at":"2026-01-01T00:01:30Z","wall":"2026-03-01T12:00:00Z",' +
  '"scope":{"partition":"aws","account":"000000000000","region":"us-east-1"},"kind":"api-call",' +
  '"detail":{"outcome":{"kind":"failed","identity":{"shape":"com.amazonaws.sqs#QueueDoesNotExist"},"status":400,"message":"The specified queue does not exist."},"latency":1500000}}';

/** line with member set to value (undefined removes it). */
function variant(member: string, value: unknown): string {
  const base: Record<string, unknown> = JSON.parse(line);
  if (value === undefined) {
    delete base[member];
  } else {
    base[member] = value;
  }
  return JSON.stringify(base);
}

describe("parseEvent", () => {
  test("a journal line parses to its event", () => {
    expect(parseEvent(line)).toEqual({ ok: true, event: JSON.parse(line) });
  });

  test("unknown kinds, gaps and roots without a parent parse", () => {
    for (const ok of [
      variant("kind", "future-kind"),
      variant("links", undefined),
      '{"id":0,"cause":{},"at":"2026-01-01T00:00:00Z","wall":"2026-01-01T00:00:00Z","scope":{"partition":"","account":"","region":""},"kind":"gap","detail":{"missed":42}}',
      variant("cause", { trace: 7 }),
      variant("detail", {}),
    ]) {
      const got = parseEvent(ok);
      expect(got.ok ? got.event : got).toEqual(JSON.parse(ok));
    }
  });

  test("a line that is not an event is refused with a reason", () => {
    const bad = [
      "not json",
      "[1]",
      "null",
      "42",
      variant("id", -1),
      variant("id", 1.5),
      variant("id", "7"),
      variant("id", undefined),
      variant("cause", undefined),
      variant("cause", null),
      variant("cause", [1]),
      variant("cause", { parent: -1 }),
      variant("cause", { parent: 1.5 }),
      variant("cause", { trace: "1" }),
      variant("links", { 0: 4 }),
      variant("links", [0]),
      variant("links", [1.5]),
      variant("links", ["4"]),
      variant("at", 5),
      variant("at", undefined),
      variant("wall", undefined),
      variant("scope", undefined),
      variant("scope", { partition: "aws", account: "000000000000" }),
      variant("scope", { partition: 1, account: "000000000000", region: "us-east-1" }),
      variant("kind", ""),
      variant("kind", 5),
      variant("kind", undefined),
      variant("detail", null),
      variant("detail", []),
      variant("detail", "x"),
      variant("detail", undefined),
    ];
    for (const text of bad) {
      const got = parseEvent(text);
      expect(got.ok ? `${text} parsed` : { line: got.line, reasoned: got.reason.length > 0 }).toEqual({ line: text, reasoned: true });
    }
  });

  test("every event the journal could write reads back as itself", () => {
    const kinds = ["api-call", "delivery", "invocation", "timer-fired", "state-change", "fault-injected", "clock-changed", "advisory", "unclaimed", "admin-action", "future-kind"];
    forAll(300, (r) => {
      const id = 1 + int(r, 1_000_000);
      const parent = int(r, 3) === 0 ? undefined : int(r, id);
      const e = event(id, {
        ...(parent === undefined || parent === 0 ? {} : { parent }),
        trace: parent === undefined || parent === 0 ? id : 1 + int(r, parent),
        links: int(r, 3) === 0 ? [1 + int(r, id), 1 + int(r, id)] : [],
        kind: pick(r, kinds),
        detail: { n: int(r, 100), text: "é <&>", nested: { list: [1, "two", null] } },
      });
      const text = JSON.stringify(e);
      expect(parseEvent(text)).toEqual({ ok: true, event: JSON.parse(text) });
    });
  });
});
