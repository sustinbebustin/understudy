// The TypeScript half of the label drift guard: labels.gen.ts is generated
// from the Go enumerations by TestLabelTable (internal/server/ui), and every
// name it lists has a label, so a value added in Go fails here until the UI
// names it. Anything else renders by its wire name.

import { describe, expect, test } from "bun:test";

import { idKindLabels, kindLabels, label, mechanismLabels, viaLabels } from "../src/labels";
import { idKinds, journalKinds, mechanisms, vias } from "../src/labels.gen";

const tables = [
  ["journalKinds", journalKinds, kindLabels],
  ["vias", vias, viaLabels],
  ["mechanisms", mechanisms, mechanismLabels],
  ["idKinds", idKinds, idKindLabels],
] as const;

describe("labels", () => {
  for (const [list, names, table] of tables) {
    test(`every generated ${list} name has a label`, () => {
      expect(names.length).toBeGreaterThan(0);
      for (const name of names) {
        const text = table[name];
        expect(typeof text === "string" && text.length > 0 ? name : `${name} has no label`).toBe(name);
      }
    });
  }

  // Kinds in value order, api-call first and a kind added later appended
  // (internal/journal/kind_wire_test.go); gap and admin-action from ADR 0007.
  test("the generated journal kinds are in value order and hold gap and admin-action", () => {
    const names: readonly string[] = journalKinds;
    expect(names).toContain("gap");
    expect(names).toContain("admin-action");
    expect(names[0]).toBe("api-call");
  });

  test("label falls back to the wire name", () => {
    expect(label({ "api-call": "API call" }, "api-call")).toBe("API call");
    expect(label({ "api-call": "API call" }, "future-kind")).toBe("future-kind");
    expect(label({ "api-call": "" }, "api-call")).toBe("api-call");
    expect(label({}, "")).toBe("");
  });
});
