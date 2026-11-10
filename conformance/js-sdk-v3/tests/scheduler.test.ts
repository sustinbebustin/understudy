// EventBridge Scheduler over restJson1 through @aws-sdk/client-scheduler:
// the tracer bullet, a rate() schedule with an SQS target read back with
// the defaults AWS fills, invoked on the emulator clock, then deleted.
// Expected behavior: docs/research/scheduler.md.

import { afterEach, describe, expect, test } from "bun:test";
import { CreateRoleCommand, DeleteRoleCommand, IAMClient } from "@aws-sdk/client-iam";
import {
  CreateScheduleCommand,
  type CreateScheduleCommandInput,
  CreateScheduleGroupCommand,
  DeleteScheduleCommand,
  DeleteScheduleGroupCommand,
  GetScheduleCommand,
  GetScheduleGroupCommand,
  ListTagsForResourceCommand,
  paginateListSchedules,
  SchedulerClient,
  type Target,
  UntagResourceCommand,
  UpdateScheduleCommand,
} from "@aws-sdk/client-scheduler";
import {
  CreateStateMachineCommand,
  DeleteStateMachineCommand,
  DescribeExecutionCommand,
  ListExecutionsCommand,
  SFNClient,
} from "@aws-sdk/client-sfn";
import {
  CreateQueueCommand,
  DeleteQueueCommand,
  GetQueueAttributesCommand,
  ReceiveMessageCommand,
  SQSClient,
} from "@aws-sdk/client-sqs";
import { CreateFunctionCommand, DeleteFunctionCommand, LambdaClient, waitUntilFunctionActiveV2 } from "@aws-sdk/client-lambda";
import { clientConfig, endpoint, lambdaExecutionEnabled } from "../support/env";
import { expectFailure, uniq } from "../support/helpers";
import { zip } from "../support/zip";

