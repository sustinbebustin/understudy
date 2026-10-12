// CloudWatch Logs over awsJson1_1, through @aws-sdk/client-cloudwatch-logs.
// Expected behavior: docs/research/phase1/secrets-logs-events-alarms.md section 3
// and logs-filter-patterns.md.

import { describe, expect, test } from "bun:test";
import {
  CloudWatchLogsClient,
  CreateLogGroupCommand,
  CreateLogStreamCommand,
  DeleteLogGroupCommand,
  DeleteLogStreamCommand,
  DeleteRetentionPolicyCommand,
  DescribeLogGroupsCommand,
  DescribeLogStreamsCommand,
  FilterLogEventsCommand,
  GetLogEventsCommand,
  ListTagsForResourceCommand,
  ListTagsLogGroupCommand,
  PutLogEventsCommand,
  PutRetentionPolicyCommand,
  TagLogGroupCommand,
  TagResourceCommand,
  UntagLogGroupCommand,
  UntagResourceCommand,
  type OutputLogEvent,
} from "@aws-sdk/client-cloudwatch-logs";
import { clientConfig, defaultAccount, region } from "../support/env";
import { awsFailure, expectFailure, uniq } from "../support/helpers";

const logs = new CloudWatchLogsClient(clientConfig());

const groupName = () => `/conformance/${uniq("group")}`;

async function makeGroup(extra: Partial<ConstructorParameters<typeof CreateLogGroupCommand>[0]> = {}): Promise<string> {
  const name = groupName();
  await logs.send(new CreateLogGroupCommand({ logGroupName: name, ...extra }));
  return name;
}

async function makeStream(group: string, name = uniq("stream")): Promise<string> {
  await logs.send(new CreateLogStreamCommand({ logGroupName: group, logStreamName: name }));
  return name;
}

/** A group and stream holding the given messages, one second apart ending now. */
async function seeded(messages: string[]): Promise<{ group: string; stream: string; times: number[] }> {
  const group = await makeGroup();
  const stream = await makeStream(group);
  const now = Date.now();
  const times = messages.map((_, i) => now - (messages.length - i) * 1000);
  await logs.send(
    new PutLogEventsCommand({
      logGroupName: group,
      logStreamName: stream,
      logEvents: messages.map((message, i) => ({ timestamp: times[i], message })),
    }),
  );
  return { group, stream, times };
}

async function filter(group: string, pattern: string): Promise<string[]> {
  const out = await logs.send(new FilterLogEventsCommand({ logGroupName: group, filterPattern: pattern }));
  return (out.events ?? []).map((e) => e.message ?? "");
}

