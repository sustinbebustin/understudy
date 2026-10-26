// Live trace reconstruction from journal events as the stream and the tail
// deliver them. The rules follow the journal's event model (ADR 0007;
// internal/journal/doc.go): recording order is not id order, so a child can
// arrive before its parent; an evicted parent never arrives, and a child of
// an evicted parent roots its own trace; a slow stream consumer gets a Gap;
// links are not edges; in-function calls are children of the Invoke call.

import { describe, expect, test } from "bun:test";

import type { JournalEvent } from "../src/journal";
import { Forest, type Trace, type TraceNode } from "../src/traces";
import { event, forAll, gap, int, pick, type Rng, shuffle } from "./gen";

type Shape = { readonly id: number; readonly orphan: boolean; readonly children: readonly Shape[] };
type TraceShape = { readonly id: number; readonly root: Shape | null; readonly orphans: readonly Shape[]; readonly size: number };

const shape = (n: TraceNode): Shape => ({ id: n.event.id, orphan: n.orphan, children: n.children.map(shape) });
const traceShape = (t: Trace): TraceShape => ({
  id: t.id,
  root: t.root === null ? null : shape(t.root),
  orphans: t.orphans.map(shape),
  size: t.size,
});
const node = (id: number, children: readonly Shape[] = [], orphan = false): Shape => ({ id, orphan, children });

function shapeOf(f: Forest, id: number): TraceShape | undefined {
  const t = f.trace(id);
  return t === undefined ? undefined : traceShape(t);
}

function forestOf(events: readonly JournalEvent[], maxTraces?: number): Forest {
  const f = new Forest(maxTraces);
  for (const e of events) {
    f.add(e);
  }
  return f;
}

describe("trees", () => {
  test("children hang from their parents in ascending id order", () => {
    const f = new Forest();
    for (const e of [event(1, { trace: 1 }), event(3, { parent: 1, trace: 1 }), event(2, { parent: 1, trace: 1 }), event(4, { parent: 2, trace: 1 })]) {
      expect(f.add(e)).toBe("added");
    }
    expect(shapeOf(f, 1)).toEqual({ id: 1, root: node(1, [node(2, [node(4)]), node(3)]), orphans: [], size: 4 });
    for (const id of [1, 2, 3, 4]) {
      expect(f.has(id)).toBe(true);
    }
    expect(f.has(5)).toBe(false);
  });

  test("the trace id is cause.trace when a positive integer, else the event's own id", () => {
    const f = new Forest();
    f.add(event(1));
    f.add(event(2, { trace: 0 }));
    f.add(event(3, { trace: 1.5 }));
    f.add(event(4, { parent: 4, trace: 4 }));
    expect(f.traces().map((t) => t.id)).toEqual([4, 3, 2, 1]);
    for (const id of [1, 2, 3, 4]) {
      // A parent equal to the event's own id names no parent.
      expect(shapeOf(f, id)).toEqual({ id, root: node(id), orphans: [], size: 1 });
    }
  });

  test("traces() lists the traces by descending trace id", () => {
    const f = forestOf([event(1, { trace: 1 }), event(7, { trace: 7 }), event(3, { trace: 3 }), event(8, { parent: 1, trace: 1 })]);
    expect(f.traces().map((t) => t.id)).toEqual([7, 3, 1]);
  });
});

describe("child before parent", () => {
  // A call's id comes from Begin, so a call that ends after its internal
  // calls is recorded, and streamed, after them (doc.go).
  test("an orphan moves under its parent when the parent arrives", () => {
    const f = new Forest();
    f.add(event(4, { parent: 2, trace: 1 }));
    expect(shapeOf(f, 1)).toEqual({ id: 1, root: null, orphans: [node(4, [], true)], size: 1 });
    f.add(event(2, { parent: 1, trace: 1 }));
    expect(shapeOf(f, 1)).toEqual({ id: 1, root: null, orphans: [node(2, [node(4)], true)], size: 2 });
    f.add(event(1, { trace: 1 }));
    expect(shapeOf(f, 1)).toEqual({ id: 1, root: node(1, [node(2, [node(4)])]), orphans: [], size: 3 });
  });

  test("every arrival order of one trace gives the same tree", () => {
    const events = [
      event(1, { trace: 1 }),
      event(2, { parent: 1, trace: 1 }),
      event(3, { parent: 2, trace: 1 }),
      event(4, { parent: 1, trace: 1 }),
      event(5, { parent: 3, trace: 1 }),
    ];
    const want = traceShape(forestOf(events).traces()[0] ?? fail("no trace"));
    for (const order of permutations(events)) {
      expect(forestOf(order).traces().map(traceShape)).toEqual([want]);
    }
  });
});

