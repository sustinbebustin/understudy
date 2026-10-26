// Which views an event makes stale, so resets, snapshot loads, fault edits
// and clock moves show without polling.

import type { JournalEvent } from "./journal";

export type View = "resources" | "faults" | "clock" | "iam";

// refreshFor returns the views to refetch after e. Not implemented yet: it
// returns none.
export function refreshFor(_e: JournalEvent): readonly View[] {
  return [];
}
