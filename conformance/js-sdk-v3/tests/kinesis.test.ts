// Kinesis Data Streams over awsJson1_1, through @aws-sdk/client-kinesis on an
// HTTP/1.1 handler (support/kinesis.ts says why). Expected behavior: real
// Kinesis as documented in docs/research/kinesis.md sections 3 to 6, 10 and 13.

import { afterAll, describe, expect, test } from "bun:test";
import {
  AddTagsToStreamCommand,
  DeleteStreamCommand,
  DescribeStreamCommand,
  DescribeStreamSummaryCommand,
  GetRecordsCommand,
  GetShardIteratorCommand,
  ListShardsCommand,
  ListTagsForStreamCommand,
  PutRecordCommand,
  PutRecordsCommand,
  SplitShardCommand,
  waitUntilStreamNotExists,
} from "@aws-sdk/client-kinesis";
import { defaultAccount, region } from "../support/env";
import { expectFailure, uniq } from "../support/helpers";
import { createStream, kinesisClient } from "../support/kinesis";

const kinesis = kinesisClient();
const streams: string[] = [];

afterAll(async () => {
  for (const name of streams) await kinesis.send(new DeleteStreamCommand({ StreamName: name })).catch(() => undefined);
});

async function newStream(shards?: number): Promise<string> {
  const name = uniq("conf-kin");
  await createStream(kinesis, name, shards);
  streams.push(name);
  return name;
}

const encoder = new TextEncoder();
const decoder = new TextDecoder();