describe("evicted parents", () => {
  test("a parent that never arrives leaves its child an orphan for good", () => {
    const f = forestOf([event(1, { trace: 1 }), event(5, { parent: 3, trace: 1 }), event(6, { parent: 5, trace: 1 })]);
    expect(shapeOf(f, 1)).toEqual({ id: 1, root: node(1), orphans: [node(5, [node(6)], true)], size: 3 });
  });

  test("a child of an evicted parent roots its own trace and is marked orphan", () => {
    // The journal gives a child whose parent is no longer held its own
    // trace (doc.go: a root, or a child whose parent was evicted, roots its
    // own trace).
    const f = forestOf([event(7, { parent: 3, trace: 7 }), event(8, { parent: 7, trace: 7 })]);
    expect(shapeOf(f, 7)).toEqual({ id: 7, root: node(7, [node(8)], true), orphans: [], size: 2 });
    // Its parent arriving later, in another trace, does not move it.
    f.add(event(1, { trace: 1 }));
    f.add(event(3, { parent: 1, trace: 1 }));
    expect(shapeOf(f, 7)).toEqual({ id: 7, root: node(7, [node(8)], true), orphans: [], size: 2 });
    expect(shapeOf(f, 1)).toEqual({ id: 1, root: node(1, [node(3)]), orphans: [], size: 2 });
  });

  test("a trace whose root is not held lists its events under orphans", () => {
    const f = forestOf([event(4, { parent: 2, trace: 1 }), event(2, { parent: 1, trace: 1 }), event(3, { parent: 1, trace: 1 })]);
    expect(shapeOf(f, 1)).toEqual({ id: 1, root: null, orphans: [node(2, [node(4)], true), node(3, [], true)], size: 3 });
  });

  test("a non-root event without a parent is an orphan of its trace", () => {
    const f = forestOf([event(1, { trace: 1 }), event(2, { trace: 1 })]);
    expect(shapeOf(f, 1)).toEqual({ id: 1, root: node(1), orphans: [node(2, [], true)], size: 2 });
  });
});

describe("gaps", () => {
  // A slow stream consumer gets a Gap event in place of what it missed
  // (ADR 0007); streams write it with id 0.
  test("a gap is recorded in arrival order with the highest id seen before it, never as part of a trace", () => {
    const f = new Forest();
    expect(f.add(gap(3))).toBe("gap");
    f.add(event(5, { trace: 5 }));
    f.add(event(2, { trace: 2 }));
    expect(f.add(gap(7))).toBe("gap");
    expect(f.add(gap(7))).toBe("gap");
    expect(f.gaps()).toEqual([
      { missed: 3, afterId: null },
      { missed: 7, afterId: 5 },
      { missed: 7, afterId: 5 },
    ]);
    expect(f.has(0)).toBe(false);
    expect(f.traces().map((t) => t.id)).toEqual([5, 2]);
  });

  test("a gap whose missed count is not a non-negative integer counts 0", () => {
    const f = new Forest();
    for (const missed of [-1, 1.5, "4", null, undefined, Number.NaN]) {
      f.add(gap(missed));
    }
    expect(f.gaps().map((g) => g.missed)).toEqual([0, 0, 0, 0, 0, 0]);
  });

  test("a duplicate counts toward the highest id seen", () => {
    const f = new Forest();
    f.add(event(9, { trace: 9 }));
    f.add(event(9, { trace: 9 }));
    f.add(gap(1));
    expect(f.gaps()).toEqual([{ missed: 1, afterId: 9 }]);
  });
});

