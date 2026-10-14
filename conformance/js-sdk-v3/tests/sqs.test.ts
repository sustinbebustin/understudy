// SQS over awsJson1_0 with awsQueryCompatible errors, through @aws-sdk/client-sqs.
// Expected behavior: docs/research/sqs.md.

import { describe, expect, test } from "bun:test";
import {
  AddPermissionCommand,
  CancelMessageMoveTaskCommand,
  ChangeMessageVisibilityBatchCommand,
  ChangeMessageVisibilityCommand,
  CreateQueueCommand,
  DeleteMessageBatchCommand,
  DeleteMessageCommand,
  DeleteQueueCommand,
  GetQueueAttributesCommand,
  GetQueueUrlCommand,
  ListDeadLetterSourceQueuesCommand,
  ListMessageMoveTasksCommand,
  ListQueueTagsCommand,
  ListQueuesCommand,
  PurgeQueueCommand,
  ReceiveMessageCommand,
  RemovePermissionCommand,
  SendMessageBatchCommand,
  SendMessageCommand,
  SetQueueAttributesCommand,
  SQSClient,
  StartMessageMoveTaskCommand,
  TagQueueCommand,
  UntagQueueCommand,
  type Message,
} from "@aws-sdk/client-sqs";
import { createHash } from "node:crypto";
import { clientConfig, defaultAccount, region } from "../support/env";
import { awsFailure, eventually, expectFailure, sleep, uniq } from "../support/helpers";

const sqs = new SQSClient(clientConfig());

async function makeQueue(
  attributes: Record<string, string> = {},
  options: { name?: string; tags?: Record<string, string> } = {},
): Promise<{ url: string; name: string; arn: string }> {
  const name = options.name ?? uniq("q");
  const created = await sqs.send(
    new CreateQueueCommand({
      QueueName: name,
      Attributes: attributes,
      tags: options.tags,
    }),
  );
  if (created.QueueUrl === undefined) throw new Error("CreateQueue returned no QueueUrl");
  return { url: created.QueueUrl, name, arn: `arn:aws:sqs:${region}:${defaultAccount}:${name}` };
}

async function attributes(url: string, names: string[] = ["All"]): Promise<Record<string, string>> {
  const out = await sqs.send(new GetQueueAttributesCommand({ QueueUrl: url, AttributeNames: names as never }));
  return out.Attributes ?? {};
}

async function receiveAll(url: string, options: { max?: number; wait?: number; visibility?: number } = {}) {
  const out = await sqs.send(
    new ReceiveMessageCommand({
      QueueUrl: url,
      MaxNumberOfMessages: options.max ?? 10,
      WaitTimeSeconds: options.wait ?? 0,
      VisibilityTimeout: options.visibility,
      MessageSystemAttributeNames: ["All"],
      MessageAttributeNames: ["All"],
    }),
  );
  return out.Messages ?? [];
}

const md5 = (text: string) => createHash("md5").update(text).digest("hex");

