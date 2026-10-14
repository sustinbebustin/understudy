// EventBridge over awsJson1_1, through @aws-sdk/client-eventbridge, with SQS
// targets read back through @aws-sdk/client-sqs.
// Expected behavior: docs/research/secrets-logs-events-alarms.md section 4.

import { describe, expect, test } from "bun:test";
import {
  DeleteRuleCommand,
  DescribeRuleCommand,
  DisableRuleCommand,
  EnableRuleCommand,
  EventBridgeClient,
  ListRulesCommand,
  ListTagsForResourceCommand,
  ListTargetsByRuleCommand,
  PutEventsCommand,
  PutRuleCommand,
  PutTargetsCommand,
  RemoveTargetsCommand,
  TagResourceCommand,
  TestEventPatternCommand,
  UntagResourceCommand,
} from "@aws-sdk/client-eventbridge";
import { CreateQueueCommand, DeleteMessageBatchCommand, ReceiveMessageCommand, SQSClient, type Message } from "@aws-sdk/client-sqs";
import { CreateTopicCommand, SNSClient, SubscribeCommand } from "@aws-sdk/client-sns";
import { clientConfig, defaultAccount, region } from "../support/env";
import { awsFailure, expectFailure, uniq } from "../support/helpers";

const events = new EventBridgeClient(clientConfig());
const sqs = new SQSClient(clientConfig());
const sns = new SNSClient(clientConfig());

const ruleName = () => uniq("rule");
const ruleArn = (name: string) => `arn:aws:events:${region}:${defaultAccount}:rule/${name}`;

async function makeQueue(): Promise<{ url: string; arn: string }> {
  const name = uniq("evq");
  const out = await sqs.send(new CreateQueueCommand({ QueueName: name }));
  return { url: out.QueueUrl ?? "", arn: `arn:aws:sqs:${region}:${defaultAccount}:${name}` };
}

async function drain(queueUrl: string, want: number, timeoutMs = 10_000): Promise<Message[]> {
  const got: Message[] = [];
  const deadline = Date.now() + timeoutMs;
  while (got.length < want && Date.now() < deadline) {
    const out = await sqs.send(
      new ReceiveMessageCommand({ QueueUrl: queueUrl, MaxNumberOfMessages: 10, WaitTimeSeconds: 1, MessageSystemAttributeNames: ["All"] }),
    );
    const messages = out.Messages ?? [];
    if (messages.length > 0) {
      await sqs.send(new DeleteMessageBatchCommand({ QueueUrl: queueUrl, Entries: messages.map((m, i) => ({ Id: `m${i}`, ReceiptHandle: m.ReceiptHandle })) }));
    }
    got.push(...messages);
  }
  return got;
}

/** A rule with the pattern, an SQS target and the queue. */
async function patternRule(pattern: unknown, target: Partial<ConstructorParameters<typeof PutTargetsCommand>[0]["Targets"] extends (infer T)[] | undefined ? T : never> = {}) {
  const name = ruleName();
  const queue = await makeQueue();
  await events.send(new PutRuleCommand({ Name: name, EventPattern: JSON.stringify(pattern) }));
  const result = await events.send(new PutTargetsCommand({ Rule: name, Targets: [{ Id: "t1", Arn: queue.arn, ...target }] }));
  expect(result.FailedEntryCount).toBe(0);
  return { name, queue };
}

const entry = (overrides: Record<string, unknown> = {}) => ({
  Source: "conformance.app",
  DetailType: "Order Placed",
  Detail: JSON.stringify({ orderId: "o-1", state: "paid", amount: 25 }),
  ...overrides,
});

