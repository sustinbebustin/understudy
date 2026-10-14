// DynamoDB over awsJson1_0, through @aws-sdk/client-dynamodb and
// @aws-sdk/lib-dynamodb. Expected behavior: real DynamoDB as documented in
// docs/research/phase1/dynamodb-local.md and error-matrix.md section 5.5.
//
// understudy serves DynamoDB through a DynamoDB Local container, pulled on
// first use. CONFORMANCE_DYNAMODB=0 skips this file, and says why.

import { describe, expect, test } from "bun:test";
import {
  BatchGetItemCommand,
  BatchWriteItemCommand,
  CreateTableCommand,
  DeleteItemCommand,
  DeleteTableCommand,
  DescribeContinuousBackupsCommand,
  DescribeTableCommand,
  DescribeTimeToLiveCommand,
  DynamoDBClient,
  GetItemCommand,
  ListTablesCommand,
  ListTagsOfResourceCommand,
  PutItemCommand,
  QueryCommand,
  ScanCommand,
  TagResourceCommand,
  TransactGetItemsCommand,
  TransactWriteItemsCommand,
  UntagResourceCommand,
  UpdateContinuousBackupsCommand,
  UpdateItemCommand,
  UpdateTableCommand,
  UpdateTimeToLiveCommand,
  waitUntilTableExists,
  type AttributeValue,
  type CreateTableCommandInput,
} from "@aws-sdk/client-dynamodb";
import { DeleteCommand, DynamoDBDocumentClient, GetCommand, PutCommand, QueryCommand as DocQueryCommand, UpdateCommand } from "@aws-sdk/lib-dynamodb";
import { clientConfig, defaultAccount, dynamoDbOptedOut, region } from "../support/env";
import { awsFailure, expectFailure, uniq } from "../support/helpers";

if (dynamoDbOptedOut) {
  console.warn("dynamodb.test.ts: skipped. DynamoDB opted out (CONFORMANCE_DYNAMODB=0).");
}

const ddb = new DynamoDBClient(clientConfig());
const doc = DynamoDBDocumentClient.from(ddb, { marshallOptions: { removeUndefinedValues: true } });

const S = (value: string): AttributeValue => ({ S: value });
const N = (value: number | string): AttributeValue => ({ N: String(value) });

const tableArn = (name: string) => `arn:aws:dynamodb:${region}:${defaultAccount}:table/${name}`;

async function makeTable(extra: Partial<CreateTableCommandInput> = {}): Promise<string> {
  const name = extra.TableName ?? uniq("tbl");
  await ddb.send(
    new CreateTableCommand({
      TableName: name,
      AttributeDefinitions: [{ AttributeName: "pk", AttributeType: "S" }],
      KeySchema: [{ AttributeName: "pk", KeyType: "HASH" }],
      BillingMode: "PAY_PER_REQUEST",
      ...extra,
    }),
  );
  await waitUntilTableExists({ client: ddb, maxWaitTime: 30 }, { TableName: name });
  return name;
}

async function makeRangeTable(): Promise<string> {
  return makeTable({
    AttributeDefinitions: [
      { AttributeName: "pk", AttributeType: "S" },
      { AttributeName: "sk", AttributeType: "N" },
    ],
    KeySchema: [
      { AttributeName: "pk", KeyType: "HASH" },
      { AttributeName: "sk", KeyType: "RANGE" },
    ],
  });
}