describe("SQS queues: control plane", () => {
  test("CreateQueue returns a queue URL whose path is /<account>/<name>", async () => {
    const q = await makeQueue();
    expect(new URL(q.url).pathname).toBe(`/${defaultAccount}/${q.name}`);
  });

  test("GetQueueUrl returns the URL CreateQueue returned", async () => {
    const q = await makeQueue();
    const out = await sqs.send(new GetQueueUrlCommand({ QueueName: q.name }));
    expect(out.QueueUrl).toBe(q.url);
  });

  test("GetQueueUrl on a missing queue fails QueueDoesNotExist with the NonExistentQueue query code", async () => {
    const failure = await expectFailure(sqs.send(new GetQueueUrlCommand({ QueueName: uniq("missing") })), {
      name: "QueueDoesNotExist",
      status: 400,
      messageIncludes: "The specified queue does not exist.",
    });
    expect(failure.code).toBe("AWS.SimpleQueueService.NonExistentQueue");
    expect(failure.fault).toBe("client");
  });

  test("a fresh standard queue reports exactly the documented attribute set and defaults", async () => {
    const q = await makeQueue();
    const attrs = await attributes(q.url);
    expect(Object.keys(attrs).sort()).toEqual(
      [
        "ApproximateNumberOfMessages",
        "ApproximateNumberOfMessagesDelayed",
        "ApproximateNumberOfMessagesNotVisible",
        "CreatedTimestamp",
        "DelaySeconds",
        "LastModifiedTimestamp",
        "MaximumMessageSize",
        "MessageRetentionPeriod",
        "QueueArn",
        "ReceiveMessageWaitTimeSeconds",
        "SqsManagedSseEnabled",
        "VisibilityTimeout",
      ].sort(),
    );
    expect(attrs).toMatchObject({
      QueueArn: q.arn,
      DelaySeconds: "0",
      MaximumMessageSize: "1048576",
      MessageRetentionPeriod: "345600",
      ReceiveMessageWaitTimeSeconds: "0",
      VisibilityTimeout: "30",
      SqsManagedSseEnabled: "true",
    });
    expect(attrs.CreatedTimestamp).toMatch(/^\d{10}$/);
  });

  test("GetQueueAttributes with no attribute names returns no attributes", async () => {
    const q = await makeQueue();
    const out = await sqs.send(new GetQueueAttributesCommand({ QueueUrl: q.url }));
    expect(out.Attributes ?? {}).toEqual({});
  });

  test("GetQueueAttributes returns only the attributes named", async () => {
    const q = await makeQueue({ VisibilityTimeout: "40" });
    expect(await attributes(q.url, ["VisibilityTimeout", "QueueArn"])).toEqual({
      VisibilityTimeout: "40",
      QueueArn: q.arn,
    });
  });

  test("GetQueueAttributes with an unknown attribute name fails InvalidAttributeName", async () => {
    const q = await makeQueue();
    await expectFailure(attributes(q.url, ["Foobar"]), {
      name: "InvalidAttributeName",
      status: 400,
      messageIncludes: "Unknown Attribute Foobar.",
    });
  });

  test("CreateQueue with an existing name and identical attributes returns the same URL", async () => {
    const q = await makeQueue({ VisibilityTimeout: "45" });
    const again = await sqs.send(
      new CreateQueueCommand({ QueueName: q.name, Attributes: { VisibilityTimeout: "45" } }),
    );
    expect(again.QueueUrl).toBe(q.url);
  });

  test("CreateQueue with an existing name and no attributes returns the same URL", async () => {
    const q = await makeQueue({ VisibilityTimeout: "45" });
    const again = await sqs.send(new CreateQueueCommand({ QueueName: q.name }));
    expect(again.QueueUrl).toBe(q.url);
  });

  test("CreateQueue with an existing name and a different attribute value fails QueueAlreadyExists", async () => {
    const q = await makeQueue({ DelaySeconds: "5" });
    const failure = await expectFailure(
      sqs.send(new CreateQueueCommand({ QueueName: q.name, Attributes: { DelaySeconds: "6" } })),
      {
        name: "QueueNameExists",
        status: 400,
        messageIncludes: "A queue already exists with the same name and a different value for attribute DelaySeconds",
      },
    );
    expect(failure.code).toBe("QueueAlreadyExists");
  });

  test("CreateQueue rejects a name with characters outside [A-Za-z0-9_-]", async () => {
    await expectFailure(sqs.send(new CreateQueueCommand({ QueueName: "bad.name!" })), {
      name: "InvalidParameterValue",
      status: 400,
    });
  });

  test("CreateQueue rejects a standard queue name ending in .fifo", async () => {
    await expectFailure(sqs.send(new CreateQueueCommand({ QueueName: `${uniq("std")}.fifo` })), {
      name: "InvalidParameterValue",
      status: 400,
    });
  });

  test("CreateQueue rejects an unknown attribute name", async () => {
    const attributes: Record<string, string> = { Bogus: "1" };
    await expectFailure(sqs.send(new CreateQueueCommand({ QueueName: uniq("q"), Attributes: attributes })), {
      name: "InvalidAttributeName",
      status: 400,
    });
  });

  test("CreateQueue rejects an out-of-range attribute value", async () => {
    await expectFailure(
      sqs.send(new CreateQueueCommand({ QueueName: uniq("q"), Attributes: { DelaySeconds: "901" } })),
      { name: "InvalidAttributeValue", status: 400 },
    );
  });

  test("SetQueueAttributes changes a mutable attribute and moves LastModifiedTimestamp forward", async () => {
    const q = await makeQueue();
    const before = await attributes(q.url, ["LastModifiedTimestamp"]);
    await sleep(1100);
    await sqs.send(
      new SetQueueAttributesCommand({ QueueUrl: q.url, Attributes: { VisibilityTimeout: "60", DelaySeconds: "3" } }),
    );
    const after = await attributes(q.url);
    expect(after.VisibilityTimeout).toBe("60");
    expect(after.DelaySeconds).toBe("3");
    expect(Number(after.LastModifiedTimestamp)).toBeGreaterThan(Number(before.LastModifiedTimestamp));
  });

  test("SetQueueAttributes rejects a read-only attribute with InvalidAttributeName", async () => {
    const q = await makeQueue();
    await expectFailure(
      sqs.send(new SetQueueAttributesCommand({ QueueUrl: q.url, Attributes: { QueueArn: "arn:aws:sqs:x:y:z" } })),
      { name: "InvalidAttributeName", status: 400 },
    );
  });

  test("SetQueueAttributes rejects FifoQueue on a standard queue with Unknown Attribute FifoQueue.", async () => {
    const q = await makeQueue();
    await expectFailure(
      sqs.send(new SetQueueAttributesCommand({ QueueUrl: q.url, Attributes: { FifoQueue: "true" } })),
      { name: "InvalidAttributeName", status: 400, messageIncludes: "Unknown Attribute FifoQueue." },
    );
  });

  test("a queue Policy set through SetQueueAttributes is returned and an empty string clears it", async () => {
    const q = await makeQueue();
    const policy = JSON.stringify({
      Version: "2012-10-17",
      Statement: [
        {
          Sid: "allow",
          Effect: "Allow",
          Principal: { AWS: `arn:aws:iam::${defaultAccount}:root` },
          Action: "sqs:SendMessage",
          Resource: q.arn,
        },
      ],
    });
    await sqs.send(new SetQueueAttributesCommand({ QueueUrl: q.url, Attributes: { Policy: policy } }));
    expect(JSON.parse((await attributes(q.url, ["Policy"])).Policy ?? "null")).toEqual(JSON.parse(policy));
    await sqs.send(new SetQueueAttributesCommand({ QueueUrl: q.url, Attributes: { Policy: "" } }));
    expect(await attributes(q.url, ["Policy"])).toEqual({});
  });

  test("ListQueues filters by a case-sensitive name prefix", async () => {
    const prefix = uniq("listpfx");
    const a = await makeQueue({}, { name: `${prefix}-a` });
    const b = await makeQueue({}, { name: `${prefix}-b` });
    await makeQueue({}, { name: `other-${prefix}` });
    const out = await sqs.send(new ListQueuesCommand({ QueueNamePrefix: prefix }));
    expect([...(out.QueueUrls ?? [])].sort()).toEqual([a.url, b.url].sort());
    const upper = await sqs.send(new ListQueuesCommand({ QueueNamePrefix: prefix.toUpperCase() }));
    expect(upper.QueueUrls).toBeUndefined();
  });

  test("ListQueues paginates with MaxResults and NextToken without repeating a queue", async () => {
    const prefix = uniq("page");
    const created = await Promise.all([1, 2, 3].map((i) => makeQueue({}, { name: `${prefix}-${i}` })));
    const seen: string[] = [];
    let token: string | undefined;
    let pages = 0;
    do {
      const out = await sqs.send(new ListQueuesCommand({ QueueNamePrefix: prefix, MaxResults: 2, NextToken: token }));
      expect((out.QueueUrls ?? []).length).toBeLessThanOrEqual(2);
      seen.push(...(out.QueueUrls ?? []));
      token = out.NextToken;
      pages += 1;
    } while (token !== undefined && pages < 5);
    expect(pages).toBe(2);
    expect(seen.sort()).toEqual(created.map((q) => q.url).sort());
  });

  test("ListQueues with no match returns no QueueUrls and no NextToken", async () => {
    const out = await sqs.send(new ListQueuesCommand({ QueueNamePrefix: uniq("nomatch") }));
    expect(out.QueueUrls).toBeUndefined();
    expect(out.NextToken).toBeUndefined();
  });

  test("tags set at creation are listed, TagQueue merges, UntagQueue ignores unknown keys", async () => {
    const q = await makeQueue({}, { tags: { env: "test", Owner: "a" } });
    expect((await sqs.send(new ListQueueTagsCommand({ QueueUrl: q.url }))).Tags).toEqual({ env: "test", Owner: "a" });
    await sqs.send(new TagQueueCommand({ QueueUrl: q.url, Tags: { env: "prod", empty: "" } }));
    expect((await sqs.send(new ListQueueTagsCommand({ QueueUrl: q.url }))).Tags).toEqual({
      env: "prod",
      Owner: "a",
      empty: "",
    });
    await sqs.send(new UntagQueueCommand({ QueueUrl: q.url, TagKeys: ["Owner", "never-set"] }));
    expect((await sqs.send(new ListQueueTagsCommand({ QueueUrl: q.url }))).Tags).toEqual({ env: "prod", empty: "" });
  });

  test("tag keys are case-sensitive", async () => {
    const q = await makeQueue({}, { tags: { key: "1" } });
    await sqs.send(new TagQueueCommand({ QueueUrl: q.url, Tags: { Key: "2" } }));
    expect((await sqs.send(new ListQueueTagsCommand({ QueueUrl: q.url }))).Tags).toEqual({ key: "1", Key: "2" });
  });

  test("ListQueueTags on an untagged queue returns no Tags member", async () => {
    const q = await makeQueue();
    expect((await sqs.send(new ListQueueTagsCommand({ QueueUrl: q.url }))).Tags).toBeUndefined();
  });

  test("AddPermission stores a statement in the recorded form and RemovePermission deletes the Policy", async () => {
    const q = await makeQueue();
    await sqs.send(
      new AddPermissionCommand({
        QueueUrl: q.url,
        Label: "crossaccountpermission",
        AWSAccountIds: ["111122223333", "444455556666"],
        Actions: ["ReceiveMessage"],
      }),
    );
    const policy = JSON.parse((await attributes(q.url, ["Policy"])).Policy ?? "null");
    expect(policy).toEqual({
      Version: "2008-10-17",
      Id: `${q.arn}/SQSDefaultPolicy`,
      Statement: [
        {
          Sid: "crossaccountpermission",
          Effect: "Allow",
          Principal: { AWS: ["arn:aws:iam::111122223333:root", "arn:aws:iam::444455556666:root"] },
          Action: "SQS:ReceiveMessage",
          Resource: q.arn,
        },
      ],
    });
    await sqs.send(new RemovePermissionCommand({ QueueUrl: q.url, Label: "crossaccountpermission" }));
    expect(await attributes(q.url, ["Policy"])).toEqual({});
  });

  test("AddPermission with a duplicate label fails InvalidParameterValue", async () => {
    const q = await makeQueue();
    const input = { QueueUrl: q.url, Label: "dup", AWSAccountIds: ["111122223333"], Actions: ["SendMessage"] };
    await sqs.send(new AddPermissionCommand(input));
    await expectFailure(sqs.send(new AddPermissionCommand(input)), {
      name: "InvalidParameterValue",
      status: 400,
      messageIncludes: "Value dup for parameter Label is invalid. Reason: Already exists.",
    });
  });

  test("RemovePermission with an unknown label fails InvalidParameterValue", async () => {
    const q = await makeQueue();
    await expectFailure(sqs.send(new RemovePermissionCommand({ QueueUrl: q.url, Label: "nolabel" })), {
      name: "InvalidParameterValue",
      status: 400,
      messageIncludes: "can't find label",
    });
  });

  test("DeleteQueue makes the queue unreachable and blocks re-creation for 60 seconds", async () => {
    const q = await makeQueue();
    await sqs.send(new DeleteQueueCommand({ QueueUrl: q.url }));
    await expectFailure(sqs.send(new GetQueueUrlCommand({ QueueName: q.name })), { name: "QueueDoesNotExist" });
    const failure = await expectFailure(sqs.send(new CreateQueueCommand({ QueueName: q.name })), {
      name: "QueueDeletedRecently",
      status: 400,
      messageIncludes: "You must wait 60 seconds after deleting a queue before you can create another with the same name.",
    });
    expect(failure.code).toBe("AWS.SimpleQueueService.QueueDeletedRecently");
  });

  test("operations on a deleted queue URL fail QueueDoesNotExist", async () => {
    const q = await makeQueue();
    await sqs.send(new DeleteQueueCommand({ QueueUrl: q.url }));
    await expectFailure(sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "x" })), {
      name: "QueueDoesNotExist",
      status: 400,
    });
  });

  test("a second PurgeQueue within 60 seconds fails PurgeQueueInProgress with status 403", async () => {
    const q = await makeQueue();
    await sqs.send(new PurgeQueueCommand({ QueueUrl: q.url }));
    const failure = await expectFailure(sqs.send(new PurgeQueueCommand({ QueueUrl: q.url })), {
      name: "PurgeQueueInProgress",
      status: 403,
      messageIncludes: `Only one PurgeQueue operation on ${q.name} is allowed every 60 seconds.`,
    });
    expect(failure.code).toBe("AWS.SimpleQueueService.PurgeQueueInProgress");
  });

  test("PurgeQueue removes visible, in-flight and delayed messages", async () => {
    const q = await makeQueue();
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "visible" }));
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "inflight" }));
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "delayed", DelaySeconds: 60 }));
    expect(await receiveAll(q.url, { max: 1 })).toHaveLength(1);
    await sqs.send(new PurgeQueueCommand({ QueueUrl: q.url }));
    const attrs = await attributes(q.url);
    expect(attrs.ApproximateNumberOfMessages).toBe("0");
    expect(attrs.ApproximateNumberOfMessagesNotVisible).toBe("0");
    expect(attrs.ApproximateNumberOfMessagesDelayed).toBe("0");
  });
});