describe("EventBridge rules", () => {
  test("PutRule returns the rule ARN and DescribeRule returns the documented shape", async () => {
    const name = ruleName();
    const pattern = JSON.stringify({ source: ["a.b"] });
    const put = await events.send(new PutRuleCommand({ Name: name, EventPattern: pattern }));
    expect(put.RuleArn).toBe(ruleArn(name));
    const described = await events.send(new DescribeRuleCommand({ Name: name }));
    expect(described).toMatchObject({
      Name: name,
      Arn: ruleArn(name),
      EventBusName: "default",
      State: "ENABLED",
      CreatedBy: defaultAccount,
    });
    expect(JSON.parse(described.EventPattern ?? "null")).toEqual({ source: ["a.b"] });
    expect(described.ScheduleExpression).toBeUndefined();
    expect(described.Description).toBeUndefined();
  });

  test("a schedule rule describes with its ScheduleExpression and no EventPattern", async () => {
    const name = ruleName();
    await events.send(new PutRuleCommand({ Name: name, ScheduleExpression: "rate(5 minutes)", Description: "every five", State: "DISABLED" }));
    const described = await events.send(new DescribeRuleCommand({ Name: name }));
    expect(described).toMatchObject({ ScheduleExpression: "rate(5 minutes)", Description: "every five", State: "DISABLED" });
    expect(described.EventPattern).toBeUndefined();
  });

  test("PutRule on an existing rule replaces it and omitted arguments become unset", async () => {
    const name = ruleName();
    await events.send(new PutRuleCommand({ Name: name, ScheduleExpression: "rate(5 minutes)", Description: "first" }));
    await events.send(new PutRuleCommand({ Name: name, EventPattern: JSON.stringify({ source: ["x"] }) }));
    const described = await events.send(new DescribeRuleCommand({ Name: name }));
    expect(described.ScheduleExpression).toBeUndefined();
    expect(described.Description).toBeUndefined();
    expect(described.EventPattern).toBeDefined();
  });

  test("DisableRule and EnableRule flip State", async () => {
    const name = ruleName();
    await events.send(new PutRuleCommand({ Name: name, EventPattern: JSON.stringify({ source: ["x"] }) }));
    await events.send(new DisableRuleCommand({ Name: name }));
    expect((await events.send(new DescribeRuleCommand({ Name: name }))).State).toBe("DISABLED");
    await events.send(new EnableRuleCommand({ Name: name }));
    expect((await events.send(new DescribeRuleCommand({ Name: name }))).State).toBe("ENABLED");
  });

  test("DescribeRule on a missing rule fails ResourceNotFoundException", async () => {
    const name = ruleName();
    await expectFailure(events.send(new DescribeRuleCommand({ Name: name })), {
      name: "ResourceNotFoundException",
      status: 400,
      messageIncludes: `Rule ${name} does not exist on EventBus default.`,
    });
  });

  test("ListRules filters by name prefix, omits CreatedBy and pages with Limit and NextToken", async () => {
    const prefix = uniq("lr");
    for (const suffix of ["a", "b", "c"]) await events.send(new PutRuleCommand({ Name: `${prefix}-${suffix}`, EventPattern: JSON.stringify({ source: ["x"] }) }));
    const first = await events.send(new ListRulesCommand({ NamePrefix: prefix, Limit: 2 }));
    expect(first.Rules?.map((r) => r.Name)).toEqual([`${prefix}-a`, `${prefix}-b`]);
    expect(first.NextToken).toBeDefined();
    expect(first.Rules?.[0]).toMatchObject({ Arn: ruleArn(`${prefix}-a`), EventBusName: "default", State: "ENABLED" });
    const second = await events.send(new ListRulesCommand({ NamePrefix: prefix, Limit: 2, NextToken: first.NextToken }));
    expect(second.Rules?.map((r) => r.Name)).toEqual([`${prefix}-c`]);
    expect(second.NextToken).toBeUndefined();
  });

  test("DeleteRule removes the rule, and a rule with targets cannot be deleted", async () => {
    const name = ruleName();
    const queue = await makeQueue();
    await events.send(new PutRuleCommand({ Name: name, EventPattern: JSON.stringify({ source: ["x"] }) }));
    await events.send(new PutTargetsCommand({ Rule: name, Targets: [{ Id: "t", Arn: queue.arn }] }));
    await expectFailure(events.send(new DeleteRuleCommand({ Name: name })), {
      name: "ValidationException",
      status: 400,
      messageIncludes: "Rule can't be deleted since it has targets.",
    });
    await events.send(new RemoveTargetsCommand({ Rule: name, Ids: ["t"] }));
    await events.send(new DeleteRuleCommand({ Name: name }));
    await expectFailure(events.send(new DescribeRuleCommand({ Name: name })), { name: "ResourceNotFoundException", status: 400 });
  });

  test("PutRule without a pattern or schedule is refused", async () => {
    const failure = await awsFailure(events.send(new PutRuleCommand({ Name: ruleName() })));
    expect(failure.status).toBe(400);
  });

  test("a rule name outside the allowed characters fails ValidationException", async () => {
    await expectFailure(events.send(new PutRuleCommand({ Name: "bad name!", EventPattern: JSON.stringify({ source: ["x"] }) })), {
      name: "ValidationException",
      status: 400,
      messageIncludes: "at 'name' failed to satisfy constraint: Member must satisfy regular expression pattern: [\\.\\-_A-Za-z0-9]+",
    });
  });

  test("tags round trip through TagResource, ListTagsForResource and UntagResource", async () => {
    const name = ruleName();
    await events.send(new PutRuleCommand({ Name: name, EventPattern: JSON.stringify({ source: ["x"] }), Tags: [{ Key: "env", Value: "test" }] }));
    const arn = ruleArn(name);
    const tags = async () => Object.fromEntries(((await events.send(new ListTagsForResourceCommand({ ResourceARN: arn }))).Tags ?? []).map((t) => [t.Key, t.Value]));
    expect(await tags()).toEqual({ env: "test" });
    await events.send(new TagResourceCommand({ ResourceARN: arn, Tags: [{ Key: "team", Value: "a" }] }));
    expect(await tags()).toEqual({ env: "test", team: "a" });
    await events.send(new UntagResourceCommand({ ResourceARN: arn, TagKeys: ["env"] }));
    expect(await tags()).toEqual({ team: "a" });
  });

  test("tagging a rule that does not exist fails ResourceNotFoundException", async () => {
    const name = ruleName();
    await expectFailure(events.send(new TagResourceCommand({ ResourceARN: ruleArn(name), Tags: [{ Key: "a", Value: "b" }] })), {
      name: "ResourceNotFoundException",
      status: 400,
      messageIncludes: `Rule ${name} does not exist on EventBus default.`,
    });
  });
});

