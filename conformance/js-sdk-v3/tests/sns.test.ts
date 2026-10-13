// SNS over awsQuery, through @aws-sdk/client-sns, with SQS endpoints read back
// through @aws-sdk/client-sqs. Expected behavior: docs/research/phase1/sns.md.

import { describe, expect, test } from "bun:test";
import {
  AddPermissionCommand,
  CreateTopicCommand,
  DeleteTopicCommand,
  GetSubscriptionAttributesCommand,
  GetTopicAttributesCommand,
  ListSubscriptionsByTopicCommand,
  ListSubscriptionsCommand,
  ListTagsForResourceCommand,
  ListTopicsCommand,
  PublishBatchCommand,
  PublishCommand,
  RemovePermissionCommand,
  SetSubscriptionAttributesCommand,
  SetTopicAttributesCommand,
  SNSClient,
  SubscribeCommand,
  TagResourceCommand,
  UnsubscribeCommand,
  UntagResourceCommand,
  type MessageAttributeValue,
} from "@aws-sdk/client-sns";
import {
  CreateQueueCommand,
  DeleteMessageBatchCommand,
  GetQueueAttributesCommand,
  ReceiveMessageCommand,
  SQSClient,
  type Message,
} from "@aws-sdk/client-sqs";
import { clientConfig, defaultAccount, region } from "../support/env";
import { awsFailure, eventually, expectFailure, sleep, uniq } from "../support/helpers";

const sns = new SNSClient(clientConfig());
const sqs = new SQSClient(clientConfig());

async function makeTopic(
  options: { fifo?: boolean; attributes?: Record<string, string>; tags?: Array<{ Key: string; Value: string }> } = {},
): Promise<{ arn: string; name: string }> {
  const name = options.fifo ? `${uniq("t")}.fifo` : uniq("t");
  const out = await sns.send(
    new CreateTopicCommand({
      Name: name,
      Attributes: { ...(options.fifo ? { FifoTopic: "true" } : {}), ...options.attributes },
      Tags: options.tags,
    }),
  );
  if (out.TopicArn === undefined) throw new Error("CreateTopic returned no TopicArn");
  return { arn: out.TopicArn, name };
}

async function makeQueue(fifo = false): Promise<{ url: string; arn: string }> {
  const name = fifo ? `${uniq("q")}.fifo` : uniq("q");
  const out = await sqs.send(
    new CreateQueueCommand({ QueueName: name, Attributes: fifo ? { FifoQueue: "true" } : {} }),
  );
  if (out.QueueUrl === undefined) throw new Error("CreateQueue returned no QueueUrl");
  return { url: out.QueueUrl, arn: `arn:aws:sqs:${region}:${defaultAccount}:${name}` };
}

async function subscribeQueue(
  topicArn: string,
  queueArn: string,
  attributes: Record<string, string> = {},
): Promise<string> {
  const out = await sns.send(
    new SubscribeCommand({
      TopicArn: topicArn,
      Protocol: "sqs",
      Endpoint: queueArn,
      Attributes: attributes,
      ReturnSubscriptionArn: true,
    }),
  );
  if (out.SubscriptionArn === undefined) throw new Error("Subscribe returned no SubscriptionArn");
  return out.SubscriptionArn;
}

/**
 * Receives and deletes messages until want have arrived, then waits settleMs
 * more and fails if extras show up. Use want 0 only with a sentinel pattern.
 */
async function collect(queueUrl: string, want: number, settleMs = 400): Promise<Message[]> {
  const got: Message[] = [];
  const pull = async (waitSeconds: number) => {
    const out = await sqs.send(
      new ReceiveMessageCommand({
        QueueUrl: queueUrl,
        MaxNumberOfMessages: 10,
        WaitTimeSeconds: waitSeconds,
        MessageAttributeNames: ["All"],
        MessageSystemAttributeNames: ["All"],
      }),
    );
    const messages = out.Messages ?? [];
    if (messages.length > 0) {
      await sqs.send(
        new DeleteMessageBatchCommand({
          QueueUrl: queueUrl,
          Entries: messages.map((m, i) => ({ Id: `m${i}`, ReceiptHandle: m.ReceiptHandle })),
        }),
      );
    }
    got.push(...messages);
  };
  const deadline = Date.now() + 10_000;
  while (got.length < want && Date.now() < deadline) await pull(1);
  if (got.length < want) throw new Error(`wanted ${want} messages from ${queueUrl}, got ${got.length}`);
  await sleep(settleMs);
  await pull(0);
  return got;
}

const bodiesOf = (messages: Message[]) => messages.map((m) => m.Body).sort();

