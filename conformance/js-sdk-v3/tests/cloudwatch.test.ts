// CloudWatch metrics and alarms, through @aws-sdk/client-cloudwatch (the JS
// SDK speaks awsJson1_0 with awsQueryCompatible errors).
// Expected behavior: docs/research/phase1/secrets-logs-events-alarms.md section 5.

import { describe, expect, test } from "bun:test";
import {
  CloudWatchClient,
  DeleteAlarmsCommand,
  DescribeAlarmsCommand,
  DisableAlarmActionsCommand,
  EnableAlarmActionsCommand,
  GetMetricDataCommand,
  ListMetricsCommand,
  ListTagsForResourceCommand,
  PutMetricAlarmCommand,
  PutMetricDataCommand,
  SetAlarmStateCommand,
  TagResourceCommand,
  UntagResourceCommand,
} from "@aws-sdk/client-cloudwatch";
import { clientConfig, defaultAccount, region } from "../support/env";
import { awsFailure, eventually, expectFailure, uniq } from "../support/helpers";

const cw = new CloudWatchClient(clientConfig());

const alarmArn = (name: string) => `arn:aws:cloudwatch:${region}:${defaultAccount}:alarm:${name}`;

function alarmInput(name: string, extra: Partial<ConstructorParameters<typeof PutMetricAlarmCommand>[0]> = {}) {
  return {
    AlarmName: name,
    Namespace: "Conformance",
    MetricName: "Errors",
    Statistic: "Sum" as const,
    Period: 60,
    EvaluationPeriods: 1,
    Threshold: 5,
    ComparisonOperator: "GreaterThanThreshold" as const,
    ...extra,
  };
}

async function describeOne(name: string) {
  const out = await cw.send(new DescribeAlarmsCommand({ AlarmNames: [name] }));
  return out.MetricAlarms?.[0];
}