describe("EventBridge schedule expressions", () => {
  const valid = [
    "rate(1 minute)",
    "rate(5 minutes)",
    "rate(1 hour)",
    "rate(12 hours)",
    "rate(1 day)",
    "rate(7 days)",
    "cron(0 10 * * ? *)",
    "cron(15 12 * * ? *)",
    "cron(0 18 ? * MON-FRI *)",
    "cron(0 8 1 * ? *)",
    "cron(0/15 * * * ? *)",
    "cron(0/5 8-17 ? * MON-FRI *)",
    "cron(5,35 14 * * ? *)",
    "cron(15 10 ? * 6L 2002-2005)",
    "cron(0 2 ? * SAT#3 *)",
  ];
  for (const expression of valid) {
    test(`${expression} is accepted`, async () => {
      const out = await events.send(new PutRuleCommand({ Name: ruleName(), ScheduleExpression: expression, State: "DISABLED" }));
      expect(out.RuleArn).toBeDefined();
    });
  }

  const invalid = [
    "rate(10 seconds)",
    "rate(10 years)",
    "rate(1 minutes)",
    "rate(1 hours)",
    "rate(1 days)",
    "rate(10 minute)",
    "rate()",
    "rate(10)",
    "rate(foo minutes)",
    "rate(0 minutes)",
    "rate(-10 minutes)",
    "rate(10 MINUTES)",
    "rate( 10 minutes )",
    "cron(0 1 * * * *)",
    "cron(0 dummy ? * MON-FRI *)",
    "cron(71 8 1 * ? *)",
    "cron(7 20 * * NOT *)",
    "cron(INVALID)",
  ];
  for (const expression of invalid) {
    test(`${expression} is rejected with Parameter ScheduleExpression is not valid.`, async () => {
      await expectFailure(events.send(new PutRuleCommand({ Name: ruleName(), ScheduleExpression: expression })), {
        name: "ValidationException",
        status: 400,
        messageIncludes: "Parameter ScheduleExpression is not valid.",
      });
    });
  }

  test("a schedule rule on a custom event bus is refused", async () => {
    await expectFailure(events.send(new PutRuleCommand({ Name: ruleName(), ScheduleExpression: "rate(5 minutes)", EventBusName: uniq("bus") })), {
      name: "ResourceNotFoundException",
      status: 400,
    });
  });
});