describe("SNS topics", () => {
  test("CreateTopic returns an ARN of the form arn:aws:sns:<region>:<account>:<name>", async () => {
    const t = await makeTopic();
    expect(t.arn).toBe(`arn:aws:sns:${region}:${defaultAccount}:${t.name}`);
  });

  test("CreateTopic is idempotent and returns the same ARN", async () => {
    const t = await makeTopic();
    const again = await sns.send(new CreateTopicCommand({ Name: t.name }));
    expect(again.TopicArn).toBe(t.arn);
  });

  test("CreateTopic with different attributes on an existing topic fails InvalidParameter", async () => {
    const t = await makeTopic({ attributes: { DisplayName: "one" } });
    await expectFailure(sns.send(new CreateTopicCommand({ Name: t.name, Attributes: { DisplayName: "two" } })), {
      name: "InvalidParameter",
      status: 400,
      messageIncludes: "Invalid parameter: Attributes Reason: Topic already exists with different attributes",
    });
  });

  test("CreateTopic rejects names with characters outside [A-Za-z0-9_-]", async () => {
    await expectFailure(sns.send(new CreateTopicCommand({ Name: "bad name" })), {
      name: "InvalidParameter",
      status: 400,
      messageIncludes: "Invalid parameter: Topic Name",
    });
  });

  test("CreateTopic rejects a .fifo name without FifoTopic and FifoTopic without a .fifo name", async () => {
    await expectFailure(sns.send(new CreateTopicCommand({ Name: `${uniq("x")}.fifo` })), {
      name: "InvalidParameter",
      messageIncludes: "Invalid parameter: Topic Name",
    });
    await expectFailure(sns.send(new CreateTopicCommand({ Name: uniq("x"), Attributes: { FifoTopic: "true" } })), {
      name: "InvalidParameter",
      messageIncludes: "Invalid parameter: Topic Name",
    });
  });

  test("a new standard topic has the recorded default attributes", async () => {
    const t = await makeTopic();
    const attrs = (await sns.send(new GetTopicAttributesCommand({ TopicArn: t.arn }))).Attributes ?? {};
    expect(attrs).toMatchObject({
      TopicArn: t.arn,
      Owner: defaultAccount,
      DisplayName: "",
      SubscriptionsConfirmed: "0",
      SubscriptionsDeleted: "0",
      SubscriptionsPending: "0",
    });
    expect(attrs.FifoTopic).toBeUndefined();
    expect(JSON.parse(attrs.Policy ?? "null")).toEqual({
      Version: "2008-10-17",
      Id: "__default_policy_ID",
      Statement: [
        {
          Sid: "__default_statement_ID",
          Effect: "Allow",
          Principal: { AWS: "*" },
          Action: [
            "SNS:GetTopicAttributes",
            "SNS:SetTopicAttributes",
            "SNS:AddPermission",
            "SNS:RemovePermission",
            "SNS:DeleteTopic",
            "SNS:Subscribe",
            "SNS:ListSubscriptionsByTopic",
            "SNS:Publish",
          ],
          Resource: t.arn,
          Condition: { StringEquals: { "AWS:SourceOwner": defaultAccount } },
        },
      ],
    });
    expect(JSON.parse(attrs.EffectiveDeliveryPolicy ?? "null")).toEqual({
      http: {
        defaultHealthyRetryPolicy: {
          minDelayTarget: 20,
          maxDelayTarget: 20,
          numRetries: 3,
          numMaxDelayRetries: 0,
          numNoDelayRetries: 0,
          numMinDelayRetries: 0,
          backoffFunction: "linear",
        },
        disableSubscriptionOverrides: false,
        defaultRequestPolicy: { headerContentType: "text/plain; charset=UTF-8" },
      },
    });
  });

  test("a FIFO topic reports FifoTopic true and ContentBasedDeduplication false", async () => {
    const t = await makeTopic({ fifo: true });
    const attrs = (await sns.send(new GetTopicAttributesCommand({ TopicArn: t.arn }))).Attributes ?? {};
    expect(attrs).toMatchObject({ FifoTopic: "true", ContentBasedDeduplication: "false" });
  });

  test("GetTopicAttributes on a missing topic fails NotFound with status 404", async () => {
    await expectFailure(
      sns.send(new GetTopicAttributesCommand({ TopicArn: `arn:aws:sns:${region}:${defaultAccount}:${uniq("nope")}` })),
      { name: "NotFound", status: 404, messageIncludes: "Topic does not exist" },
    );
  });

  test("SetTopicAttributes changes DisplayName", async () => {
    const t = await makeTopic();
    await sns.send(new SetTopicAttributesCommand({ TopicArn: t.arn, AttributeName: "DisplayName", AttributeValue: "shown" }));
    const attrs = (await sns.send(new GetTopicAttributesCommand({ TopicArn: t.arn }))).Attributes ?? {};
    expect(attrs.DisplayName).toBe("shown");
  });

  test("SetTopicAttributes cannot set FifoTopic after creation", async () => {
    const t = await makeTopic();
    await expectFailure(
      sns.send(new SetTopicAttributesCommand({ TopicArn: t.arn, AttributeName: "FifoTopic", AttributeValue: "true" })),
      { name: "InvalidParameter", status: 400, messageIncludes: "Invalid parameter: AttributeName" },
    );
  });

  test("a topic DeliveryPolicy reads back with disableSubscriptionOverrides added", async () => {
    const t = await makeTopic();
    await sns.send(
      new SetTopicAttributesCommand({
        TopicArn: t.arn,
        AttributeName: "DeliveryPolicy",
        AttributeValue: JSON.stringify({ http: { defaultRequestPolicy: { headerContentType: "application/json" } } }),
      }),
    );
    const attrs = (await sns.send(new GetTopicAttributesCommand({ TopicArn: t.arn }))).Attributes ?? {};
    expect(JSON.parse(attrs.DeliveryPolicy ?? "null")).toEqual({
      http: { disableSubscriptionOverrides: false, defaultRequestPolicy: { headerContentType: "application/json" } },
    });
    const effective = JSON.parse(attrs.EffectiveDeliveryPolicy ?? "null");
    expect(effective.http.defaultRequestPolicy.headerContentType).toBe("application/json");
    expect(effective.http.defaultHealthyRetryPolicy.numRetries).toBe(3);
  });

  test("a topic Policy set through SetTopicAttributes is stored verbatim", async () => {
    const t = await makeTopic();
    const policy = {
      Version: "2012-10-17",
      Statement: [
        { Sid: "s", Effect: "Allow", Principal: { AWS: `arn:aws:iam::${defaultAccount}:root` }, Action: "sns:Publish", Resource: t.arn },
      ],
    };
    await sns.send(new SetTopicAttributesCommand({ TopicArn: t.arn, AttributeName: "Policy", AttributeValue: JSON.stringify(policy) }));
    const attrs = (await sns.send(new GetTopicAttributesCommand({ TopicArn: t.arn }))).Attributes ?? {};
    expect(JSON.parse(attrs.Policy ?? "null")).toEqual(policy);
  });

  test("AddPermission appends a statement and RemovePermission removes it", async () => {
    const t = await makeTopic();
    await sns.send(
      new AddPermissionCommand({ TopicArn: t.arn, Label: "extra", AWSAccountId: ["111122223333"], ActionName: ["Publish"] }),
    );
    let policy = JSON.parse(
      ((await sns.send(new GetTopicAttributesCommand({ TopicArn: t.arn }))).Attributes ?? {}).Policy ?? "null",
    );
    expect(policy.Statement.find((s: { Sid: string }) => s.Sid === "extra")).toEqual({
      Sid: "extra",
      Effect: "Allow",
      Principal: { AWS: "arn:aws:iam::111122223333:root" },
      Action: "SNS:Publish",
      Resource: t.arn,
    });
    await sns.send(new RemovePermissionCommand({ TopicArn: t.arn, Label: "extra" }));
    policy = JSON.parse(
      ((await sns.send(new GetTopicAttributesCommand({ TopicArn: t.arn }))).Attributes ?? {}).Policy ?? "null",
    );
    expect(policy.Statement.map((s: { Sid: string }) => s.Sid)).toEqual(["__default_statement_ID"]);
  });

  test("AddPermission with a duplicate label fails InvalidParameter", async () => {
    const t = await makeTopic();
    const input = { TopicArn: t.arn, Label: "dup", AWSAccountId: ["111122223333"], ActionName: ["Publish"] };
    await sns.send(new AddPermissionCommand(input));
    await expectFailure(sns.send(new AddPermissionCommand(input)), {
      name: "InvalidParameter",
      status: 400,
      messageIncludes: "Statement already exists",
    });
  });

  test("ListTopics includes a created topic and DeleteTopic removes it, idempotently", async () => {
    const t = await makeTopic();
    const found = await eventually("the topic in ListTopics", async () => {
      let token: string | undefined;
      do {
        const out = await sns.send(new ListTopicsCommand({ NextToken: token }));
        if (out.Topics?.some((x) => x.TopicArn === t.arn)) return true;
        token = out.NextToken;
      } while (token !== undefined);
      return undefined;
    });
    expect(found).toBe(true);
    await sns.send(new DeleteTopicCommand({ TopicArn: t.arn }));
    await sns.send(new DeleteTopicCommand({ TopicArn: t.arn }));
    await expectFailure(sns.send(new GetTopicAttributesCommand({ TopicArn: t.arn })), { name: "NotFound", status: 404 });
  });

  test("tags set at creation are listed; TagResource replaces by key; UntagResource removes", async () => {
    const t = await makeTopic({ tags: [{ Key: "env", Value: "test" }] });
    const list = async () =>
      Object.fromEntries(
        ((await sns.send(new ListTagsForResourceCommand({ ResourceArn: t.arn }))).Tags ?? []).map((x) => [x.Key, x.Value]),
      );
    expect(await list()).toEqual({ env: "test" });
    await sns.send(
      new TagResourceCommand({
        ResourceArn: t.arn,
        Tags: [
          { Key: "env", Value: "prod" },
          { Key: "team", Value: "a" },
        ],
      }),
    );
    expect(await list()).toEqual({ env: "prod", team: "a" });
    await sns.send(new UntagResourceCommand({ ResourceArn: t.arn, TagKeys: ["env"] }));
    expect(await list()).toEqual({ team: "a" });
  });

  test("TagResource with duplicate keys in one request fails InvalidParameter", async () => {
    const t = await makeTopic();
    await expectFailure(
      sns.send(
        new TagResourceCommand({
          ResourceArn: t.arn,
          Tags: [
            { Key: "k", Value: "1" },
            { Key: "k", Value: "2" },
          ],
        }),
      ),
      { name: "InvalidParameter", status: 400, messageIncludes: "Duplicated keys are not allowed." },
    );
  });
});