describe("SQS send and receive", () => {
  test("SendMessage returns the MD5 of the body and a message id", async () => {
    const q = await makeQueue();
    const out = await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "message" }));
    expect(out.MD5OfMessageBody).toBe("78e731027d8fd50ed642340b7c9a63b3");
    expect(out.MessageId).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
    expect(out.MD5OfMessageAttributes).toBeUndefined();
    expect(out.SequenceNumber).toBeUndefined();
  });

  test("ReceiveMessage returns the body byte-exact with the same id and MD5", async () => {
    const q = await makeQueue();
    const body = 'tab\t crlf\r\n <xml a="1">&amp; é中\u{1F600}';
    const sent = await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: body }));
    const [message] = await receiveAll(q.url);
    expect(message?.Body).toBe(body);
    expect(message?.MessageId).toBe(sent.MessageId);
    expect(message?.MD5OfBody).toBe(md5(body));
    expect(message?.ReceiptHandle?.length ?? 0).toBeGreaterThan(0);
  });

  test("ReceiveMessage on an empty queue returns no Messages", async () => {
    const q = await makeQueue();
    const out = await sqs.send(new ReceiveMessageCommand({ QueueUrl: q.url }));
    expect(out.Messages).toBeUndefined();
  });

  test("SendMessage with an empty body fails MissingParameter", async () => {
    const q = await makeQueue();
    await expectFailure(sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "" })), {
      name: "MissingParameter",
      status: 400,
      messageIncludes: "The request must contain the parameter MessageBody.",
    });
  });

  test("SendMessage with an invalid control character fails InvalidMessageContents", async () => {
    const q = await makeQueue();
    await expectFailure(sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "bad\u0001body" })), {
      name: "InvalidMessageContents",
      status: 400,
    });
  });

  test("SendMessage over the queue MaximumMessageSize fails InvalidParameterValue", async () => {
    const q = await makeQueue({ MaximumMessageSize: "1024" });
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "a".repeat(1024) }));
    await expectFailure(sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "a".repeat(1025) })), {
      name: "InvalidParameterValue",
      status: 400,
      messageIncludes: "Message must be shorter than 1024 bytes.",
    });
  });

  test("message attribute size counts toward MaximumMessageSize", async () => {
    const q = await makeQueue({ MaximumMessageSize: "1024" });
    // name "k" (1) + type "String" (6) + value "x" (1) = 8 bytes of attribute.
    await sqs.send(
      new SendMessageCommand({
        QueueUrl: q.url,
        MessageBody: "a".repeat(1016),
        MessageAttributes: { k: { DataType: "String", StringValue: "x" } },
      }),
    );
    await expectFailure(
      sqs.send(
        new SendMessageCommand({
          QueueUrl: q.url,
          MessageBody: "a".repeat(1017),
          MessageAttributes: { k: { DataType: "String", StringValue: "x" } },
        }),
      ),
      { name: "InvalidParameterValue", status: 400 },
    );
  });

  test("ReceiveMessage with MaxNumberOfMessages above 10 fails with the documented message", async () => {
    const q = await makeQueue();
    await expectFailure(sqs.send(new ReceiveMessageCommand({ QueueUrl: q.url, MaxNumberOfMessages: 11 })), {
      name: "InvalidParameterValue",
      status: 400,
      messageIncludes: "Value 11 for parameter MaxNumberOfMessages is invalid. Reason: Must be between 1 and 10, if provided.",
    });
  });

  test("ReceiveMessage with WaitTimeSeconds above 20 fails with the documented message", async () => {
    const q = await makeQueue();
    await expectFailure(sqs.send(new ReceiveMessageCommand({ QueueUrl: q.url, WaitTimeSeconds: 21 })), {
      name: "InvalidParameterValue",
      status: 400,
      messageIncludes: "Value 21 for parameter WaitTimeSeconds is invalid. Reason: Must be >= 0 and <= 20, if provided.",
    });
  });

  test("ReceiveMessage never returns more than MaxNumberOfMessages and returns the rest next", async () => {
    const q = await makeQueue();
    for (let i = 0; i < 5; i += 1) {
      await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: `m${i}` }));
    }
    const first = await receiveAll(q.url, { max: 3 });
    expect(first.length).toBeLessThanOrEqual(3);
    expect(first.length).toBeGreaterThan(0);
    const seen = new Set(first.map((m) => m.Body));
    // Short polling may return fewer messages per call; drain until all five were seen.
    await eventually("all five messages received", async () => {
      for (const m of await receiveAll(q.url, { max: 10 })) seen.add(m.Body);
      return seen.size === 5 ? true : undefined;
    });
    expect([...seen].sort()).toEqual(["m0", "m1", "m2", "m3", "m4"]);
  });

  test("system attributes: SenderId, SentTimestamp, ApproximateReceiveCount and ApproximateFirstReceiveTimestamp", async () => {
    const q = await makeQueue();
    const before = Date.now();
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "sys" }));
    const [message] = await receiveAll(q.url);
    const attrs = message?.Attributes ?? {};
    expect(attrs.ApproximateReceiveCount).toBe("1");
    expect(attrs.SenderId?.length ?? 0).toBeGreaterThan(0);
    expect(Number(attrs.SentTimestamp)).toBeGreaterThanOrEqual(before - 2000);
    expect(Number(attrs.SentTimestamp)).toBeLessThanOrEqual(Date.now() + 2000);
    expect(attrs.SentTimestamp).toMatch(/^\d{13}$/);
    expect(attrs.ApproximateFirstReceiveTimestamp).toMatch(/^\d{13}$/);
    expect(Number(attrs.ApproximateFirstReceiveTimestamp)).toBeGreaterThanOrEqual(Number(attrs.SentTimestamp));
  });

  test("no system attributes are returned unless requested", async () => {
    const q = await makeQueue();
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "x" }));
    const out = await sqs.send(new ReceiveMessageCommand({ QueueUrl: q.url }));
    expect(out.Messages?.[0]?.Attributes).toBeUndefined();
  });

  test("requesting a named system attribute returns only that attribute", async () => {
    const q = await makeQueue();
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "x" }));
    const out = await sqs.send(
      new ReceiveMessageCommand({ QueueUrl: q.url, MessageSystemAttributeNames: ["SenderId", "SequenceNumber"] }),
    );
    expect(Object.keys(out.Messages?.[0]?.Attributes ?? {})).toEqual(["SenderId"]);
  });

  test("ReceiveMessage with a VisibilityTimeout hides the message then redelivers it with a new receipt handle and count 2", async () => {
    const q = await makeQueue();
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "again" }));
    const [first] = await receiveAll(q.url, { visibility: 1 });
    expect(first?.Body).toBe("again");
    expect(await receiveAll(q.url)).toHaveLength(0);
    const second = await eventually("redelivery after the visibility timeout", async () => {
      const [m] = await receiveAll(q.url, { visibility: 30 });
      return m;
    });
    expect(second.MessageId).toBe(first?.MessageId);
    expect(second.ReceiptHandle).not.toBe(first?.ReceiptHandle);
    expect(second.Attributes?.ApproximateReceiveCount).toBe("2");
  });

  test("the queue VisibilityTimeout applies when the receive names none", async () => {
    const q = await makeQueue({ VisibilityTimeout: "1" });
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "q-vis" }));
    expect(await receiveAll(q.url)).toHaveLength(1);
    expect(await receiveAll(q.url)).toHaveLength(0);
    await eventually("redelivery after the queue visibility timeout", async () => {
      const [m] = await receiveAll(q.url);
      return m;
    });
  });

  test("a receive VisibilityTimeout of 0 leaves the message immediately re-receivable and still counts the receive", async () => {
    const q = await makeQueue();
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "zero" }));
    const [first] = await receiveAll(q.url, { visibility: 0 });
    const [second] = await receiveAll(q.url, { visibility: 0 });
    expect(second?.MessageId).toBe(first?.MessageId);
    expect(second?.ReceiptHandle).not.toBe(first?.ReceiptHandle);
    expect(second?.Attributes?.ApproximateReceiveCount).toBe("2");
  });

  test("ApproximateNumberOfMessagesNotVisible counts in-flight messages", async () => {
    const q = await makeQueue();
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "a" }));
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "b" }));
    await receiveAll(q.url, { max: 1 });
    const attrs = await attributes(q.url);
    expect(attrs.ApproximateNumberOfMessages).toBe("1");
    expect(attrs.ApproximateNumberOfMessagesNotVisible).toBe("1");
  });

  test("DeleteMessage removes the message permanently and a repeated delete succeeds", async () => {
    const q = await makeQueue();
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "del" }));
    const [message] = await receiveAll(q.url, { visibility: 1 });
    await sqs.send(new DeleteMessageCommand({ QueueUrl: q.url, ReceiptHandle: message?.ReceiptHandle }));
    await sqs.send(new DeleteMessageCommand({ QueueUrl: q.url, ReceiptHandle: message?.ReceiptHandle }));
    await sleep(1300);
    expect(await receiveAll(q.url)).toHaveLength(0);
    expect((await attributes(q.url)).ApproximateNumberOfMessages).toBe("0");
  });

  test("DeleteMessage with a malformed receipt handle fails ReceiptHandleIsInvalid", async () => {
    const q = await makeQueue();
    const failure = await expectFailure(
      sqs.send(new DeleteMessageCommand({ QueueUrl: q.url, ReceiptHandle: "garbage" })),
      { name: "ReceiptHandleIsInvalid", status: 404, messageIncludes: 'The input receipt handle "garbage" is not a valid receipt handle.' },
    );
    expect(failure.code).toBe("ReceiptHandleIsInvalid");
  });

  test("ChangeMessageVisibility to 0 makes the message receivable again at once", async () => {
    const q = await makeQueue({ VisibilityTimeout: "60" });
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "cmv0" }));
    const [first] = await receiveAll(q.url);
    expect(await receiveAll(q.url)).toHaveLength(0);
    await sqs.send(
      new ChangeMessageVisibilityCommand({ QueueUrl: q.url, ReceiptHandle: first?.ReceiptHandle, VisibilityTimeout: 0 }),
    );
    const [again] = await receiveAll(q.url);
    expect(again?.MessageId).toBe(first?.MessageId);
  });

  test("ChangeMessageVisibility extends the timeout past the original one", async () => {
    const q = await makeQueue();
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "cmv-ext" }));
    const [first] = await receiveAll(q.url, { visibility: 1 });
    await sqs.send(
      new ChangeMessageVisibilityCommand({ QueueUrl: q.url, ReceiptHandle: first?.ReceiptHandle, VisibilityTimeout: 30 }),
    );
    await sleep(1500);
    expect(await receiveAll(q.url)).toHaveLength(0);
  });

  test("ChangeMessageVisibility with a malformed handle fails ReceiptHandleIsInvalid", async () => {
    const q = await makeQueue();
    await expectFailure(
      sqs.send(new ChangeMessageVisibilityCommand({ QueueUrl: q.url, ReceiptHandle: "garbage", VisibilityTimeout: 5 })),
      { name: "ReceiptHandleIsInvalid", status: 404 },
    );
  });

  test("ChangeMessageVisibility above 43200 seconds is rejected", async () => {
    const q = await makeQueue();
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "cmv-big" }));
    const [m] = await receiveAll(q.url);
    await expectFailure(
      sqs.send(new ChangeMessageVisibilityCommand({ QueueUrl: q.url, ReceiptHandle: m?.ReceiptHandle, VisibilityTimeout: 43201 })),
      { name: "InvalidParameterValue", status: 400 },
    );
  });

  test("long polling returns as soon as a message arrives, well before WaitTimeSeconds", async () => {
    const q = await makeQueue();
    const started = Date.now();
    setTimeout(() => {
      void sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "late" }));
    }, 700);
    const out = await sqs.send(new ReceiveMessageCommand({ QueueUrl: q.url, WaitTimeSeconds: 10 }));
    const elapsed = Date.now() - started;
    expect(out.Messages?.[0]?.Body).toBe("late");
    expect(elapsed).toBeGreaterThanOrEqual(600);
    expect(elapsed).toBeLessThan(5000);
  });

  test("long polling an empty queue waits for WaitTimeSeconds then returns no messages", async () => {
    const q = await makeQueue();
    const started = Date.now();
    const out = await sqs.send(new ReceiveMessageCommand({ QueueUrl: q.url, WaitTimeSeconds: 2 }));
    const elapsed = Date.now() - started;
    expect(out.Messages).toBeUndefined();
    expect(elapsed).toBeGreaterThanOrEqual(1800);
    expect(elapsed).toBeLessThan(4500);
  });

  test("the queue ReceiveMessageWaitTimeSeconds makes a plain receive long-poll, and WaitTimeSeconds 0 overrides it", async () => {
    const q = await makeQueue({ ReceiveMessageWaitTimeSeconds: "2" });
    let started = Date.now();
    await sqs.send(new ReceiveMessageCommand({ QueueUrl: q.url }));
    expect(Date.now() - started).toBeGreaterThanOrEqual(1800);
    started = Date.now();
    await sqs.send(new ReceiveMessageCommand({ QueueUrl: q.url, WaitTimeSeconds: 0 }));
    expect(Date.now() - started).toBeLessThan(1000);
  });

  test("a per-message DelaySeconds hides the message and counts it as delayed", async () => {
    const q = await makeQueue();
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "delayed", DelaySeconds: 2 }));
    expect(await receiveAll(q.url)).toHaveLength(0);
    const attrs = await attributes(q.url);
    expect(attrs.ApproximateNumberOfMessagesDelayed).toBe("1");
    expect(attrs.ApproximateNumberOfMessages).toBe("0");
    await eventually("the delayed message becomes visible", async () => {
      const [m] = await receiveAll(q.url);
      return m;
    }, { timeoutMs: 6000 });
  });

  test("the queue DelaySeconds delays every message and an explicit per-message 0 overrides it", async () => {
    const q = await makeQueue({ DelaySeconds: "5" });
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "queue-delayed" }));
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "immediate", DelaySeconds: 0 }));
    const bodies = (await receiveAll(q.url)).map((m) => m.Body);
    expect(bodies).toEqual(["immediate"]);
  });

  test("SendMessage rejects DelaySeconds above 900", async () => {
    const q = await makeQueue();
    await expectFailure(sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "x", DelaySeconds: 901 })), {
      name: "InvalidParameterValue",
      status: 400,
    });
  });
});

