// Journal events in their NDJSON form (admin.JournalEvent), parsed at the
// boundary.

export type Cause = { readonly parent?: number; readonly trace?: number };

export type Scope = {
  readonly partition: string;
  readonly account: string;
  readonly region: string;
};

export type JournalEvent = {
  readonly id: number;
  readonly cause: Cause;
  readonly links?: readonly number[];
  readonly at: string;
  readonly wall: string;
  readonly scope: Scope;
  readonly kind: string;
  readonly detail: Readonly<Record<string, unknown>>;
};

export type Parsed =
  | { readonly ok: true; readonly event: JournalEvent }
  | { readonly ok: false; readonly line: string; readonly reason: string };

// parseEvent parses one NDJSON line into an event. Not implemented yet: it
// refuses every line.
export function parseEvent(line: string): Parsed {
  return { ok: false, line, reason: "parseEvent is not implemented yet" };
}