describe("Kinesis Data Streams", () => {
  test("DescribeStreamSummary reports a provisioned stream", async () => {
    const name = await newStream(2);
    const s = (await kinesis.send(new DescribeStreamSummaryCommand({ StreamName: name }))).StreamDescriptionSummary;
    // kinesis.md 3.1 (ARN form), 4.4 (required members), 4.7 (24 hours by default), 10.3
    // (PROVISIONED and two open shards for a config with only a shard count).
    expect(s).toMatchObject({
      StreamName: name,
      StreamARN: `arn:aws:kinesis:${region}:${defaultAccount}:stream/${name}`,
      StreamStatus: "ACTIVE",
      RetentionPeriodHours: 24,
      OpenShardCount: 2,
      StreamModeDetails: { StreamMode: "PROVISIONED" },
    });
    expect(s?.StreamCreationTimestamp).toBeInstanceOf(Date);
    expect((s?.EnhancedMonitoring ?? []).flatMap((e) => e.ShardLevelMetrics ?? [])).toEqual([]);
  });

  test("a stream created with only a name has four shards", async () => {
    // kinesis.md finding 4 (aws-recorded, LS test_kinesis.snapshot.json:L2-L47).
    const name = await newStream();
    const shards = (await kinesis.send(new ListShardsCommand({ StreamName: name }))).Shards ?? [];
    expect(shards.map((s) => s.ShardId)).toEqual(["shardId-000000000000", "shardId-000000000001", "shardId-000000000002", "shardId-000000000003"]);
  });

  test("three shards split the hash key space as the documented example does", async () => {
    // kinesis.md 3.3: the DescribeStream example's ranges; 3.4: open shards have no EndingSequenceNumber.
    const name = await newStream(3);
    const shards = (await kinesis.send(new ListShardsCommand({ StreamName: name }))).Shards ?? [];
    expect(shards.map((s) => [s.ShardId, s.HashKeyRange?.StartingHashKey, s.HashKeyRange?.EndingHashKey])).toEqual([
      ["shardId-000000000000", "0", "113427455640312821154458202477256070484"],
      ["shardId-000000000001", "113427455640312821154458202477256070485", "226854911280625642308916404954512140969"],
      ["shardId-000000000002", "226854911280625642308916404954512140970", "340282366920938463463374607431768211455"],
    ]);
    expect(shards.every((s) => s.SequenceNumberRange?.EndingSequenceNumber === undefined)).toBe(true);
  });

  test("a missing stream is named with its account, even when addressed by ARN", async () => {
    // kinesis.md finding 5 (aws-recorded over CBOR, LS test_kinesis.snapshot.json:L230-L237).
    const name = uniq("conf-kin-missing");
    const failure = await expectFailure(
      kinesis.send(new DescribeStreamCommand({ StreamARN: `arn:aws:kinesis:${region}:${defaultAccount}:stream/${name}` })),
      { name: "ResourceNotFoundException", status: 400 },
    );
    expect(failure.message).toBe(`Stream ${name} under account ${defaultAccount} not found.`);
  });

  test("tags added to a stream are listed", async () => {
    // kinesis.md 4.10 (aws-recorded, LS test_kinesis.snapshot.json:L49-L57).
    const name = await newStream(1);
    await kinesis.send(new AddTagsToStreamCommand({ StreamName: name, Tags: { foo: "bar" } }));
    const tags = await kinesis.send(new ListTagsForStreamCommand({ StreamName: name }));
    expect(tags.Tags).toEqual([{ Key: "foo", Value: "bar" }]);
    expect(tags.HasMoreTags).toBe(false);
  });

  test("records put are read back in order from TRIM_HORIZON", async () => {
    const name = await newStream(1);
    const first = await kinesis.send(new PutRecordCommand({ StreamName: name, PartitionKey: "k1", Data: encoder.encode("hello") }));
    expect(first.ShardId).toBe("shardId-000000000000");
    // kinesis.md 5.2: one result per entry, in request order; 5.1: empty data is legal
    // (aws-recorded, LS test_kinesis.snapshot.json:L58-L67).
    const batch = await kinesis.send(new PutRecordsCommand({
      StreamName: name,
      Records: [{ PartitionKey: "k2", Data: new Uint8Array() }, { PartitionKey: "k3", Data: encoder.encode("three") }],
    }));
    expect(batch.FailedRecordCount ?? 0).toBe(0);
    expect(batch.Records?.length).toBe(2);

    const it = await kinesis.send(new GetShardIteratorCommand({ StreamName: name, ShardId: "shardId-000000000000", ShardIteratorType: "TRIM_HORIZON" }));
    const page = await kinesis.send(new GetRecordsCommand({ ShardIterator: it.ShardIterator }));
    const records = page.Records ?? [];
    expect(records.map((r) => [r.PartitionKey, decoder.decode(r.Data)])).toEqual([["k1", "hello"], ["k2", ""], ["k3", "three"]]);
    expect(records[0]?.SequenceNumber).toBe(first.SequenceNumber);
    // kinesis.md 4.11 (aws-recorded, LS test_kinesis.snapshot.json:L58-L88): no EncryptionType
    // on an unencrypted stream's records.
    expect(records.every((r) => r.EncryptionType === undefined)).toBe(true);
    // kinesis.md 3.4: increasing decimal sequence numbers within a shard.
    const seqs = records.map((r) => BigInt(r.SequenceNumber ?? "0"));
    expect(seqs.every((s, i) => i === 0 || s > (seqs[i - 1] ?? 0n))).toBe(true);
    // kinesis.md 5.5: an open shard always answers a NextShardIterator; an empty read returns
    // Records [] (aws-recorded, LS test_kinesis.py:L552-L575).
    const tail = await kinesis.send(new GetRecordsCommand({ ShardIterator: page.NextShardIterator }));
    expect(tail.Records).toEqual([]);
    expect(tail.NextShardIterator).toBeTruthy();
  });

  test("resharding answers 501 rather than being accepted and ignored", async () => {
    // kinesis.md 13.1 and 13.9.
    const name = await newStream(1);
    await expectFailure(
      kinesis.send(new SplitShardCommand({ StreamName: name, ShardToSplit: "shardId-000000000000", NewStartingHashKey: "170141183460469231731687303715884105728" })),
      { name: "NotImplemented", status: 501 },
    );
  });

  test("a deleted stream is gone", async () => {
    // kinesis.md 4.3 and 10.1; the model's StreamNotExists waiter.
    const name = uniq("conf-kin");
    await createStream(kinesis, name, 1);
    await kinesis.send(new DeleteStreamCommand({ StreamName: name }));
    await waitUntilStreamNotExists({ client: kinesis, maxWaitTime: 120, minDelay: 1 }, { StreamName: name });
    await expectFailure(kinesis.send(new DescribeStreamSummaryCommand({ StreamName: name })), { name: "ResourceNotFoundException", status: 400 });
  });
});
