// Following the journal: on a first load, subscribe to the stream first,
// then query the newest events, so nothing recorded between the two is lost
// (ARCHITECTURE.md 17). An event both deliver reaches onEvent twice; the
// forest deduplicates it by id. A resume reopens the stream after the newest
// event an earlier stream delivered, and the server replays what it missed
// (ARCHITECTURE.md 10). The stream is NDJSON read with fetch, not
// EventSource, since every admin call carries the session header (ADR
// 0020).

import { type AdminError, adminError } from "./api";
import { isRecord, type JournalEvent, parseEvent, readValue } from "./journal";
import { lines } from "./ndjson";

// FollowStart is where following starts: a first load queries the newest
// tail events once subscribed; a resume names the id of an event an earlier
// stream delivered, after which the server replays every event recorded
// since, or starts with a Gap for those the journal evicted, and no tail is
// queried.
export type FollowStart = { readonly tail: number } | { readonly after: number };

// DeliveredBy is the request that delivered an event.
export type DeliveredBy = "tail" | "stream";

export type FollowOptions = FollowStart & {
  readonly fetch: (input: string, init: RequestInit) => Promise<Response>;
  readonly headers: Readonly<Record<string, string>>;
  readonly signal: AbortSignal;
  readonly onEvent: (e: JournalEvent, by: DeliveredBy) => void;
  readonly onBadLine: (line: string, reason: string) => void;
};

export type FollowEnd =
  | { readonly kind: "aborted" }
  | { readonly kind: "ended" }
  | {
      readonly kind: "http";
      readonly request: "stream" | "tail";
      readonly status: number;
      readonly error: AdminError | null;
    }
  | { readonly kind: "network"; readonly message: string };

const streamPath = "/_understudy/v1/journal/stream";
const eventsPath = "/_understudy/v1/journal/events";

// deliverTail passes each event of a GET /journal/events answer on, in the
// order sent; an answer or element that is not an event is a bad line.
function deliverTail(text: string, o: FollowOptions): void {
  let body: unknown;
  try {
    body = JSON.parse(text);
  } catch {
    o.onBadLine(text, 'the tail answer is not JSON; want {"events": [...]}');
    return;
  }
  if (!isRecord(body) || !Array.isArray(body.events)) {
    o.onBadLine(text, 'the tail answer has no events array; want {"events": [...]}');
    return;
  }
  for (const v of body.events) {
    const p = readValue(v, JSON.stringify(v));
    if (p.ok) {
      o.onEvent(p.event, "tail");
    } else {
      o.onBadLine(p.line, p.reason);
    }
  }
}

// follow subscribes, queries the tail on a first load, then reads the
// stream until it ends, the signal aborts or a request fails.
export async function follow(o: FollowOptions): Promise<FollowEnd> {
  if (o.signal.aborted) {
    return { kind: "aborted" };
  }
  const init: RequestInit = { method: "GET", headers: o.headers, signal: o.signal };
  try {
    // The server has subscribed once the stream's headers arrive.
    const stream = await o.fetch("after" in o ? `${streamPath}?after=${String(o.after)}` : streamPath, init);
    if (!stream.ok) {
      return { kind: "http", request: "stream", status: stream.status, error: adminError(await stream.text()) };
    }
    if (!("after" in o)) {
      const tail = await o.fetch(`${eventsPath}?tail=${String(o.tail)}`, init);
      if (!tail.ok) {
        await stream.body?.cancel();
        return { kind: "http", request: "tail", status: tail.status, error: adminError(await tail.text()) };
      }
      deliverTail(await tail.text(), o);
    }
    if (stream.body !== null) {
      for await (const line of lines(stream.body)) {
        const p = parseEvent(line);
        if (p.ok) {
          o.onEvent(p.event, "stream");
        } else {
          o.onBadLine(p.line, p.reason);
        }
      }
    }
    return { kind: "ended" };
  } catch (err) {
    if (o.signal.aborted) {
      return { kind: "aborted" };
    }
    const message = err instanceof Error ? err.message : String(err);
    return { kind: "network", message: message === "" ? "the request failed without a reason" : message };
  }
}