describe("deduplication", () => {
  // Subscribe-then-query delivers an event that arrives during the query on
  // both, so the forest sees it twice.
  test("an id already held is a duplicate and changes nothing", () => {
    const f = forestOf([event(1, { trace: 1 }), event(2, { parent: 1, trace: 1 })]);
    const before = f.traces().map(traceShape);
    expect(f.add(event(2, { parent: 1, trace: 1, kind: "delivery", detail: { outcome: { kind: "delivered" } } }))).toBe("duplicate");
    expect(f.add(event(1, { trace: 1 }))).toBe("duplicate");
    expect(f.traces().map(traceShape)).toEqual(before);
    expect(f.trace(1)?.root?.children[0]?.event.kind).toBe("api-call");
  });

  test("a pending call is replaced in place by its ended form, keeping its children", () => {
    // A pending call is visible to queries, and streams see it once, when it
    // ends (doc.go).
    const pending = event(2, { parent: 1, trace: 1, detail: { outcome: { kind: "pending" } } });
    const ended = event(2, { parent: 1, trace: 1, detail: { outcome: { kind: "ok" }, latency: 1500 } });
    const f = forestOf([event(1, { trace: 1 }), pending, event(3, { parent: 2, trace: 1 })]);
    expect(f.add(ended)).toBe("replaced");
    const held = f.trace(1)?.root?.children[0];
    expect(held?.event).toEqual(ended);
    expect(held?.children.map((c) => c.event.id)).toEqual([3]);
    expect(f.trace(1)?.size).toBe(3);
    expect(f.add(ended)).toBe("duplicate");
  });

  test("only a pending api-call is replaced", () => {
    const f = forestOf([event(1, { trace: 1 }), event(2, { parent: 1, trace: 1, kind: "invocation", detail: { outcome: { kind: "pending" } } })]);
    expect(f.add(event(2, { parent: 1, trace: 1, kind: "invocation", detail: { outcome: { kind: "ok" } } }))).toBe("duplicate");
    expect(f.trace(1)?.root?.children[0]?.event.detail).toEqual({ outcome: { kind: "pending" } });
  });
});

describe("links", () => {
  // The ESM invocation's parent is the earliest message's event; the rest
  // are links (ADR 0007).
  test("an event hangs from its parent, never from what it links to", () => {
    const f = forestOf([
      event(1, { trace: 1 }),
      event(2, { parent: 1, trace: 1 }),
      event(3, { parent: 1, trace: 1 }),
      event(4, { parent: 2, trace: 1, links: [3], kind: "invocation", detail: { outcome: { kind: "ok" } } }),
      event(5, { parent: 99, trace: 1, links: [3] }),
    ]);
    expect(shapeOf(f, 1)).toEqual({ id: 1, root: node(1, [node(2, [node(4)]), node(3)]), orphans: [node(5, [], true)], size: 5 });
    expect(f.trace(1)?.root?.children[0]?.children[0]?.event.links).toEqual([3]);
  });
});

describe("in-function calls", () => {
  // In-function calls are children of the Invoke call, the Invocation
  // event's own parent, which is written when the invocation ends (ADR
  // 0007 pre-tag amendment).
  test("the calls a function makes and its Invocation are all children of the Invoke call, in any arrival order", () => {
    const invoke = event(10, { trace: 10, detail: { op: { service: "lambda", name: "Invoke" }, outcome: { kind: "ok" } } });
    const inFunction = [
      event(11, { parent: 10, trace: 10, detail: { op: { service: "sqs", name: "SendMessage" }, outcome: { kind: "ok" } } }),
      event(12, { parent: 10, trace: 10, detail: { op: { service: "s3", name: "PutObject" }, outcome: { kind: "ok" } } }),
    ];
    const invocation = event(13, { parent: 10, trace: 10, kind: "invocation", detail: { outcome: { kind: "ok" } } });
    for (const order of permutations([invoke, ...inFunction, invocation])) {
      expect(forestOf(order).traces().map(traceShape)).toEqual([{ id: 10, root: node(10, [node(11), node(12), node(13)]), orphans: [], size: 4 }]);
    }
  });
});