describe.skipIf(dynamoDbOptedOut)("DynamoDB tables", () => {
  test("CreateTable returns the table description and DescribeTable agrees", async () => {
    const name = uniq("tbl");
    const created = await ddb.send(
      new CreateTableCommand({
        TableName: name,
        AttributeDefinitions: [{ AttributeName: "LockID", AttributeType: "S" }],
        KeySchema: [{ AttributeName: "LockID", KeyType: "HASH" }],
        BillingMode: "PAY_PER_REQUEST",
      }),
    );
    expect(created.TableDescription).toMatchObject({
      TableName: name,
      TableArn: tableArn(name),
      KeySchema: [{ AttributeName: "LockID", KeyType: "HASH" }],
      AttributeDefinitions: [{ AttributeName: "LockID", AttributeType: "S" }],
    });
    await waitUntilTableExists({ client: ddb, maxWaitTime: 30 }, { TableName: name });
    const described = (await ddb.send(new DescribeTableCommand({ TableName: name }))).Table;
    expect(described).toMatchObject({
      TableName: name,
      TableStatus: "ACTIVE",
      TableArn: tableArn(name),
      ItemCount: 0,
      TableSizeBytes: 0,
      BillingModeSummary: { BillingMode: "PAY_PER_REQUEST" },
    });
    expect(described?.CreationDateTime).toBeInstanceOf(Date);
    expect(described?.TableId).toMatch(/^[0-9a-f-]{36}$/);
  });

  test("CreateTable on an existing name fails ResourceInUseException", async () => {
    const name = await makeTable();
    await expectFailure(
      ddb.send(
        new CreateTableCommand({
          TableName: name,
          AttributeDefinitions: [{ AttributeName: "pk", AttributeType: "S" }],
          KeySchema: [{ AttributeName: "pk", KeyType: "HASH" }],
          BillingMode: "PAY_PER_REQUEST",
        }),
      ),
      { name: "ResourceInUseException", status: 400, messageIncludes: "Table already exists" },
    );
  });

  test("table names are case-sensitive on AWS: a name differing only by case is a different table", async () => {
    const name = uniq("tbl");
    await makeTable({ TableName: name });
    await makeTable({ TableName: `${name}-X` });
    await expectFailure(ddb.send(new DescribeTableCommand({ TableName: name.toUpperCase() })), { name: "ResourceNotFoundException", status: 400 });
  });

  test("DescribeTable on a missing table fails ResourceNotFoundException", async () => {
    await expectFailure(ddb.send(new DescribeTableCommand({ TableName: uniq("missing") })), {
      name: "ResourceNotFoundException",
      status: 400,
      messageIncludes: "Requested resource not found",
    });
  });

  test("CreateTable with a one-character name fails ValidationException", async () => {
    await expectFailure(
      ddb.send(
        new CreateTableCommand({
          TableName: "x",
          AttributeDefinitions: [{ AttributeName: "pk", AttributeType: "S" }],
          KeySchema: [{ AttributeName: "pk", KeyType: "HASH" }],
          BillingMode: "PAY_PER_REQUEST",
        }),
      ),
      { name: "ValidationException", status: 400 },
    );
  });

  test("CreateTable with a key attribute missing from AttributeDefinitions fails ValidationException", async () => {
    await expectFailure(
      ddb.send(
        new CreateTableCommand({
          TableName: uniq("tbl"),
          AttributeDefinitions: [{ AttributeName: "other", AttributeType: "S" }],
          KeySchema: [{ AttributeName: "pk", KeyType: "HASH" }],
          BillingMode: "PAY_PER_REQUEST",
        }),
      ),
      { name: "ValidationException", status: 400 },
    );
  });

  test("ListTables lists created tables and pages with Limit and ExclusiveStartTableName", async () => {
    const prefix = uniq("lt");
    const names = [`${prefix}-a`, `${prefix}-b`, `${prefix}-c`];
    for (const name of names) await makeTable({ TableName: name });
    const seen: string[] = [];
    let start: string | undefined;
    let pages = 0;
    do {
      const out = await ddb.send(new ListTablesCommand({ Limit: 1, ExclusiveStartTableName: start }));
      expect(out.TableNames?.length ?? 0).toBeLessThanOrEqual(1);
      seen.push(...(out.TableNames ?? []));
      start = out.LastEvaluatedTableName;
      pages += 1;
    } while (start !== undefined && pages < 1000);
    for (const name of names) expect(seen).toContain(name);
  });

  test("DeleteTable returns the table in DELETING and the table is then gone", async () => {
    const name = await makeTable();
    const out = await ddb.send(new DeleteTableCommand({ TableName: name }));
    expect(out.TableDescription).toMatchObject({ TableName: name, TableStatus: "DELETING" });
    await expectFailure(ddb.send(new DescribeTableCommand({ TableName: name })), { name: "ResourceNotFoundException", status: 400 });
  });

  test("DeleteTable on a missing table fails ResourceNotFoundException naming the table", async () => {
    const name = uniq("missing");
    await expectFailure(ddb.send(new DeleteTableCommand({ TableName: name })), {
      name: "ResourceNotFoundException",
      status: 400,
      messageIncludes: `Requested resource not found: Table: ${name} not found`,
    });
  });

  test("a provisioned table reports its throughput and UpdateTable can switch to on-demand", async () => {
    const name = await makeTable({ BillingMode: "PROVISIONED", ProvisionedThroughput: { ReadCapacityUnits: 5, WriteCapacityUnits: 6 } });
    const described = (await ddb.send(new DescribeTableCommand({ TableName: name }))).Table;
    expect(described?.ProvisionedThroughput).toMatchObject({ ReadCapacityUnits: 5, WriteCapacityUnits: 6 });
    await ddb.send(new UpdateTableCommand({ TableName: name, BillingMode: "PAY_PER_REQUEST" }));
    const updated = (await ddb.send(new DescribeTableCommand({ TableName: name }))).Table;
    expect(updated?.BillingModeSummary?.BillingMode).toBe("PAY_PER_REQUEST");
  });

  test("tables are isolated per region", async () => {
    const name = await makeTable();
    const other = new DynamoDBClient(clientConfig({ region: "eu-west-1" }));
    await expectFailure(other.send(new DescribeTableCommand({ TableName: name })), { name: "ResourceNotFoundException", status: 400 });
    expect((await other.send(new ListTablesCommand({}))).TableNames ?? []).not.toContain(name);
  });

  test("tables are isolated per account, taken from the access key", async () => {
    const name = await makeTable();
    const other = new DynamoDBClient(clientConfig({ accessKeyId: "210987654321" }));
    await expectFailure(other.send(new DescribeTableCommand({ TableName: name })), { name: "ResourceNotFoundException", status: 400 });
  });

  test("a table in another account reports that account in its ARN", async () => {
    const other = new DynamoDBClient(clientConfig({ accessKeyId: "210987654321" }));
    const name = uniq("tbl");
    await other.send(
      new CreateTableCommand({
        TableName: name,
        AttributeDefinitions: [{ AttributeName: "pk", AttributeType: "S" }],
        KeySchema: [{ AttributeName: "pk", KeyType: "HASH" }],
        BillingMode: "PAY_PER_REQUEST",
      }),
    );
    const described = (await other.send(new DescribeTableCommand({ TableName: name }))).Table;
    expect(described?.TableArn).toBe(`arn:aws:dynamodb:${region}:210987654321:table/${name}`);
  });

  test("tags round trip through TagResource, ListTagsOfResource and UntagResource", async () => {
    const name = await makeTable({ Tags: [{ Key: "env", Value: "test" }] });
    const arn = tableArn(name);
    const tags = async () => Object.fromEntries(((await ddb.send(new ListTagsOfResourceCommand({ ResourceArn: arn }))).Tags ?? []).map((t) => [t.Key, t.Value]));
    expect(await tags()).toEqual({ env: "test" });
    await ddb.send(new TagResourceCommand({ ResourceArn: arn, Tags: [{ Key: "team", Value: "a" }] }));
    expect(await tags()).toEqual({ env: "test", team: "a" });
    await ddb.send(new UntagResourceCommand({ ResourceArn: arn, TagKeys: ["env"] }));
    expect(await tags()).toEqual({ team: "a" });
  });

  test("TTL is disabled by default, can be enabled on an attribute, and DescribeTimeToLive reports it", async () => {
    const name = await makeTable();
    expect((await ddb.send(new DescribeTimeToLiveCommand({ TableName: name }))).TimeToLiveDescription?.TimeToLiveStatus).toBe("DISABLED");
    const out = await ddb.send(new UpdateTimeToLiveCommand({ TableName: name, TimeToLiveSpecification: { Enabled: true, AttributeName: "ExpiresAt" } }));
    expect(out.TimeToLiveSpecification).toEqual({ Enabled: true, AttributeName: "ExpiresAt" });
    const described = (await ddb.send(new DescribeTimeToLiveCommand({ TableName: name }))).TimeToLiveDescription;
    expect(described?.AttributeName).toBe("ExpiresAt");
    expect(["ENABLED", "ENABLING"]).toContain(String(described?.TimeToLiveStatus));
  });

  test("continuous backups report ENABLED with point-in-time recovery DISABLED until it is turned on", async () => {
    const name = await makeTable();
    const before = (await ddb.send(new DescribeContinuousBackupsCommand({ TableName: name }))).ContinuousBackupsDescription;
    expect(before?.ContinuousBackupsStatus).toBe("ENABLED");
    expect(before?.PointInTimeRecoveryDescription?.PointInTimeRecoveryStatus).toBe("DISABLED");
    await ddb.send(new UpdateContinuousBackupsCommand({ TableName: name, PointInTimeRecoverySpecification: { PointInTimeRecoveryEnabled: true } }));
    const after = (await ddb.send(new DescribeContinuousBackupsCommand({ TableName: name }))).ContinuousBackupsDescription;
    expect(after?.PointInTimeRecoveryDescription?.PointInTimeRecoveryStatus).toBe("ENABLED");
    expect(after?.PointInTimeRecoveryDescription?.EarliestRestorableDateTime).toBeInstanceOf(Date);
  });
});