describe("EventBridge targets", () => {
  test("PutTargets then ListTargetsByRule returns the target with its Id and Arn", async () => {
    const name = ruleName();
    const queue = await makeQueue();
    await events.send(new PutRuleCommand({ Name: name, EventPattern: JSON.stringify({ source: ["x"] }) }));
    const put = await events.send(new PutTargetsCommand({ Rule: name, Targets: [{ Id: "t1", Arn: queue.arn, Input: '{"fixed":true}' }] }));
    expect(put).toMatchObject({ FailedEntryCount: 0 });
    expect(put.FailedEntries ?? []).toEqual([]);
    const listed = await events.send(new ListTargetsByRuleCommand({ Rule: name }));
    expect(listed.Targets).toEqual([{ Id: "t1", Arn: queue.arn, Input: '{"fixed":true}' }]);
  });

  test("PutTargets with an existing Id replaces that target", async () => {
    const name = ruleName();
    const a = await makeQueue();
    const b = await makeQueue();
    await events.send(new PutRuleCommand({ Name: name, EventPattern: JSON.stringify({ source: ["x"] }) }));
    await events.send(new PutTargetsCommand({ Rule: name, Targets: [{ Id: "t", Arn: a.arn }] }));
    await events.send(new PutTargetsCommand({ Rule: name, Targets: [{ Id: "t", Arn: b.arn }] }));
    expect((await events.send(new ListTargetsByRuleCommand({ Rule: name }))).Targets).toEqual([{ Id: "t", Arn: b.arn }]);
  });

  test("a sixth target on one rule fails LimitExceededException", async () => {
    const name = ruleName();
    await events.send(new PutRuleCommand({ Name: name, EventPattern: JSON.stringify({ source: ["x"] }) }));
    const targets = [];
    for (let i = 0; i < 6; i += 1) targets.push({ Id: `t${i}`, Arn: (await makeQueue()).arn });
    await expectFailure(events.send(new PutTargetsCommand({ Rule: name, Targets: targets })), {
      name: "LimitExceededException",
      status: 400,
      messageIncludes: "The requested resource exceeds the maximum number allowed.",
    });
  });

  test("Input, InputPath and InputTransformer are mutually exclusive", async () => {
    const name = ruleName();
    const queue = await makeQueue();
    await events.send(new PutRuleCommand({ Name: name, EventPattern: JSON.stringify({ source: ["x"] }) }));
    await expectFailure(events.send(new PutTargetsCommand({ Rule: name, Targets: [{ Id: "t", Arn: queue.arn, Input: "{}", InputPath: "$.detail" }] })), {
      name: "ValidationException",
      status: 400,
      messageIncludes: "Only one of Input, InputPath, or InputTransformer must be provided for target t.",
    });
  });

  test("a target Id with invalid characters fails with the targets.1.member.id path", async () => {
    const name = ruleName();
    const queue = await makeQueue();
    await events.send(new PutRuleCommand({ Name: name, EventPattern: JSON.stringify({ source: ["x"] }) }));
    await expectFailure(events.send(new PutTargetsCommand({ Rule: name, Targets: [{ Id: "!@#$@!#$", Arn: queue.arn }] })), {
      name: "ValidationException",
      status: 400,
      messageIncludes: "Value '!@#$@!#$' at 'targets.1.member.id' failed to satisfy constraint: Member must satisfy regular expression pattern: [\\.\\-_A-Za-z0-9]+",
    });
  });

  test("an InputTemplate that uses an undefined placeholder fails ValidationException", async () => {
    const name = ruleName();
    const queue = await makeQueue();
    await events.send(new PutRuleCommand({ Name: name, EventPattern: JSON.stringify({ source: ["x"] }) }));
    await expectFailure(
      events.send(new PutTargetsCommand({ Rule: name, Targets: [{ Id: "t", Arn: queue.arn, InputTransformer: { InputTemplate: '{"x": <notdefinedkey>}' } }] })),
      { name: "ValidationException", status: 400, messageIncludes: "InputTemplate for target t contains invalid placeholder notdefinedkey." },
    );
  });

  test("RemoveTargets removes the target and reports no failures", async () => {
    const name = ruleName();
    const queue = await makeQueue();
    await events.send(new PutRuleCommand({ Name: name, EventPattern: JSON.stringify({ source: ["x"] }) }));
    await events.send(new PutTargetsCommand({ Rule: name, Targets: [{ Id: "t", Arn: queue.arn }] }));
    const removed = await events.send(new RemoveTargetsCommand({ Rule: name, Ids: ["t"] }));
    expect(removed.FailedEntryCount).toBe(0);
    expect((await events.send(new ListTargetsByRuleCommand({ Rule: name }))).Targets ?? []).toEqual([]);
  });

  test("PutTargets on a missing rule fails ResourceNotFoundException", async () => {
    const queue = await makeQueue();
    await expectFailure(events.send(new PutTargetsCommand({ Rule: ruleName(), Targets: [{ Id: "t", Arn: queue.arn }] })), {
      name: "ResourceNotFoundException",
      status: 400,
    });
  });
});