describe("SNS subscriptions", () => {
  test("an SQS subscription in the same account is confirmed on return with the recorded attributes", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    const arn = await subscribeQueue(t.arn, q.arn);
    expect(arn.startsWith(`${t.arn}:`)).toBe(true);
    const attrs = (await sns.send(new GetSubscriptionAttributesCommand({ SubscriptionArn: arn }))).Attributes ?? {};
    expect(attrs).toMatchObject({
      SubscriptionArn: arn,
      TopicArn: t.arn,
      Protocol: "sqs",
      Endpoint: q.arn,
      Owner: defaultAccount,
      PendingConfirmation: "false",
      ConfirmationWasAuthenticated: "true",
      RawMessageDelivery: "false",
    });
    expect((await sns.send(new GetTopicAttributesCommand({ TopicArn: t.arn }))).Attributes?.SubscriptionsConfirmed).toBe("1");
  });

  test("subscribing the same endpoint twice returns the same subscription ARN", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    expect(await subscribeQueue(t.arn, q.arn)).toBe(await subscribeQueue(t.arn, q.arn));
  });

  test("subscribing the same endpoint with a different attribute value fails InvalidParameter", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    await subscribeQueue(t.arn, q.arn, { RawMessageDelivery: "true" });
    await expectFailure(subscribeQueue(t.arn, q.arn, { RawMessageDelivery: "false" }), {
      name: "InvalidParameter",
      status: 400,
      messageIncludes: "Invalid parameter: Attributes Reason: Subscription already exists with different attributes",
    });
  });

  test("an sqs endpoint that is a queue URL fails InvalidParameter: SQS endpoint ARN", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    await expectFailure(sns.send(new SubscribeCommand({ TopicArn: t.arn, Protocol: "sqs", Endpoint: q.url })), {
      name: "InvalidParameter",
      status: 400,
      messageIncludes: "Invalid parameter: SQS endpoint ARN",
    });
  });

  test("an sqs endpoint ARN of a queue that does not exist is accepted", async () => {
    const t = await makeTopic();
    const arn = await subscribeQueue(t.arn, `arn:aws:sqs:${region}:${defaultAccount}:${uniq("ghost")}`);
    expect(arn.startsWith(`${t.arn}:`)).toBe(true);
  });

  test("Subscribe with an unsupported protocol fails with the documented message", async () => {
    const t = await makeTopic();
    await expectFailure(sns.send(new SubscribeCommand({ TopicArn: t.arn, Protocol: "test-protocol", Endpoint: "x" })), {
      name: "InvalidParameter",
      status: 400,
      messageIncludes: "Invalid parameter: Amazon SNS does not support this protocol string: test-protocol",
    });
  });

  test("Subscribe to a missing topic fails NotFound", async () => {
    const q = await makeQueue();
    await expectFailure(
      subscribeQueue(`arn:aws:sns:${region}:${defaultAccount}:${uniq("nope")}`, q.arn),
      { name: "NotFound", status: 404, messageIncludes: "Topic does not exist" },
    );
  });

  test("a FIFO queue cannot subscribe to a standard topic", async () => {
    const t = await makeTopic();
    const q = await makeQueue(true);
    await expectFailure(subscribeQueue(t.arn, q.arn), {
      name: "InvalidParameter",
      status: 400,
      messageIncludes: "FIFO SQS Queues can not be subscribed to standard SNS topics",
    });
  });

  test("an HTTP subscription is pending: the ARN is the literal 'pending confirmation' unless ReturnSubscriptionArn is set", async () => {
    const t = await makeTopic();
    const plain = await sns.send(new SubscribeCommand({ TopicArn: t.arn, Protocol: "http", Endpoint: "http://127.0.0.1:9/hook" }));
    expect(plain.SubscriptionArn).toBe("pending confirmation");
    const t2 = await makeTopic();
    const returned = await sns.send(
      new SubscribeCommand({
        TopicArn: t2.arn,
        Protocol: "http",
        Endpoint: "http://127.0.0.1:9/hook",
        ReturnSubscriptionArn: true,
      }),
    );
    expect(returned.SubscriptionArn?.startsWith(`${t2.arn}:`)).toBe(true);
    const attrs = (await sns.send(new GetSubscriptionAttributesCommand({ SubscriptionArn: returned.SubscriptionArn }))).Attributes ?? {};
    expect(attrs.PendingConfirmation).toBe("true");
  });

  test("an invalid SMS endpoint fails InvalidParameter with 'Invalid SMS endpoint'", async () => {
    const t = await makeTopic();
    await expectFailure(sns.send(new SubscribeCommand({ TopicArn: t.arn, Protocol: "sms", Endpoint: "invalid-number" })), {
      name: "InvalidParameter",
      status: 400,
      messageIncludes: "Invalid SMS endpoint: invalid-number",
    });
  });

  test("Unsubscribe removes the subscription, is idempotent, and GetSubscriptionAttributes then fails NotFound", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    const arn = await subscribeQueue(t.arn, q.arn);
    await sns.send(new UnsubscribeCommand({ SubscriptionArn: arn }));
    await sns.send(new UnsubscribeCommand({ SubscriptionArn: arn }));
    await expectFailure(sns.send(new GetSubscriptionAttributesCommand({ SubscriptionArn: arn })), {
      name: "NotFound",
      status: 404,
      messageIncludes: "Subscription does not exist",
    });
  });

  test("Unsubscribe with a malformed ARN fails InvalidParameter naming the ARN element count", async () => {
    await expectFailure(sns.send(new UnsubscribeCommand({ SubscriptionArn: "not-an-arn" })), {
      name: "InvalidParameter",
      status: 400,
      messageIncludes: "Invalid parameter: SubscriptionArn Reason: An ARN must have at least 6 elements, not 1",
    });
  });

  test("ListSubscriptionsByTopic and ListSubscriptions list the subscription with its protocol and endpoint", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    const arn = await subscribeQueue(t.arn, q.arn);
    const byTopic = await sns.send(new ListSubscriptionsByTopicCommand({ TopicArn: t.arn }));
    expect(byTopic.Subscriptions).toEqual([
      { SubscriptionArn: arn, Owner: defaultAccount, Protocol: "sqs", Endpoint: q.arn, TopicArn: t.arn },
    ]);
    const all = await eventually("the subscription in ListSubscriptions", async () => {
      let token: string | undefined;
      do {
        const out = await sns.send(new ListSubscriptionsCommand({ NextToken: token }));
        const hit = out.Subscriptions?.find((s) => s.SubscriptionArn === arn);
        if (hit !== undefined) return hit;
        token = out.NextToken;
      } while (token !== undefined);
      return undefined;
    });
    expect(all.Endpoint).toBe(q.arn);
  });

  // AWS keeps a deleted topic's subscriptions readable until they are
  // unsubscribed (sns.md 2.2, test_delete_subscriptions_on_delete_topic).
  test("DeleteTopic detaches its subscriptions until they are unsubscribed", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    const arn = await subscribeQueue(t.arn, q.arn);
    await sns.send(new DeleteTopicCommand({ TopicArn: t.arn }));
    const detached = await sns.send(new GetSubscriptionAttributesCommand({ SubscriptionArn: arn }));
    expect(detached.Attributes?.TopicArn).toBe(t.arn);
    await sns.send(new UnsubscribeCommand({ SubscriptionArn: arn }));
    await expectFailure(sns.send(new GetSubscriptionAttributesCommand({ SubscriptionArn: arn })), { name: "NotFound", status: 404 });
  });

  test("SetSubscriptionAttributes toggles RawMessageDelivery, rejecting values other than true or false", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    const arn = await subscribeQueue(t.arn, q.arn);
    await sns.send(new SetSubscriptionAttributesCommand({ SubscriptionArn: arn, AttributeName: "RawMessageDelivery", AttributeValue: "true" }));
    expect(
      ((await sns.send(new GetSubscriptionAttributesCommand({ SubscriptionArn: arn }))).Attributes ?? {}).RawMessageDelivery,
    ).toBe("true");
    await expectFailure(
      sns.send(new SetSubscriptionAttributesCommand({ SubscriptionArn: arn, AttributeName: "RawMessageDelivery", AttributeValue: "wrongvalue" })),
      {
        name: "InvalidParameter",
        status: 400,
        messageIncludes: "Invalid parameter: RawMessageDelivery: Invalid value [wrongvalue]. Must be true or false.",
      },
    );
  });

  test("SetSubscriptionAttributes rejects an attribute that cannot be set", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    const arn = await subscribeQueue(t.arn, q.arn);
    await expectFailure(
      sns.send(new SetSubscriptionAttributesCommand({ SubscriptionArn: arn, AttributeName: "Endpoint", AttributeValue: "x" })),
      { name: "InvalidParameter", status: 400, messageIncludes: "Invalid parameter: AttributeName" },
    );
  });

  test("SetSubscriptionAttributes on a missing subscription fails NotFound", async () => {
    const t = await makeTopic();
    await expectFailure(
      sns.send(
        new SetSubscriptionAttributesCommand({
          SubscriptionArn: `${t.arn}:00000000-0000-4000-8000-000000000000`,
          AttributeName: "RawMessageDelivery",
          AttributeValue: "true",
        }),
      ),
      { name: "NotFound", status: 404, messageIncludes: "Subscription does not exist" },
    );
  });

  test("FilterPolicy and FilterPolicyScope are hidden until a policy is set, then default to MessageAttributes", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    const arn = await subscribeQueue(t.arn, q.arn);
    const read = async () => (await sns.send(new GetSubscriptionAttributesCommand({ SubscriptionArn: arn }))).Attributes ?? {};
    expect((await read()).FilterPolicy).toBeUndefined();
    expect((await read()).FilterPolicyScope).toBeUndefined();
    await sns.send(new SetSubscriptionAttributesCommand({ SubscriptionArn: arn, AttributeName: "FilterPolicy", AttributeValue: '{"a":["b"]}' }));
    const attrs = await read();
    expect(JSON.parse(attrs.FilterPolicy ?? "null")).toEqual({ a: ["b"] });
    expect(attrs.FilterPolicyScope).toBe("MessageAttributes");
  });

  test("an empty FilterPolicy removes the policy", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    const arn = await subscribeQueue(t.arn, q.arn, { FilterPolicy: '{"a":["b"]}' });
    await sns.send(new SetSubscriptionAttributesCommand({ SubscriptionArn: arn, AttributeName: "FilterPolicy", AttributeValue: "" }));
    expect(((await sns.send(new GetSubscriptionAttributesCommand({ SubscriptionArn: arn }))).Attributes ?? {}).FilterPolicy).toBeUndefined();
  });

  test("an invalid FilterPolicyScope fails with the documented message", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    const arn = await subscribeQueue(t.arn, q.arn);
    await expectFailure(
      sns.send(new SetSubscriptionAttributesCommand({ SubscriptionArn: arn, AttributeName: "FilterPolicyScope", AttributeValue: "RandomValue" })),
      {
        name: "InvalidParameter",
        status: 400,
        messageIncludes: "Invalid parameter: FilterPolicyScope: Invalid value [RandomValue]. Please use either MessageBody or MessageAttributes",
      },
    );
  });

  test("a subscription RedrivePolicy must hold a valid SQS ARN", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    const arn = await subscribeQueue(t.arn, q.arn);
    await expectFailure(
      sns.send(
        new SetSubscriptionAttributesCommand({
          SubscriptionArn: arn,
          AttributeName: "RedrivePolicy",
          AttributeValue: JSON.stringify({ deadLetterTargetArn: "not-an-arn" }),
        }),
      ),
      { name: "InvalidParameter", status: 400, messageIncludes: "Invalid parameter: RedrivePolicy: deadLetterTargetArn is an invalid arn" },
    );
    const dlq = await makeQueue();
    await sns.send(
      new SetSubscriptionAttributesCommand({
        SubscriptionArn: arn,
        AttributeName: "RedrivePolicy",
        AttributeValue: JSON.stringify({ deadLetterTargetArn: dlq.arn }),
      }),
    );
    const attrs = (await sns.send(new GetSubscriptionAttributesCommand({ SubscriptionArn: arn }))).Attributes ?? {};
    expect(JSON.parse(attrs.RedrivePolicy ?? "null")).toEqual({ deadLetterTargetArn: dlq.arn });
  });
});

