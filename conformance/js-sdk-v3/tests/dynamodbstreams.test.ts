// DynamoDB Streams over awsJson1_0, through @aws-sdk/client-dynamodb-streams,
// signed for dynamodb. Expected behavior: real DynamoDB Streams as documented
// in docs/research/dynamodb-streams.md sections 3 to 7 and 11.
//
// understudy serves the stream log beside DynamoDB, which runs through a
// DynamoDB Local container. CONFORMANCE_DYNAMODB=0 skips this file.

import { afterAll, describe, expect, test } from "bun:test";
import { CreateTableCommand, DeleteItemCommand, DeleteTableCommand, DescribeTableCommand, DynamoDBClient, PutItemCommand, UpdateItemCommand, UpdateTableCommand, waitUntilTableExists } from "@aws-sdk/client-dynamodb";
import { DescribeStreamCommand, DynamoDBStreamsClient, GetRecordsCommand, GetShardIteratorCommand, ListStreamsCommand, type _Record } from "@aws-sdk/client-dynamodb-streams";
import { clientConfig, defaultAccount, dynamoDbOptedOut, region } from "../support/env";
import { eventually, expectFailure, uniq } from "../support/helpers";

if (dynamoDbOptedOut) {
  console.warn("dynamodbstreams.test.ts: skipped. DynamoDB opted out (CONFORMANCE_DYNAMODB=0).");
}

const ddb = new DynamoDBClient(clientConfig());
const streams = new DynamoDBStreamsClient(clientConfig());
const tables: string[] = [];

afterAll(async () => {
  for (const name of tables) await ddb.send(new DeleteTableCommand({ TableName: name })).catch(() => undefined);
});

// dynamodb-streams.md 4.1: the label form AWS answers and Terraform matches against AWS.
const label = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}$/;

async function streamTable(view: "NEW_AND_OLD_IMAGES" | "NEW_IMAGE" = "NEW_AND_OLD_IMAGES") {
  const name = uniq("conf-streamed");
  const created = await ddb.send(new CreateTableCommand({
    TableName: name,
    KeySchema: [{ AttributeName: "id", KeyType: "HASH" }],
    AttributeDefinitions: [{ AttributeName: "id", AttributeType: "S" }],
    BillingMode: "PAY_PER_REQUEST",
    StreamSpecification: { StreamEnabled: true, StreamViewType: view },
  }));
  tables.push(name);
  await waitUntilTableExists({ client: ddb, maxWaitTime: 120 }, { TableName: name });
  return { name, arn: created.TableDescription?.LatestStreamArn ?? "", label: created.TableDescription?.LatestStreamLabel ?? "" };
}

async function readAll(arn: string, shard: string): Promise<_Record[]> {
  let it = (await streams.send(new GetShardIteratorCommand({ StreamArn: arn, ShardId: shard, ShardIteratorType: "TRIM_HORIZON" }))).ShardIterator;
  // dynamodb-streams.md 4.4: "<stream ARN>|1|<token>".
  expect(it?.startsWith(`${arn}|1|`)).toBe(true);
  const out: _Record[] = [];
  while (it) {
    const page = await streams.send(new GetRecordsCommand({ ShardIterator: it }));
    out.push(...(page.Records ?? []));
    if ((page.Records ?? []).length === 0) break;
    it = page.NextShardIterator;
  }
  return out;
}