describe("EventBridge PutEvents", () => {
  test("PutEvents returns an EventId per entry and delivers the event envelope to an SQS target", async () => {
    const { queue } = await patternRule({ source: ["conformance.app"] });
    const out = await events.send(new PutEventsCommand({ Entries: [entry({ Resources: ["arn:aws:ec2:us-east-1:000000000000:instance/i-1"] })] }));
    expect(out.FailedEntryCount).toBe(0);
    const eventId = out.Entries?.[0]?.EventId;
    expect(eventId).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
    const [message] = await drain(queue.url, 1);
    const body = JSON.parse(message?.Body ?? "null");
    expect(Object.keys(body)).toEqual(["version", "id", "detail-type", "source", "account", "time", "region", "resources", "detail"]);
    expect(body).toMatchObject({
      version: "0",
      id: eventId,
      "detail-type": "Order Placed",
      source: "conformance.app",
      account: defaultAccount,
      region,
      resources: ["arn:aws:ec2:us-east-1:000000000000:instance/i-1"],
      detail: { orderId: "o-1", state: "paid", amount: 25 },
    });
    expect(body.time).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/);
  });

  test("events that do not match the pattern are not delivered", async () => {
    const { queue } = await patternRule({ source: ["conformance.app"], "detail-type": ["Order Placed"] });
    await events.send(
      new PutEventsCommand({ Entries: [entry({ Source: "other.app" }), entry({ DetailType: "Other" })] }),
    );
    await events.send(new PutEventsCommand({ Entries: [entry({ Detail: JSON.stringify({ marker: "matching" }) })] }));
    const got = await drain(queue.url, 2, 3000);
    expect(got.map((m) => JSON.parse(m.Body ?? "{}").detail)).toEqual([{ marker: "matching" }]);
  });

  test("a disabled rule delivers nothing", async () => {
    const { name, queue } = await patternRule({ source: ["conformance.app"] });
    await events.send(new DisableRuleCommand({ Name: name }));
    await events.send(new PutEventsCommand({ Entries: [entry()] }));
    expect(await drain(queue.url, 1, 2500)).toEqual([]);
  });

  test("a fixed Input replaces the event as the message body", async () => {
    const { queue } = await patternRule({ source: ["conformance.app"] }, { Input: '{"constant":"value"}' });
    await events.send(new PutEventsCommand({ Entries: [entry()] }));
    const [message] = await drain(queue.url, 1);
    expect(JSON.parse(message?.Body ?? "null")).toEqual({ constant: "value" });
  });

  test("InputPath selects a subtree of the event", async () => {
    const { queue } = await patternRule({ source: ["conformance.app"] }, { InputPath: "$.detail" });
    await events.send(new PutEventsCommand({ Entries: [entry()] }));
    const [message] = await drain(queue.url, 1);
    expect(JSON.parse(message?.Body ?? "null")).toEqual({ orderId: "o-1", state: "paid", amount: 25 });
  });

  test("an InputPath that selects a string delivers the JSON text of that string", async () => {
    const { queue } = await patternRule({ source: ["conformance.app"] }, { InputPath: "$.detail.orderId" });
    await events.send(new PutEventsCommand({ Entries: [entry()] }));
    const [message] = await drain(queue.url, 1);
    expect(message?.Body).toBe('"o-1"');
  });

  test("an InputTransformer builds the body from extracted variables and reserved names", async () => {
    const name = ruleName();
    const queue = await makeQueue();
    await events.send(new PutRuleCommand({ Name: name, EventPattern: JSON.stringify({ source: ["conformance.app"] }) }));
    await events.send(
      new PutTargetsCommand({
        Rule: name,
        Targets: [
          {
            Id: "t",
            Arn: queue.arn,
            InputTransformer: {
              InputPathsMap: { order: "$.detail.orderId", state: "$.detail.state", detail: "$.detail" },
              InputTemplate: '{"order": <order>, "state": "<state>", "rule": "<aws.events.rule-name>", "detail": <detail>}',
            },
          },
        ],
      }),
    );
    await events.send(new PutEventsCommand({ Entries: [entry()] }));
    const [message] = await drain(queue.url, 1);
    expect(JSON.parse(message?.Body ?? "null")).toEqual({
      order: "o-1",
      state: "paid",
      rule: name,
      detail: { orderId: "o-1", state: "paid", amount: 25 },
    });
  });

  test("a variable whose path matches nothing renders empty without an error", async () => {
    const name = ruleName();
    const queue = await makeQueue();
    await events.send(new PutRuleCommand({ Name: name, EventPattern: JSON.stringify({ source: ["conformance.app"] }) }));
    await events.send(
      new PutTargetsCommand({
        Rule: name,
        Targets: [{ Id: "t", Arn: queue.arn, InputTransformer: { InputPathsMap: { gone: "$.detail.absent" }, InputTemplate: '{"value": "<gone>"}' } }],
      }),
    );
    await events.send(new PutEventsCommand({ Entries: [entry()] }));
    const [message] = await drain(queue.url, 1);
    expect(JSON.parse(message?.Body ?? "null")).toEqual({ value: "" });
  });

  test("an event fans out to several rules and several targets", async () => {
    const a = await patternRule({ source: ["conformance.fanout"] });
    const b = await patternRule({ source: ["conformance.fanout"] });
    await events.send(new PutEventsCommand({ Entries: [entry({ Source: "conformance.fanout" })] }));
    expect(await drain(a.queue.url, 1)).toHaveLength(1);
    expect(await drain(b.queue.url, 1)).toHaveLength(1);
  });

  test("an SNS topic target receives the event as the Message of a notification", async () => {
    const topic = await sns.send(new CreateTopicCommand({ Name: uniq("evtopic") }));
    const queue = await makeQueue();
    await sns.send(new SubscribeCommand({ TopicArn: topic.TopicArn, Protocol: "sqs", Endpoint: queue.arn, Attributes: { RawMessageDelivery: "true" } }));
    const name = ruleName();
    await events.send(new PutRuleCommand({ Name: name, EventPattern: JSON.stringify({ source: ["conformance.sns"] }) }));
    await events.send(new PutTargetsCommand({ Rule: name, Targets: [{ Id: "t", Arn: topic.TopicArn ?? "" }] }));
    await events.send(new PutEventsCommand({ Entries: [entry({ Source: "conformance.sns" })] }));
    const [message] = await drain(queue.url, 1);
    expect(JSON.parse(message?.Body ?? "null")).toMatchObject({ source: "conformance.sns", "detail-type": "Order Placed" });
  });

  test("a FIFO SQS target takes its group id from SqsParameters", async () => {
    const queueName = `${uniq("evfifo")}.fifo`;
    const q = await sqs.send(new CreateQueueCommand({ QueueName: queueName, Attributes: { FifoQueue: "true", ContentBasedDeduplication: "true" } }));
    const name = ruleName();
    await events.send(new PutRuleCommand({ Name: name, EventPattern: JSON.stringify({ source: ["conformance.fifo"] }) }));
    await events.send(
      new PutTargetsCommand({
        Rule: name,
        Targets: [{ Id: "t", Arn: `arn:aws:sqs:${region}:${defaultAccount}:${queueName}`, SqsParameters: { MessageGroupId: "grp" } }],
      }),
    );
    await events.send(new PutEventsCommand({ Entries: [entry({ Source: "conformance.fifo" })] }));
    const [message] = await drain(q.QueueUrl ?? "", 1);
    expect(message?.Attributes?.MessageGroupId).toBe("grp");
    expect(JSON.parse(message?.Body ?? "null")).toMatchObject({ source: "conformance.fifo" });
  });

  test("PutEvents with more than 10 entries fails ValidationException", async () => {
    await expectFailure(events.send(new PutEventsCommand({ Entries: Array.from({ length: 11 }, () => entry()) })), {
      name: "ValidationException",
      status: 400,
      messageIncludes: "at 'entries' failed to satisfy constraint: Member must have length less than or equal to 10",
    });
  });

  test("a missing Source is a per-entry InvalidArgument failure with status 200", async () => {
    const out = await events.send(new PutEventsCommand({ Entries: [entry(), entry({ Source: undefined })] }));
    expect(out.FailedEntryCount).toBe(1);
    expect(out.Entries?.[0]?.EventId).toBeDefined();
    expect(out.Entries?.[1]).toMatchObject({
      ErrorCode: "InvalidArgument",
      ErrorMessage: "Parameter Source is not valid. Reason: Source is a required argument.",
    });
  });

  test("a missing DetailType and a missing Detail are per-entry failures", async () => {
    const out = await events.send(new PutEventsCommand({ Entries: [entry(), entry({ DetailType: undefined }), entry({ Detail: undefined })] }));
    expect(out.FailedEntryCount).toBe(2);
    expect(out.Entries?.[1]).toMatchObject({ ErrorCode: "InvalidArgument", ErrorMessage: "Parameter DetailType is not valid. Reason: DetailType is a required argument." });
    expect(out.Entries?.[2]).toMatchObject({ ErrorCode: "InvalidArgument", ErrorMessage: "Parameter Detail is not valid. Reason: Detail is a required argument." });
  });

  test("a Detail that is not a JSON object is a per-entry MalformedDetail failure", async () => {
    const out = await events.send(new PutEventsCommand({ Entries: [entry(), entry({ Detail: "not json" }), entry({ Detail: "[1,2]" })] }));
    expect(out.FailedEntryCount).toBe(2);
    expect(out.Entries?.[1]).toMatchObject({ ErrorCode: "MalformedDetail", ErrorMessage: "Detail is malformed." });
    expect(out.Entries?.[2]).toMatchObject({ ErrorCode: "MalformedDetail" });
  });

  test("an event put to a bus that does not exist is accepted and dropped", async () => {
    const out = await events.send(new PutEventsCommand({ Entries: [entry({ EventBusName: uniq("nobus") })] }));
    expect(out.FailedEntryCount).toBe(0);
    expect(out.Entries?.[0]?.EventId).toBeDefined();
  });

  test("a request whose entries total more than 1 MiB fails ValidationException", async () => {
    await expectFailure(
      events.send(new PutEventsCommand({ Entries: [entry({ Detail: JSON.stringify({ blob: "x".repeat(1_048_576) }) })] })),
      { name: "ValidationException", status: 400, messageIncludes: "Total size of the entries in the request is over the limit." },
    );
  });
});

