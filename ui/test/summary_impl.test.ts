// Known kinds' summaries over the detail forms the journal writes
// (internal/journal/ndjson.go), mirroring the CLI's (internal/cli/journal.go),
// and the generic form for a known kind whose detail lacks what its summary
// reads.

import { describe, expect, test } from "bun:test";

import { shortARN, summary } from "../src/summary";
import { event } from "./gen";

describe("summary of known kinds", () => {
  test("each kind names what the CLI names", () => {
    const cases: readonly (readonly [string, Record<string, unknown>, string])[] = [
      [
        "api-call",
        {
          service: "sqs",
          operation: "SendMessage",
          origin: { kind: "internal", via: "sns-delivery" },
          outcome: { kind: "failed", identity: { shape: "com.amazonaws.sqs#QueueDoesNotExist" }, status: 400 },
          latency: 1_500_000,
        },
        "sqs.SendMessage failed QueueDoesNotExist 400 1.5ms via SNS delivery",
      ],
      ["api-call", { service: "s3", operation: "", origin: { kind: "client" }, outcome: { kind: "injected", rule: "r1" } }, "s3.<unknown operation> injected by rule r1"],
      [
        "delivery",
        { from: "arn:aws:sns:us-east-1:000000000000:t1", to: "arn:aws:sqs:us-east-1:000000000000:q1", mechanism: "sns-to-sqs", attempt: 1, outcome: { kind: "delivered" } },
        "SNS to SQS t1 -> q1 attempt 1 delivered",
      ],
      [
        "delivery",
        { from: "arn:aws:sns:us-east-1:000000000000:t1", to: "", endpoint: "127.0.0.1:9000", mechanism: "sns-to-sqs", attempt: 2, outcome: { kind: "retrying", next: "2026-01-01T00:00:10Z" } },
        "SNS to SQS t1 -> 127.0.0.1:9000 attempt 2 retrying, next attempt at 2026-01-01T00:00:10Z",
      ],
      [
        "invocation",
        { function: "arn:aws:lambda:us-east-1:000000000000:function:worker", version: "3", cold: true, duration: 120_000_000, outcome: { kind: "function-error", type: "Unhandled" } },
        "worker:3 function-error Unhandled cold 120ms",
      ],
      ["invocation", { function: "arn:aws:lambda:us-east-1:000000000000:function:worker", version: "$LATEST", outcome: { kind: "exit-error", code: 2 } }, "worker exit-error code 2"],
      ["timer-fired", { timerKind: "sqs.visibility", timerId: "q1/m1" }, "sqs.visibility q1/m1"],
      ["state-change", { resource: "arn:aws:lambda:us-east-1:000000000000:function:worker", from: "Pending", to: "Active" }, "worker Pending -> Active"],
      ["clock-changed", { op: "advance", from: { kind: "frozen", at: "2026-01-01T00:00:00Z" }, to: { kind: "frozen", at: "2026-01-01T00:01:30Z" } }, "advance: frozen at 2026-01-01T00:00:00Z -> frozen at 2026-01-01T00:01:30Z"],
      ["clock-changed", { from: { kind: "running" }, to: { kind: "scaled", factor: 60 } }, "running -> scaled x60"],
      ["advisory", { advisory: "policy-would-deny", resource: "q1", text: "the queue policy denies sns" }, "policy-would-deny: the queue policy denies sns"],
      ["unclaimed", { method: "GET", host: "localhost:4566", path: "/x", reasons: ["no service", "no prefix"] }, "GET localhost:4566/x: no service; no prefix"],
      ["gap", { missed: 42 }, "missed 42 event(s)"],
      ["admin-action", { op: "reset", services: ["sqs", "sns"], clock: true, faults: 2 }, "reset sqs, sns; every account; every region; clock restored; 2 fault rule(s) removed"],
      ["admin-action", { op: "reset", services: ["sqs"], accounts: ["111111111111"], regions: ["us-east-1"] }, "reset sqs; accounts 111111111111; regions us-east-1"],
      ["admin-action", { op: "snapshot-save", path: "before.tar" }, "snapshot saved to before.tar"],
      ["admin-action", { op: "snapshot-save" }, "snapshot saved to the response"],
      ["admin-action", { op: "snapshot-load" }, "snapshot loaded from the request"],
      ["admin-action", { op: "fault-rule-added", rule: "orders-throttle" }, "fault rule orders-throttle added"],
      ["admin-action", { op: "fault-rule-removed", rules: ["a", "b"] }, "fault rule(s) a, b removed"],
      ["admin-action", { op: "fault-rule-removed" }, "no fault rules to remove"],
    ];
    for (const [kind, detail, want] of cases) {
      expect(summary(event(1, { kind, detail }))).toBe(want);
    }
  });

  test("a fault hit names the event of the call it hit", () => {
    expect(summary(event(9, { parent: 8, trace: 1, kind: "fault-injected", detail: { rule: "r", hit: 1, of: 3 } }))).toBe("rule r hit 1 of 3, on event 8");
  });

  test("a known kind whose detail lacks its members renders generically", () => {
    for (const [kind, detail] of [
      ["api-call", { operation: "SendMessage" }],
      ["delivery", { mechanism: "sns-to-sqs", outcome: {} }],
      ["clock-changed", { op: "freeze", from: { kind: "running" }, to: {} }],
      ["admin-action", { op: "future-op" }],
      ["admin-action", { op: "fault-rule-added" }],
      ["gap", { missed: -1 }],
    ] as const) {
      expect(summary(event(1, { kind, detail }))).toBe(`${kind} ${JSON.stringify(detail)}`);
    }
  });
});

describe("shortARN", () => {
  test("drops the ARN prefix and a resource type, and leaves other text alone", () => {
    expect(shortARN("arn:aws:sqs:us-east-1:000000000000:q1")).toBe("q1");
    expect(shortARN("arn:aws:lambda:us-east-1:000000000000:function:worker")).toBe("worker");
    expect(shortARN("arn:aws:s3:::bucket/key:with:colons")).toBe("bucket/key:with:colons");
    expect(shortARN("arn:aws:events:us-east-1:000000000000:rule/bus/r1")).toBe("rule/bus/r1");
    expect(shortARN("not an arn")).toBe("not an arn");
  });
});