const scheduler = new SchedulerClient(clientConfig());
const sqs = new SQSClient(clientConfig());
const iam = new IAMClient(clientConfig());
const lambda = new LambdaClient(clientConfig());
const sfn = new SFNClient(clientConfig());

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
  // time, a lower-case UUID execution id and attempt 1 (aws-doc
  // managing-schedule-context-attributes; scheduler.md 6.3; the UUID form is
  // testdata/parity/scheduler/SCH/07-sqs-context.json, aws-recorded); the
  // zone, State and StartDate read back as sent
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
    expect(execution).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
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

  // A universal (aws-sdk) target answers 501 NotImplemented and nothing is
  // stored (CLAUDE.md invariant 3).
  test("a universal target answers NotImplemented", async () => {
    const { target } = await queueTarget();
    const name = uniq("sch");
    await expectFailure(
      scheduler.send(
        new CreateScheduleCommand({
          Name: name,
          ScheduleExpression: "rate(1 hour)",
          FlexibleTimeWindow: { Mode: "OFF" },
          Target: { ...target, Arn: "arn:aws:scheduler:::aws-sdk:sqs:sendMessage", Input: "{}" },
        }),
      ),
      { name: "NotImplemented", status: 501 },
    );
    await expectFailure(scheduler.send(new GetScheduleCommand({ Name: name })), {
      name: "ResourceNotFoundException",
      status: 404,
    });
  });

  // A schedule whose target queue was deleted, with MaximumRetryAttempts 0,
  // sends the SQS request it built ({"MessageBody": <Input>, "QueueUrl": ...})
  // to the dead-letter queue with nine String attributes, ERROR_CODE
  // AWS.SimpleQueueService.NonExistentQueue and TARGET_ARN the queue as
  // configured (scheduler.md 7.2; testdata/parity/scheduler/SCH/08-gone.json,
  // aws-recorded). RETRY_ATTEMPTS and
  // EXHAUSTED_RETRY_CONDITION depend on scheduler.retry.permanent-errors and
  // are asserted in Go tests; DeadLetterConfig reads back as sent (4.2).
  test("a deleted target queue dead-letters with the documented attributes", async () => {
    const { queueUrl: dlqUrl, target } = await queueTarget();
    const dlqArn = target.Arn ?? "";
    const goneUrl = (await sqs.send(new CreateQueueCommand({ QueueName: uniq("sch-gone") }))).QueueUrl ?? "";
    const goneArn =
      (await sqs.send(new GetQueueAttributesCommand({ QueueUrl: goneUrl, AttributeNames: ["QueueArn"] }))).Attributes
        ?.QueueArn ?? "";
    await sqs.send(new DeleteQueueCommand({ QueueUrl: goneUrl }));
    const at = new Date((await emulatorNow()).getTime() + 10 * 60_000);
    const atText = at.toISOString().replace(".000Z", "");
    const name = uniq("sch");
    const arn = await createSchedule({
      Name: name,
      ScheduleExpression: `at(${atText})`,
      FlexibleTimeWindow: { Mode: "OFF" },
      Target: {
        ...target,
        Arn: goneArn,
        Input: "Hello, world!",
        RetryPolicy: { MaximumRetryAttempts: 0 },
        DeadLetterConfig: { Arn: dlqArn },
      },
    });
    const got = await scheduler.send(new GetScheduleCommand({ Name: name }));
    expect(got.Target?.DeadLetterConfig).toEqual({ Arn: dlqArn });

    await advance(660);
    const received = await sqs.send(
      new ReceiveMessageCommand({ QueueUrl: dlqUrl, MaxNumberOfMessages: 10, MessageAttributeNames: ["All"] }),
    );
    const messages = received.Messages ?? [];
    expect(messages.length).toBe(1);
    const attrs = messages[0]?.MessageAttributes ?? {};
    const [, , , goneRegion, goneAccount, goneQueue] = goneArn.split(":");
    expect(JSON.parse(messages[0]?.Body ?? "null")).toEqual({
      MessageBody: "Hello, world!",
      QueueUrl: `https://sqs.${goneRegion}.amazonaws.com/${goneAccount}/${goneQueue}`,
    });
    for (const n of [
      "CONTAINS_INVALID_PAYLOAD",
      "ERROR_CODE",
      "ERROR_MESSAGE",
      "EXECUTION_ID",
      "IS_PAYLOAD_TRUNCATED",
      "RETRY_ATTEMPTS",
      "SCHEDULED_TIME",
      "SCHEDULE_ARN",
      "TARGET_ARN",
    ]) {
      expect(attrs[n]?.DataType).toBe("String");
    }
    expect(attrs.ERROR_CODE?.StringValue).toBe("AWS.SimpleQueueService.NonExistentQueue");
    expect(attrs.TARGET_ARN?.StringValue).toBe(goneArn);
    expect(attrs.SCHEDULE_ARN?.StringValue).toBe(arn);
    expect(attrs.SCHEDULED_TIME?.StringValue).toBe(`${atText}Z`);
  });

  // A one-time at() with ActionAfterCompletion DELETE reads DELETE back,
  // invokes its target once, and is gone after it has (aws-doc
  // managing-schedule-delete; scheduler.md 8).
  test("an at() with DELETE is gone after it fires", async () => {
    const { queueUrl, target } = await queueTarget();
    const at = new Date((await emulatorNow()).getTime() + 10 * 60_000);
    const name = uniq("sch");
    await createSchedule({
      Name: name,
      ScheduleExpression: `at(${at.toISOString().slice(0, 19)})`,
      ActionAfterCompletion: "DELETE",
      FlexibleTimeWindow: { Mode: "OFF" },
      Target: { ...target, Input: "once" },
    });
    const got = await scheduler.send(new GetScheduleCommand({ Name: name }));
    expect(got.ActionAfterCompletion).toBe("DELETE");

    await advance(660);
    const received = await sqs.send(new ReceiveMessageCommand({ QueueUrl: queueUrl, MaxNumberOfMessages: 10 }));
    expect((received.Messages ?? []).map((m) => m.Body)).toEqual(["once"]);
    await expectFailure(scheduler.send(new GetScheduleCommand({ Name: name })), {
      name: "ResourceNotFoundException",
      status: 404,
    });
  });

  // A schedule group created with a tag reads ACTIVE with its ARN (tf-acc
  // schedule_group_test.go:L25-L72), lists its tag, and untagged lists
  // "Tags": [] (G2: localstack@8b9a79f test_scheduler.snapshot.json:L19-L30);
  // deleting it deletes its schedules, after which the group and the
  // schedule answer 404 (aws-doc managing-schedule-group-delete;
  // scheduler.md 3.2-3.4).
  test("a tagged group with members is deleted with them", async () => {
    const group = uniq("sch-g");
    const created = await scheduler.send(
      new CreateScheduleGroupCommand({ Name: group, Tags: [{ Key: "TagKey", Value: "TagValue" }] }),
    );
    cleanups.push(() => scheduler.send(new DeleteScheduleGroupCommand({ Name: group })));
    const arn = created.ScheduleGroupArn ?? "";
    expect(arn).toMatch(new RegExp(`^arn:aws:scheduler:[a-z0-9-]+:\\d{12}:schedule-group/${group}$`));
    const got = await scheduler.send(new GetScheduleGroupCommand({ Name: group }));
    expect([got.Arn, got.Name, got.State]).toEqual([arn, group, "ACTIVE"]);
    expect((await scheduler.send(new ListTagsForResourceCommand({ ResourceArn: arn }))).Tags).toEqual([
      { Key: "TagKey", Value: "TagValue" },
    ]);
    await scheduler.send(new UntagResourceCommand({ ResourceArn: arn, TagKeys: ["TagKey"] }));
    expect((await scheduler.send(new ListTagsForResourceCommand({ ResourceArn: arn }))).Tags).toEqual([]);

    const { target } = await queueTarget();
    await scheduler.send(
      new CreateScheduleCommand({
        Name: "member",
        GroupName: group,
        ScheduleExpression: "rate(1 hour)",
        FlexibleTimeWindow: { Mode: "OFF" },
        Target: target,
      }),
    );
    await scheduler.send(new DeleteScheduleGroupCommand({ Name: group }));
    await expectFailure(scheduler.send(new GetScheduleGroupCommand({ Name: group })), {
      name: "ResourceNotFoundException",
      status: 404,
    });
    await expectFailure(scheduler.send(new GetScheduleCommand({ Name: "member", GroupName: group })), {
      name: "ResourceNotFoundException",
      status: 404,
    });
  });

  // A Step Functions target starts one execution of the state machine with
  // the Input as its input, context attributes substituted (aws-doc
  // managing-targets-templated, managing-schedule-context-attributes;
  // scheduler.md 6.3, 6.4).
  test("a Step Functions target starts an execution with the Input", async () => {
    const { target } = await queueTarget();
    const statesRoleName = uniq("sfn-role");
    const statesRole = (
      await iam.send(new CreateRoleCommand({ RoleName: statesRoleName, AssumeRolePolicyDocument: statesTrust }))
    ).Role?.Arn;
    cleanups.push(() => iam.send(new DeleteRoleCommand({ RoleName: statesRoleName })));
    const machine =
      (
        await sfn.send(
          new CreateStateMachineCommand({
            name: uniq("sch-flow"),
            definition: JSON.stringify({ StartAt: "P", States: { P: { Type: "Pass", End: true } } }),
            roleArn: statesRole,
          }),
        )
      ).stateMachineArn ?? "";
    cleanups.push(() => sfn.send(new DeleteStateMachineCommand({ stateMachineArn: machine })));
    const at = (await emulatorNow()).getTime() + 10 * 60 * 1000;
    await createSchedule({
      Name: uniq("sch"),
      ScheduleExpression: `at(${new Date(at).toISOString().slice(0, 19)})`,
      FlexibleTimeWindow: { Mode: "OFF" },
      Target: { Arn: machine, RoleArn: target.RoleArn, Input: '{"attempt":"<aws.scheduler.attempt-number>"}' },
    });
    await advance(660);

    const executions = (await sfn.send(new ListExecutionsCommand({ stateMachineArn: machine }))).executions ?? [];
    expect(executions.length).toBe(1);
    const described = await sfn.send(new DescribeExecutionCommand({ executionArn: executions[0]?.executionArn }));
    expect(JSON.parse(described.input ?? "")).toEqual({ attempt: "1" });
  });
});

