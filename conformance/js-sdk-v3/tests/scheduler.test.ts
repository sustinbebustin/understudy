// EventBridge Scheduler over restJson1 through @aws-sdk/client-scheduler:
// the tracer bullet, a rate() schedule with an SQS target read back with
// the defaults AWS fills, invoked on the emulator clock, then deleted.
// Expected behavior: docs/research/scheduler.md.

import { afterEach, describe, expect, test } from "bun:test";
import { CreateRoleCommand, DeleteRoleCommand, IAMClient } from "@aws-sdk/client-iam";
import {
  CreateScheduleCommand,
  type CreateScheduleCommandInput,
  DeleteScheduleCommand,
  GetScheduleCommand,
  paginateListSchedules,
  SchedulerClient,
  type Target,
  UpdateScheduleCommand,
} from "@aws-sdk/client-scheduler";
import {
  CreateQueueCommand,
  DeleteQueueCommand,
  GetQueueAttributesCommand,
  ReceiveMessageCommand,
  SQSClient,
} from "@aws-sdk/client-sqs";
import { clientConfig, endpoint } from "../support/env";
import { expectFailure, uniq } from "../support/helpers";

const scheduler = new SchedulerClient(clientConfig());
const sqs = new SQSClient(clientConfig());
const iam = new IAMClient(clientConfig());

const schedulerTrust = JSON.stringify({
  Version: "2012-10-17",
  Statement: [{ Effect: "Allow", Principal: { Service: "scheduler.amazonaws.com" }, Action: "sts:AssumeRole" }],
});

const cleanups: Array<() => Promise<unknown>> = [];
afterEach(async () => {
  while (cleanups.length > 0) {
    await cleanups.pop()?.().catch(() => undefined);
  }
});