describe("SQS message attributes", () => {
  // MD5 vectors recorded from AWS (sqs.md 6.3).
  const vectors: Array<{ title: string; attrs: Record<string, { DataType: string; StringValue?: string; BinaryValue?: Uint8Array }>; md5: string }> = [
    { title: "one String attribute", attrs: { k: { DataType: "String", StringValue: "x" } }, md5: "a45daa70926828a2f0a1db3418e6feb4" },
    {
      title: "MyAttribute String",
      attrs: { MyAttribute: { DataType: "String", StringValue: "foobar" } },
      md5: "adb59cd4678ea5a855436b949cd07ab6",
    },
    {
      title: "three String attributes sorted by name",
      attrs: {
        Hello: { DataType: "String", StringValue: "There" },
        General: { DataType: "String", StringValue: "Kenobi" },
        "Help.Me": { DataType: "String", StringValue: "Me" },
      },
      md5: "4c360f3fdafd970e05fae2f149d997f5",
    },
    {
      title: "Number attribute",
      attrs: { timestamp: { DataType: "Number", StringValue: "1493147359900" } },
      md5: "235c5c510d26fb653d073faed50ae77c",
    },
    {
      title: "Binary attribute",
      attrs: {
        attr1: {
          DataType: "Binary",
          BinaryValue: new TextEncoder().encode("traceparent\x1e00-774062d6c37081a5a0b9b5b88e30627c-2d2482211f6489da-01"),
        },
      },
      md5: "8cbe4db156db8a94db8b801b7addb984",
    },
  ];

  for (const vector of vectors) {
    test(`MD5OfMessageAttributes matches AWS for ${vector.title}`, async () => {
      const q = await makeQueue();
      const sent = await sqs.send(
        new SendMessageCommand({ QueueUrl: q.url, MessageBody: "attrs", MessageAttributes: vector.attrs }),
      );
      expect(sent.MD5OfMessageAttributes).toBe(vector.md5);
      const [received] = await receiveAll(q.url);
      expect(received?.MD5OfMessageAttributes).toBe(vector.md5);
    });
  }

  test("received message attributes keep their data type and value", async () => {
    const q = await makeQueue();
    await sqs.send(
      new SendMessageCommand({
        QueueUrl: q.url,
        MessageBody: "typed",
        MessageAttributes: {
          s: { DataType: "String", StringValue: "text" },
          n: { DataType: "Number.int", StringValue: "42" },
          b: { DataType: "Binary", BinaryValue: new Uint8Array([1, 2, 3]) },
        },
      }),
    );
    const [m] = await receiveAll(q.url);
    expect(m?.MessageAttributes?.s).toMatchObject({ DataType: "String", StringValue: "text" });
    expect(m?.MessageAttributes?.n).toMatchObject({ DataType: "Number.int", StringValue: "42" });
    expect(Array.from(m?.MessageAttributes?.b?.BinaryValue ?? [])).toEqual([1, 2, 3]);
  });

  describe("MessageAttributeNames", () => {
    const attrs = {
      Hello: { DataType: "String", StringValue: "There" },
      "Help.Me": { DataType: "String", StringValue: "Me" },
      General: { DataType: "String", StringValue: "Kenobi" },
    };
    const selected = async (selector: string[] | undefined) => {
      const q = await makeQueue();
      await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "sel", MessageAttributes: attrs }));
      const out = await sqs.send(new ReceiveMessageCommand({ QueueUrl: q.url, MessageAttributeNames: selector }));
      return out.Messages?.[0];
    };

    test("All returns every attribute", async () => {
      const m = await selected(["All"]);
      expect(Object.keys(m?.MessageAttributes ?? {}).sort()).toEqual(["General", "Hello", "Help.Me"]);
    });

    test("a Prefix.* pattern returns the attributes with that prefix", async () => {
      const m = await selected(["Hel.*"]);
      expect(Object.keys(m?.MessageAttributes ?? {}).sort()).toEqual(["Hello", "Help.Me"]);
    });

    test("naming none returns no attributes and no attribute MD5", async () => {
      const m = await selected(undefined);
      expect(m?.MessageAttributes).toBeUndefined();
      expect(m?.MD5OfMessageAttributes).toBeUndefined();
    });
  });

  test("a message attribute name starting with AWS. is rejected", async () => {
    const q = await makeQueue();
    await expectFailure(
      sqs.send(
        new SendMessageCommand({
          QueueUrl: q.url,
          MessageBody: "x",
          MessageAttributes: { "AWS.bad": { DataType: "String", StringValue: "v" } },
        }),
      ),
      { name: "InvalidParameterValue", status: 400 },
    );
  });

  test("a message attribute with an unsupported data type is rejected", async () => {
    const q = await makeQueue();
    await expectFailure(
      sqs.send(
        new SendMessageCommand({
          QueueUrl: q.url,
          MessageBody: "x",
          MessageAttributes: { a: { DataType: "Invalid", StringValue: "v" } },
        }),
      ),
      { name: "InvalidParameterValue", status: 400 },
    );
  });

  test("an empty String attribute value is rejected with the documented message", async () => {
    const q = await makeQueue();
    await expectFailure(
      sqs.send(
        new SendMessageCommand({
          QueueUrl: q.url,
          MessageBody: "x",
          MessageAttributes: { ErrorDetails: { DataType: "String", StringValue: "" } },
        }),
      ),
      {
        name: "InvalidParameterValue",
        status: 400,
        messageIncludes: "Message (user) attribute 'ErrorDetails' must contain a non-empty value of type 'String'.",
      },
    );
  });

  test("more than 10 message attributes are rejected", async () => {
    const q = await makeQueue();
    const attrs: Record<string, { DataType: string; StringValue: string }> = {};
    for (let i = 0; i < 11; i += 1) attrs[`a${i}`] = { DataType: "String", StringValue: "v" };
    await expectFailure(
      sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "x", MessageAttributes: attrs })),
      { name: "InvalidParameterValue", status: 400 },
    );
  });

  test("the AWSTraceHeader system attribute is stored, MD5'd and returned", async () => {
    const q = await makeQueue();
    const header = "Root=1-5759e988-bd862e3fe1be46a994272793;Parent=53995c3f42cd8ad8;Sampled=1";
    const sent = await sqs.send(
      new SendMessageCommand({
        QueueUrl: q.url,
        MessageBody: "trace",
        MessageSystemAttributes: { AWSTraceHeader: { DataType: "String", StringValue: header } },
      }),
    );
    expect(sent.MD5OfMessageSystemAttributes).toBe("5ae4d5d7636402d80f4eb6d213245a88");
    const [m] = await receiveAll(q.url);
    expect(m?.Attributes?.AWSTraceHeader).toBe(header);
  });
});

