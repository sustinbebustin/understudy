// Test helpers: journal event builders and a seeded generator for the
// hand-written property cases (bun has no property-testing library, and the
// UI takes no dependency: ADR 0016).

import type { JournalEvent } from "../src/journal";

export type Rng = () => number;

/** mulberry32: a small seeded PRNG, so a failing case is replayed by its seed. */
export function rng(seed: number): Rng {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/** An integer in [0, n). */
export function int(r: Rng, n: number): number {
  return Math.floor(r() * n);
}

export function pick<T>(r: Rng, xs: readonly T[]): T {
  const x = xs[int(r, xs.length)];
  if (x === undefined) {
    throw new Error("pick from an empty list");
  }
  return x;
}

/** A shuffled copy of xs (Fisher-Yates). */
export function shuffle<T>(r: Rng, xs: readonly T[]): T[] {
  const out = [...xs];
  for (let i = out.length - 1; i > 0; i--) {
    const j = int(r, i + 1);
    const a = out[i];
    const b = out[j];
    if (a === undefined || b === undefined) {
      throw new Error("shuffle index out of range");
    }
    out[i] = b;
    out[j] = a;
  }
  return out;
}

/** Runs body for seeds 1..runs, naming the seed of the first failure. */
export function forAll(runs: number, body: (r: Rng, seed: number) => void): void {
  for (let seed = 1; seed <= runs; seed++) {
    try {
      body(rng(seed), seed);
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      throw new Error(`seed ${seed}: ${message}`, { cause: err });
    }
  }
}

export type EventSpec = {
  readonly parent?: number;
  readonly trace?: number;
  readonly links?: readonly number[];
  readonly kind?: string;
  readonly detail?: Readonly<Record<string, unknown>>;
};

/**
 * An event as the journal's NDJSON writes it (internal/journal/ndjson.go):
 * cause members omitted when zero, links omitted when empty.
 */
export function event(id: number, s: EventSpec = {}): JournalEvent {
  const cause: { parent?: number; trace?: number } = {};
  if (s.parent !== undefined) {
    cause.parent = s.parent;
  }
  if (s.trace !== undefined) {
    cause.trace = s.trace;
  }
  return {
    id,
    cause,
    ...(s.links !== undefined && s.links.length > 0 ? { links: s.links } : {}),
    at: "2026-10-26T12:00:00Z",
    wall: "2026-10-26T12:00:00.5Z",
    scope: { partition: "aws", account: "000000000000", region: "us-east-1" },
    kind: s.kind ?? "api-call",
    detail: s.detail ?? { outcome: { kind: "ok" } },
  };
}

/** A Gap as a stream writes it: id 0, kind gap, detail {missed} (ADR 0007). */
export function gap(missed: unknown): JournalEvent {
  return {
    id: 0,
    cause: {},
    at: "2026-10-26T12:00:00Z",
    wall: "2026-10-26T12:00:00.5Z",
    scope: { partition: "", account: "", region: "" },
    kind: "gap",
    detail: { missed },
  };
}