describe.skipIf(dynamoDbOptedOut)("DynamoDB Streams", () => {
  test("CreateTable answers the stream, and ListStreams and DescribeStream serve it", async () => {
    const table = await streamTable();
    expect(table.label).toMatch(label);
    expect(table.arn).toBe(`arn:aws:dynamodb:${region}:${defaultAccount}:table/${table.name}/stream/${table.label}`);
    // dynamodb-streams.md 9.3: identical on every read.
    const described = (await ddb.send(new DescribeTableCommand({ TableName: table.name }))).Table;
    expect([described?.LatestStreamArn, described?.LatestStreamLabel]).toEqual([table.arn, table.label]);
    const listed = await streams.send(new ListStreamsCommand({ TableName: table.name }));
    expect(listed.Streams).toEqual([{ StreamArn: table.arn, TableName: table.name, StreamLabel: table.label }]);
    // DDB-SNAP:L708-L735 (third-party-recorded): one open shard, ENABLED.
    const d = (await streams.send(new DescribeStreamCommand({ StreamArn: table.arn }))).StreamDescription;
    expect(d).toMatchObject({ StreamStatus: "ENABLED", StreamViewType: "NEW_AND_OLD_IMAGES", TableName: table.name });
    expect(d?.Shards).toHaveLength(1);
    expect(d?.Shards?.[0]?.SequenceNumberRange?.EndingSequenceNumber).toBeUndefined();
  });

  test("PutItem, UpdateItem and DeleteItem reach the stream in order with the view type's images", async () => {
    const table = await streamTable();
    const shard = (await streams.send(new DescribeStreamCommand({ StreamArn: table.arn }))).StreamDescription?.Shards?.[0];
    await ddb.send(new PutItemCommand({ TableName: table.name, Item: { id: { S: "a" }, v: { S: "1" } } }));
    await ddb.send(new UpdateItemCommand({ TableName: table.name, Key: { id: { S: "a" } }, UpdateExpression: "SET v = :v", ExpressionAttributeValues: { ":v": { S: "2" } } }));
    await ddb.send(new DeleteItemCommand({ TableName: table.name, Key: { id: { S: "a" } } }));
    const records = await readAll(table.arn, shard?.ShardId ?? "");
    // dynamodb-streams.md 7.1, 7.2, 7.3; eventVersion 1.1 (DDB-SNAP:L761); the first record above
    // StartingSequenceNumber (DDB-TEST:L1993-L1994).
    expect(records.map((r) => r.eventName)).toEqual(["INSERT", "MODIFY", "REMOVE"]);
    let previous = BigInt(shard?.SequenceNumberRange?.StartingSequenceNumber ?? "0");
    records.forEach((r, i) => {
      expect(r).toMatchObject({ eventSource: "aws:dynamodb", eventVersion: "1.1", awsRegion: region });
      expect(r.userIdentity).toBeUndefined();
      const seq = BigInt(r.dynamodb?.SequenceNumber ?? "0");
      expect(seq > previous).toBe(true);
      previous = seq;
      expect(r.dynamodb?.ApproximateCreationDateTime?.getMilliseconds()).toBe(0);
      expect(r.dynamodb?.NewImage !== undefined).toBe(i < 2);
      expect(r.dynamodb?.OldImage !== undefined).toBe(i > 0);
    });
  });

  test("DescribeStream of a missing stream fails with the recorded text", async () => {
    const table = await streamTable();
    const missing = table.arn.replace("/stream/", "x/stream/");
    // STREAMS-SNAP:L90-L104 (third-party-recorded).
    const failure = await expectFailure(streams.send(new DescribeStreamCommand({ StreamArn: missing })), { name: "ResourceNotFoundException", status: 400 });
    expect(failure.message).toBe(`Requested resource not found: Stream: ${missing} not found`);
  });

  test("a disable closes the shard and keeps LatestStreamArn", async () => {
    const table = await streamTable();
    await ddb.send(new PutItemCommand({ TableName: table.name, Item: { id: { S: "a" } } }));
    await ddb.send(new UpdateTableCommand({ TableName: table.name, StreamSpecification: { StreamEnabled: false } }));
    // dynamodb-streams.md 5.3 and 9.3.
    const d = await eventually("the stream to be DISABLED", async () => {
      const got = (await streams.send(new DescribeStreamCommand({ StreamArn: table.arn }))).StreamDescription;
      return got?.StreamStatus === "DISABLED" ? got : undefined;
    });
    expect(d.Shards?.[0]?.SequenceNumberRange?.EndingSequenceNumber).toBeDefined();
    const described = (await ddb.send(new DescribeTableCommand({ TableName: table.name }))).Table;
    expect(described?.LatestStreamArn).toBe(table.arn);
    expect(described?.StreamSpecification).toBeUndefined();
    const it = (await streams.send(new GetShardIteratorCommand({ StreamArn: table.arn, ShardId: d.Shards?.[0]?.ShardId, ShardIteratorType: "TRIM_HORIZON" }))).ShardIterator;
    const page = await streams.send(new GetRecordsCommand({ ShardIterator: it }));
    expect(page.Records).toHaveLength(1);
    expect(page.NextShardIterator).toBeUndefined();
  });
});