describe("SNS publish", () => {
  test("Publish returns a MessageId and the message arrives in the SQS envelope", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    const subscriptionArn = await subscribeQueue(t.arn, q.arn);
    const sent = await sns.send(new PublishCommand({ TopicArn: t.arn, Message: "Hello world!", Subject: "Testing" }));
    expect(sent.MessageId).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
    expect(sent.SequenceNumber).toBeUndefined();
    const [message] = await collect(q.url, 1);
    const envelope = JSON.parse(message?.Body ?? "null");
    expect(Object.keys(envelope)).toEqual([
      "Type",
      "MessageId",
      "TopicArn",
      "Subject",
      "Message",
      "Timestamp",
      "SignatureVersion",
      "Signature",
      "SigningCertURL",
      "UnsubscribeURL",
    ]);
    expect(envelope).toMatchObject({
      Type: "Notification",
      MessageId: sent.MessageId,
      TopicArn: t.arn,
      Subject: "Testing",
      Message: "Hello world!",
      SignatureVersion: "1",
    });
    expect(envelope.Timestamp).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/);
    expect(envelope.UnsubscribeURL).toContain(`Action=Unsubscribe&SubscriptionArn=${subscriptionArn}`);
    expect(envelope.Signature.length).toBeGreaterThan(0);
    expect(message?.MessageAttributes).toBeUndefined();
  });

  test("the envelope omits Subject when none was published and carries MessageAttributes as Type/Value", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    await subscribeQueue(t.arn, q.arn);
    await sns.send(
      new PublishCommand({
        TopicArn: t.arn,
        Message: "attrs",
        MessageAttributes: {
          attr1: { DataType: "Number", StringValue: "99.12" },
          bin: { DataType: "Binary", BinaryValue: new Uint8Array([2, 3, 4]) },
        },
      }),
    );
    const [message] = await collect(q.url, 1);
    const envelope = JSON.parse(message?.Body ?? "null");
    expect("Subject" in envelope).toBe(false);
    expect(envelope.MessageAttributes).toEqual({
      attr1: { Type: "Number", Value: "99.12" },
      bin: { Type: "Binary", Value: "AgME" },
    });
  });

  test("raw delivery sends the Message as the SQS body and the attributes as SQS message attributes", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    await subscribeQueue(t.arn, q.arn, { RawMessageDelivery: "true" });
    await sns.send(
      new PublishCommand({
        TopicArn: t.arn,
        Message: "raw body",
        Subject: "dropped",
        MessageAttributes: {
          n: { DataType: "Number", StringValue: "111" },
          s: { DataType: "String", StringValue: "text" },
          b: { DataType: "Binary", BinaryValue: new Uint8Array([1, 2]) },
        },
      }),
    );
    const [message] = await collect(q.url, 1);
    expect(message?.Body).toBe("raw body");
    expect(message?.MessageAttributes?.n).toMatchObject({ DataType: "Number", StringValue: "111" });
    expect(message?.MessageAttributes?.s).toMatchObject({ DataType: "String", StringValue: "text" });
    expect(Array.from(message?.MessageAttributes?.b?.BinaryValue ?? [])).toEqual([1, 2]);
  });

  test("MessageStructure json delivers the sqs key to an SQS subscriber and default to the rest", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    await subscribeQueue(t.arn, q.arn, { RawMessageDelivery: "true" });
    await sns.send(
      new PublishCommand({
        TopicArn: t.arn,
        MessageStructure: "json",
        Message: JSON.stringify({ default: "fallback", sqs: "for sqs" }),
      }),
    );
    expect((await collect(q.url, 1)).map((m) => m.Body)).toEqual(["for sqs"]);
  });

  test("a published message fans out to every confirmed subscription", async () => {
    const t = await makeTopic();
    const queues = await Promise.all([makeQueue(), makeQueue(), makeQueue()]);
    for (const q of queues) await subscribeQueue(t.arn, q.arn, { RawMessageDelivery: "true" });
    await sns.send(new PublishCommand({ TopicArn: t.arn, Message: "fan" }));
    for (const q of queues) expect(bodiesOf(await collect(q.url, 1))).toEqual(["fan"]);
  });

  test("Publish with an empty message fails InvalidParameter: Empty message", async () => {
    const t = await makeTopic();
    await expectFailure(sns.send(new PublishCommand({ TopicArn: t.arn, Message: "" })), {
      name: "InvalidParameter",
      status: 400,
      messageIncludes: "Invalid parameter: Empty message",
    });
  });

  test("Publish with an empty subject fails InvalidParameter: Subject", async () => {
    const t = await makeTopic();
    await expectFailure(sns.send(new PublishCommand({ TopicArn: t.arn, Message: "m", Subject: "" })), {
      name: "InvalidParameter",
      status: 400,
      messageIncludes: "Invalid parameter: Subject",
    });
  });

  test("Publish over 262144 bytes fails InvalidParameter: Message too long", async () => {
    const t = await makeTopic();
    await sns.send(new PublishCommand({ TopicArn: t.arn, Message: "a".repeat(262_144) }));
    await expectFailure(sns.send(new PublishCommand({ TopicArn: t.arn, Message: "a".repeat(262_145) })), {
      name: "InvalidParameter",
      status: 400,
      messageIncludes: "Invalid parameter: Message too long",
    });
  });

  test("Publish to a missing topic fails NotFound", async () => {
    await expectFailure(
      sns.send(new PublishCommand({ TopicArn: `arn:aws:sns:${region}:${defaultAccount}:${uniq("nope")}`, Message: "m" })),
      { name: "NotFound", status: 404, messageIncludes: "Topic does not exist" },
    );
  });

  test("Publish with a malformed TopicArn fails InvalidParameter naming the ARN element count", async () => {
    await expectFailure(sns.send(new PublishCommand({ TopicArn: "garbage", Message: "m" })), {
      name: "InvalidParameter",
      status: 400,
      messageIncludes: "Invalid parameter: TopicArn Reason: An ARN must have at least 6 elements, not 1",
    });
  });

  test("a message attribute with an unsupported type prefix fails ParameterValueInvalid", async () => {
    const t = await makeTopic();
    await expectFailure(
      sns.send(
        new PublishCommand({ TopicArn: t.arn, Message: "m", MessageAttributes: { attr1: { DataType: "Boolean", StringValue: "true" } } }),
      ),
      {
        name: "ParameterValueInvalid",
        status: 400,
        messageIncludes:
          "The message attribute 'attr1' has an invalid message attribute type, the set of supported type prefixes is Binary, Number, and String.",
      },
    );
  });

  test("an empty String message attribute fails ParameterValueInvalid", async () => {
    const t = await makeTopic();
    await expectFailure(
      sns.send(new PublishCommand({ TopicArn: t.arn, Message: "m", MessageAttributes: { attr1: { DataType: "String", StringValue: "" } } })),
      {
        name: "ParameterValueInvalid",
        status: 400,
        messageIncludes: "The message attribute 'attr1' must contain non-empty message attribute value for message attribute type 'String'.",
      },
    );
  });

  test("a message published before a subscription exists is not delivered to it", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    await sns.send(new PublishCommand({ TopicArn: t.arn, Message: "early" }));
    await subscribeQueue(t.arn, q.arn, { RawMessageDelivery: "true" });
    await sns.send(new PublishCommand({ TopicArn: t.arn, Message: "late" }));
    expect(bodiesOf(await collect(q.url, 1))).toEqual(["late"]);
  });

  test("an unsubscribed queue receives nothing further", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    const arn = await subscribeQueue(t.arn, q.arn, { RawMessageDelivery: "true" });
    await sns.send(new UnsubscribeCommand({ SubscriptionArn: arn }));
    await sns.send(new PublishCommand({ TopicArn: t.arn, Message: "gone" }));
    await sleep(500);
    const out = await sqs.send(new GetQueueAttributesCommand({ QueueUrl: q.url, AttributeNames: ["ApproximateNumberOfMessages"] }));
    expect(out.Attributes?.ApproximateNumberOfMessages).toBe("0");
  });
});