describe.skipIf(dynamoDbOptedOut)("DynamoDB items", () => {
  test("PutItem then GetItem round trips every attribute type", async () => {
    const table = await makeTable();
    const item: Record<string, AttributeValue> = {
      pk: S("all-types"),
      str: S("text"),
      num: N("3.14159"),
      big: N("12345678901234567890"),
      bin: { B: new Uint8Array([0, 1, 2, 255]) },
      yes: { BOOL: true },
      no: { BOOL: false },
      nothing: { NULL: true },
      list: { L: [S("a"), N(1), { BOOL: true }, { L: [] }] },
      map: { M: { nested: { M: { deep: S("value") } }, n: N(2) } },
      ss: { SS: ["a", "b", "c"] },
      ns: { NS: ["1", "2.5"] },
      bs: { BS: [new Uint8Array([1]), new Uint8Array([2])] },
      empty: S(""),
    };
    await ddb.send(new PutItemCommand({ TableName: table, Item: item }));
    const got = (await ddb.send(new GetItemCommand({ TableName: table, Key: { pk: S("all-types") }, ConsistentRead: true }))).Item;
    expect(got?.str).toEqual(S("text"));
    expect(got?.num).toEqual(N("3.14159"));
    expect(got?.big).toEqual(N("12345678901234567890"));
    expect(Array.from(got?.bin?.B ?? [])).toEqual([0, 1, 2, 255]);
    expect(got?.yes).toEqual({ BOOL: true });
    expect(got?.nothing).toEqual({ NULL: true });
    expect(got?.list).toEqual(item.list);
    expect(got?.map).toEqual(item.map);
    expect([...(got?.ss?.SS ?? [])].sort()).toEqual(["a", "b", "c"]);
    expect([...(got?.ns?.NS ?? [])].sort()).toEqual(["1", "2.5"]);
    expect(got?.bs?.BS).toHaveLength(2);
    expect(got?.empty).toEqual(S(""));
  });

  test("GetItem of a missing key returns no Item", async () => {
    const table = await makeTable();
    const out = await ddb.send(new GetItemCommand({ TableName: table, Key: { pk: S("nope") } }));
    expect(out.Item).toBeUndefined();
  });

  test("GetItem with ProjectionExpression returns only the named attributes", async () => {
    const table = await makeTable();
    await ddb.send(new PutItemCommand({ TableName: table, Item: { pk: S("k"), a: S("1"), b: S("2"), c: S("3") } }));
    const out = await ddb.send(
      new GetItemCommand({ TableName: table, Key: { pk: S("k") }, ProjectionExpression: "a, #c", ExpressionAttributeNames: { "#c": "c" } }),
    );
    expect(out.Item).toEqual({ a: S("1"), c: S("3") });
  });

  test("PutItem with ReturnValues ALL_OLD returns the replaced item", async () => {
    const table = await makeTable();
    await ddb.send(new PutItemCommand({ TableName: table, Item: { pk: S("k"), v: S("old") } }));
    const out = await ddb.send(new PutItemCommand({ TableName: table, Item: { pk: S("k"), v: S("new") }, ReturnValues: "ALL_OLD" }));
    expect(out.Attributes).toEqual({ pk: S("k"), v: S("old") });
  });

  test("DeleteItem removes the item and ALL_OLD returns it", async () => {
    const table = await makeTable();
    await ddb.send(new PutItemCommand({ TableName: table, Item: { pk: S("k"), v: S("x") } }));
    const out = await ddb.send(new DeleteItemCommand({ TableName: table, Key: { pk: S("k") }, ReturnValues: "ALL_OLD" }));
    expect(out.Attributes).toEqual({ pk: S("k"), v: S("x") });
    expect((await ddb.send(new GetItemCommand({ TableName: table, Key: { pk: S("k") } }))).Item).toBeUndefined();
  });

  test("an item without the key attribute fails ValidationException", async () => {
    const table = await makeTable();
    await expectFailure(ddb.send(new PutItemCommand({ TableName: table, Item: { other: S("x") } })), { name: "ValidationException", status: 400 });
  });

  test("a key attribute of the wrong type fails ValidationException", async () => {
    const table = await makeTable();
    await expectFailure(ddb.send(new PutItemCommand({ TableName: table, Item: { pk: N(1) } })), { name: "ValidationException", status: 400 });
  });

  test("an item over 400 KB fails ValidationException about its size", async () => {
    const table = await makeTable();
    await expectFailure(ddb.send(new PutItemCommand({ TableName: table, Item: { pk: S("big"), blob: S("x".repeat(410 * 1024)) } })), {
      name: "ValidationException",
      status: 400,
      messageIncludes: "Item size has exceeded the maximum allowed size",
    });
  });

  test("operations on a missing table fail ResourceNotFoundException", async () => {
    await expectFailure(ddb.send(new GetItemCommand({ TableName: uniq("missing"), Key: { pk: S("k") } })), {
      name: "ResourceNotFoundException",
      status: 400,
      messageIncludes: "Requested resource not found",
    });
  });

  test("ReturnConsumedCapacity TOTAL reports the table name and capacity units", async () => {
    const table = await makeTable();
    const out = await ddb.send(new PutItemCommand({ TableName: table, Item: { pk: S("k") }, ReturnConsumedCapacity: "TOTAL" }));
    expect(out.ConsumedCapacity?.TableName).toBe(table);
    expect(out.ConsumedCapacity?.CapacityUnits).toBeGreaterThan(0);
  });
});

