// Live causal trees built from journal events as they arrive: children may
// arrive before their parents, evicted parents leave orphans, gaps stand for
// lost events and links are not edges (ADR 0007; internal/journal/doc.go).
//
// The forest holds each trace's events by id and builds the trees when
// asked, from each event's parent, so the order events arrive in never
// changes a tree: a child that arrived first simply finds its parent once
// the parent is held.

import { isCount, isRecord, type JournalEvent } from "./journal";

export type TraceNode = {
  readonly event: JournalEvent;
  // orphan: names a parent that is not held in the same trace.
  readonly orphan: boolean;
  // children: ascending id.
  readonly children: readonly TraceNode[];
};

export type Trace = {
  // id: the trace id, the root event's id.
  readonly id: number;
  // root: null while the root event is not held.
  readonly root: TraceNode | null;
  // orphans: non-root nodes of this trace whose parent is not held, in
  // ascending id.
  readonly orphans: readonly TraceNode[];
  // size: the events of this trace held.
  readonly size: number;
};

export type Gap = { readonly missed: number; readonly afterId: number | null };

export type AddResult = "added" | "replaced" | "duplicate" | "gap";

const isPositive = (v: unknown): v is number => isCount(v) && v > 0;

// traceIdOf is cause.trace when a positive integer, else the event's own id.
function traceIdOf(e: JournalEvent): number {
  return isPositive(e.cause.trace) ? e.cause.trace : e.id;
}

// parentOf is cause.parent when a positive integer other than the event's
// own id.
function parentOf(e: JournalEvent): number | undefined {
  const p = e.cause.parent;
  return isPositive(p) && p !== e.id ? p : undefined;
}

// isPending reports an api-call still in flight: queries see it, and a
// stream sees it once, when it ends (doc.go).
function isPending(e: JournalEvent): boolean {
  return e.kind === "api-call" && isRecord(e.detail.outcome) && e.detail.outcome.kind === "pending";
}

// build makes trace id's value from its events.
function build(id: number, members: ReadonlyMap<number, JournalEvent>): Trace {
  const sorted = [...members.values()].sort((a, b) => a.id - b.id);
  const childrenOf = new Map<number, JournalEvent[]>();
  const orphanEvents: JournalEvent[] = [];
  for (const e of sorted) {
    if (e.id === id) {
      continue;
    }
    const p = parentOf(e);
    if (p !== undefined && members.has(p)) {
      const siblings = childrenOf.get(p);
      if (siblings === undefined) {
        childrenOf.set(p, [e]);
      } else {
        siblings.push(e);
      }
    } else {
      orphanEvents.push(e);
    }
  }
  // seen guards against a cycle of parents, which the journal never
  // writes: a node is placed once.
  const seen = new Set<number>();
  const node = (e: JournalEvent, orphan: boolean): TraceNode => {
    seen.add(e.id);
    const children = (childrenOf.get(e.id) ?? []).filter((c) => !seen.has(c.id)).map((c) => node(c, false));
    return { event: e, orphan, children };
  };
  // A root that names a parent is a child of an evicted event, which the
  // journal roots in its own trace (doc.go): it is marked orphan.
  const rootEvent = members.get(id);
  const rootParent = rootEvent === undefined ? undefined : parentOf(rootEvent);
  const root = rootEvent === undefined ? null : node(rootEvent, rootParent !== undefined && !members.has(rootParent));
  const orphans = orphanEvents.filter((e) => !seen.has(e.id)).map((e) => node(e, true));
  // Events on a cycle are reachable from nothing above; list each cycle
  // once, from its smallest id, rather than lose it.
  for (const e of sorted) {
    if (!seen.has(e.id) && e.id !== id) {
      orphans.push(node(e, true));
    }
  }
  orphans.sort((a, b) => a.event.id - b.event.id);
  return { id, root, orphans, size: members.size };
}

// Forest holds the traces of the events added to it, at most maxTraces of
// them: a new trace beyond the bound drops the held trace with the smallest
// id, with all its events.
export class Forest {
  readonly #maxTraces: number;
  // #traces: each held trace's events by id.
  readonly #traces = new Map<number, Map<number, JournalEvent>>();
  // #traceOf: the trace each held event is in.
  readonly #traceOf = new Map<number, number>();
  readonly #gaps: Gap[] = [];
  #highest: number | null = null;

  constructor(maxTraces = 1000) {
    if (!isPositive(maxTraces)) {
      throw new RangeError(`Forest: maxTraces is ${String(maxTraces)}, want a positive integer`);
    }
    this.#maxTraces = maxTraces;
  }

  // add places e in its trace: "gap" for a Gap event, "duplicate" for an id
  // already held (nothing changes), "replaced" when e ends a held pending
  // call, which keeps its place and children, else "added".
  add(e: JournalEvent): AddResult {
    if (e.kind === "gap") {
      this.#gaps.push({ missed: isCount(e.detail.missed) ? e.detail.missed : 0, afterId: this.#highest });
      return "gap";
    }
    this.#highest = this.#highest === null ? e.id : Math.max(this.#highest, e.id);
    const heldIn = this.#traceOf.get(e.id);
    if (heldIn !== undefined) {
      const members = this.#traces.get(heldIn);
      const held = members?.get(e.id);
      if (members === undefined || held === undefined || !isPending(held)) {
        return "duplicate";
      }
      members.set(e.id, e);
      return "replaced";
    }
    const t = traceIdOf(e);
    let members = this.#traces.get(t);
    if (members === undefined) {
      if (this.#traces.size >= this.#maxTraces) {
        this.#drop(Math.min(...this.#traces.keys()));
      }
      members = new Map();
      this.#traces.set(t, members);
    }
    members.set(e.id, e);
    this.#traceOf.set(e.id, t);
    return "added";
  }

  // has reports whether the event with id is held.
  has(id: number): boolean {
    return this.#traceOf.has(id);
  }

  // trace returns the held trace with id, as a fresh value.
  trace(id: number): Trace | undefined {
    const members = this.#traces.get(id);
    return members === undefined ? undefined : build(id, members);
  }

  // traces returns every held trace, by descending trace id, as fresh values.
  traces(): readonly Trace[] {
    return [...this.#traces.keys()].sort((a, b) => b - a).map((id) => build(id, this.#traces.get(id) ?? new Map()));
  }

  // gaps returns the Gaps added, in arrival order.
  gaps(): readonly Gap[] {
    return this.#gaps.map((g) => ({ ...g }));
  }

  #drop(id: number): void {
    for (const eventId of this.#traces.get(id)?.keys() ?? []) {
      this.#traceOf.delete(eventId);
    }
    this.#traces.delete(id);
  }
}