describe("bound", () => {
  test("a new trace beyond maxTraces drops the held trace with the smallest id, with all its events", () => {
    const f = new Forest(2);
    f.add(event(1, { trace: 1 }));
    f.add(event(2, { parent: 1, trace: 1 }));
    f.add(event(3, { trace: 3 }));
    f.add(event(5, { trace: 5 }));
    expect(f.traces().map((t) => t.id)).toEqual([5, 3]);
    expect([f.has(1), f.has(2), f.trace(1)]).toEqual([false, false, undefined]);
    // An event of a held trace drops nothing.
    f.add(event(6, { parent: 5, trace: 5 }));
    expect(f.traces().map((t) => t.id)).toEqual([5, 3]);
    // An event of a dropped trace starts it again, dropping the smallest held.
    f.add(event(4, { parent: 2, trace: 1 }));
    expect(f.traces().map((t) => t.id)).toEqual([5, 1]);
    expect(shapeOf(f, 1)).toEqual({ id: 1, root: null, orphans: [node(4, [], true)], size: 1 });
    expect(f.has(3)).toBe(false);
  });

  test("the default bound is 1000 traces", () => {
    const f = new Forest();
    for (let id = 1; id <= 1001; id++) {
      f.add(event(id, { trace: id }));
    }
    expect(f.traces().length).toBe(1000);
    expect(f.has(1)).toBe(false);
    expect(f.has(2)).toBe(true);
  });
});

describe("returned values", () => {
  test("traces() and trace() are fresh values the caller may change", () => {
    const f = forestOf([event(1, { trace: 1 }), event(2, { parent: 1, trace: 1 }), event(3, { parent: 9, trace: 1 })]);
    const want = f.traces().map(traceShape);
    for (const t of [f.trace(1), f.traces()[0]]) {
      if (t === undefined || t.root === null) {
        throw new Error("trace 1 is missing its root");
      }
      (t.root.children as TraceNode[]).length = 0;
      (t.orphans as TraceNode[]).push(t.root);
      (t.root as { orphan: boolean }).orphan = true;
      (t as { size: number }).size = 0;
    }
    expect(f.traces().map(traceShape)).toEqual(want);
  });
});

// Hand-written property cases: seeded random journals checked against a
// model of the rules above.

/** The trace id rule: cause.trace when a positive integer, else the id. */
function traceIdOf(e: JournalEvent): number {
  const t = e.cause.trace;
  return t !== undefined && Number.isInteger(t) && t > 0 ? t : e.id;
}

/** The parent rule: cause.parent when a positive integer other than the id. */
function parentOf(e: JournalEvent): number | undefined {
  const p = e.cause.parent;
  return p !== undefined && Number.isInteger(p) && p > 0 && p !== e.id ? p : undefined;
}

/** The model: the traces the rules give for held events of distinct ids. */
function model(held: readonly JournalEvent[]): TraceShape[] {
  const byTrace = new Map<number, JournalEvent[]>();
  for (const e of held) {
    const t = traceIdOf(e);
    byTrace.set(t, [...(byTrace.get(t) ?? []), e]);
  }
  const out: TraceShape[] = [];
  for (const [t, events] of byTrace) {
    const ids = new Set(events.map((e) => e.id));
    const sorted = [...events].sort((a, b) => a.id - b.id);
    const build = (e: JournalEvent): Shape => {
      const p = parentOf(e);
      const orphan = e.id === t ? p !== undefined && !ids.has(p) : p === undefined || !ids.has(p);
      const children = sorted.filter((c) => c.id !== t && parentOf(c) === e.id).map(build);
      return { id: e.id, orphan, children };
    };
    const root = sorted.find((e) => e.id === t);
    const orphans = sorted.filter((e) => e.id !== t && build(e).orphan).map(build);
    out.push({ id: t, root: root === undefined ? null : build(root), orphans, size: events.length });
  }
  return out.sort((a, b) => b.id - a.id);
}

const kinds = ["api-call", "delivery", "invocation", "timer-fired", "state-change", "future-kind"] as const;

/**
 * A random journal: traces whose events have ascending ids, each child
 * after its parent (Begin issues a parent's id before its children's),
 * some with links to earlier events, some evicted (never delivered), and
 * some children of evicted events rooting their own trace (doc.go).
 */
function journal(r: Rng, maxTraces: number): JournalEvent[] {
  let next = 1;
  const traces: { readonly id: number; readonly members: number[] }[] = [];
  const all: JournalEvent[] = [];
  const steps = 1 + int(r, 40);
  for (let s = 0; s < steps; s++) {
    const kind = pick(r, kinds);
    const links = next > 1 && r() < 0.3 ? [1 + int(r, next - 1)] : [];
    const t = traces.length > 0 && (traces.length >= maxTraces || r() < 0.7) ? pick(r, traces) : undefined;
    if (t === undefined) {
      all.push(event(next, { trace: next, kind, links }));
      traces.push({ id: next, members: [next] });
    } else {
      all.push(event(next, { parent: pick(r, t.members), trace: t.id, kind, links }));
      t.members.push(next);
    }
    next++;
  }
  const delivered = all.filter(() => r() >= 0.2);
  const evicted = all.filter((e) => !delivered.includes(e));
  if (evicted.length > 0 && traces.length < maxTraces && r() < 0.5) {
    const parent = pick(r, evicted).id;
    delivered.push(event(next, { parent, trace: next }), event(next + 1, { parent: next, trace: next }));
  }
  return delivered;
}