describe.skipIf(dynamoDbOptedOut)("DynamoDB conditional writes: the lock table pattern", () => {
  const lockCondition = "attribute_not_exists(LockID) OR ExpiresAt < :now";

  async function lockTable() {
    return makeTable({
      AttributeDefinitions: [{ AttributeName: "LockID", AttributeType: "S" }],
      KeySchema: [{ AttributeName: "LockID", KeyType: "HASH" }],
    });
  }

  const acquire = (table: string, owner: string, expiresAt: number, now: number) =>
    ddb.send(
      new PutItemCommand({
        TableName: table,
        Item: { LockID: S("job-1"), Owner: S(owner), ExpiresAt: N(expiresAt) },
        ConditionExpression: lockCondition,
        ExpressionAttributeValues: { ":now": N(now) },
      }),
    );

  test("the first writer takes a free lock", async () => {
    const table = await lockTable();
    await acquire(table, "a", 2000, 1000);
    expect((await ddb.send(new GetItemCommand({ TableName: table, Key: { LockID: S("job-1") } }))).Item?.Owner).toEqual(S("a"));
  });

  test("a second writer is refused with ConditionalCheckFailedException while the lock is live", async () => {
    const table = await lockTable();
    await acquire(table, "a", 2000, 1000);
    await expectFailure(acquire(table, "b", 3000, 1500), {
      name: "ConditionalCheckFailedException",
      status: 400,
      messageIncludes: "The conditional request failed",
    });
    expect((await ddb.send(new GetItemCommand({ TableName: table, Key: { LockID: S("job-1") } }))).Item?.Owner).toEqual(S("a"));
  });

  test("a writer takes over an expired lock", async () => {
    const table = await lockTable();
    await acquire(table, "a", 2000, 1000);
    await acquire(table, "b", 5000, 2500);
    expect((await ddb.send(new GetItemCommand({ TableName: table, Key: { LockID: S("job-1") } }))).Item?.Owner).toEqual(S("b"));
  });

  test("ReturnValuesOnConditionCheckFailure ALL_OLD returns the item that blocked the write", async () => {
    const table = await lockTable();
    await acquire(table, "a", 2000, 1000);
    const failure = await expectFailure(
      ddb.send(
        new PutItemCommand({
          TableName: table,
          Item: { LockID: S("job-1"), Owner: S("b"), ExpiresAt: N(9000) },
          ConditionExpression: lockCondition,
          ExpressionAttributeValues: { ":now": N(1500) },
          ReturnValuesOnConditionCheckFailure: "ALL_OLD",
        }),
      ),
      { name: "ConditionalCheckFailedException", status: 400 },
    );
    expect(failure.error.Item).toEqual({ LockID: S("job-1"), Owner: S("a"), ExpiresAt: N(2000) });
  });

  test("a conditional DeleteItem releases only the owner's lock", async () => {
    const table = await lockTable();
    await acquire(table, "a", 2000, 1000);
    await expectFailure(
      ddb.send(
        new DeleteItemCommand({
          TableName: table,
          Key: { LockID: S("job-1") },
          ConditionExpression: "#owner = :me",
          ExpressionAttributeNames: { "#owner": "Owner" },
          ExpressionAttributeValues: { ":me": S("b") },
        }),
      ),
      { name: "ConditionalCheckFailedException", status: 400 },
    );
    await ddb.send(
      new DeleteItemCommand({
        TableName: table,
        Key: { LockID: S("job-1") },
        ConditionExpression: "#owner = :me",
        ExpressionAttributeNames: { "#owner": "Owner" },
        ExpressionAttributeValues: { ":me": S("a") },
      }),
    );
    expect((await ddb.send(new GetItemCommand({ TableName: table, Key: { LockID: S("job-1") } }))).Item).toBeUndefined();
  });

  test("concurrent acquirers: exactly one wins", async () => {
    const table = await lockTable();
    const results = await Promise.allSettled(Array.from({ length: 10 }, (_, i) => acquire(table, `w${i}`, 9000, 1000)));
    expect(results.filter((r) => r.status === "fulfilled")).toHaveLength(1);
    const failed = results.filter((r): r is PromiseRejectedResult => r.status === "rejected");
    expect(failed).toHaveLength(9);
    expect(failed.every((r) => (r.reason as { name?: string }).name === "ConditionalCheckFailedException")).toBe(true);
  });

  test("a ConditionExpression that references an unused ExpressionAttributeValue fails ValidationException", async () => {
    const table = await lockTable();
    await expectFailure(
      ddb.send(
        new PutItemCommand({
          TableName: table,
          Item: { LockID: S("k") },
          ConditionExpression: "attribute_not_exists(LockID)",
          ExpressionAttributeValues: { ":unused": N(1) },
        }),
      ),
      { name: "ValidationException", status: 400 },
    );
  });
});

