// Which views an event makes stale, so resets, snapshot loads, fault edits
// and clock moves show without polling: the admin handler journals each as
// an admin-action or clock-changed event (ADR 0007 pre-tag amendment), and a
// fired fault rule as fault-injected.

import type { JournalEvent } from "./journal";

export type View = "resources" | "faults" | "clock" | "iam";

const everyView: readonly View[] = ["resources", "faults", "clock", "iam"];

// refreshFor returns the views to refetch after e.
export function refreshFor(e: JournalEvent): readonly View[] {
  switch (e.kind) {
    case "admin-action":
      return [...everyView];
    case "clock-changed":
      return ["clock"];
    case "fault-injected":
      return ["faults"];
    default:
      return [];
  }
}