describe("SQS batch operations", () => {
  test("SendMessageBatch reports each entry in Successful with the MD5 of its body", async () => {
    const q = await makeQueue();
    const out = await sqs.send(
      new SendMessageBatchCommand({
        QueueUrl: q.url,
        Entries: [
          { Id: "e1", MessageBody: "one" },
          { Id: "e2", MessageBody: "two" },
        ],
      }),
    );
    expect(out.Failed ?? []).toEqual([]);
    expect(out.Successful?.map((s) => s.Id)).toEqual(["e1", "e2"]);
    expect(out.Successful?.map((s) => s.MD5OfMessageBody)).toEqual([md5("one"), md5("two")]);
    expect(out.Successful?.every((s) => (s.MessageId ?? "").length > 0)).toBe(true);
  });

  test("SendMessageBatch with more than 10 entries fails TooManyEntriesInBatchRequest", async () => {
    const q = await makeQueue();
    const entries = Array.from({ length: 11 }, (_, i) => ({ Id: `e${i}`, MessageBody: "x" }));
    const failure = await expectFailure(sqs.send(new SendMessageBatchCommand({ QueueUrl: q.url, Entries: entries })), {
      name: "TooManyEntriesInBatchRequest",
      status: 400,
      messageIncludes: "Maximum number of entries per request are 10. You have sent 11.",
    });
    expect(failure.code).toBe("AWS.SimpleQueueService.TooManyEntriesInBatchRequest");
  });

  test("SendMessageBatch with no entries fails EmptyBatchRequest", async () => {
    const q = await makeQueue();
    await expectFailure(sqs.send(new SendMessageBatchCommand({ QueueUrl: q.url, Entries: [] })), {
      name: "EmptyBatchRequest",
      status: 400,
    });
  });

  test("SendMessageBatch with repeated entry ids fails BatchEntryIdsNotDistinct", async () => {
    const q = await makeQueue();
    await expectFailure(
      sqs.send(
        new SendMessageBatchCommand({
          QueueUrl: q.url,
          Entries: [
            { Id: "same", MessageBody: "a" },
            { Id: "same", MessageBody: "b" },
          ],
        }),
      ),
      { name: "BatchEntryIdsNotDistinct", status: 400 },
    );
  });

  test("SendMessageBatch with an invalid entry id fails InvalidBatchEntryId for the whole request", async () => {
    const q = await makeQueue();
    await expectFailure(
      sqs.send(new SendMessageBatchCommand({ QueueUrl: q.url, Entries: [{ Id: "invalid:id", MessageBody: "a" }] })),
      {
        name: "InvalidBatchEntryId",
        status: 400,
        messageIncludes: "A batch entry id can only contain alphanumeric characters, hyphens and underscores. It can be at most 80 letters long.",
      },
    );
  });

  test("an entry over MaximumMessageSize lands in Failed while the others succeed", async () => {
    const q = await makeQueue({ MaximumMessageSize: "1024" });
    const out = await sqs.send(
      new SendMessageBatchCommand({
        QueueUrl: q.url,
        Entries: [
          { Id: "ok", MessageBody: "small" },
          { Id: "big", MessageBody: "a".repeat(1025) },
        ],
      }),
    );
    expect(out.Successful?.map((s) => s.Id)).toEqual(["ok"]);
    expect(out.Failed).toHaveLength(1);
    expect(out.Failed?.[0]).toMatchObject({ Id: "big", SenderFault: true, Code: "InvalidParameterValue" });
  });

  test("a batch whose entries total more than 1048576 bytes fails BatchRequestTooLong", async () => {
    const q = await makeQueue();
    const entries = [
      { Id: "a", MessageBody: "a".repeat(524_289) },
      { Id: "b", MessageBody: "b".repeat(524_288) },
    ];
    const failure = await expectFailure(sqs.send(new SendMessageBatchCommand({ QueueUrl: q.url, Entries: entries })), {
      name: "BatchRequestTooLong",
      status: 400,
      messageIncludes: "Batch requests cannot be longer than 1048576 bytes. You have sent 1048577 bytes.",
    });
    expect(failure.code).toBe("AWS.SimpleQueueService.BatchRequestTooLong");
  });

  test("DeleteMessageBatch deletes every message and reports a bad handle per entry", async () => {
    const q = await makeQueue();
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "one" }));
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "two" }));
    const messages: Message[] = [];
    await eventually("both messages received", async () => {
      messages.push(...(await receiveAll(q.url, { visibility: 30 })));
      return messages.length >= 2 ? true : undefined;
    });
    const out = await sqs.send(
      new DeleteMessageBatchCommand({
        QueueUrl: q.url,
        Entries: [
          ...messages.map((m, i) => ({ Id: `d${i}`, ReceiptHandle: m.ReceiptHandle })),
          { Id: "bad", ReceiptHandle: "garbage" },
        ],
      }),
    );
    expect(out.Successful?.map((s) => s.Id).sort()).toEqual(["d0", "d1"]);
    expect(out.Failed).toHaveLength(1);
    expect(out.Failed?.[0]).toMatchObject({ Id: "bad", SenderFault: true, Code: "ReceiptHandleIsInvalid" });
    expect((await attributes(q.url)).ApproximateNumberOfMessagesNotVisible).toBe("0");
  });

  test("ChangeMessageVisibilityBatch changes visibility per entry", async () => {
    const q = await makeQueue({ VisibilityTimeout: "60" });
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "one" }));
    const [m] = await receiveAll(q.url);
    const out = await sqs.send(
      new ChangeMessageVisibilityBatchCommand({
        QueueUrl: q.url,
        Entries: [{ Id: "c1", ReceiptHandle: m?.ReceiptHandle, VisibilityTimeout: 0 }],
      }),
    );
    expect(out.Successful?.map((s) => s.Id)).toEqual(["c1"]);
    expect(await receiveAll(q.url)).toHaveLength(1);
  });
});