describe.skipIf(dynamoDbOptedOut)("DynamoDB UpdateItem", () => {
  test("SET, ADD, REMOVE and if_not_exists apply and UPDATED_NEW returns the changed attributes", async () => {
    const table = await makeTable();
    await ddb.send(new PutItemCommand({ TableName: table, Item: { pk: S("k"), count: N(1), gone: S("x"), tags: { SS: ["a"] } } }));
    const out = await ddb.send(
      new UpdateItemCommand({
        TableName: table,
        Key: { pk: S("k") },
        UpdateExpression: "SET label = :label, created = if_not_exists(created, :created) ADD #count :inc, tags :more REMOVE gone",
        ExpressionAttributeNames: { "#count": "count" },
        ExpressionAttributeValues: { ":label": S("L"), ":created": N(100), ":inc": N(4), ":more": { SS: ["b"] } },
        ReturnValues: "UPDATED_NEW",
      }),
    );
    expect(out.Attributes).toMatchObject({ label: S("L"), created: N(100), count: N(5) });
    const item = (await ddb.send(new GetItemCommand({ TableName: table, Key: { pk: S("k") } }))).Item;
    expect(item?.gone).toBeUndefined();
    expect([...(item?.tags?.SS ?? [])].sort()).toEqual(["a", "b"]);
  });

  test("UpdateItem creates the item when it does not exist", async () => {
    const table = await makeTable();
    await ddb.send(new UpdateItemCommand({ TableName: table, Key: { pk: S("new") }, UpdateExpression: "SET v = :v", ExpressionAttributeValues: { ":v": S("created") } }));
    expect((await ddb.send(new GetItemCommand({ TableName: table, Key: { pk: S("new") } }))).Item).toEqual({ pk: S("new"), v: S("created") });
  });

  test("list_append and list index updates", async () => {
    const table = await makeTable();
    await ddb.send(new PutItemCommand({ TableName: table, Item: { pk: S("k"), items: { L: [S("a")] } } }));
    // ITEMS is a DynamoDB reserved word, and one UpdateExpression may not
    // name both a list and one of its elements (overlapping document paths),
    // so the append and the index update are two calls.
    await ddb.send(
      new UpdateItemCommand({
        TableName: table,
        Key: { pk: S("k") },
        UpdateExpression: "SET #items = list_append(#items, :more)",
        ExpressionAttributeNames: { "#items": "items" },
        ExpressionAttributeValues: { ":more": { L: [S("b"), S("c")] } },
      }),
    );
    await ddb.send(
      new UpdateItemCommand({
        TableName: table,
        Key: { pk: S("k") },
        UpdateExpression: "SET #items[0] = :first",
        ExpressionAttributeNames: { "#items": "items" },
        ExpressionAttributeValues: { ":first": S("A") },
      }),
    );
    expect((await ddb.send(new GetItemCommand({ TableName: table, Key: { pk: S("k") } }))).Item?.items).toEqual({ L: [S("A"), S("b"), S("c")] });
  });

  test("a failed condition leaves the item unchanged", async () => {
    const table = await makeTable();
    await ddb.send(new PutItemCommand({ TableName: table, Item: { pk: S("k"), v: N(1) } }));
    await expectFailure(
      ddb.send(
        new UpdateItemCommand({
          TableName: table,
          Key: { pk: S("k") },
          UpdateExpression: "SET v = :new",
          ConditionExpression: "v = :expected",
          ExpressionAttributeValues: { ":new": N(2), ":expected": N(99) },
        }),
      ),
      { name: "ConditionalCheckFailedException", status: 400 },
    );
    expect((await ddb.send(new GetItemCommand({ TableName: table, Key: { pk: S("k") } }))).Item?.v).toEqual(N(1));
  });

  test("updating a key attribute fails ValidationException", async () => {
    const table = await makeTable();
    await ddb.send(new PutItemCommand({ TableName: table, Item: { pk: S("k") } }));
    await expectFailure(
      ddb.send(new UpdateItemCommand({ TableName: table, Key: { pk: S("k") }, UpdateExpression: "SET pk = :v", ExpressionAttributeValues: { ":v": S("other") } })),
      { name: "ValidationException", status: 400 },
    );
  });

  test("a malformed UpdateExpression fails ValidationException", async () => {
    const table = await makeTable();
    await expectFailure(ddb.send(new UpdateItemCommand({ TableName: table, Key: { pk: S("k") }, UpdateExpression: "SET = broken" })), {
      name: "ValidationException",
      status: 400,
    });
  });

  test("concurrent increments are all applied", async () => {
    const table = await makeTable();
    await ddb.send(new PutItemCommand({ TableName: table, Item: { pk: S("counter"), n: N(0) } }));
    await Promise.all(
      Array.from({ length: 20 }, () =>
        ddb.send(new UpdateItemCommand({ TableName: table, Key: { pk: S("counter") }, UpdateExpression: "ADD n :one", ExpressionAttributeValues: { ":one": N(1) } })),
      ),
    );
    expect((await ddb.send(new GetItemCommand({ TableName: table, Key: { pk: S("counter") }, ConsistentRead: true }))).Item?.n).toEqual(N(20));
  });
});