describe("SNS PublishBatch", () => {
  test("PublishBatch delivers every entry and reports a MessageId per entry", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    await subscribeQueue(t.arn, q.arn, { RawMessageDelivery: "true" });
    const out = await sns.send(
      new PublishBatchCommand({
        TopicArn: t.arn,
        PublishBatchRequestEntries: [
          { Id: "a", Message: "one" },
          { Id: "b", Message: "two" },
        ],
      }),
    );
    expect(out.Failed ?? []).toEqual([]);
    expect(out.Successful?.map((s) => s.Id)).toEqual(["a", "b"]);
    expect(out.Successful?.every((s) => (s.MessageId ?? "").length > 0)).toBe(true);
    expect(bodiesOf(await collect(q.url, 2))).toEqual(["one", "two"]);
  });

  test("PublishBatch with more than 10 entries fails TooManyEntriesInBatchRequest", async () => {
    const t = await makeTopic();
    const entries = Array.from({ length: 11 }, (_, i) => ({ Id: `e${i}`, Message: "m" }));
    await expectFailure(sns.send(new PublishBatchCommand({ TopicArn: t.arn, PublishBatchRequestEntries: entries })), {
      name: "TooManyEntriesInBatchRequest",
      status: 400,
      messageIncludes: "The batch request contains more entries than permissible.",
    });
  });

  test("PublishBatch with duplicate entry ids fails BatchEntryIdsNotDistinct", async () => {
    const t = await makeTopic();
    await expectFailure(
      sns.send(
        new PublishBatchCommand({
          TopicArn: t.arn,
          PublishBatchRequestEntries: [
            { Id: "same", Message: "a" },
            { Id: "same", Message: "b" },
          ],
        }),
      ),
      {
        name: "BatchEntryIdsNotDistinct",
        status: 400,
        messageIncludes: "Two or more batch entries in the request have the same Id.",
      },
    );
  });

  test("PublishBatch with an invalid entry id fails InvalidBatchEntryId", async () => {
    const t = await makeTopic();
    await expectFailure(
      sns.send(new PublishBatchCommand({ TopicArn: t.arn, PublishBatchRequestEntries: [{ Id: "bad id!", Message: "a" }] })),
      { name: "InvalidBatchEntryId", status: 400 },
    );
  });

  test("PublishBatch entries whose bodies exceed the topic size in total fail BatchRequestTooLong", async () => {
    const t = await makeTopic();
    await expectFailure(
      sns.send(
        new PublishBatchCommand({
          TopicArn: t.arn,
          PublishBatchRequestEntries: [
            { Id: "a", Message: "a".repeat(131_072) },
            { Id: "b", Message: "b".repeat(131_073) },
          ],
        }),
      ),
      {
        name: "BatchRequestTooLong",
        status: 400,
        messageIncludes: "The length of all the messages put together is more than the limit.",
      },
    );
  });
});

