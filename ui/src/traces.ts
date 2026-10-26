// Live causal trees built from journal events as they arrive: children may
// arrive before their parents, evicted parents leave orphans, gaps stand for
// lost events and links are not edges (ADR 0007).

import type { JournalEvent } from "./journal";

export type TraceNode = {
  readonly event: JournalEvent;
  readonly orphan: boolean;
  readonly children: readonly TraceNode[];
};

export type Trace = {
  readonly id: number;
  readonly root: TraceNode | null;
  readonly orphans: readonly TraceNode[];
  readonly size: number;
};

export type Gap = { readonly missed: number; readonly afterId: number | null };

export type AddResult = "added" | "replaced" | "duplicate" | "gap";

// Forest holds the traces of the events added to it. Not implemented yet:
// it holds nothing.
export class Forest {
  constructor(_maxTraces = 1000) {}

  add(_e: JournalEvent): AddResult {
    return "duplicate";
  }

  has(_id: number): boolean {
    return false;
  }

  trace(_id: number): Trace | undefined {
    return undefined;
  }

  traces(): readonly Trace[] {
    return [];
  }

  gaps(): readonly Gap[] {
    return [];
  }
}