async function admin(method: string, path: string, body?: unknown): Promise<unknown> {
  const resp = await fetch(`${endpoint}/_understudy/v1${path}`, {
    method,
    headers: body === undefined ? {} : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await resp.text();
  if (!resp.ok) throw new Error(`${method} ${path} = ${resp.status}: ${text}`);
  return text === "" ? {} : JSON.parse(text);
}

/** Moves emulator time forward and settles; the clock is put back after the test. */
async function advance(seconds: number): Promise<void> {
  cleanups.push(() => admin("POST", "/reset", { services: ["sts"], clock: true }));
  await admin("POST", "/clock", { op: "advance", by: `${seconds}s` });
  const out = (await admin("GET", "/settle?timeout=30s")) as { settled?: boolean };
  if (!out.settled) throw new Error("the emulator did not settle within 30s");
}

/** A fresh queue's URL and a target naming it with a role Scheduler may assume. */
async function queueTarget(): Promise<{ queueUrl: string; target: Target }> {
  const queueUrl = (await sqs.send(new CreateQueueCommand({ QueueName: uniq("sch-q") }))).QueueUrl ?? "";
  cleanups.push(() => sqs.send(new DeleteQueueCommand({ QueueUrl: queueUrl })));
  const queueArn =
    (await sqs.send(new GetQueueAttributesCommand({ QueueUrl: queueUrl, AttributeNames: ["QueueArn"] }))).Attributes
      ?.QueueArn ?? "";
  const roleName = uniq("sch-role");
  const role = (await iam.send(new CreateRoleCommand({ RoleName: roleName, AssumeRolePolicyDocument: schedulerTrust })))
    .Role?.Arn;
  cleanups.push(() => iam.send(new DeleteRoleCommand({ RoleName: roleName })));
  return { queueUrl, target: { Arn: queueArn, RoleArn: role } };
}

/** Creates a schedule, deleted after the test. */
async function createSchedule(input: CreateScheduleCommandInput): Promise<string> {
  const out = await scheduler.send(new CreateScheduleCommand(input));
  cleanups.push(() => scheduler.send(new DeleteScheduleCommand({ Name: input.Name })));
  return out.ScheduleArn ?? "";
}

/** Emulator time from GET /clock, to the second. */
async function emulatorNow(): Promise<Date> {
  const { now } = (await admin("GET", "/clock")) as { now: string };
  const t = new Date(now);
  t.setUTCMilliseconds(0);
  return t;
}

describe("EventBridge Scheduler", () => {
  test("a rate schedule reads back AWS's defaults, delivers to SQS within the hour and deletes", async () => {
    const queueUrl = (await sqs.send(new CreateQueueCommand({ QueueName: uniq("sch-q") }))).QueueUrl ?? "";
    cleanups.push(() => sqs.send(new DeleteQueueCommand({ QueueUrl: queueUrl })));
    const queueArn =
      (await sqs.send(new GetQueueAttributesCommand({ QueueUrl: queueUrl, AttributeNames: ["QueueArn"] }))).Attributes
        ?.QueueArn ?? "";
    const roleName = uniq("sch-role");
    const role = (await iam.send(new CreateRoleCommand({ RoleName: roleName, AssumeRolePolicyDocument: schedulerTrust })))
      .Role?.Arn;
    cleanups.push(() => iam.send(new DeleteRoleCommand({ RoleName: roleName })));
    const name = uniq("sch");

    const created = await scheduler.send(
      new CreateScheduleCommand({
        Name: name,
        ScheduleExpression: "rate(1 hour)",
        FlexibleTimeWindow: { Mode: "OFF" },
        Target: { Arn: queueArn, RoleArn: role, Input: "hello <aws.scheduler.attempt-number>" },
      }),
    );
    cleanups.push(() => scheduler.send(new DeleteScheduleCommand({ Name: name })));
    expect(created.ScheduleArn).toEndWith(`:schedule/default/${name}`);

    // Filled although never sent (tf-acc schedule_test.go:L199-L223; scheduler.md 4.2).
    const got = await scheduler.send(new GetScheduleCommand({ Name: name }));
    expect(got).toMatchObject({
      GroupName: "default",
      ActionAfterCompletion: "NONE",
      Target: { RetryPolicy: { MaximumEventAgeInSeconds: 86400, MaximumRetryAttempts: 185 } },
    });

    // <aws.scheduler.attempt-number> is 1 on a first attempt (aws-doc
    // managing-schedule-context-attributes; scheduler.md 6.3).
    await advance(3600);
    const received = await sqs.send(new ReceiveMessageCommand({ QueueUrl: queueUrl, MaxNumberOfMessages: 10 }));
    const bodies = (received.Messages ?? []).map((m) => m.Body);
    expect(bodies.length).toBeGreaterThan(0);
    expect(new Set(bodies)).toEqual(new Set(["hello 1"]));

    await scheduler.send(new DeleteScheduleCommand({ Name: name }));
    await expectFailure(scheduler.send(new GetScheduleCommand({ Name: name })), {
      name: "ResourceNotFoundException",
      status: 404,
    });
  });

  // A rate schedule first fires at its StartDate (aws-doc schedule-types;
  // scheduler.md 5.2), its body carrying the schedule ARN, the scheduled
  // time, a 16-hex execution id and attempt 1 (aws-doc
  // managing-schedule-context-attributes, configuring-schedule-dlq;
  // scheduler.md 6.3, 7.2); the zone, State and StartDate read back as sent
  // (tf-acc schedule_test.go:L365-L419, L1870-L1895; scheduler.md 4.2).
  test("a StartDate fire carries the substituted context attributes", async () => {
    const { queueUrl, target } = await queueTarget();
    const start = new Date((await emulatorNow()).getTime() + 10 * 60_000);
    const name = uniq("sch");
    const arn = await createSchedule({
      Name: name,
      ScheduleExpression: "rate(1 hour)",
      ScheduleExpressionTimezone: "Europe/Paris",
      State: "ENABLED",
      StartDate: start,
      FlexibleTimeWindow: { Mode: "OFF" },
      Target: {
        ...target,
        Input:
          "<aws.scheduler.schedule-arn>|<aws.scheduler.scheduled-time>|<aws.scheduler.execution-id>|<aws.scheduler.attempt-number>",
      },
    });
    const got = await scheduler.send(new GetScheduleCommand({ Name: name }));
    expect(got).toMatchObject({ ScheduleExpressionTimezone: "Europe/Paris", State: "ENABLED", StartDate: start });

    await advance(660);
    const received = await sqs.send(new ReceiveMessageCommand({ QueueUrl: queueUrl, MaxNumberOfMessages: 10 }));
    const bodies = (received.Messages ?? []).map((m) => m.Body ?? "");
    expect(bodies.length).toBe(1);
    const [scheduleArn, scheduled, execution, attempt] = (bodies[0] ?? "").split("|");
    expect(scheduleArn).toBe(arn);
    expect(scheduled).toBe(start.toISOString().replace(".000Z", "Z"));
    expect(execution).toMatch(/^[0-9a-f]{16}$/);
    expect(attempt).toBe("1");
  });

  // UpdateSchedule replaces the schedule: members left out read back their
  // defaults (aws-doc UpdateSchedule; tf-acc schedule_test.go:L409, L776,
  // L1338; scheduler.md 4.3).
  test("an update that drops members reads their defaults back", async () => {
    const { target } = await queueTarget();
    const name = uniq("sch");
    await createSchedule({
      Name: name,
      ScheduleExpression: "rate(1 hour)",
      Description: "before",
      StartDate: new Date("2099-01-01T01:00:00Z"),
      EndDate: new Date("2100-01-01T01:02:03Z"),
      FlexibleTimeWindow: { Mode: "OFF" },
      Target: { ...target, Input: "x", RetryPolicy: { MaximumEventAgeInSeconds: 60, MaximumRetryAttempts: 1 } },
    });
    await scheduler.send(
      new UpdateScheduleCommand({
        Name: name,
        ScheduleExpression: "rate(1 hour)",
        FlexibleTimeWindow: { Mode: "OFF" },
        Target: target,
      }),
    );
    const got = await scheduler.send(new GetScheduleCommand({ Name: name }));
    expect(got.Description ?? "").toBe("");
    expect(got.StartDate).toBeUndefined();
    expect(got.EndDate).toBeUndefined();
    expect(got.Target?.Input).toBeUndefined();
    expect(got.Target?.RetryPolicy).toEqual({ MaximumEventAgeInSeconds: 86400, MaximumRetryAttempts: 185 });
  });

  // ListSchedules returns a Schedules list (localstack@8b9a79f
  // test_scheduler.py:L13-L20, validated 2024-06-11); NamePrefix filters,
  // and following NextToken with MaxResults 1 yields every match once
  // (scheduler.md 4.5; the order is unrecorded, so the union is compared).
  test("ListSchedules by prefix yields every match across pages", async () => {
    const { target } = await queueTarget();
    const prefix = uniq("ls");
    const names = [0, 1, 2].map((i) => `${prefix}-${i}`);
    for (const n of names) {
      await createSchedule({ Name: n, ScheduleExpression: "rate(1 hour)", FlexibleTimeWindow: { Mode: "OFF" }, Target: target });
    }
    const seen: string[] = [];
    for await (const page of paginateListSchedules({ client: scheduler, pageSize: 1 }, { NamePrefix: prefix })) {
      expect(Array.isArray(page.Schedules)).toBe(true);
      seen.push(...(page.Schedules ?? []).map((s) => s.Name ?? ""));
    }
    expect(seen.sort()).toEqual(names);
  });

  // rate(10 seconds) is refused with "Invalid Schedule Expression <expr>."
  // (G3: localstack@8b9a79f test_scheduler.snapshot.json:L127-L270,
  // validated 2025-01-26); a past at() is accepted (G4: snapshot
  // L303-L314, validated 2025-02-02; scheduler.md 5.1).
  test("a recorded invalid expression is refused and a past at() accepted", async () => {
    const { target } = await queueTarget();
    await expectFailure(
      scheduler.send(
        new CreateScheduleCommand({
          Name: uniq("sch"),
          ScheduleExpression: "rate(10 seconds)",
          FlexibleTimeWindow: { Mode: "OFF" },
          Target: target,
        }),
      ),
      { name: "ValidationException", status: 400, messageIncludes: "Invalid Schedule Expression rate(10 seconds)." },
    );
    const name = uniq("sch");
    const arn = await createSchedule({
      Name: name,
      ScheduleExpression: "at(2022-12-31T23:59:59)",
      FlexibleTimeWindow: { Mode: "OFF" },
      Target: target,
    });
    expect(arn).toEndWith(`:schedule/default/${name}`);
  });
});
