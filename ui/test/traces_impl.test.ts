// Forest behavior beyond the reconstruction rules: its bound is checked,
// malformed parents never hang it or lose an event, and what it returns is
// the caller's to change.

import { describe, expect, test } from "bun:test";

import { Forest, type TraceNode } from "../src/traces";
import { event, gap } from "./gen";

const ids = (n: TraceNode): number[] => [n.event.id, ...n.children.flatMap(ids)];

describe("Forest", () => {
  test("refuses a bound that is not a positive integer", () => {
    for (const bad of [0, -1, 1.5, Number.NaN]) {
      expect(() => new Forest(bad)).toThrow(RangeError);
    }
  });

  test("a cycle of parents, which the journal never writes, keeps every event reachable", () => {
    const f = new Forest();
    f.add(event(1, { trace: 1 }));
    f.add(event(2, { parent: 3, trace: 1 }));
    f.add(event(3, { parent: 2, trace: 1 }));
    const t = f.trace(1);
    if (t === undefined || t.root === null) {
      throw new Error("trace 1 is missing its root");
    }
    expect(t.size).toBe(3);
    expect([...ids(t.root), ...t.orphans.flatMap(ids)].sort((a, b) => a - b)).toEqual([1, 2, 3]);
    // The cycle is listed once, from its smallest id.
    expect(t.orphans.map((o) => [o.event.id, o.orphan, o.children.map((c) => c.event.id)])).toEqual([[2, true, [3]]]);
  });

  test("a pending call may be replaced by a later pending form, then by its end", () => {
    const pending = (latency: number) => event(2, { parent: 1, trace: 1, detail: { outcome: { kind: "pending" }, latency } });
    const f = new Forest();
    f.add(event(1, { trace: 1 }));
    expect(f.add(pending(1))).toBe("added");
    expect(f.add(pending(2))).toBe("replaced");
    expect(f.add(event(2, { parent: 1, trace: 1 }))).toBe("replaced");
    expect(f.add(pending(3))).toBe("duplicate");
    expect(f.trace(1)?.root?.children[0]?.event.detail).toEqual({ outcome: { kind: "ok" } });
  });

  test("gaps() is a fresh value", () => {
    const f = new Forest();
    f.add(gap(2));
    const got = f.gaps();
    (got as { missed: number }[]).length = 0;
    expect(f.gaps()).toEqual([{ missed: 2, afterId: null }]);
  });
});