describe.skipIf(dynamoDbOptedOut)("DynamoDB Query and Scan", () => {
  async function seeded() {
    const table = await makeRangeTable();
    const rows = [];
    for (const pk of ["a", "b"]) {
      for (let sk = 1; sk <= 5; sk += 1) rows.push({ pk: S(pk), sk: N(sk), color: S(sk % 2 === 0 ? "red" : "blue"), label: S(`${pk}-${sk}`) });
    }
    await ddb.send(new BatchWriteItemCommand({ RequestItems: { [table]: rows.slice(0, 10).map((Item) => ({ PutRequest: { Item } })) } }));
    return table;
  }

  test("Query by partition key returns that partition in ascending sort-key order", async () => {
    const table = await seeded();
    const out = await ddb.send(new QueryCommand({ TableName: table, KeyConditionExpression: "pk = :p", ExpressionAttributeValues: { ":p": S("a") } }));
    expect(out.Items?.map((i) => i.sk?.N)).toEqual(["1", "2", "3", "4", "5"]);
    expect(out.Count).toBe(5);
    expect(out.ScannedCount).toBe(5);
  });

  test("ScanIndexForward false reverses the order", async () => {
    const table = await seeded();
    const out = await ddb.send(
      new QueryCommand({ TableName: table, KeyConditionExpression: "pk = :p", ExpressionAttributeValues: { ":p": S("a") }, ScanIndexForward: false }),
    );
    expect(out.Items?.map((i) => i.sk?.N)).toEqual(["5", "4", "3", "2", "1"]);
  });

  test("sort key conditions: between, >, and begins_with on a string sort key", async () => {
    const table = await seeded();
    const between = await ddb.send(
      new QueryCommand({
        TableName: table,
        KeyConditionExpression: "pk = :p AND sk BETWEEN :lo AND :hi",
        ExpressionAttributeValues: { ":p": S("a"), ":lo": N(2), ":hi": N(4) },
      }),
    );
    expect(between.Items?.map((i) => i.sk?.N)).toEqual(["2", "3", "4"]);
    const greater = await ddb.send(
      new QueryCommand({ TableName: table, KeyConditionExpression: "pk = :p AND sk > :v", ExpressionAttributeValues: { ":p": S("a"), ":v": N(3) } }),
    );
    expect(greater.Items?.map((i) => i.sk?.N)).toEqual(["4", "5"]);
    const strTable = await makeTable({
      AttributeDefinitions: [
        { AttributeName: "pk", AttributeType: "S" },
        { AttributeName: "sk", AttributeType: "S" },
      ],
      KeySchema: [
        { AttributeName: "pk", KeyType: "HASH" },
        { AttributeName: "sk", KeyType: "RANGE" },
      ],
    });
    for (const sk of ["log#1", "log#2", "meta#1"]) await ddb.send(new PutItemCommand({ TableName: strTable, Item: { pk: S("p"), sk: S(sk) } }));
    const begins = await ddb.send(
      new QueryCommand({ TableName: strTable, KeyConditionExpression: "pk = :p AND begins_with(sk, :b)", ExpressionAttributeValues: { ":p": S("p"), ":b": S("log#") } }),
    );
    expect(begins.Items?.map((i) => i.sk?.S)).toEqual(["log#1", "log#2"]);
  });

  test("FilterExpression filters after the read: Count shrinks, ScannedCount does not", async () => {
    const table = await seeded();
    const out = await ddb.send(
      new QueryCommand({
        TableName: table,
        KeyConditionExpression: "pk = :p",
        FilterExpression: "color = :c",
        ExpressionAttributeValues: { ":p": S("a"), ":c": S("red") },
      }),
    );
    expect(out.Items?.map((i) => i.sk?.N)).toEqual(["2", "4"]);
    expect(out.Count).toBe(2);
    expect(out.ScannedCount).toBe(5);
  });

  test("Limit paginates with LastEvaluatedKey and ExclusiveStartKey without gaps or repeats", async () => {
    const table = await seeded();
    const seen: string[] = [];
    let start: Record<string, AttributeValue> | undefined;
    let pages = 0;
    do {
      const out = await ddb.send(
        new QueryCommand({ TableName: table, KeyConditionExpression: "pk = :p", ExpressionAttributeValues: { ":p": S("b") }, Limit: 2, ExclusiveStartKey: start }),
      );
      seen.push(...(out.Items ?? []).map((i) => i.sk?.N ?? ""));
      start = out.LastEvaluatedKey;
      pages += 1;
    } while (start !== undefined && pages < 10);
    expect(seen).toEqual(["1", "2", "3", "4", "5"]);
    expect(pages).toBe(3);
  });

  test("Select COUNT returns only the count", async () => {
    const table = await seeded();
    const out = await ddb.send(
      new QueryCommand({ TableName: table, KeyConditionExpression: "pk = :p", ExpressionAttributeValues: { ":p": S("a") }, Select: "COUNT" }),
    );
    expect(out.Count).toBe(5);
    expect(out.Items).toBeUndefined();
  });

  test("Scan returns every item and a FilterExpression narrows it", async () => {
    const table = await seeded();
    expect((await ddb.send(new ScanCommand({ TableName: table }))).Count).toBe(10);
    const red = await ddb.send(new ScanCommand({ TableName: table, FilterExpression: "color = :c", ExpressionAttributeValues: { ":c": S("red") } }));
    expect(red.Count).toBe(4);
    expect(red.ScannedCount).toBe(10);
  });

  test("a Query without the partition key condition fails ValidationException", async () => {
    const table = await seeded();
    await expectFailure(
      ddb.send(new QueryCommand({ TableName: table, KeyConditionExpression: "sk = :v", ExpressionAttributeValues: { ":v": N(1) } })),
      { name: "ValidationException", status: 400 },
    );
  });

  test("a global secondary index is queryable with the projected attributes", async () => {
    const table = await makeTable({
      AttributeDefinitions: [
        { AttributeName: "pk", AttributeType: "S" },
        { AttributeName: "gsi", AttributeType: "S" },
      ],
      GlobalSecondaryIndexes: [
        { IndexName: "by-gsi", KeySchema: [{ AttributeName: "gsi", KeyType: "HASH" }], Projection: { ProjectionType: "ALL" } },
      ],
    });
    await ddb.send(new PutItemCommand({ TableName: table, Item: { pk: S("1"), gsi: S("x"), v: S("one") } }));
    await ddb.send(new PutItemCommand({ TableName: table, Item: { pk: S("2"), gsi: S("y"), v: S("two") } }));
    const out = await ddb.send(
      new QueryCommand({ TableName: table, IndexName: "by-gsi", KeyConditionExpression: "gsi = :g", ExpressionAttributeValues: { ":g": S("x") } }),
    );
    expect(out.Items).toEqual([{ pk: S("1"), gsi: S("x"), v: S("one") }]);
    const described = (await ddb.send(new DescribeTableCommand({ TableName: table }))).Table;
    expect(described?.GlobalSecondaryIndexes?.[0]).toMatchObject({ IndexName: "by-gsi", IndexStatus: "ACTIVE" });
  });

  test("Query on an index that does not exist fails ValidationException", async () => {
    const table = await makeTable();
    await expectFailure(
      ddb.send(new QueryCommand({ TableName: table, IndexName: "nope", KeyConditionExpression: "pk = :p", ExpressionAttributeValues: { ":p": S("a") } })),
      { name: "ValidationException", status: 400 },
    );
  });
});