describe("CloudWatch Logs: groups", () => {
  test("DescribeLogGroups returns the group with both ARN forms and the documented defaults", async () => {
    const name = await makeGroup();
    const out = await logs.send(new DescribeLogGroupsCommand({ logGroupNamePrefix: name }));
    expect(out.logGroups).toHaveLength(1);
    const group = out.logGroups?.[0];
    expect(group).toMatchObject({
      logGroupName: name,
      arn: `arn:aws:logs:${region}:${defaultAccount}:log-group:${name}:*`,
      logGroupArn: `arn:aws:logs:${region}:${defaultAccount}:log-group:${name}`,
      logGroupClass: "STANDARD",
      metricFilterCount: 0,
      storedBytes: 0,
    });
    expect(group?.creationTime).toBeGreaterThan(0);
    expect(group?.retentionInDays).toBeUndefined();
    expect(group?.kmsKeyId).toBeUndefined();
  });

  test("CreateLogGroup on an existing name fails ResourceAlreadyExistsException", async () => {
    const name = await makeGroup();
    await expectFailure(logs.send(new CreateLogGroupCommand({ logGroupName: name })), {
      name: "ResourceAlreadyExistsException",
      status: 400,
      messageIncludes: "The specified log group already exists",
    });
  });

  test("CreateLogGroup with an over-length name fails InvalidParameterException", async () => {
    await expectFailure(logs.send(new CreateLogGroupCommand({ logGroupName: `/${"a".repeat(512)}` })), {
      name: "InvalidParameterException",
      status: 400,
    });
  });

  test("CreateLogGroup with a character outside the allowed set fails", async () => {
    const failure = await awsFailure(logs.send(new CreateLogGroupCommand({ logGroupName: "/bad name with spaces" })));
    expect(failure.status).toBe(400);
    expect(["InvalidParameterException", "ValidationException"]).toContain(failure.name);
  });

  test("tags given at creation are listed by ListTagsLogGroup and ListTagsForResource", async () => {
    const name = await makeGroup({ tags: { env: "test", team: "a" } });
    expect((await logs.send(new ListTagsLogGroupCommand({ logGroupName: name }))).tags).toEqual({ env: "test", team: "a" });
    const arn = `arn:aws:logs:${region}:${defaultAccount}:log-group:${name}`;
    expect((await logs.send(new ListTagsForResourceCommand({ resourceArn: arn }))).tags).toEqual({ env: "test", team: "a" });
  });

  test("tags added through either API family appear in both", async () => {
    const name = await makeGroup();
    const arn = `arn:aws:logs:${region}:${defaultAccount}:log-group:${name}`;
    await logs.send(new TagLogGroupCommand({ logGroupName: name, tags: { legacy: "1" } }));
    await logs.send(new TagResourceCommand({ resourceArn: arn, tags: { modern: "2" } }));
    expect((await logs.send(new ListTagsForResourceCommand({ resourceArn: arn }))).tags).toEqual({ legacy: "1", modern: "2" });
    expect((await logs.send(new ListTagsLogGroupCommand({ logGroupName: name }))).tags).toEqual({ legacy: "1", modern: "2" });
    await logs.send(new UntagLogGroupCommand({ logGroupName: name, tags: ["legacy"] }));
    await logs.send(new UntagResourceCommand({ resourceArn: arn, tagKeys: ["modern"] }));
    expect((await logs.send(new ListTagsLogGroupCommand({ logGroupName: name }))).tags ?? {}).toEqual({});
  });

  test("DescribeLogGroups filters by prefix, sorts by name and honors limit with nextToken", async () => {
    const prefix = `/conformance/${uniq("page")}`;
    for (const suffix of ["c", "a", "b"]) await logs.send(new CreateLogGroupCommand({ logGroupName: `${prefix}-${suffix}` }));
    const first = await logs.send(new DescribeLogGroupsCommand({ logGroupNamePrefix: prefix, limit: 2 }));
    expect(first.logGroups?.map((g) => g.logGroupName)).toEqual([`${prefix}-a`, `${prefix}-b`]);
    expect(first.nextToken).toBeDefined();
    const second = await logs.send(new DescribeLogGroupsCommand({ logGroupNamePrefix: prefix, limit: 2, nextToken: first.nextToken }));
    expect(second.logGroups?.map((g) => g.logGroupName)).toEqual([`${prefix}-c`]);
    expect(second.nextToken).toBeUndefined();
  });

  test("DescribeLogGroups filters by a case-sensitive name pattern", async () => {
    const marker = uniq("pat");
    const name = `/conformance/${marker}-needle`;
    await logs.send(new CreateLogGroupCommand({ logGroupName: name }));
    const hit = await logs.send(new DescribeLogGroupsCommand({ logGroupNamePattern: `${marker}-needle` }));
    expect(hit.logGroups?.map((g) => g.logGroupName)).toEqual([name]);
    const miss = await logs.send(new DescribeLogGroupsCommand({ logGroupNamePattern: `${marker}-NEEDLE` }));
    expect(miss.logGroups ?? []).toEqual([]);
  });

  test("DescribeLogGroups with both a prefix and a pattern fails InvalidParameterException", async () => {
    await expectFailure(logs.send(new DescribeLogGroupsCommand({ logGroupNamePrefix: "/a", logGroupNamePattern: "a" })), {
      name: "InvalidParameterException",
      status: 400,
      messageIncludes: "LogGroup name prefix and LogGroup name pattern are mutually exclusive parameters.",
    });
  });

  test("DeleteLogGroup removes the group, its streams and events, and a second delete fails ResourceNotFoundException", async () => {
    const { group } = await seeded(["x"]);
    await logs.send(new DeleteLogGroupCommand({ logGroupName: group }));
    expect((await logs.send(new DescribeLogGroupsCommand({ logGroupNamePrefix: group }))).logGroups ?? []).toEqual([]);
    await expectFailure(logs.send(new DeleteLogGroupCommand({ logGroupName: group })), {
      name: "ResourceNotFoundException",
      status: 400,
    });
  });
});

