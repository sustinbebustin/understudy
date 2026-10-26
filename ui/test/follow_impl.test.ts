// follow's handling of a tail answer that is not what GET /journal/events
// sends ({"events": [...]}, admin.JournalPage): each bad part is reported
// as a bad line and the stream is still read.

import { describe, expect, test } from "bun:test";

import { follow } from "../src/follow";
import { event } from "./gen";

const ndjson = (text: string) => new Response(text, { status: 200, headers: { "Content-Type": "application/x-ndjson" } });

async function run(tailBody: string): Promise<{ end: unknown; events: number[]; bad: string[] }> {
  const events: number[] = [];
  const bad: string[] = [];
  const end = await follow({
    fetch: (url) => Promise.resolve(url.endsWith("/journal/stream") ? ndjson(`${JSON.stringify(event(8, { trace: 8 }))}\n`) : new Response(tailBody, { status: 200 })),
    headers: {},
    tail: 10,
    signal: new AbortController().signal,
    onEvent: (e) => events.push(e.id),
    onBadLine: (line, reason) => bad.push(reason.length > 0 ? line : `${line} without a reason`),
  });
  return { end, events, bad };
}

describe("follow with a malformed tail answer", () => {
  test("an answer that is not JSON, or has no events array, is one bad line", async () => {
    for (const body of ["<html>", '{"items":[]}', "[]"]) {
      expect(await run(body)).toEqual({ end: { kind: "ended" }, events: [8], bad: [body] });
    }
  });

  test("an element that is not an event is a bad line; the others are delivered", async () => {
    const good = event(5, { trace: 5 });
    expect(await run(JSON.stringify({ events: [good, { id: "x" }] }))).toEqual({ end: { kind: "ended" }, events: [5, 8], bad: ['{"id":"x"}'] });
  });
});