const statesTrust = JSON.stringify({
  Version: "2012-10-17",
  Statement: [{ Effect: "Allow", Principal: { Service: "states.amazonaws.com" }, Action: "sts:AssumeRole" }],
});

const FORWARD_HANDLER = `
import json
import os

import boto3


def handler(event, context):
    boto3.client("sqs").send_message(QueueUrl=os.environ["FORWARD_QUEUE_URL"], MessageBody=json.dumps(event))
`;

const TRUST_LAMBDA = JSON.stringify({
  Version: "2012-10-17",
  Statement: [{ Effect: "Allow", Principal: { Service: "lambda.amazonaws.com" }, Action: "sts:AssumeRole" }],
});

describe.skipIf(!lambdaExecutionEnabled)("EventBridge Scheduler Lambda target", () => {
  // A Lambda target is invoked asynchronously with the Input as its event,
  // context attributes substituted ("EventBridge Scheduler invokes your
  // Lambda Event Function asynchronously", aws-doc with-eventbridge-scheduler;
  // aws-doc managing-schedule-context-attributes; scheduler.md 6.3, 6.4); the
  // function forwards the event it received to a queue.
  test("a Lambda target receives the Input with the attempt number", async () => {
    const { queueUrl: forward, target } = await queueTarget();
    const fnRole = uniq("sch-fn-role");
    const fnRoleArn = (await iam.send(new CreateRoleCommand({ RoleName: fnRole, AssumeRolePolicyDocument: TRUST_LAMBDA })))
      .Role?.Arn;
    cleanups.push(() => iam.send(new DeleteRoleCommand({ RoleName: fnRole })));
    const fn = uniq("sch-fn");
    const created = await lambda.send(
      new CreateFunctionCommand({
        FunctionName: fn,
        Runtime: "python3.13",
        Role: fnRoleArn,
        Handler: "lambda_function.handler",
        Code: { ZipFile: zip({ "lambda_function.py": FORWARD_HANDLER }) },
        Environment: { Variables: { FORWARD_QUEUE_URL: forward } },
      }),
    );
    cleanups.push(() => lambda.send(new DeleteFunctionCommand({ FunctionName: fn })));
    await waitUntilFunctionActiveV2({ client: lambda, maxWaitTime: 120, minDelay: 1, maxDelay: 2 }, { FunctionName: fn });

    const at = (await emulatorNow()).getTime() + 10 * 60 * 1000;
    await createSchedule({
      Name: uniq("sch"),
      ScheduleExpression: `at(${new Date(at).toISOString().slice(0, 19)})`,
      FlexibleTimeWindow: { Mode: "OFF" },
      Target: { Arn: created.FunctionArn, RoleArn: target.RoleArn, Input: '{"attempt":"<aws.scheduler.attempt-number>"}' },
    });
    await advance(660);

    const deadline = Date.now() + 90_000;
    let bodies: string[] = [];
    while (bodies.length === 0 && Date.now() < deadline) {
      const out = await sqs.send(new ReceiveMessageCommand({ QueueUrl: forward, MaxNumberOfMessages: 10, WaitTimeSeconds: 5 }));
      bodies = (out.Messages ?? []).map((m) => m.Body ?? "");
    }
    expect(bodies.map((b) => JSON.parse(b))).toEqual([{ attempt: "1" }]);
  }, 180_000);
});