describe.skipIf(dynamoDbOptedOut)("DynamoDB batch and transactions", () => {
  test("BatchWriteItem puts and deletes, and BatchGetItem reads back", async () => {
    const table = await makeTable();
    await ddb.send(
      new BatchWriteItemCommand({ RequestItems: { [table]: ["a", "b", "c"].map((k) => ({ PutRequest: { Item: { pk: S(k), v: S(`v-${k}`) } } })) } }),
    );
    await ddb.send(new BatchWriteItemCommand({ RequestItems: { [table]: [{ DeleteRequest: { Key: { pk: S("b") } } }] } }));
    const got = await ddb.send(new BatchGetItemCommand({ RequestItems: { [table]: { Keys: ["a", "b", "c"].map((k) => ({ pk: S(k) })) } } }));
    expect((got.Responses?.[table] ?? []).map((i) => i.pk?.S).sort()).toEqual(["a", "c"]);
    expect(got.UnprocessedKeys ?? {}).toEqual({});
  });

  test("BatchWriteItem with more than 25 requests fails ValidationException", async () => {
    const table = await makeTable();
    const requests = Array.from({ length: 26 }, (_, i) => ({ PutRequest: { Item: { pk: S(`k${i}`) } } }));
    await expectFailure(ddb.send(new BatchWriteItemCommand({ RequestItems: { [table]: requests } })), { name: "ValidationException", status: 400 });
  });

  test("TransactWriteItems applies every action atomically", async () => {
    const table = await makeTable();
    await ddb.send(new PutItemCommand({ TableName: table, Item: { pk: S("existing"), v: N(1) } }));
    await ddb.send(
      new TransactWriteItemsCommand({
        TransactItems: [
          { Put: { TableName: table, Item: { pk: S("new"), v: N(10) } } },
          { Update: { TableName: table, Key: { pk: S("existing") }, UpdateExpression: "SET v = v + :one", ExpressionAttributeValues: { ":one": N(1) } } },
        ],
      }),
    );
    expect((await ddb.send(new GetItemCommand({ TableName: table, Key: { pk: S("new") } }))).Item?.v).toEqual(N(10));
    expect((await ddb.send(new GetItemCommand({ TableName: table, Key: { pk: S("existing") } }))).Item?.v).toEqual(N(2));
  });

  test("a failed condition cancels the whole transaction with per-item CancellationReasons", async () => {
    const table = await makeTable();
    await ddb.send(new PutItemCommand({ TableName: table, Item: { pk: S("taken") } }));
    const failure = await expectFailure(
      ddb.send(
        new TransactWriteItemsCommand({
          TransactItems: [
            { Put: { TableName: table, Item: { pk: S("fresh") } } },
            { Put: { TableName: table, Item: { pk: S("taken") }, ConditionExpression: "attribute_not_exists(pk)" } },
          ],
        }),
      ),
      { name: "TransactionCanceledException", status: 400 },
    );
    const reasons = failure.error.CancellationReasons as Array<{ Code?: string }>;
    expect(reasons.map((r) => r.Code)).toEqual(["None", "ConditionalCheckFailed"]);
    expect((await ddb.send(new GetItemCommand({ TableName: table, Key: { pk: S("fresh") } }))).Item).toBeUndefined();
  });

  test("TransactGetItems reads several items consistently", async () => {
    const table = await makeTable();
    await ddb.send(new PutItemCommand({ TableName: table, Item: { pk: S("a"), v: S("1") } }));
    await ddb.send(new PutItemCommand({ TableName: table, Item: { pk: S("b"), v: S("2") } }));
    const out = await ddb.send(
      new TransactGetItemsCommand({
        TransactItems: [{ Get: { TableName: table, Key: { pk: S("a") } } }, { Get: { TableName: table, Key: { pk: S("missing") } } }, { Get: { TableName: table, Key: { pk: S("b") } } }],
      }),
    );
    expect(out.Responses?.map((r) => r.Item?.v?.S)).toEqual(["1", undefined, "2"]);
  });

  test("a ConditionCheck item in a transaction guards the other writes", async () => {
    const table = await makeTable();
    await ddb.send(new PutItemCommand({ TableName: table, Item: { pk: S("flag"), on: { BOOL: false } } }));
    await expectFailure(
      ddb.send(
        new TransactWriteItemsCommand({
          TransactItems: [
            { ConditionCheck: { TableName: table, Key: { pk: S("flag") }, ConditionExpression: "#on = :t", ExpressionAttributeNames: { "#on": "on" }, ExpressionAttributeValues: { ":t": { BOOL: true } } } },
            { Put: { TableName: table, Item: { pk: S("guarded") } } },
          ],
        }),
      ),
      { name: "TransactionCanceledException", status: 400 },
    );
    expect((await ddb.send(new GetItemCommand({ TableName: table, Key: { pk: S("guarded") } }))).Item).toBeUndefined();
  });
});

