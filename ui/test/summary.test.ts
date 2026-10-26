// One-line event summaries. The UI renders a kind it does not know
// generically, so a newer server's events still show (the journal appends
// new kinds: internal/journal/kind_wire_test.go), and no detail, however
// malformed, breaks the page.

import { describe, expect, test } from "bun:test";

import type { JournalEvent } from "../src/journal";
import { journalKinds } from "../src/labels.gen";
import { summary } from "../src/summary";
import { event, forAll, int, pick, type Rng } from "./gen";

/** A random JSON value, nested up to depth. */
function value(r: Rng, depth: number): unknown {
  switch (int(r, depth > 0 ? 7 : 5)) {
    case 0:
      return null;
    case 1:
      return int(r, 2) === 0;
    case 2:
      return pick(r, [0, -1, 1.5, 1e21, 42]);
    case 3:
      return pick(r, ["", "x", "é€😀", "<script>"]);
    case 4:
      return pick(r, ["sqs", "ok", "pending", "reset"]);
    case 5:
      return Array.from({ length: int(r, 3) }, () => value(r, depth - 1));
    default:
      return detail(r, depth - 1);
  }
}

function detail(r: Rng, depth: number): Record<string, unknown> {
  const members = ["op", "outcome", "from", "to", "rule", "missed", "kind", "function", "resource", "key", "mechanism", "path", "rules"];
  const out: Record<string, unknown> = {};
  for (let i = int(r, 4); i > 0; i--) {
    out[pick(r, members)] = value(r, depth);
  }
  return out;
}

describe("summary", () => {
  test("an unknown kind renders as its kind and detail", () => {
    const e = event(1, { kind: "future-kind", detail: { a: 1, b: ["x"] } });
    expect(summary(e)).toBe('future-kind {"a":1,"b":["x"]}');
  });

  test("never throws and never answers empty, whatever the detail", () => {
    const kinds: readonly string[] = [...journalKinds, "future-kind"];
    forAll(500, (r) => {
      const e: JournalEvent = event(1 + int(r, 100), { kind: pick(r, kinds.length > 1 ? kinds : ["api-call", "future-kind"]), detail: detail(r, 3) });
      const text = summary(e);
      expect(typeof text === "string" && text.length > 0 ? "text" : `${JSON.stringify(e)} -> ${String(text)}`).toBe("text");
    });
  });
});
