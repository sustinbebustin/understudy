// EventBridge Scheduler over restJson1 through @aws-sdk/client-scheduler:
// the tracer bullet, a rate() schedule with an SQS target read back with
// the defaults AWS fills, invoked on the emulator clock, then deleted.
// Expected behavior: docs/research/scheduler.md.

import { afterEach, describe, expect, test } from "bun:test";
import { CreateRoleCommand, DeleteRoleCommand, IAMClient } from "@aws-sdk/client-iam";
import {
  CreateScheduleCommand,
  DeleteScheduleCommand,
  GetScheduleCommand,
  SchedulerClient,
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
});
