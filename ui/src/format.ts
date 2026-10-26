// Formatting of journal values for display.

// Largest first: a duration takes the largest unit whose value is at least 1.
const units: readonly (readonly [string, number])[] = [
  ["s", 1e9],
  ["ms", 1e6],
  ["µs", 1e3],
];

// formatDuration renders integer nanoseconds, the journal's duration unit
// (internal/journal/ndjson.go), in the largest of ns, µs, ms and s whose
// value is at least 1, chosen before rounding, to one decimal with ".0"
// dropped. Anything that is not a non-negative integer is shown as given,
// in ns.
export function formatDuration(ns: number): string {
  if (!Number.isInteger(ns) || ns < 0) {
    return `${String(ns)}ns`;
  }
  for (const [unit, scale] of units) {
    if (ns >= scale) {
      return `${String(Math.round((ns / scale) * 10) / 10)}${unit}`;
    }
  }
  return `${String(ns)}ns`;
}