describe("CloudWatch metric alarms", () => {
  test("PutMetricAlarm creates an alarm that DescribeAlarms returns in INSUFFICIENT_DATA with the documented shape", async () => {
    const name = uniq("alarm");
    const put = await cw.send(new PutMetricAlarmCommand(alarmInput(name)));
    expect(put.$metadata.httpStatusCode).toBe(200);
    const alarm = await describeOne(name);
    expect(alarm).toMatchObject({
      AlarmName: name,
      AlarmArn: alarmArn(name),
      Namespace: "Conformance",
      MetricName: "Errors",
      Statistic: "Sum",
      Period: 60,
      EvaluationPeriods: 1,
      Threshold: 5,
      ComparisonOperator: "GreaterThanThreshold",
      ActionsEnabled: true,
      StateValue: "INSUFFICIENT_DATA",
      StateReason: "Unchecked: Initial alarm creation",
      AlarmActions: [],
      OKActions: [],
      InsufficientDataActions: [],
      Dimensions: [],
    });
    expect(alarm?.AlarmConfigurationUpdatedTimestamp).toBeInstanceOf(Date);
    expect(alarm?.StateUpdatedTimestamp).toBeInstanceOf(Date);
    expect(alarm?.TreatMissingData).toBeUndefined();
    expect(alarm?.AlarmDescription).toBeUndefined();
    expect(alarm?.Unit).toBeUndefined();
    expect(alarm?.DatapointsToAlarm).toBeUndefined();
  });

  test("optional fields round trip: description, dimensions, actions, TreatMissingData, unit and DatapointsToAlarm", async () => {
    const name = uniq("alarm");
    const topic = `arn:aws:sns:${region}:${defaultAccount}:alarm-topic`;
    await cw.send(
      new PutMetricAlarmCommand(
        alarmInput(name, {
          AlarmDescription: "described",
          Dimensions: [{ Name: "FunctionName", Value: "worker" }],
          AlarmActions: [topic],
          OKActions: [topic],
          InsufficientDataActions: [topic],
          TreatMissingData: "notBreaching",
          Unit: "Count",
          EvaluationPeriods: 3,
          DatapointsToAlarm: 2,
          ActionsEnabled: false,
        }),
      ),
    );
    expect(await describeOne(name)).toMatchObject({
      AlarmDescription: "described",
      Dimensions: [{ Name: "FunctionName", Value: "worker" }],
      AlarmActions: [topic],
      OKActions: [topic],
      InsufficientDataActions: [topic],
      TreatMissingData: "notBreaching",
      Unit: "Count",
      EvaluationPeriods: 3,
      DatapointsToAlarm: 2,
      ActionsEnabled: false,
    });
  });

  test("PutMetricAlarm on an existing alarm overwrites the configuration but keeps the state", async () => {
    const name = uniq("alarm");
    await cw.send(new PutMetricAlarmCommand(alarmInput(name)));
    await cw.send(new SetAlarmStateCommand({ AlarmName: name, StateValue: "ALARM", StateReason: "forced" }));
    await cw.send(new PutMetricAlarmCommand(alarmInput(name, { Threshold: 99, AlarmDescription: "second" })));
    const alarm = await describeOne(name);
    expect(alarm).toMatchObject({ Threshold: 99, AlarmDescription: "second", StateValue: "ALARM" });
  });

  test("SetAlarmState sets the state value and reason", async () => {
    const name = uniq("alarm");
    await cw.send(new PutMetricAlarmCommand(alarmInput(name)));
    await cw.send(new SetAlarmStateCommand({ AlarmName: name, StateValue: "OK", StateReason: "all good" }));
    expect(await describeOne(name)).toMatchObject({ StateValue: "OK", StateReason: "all good" });
  });

  test("SetAlarmState with an unknown state fails ValidationError with the enum message", async () => {
    const name = uniq("alarm");
    await cw.send(new PutMetricAlarmCommand(alarmInput(name)));
    await expectFailure(cw.send(new SetAlarmStateCommand({ AlarmName: name, StateValue: "INVALID" as never, StateReason: "x" })), {
      name: "ValidationError",
      status: 400,
      messageIncludes:
        "1 validation error detected: Value 'INVALID' at 'stateValue' failed to satisfy constraint: Member must satisfy enum value set: [INSUFFICIENT_DATA, ALARM, OK]",
    });
  });

  test("SetAlarmState on a missing alarm fails ResourceNotFound with status 404", async () => {
    await expectFailure(cw.send(new SetAlarmStateCommand({ AlarmName: uniq("missing"), StateValue: "OK", StateReason: "x" })), {
      name: "ResourceNotFound",
      status: 404,
    });
  });

  test("DescribeAlarms for a missing name returns an empty list, not an error", async () => {
    const out = await cw.send(new DescribeAlarmsCommand({ AlarmNames: [uniq("missing")] }));
    expect(out.MetricAlarms ?? []).toEqual([]);
    expect(out.CompositeAlarms ?? []).toEqual([]);
  });

  test("DescribeAlarms filters by name prefix and state and pages with MaxRecords", async () => {
    const prefix = uniq("dp");
    for (const suffix of ["a", "b", "c"]) await cw.send(new PutMetricAlarmCommand(alarmInput(`${prefix}-${suffix}`)));
    await cw.send(new SetAlarmStateCommand({ AlarmName: `${prefix}-b`, StateValue: "ALARM", StateReason: "x" }));
    const inAlarm = await cw.send(new DescribeAlarmsCommand({ AlarmNamePrefix: prefix, StateValue: "ALARM" }));
    expect(inAlarm.MetricAlarms?.map((a) => a.AlarmName)).toEqual([`${prefix}-b`]);
    const first = await cw.send(new DescribeAlarmsCommand({ AlarmNamePrefix: prefix, MaxRecords: 2 }));
    expect(first.MetricAlarms).toHaveLength(2);
    expect(first.NextToken).toBeDefined();
    const second = await cw.send(new DescribeAlarmsCommand({ AlarmNamePrefix: prefix, MaxRecords: 2, NextToken: first.NextToken }));
    expect(second.MetricAlarms).toHaveLength(1);
    expect(second.NextToken).toBeUndefined();
  });

  test("DescribeAlarms with an invalid NextToken fails InvalidNextToken", async () => {
    await expectFailure(cw.send(new DescribeAlarmsCommand({ NextToken: "garbage" })), { name: "InvalidNextToken", status: 400 });
  });

  test("DeleteAlarms removes the named alarms and skips names that do not exist", async () => {
    const a = uniq("alarm");
    const b = uniq("alarm");
    await cw.send(new PutMetricAlarmCommand(alarmInput(a)));
    await cw.send(new PutMetricAlarmCommand(alarmInput(b)));
    const out = await cw.send(new DeleteAlarmsCommand({ AlarmNames: [a, uniq("never"), b] }));
    expect(out.$metadata.httpStatusCode).toBe(200);
    expect(await describeOne(a)).toBeUndefined();
    expect(await describeOne(b)).toBeUndefined();
  });

  test("EnableAlarmActions and DisableAlarmActions toggle ActionsEnabled", async () => {
    const name = uniq("alarm");
    await cw.send(new PutMetricAlarmCommand(alarmInput(name)));
    await cw.send(new DisableAlarmActionsCommand({ AlarmNames: [name] }));
    expect((await describeOne(name))?.ActionsEnabled).toBe(false);
    await cw.send(new EnableAlarmActionsCommand({ AlarmNames: [name] }));
    expect((await describeOne(name))?.ActionsEnabled).toBe(true);
  });

  test("a threshold alarm with a missing required field is refused", async () => {
    const failure = await awsFailure(cw.send(new PutMetricAlarmCommand({ AlarmName: uniq("bad"), Namespace: "X", MetricName: "Y" } as never)));
    expect(failure.status).toBe(400);
  });

  test("a ComparisonOperator outside the enum fails ValidationError", async () => {
    await expectFailure(cw.send(new PutMetricAlarmCommand(alarmInput(uniq("bad"), { ComparisonOperator: "Nope" as never }))), {
      name: "ValidationError",
      status: 400,
      messageIncludes: "at 'comparisonOperator' failed to satisfy constraint: Member must satisfy enum value set: [",
    });
  });

  test("a Statistic together with an ExtendedStatistic is refused", async () => {
    const failure = await awsFailure(cw.send(new PutMetricAlarmCommand(alarmInput(uniq("bad"), { ExtendedStatistic: "p99" }))));
    expect(failure.status).toBe(400);
  });

  test("tags set at creation are listed; TagResource adds and UntagResource removes", async () => {
    const name = uniq("alarm");
    await cw.send(new PutMetricAlarmCommand(alarmInput(name, { Tags: [{ Key: "env", Value: "test" }] })));
    const arn = alarmArn(name);
    const tags = async () => Object.fromEntries(((await cw.send(new ListTagsForResourceCommand({ ResourceARN: arn }))).Tags ?? []).map((t) => [t.Key, t.Value]));
    expect(await tags()).toEqual({ env: "test" });
    await cw.send(new TagResourceCommand({ ResourceARN: arn, Tags: [{ Key: "team", Value: "a" }] }));
    expect(await tags()).toEqual({ env: "test", team: "a" });
    await cw.send(new UntagResourceCommand({ ResourceARN: arn, TagKeys: ["env"] }));
    expect(await tags()).toEqual({ team: "a" });
  });

  test("ListTagsForResource for a deleted alarm returns an empty list", async () => {
    const name = uniq("alarm");
    await cw.send(new PutMetricAlarmCommand(alarmInput(name, { Tags: [{ Key: "k", Value: "v" }] })));
    await cw.send(new DeleteAlarmsCommand({ AlarmNames: [name] }));
    expect((await cw.send(new ListTagsForResourceCommand({ ResourceARN: alarmArn(name) }))).Tags ?? []).toEqual([]);
  });
});

