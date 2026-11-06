// Following the journal on load: subscribe to the stream first, then query
// the tail, so no event recorded in between is missed; an event both
// deliver is deduplicated by the forest. The stream is NDJSON read with
// fetch, not EventSource, since every admin call carries the session
// header (ADR 0007: NDJSON stream; ADR 0020: the secret in a custom request
// header). Errors arrive in the admin envelope (ADR 0017).

import { describe, expect, test } from "bun:test";

import { follow, type FollowEnd, type FollowOptions } from "../src/follow";
import type { JournalEvent } from "../src/journal";
import { event } from "./gen";

const streamURL = "/_understudy/v1/journal/stream";
const headers = { "Understudy-UI-Session": "ABCDEFGHIJKLMNOPQRSTUVWX27" };

type Deferred<T> = { readonly promise: Promise<T>; resolve(v: T): void };

function deferred<T>(): Deferred<T> {
  let resolve: (v: T) => void = () => {};
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

/** A response body the test writes lines into. */
function liveBody() {
  let controller: ReadableStreamDefaultController<Uint8Array> | undefined;
  let cancelled = false;
  const stream = new ReadableStream<Uint8Array>({
    start(c) {
      controller = c;
    },
    cancel() {
      cancelled = true;
    },
  });
  const ctl = () => {
    if (controller === undefined) {
      throw new Error("stream not started");
    }
    return controller;
  };
  return {
    stream,
    push: (text: string) => ctl().enqueue(new TextEncoder().encode(text)),
    close: () => ctl().close(),
    fail: (err: unknown) => ctl().error(err),
    cancelled: () => cancelled,
  };
}

type Seen = { url: string; init: RequestInit };
type Answer = () => Promise<Response>;

const unexpected = (what: string): Answer => () => Promise.reject(new Error(`unexpected ${what}`));
const failing = (err: Error): Answer => () => Promise.reject(err);

/** A fake fetch: the stream answers with stream(), the tail with tail(). */
function fakeFetch(stream: Answer, tail: Answer) {
  const seen: Seen[] = [];
  const fetch = (url: string, init: RequestInit): Promise<Response> => {
    seen.push({ url, init });
    if (init.signal?.aborted === true) {
      return Promise.reject(new DOMException("The operation was aborted.", "AbortError"));
    }
    if (url === streamURL) {
      return stream();
    }
    if (url.startsWith("/_understudy/v1/journal/events?")) {
      return tail();
    }
    return unexpected(`fetch of ${url}`)();
  };
  return { fetch, seen };
}

type Got = { events: number[]; bad: [string, string][] };

function options(fetch: FollowOptions["fetch"], signal: AbortSignal, got: Got): FollowOptions {
  return {
    fetch,
    headers,
    tail: 500,
    signal,
    onEvent: (e: JournalEvent) => got.events.push(e.id),
    onBadLine: (line: string, reason: string) => got.bad.push([line, reason]),
  };
}

const got = (): Got => ({ events: [], bad: [] });
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const lineOf = (id: number) => `${JSON.stringify(event(id, { trace: id }))}\n`;
const ndjson = (body: ReadableStream<Uint8Array>): Answer => () => Promise.resolve(new Response(body, { status: 200, headers: { "Content-Type": "application/x-ndjson" } }));
const answer = (status: number, body: unknown): Answer => () => Promise.resolve(json(status, body));

/** Lets pending promise callbacks run. */
async function settle(): Promise<void> {
  for (let i = 0; i < 20; i++) {
    await new Promise<void>((r) => setTimeout(r, 0));
  }
}

describe("follow", () => {
  test("subscribes, then queries the tail, and delivers the tail before every stream event", async () => {
    const body = liveBody();
    const stream = deferred<Response>();
    const tail = deferred<Response>();
    const { fetch, seen } = fakeFetch(() => stream.promise, () => tail.promise);
    const g = got();
    const signal = new AbortController().signal;
    const done = follow(options(fetch, signal, g));

    await settle();
    // The tail waits for the stream's response: the server has subscribed
    // once the headers arrive.
    expect(seen.map((s) => s.url)).toEqual([streamURL]);
    stream.resolve(new Response(body.stream, { status: 200, headers: { "Content-Type": "application/x-ndjson" } }));
    await settle();
    expect(seen.map((s) => s.url)).toEqual([streamURL, "/_understudy/v1/journal/events?tail=500"]);
    for (const s of seen) {
      expect([s.init.method, s.init.headers, s.init.signal]).toEqual(["GET", headers, signal]);
    }

    // Recorded while the tail query is in flight: on the stream, and maybe
    // in the tail too.
    body.push(lineOf(6));
    body.push(lineOf(9));
    await settle();
    expect(g.events).toEqual([]);
    tail.resolve(json(200, { events: [event(5, { trace: 5 }), event(6, { trace: 6 })] }));
    await settle();
    body.push(`not an event\n${lineOf(10)}`);
    body.close();
    expect(await done).toEqual({ kind: "ended" });
    expect(g.events).toEqual([5, 6, 6, 9, 10]);
    expect(g.bad.map(([line]) => line)).toEqual(["not an event"]);
    expect(g.bad.every(([, reason]) => reason.length > 0)).toBe(true);
  });

  test("a refused stream ends with its status and error, and no tail query", async () => {
    const refused = { error: { code: "unauthorized", message: "the admin API needs the token", hint: "send Authorization: Bearer <token>" } };
    const { fetch, seen } = fakeFetch(answer(401, refused), unexpected("tail query"));
    const end: FollowEnd = await follow(options(fetch, new AbortController().signal, got()));
    expect(end).toEqual({ kind: "http", request: "stream", status: 401, error: refused.error });
    expect(seen.length).toBe(1);
  });

  test("a refused tail cancels the stream and ends with its status and error", async () => {
    const body = liveBody();
    const refused = { error: { code: "invalid_request", message: "tail cannot be combined with limit or cursor" } };
    const g = got();
    const { fetch } = fakeFetch(ndjson(body.stream), answer(400, refused));
    expect(await follow(options(fetch, new AbortController().signal, g))).toEqual({ kind: "http", request: "tail", status: 400, error: refused.error });
    expect(body.cancelled()).toBe(true);
    expect(g.events).toEqual([]);
  });

  test("an error body that is not the admin envelope ends with a null error", async () => {
    const { fetch } = fakeFetch(() => Promise.resolve(new Response("<html>bad gateway</html>", { status: 502 })), unexpected("tail query"));
    expect(await follow(options(fetch, new AbortController().signal, got()))).toEqual({ kind: "http", request: "stream", status: 502, error: null });
  });

  test("an abort before or during the stream ends as aborted", async () => {
    const before = new AbortController();
    before.abort();
    const never: Answer = () => new Promise<Response>(() => {});
    expect(await follow(options(fakeFetch(never, never).fetch, before.signal, got()))).toEqual({ kind: "aborted" });

    const during = new AbortController();
    const body = liveBody();
    during.signal.addEventListener("abort", () => body.fail(new DOMException("The operation was aborted.", "AbortError")));
    const g = got();
    const done = follow(options(fakeFetch(ndjson(body.stream), answer(200, { events: [] })).fetch, during.signal, g));
    body.push(lineOf(3));
    await settle();
    during.abort();
    expect(await done).toEqual({ kind: "aborted" });
    expect(g.events).toEqual([3]);
  });

  test("a fetch or read that fails for another reason ends as network", async () => {
    const unreachable = fakeFetch(failing(new TypeError("Failed to fetch")), unexpected("tail query"));
    const end = await follow(options(unreachable.fetch, new AbortController().signal, got()));
    expect(end.kind === "network" && end.message.length > 0 ? "network" : JSON.stringify(end)).toBe("network");

    const body = liveBody();
    const done = follow(options(fakeFetch(ndjson(body.stream), answer(200, { events: [] })).fetch, new AbortController().signal, got()));
    await settle();
    body.fail(new TypeError("network error"));
    const broken = await done;
    expect(broken.kind === "network" && broken.message.length > 0 ? "network" : JSON.stringify(broken)).toBe("network");
  });
});