describe.skipIf(dynamoDbOptedOut)("DynamoDB document client", () => {
  test("native JavaScript values round trip through the marshaller", async () => {
    const table = await makeTable();
    const item = { pk: "doc", n: 42, f: 1.5, s: "text", b: true, nil: null, list: [1, "two", { three: 3 }], map: { a: { b: "c" } }, set: new Set(["x", "y"]) };
    await doc.send(new PutCommand({ TableName: table, Item: item }));
    const got = (await doc.send(new GetCommand({ TableName: table, Key: { pk: "doc" } }))).Item;
    expect(got).toMatchObject({ pk: "doc", n: 42, f: 1.5, s: "text", b: true, nil: null, list: [1, "two", { three: 3 }], map: { a: { b: "c" } } });
    expect([...(got?.set as Set<string>)].sort()).toEqual(["x", "y"]);
  });

  test("UpdateCommand and DeleteCommand work with native values", async () => {
    const table = await makeTable();
    await doc.send(new PutCommand({ TableName: table, Item: { pk: "k", n: 1 } }));
    const updated = await doc.send(
      new UpdateCommand({ TableName: table, Key: { pk: "k" }, UpdateExpression: "SET n = n + :d", ExpressionAttributeValues: { ":d": 9 }, ReturnValues: "ALL_NEW" }),
    );
    expect(updated.Attributes).toEqual({ pk: "k", n: 10 });
    await doc.send(new DeleteCommand({ TableName: table, Key: { pk: "k" } }));
    expect((await doc.send(new GetCommand({ TableName: table, Key: { pk: "k" } }))).Item).toBeUndefined();
  });

  test("QueryCommand paginates native items", async () => {
    const table = await makeRangeTable();
    for (let i = 1; i <= 4; i += 1) await doc.send(new PutCommand({ TableName: table, Item: { pk: "p", sk: i } }));
    const out = await doc.send(new DocQueryCommand({ TableName: table, KeyConditionExpression: "pk = :p", ExpressionAttributeValues: { ":p": "p" }, Limit: 3 }));
    expect(out.Items?.map((i) => i.sk)).toEqual([1, 2, 3]);
    expect(out.LastEvaluatedKey).toEqual({ pk: "p", sk: 3 });
  });
});

describe.skipIf(dynamoDbOptedOut)("DynamoDB protocol errors", () => {
  test("an unknown table in a Query fails ResourceNotFoundException before any validation of the expression", async () => {
    const failure = await awsFailure(
      ddb.send(new QueryCommand({ TableName: uniq("missing"), KeyConditionExpression: "pk = :p", ExpressionAttributeValues: { ":p": S("a") } })),
    );
    expect(failure.name).toBe("ResourceNotFoundException");
  });
});