describe("CloudWatch Logs: retention", () => {
  test("PutRetentionPolicy sets retentionInDays and DeleteRetentionPolicy clears it", async () => {
    const group = await makeGroup();
    await logs.send(new PutRetentionPolicyCommand({ logGroupName: group, retentionInDays: 7 }));
    expect((await logs.send(new DescribeLogGroupsCommand({ logGroupNamePrefix: group }))).logGroups?.[0]?.retentionInDays).toBe(7);
    await logs.send(new DeleteRetentionPolicyCommand({ logGroupName: group }));
    expect((await logs.send(new DescribeLogGroupsCommand({ logGroupNamePrefix: group }))).logGroups?.[0]?.retentionInDays).toBeUndefined();
  });

  test("PutRetentionPolicy rejects a value that is not an allowed number of days", async () => {
    const group = await makeGroup();
    await expectFailure(logs.send(new PutRetentionPolicyCommand({ logGroupName: group, retentionInDays: 8 })), {
      name: "InvalidParameterException",
      status: 400,
    });
  });

  test("PutRetentionPolicy accepts every documented value", async () => {
    const group = await makeGroup();
    for (const days of [1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365, 400, 545, 731, 1096, 1827, 2192, 2557, 2922, 3288, 3653]) {
      await logs.send(new PutRetentionPolicyCommand({ logGroupName: group, retentionInDays: days }));
    }
  });

  test("PutRetentionPolicy on a missing group fails ResourceNotFoundException", async () => {
    await expectFailure(logs.send(new PutRetentionPolicyCommand({ logGroupName: groupName(), retentionInDays: 7 })), {
      name: "ResourceNotFoundException",
      status: 400,
    });
  });
});

describe("CloudWatch Logs: streams", () => {
  test("DescribeLogStreams returns an empty stream with only name, ARN, creation time and stored bytes", async () => {
    const group = await makeGroup();
    const stream = await makeStream(group);
    const out = await logs.send(new DescribeLogStreamsCommand({ logGroupName: group }));
    expect(out.logStreams).toHaveLength(1);
    const s = out.logStreams?.[0];
    expect(s).toMatchObject({
      logStreamName: stream,
      arn: `arn:aws:logs:${region}:${defaultAccount}:log-group:${group}:log-stream:${stream}`,
      storedBytes: 0,
    });
    expect(s?.creationTime).toBeGreaterThan(0);
    expect(s?.firstEventTimestamp).toBeUndefined();
    expect(s?.lastEventTimestamp).toBeUndefined();
  });

  test("DescribeLogStreams reports event timestamps after PutLogEvents", async () => {
    const { group, times } = await seeded(["one", "two"]);
    const s = (await logs.send(new DescribeLogStreamsCommand({ logGroupName: group }))).logStreams?.[0];
    expect(s?.firstEventTimestamp).toBe(times[0]);
    expect(s?.lastEventTimestamp).toBe(times[1]);
    expect(s?.lastIngestionTime).toBeGreaterThan(0);
  });

  test("DescribeLogStreams filters by prefix and orders by name, descending on request", async () => {
    const group = await makeGroup();
    for (const name of ["b-stream", "a-stream", "a-other"]) await makeStream(group, name);
    const byPrefix = await logs.send(new DescribeLogStreamsCommand({ logGroupName: group, logStreamNamePrefix: "a-" }));
    expect(byPrefix.logStreams?.map((s) => s.logStreamName)).toEqual(["a-other", "a-stream"]);
    const descending = await logs.send(new DescribeLogStreamsCommand({ logGroupName: group, descending: true }));
    expect(descending.logStreams?.map((s) => s.logStreamName)).toEqual(["b-stream", "a-stream", "a-other"]);
  });

  test("DescribeLogStreams ordered by LastEventTime cannot be combined with a name prefix", async () => {
    const group = await makeGroup();
    await expectFailure(logs.send(new DescribeLogStreamsCommand({ logGroupName: group, orderBy: "LastEventTime", logStreamNamePrefix: "a" })), {
      name: "InvalidParameterException",
      status: 400,
    });
  });

  test("CreateLogStream on an existing name fails ResourceAlreadyExistsException", async () => {
    const group = await makeGroup();
    const stream = await makeStream(group);
    await expectFailure(logs.send(new CreateLogStreamCommand({ logGroupName: group, logStreamName: stream })), {
      name: "ResourceAlreadyExistsException",
      status: 400,
      messageIncludes: "The specified log stream already exists",
    });
  });

  test("CreateLogStream in a missing group fails ResourceNotFoundException", async () => {
    await expectFailure(logs.send(new CreateLogStreamCommand({ logGroupName: groupName(), logStreamName: "s" })), {
      name: "ResourceNotFoundException",
      status: 400,
      messageIncludes: "The specified log group does not exist.",
    });
  });

  test("DeleteLogStream removes the stream and a second delete fails ResourceNotFoundException", async () => {
    const group = await makeGroup();
    const stream = await makeStream(group);
    await logs.send(new DeleteLogStreamCommand({ logGroupName: group, logStreamName: stream }));
    expect((await logs.send(new DescribeLogStreamsCommand({ logGroupName: group }))).logStreams ?? []).toEqual([]);
    await expectFailure(logs.send(new DeleteLogStreamCommand({ logGroupName: group, logStreamName: stream })), {
      name: "ResourceNotFoundException",
      status: 400,
    });
  });

  test("DescribeLogStreams with both a group name and a group identifier fails ValidationException", async () => {
    const group = await makeGroup();
    await expectFailure(
      logs.send(
        new DescribeLogStreamsCommand({
          logGroupName: group,
          logGroupIdentifier: `arn:aws:logs:${region}:${defaultAccount}:log-group:${group}`,
        }),
      ),
      { name: "ValidationException", status: 400, messageIncludes: "LogGroup name and LogGroup ARN are mutually exclusive parameters." },
    );
  });
});