describe("SQS FIFO queues", () => {
  const fifoAttrs = { FifoQueue: "true" };
  const fifoName = () => `${uniq("f")}.fifo`;

  test("a FIFO queue reports FifoQueue, DeduplicationScope, FifoThroughputLimit and ContentBasedDeduplication", async () => {
    const q = await makeQueue(fifoAttrs, { name: fifoName() });
    const attrs = await attributes(q.url);
    expect(attrs).toMatchObject({
      FifoQueue: "true",
      DeduplicationScope: "queue",
      FifoThroughputLimit: "perQueue",
      ContentBasedDeduplication: "false",
    });
  });

  test("FifoQueue=true on a name without .fifo is rejected", async () => {
    await expectFailure(sqs.send(new CreateQueueCommand({ QueueName: uniq("nofifo"), Attributes: fifoAttrs })), {
      name: "InvalidParameterValue",
      status: 400,
    });
  });

  test("FIFO-only attributes on a standard queue are rejected with InvalidAttributeName", async () => {
    await expectFailure(
      sqs.send(new CreateQueueCommand({ QueueName: uniq("std"), Attributes: { DeduplicationScope: "queue" } })),
      {
        name: "InvalidAttributeName",
        status: 400,
        messageIncludes: "You can specify the DeduplicationScope only when FifoQueue is set to true.",
      },
    );
  });

  test("FifoQueue cannot be changed to false", async () => {
    const q = await makeQueue(fifoAttrs, { name: fifoName() });
    await expectFailure(
      sqs.send(new SetQueueAttributesCommand({ QueueUrl: q.url, Attributes: { FifoQueue: "false" } })),
      {
        name: "InvalidAttributeValue",
        status: 400,
        messageIncludes: "Invalid value for the parameter FifoQueue. Reason: Modifying queue type is not supported.",
      },
    );
  });

  test("SendMessage without MessageGroupId fails MissingParameter", async () => {
    const q = await makeQueue(fifoAttrs, { name: fifoName() });
    await expectFailure(
      sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "x", MessageDeduplicationId: "d" })),
      { name: "MissingParameter", status: 400, messageIncludes: "The request must contain the parameter MessageGroupId." },
    );
  });

  test("SendMessage without a deduplication id fails unless ContentBasedDeduplication is on", async () => {
    const q = await makeQueue(fifoAttrs, { name: fifoName() });
    await expectFailure(sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "x", MessageGroupId: "g" })), {
      name: "InvalidParameterValue",
      status: 400,
      messageIncludes: "The queue should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly",
    });
  });

  test("a MessageGroupId with a space is rejected with the documented message", async () => {
    const q = await makeQueue(fifoAttrs, { name: fifoName() });
    await expectFailure(
      sqs.send(
        new SendMessageCommand({ QueueUrl: q.url, MessageBody: "x", MessageGroupId: "group 123", MessageDeduplicationId: "d" }),
      ),
      {
        name: "InvalidParameterValue",
        status: 400,
        messageIncludes:
          "Value group 123 for parameter MessageGroupId is invalid. Reason: MessageGroupId can only include alphanumeric and punctuation characters. 1 to 128 in length.",
      },
    );
  });

  test("a per-message DelaySeconds above 0 is rejected on a FIFO queue", async () => {
    const q = await makeQueue(fifoAttrs, { name: fifoName() });
    await expectFailure(
      sqs.send(
        new SendMessageCommand({
          QueueUrl: q.url,
          MessageBody: "x",
          MessageGroupId: "g",
          MessageDeduplicationId: "d",
          DelaySeconds: 2,
        }),
      ),
      { name: "InvalidParameterValue", status: 400 },
    );
  });

  test("SendMessage returns an increasing SequenceNumber within a group", async () => {
    const q = await makeQueue(fifoAttrs, { name: fifoName() });
    const send = async (id: string) =>
      (await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: id, MessageGroupId: "g", MessageDeduplicationId: id })))
        .SequenceNumber;
    const first = await send("a");
    const second = await send("b");
    expect(first).toMatch(/^\d+$/);
    expect(BigInt(second ?? "0")).toBeGreaterThan(BigInt(first ?? "0"));
  });

  test("a duplicate MessageDeduplicationId is accepted but enqueued once", async () => {
    const q = await makeQueue(fifoAttrs, { name: fifoName() });
    const send = () =>
      sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "dup", MessageGroupId: "g", MessageDeduplicationId: "same" }));
    await send();
    await send();
    const first = await receiveAll(q.url, { visibility: 0 });
    expect(first).toHaveLength(1);
    expect((await attributes(q.url)).ApproximateNumberOfMessages).toBe("1");
  });

  test("ContentBasedDeduplication deduplicates on the body", async () => {
    const q = await makeQueue({ ...fifoAttrs, ContentBasedDeduplication: "true" }, { name: fifoName() });
    const send = (body: string) => sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: body, MessageGroupId: "g" }));
    await send("same-body");
    await send("same-body");
    await send("other-body");
    const got = await receiveAll(q.url, { max: 10 });
    expect(got.map((m) => m.Body)).toEqual(["same-body", "other-body"]);
  });

  test("deduplication id exists as a system attribute alongside group id and sequence number on receive", async () => {
    const q = await makeQueue(fifoAttrs, { name: fifoName() });
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "attrs", MessageGroupId: "grp", MessageDeduplicationId: "dd" }));
    const [m] = await receiveAll(q.url);
    expect(m?.Attributes).toMatchObject({ MessageGroupId: "grp", MessageDeduplicationId: "dd" });
    expect(m?.Attributes?.SequenceNumber).toMatch(/^\d+$/);
  });

  test("messages of one group are delivered in send order", async () => {
    const q = await makeQueue(fifoAttrs, { name: fifoName() });
    for (const body of ["1", "2", "3", "4"]) {
      await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: body, MessageGroupId: "g", MessageDeduplicationId: body }));
    }
    const got = await receiveAll(q.url, { max: 10 });
    expect(got.map((m) => m.Body)).toEqual(["1", "2", "3", "4"]);
  });

  test("a group with an in-flight message is closed to further receives until it is deleted", async () => {
    const q = await makeQueue(fifoAttrs, { name: fifoName() });
    for (const [group, body] of [["g1", "g1-a"], ["g1", "g1-b"], ["g2", "g2-a"]] as const) {
      await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: body, MessageGroupId: group, MessageDeduplicationId: body }));
    }
    const first = await receiveAll(q.url, { max: 1, visibility: 30 });
    expect(first).toHaveLength(1);
    const firstGroup = first[0]?.Attributes?.MessageGroupId;
    const rest = await receiveAll(q.url, { max: 10, visibility: 30 });
    expect(rest.every((m) => m.Attributes?.MessageGroupId !== firstGroup)).toBe(true);
    await sqs.send(new DeleteMessageCommand({ QueueUrl: q.url, ReceiptHandle: first[0]?.ReceiptHandle }));
    const reopened = await receiveAll(q.url, { max: 10, visibility: 30 });
    expect(reopened.some((m) => m.Attributes?.MessageGroupId === firstGroup)).toBe(true);
  });

  test("DeduplicationScope=queue suppresses the same id sent to another group", async () => {
    const q = await makeQueue(fifoAttrs, { name: fifoName() });
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "a", MessageGroupId: "g1", MessageDeduplicationId: "x" }));
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "b", MessageGroupId: "g2", MessageDeduplicationId: "x" }));
    expect((await receiveAll(q.url, { max: 10 })).map((m) => m.Body)).toEqual(["a"]);
  });

  test("DeduplicationScope=messageGroup lets the same id through in another group", async () => {
    const q = await makeQueue({ ...fifoAttrs, DeduplicationScope: "messageGroup" }, { name: fifoName() });
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "a", MessageGroupId: "g1", MessageDeduplicationId: "x" }));
    await sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "b", MessageGroupId: "g2", MessageDeduplicationId: "x" }));
    expect((await receiveAll(q.url, { max: 10 })).map((m) => m.Body).sort()).toEqual(["a", "b"]);
  });

  test("a FIFO batch entry without MessageGroupId fails the whole request with InvalidParameterValue", async () => {
    const q = await makeQueue(fifoAttrs, { name: fifoName() });
    await expectFailure(
      sqs.send(
        new SendMessageBatchCommand({
          QueueUrl: q.url,
          Entries: [
            { Id: "a", MessageBody: "a", MessageGroupId: "g", MessageDeduplicationId: "a" },
            { Id: "b", MessageBody: "b", MessageDeduplicationId: "b" },
          ],
        }),
      ),
      { name: "InvalidParameterValue", status: 400, messageIncludes: "The request must contain the parameter MessageGroupId." },
    );
  });

  test("PurgeQueue clears the deduplication cache", async () => {
    const q = await makeQueue(fifoAttrs, { name: fifoName() });
    const send = () =>
      sqs.send(new SendMessageCommand({ QueueUrl: q.url, MessageBody: "p", MessageGroupId: "g", MessageDeduplicationId: "same" }));
    await send();
    await sqs.send(new PurgeQueueCommand({ QueueUrl: q.url }));
    await send();
    expect(await receiveAll(q.url)).toHaveLength(1);
  });
});