describe("EventBridge event patterns", () => {
  async function matches(pattern: unknown, event: unknown): Promise<boolean> {
    const out = await events.send(new TestEventPatternCommand({ EventPattern: JSON.stringify(pattern), Event: JSON.stringify(event) }));
    return out.Result === true;
  }

  const envelope = (detail: Record<string, unknown>, extra: Record<string, unknown> = {}) => ({
    id: "id-1",
    version: "0",
    account: defaultAccount,
    time: "2026-01-01T00:00:00Z",
    region,
    source: "conformance.app",
    "detail-type": "Thing",
    resources: [],
    detail,
    ...extra,
  });

  test("exact values, OR within a leaf and AND across fields", async () => {
    expect(await matches({ source: ["conformance.app"], detail: { state: ["a", "b"] } }, envelope({ state: "b" }))).toBe(true);
    expect(await matches({ source: ["conformance.app"], detail: { state: ["a", "b"] } }, envelope({ state: "c" }))).toBe(false);
    expect(await matches({ source: ["other"], detail: { state: ["a"] } }, envelope({ state: "a" }))).toBe(false);
  });

  test("prefix, suffix and equals-ignore-case", async () => {
    expect(await matches({ detail: { k: [{ prefix: "us-" }] } }, envelope({ k: "us-east-1" }))).toBe(true);
    expect(await matches({ detail: { k: [{ suffix: ".png" }] } }, envelope({ k: "a.png" }))).toBe(true);
    expect(await matches({ detail: { k: [{ suffix: ".png" }] } }, envelope({ k: "a.jpg" }))).toBe(false);
    expect(await matches({ detail: { k: [{ "equals-ignore-case": "ALICE" }] } }, envelope({ k: "alice" }))).toBe(true);
  });

  test("anything-but, numeric, exists, cidr and wildcard", async () => {
    expect(await matches({ detail: { k: [{ "anything-but": ["x", "y"] }] } }, envelope({ k: "z" }))).toBe(true);
    expect(await matches({ detail: { k: [{ "anything-but": ["x", "y"] }] } }, envelope({ k: "x" }))).toBe(false);
    expect(await matches({ detail: { n: [{ numeric: [">", 0, "<=", 5] }] } }, envelope({ n: 5 }))).toBe(true);
    expect(await matches({ detail: { n: [{ numeric: [">", 0, "<=", 5] }] } }, envelope({ n: 6 }))).toBe(false);
    expect(await matches({ detail: { n: [{ numeric: ["=", 300] }] } }, envelope({ n: "300" }))).toBe(false);
    expect(await matches({ detail: { k: [{ exists: true }] } }, envelope({ k: "v" }))).toBe(true);
    expect(await matches({ detail: { k: [{ exists: false }] } }, envelope({ other: 1 }))).toBe(true);
    expect(await matches({ detail: { ip: [{ cidr: "10.0.0.0/24" }] } }, envelope({ ip: "10.0.0.9" }))).toBe(true);
    expect(await matches({ detail: { ip: [{ cidr: "10.0.0.0/24" }] } }, envelope({ ip: "10.0.1.9" }))).toBe(false);
    expect(await matches({ detail: { f: [{ wildcard: "dir/*.png" }] } }, envelope({ f: "dir/a.png" }))).toBe(true);
  });

  test("arrays in the event match when any element matches, and the fields must come from one element", async () => {
    expect(await matches({ detail: { tags: ["b"] } }, envelope({ tags: ["a", "b"] }))).toBe(true);
    const employees = { employees: [{ firstName: "Anna", lastName: "Smith" }, { firstName: "Peter", lastName: "Jones" }] };
    expect(await matches({ detail: { employees: { firstName: ["Anna"] } } }, envelope(employees))).toBe(true);
    expect(await matches({ detail: { employees: { firstName: ["Anna"], lastName: ["Jones"] } } }, envelope(employees))).toBe(false);
  });

  test("$or combines alternatives", async () => {
    const pattern = { detail: { $or: [{ a: [1] }, { b: ["x"] }] } };
    expect(await matches(pattern, envelope({ b: "x" }))).toBe(true);
    expect(await matches(pattern, envelope({ a: 2, b: "y" }))).toBe(false);
  });

  test("null and the empty string are different values", async () => {
    expect(await matches({ detail: { k: [null] } }, envelope({ k: null }))).toBe(true);
    expect(await matches({ detail: { k: [""] } }, envelope({ k: null }))).toBe(false);
    expect(await matches({ detail: { k: [""] } }, envelope({ k: "" }))).toBe(true);
  });

  test("keys and operators are case-sensitive: an unknown operator is an invalid pattern", async () => {
    await expectFailure(
      events.send(new PutRuleCommand({ Name: ruleName(), EventPattern: JSON.stringify({ detail: { k: [{ EXISTS: true }] } }) })),
      { name: "InvalidEventPatternException", status: 400, messageIncludes: "Event pattern is not valid. Reason: Unrecognized match type EXISTS" },
    );
  });

  test("a non-array leaf, an empty array and broken JSON are invalid patterns", async () => {
    for (const [pattern, reason] of [
      [JSON.stringify({ source: "a" }), '"source" must be an object or an array'],
      [JSON.stringify({ source: [] }), "Empty arrays are not allowed"],
      ["{'source': ['a']}", "Unexpected character"],
    ] as const) {
      await expectFailure(events.send(new PutRuleCommand({ Name: ruleName(), EventPattern: pattern })), {
        name: "InvalidEventPatternException",
        status: 400,
        messageIncludes: `Event pattern is not valid. Reason: ${reason}`,
      });
    }
  });
});

