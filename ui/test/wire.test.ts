// Reading the admin answers the views show, in the forms a build of
// understudy answers them (admin/client.go, admin/state.go,
// internal/coverage/report.go): a body that is not the answer is refused
// with a reason rather than rendered half-read.

import { describe, expect, test } from "bun:test";

import { readClock, readClockResult, readCoverage, readFaults, readPolicy, readPrincipals, readResources } from "../src/wire";

// GET /faults after PUT /faults/r1, as a build answered it.
const faultList = {
  faults: [
    {
      rule: { id: "r1", phase: "request", match: { operations: ["sqs.SendMessage"] }, action: { kind: "throttle" }, trigger: { kind: "next-n", n: 2 } },
      hits: 0,
      warning: 'fault rule "r1" injects RequestThrottled, which AWS SDKs retry',
    },
  ],
};

describe("readers", () => {
  test("GET /faults gives each rule's id, phase, action, trigger, hits and warning", () => {
    expect(readFaults(faultList)).toEqual({
      ok: true,
      value: [{ id: "r1", phase: "request", action: "throttle", trigger: "next-n 2", hits: 0, warning: faultList.faults[0]?.warning, rule: faultList.faults[0]?.rule }],
    });
    expect(readFaults({ faults: [] })).toEqual({ ok: true, value: [] });
    expect(readFaults({ faults: [{ rule: { id: "r1" }, hits: 0 }] }).ok).toBe(false);
  });

  test("GET /clock and POST /clock", () => {
    const running = { now: "2026-10-26T20:52:12.079872274Z", mode: "running", wall: "2026-10-26T20:52:12.079872394Z" };
    expect(readClock(running)).toEqual({ ok: true, value: running });
    expect(readClock({ ...running, mode: "scaled", factor: 60 })).toEqual({ ok: true, value: { ...running, mode: "scaled", factor: 60 } });
    expect(readClock({ now: 1 }).ok).toBe(false);
    expect(readClockResult({ clock: running })).toEqual({ ok: true, value: { clock: running, fired: [] } });
    expect(readClockResult({ clock: running, fired: [{ kind: "sqs.visibility", id: "q/m" }] })).toEqual({
      ok: true,
      value: { clock: running, fired: [{ kind: "sqs.visibility", id: "q/m" }] },
    });
    expect(readClockResult({ clock: running, fired: [{ kind: 1 }] }).ok).toBe(false);
  });

  test("GET /resources", () => {
    const q = { service: "sqs", account: "000000000000", region: "us-east-1", arn: "arn:aws:sqs:us-east-1:000000000000:q1" };
    expect(readResources({ resources: [q, { ...q, summary: "2 messages" }] })).toEqual({ ok: true, value: [q, { ...q, summary: "2 messages" }] });
    expect(readResources({ resources: [{ ...q, arn: 5 }] })).toEqual({ ok: false, reason: expect.stringContaining("resources[0]") });
    expect(readResources({}).ok).toBe(false);
  });

  test("GET /iam/policy and the principals of GET /iam/usage", () => {
    const document = { Version: "2012-10-17", Statement: [{ Effect: "Allow", Action: ["sqs:SendMessage"], Resource: ["arn:aws:sqs:us-east-1:000000000000:q1"] }] };
    const principal = "arn:aws:iam::000000000000:role/worker";
    expect(readPolicy({ principal, document })).toEqual({ ok: true, value: { principal, document, resourcePolicies: [] } });
    const rp = { target: "arn:aws:sqs:us-east-1:000000000000:q1", principal: "sns.amazonaws.com", action: "sqs:SendMessage", source: "arn:aws:sns:us-east-1:000000000000:t1" };
    expect(readPolicy({ principal, document, resourcePolicies: [rp] })).toEqual({ ok: true, value: { principal, document, resourcePolicies: [rp] } });
    expect(readPolicy({ principal }).ok).toBe(false);
    const use = (p: string) => ({ principal: p, mode: "enforce", action: "sqs:SendMessage", resource: "*", calls: 1, first: "2026-01-01T00:00:00Z", last: "2026-01-01T00:00:00Z" });
    expect(readPrincipals({ usage: [use("b"), use("a"), use("b")] })).toEqual({ ok: true, value: ["b", "a"] });
    expect(readPrincipals({ usage: [] })).toEqual({ ok: true, value: [] });
  });

  test("GET /coverage counts each service's operations by status", () => {
    const report = {
      protocols: [{ protocol: "awsjson1_0", kind: "request", cases: 26, passed: 20, failed: 0, skipped: [], knownFailures: [] }],
      ungenerated: [],
      services: [
        {
          service: "sqs",
          operations: [
            { name: "SendMessage", status: { kind: "full" } },
            { name: "AddPermission", status: { kind: "partial", note: "conditions are not evaluated" } },
            { name: "TagQueue", status: { kind: "stored-only", note: "tags have no effect" } },
            { name: "ListDeadLetterSourceQueues", status: { kind: "unbound" } },
            { name: "ReceiveMessage", status: { kind: "full" } },
          ],
          internal: [],
          raw: [],
        },
      ],
      switches: [],
      overlays: { kind: "not-carried" },
    };
    expect(readCoverage(report)).toEqual({
      ok: true,
      value: {
        services: [{ service: "sqs", counts: { full: 2, partial: 1, "stored-only": 1, unbound: 1 } }],
        protocols: [{ protocol: "awsjson1_0", kind: "request", cases: 26, passed: 20 }],
      },
    });
    const future = { ...report, services: [{ service: "sqs", operations: [{ name: "X", status: { kind: "future" } }] }] };
    expect(readCoverage(future)).toEqual({ ok: false, reason: expect.stringContaining("operation X") });
  });
});