describe("SNS FIFO topics", () => {
  test("a FIFO topic delivers to a FIFO queue with group and deduplication ids, a SequenceNumber and no signature", async () => {
    const t = await makeTopic({ fifo: true });
    const q = await makeQueue(true);
    await subscribeQueue(t.arn, q.arn);
    const sent = await sns.send(
      new PublishCommand({ TopicArn: t.arn, Message: "fifo body", MessageGroupId: "grp", MessageDeduplicationId: "dedup-1" }),
    );
    expect(sent.SequenceNumber).toMatch(/^\d+$/);
    const [message] = await collect(q.url, 1);
    expect(message?.Attributes).toMatchObject({ MessageGroupId: "grp", MessageDeduplicationId: "dedup-1" });
    const envelope = JSON.parse(message?.Body ?? "null");
    expect(envelope).toMatchObject({ Type: "Notification", TopicArn: t.arn, Message: "fifo body" });
    expect(envelope.SequenceNumber).toMatch(/^\d+$/);
    expect(envelope.Signature).toBeUndefined();
    expect(envelope.SignatureVersion).toBeUndefined();
    expect(envelope.SigningCertURL).toBeUndefined();
  });

  test("Publish to a FIFO topic without MessageGroupId fails InvalidParameter", async () => {
    const t = await makeTopic({ fifo: true });
    await expectFailure(sns.send(new PublishCommand({ TopicArn: t.arn, Message: "m", MessageDeduplicationId: "d" })), {
      name: "InvalidParameter",
      status: 400,
      messageIncludes: "Invalid parameter: The MessageGroupId parameter is required for FIFO topics",
    });
  });

  test("Publish to a FIFO topic without a deduplication id fails unless ContentBasedDeduplication is on", async () => {
    const t = await makeTopic({ fifo: true });
    await expectFailure(sns.send(new PublishCommand({ TopicArn: t.arn, Message: "m", MessageGroupId: "g" })), {
      name: "InvalidParameter",
      status: 400,
      messageIncludes: "MessageDeduplicationId",
    });
    const cb = await makeTopic({ fifo: true, attributes: { ContentBasedDeduplication: "true" } });
    const out = await sns.send(new PublishCommand({ TopicArn: cb.arn, Message: "m", MessageGroupId: "g" }));
    expect(out.MessageId).toBeDefined();
  });

  test("a duplicate deduplication id is accepted but delivered once", async () => {
    const t = await makeTopic({ fifo: true });
    const q = await makeQueue(true);
    await subscribeQueue(t.arn, q.arn, { RawMessageDelivery: "true" });
    const publish = () =>
      sns.send(new PublishCommand({ TopicArn: t.arn, Message: "same", MessageGroupId: "g", MessageDeduplicationId: "d" }));
    const first = await publish();
    const second = await publish();
    expect(first.MessageId).toBeDefined();
    expect(second.MessageId).toBeDefined();
    expect(bodiesOf(await collect(q.url, 1, 800))).toEqual(["same"]);
  });

  test("a standard queue subscribed to a FIFO topic receives the message without a group id", async () => {
    const t = await makeTopic({ fifo: true });
    const q = await makeQueue(false);
    await subscribeQueue(t.arn, q.arn);
    await sns.send(new PublishCommand({ TopicArn: t.arn, Message: "to standard", MessageGroupId: "g", MessageDeduplicationId: "d" }));
    const [message] = await collect(q.url, 1);
    expect(message?.Attributes?.MessageGroupId).toBeUndefined();
  });

  test("PublishBatch on a FIFO topic returns a SequenceNumber per entry", async () => {
    const t = await makeTopic({ fifo: true });
    const q = await makeQueue(true);
    await subscribeQueue(t.arn, q.arn, { RawMessageDelivery: "true" });
    const out = await sns.send(
      new PublishBatchCommand({
        TopicArn: t.arn,
        PublishBatchRequestEntries: [
          { Id: "a", Message: "one", MessageGroupId: "g", MessageDeduplicationId: "1" },
          { Id: "b", Message: "two", MessageGroupId: "g", MessageDeduplicationId: "2" },
        ],
      }),
    );
    expect(out.Successful?.map((s) => s.SequenceNumber)).toEqual([expect.stringMatching(/^\d+$/), expect.stringMatching(/^\d+$/)]);
    const got = await collect(q.url, 2);
    expect(got.map((m) => m.Body)).toEqual(["one", "two"]);
  });
});

