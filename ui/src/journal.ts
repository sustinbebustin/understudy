// Journal events in their NDJSON form (admin.JournalEvent), parsed at the
// boundary: a line that is not an event is refused with a reason, never
// passed on half-read. Unknown kinds parse, so a newer server's events
// render generically; a Gap carries id 0 (ADR 0007).

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

// isRecord reports a JSON object: not null, not an array.
export function isRecord(v: unknown): v is Readonly<Record<string, unknown>> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

// isCount reports a non-negative integer.
export function isCount(v: unknown): v is number {
  return typeof v === "number" && Number.isInteger(v) && v >= 0;
}

const isPositive = (v: unknown): v is number => isCount(v) && v > 0;

// An event, or the reason a value is not one.
type Read = { readonly event: JournalEvent } | { readonly reason: string };

function readEvent(v: unknown): Read {
  if (!isRecord(v)) {
    return { reason: "the line is not a JSON object" };
  }
  const { id, cause, links, at, wall, scope, kind, detail } = v;
  if (!isCount(id)) {
    return { reason: "id is not a non-negative integer" };
  }
  if (!isRecord(cause)) {
    return { reason: "cause is not an object" };
  }
  const { parent, trace } = cause;
  if (parent !== undefined && !isCount(parent)) {
    return { reason: "cause.parent is not a non-negative integer" };
  }
  if (trace !== undefined && !isCount(trace)) {
    return { reason: "cause.trace is not a non-negative integer" };
  }
  if (links !== undefined && !(Array.isArray(links) && links.every(isPositive))) {
    return { reason: "links is not an array of positive integers" };
  }
  if (typeof at !== "string") {
    return { reason: "at is not a string" };
  }
  if (typeof wall !== "string") {
    return { reason: "wall is not a string" };
  }
  if (!isRecord(scope)) {
    return { reason: "scope is not an object" };
  }
  const { partition, account, region } = scope;
  if (typeof partition !== "string" || typeof account !== "string" || typeof region !== "string") {
    return { reason: "scope does not hold partition, account and region strings" };
  }
  if (typeof kind !== "string" || kind === "") {
    return { reason: "kind is not a non-empty string" };
  }
  if (!isRecord(detail)) {
    return { reason: "detail is not an object" };
  }
  return {
    event: {
      id,
      cause: { ...(parent === undefined ? {} : { parent }), ...(trace === undefined ? {} : { trace }) },
      ...(links === undefined ? {} : { links }),
      at,
      wall,
      scope: { partition, account, region },
      kind,
      detail,
    },
  };
}

// parseEvent parses one NDJSON line into an event.
export function parseEvent(line: string): Parsed {
  let v: unknown;
  try {
    v = JSON.parse(line);
  } catch (err) {
    return { ok: false, line, reason: `the line is not JSON: ${err instanceof Error ? err.message : String(err)}` };
  }
  return readValue(v, line);
}

// readValue checks an already decoded value, such as one element of GET
// /journal/events' events, as parseEvent checks a line; line is what a
// refusal reports.
export function readValue(v: unknown, line: string): Parsed {
  const read = readEvent(v);
  return "event" in read ? { ok: true, event: read.event } : { ok: false, line, reason: read.reason };
}
