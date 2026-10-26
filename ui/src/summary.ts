// One-line summaries of journal events; kinds this build does not know
// render generically.

import type { JournalEvent } from "./journal";

// summary renders e in one line and never throws. Not implemented yet: it
// returns "".
export function summary(_e: JournalEvent): string {
  return "";
}