describe("SNS filter policies", () => {
  type Attrs = Record<string, MessageAttributeValue>;
  const str = (value: string): MessageAttributeValue => ({ DataType: "String", StringValue: value });
  const num = (value: string): MessageAttributeValue => ({ DataType: "Number", StringValue: value });
  const arr = (values: unknown[]): MessageAttributeValue => ({ DataType: "String.Array", StringValue: JSON.stringify(values) });

  /**
   * Subscribes a raw queue with policy, publishes each case, then a sentinel the
   * policy cannot filter, and returns the bodies that arrived.
   */
  async function delivered(
    policy: unknown,
    cases: Array<{ body: string; attrs?: Attrs }>,
    options: { scope?: "MessageAttributes" | "MessageBody" } = {},
  ): Promise<string[]> {
    const t = await makeTopic();
    const q = await makeQueue();
    await subscribeQueue(t.arn, q.arn, {
      RawMessageDelivery: "true",
      FilterPolicy: JSON.stringify(policy),
      ...(options.scope === undefined ? {} : { FilterPolicyScope: options.scope }),
    });
    for (const c of cases) {
      await sns.send(new PublishCommand({ TopicArn: t.arn, Message: c.body, MessageAttributes: c.attrs }));
    }
    await sleep(600);
    return (await drain(q.url)).map((m) => m.Body ?? "").sort();
  }

  /** Reads whatever is in the queue now; two empty polls in a row end it. */
  async function drain(url: string): Promise<Message[]> {
    const got: Message[] = [];
    for (let emptyPolls = 0; emptyPolls < 2; ) {
      const out = await sqs.send(new ReceiveMessageCommand({ QueueUrl: url, MaxNumberOfMessages: 10, WaitTimeSeconds: 1 }));
      const messages = out.Messages ?? [];
      got.push(...messages);
      emptyPolls = messages.length === 0 ? emptyPolls + 1 : 0;
    }
    return got;
  }

  test("an exact string match delivers only matching messages", async () => {
    expect(
      await delivered({ store: ["example_corp"] }, [
        { body: "hit", attrs: { store: str("example_corp") } },
        { body: "miss", attrs: { store: str("other") } },
        { body: "absent" },
      ]),
    ).toEqual(["hit"]);
  });

  test("values within one key are OR, keys are AND", async () => {
    expect(
      await delivered({ store: ["a", "b"], event: ["order"] }, [
        { body: "a-order", attrs: { store: str("a"), event: str("order") } },
        { body: "b-order", attrs: { store: str("b"), event: str("order") } },
        { body: "a-other", attrs: { store: str("a"), event: str("other") } },
        { body: "c-order", attrs: { store: str("c"), event: str("order") } },
      ]),
    ).toEqual(["a-order", "b-order"]);
  });

  test("prefix, suffix and equals-ignore-case operators", async () => {
    expect(
      await delivered({ k: [{ prefix: "pre" }, { suffix: "post" }, { "equals-ignore-case": "MiXeD" }] }, [
        { body: "prefix", attrs: { k: str("prefix-1") } },
        { body: "suffix", attrs: { k: str("1-post") } },
        { body: "case", attrs: { k: str("mixed") } },
        { body: "none", attrs: { k: str("zzz") } },
      ]),
    ).toEqual(["case", "prefix", "suffix"]);
  });

  test("anything-but excludes the listed values but still needs the key present", async () => {
    expect(
      await delivered({ k: [{ "anything-but": ["x", "y"] }] }, [
        { body: "z", attrs: { k: str("z") } },
        { body: "x", attrs: { k: str("x") } },
        { body: "y", attrs: { k: str("y") } },
        { body: "absent" },
      ]),
    ).toEqual(["z"]);
  });

  test("numeric comparisons and ranges apply to Number attributes", async () => {
    expect(
      await delivered({ price: [{ numeric: [">", 0, "<=", 150] }] }, [
        { body: "100", attrs: { price: num("100") } },
        { body: "150", attrs: { price: num("150") } },
        { body: "0", attrs: { price: num("0") } },
        { body: "200", attrs: { price: num("200") } },
      ]),
    ).toEqual(["100", "150"]);
    expect(
      await delivered({ n: [{ numeric: ["=", 301.5] }] }, [
        { body: "eq", attrs: { n: num("301.5") } },
        { body: "ne", attrs: { n: num("301.6") } },
      ]),
    ).toEqual(["eq"]);
  });

  test("exists true and exists false", async () => {
    expect(
      await delivered({ k: [{ exists: true }] }, [{ body: "has", attrs: { k: str("v") } }, { body: "lacks", attrs: { other: str("v") } }]),
    ).toEqual(["has"]);
    expect(
      await delivered({ k: [{ exists: false }] }, [{ body: "has", attrs: { k: str("v"), other: str("1") } }, { body: "lacks", attrs: { other: str("v") } }]),
    ).toEqual(["lacks"]);
  });

  test("a String.Array attribute matches when any element matches", async () => {
    expect(
      await delivered({ sport: ["rugby", "tennis"] }, [
        { body: "match", attrs: { sport: arr(["tennis", "golf"]) } },
        { body: "miss", attrs: { sport: arr(["baseball"]) } },
      ]),
    ).toEqual(["match"]);
  });

  test("a message with no attributes does not match a policy that names a key", async () => {
    expect(await delivered({ k: [null, { "anything-but": "whatever" }] }, [{ body: "bare" }])).toEqual([]);
  });

  test("a subscription without a policy receives every message", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    await subscribeQueue(t.arn, q.arn, { RawMessageDelivery: "true" });
    await sns.send(new PublishCommand({ TopicArn: t.arn, Message: "any", MessageAttributes: { k: str("v") } }));
    expect(bodiesOf(await collect(q.url, 1))).toEqual(["any"]);
  });

  test("MessageBody scope matches nested JSON paths and treats arrays as transparent", async () => {
    expect(
      await delivered(
        { order: { status: ["paid"], tags: ["rush"] } },
        [
          { body: JSON.stringify({ order: { status: "paid", tags: ["normal", "rush"] } }) },
          { body: JSON.stringify({ order: { status: "open", tags: ["rush"] } }) },
          { body: JSON.stringify({ order: { status: "paid", tags: ["normal"] } }) },
        ],
        { scope: "MessageBody" },
      ),
    ).toEqual([JSON.stringify({ order: { status: "paid", tags: ["normal", "rush"] } })]);
  });

  test("MessageBody scope drops messages whose body is not a JSON object", async () => {
    expect(
      await delivered({ a: [{ exists: false }] }, [{ body: "plain string" }, { body: JSON.stringify({ b: 1 }) }], { scope: "MessageBody" }),
    ).toEqual([JSON.stringify({ b: 1 })]);
  });

  test("MessageBody scope distinguishes the JSON types true and the string 'true'", async () => {
    expect(
      await delivered({ flag: [true] }, [{ body: '{"flag":true}' }, { body: '{"flag":"true"}' }], { scope: "MessageBody" }),
    ).toEqual(['{"flag":true}']);
  });

  test("MessageBody scope supports numeric rules and wildcard and cidr", async () => {
    expect(
      await delivered({ n: [{ numeric: [">=", 10] }] }, [{ body: '{"n":10}' }, { body: '{"n":9}' }], { scope: "MessageBody" }),
    ).toEqual(['{"n":10}']);
    expect(
      await delivered({ f: [{ wildcard: "*.png" }] }, [{ body: '{"f":"a.png"}' }, { body: '{"f":"a.jpg"}' }], { scope: "MessageBody" }),
    ).toEqual(['{"f":"a.png"}']);
    expect(
      await delivered({ ip: [{ cidr: "10.0.0.0/24" }] }, [{ body: '{"ip":"10.0.0.7"}' }, { body: '{"ip":"10.0.1.7"}' }], { scope: "MessageBody" }),
    ).toEqual(['{"ip":"10.0.0.7"}']);
  });

  test("PublishBatch entries are filtered individually", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    await subscribeQueue(t.arn, q.arn, {
      RawMessageDelivery: "true",
      FilterPolicy: JSON.stringify({ attr1: [{ numeric: ["<", 100] }] }),
    });
    await sns.send(
      new PublishBatchCommand({
        TopicArn: t.arn,
        PublishBatchRequestEntries: [
          { Id: "a", Message: "low", MessageAttributes: { attr1: num("99") } },
          { Id: "b", Message: "high", MessageAttributes: { attr1: num("111") } },
        ],
      }),
    );
    expect(bodiesOf(await collect(q.url, 1))).toEqual(["low"]);
  });

  const invalidPolicies: Array<{ title: string; policy: string; detail: string }> = [
    { title: "an empty array", policy: '{"key":[]}', detail: "Empty arrays are not allowed" },
    { title: "a nested array value", policy: '{"key":[["value"]]}', detail: "Match value must be String, number, true, false, or null" },
    { title: "an unknown operator", policy: '{"key":[{"wrong-operator":true}]}', detail: "Unrecognized match type wrong-operator" },
    { title: "two keys in one match expression", policy: '{"key":[{"suffix":"v","prefix":"w"}]}', detail: "Only one key allowed in match expression" },
    { title: "a numeric prefix", policy: '{"key":[{"prefix":100}]}', detail: "prefix match pattern must be a string" },
    { title: "a non-boolean exists", policy: '{"key":[{"exists":"no"}]}', detail: "exists match pattern must be either true or false." },
    { title: "an unrecognized numeric range operator", policy: '{"key":[{"numeric":["operator"]}]}', detail: "Unrecognized numeric range operator: operator" },
    { title: "a numeric range with the bounds reversed", policy: '{"key":[{"numeric":[">",3,"<",1]}]}', detail: "Bottom must be less than top" },
    { title: "a CIDR without a mask", policy: '{"key":[{"cidr":"bad-filter"}]}', detail: "Malformed CIDR, one '/' required" },
    { title: "more than five keys", policy: JSON.stringify({ a: ["1"], b: ["1"], c: ["1"], d: ["1"], e: ["1"], f: ["1"] }), detail: "Filter policy can not have more than 5 keys" },
    {
      title: "more than 150 combinations",
      policy: JSON.stringify({
        a: Array.from({ length: 10 }, (_, i) => `a${i}`),
        b: Array.from({ length: 10 }, (_, i) => `b${i}`),
        c: Array.from({ length: 2 }, (_, i) => `c${i}`),
      }),
      detail: "Filter policy is too complex",
    },
  ];

  for (const c of invalidPolicies) {
    test(`SetSubscriptionAttributes rejects a FilterPolicy with ${c.title}`, async () => {
      const t = await makeTopic();
      const q = await makeQueue();
      const arn = await subscribeQueue(t.arn, q.arn);
      await expectFailure(
        sns.send(new SetSubscriptionAttributesCommand({ SubscriptionArn: arn, AttributeName: "FilterPolicy", AttributeValue: c.policy })),
        { name: "InvalidParameter", status: 400, messageIncludes: `Invalid parameter: FilterPolicy: ${c.detail}` },
      );
    });
  }

  test("Subscribe rejects an invalid FilterPolicy with the Attributes Reason prefix", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    await expectFailure(subscribeQueue(t.arn, q.arn, { FilterPolicy: '{"key":[]}' }), {
      name: "InvalidParameter",
      status: 400,
      messageIncludes: "Invalid parameter: Attributes Reason: FilterPolicy: Empty arrays are not allowed",
    });
  });

  test("a nested policy under MessageAttributes scope is rejected", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    const arn = await subscribeQueue(t.arn, q.arn);
    await expectFailure(
      sns.send(
        new SetSubscriptionAttributesCommand({
          SubscriptionArn: arn,
          AttributeName: "FilterPolicy",
          AttributeValue: '{"object":{"key":[{"prefix":"auto-"}]}}',
        }),
      ),
      { name: "InvalidParameter", status: 400, messageIncludes: "Filter policy scope MessageAttributes does not support nested filter policy" },
    );
  });
});

describe("SNS publish validation side effects", () => {
  test("a Publish with an invalid attribute name creates no SQS message", async () => {
    const t = await makeTopic();
    const q = await makeQueue();
    await subscribeQueue(t.arn, q.arn);
    const failure = await awsFailure(
      sns.send(new PublishCommand({ TopicArn: t.arn, Message: "m", MessageAttributes: { "bad name!": { DataType: "String", StringValue: "v" } } })),
    );
    expect(failure.status).toBe(400);
    await sleep(300);
    const out = await sqs.send(new GetQueueAttributesCommand({ QueueUrl: q.url, AttributeNames: ["ApproximateNumberOfMessages"] }));
    expect(out.Attributes?.ApproximateNumberOfMessages).toBe("0");
  });
});