describe("EventBridge scheduled rules", () => {
  test("rate(1 minute) rules deliver a Scheduled Event, or their fixed Input, about a minute after creation", async () => {
    const name = ruleName();
    const queue = await makeQueue();
    const fixedName = ruleName();
    const fixedQueue = await makeQueue();
    const created = Date.now();
    await events.send(new PutRuleCommand({ Name: name, ScheduleExpression: "rate(1 minute)" }));
    await events.send(new PutTargetsCommand({ Rule: name, Targets: [{ Id: "t", Arn: queue.arn }] }));
    await events.send(new PutRuleCommand({ Name: fixedName, ScheduleExpression: "rate(1 minute)" }));
    await events.send(new PutTargetsCommand({ Rule: fixedName, Targets: [{ Id: "t", Arn: fixedQueue.arn, Input: '{"job":"nightly"}' }] }));
    const [[message], [fixed]] = await Promise.all([drain(queue.url, 1, 100_000), drain(fixedQueue.url, 1, 100_000)]);
    expect(message).toBeDefined();
    expect(JSON.parse(fixed?.Body ?? "null")).toEqual({ job: "nightly" });
    await events.send(new DisableRuleCommand({ Name: fixedName }));
    const elapsed = Date.now() - created;
    expect(elapsed).toBeGreaterThan(45_000);
    expect(elapsed).toBeLessThan(95_000);
    const body = JSON.parse(message?.Body ?? "null");
    expect(body).toMatchObject({
      version: "0",
      "detail-type": "Scheduled Event",
      source: "aws.events",
      account: defaultAccount,
      region,
      resources: [ruleArn(name)],
      detail: {},
    });
    expect(body.time).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/);
    await events.send(new DisableRuleCommand({ Name: name }));
  }, 120_000);
});