describe("properties", () => {
  test("any arrival order of distinct events within the bound gives the model's traces", () => {
    forAll(300, (r) => {
      const events = journal(r, 1000);
      const want = model(events);
      expect(forestOf(events).traces().map(traceShape)).toEqual(want);
      expect(forestOf(shuffle(r, events)).traces().map(traceShape)).toEqual(want);
      expect(forestOf([...events].reverse()).traces().map(traceShape)).toEqual(want);
    });
  });

  test("every event passed in is held and reachable, as the event passed in", () => {
    forAll(200, (r) => {
      const events = journal(r, 1000);
      const f = forestOf(shuffle(r, events));
      const seen = new Map<number, JournalEvent>();
      const walk = (n: TraceNode) => {
        seen.set(n.event.id, n.event);
        n.children.forEach(walk);
      };
      for (const t of f.traces()) {
        if (t.root !== null) {
          walk(t.root);
        }
        t.orphans.forEach(walk);
      }
      for (const e of events) {
        expect(f.has(e.id)).toBe(true);
        expect(seen.get(e.id)).toEqual(e);
      }
      expect(seen.size).toBe(events.length);
    });
  });

  test("delivering events twice, as subscribe-then-query does, changes nothing", () => {
    forAll(200, (r) => {
      const events = journal(r, 1000);
      const f = new Forest();
      const results: string[] = [];
      for (const e of shuffle(r, [...events, ...events])) {
        results.push(f.add(e));
      }
      expect(f.traces().map(traceShape)).toEqual(model(events));
      expect(results.filter((x) => x === "added").length).toBe(events.length);
      expect(results.filter((x) => x === "duplicate").length).toBe(events.length);
    });
  });

  test("gaps interleaved anywhere leave the traces alone and are kept in arrival order", () => {
    forAll(200, (r) => {
      const events = journal(r, 1000);
      const arrivals = shuffle(r, events);
      const f = new Forest();
      const want: { missed: number; afterId: number | null }[] = [];
      let highest: number | null = null;
      for (const e of arrivals) {
        if (r() < 0.3) {
          const missed = int(r, 50);
          expect(f.add(gap(missed))).toBe("gap");
          want.push({ missed, afterId: highest });
        }
        f.add(e);
        highest = highest === null ? e.id : Math.max(highest, e.id);
      }
      expect(f.gaps()).toEqual(want);
      expect(f.traces().map(traceShape)).toEqual(model(events));
    });
  });

  test("beyond the bound the forest holds what dropping the smallest trace leaves", () => {
    forAll(200, (r) => {
      const maxTraces = 1 + int(r, 4);
      const events = journal(r, 3 * maxTraces);
      const arrivals = shuffle(r, events);
      const f = new Forest(maxTraces);
      // The bound rule, restated: a new trace at the bound drops the held
      // trace with the smallest id and every event of it.
      const held = new Map<number, JournalEvent[]>();
      for (const e of arrivals) {
        f.add(e);
        const t = traceIdOf(e);
        if (!held.has(t)) {
          if (held.size >= maxTraces) {
            held.delete(Math.min(...held.keys()));
          }
          held.set(t, []);
        }
        held.get(t)?.push(e);
      }
      const kept = [...held.values()].flat();
      expect(f.traces().map(traceShape)).toEqual(model(kept));
      for (const e of events) {
        expect(f.has(e.id)).toBe(kept.includes(e));
      }
    });
  });
});

function fail(message: string): never {
  throw new Error(message);
}

/** Every ordering of xs. */
function permutations<T>(xs: readonly T[]): T[][] {
  if (xs.length <= 1) {
    return [[...xs]];
  }
  const out: T[][] = [];
  xs.forEach((x, i) => {
    for (const rest of permutations([...xs.slice(0, i), ...xs.slice(i + 1)])) {
      out.push([x, ...rest]);
    }
  });
  return out;
}
