// Journal durations (latency, init, duration) are integer nanoseconds on
// the wire (internal/journal/ndjson.go encodes time.Duration as its integer
// count). The UI shows each in the largest of ns, µs, ms and s whose value
// is at least 1, chosen before rounding, to one decimal with a trailing .0
// dropped; anything that is not a non-negative integer is shown as given,
// in ns, rather than guessed at.

import { describe, expect, test } from "bun:test";

import { formatDuration } from "../src/format";
import { forAll, int } from "./gen";

describe("formatDuration", () => {
  test("picks the unit before rounding and drops .0", () => {
    const cases: readonly (readonly [number, string])[] = [
      [0, "0ns"],
      [999, "999ns"],
      [1000, "1µs"],
      [1500, "1.5µs"],
      [999_999, "1000µs"],
      [1_000_000, "1ms"],
      [1_234_567, "1.2ms"],
      [2_000_000_000, "2s"],
      [90_000_000_000, "90s"],
    ];
    for (const [ns, want] of cases) {
      expect(formatDuration(ns)).toBe(want);
    }
  });

  test("a value that is not a non-negative integer is shown as given", () => {
    for (const ns of [-1, 1.5, Number.NaN, Number.POSITIVE_INFINITY]) {
      expect(formatDuration(ns)).toBe(`${String(ns)}ns`);
    }
  });

  test("every integer duration reads back within the rounding of its unit", () => {
    const units: Readonly<Record<string, number>> = { ns: 1, "µs": 1e3, ms: 1e6, s: 1e9 };
    forAll(500, (r) => {
      const ns = int(r, 10 ** (1 + int(r, 12)));
      const text = formatDuration(ns);
      const m = /^(\d+(?:\.\d)?)(ns|µs|ms|s)$/.exec(text);
      expect(m === null ? `${ns} -> ${text}` : "matches").toBe("matches");
      if (m === null) {
        return;
      }
      const unit = m[2] ?? "";
      const scale = units[unit] ?? Number.NaN;
      expect(Math.abs(Number(m[1]) * scale - ns)).toBeLessThanOrEqual(scale / 20 + 1e-9 * ns);
      // The largest unit whose value is at least 1.
      expect(ns >= scale || (ns === 0 && unit === "ns")).toBe(true);
      expect(unit === "s" || ns < scale * 1000).toBe(true);
    });
  });
});
