// What the page refetches when the stream reports a change, so resets,
// snapshot loads, fault edits and clock moves show without polling: the
// admin handler journals each as an admin-action or clock-changed event
// (ADR 0007, amendment 2026-10-13), and a fired fault rule as fault-injected.
// Resources group by account and region, as GET /resources scopes them.

import { describe, expect, test } from "bun:test";

import { groupByScope, type Resource } from "../src/resources";
import { refreshFor } from "../src/views";
import { event, forAll, int, pick, shuffle } from "./gen";

describe("refreshFor", () => {
  test("each change signal names the views it invalidates", () => {
    expect(refreshFor(event(1, { kind: "admin-action", detail: { op: "reset" } }))).toEqual(["resources", "faults", "clock", "iam"]);
    expect(refreshFor(event(1, { kind: "clock-changed", detail: { op: "advance" } }))).toEqual(["clock"]);
    expect(refreshFor(event(1, { kind: "fault-injected", detail: { rule: "r", hit: 1, of: 2 } }))).toEqual(["faults"]);
    for (const kind of ["api-call", "delivery", "invocation", "gap", "future-kind"]) {
      expect(refreshFor(event(1, { kind }))).toEqual([]);
    }
  });
});

describe("groupByScope", () => {
  const r = (account: string, region: string, arn: string): Resource => ({ service: "sqs", account, region, arn });

  test("accounts and regions ascending, resources in input order", () => {
    const a = r("222222222222", "us-east-1", "arn:aws:sqs:us-east-1:222222222222:b");
    const b = r("111111111111", "us-west-2", "arn:aws:sqs:us-west-2:111111111111:z");
    const c = r("111111111111", "eu-west-1", "arn:aws:sqs:eu-west-1:111111111111:y");
    const d = r("111111111111", "us-west-2", "arn:aws:sqs:us-west-2:111111111111:a");
    expect(groupByScope([a, b, c, d])).toEqual([
      { account: "111111111111", regions: [{ region: "eu-west-1", resources: [c] }, { region: "us-west-2", resources: [b, d] }] },
      { account: "222222222222", regions: [{ region: "us-east-1", resources: [a] }] },
    ]);
    expect(groupByScope([])).toEqual([]);
  });

  test("every resource lands once, in its own group, and no group is empty", () => {
    forAll(200, (rand) => {
      const accounts = ["000000000000", "111111111111", "222222222222"];
      const regions = ["eu-west-1", "us-east-1", "us-west-2"];
      const rs = Array.from({ length: int(rand, 12) }, (_, i) => r(pick(rand, accounts), pick(rand, regions), `arn:aws:sqs:x:y:q${i}`));
      const groups = groupByScope(shuffle(rand, rs));
      const flat = groups.flatMap((g) => g.regions.flatMap((reg) => reg.resources.map((res) => [g.account, reg.region, res] as const)));
      expect(flat.length).toBe(rs.length);
      for (const [account, region, res] of flat) {
        expect([res.account, res.region]).toEqual([account, region]);
      }
      expect(groups.every((g) => g.regions.length > 0 && g.regions.every((reg) => reg.resources.length > 0))).toBe(true);
      const accountOrder = groups.map((g) => g.account);
      expect(accountOrder).toEqual([...accountOrder].sort());
      for (const g of groups) {
        const regionOrder = g.regions.map((reg) => reg.region);
        expect(regionOrder).toEqual([...regionOrder].sort());
      }
    });
  });
});