describe("CloudWatch metrics", () => {
  const namespace = () => `Conformance/${uniq("ns")}`;
  const minutesAgo = (n: number) => new Date(Date.now() - n * 60_000);

  test("PutMetricData then ListMetrics shows the metric with its dimensions", async () => {
    const ns = namespace();
    await cw.send(
      new PutMetricDataCommand({
        Namespace: ns,
        MetricData: [{ MetricName: "Latency", Dimensions: [{ Name: "Service", Value: "api" }], Value: 12, Unit: "Milliseconds", Timestamp: new Date() }],
      }),
    );
    const out = await eventually("the metric in ListMetrics", async () => {
      const listed = await cw.send(new ListMetricsCommand({ Namespace: ns }));
      return listed.Metrics?.length ? listed : undefined;
    });
    expect(out.Metrics).toEqual([{ Namespace: ns, MetricName: "Latency", Dimensions: [{ Name: "Service", Value: "api" }] }]);
  });

  test("GetMetricData returns the statistics of the datapoints, newest first by default", async () => {
    const ns = namespace();
    await cw.send(
      new PutMetricDataCommand({
        Namespace: ns,
        MetricData: [
          { MetricName: "Hits", Value: 2, Timestamp: minutesAgo(4) },
          { MetricName: "Hits", Value: 4, Timestamp: minutesAgo(3) },
          { MetricName: "Hits", Value: 6, Timestamp: minutesAgo(2) },
        ],
      }),
    );
    const query = (stat: string) =>
      cw.send(
        new GetMetricDataCommand({
          StartTime: minutesAgo(10),
          EndTime: new Date(Date.now() + 60_000),
          MetricDataQueries: [{ Id: "q", MetricStat: { Metric: { Namespace: ns, MetricName: "Hits" }, Period: 60, Stat: stat } }],
        }),
      );
    const sum = await query("Sum");
    expect(sum.MetricDataResults?.[0]?.Id).toBe("q");
    expect(sum.MetricDataResults?.[0]?.StatusCode).toBe("Complete");
    expect(sum.MetricDataResults?.[0]?.Values).toEqual([6, 4, 2]);
    const timestamps = sum.MetricDataResults?.[0]?.Timestamps?.map((t) => t.getTime()) ?? [];
    expect([...timestamps].sort((a, b) => b - a)).toEqual(timestamps);
    const max = await query("Maximum");
    expect(max.MetricDataResults?.[0]?.Values).toEqual([6, 4, 2]);
  });

  test("GetMetricData aggregates datapoints that fall in the same period", async () => {
    const ns = namespace();
    const at = minutesAgo(3);
    await cw.send(
      new PutMetricDataCommand({
        Namespace: ns,
        MetricData: [
          { MetricName: "Hits", Value: 1, Timestamp: at },
          { MetricName: "Hits", Value: 3, Timestamp: at },
        ],
      }),
    );
    const stats = async (stat: string) =>
      (
        await cw.send(
          new GetMetricDataCommand({
            StartTime: minutesAgo(10),
            EndTime: new Date(Date.now() + 60_000),
            MetricDataQueries: [{ Id: "q", MetricStat: { Metric: { Namespace: ns, MetricName: "Hits" }, Period: 60, Stat: stat } }],
          }),
        )
      ).MetricDataResults?.[0]?.Values;
    expect(await stats("Sum")).toEqual([4]);
    expect(await stats("Average")).toEqual([2]);
    expect(await stats("Minimum")).toEqual([1]);
    expect(await stats("Maximum")).toEqual([3]);
    expect(await stats("SampleCount")).toEqual([2]);
  });

  test("a datum with StatisticValues contributes its sample count and sum", async () => {
    const ns = namespace();
    await cw.send(
      new PutMetricDataCommand({
        Namespace: ns,
        MetricData: [{ MetricName: "Sized", StatisticValues: { SampleCount: 4, Sum: 20, Minimum: 1, Maximum: 9 }, Timestamp: minutesAgo(2) }],
      }),
    );
    const out = await cw.send(
      new GetMetricDataCommand({
        StartTime: minutesAgo(10),
        EndTime: new Date(Date.now() + 60_000),
        MetricDataQueries: [
          { Id: "count", MetricStat: { Metric: { Namespace: ns, MetricName: "Sized" }, Period: 60, Stat: "SampleCount" } },
          { Id: "sum", MetricStat: { Metric: { Namespace: ns, MetricName: "Sized" }, Period: 60, Stat: "Sum" } },
        ],
      }),
    );
    expect(out.MetricDataResults?.find((r) => r.Id === "count")?.Values).toEqual([4]);
    expect(out.MetricDataResults?.find((r) => r.Id === "sum")?.Values).toEqual([20]);
  });

  test("datapoints with different dimensions are separate metrics", async () => {
    const ns = namespace();
    await cw.send(
      new PutMetricDataCommand({
        Namespace: ns,
        MetricData: [
          { MetricName: "M", Value: 1, Dimensions: [{ Name: "D", Value: "a" }], Timestamp: minutesAgo(2) },
          { MetricName: "M", Value: 10, Dimensions: [{ Name: "D", Value: "b" }], Timestamp: minutesAgo(2) },
        ],
      }),
    );
    const out = await cw.send(
      new GetMetricDataCommand({
        StartTime: minutesAgo(10),
        EndTime: new Date(Date.now() + 60_000),
        MetricDataQueries: [{ Id: "q", MetricStat: { Metric: { Namespace: ns, MetricName: "M", Dimensions: [{ Name: "D", Value: "b" }] }, Period: 60, Stat: "Sum" } }],
      }),
    );
    expect(out.MetricDataResults?.[0]?.Values).toEqual([10]);
  });

  test("a Value together with Values fails InvalidParameterCombination with the member path message", async () => {
    await expectFailure(
      cw.send(new PutMetricDataCommand({ Namespace: namespace(), MetricData: [{ MetricName: "M", Value: 1, Values: [1], Counts: [1] }] })),
      {
        name: "InvalidParameterCombination",
        status: 400,
        messageIncludes: "The parameters MetricData.member.1.Value and MetricData.member.1.Values are mutually exclusive and you have specified both.",
      },
    );
  });

  test("Values and Counts of different sizes fail InvalidParameterValue", async () => {
    await expectFailure(cw.send(new PutMetricDataCommand({ Namespace: namespace(), MetricData: [{ MetricName: "M", Values: [1, 2], Counts: [1] }] })), {
      name: "InvalidParameterValue",
      status: 400,
      messageIncludes: "The parameters MetricData.member.1.Values and MetricData.member.1.Counts must be of the same size.",
    });
  });

  test("GetMetricData with StartTime after EndTime is refused", async () => {
    const failure = await awsFailure(
      cw.send(
        new GetMetricDataCommand({
          StartTime: new Date(),
          EndTime: minutesAgo(10),
          MetricDataQueries: [{ Id: "q", MetricStat: { Metric: { Namespace: "X", MetricName: "Y" }, Period: 60, Stat: "Sum" } }],
        }),
      ),
    );
    expect(failure.status).toBe(400);
  });
});