describe("SQS dead-letter queues and redrive", () => {
  async function pair(maxReceiveCount: number) {
    const dlq = await makeQueue();
    const source = await makeQueue({
      RedrivePolicy: JSON.stringify({ deadLetterTargetArn: dlq.arn, maxReceiveCount }),
    });
    return { dlq, source };
  }

  test("GetQueueAttributes returns RedrivePolicy with maxReceiveCount as a JSON number", async () => {
    const { dlq, source } = await pair(3);
    const policy = JSON.parse((await attributes(source.url, ["RedrivePolicy"])).RedrivePolicy ?? "null");
    expect(policy).toEqual({ deadLetterTargetArn: dlq.arn, maxReceiveCount: 3 });
  });

  test("a RedrivePolicy without maxReceiveCount is rejected", async () => {
    const dlq = await makeQueue();
    await expectFailure(
      sqs.send(
        new CreateQueueCommand({
          QueueName: uniq("q"),
          Attributes: { RedrivePolicy: JSON.stringify({ deadLetterTargetArn: dlq.arn }) },
        }),
      ),
      { name: "InvalidParameterValue", status: 400 },
    );
  });

  test("a RedrivePolicy naming a queue that does not exist is rejected", async () => {
    const failure = await awsFailure(
      sqs.send(
        new CreateQueueCommand({
          QueueName: uniq("q"),
          Attributes: {
            RedrivePolicy: JSON.stringify({
              deadLetterTargetArn: `arn:aws:sqs:${region}:${defaultAccount}:${uniq("ghost")}`,
              maxReceiveCount: 2,
            }),
          },
        }),
      ),
    );
    expect(failure.status).toBe(400);
  });

  test("a message received maxReceiveCount times moves to the DLQ on the next receive, keeping its id", async () => {
    const { dlq, source } = await pair(2);
    const sent = await sqs.send(new SendMessageCommand({ QueueUrl: source.url, MessageBody: "poison" }));
    expect(await receiveAll(source.url, { visibility: 0 })).toHaveLength(1);
    expect(await receiveAll(source.url, { visibility: 0 })).toHaveLength(1);
    expect(await receiveAll(source.url, { visibility: 0 })).toHaveLength(0);
    const moved = await eventually("the message in the DLQ", async () => {
      const [m] = await receiveAll(dlq.url, { visibility: 30 });
      return m;
    });
    expect(moved.MessageId).toBe(sent.MessageId);
    expect(moved.Body).toBe("poison");
    expect(moved.Attributes?.DeadLetterQueueSourceArn).toBe(source.arn);
    expect(moved.Attributes?.ApproximateReceiveCount).toBe("3");
  });

  test("ListDeadLetterSourceQueues lists the queues whose RedrivePolicy targets the DLQ", async () => {
    const { dlq, source } = await pair(5);
    const out = await sqs.send(new ListDeadLetterSourceQueuesCommand({ QueueUrl: dlq.url }));
    expect(out.queueUrls).toEqual([source.url]);
  });

  test("RedriveAllowPolicy is stored as given", async () => {
    const q = await makeQueue({ RedriveAllowPolicy: JSON.stringify({ redrivePermission: "denyAll" }) });
    expect(JSON.parse((await attributes(q.url, ["RedriveAllowPolicy"])).RedriveAllowPolicy ?? "null")).toEqual({
      redrivePermission: "denyAll",
    });
  });

  test("StartMessageMoveTask moves DLQ messages back to their source queue and the task completes", async () => {
    const { dlq, source } = await pair(1);
    await sqs.send(new SendMessageCommand({ QueueUrl: source.url, MessageBody: "redrive-me" }));
    await receiveAll(source.url, { visibility: 0 });
    await receiveAll(source.url, { visibility: 0 });
    await eventually("the message in the DLQ", async () => (await attributes(dlq.url)).ApproximateNumberOfMessages === "1" ? true : undefined);
    const started = await sqs.send(new StartMessageMoveTaskCommand({ SourceArn: dlq.arn }));
    expect((started.TaskHandle ?? "").length).toBeGreaterThan(0);
    const task = await eventually("the move task completes", async () => {
      const listed = await sqs.send(new ListMessageMoveTasksCommand({ SourceArn: dlq.arn }));
      const first = listed.Results?.[0];
      return first?.Status === "COMPLETED" ? first : undefined;
    });
    expect(task.ApproximateNumberOfMessagesMoved).toBe(1);
    expect(task.SourceArn).toBe(dlq.arn);
    const [back] = await receiveAll(source.url);
    expect(back?.Body).toBe("redrive-me");
  });

  test("StartMessageMoveTask from a queue that is not a DLQ fails InvalidParameterValue", async () => {
    const q = await makeQueue();
    await expectFailure(sqs.send(new StartMessageMoveTaskCommand({ SourceArn: q.arn })), {
      name: "InvalidParameterValue",
      status: 400,
      messageIncludes: "Source queue must be configured as a Dead Letter Queue.",
    });
  });

  test("StartMessageMoveTask with a missing destination fails ResourceNotFoundException", async () => {
    const { dlq } = await pair(1);
    await expectFailure(
      sqs.send(
        new StartMessageMoveTaskCommand({
          SourceArn: dlq.arn,
          DestinationArn: `arn:aws:sqs:${region}:${defaultAccount}:${uniq("ghost")}`,
        }),
      ),
      {
        name: "ResourceNotFoundException",
        status: 404,
        messageIncludes: "The resource that you specified for the DestinationArn parameter doesn't exist.",
      },
    );
  });

  test("CancelMessageMoveTask with a garbage handle fails InvalidParameterValue", async () => {
    await expectFailure(sqs.send(new CancelMessageMoveTaskCommand({ TaskHandle: "garbage" })), {
      name: "InvalidParameterValue",
      status: 400,
      messageIncludes: "Value for parameter TaskHandle is invalid.",
    });
  });
});