describe("CloudWatch Logs: PutLogEvents and GetLogEvents", () => {
  test("PutLogEvents returns a nextSequenceToken and no rejected-events info for valid events", async () => {
    const group = await makeGroup();
    const stream = await makeStream(group);
    const out = await logs.send(
      new PutLogEventsCommand({ logGroupName: group, logStreamName: stream, logEvents: [{ timestamp: Date.now(), message: "hello" }] }),
    );
    expect(out.nextSequenceToken?.length ?? 0).toBeGreaterThan(0);
    expect(out.rejectedLogEventsInfo).toBeUndefined();
  });

  test("GetLogEvents returns the events oldest first with startFromHead, with ingestion times", async () => {
    const { group, stream, times } = await seeded(["first", "second", "third"]);
    const out = await logs.send(new GetLogEventsCommand({ logGroupName: group, logStreamName: stream, startFromHead: true }));
    expect(out.events?.map((e) => e.message)).toEqual(["first", "second", "third"]);
    expect(out.events?.map((e) => e.timestamp)).toEqual(times);
    expect(out.events?.every((e) => (e.ingestionTime ?? 0) > 0)).toBe(true);
  });

  test("GetLogEvents without startFromHead returns the newest events up to limit, still in ascending order", async () => {
    const { group, stream } = await seeded(["a", "b", "c", "d"]);
    const out = await logs.send(new GetLogEventsCommand({ logGroupName: group, logStreamName: stream, limit: 2 }));
    expect(out.events?.map((e) => e.message)).toEqual(["c", "d"]);
  });

  test("GetLogEvents treats startTime as inclusive and endTime as exclusive", async () => {
    const { group, stream, times } = await seeded(["a", "b", "c"]);
    const out = await logs.send(
      new GetLogEventsCommand({ logGroupName: group, logStreamName: stream, startFromHead: true, startTime: times[1], endTime: times[2] }),
    );
    expect(out.events?.map((e) => e.message)).toEqual(["b"]);
  });

  test("GetLogEvents tokens are never null and page forward to the end where the token repeats", async () => {
    const { group, stream } = await seeded(["a", "b", "c"]);
    const seen: string[] = [];
    let token: string | undefined;
    let last: string | undefined;
    for (let page = 0; page < 10; page += 1) {
      const out: { events?: OutputLogEvent[]; nextForwardToken?: string } = await logs.send(
        new GetLogEventsCommand({ logGroupName: group, logStreamName: stream, startFromHead: true, limit: 2, nextToken: token }),
      );
      seen.push(...(out.events ?? []).map((e) => e.message ?? ""));
      expect(out.nextForwardToken).toMatch(/^f\//);
      if (out.nextForwardToken === token) {
        last = out.nextForwardToken;
        break;
      }
      token = out.nextForwardToken;
    }
    expect(seen).toEqual(["a", "b", "c"]);
    expect(last).toBeDefined();
  });

  test("GetLogEvents returns backward and forward tokens in the f/ and b/ forms", async () => {
    const { group, stream } = await seeded(["a"]);
    const out = await logs.send(new GetLogEventsCommand({ logGroupName: group, logStreamName: stream, startFromHead: true }));
    expect(out.nextForwardToken).toMatch(/^f\/\d+$/);
    expect(out.nextBackwardToken).toMatch(/^b\/\d+$/);
  });

  test("GetLogEvents with an invalid token fails InvalidParameterException", async () => {
    const { group, stream } = await seeded(["a"]);
    await expectFailure(logs.send(new GetLogEventsCommand({ logGroupName: group, logStreamName: stream, nextToken: "garbage" })), {
      name: "InvalidParameterException",
      status: 400,
      messageIncludes: "The specified nextToken is invalid.",
    });
  });

  test("GetLogEvents with limit 10001 fails with the Smithy constraint message", async () => {
    const { group, stream } = await seeded(["a"]);
    await expectFailure(logs.send(new GetLogEventsCommand({ logGroupName: group, logStreamName: stream, limit: 10_001 })), {
      name: "InvalidParameterException",
      status: 400,
      messageIncludes: "1 validation error detected: Value '10001' at 'limit' failed to satisfy constraint: Member must have value less than or equal to 10000",
    });
  });

  test("GetLogEvents on a missing stream fails ResourceNotFoundException", async () => {
    const group = await makeGroup();
    await expectFailure(logs.send(new GetLogEventsCommand({ logGroupName: group, logStreamName: "nope" })), {
      name: "ResourceNotFoundException",
      status: 400,
      messageIncludes: "The specified log stream does not exist.",
    });
  });

  test("PutLogEvents on a missing stream fails ResourceNotFoundException", async () => {
    const group = await makeGroup();
    await expectFailure(
      logs.send(new PutLogEventsCommand({ logGroupName: group, logStreamName: "nope", logEvents: [{ timestamp: Date.now(), message: "x" }] })),
      { name: "ResourceNotFoundException", status: 400, messageIncludes: "The specified log stream does not exist." },
    );
  });

  test("PutLogEvents on a missing group fails ResourceNotFoundException", async () => {
    await expectFailure(
      logs.send(new PutLogEventsCommand({ logGroupName: groupName(), logStreamName: "s", logEvents: [{ timestamp: Date.now(), message: "x" }] })),
      { name: "ResourceNotFoundException", status: 400, messageIncludes: "The specified log group does not exist." },
    );
  });

  test("PutLogEvents with events out of chronological order fails InvalidParameterException", async () => {
    const group = await makeGroup();
    const stream = await makeStream(group);
    const now = Date.now();
    await expectFailure(
      logs.send(
        new PutLogEventsCommand({
          logGroupName: group,
          logStreamName: stream,
          logEvents: [
            { timestamp: now, message: "newer" },
            { timestamp: now - 1000, message: "older" },
          ],
        }),
      ),
      {
        name: "InvalidParameterException",
        status: 400,
        messageIncludes: "Log events in a single PutLogEvents request must be in chronological order.",
      },
    );
  });

  test("an event older than 14 days is reported in rejectedLogEventsInfo.tooOldLogEventEndIndex and not stored", async () => {
    const group = await makeGroup();
    const stream = await makeStream(group);
    const out = await logs.send(
      new PutLogEventsCommand({
        logGroupName: group,
        logStreamName: stream,
        logEvents: [{ timestamp: Date.now() - 30 * 86_400_000, message: "ancient" }],
      }),
    );
    expect(out.rejectedLogEventsInfo?.tooOldLogEventEndIndex).toBe(1);
    expect((await logs.send(new GetLogEventsCommand({ logGroupName: group, logStreamName: stream, startFromHead: true }))).events ?? []).toEqual([]);
  });

  test("an event more than two hours in the future is reported in tooNewLogEventStartIndex", async () => {
    const group = await makeGroup();
    const stream = await makeStream(group);
    const now = Date.now();
    const out = await logs.send(
      new PutLogEventsCommand({
        logGroupName: group,
        logStreamName: stream,
        logEvents: [
          { timestamp: now, message: "fine" },
          { timestamp: now + 24 * 3_600_000, message: "future" },
        ],
      }),
    );
    expect(out.rejectedLogEventsInfo?.tooNewLogEventStartIndex).toBe(1);
    const stored = await logs.send(new GetLogEventsCommand({ logGroupName: group, logStreamName: stream, startFromHead: true }));
    expect(stored.events?.map((e) => e.message)).toEqual(["fine"]);
  });

  test("PutLogEvents with no events fails with a validation error", async () => {
    const group = await makeGroup();
    const stream = await makeStream(group);
    const failure = await awsFailure(logs.send(new PutLogEventsCommand({ logGroupName: group, logStreamName: stream, logEvents: [] })));
    expect(failure.status).toBe(400);
  });

  test("a message with multi-byte characters and newlines round trips", async () => {
    const message = "line one\nline two é中\u{1F600}";
    const { group, stream } = await seeded([message]);
    const out = await logs.send(new GetLogEventsCommand({ logGroupName: group, logStreamName: stream, startFromHead: true }));
    expect(out.events?.[0]?.message).toBe(message);
  });
});

describe("CloudWatch Logs: FilterLogEvents", () => {
  test("an empty pattern returns every event across streams, oldest first, with stream names and event ids", async () => {
    const group = await makeGroup();
    const now = Date.now();
    const s1 = await makeStream(group, "s1");
    const s2 = await makeStream(group, "s2");
    await logs.send(new PutLogEventsCommand({ logGroupName: group, logStreamName: s1, logEvents: [{ timestamp: now - 3000, message: "a" }, { timestamp: now - 1000, message: "c" }] }));
    await logs.send(new PutLogEventsCommand({ logGroupName: group, logStreamName: s2, logEvents: [{ timestamp: now - 2000, message: "b" }] }));
    const out = await logs.send(new FilterLogEventsCommand({ logGroupName: group }));
    expect(out.events?.map((e) => e.message)).toEqual(["a", "b", "c"]);
    expect(out.events?.map((e) => e.logStreamName)).toEqual(["s1", "s2", "s1"]);
    expect(out.events?.every((e) => /^\d+$/.test(e.eventId ?? ""))).toBe(true);
    expect(out.nextToken).toBeUndefined();
  });

  test("logStreamNames and logStreamNamePrefix restrict the streams searched", async () => {
    const group = await makeGroup();
    const now = Date.now();
    for (const name of ["app-1", "app-2", "other"]) {
      await makeStream(group, name);
      await logs.send(new PutLogEventsCommand({ logGroupName: group, logStreamName: name, logEvents: [{ timestamp: now, message: name }] }));
    }
    const named = await logs.send(new FilterLogEventsCommand({ logGroupName: group, logStreamNames: ["app-1", "other"] }));
    expect((named.events ?? []).map((e) => e.message).sort()).toEqual(["app-1", "other"]);
    const prefixed = await logs.send(new FilterLogEventsCommand({ logGroupName: group, logStreamNamePrefix: "app-" }));
    expect((prefixed.events ?? []).map((e) => e.message).sort()).toEqual(["app-1", "app-2"]);
  });

  test("startTime is inclusive and bounds the result", async () => {
    const { group, times } = await seeded(["a", "b", "c"]);
    const out = await logs.send(new FilterLogEventsCommand({ logGroupName: group, startTime: times[1] }));
    expect(out.events?.map((e) => e.message)).toEqual(["b", "c"]);
  });

  test("limit bounds a page and nextToken continues without repeats", async () => {
    const { group } = await seeded(["1", "2", "3", "4", "5"]);
    const seen: string[] = [];
    let token: string | undefined;
    let pages = 0;
    do {
      const out = await logs.send(new FilterLogEventsCommand({ logGroupName: group, limit: 2, nextToken: token }));
      expect(out.events?.length ?? 0).toBeLessThanOrEqual(2);
      seen.push(...(out.events ?? []).map((e) => e.message ?? ""));
      token = out.nextToken;
      pages += 1;
    } while (token !== undefined && pages < 10);
    expect(seen).toEqual(["1", "2", "3", "4", "5"]);
  });

  test("an invalid nextToken fails InvalidParameterException", async () => {
    const { group } = await seeded(["a"]);
    await expectFailure(logs.send(new FilterLogEventsCommand({ logGroupName: group, nextToken: "garbage" })), {
      name: "InvalidParameterException",
      status: 400,
      messageIncludes: "The specified nextToken is invalid.",
    });
  });

  test("a missing group fails ResourceNotFoundException", async () => {
    await expectFailure(logs.send(new FilterLogEventsCommand({ logGroupName: groupName() })), { name: "ResourceNotFoundException", status: 400 });
  });

  test("logStreamNames together with logStreamNamePrefix fails InvalidParameterException", async () => {
    const { group, stream } = await seeded(["a"]);
    await expectFailure(logs.send(new FilterLogEventsCommand({ logGroupName: group, logStreamNames: [stream], logStreamNamePrefix: "x" })), {
      name: "InvalidParameterException",
      status: 400,
    });
  });
});

describe("CloudWatch Logs: filter pattern syntax", () => {
  const lines = [
    "ERROR connection refused",
    "WARN ARGUMENTS missing",
    "INFO started ERROR ARGUMENTS",
    "INFO all good",
    "UNAUTHORIZED request denied",
    "AUTHORIZED request granted",
    "INTERNAL SERVER ERROR happened",
  ];

  test("a single term matches events containing it, case-sensitively", async () => {
    const { group } = await seeded(lines);
    expect(await filter(group, "ERROR")).toEqual([lines[0], lines[2], lines[6]]);
    expect(await filter(group, "error")).toEqual([]);
  });

  test("several terms are ANDed", async () => {
    const { group } = await seeded(lines);
    expect(await filter(group, "ERROR ARGUMENTS")).toEqual([lines[2]]);
  });

  test("?terms are ORed", async () => {
    const { group } = await seeded(lines);
    expect(await filter(group, "?ERROR ?ARGUMENTS")).toEqual([lines[0], lines[1], lines[2], lines[6]]);
  });

  test("an excluded term with a leading minus removes matches", async () => {
    const { group } = await seeded(lines);
    expect(await filter(group, "ERROR -ARGUMENTS")).toEqual([lines[0], lines[6]]);
  });

  test("a quoted phrase matches the exact phrase", async () => {
    const { group } = await seeded(lines);
    expect(await filter(group, '"INTERNAL SERVER ERROR"')).toEqual([lines[6]]);
    expect(await filter(group, '"SERVER INTERNAL"')).toEqual([]);
  });

  test("a quoted single space matches every event", async () => {
    const { group } = await seeded(lines);
    expect(await filter(group, '" "')).toEqual(lines);
  });

  test("a %regex% term is a substring search", async () => {
    const { group } = await seeded(lines);
    expect(await filter(group, "%AUTHORIZED%")).toEqual([lines[4], lines[5]]);
    expect(await filter(group, "%^INFO all%")).toEqual([lines[3]]);
  });

  const jsonLines = [
    JSON.stringify({ eventType: "Trail", user: { id: 1, email: "john@example.com" }, status: 200, ip: "123.123.5.5", tags: ["a", "b"], flag: true, none: null }),
    JSON.stringify({ eventType: "Other", user: { id: 2, email: "jane@example.com" }, status: 404, ip: "10.0.0.1", tags: ["c"], flag: false }),
    "not json at all",
  ];

  test("JSON equality on strings, numbers and nested selectors", async () => {
    const { group } = await seeded(jsonLines);
    expect(await filter(group, '{ $.eventType = "Trail" }')).toEqual([jsonLines[0]]);
    expect(await filter(group, "{ $.status = 404 }")).toEqual([jsonLines[1]]);
    expect(await filter(group, '{ $.user.email = "jane@example.com" }')).toEqual([jsonLines[1]]);
    expect(await filter(group, "{ $.user.id = 1 }")).toEqual([jsonLines[0]]);
  });

  test("JSON numeric comparison operators", async () => {
    const { group } = await seeded(jsonLines);
    expect(await filter(group, "{ $.status >= 300 }")).toEqual([jsonLines[1]]);
    expect(await filter(group, "{ $.status < 300 }")).toEqual([jsonLines[0]]);
    expect(await filter(group, "{ $.status != 200 }")).toEqual([jsonLines[1]]);
  });

  test("JSON string wildcard and regex", async () => {
    const { group } = await seeded(jsonLines);
    expect(await filter(group, "{ $.ip = 123.123.* }")).toEqual([jsonLines[0]]);
    expect(await filter(group, "{ $.eventType = %^Tra% }")).toEqual([jsonLines[0]]);
  });

  test("JSON array index and wildcard selectors", async () => {
    const { group } = await seeded(jsonLines);
    expect(await filter(group, '{ $.tags[0] = "c" }')).toEqual([jsonLines[1]]);
    expect(await filter(group, '{ $.tags[*] = "b" }')).toEqual([jsonLines[0]]);
  });

  test("JSON IS NULL, IS TRUE, IS FALSE and NOT EXISTS", async () => {
    const { group } = await seeded(jsonLines);
    expect(await filter(group, "{ $.none IS NULL }")).toEqual([jsonLines[0]]);
    expect(await filter(group, "{ $.flag IS TRUE }")).toEqual([jsonLines[0]]);
    expect(await filter(group, "{ $.flag IS FALSE }")).toEqual([jsonLines[1]]);
    expect(await filter(group, "{ $.none NOT EXISTS }")).toEqual([jsonLines[1]]);
  });

  test("JSON compound expressions follow && over || precedence", async () => {
    const { group } = await seeded(jsonLines);
    expect(await filter(group, '{ ($.user.id = 1) && ($.status = 200) }')).toEqual([jsonLines[0]]);
    expect(await filter(group, '{ $.user.id = 1 || $.status = 404 && $.eventType = "nonmatch" }')).toEqual([jsonLines[0]]);
    expect(await filter(group, '{ ($.user.id = 1 || $.status = 404) && $.eventType = "Other" }')).toEqual([jsonLines[1]]);
  });

  test("a JSON pattern never matches a message that is not JSON", async () => {
    const { group } = await seeded(["plain text status 200"]);
    expect(await filter(group, "{ $.status = 200 }")).toEqual([]);
  });

  const accessLog = [
    '127.0.0.1 - frank [10/Oct/2000:13:55:36 -0700] "GET /index.html HTTP/1.0" 200 2326',
    '127.0.0.5 - alice [10/Oct/2000:13:56:01 -0700] "GET /missing.html HTTP/1.0" 404 128',
    '10.0.0.9 - bob [10/Oct/2000:13:57:10 -0700] "POST /form HTTP/1.0" 410 64',
  ];

  test("space-delimited patterns select fields by position and compare them", async () => {
    const { group } = await seeded(accessLog);
    const fields = "ip, user, username, timestamp, request, status_code, bytes";
    expect(await filter(group, `[${fields.replace("status_code", "status_code = 404")}]`)).toEqual([accessLog[1]]);
    expect(await filter(group, `[${fields.replace("status_code", "status_code = 4*")}]`)).toEqual([accessLog[1], accessLog[2]]);
    expect(await filter(group, `[${fields.replace("status_code", "status_code = 404 || status_code = 410")}]`)).toEqual([accessLog[1], accessLog[2]]);
    expect(await filter(group, `[${fields.replace("ip", "ip = %^127\\.0\\.0\\.[1-9]%")}]`)).toEqual([accessLog[0], accessLog[1]]);
    expect(await filter(group, `[${fields.replace("bytes", "bytes > 100")}]`)).toEqual([accessLog[0], accessLog[1]]);
  });

  test("an ellipsis stands for any number of unnamed fields", async () => {
    const { group } = await seeded(accessLog);
    expect(await filter(group, "[..., request = *form*, status_code, bytes]")).toEqual([accessLog[2]]);
  });

  test("positional w1 and w2 fields match the first word", async () => {
    const { group } = await seeded(["ERROR disk full", "WARNING disk slow", "INFO disk ok"]);
    expect(await filter(group, "[w1 = ERROR, w2]")).toEqual(["ERROR disk full"]);
    expect(await filter(group, "[w1 != ERROR && w1 != WARNING, w2]")).toEqual(["INFO disk ok"]);
  });

  test("a malformed filter pattern fails InvalidParameterException", async () => {
    const { group } = await seeded(["a"]);
    await expectFailure(logs.send(new FilterLogEventsCommand({ logGroupName: group, filterPattern: "{ $.a = }" })), {
      name: "InvalidParameterException",
      status: 400,
    });
  });
});
