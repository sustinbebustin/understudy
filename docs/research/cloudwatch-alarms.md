# CloudWatch alarm evaluation and GetMetricStatistics: primary-source research

Scope: how CloudWatch evaluates a metric alarm over stored metric data (cadence, window, M out of N, missing data), the texts and JSON it writes (`StateReason`, `StateReasonData`, alarm history), what a state change sends (SNS notification, Lambda event, EventBridge events), which actions an emulator can honestly run, and `GetMetricStatistics`. This is the CloudWatch alarm evaluation feature. Alarm storage, `PutMetricAlarm` validation, `DescribeAlarms` shapes, `PutMetricData` and `GetMetricData` are in [secrets-logs-events-alarms.md 5](secrets-logs-events-alarms.md#5-cloudwatch-metric-alarms-and-metrics) and are not repeated. Sources retrieved 2026-10-25: the CloudWatch user guide and API reference, the CloudWatch Smithy model pinned at aws/api-models-aws `1cfdfb4bb9ea858e65623e66d73af04bec353201` (vendored as `third_party/api-models-aws/models/cloudwatch/service/2010-08-01/cloudwatch-2010-08-01.json`), LocalStack's AWS-validated CloudWatch snapshots at its final commit, hashicorp/terraform-provider-aws v6.68.0, the AWS CLI's example pages, and real alarm notifications pasted into public test fixtures.

## 0. How to read this

| Label | Meaning |
|---|---|
| aws-doc | AWS user guide or API reference page, linked inline. Worked examples on these pages count: they are AWS-authored output |
| model | The pinned Smithy model, cited by shape id (`com.amazonaws.cloudwatch#GetMetricStatisticsInput`) |
| third-party-recorded | A LocalStack snapshot recorded against real AWS, cited as `localstack@8b9a79f <path>:L<line>` with the `last_validated_date` from its `.validation.json` sibling. LocalStack's archived repo is Apache-2.0 at commit `8b9a79f05846835cf4dff63ab7eefdde9df83783`. Its transformers mask names, dates and some values; a masked value is said to be masked |
| captured | A real CloudWatch notification body pasted into a public repository's test fixture, cited by repository, commit and line. Real bytes from the year shown, but not validated by anyone; a hint, weaker than third-party-recorded |
| tf | hashicorp/terraform-provider-aws v6.68.0, cited by file and line |
| emu | Another emulator's code. Never evidence of AWS behavior |
| inferred | Derived here from the sources above; the reasoning is given |
| UNVERIFIED | No source fixes the behavior. The plausible options are listed, a default is chosen, and a recording case in section 13 settles it |

LocalStack holds 36 alarm cases (35 in `tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json`, counting each protocol variant, and 1 in `test_cloudwatch_metrics.snapshot.json`). The ones that carry evaluation output are `test_put_metric_alarm` (validated 2026-02-26), `test_breaching_alarm_actions[json]` (2025-09-19), `test_enable_disable_alarm_actions[json]` (2025-09-19), `test_set_alarm[json]` (2025-09-19), `test_alarm_lambda_target` (2024-01-03) and `TestSQSMetrics::test_alarm_number_of_messages_sent` (2024-01-03). The JSON, query and CBOR variants of a case record the same bodies.

## 1. Model and wire facts

### 1.1 Service traits

`com.amazonaws.cloudwatch#GraniteServiceVersion20100801` carries `aws.protocols#awsJson1_0`, `aws.protocols#awsQuery`, `aws.protocols#awsQueryCompatible` and `smithy.protocols#rpcv2Cbor`; `aws.auth#sigv4` name `monitoring`; `aws.api#service` endpointPrefix `monitoring`, arnNamespace `monitoring`; `smithy.api#xmlNamespace` `http://monitoring.amazonaws.com/doc/2010-08-01/` (model). No operation in this note has an `endpoint` or `hostPrefix` trait (model). Which client speaks which protocol is in [secrets-logs-events-alarms.md 1.1](secrets-logs-events-alarms.md#11-cloudwatch-needs-three-codecs) and [wire-cbor.md](wire-cbor.md).

### 1.2 Operation shapes and errors

| Operation | Input (model) | Output | Errors (awsQueryError code, httpError) |
|---|---|---|---|
| `GetMetricStatistics` | `Namespace` (required, 1 to 255, pattern `^[^:]` in the model; the API reference prints `[^:].*`), `MetricName` (required, 1 to 255), `Dimensions` (max 30), `StartTime` (required), `EndTime` (required), `Period` (required, `@range min 1`), `Statistics` (`@length` 1 to 5, enum `SampleCount Average Sum Minimum Maximum`), `ExtendedStatistics` (`@length` 1 to 10, free strings), `Unit` (`StandardUnit`) | `{Label, Datapoints[]}`; `Datapoint` = `Timestamp, SampleCount, Average, Sum, Minimum, Maximum, Unit, ExtendedStatistics` (map) | `InternalServiceFault` (`InternalServiceError`, 500), `InvalidParameterCombinationException` (`InvalidParameterCombination`, 400), `InvalidParameterValueException` (`InvalidParameterValue`, 400), `MissingRequiredParameterException` (`MissingParameter`, 400) |
| `SetAlarmState` | `AlarmName` (required), `StateValue` (required), `StateReason` (required, `@length` 0 to 1023), `StateReasonData` (`@length` 0 to 4000) | `smithy.api#Unit` | `InvalidFormatFault` (`InvalidFormat`, 400), `ResourceNotFound` (`ResourceNotFound`, 404) |
| `DescribeAlarmHistory` | `AlarmName`, `AlarmContributorId`, `AlarmTypes`, `HistoryItemType`, `StartDate`, `EndDate`, `MaxRecords` (1 to 100), `NextToken`, `ScanBy`; `@paginated` on `NextToken`/`AlarmHistoryItems`/`MaxRecords` | `{AlarmHistoryItems[], NextToken}`; `AlarmHistoryItem` = `AlarmName, AlarmContributorId, AlarmType, Timestamp, HistoryItemType, HistorySummary (1 to 255), HistoryData (1 to 4095), AlarmContributorAttributes` | `InvalidNextToken` (`InvalidNextToken`, 400) |
| `DescribeAlarmsForMetric` | `MetricName` (required), `Namespace` (required), `Statistic`, `ExtendedStatistic`, `Dimensions`, `Period`, `Unit` | `{MetricAlarms[]}` | none listed |

Shape ids: `com.amazonaws.cloudwatch#GetMetricStatisticsInput`, `#GetMetricStatisticsOutput`, `#Datapoint`, `#Statistics`, `#ExtendedStatistics`, `#Period`, `#SetAlarmStateInput`, `#StateReason`, `#StateReasonData`, `#DescribeAlarmHistoryInput`, `#AlarmHistoryItem`, `#HistoryItemType` (`ConfigurationUpdate | StateUpdate | Action | AlarmContributorStateUpdate | AlarmContributorAction`), `#HistoryData`, `#HistorySummary`, `#MaxRecords` (model). `StateReasonData` and `HistoryData` are strings that hold JSON on the wire ([API_MetricAlarm](https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/API_MetricAlarm.html), [API_AlarmHistoryItem](https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/API_AlarmHistoryItem.html)). The `SetAlarmState` page describes `InvalidFormat` as "Data was not syntactically valid JSON." ([API_SetAlarmState](https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/API_SetAlarmState.html)); the wire message text is not recorded anywhere.

`PutMetricAlarm` documents only `LimitExceededFault` (model `com.amazonaws.cloudwatch#PutMetricAlarm`); its validation texts are already in `internal/service/cloudwatch/validate.go`.

### 1.3 Pinned model versus current documentation

- `PutMetricAlarmInput` and `MetricAlarm` in the pin carry `EvaluationWindow` (a union of `SlidingWindow {}` and `WallClockWindow {Timezone}`), `EvaluationInterval`, `EvaluationCriteria` and `WarmUpConfiguration` (model `com.amazonaws.cloudwatch#EvaluationWindow`, `#WallClockWindow`).
- The current `MetricAlarm` page also lists `ActionConfiguration` (alarm tag propagation) ([API_MetricAlarm](https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/API_MetricAlarm.html)); the pin has no such member, so understudy cannot accept or echo it until the pin moves.
- The user guide lists an evaluation state `IN_WARM_UP` ([alarm-evaluation](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-evaluation.html)); the pinned enum `com.amazonaws.cloudwatch#EvaluationState` holds only `PARTIAL_DATA`, `EVALUATION_FAILURE`, `EVALUATION_ERROR`. AWS returns `"EvaluationState": "IN_WARM_UP"` in `DescribeAlarms` (CWA-22, recorded 2026-11-03).

## 2. Alarm state lifecycle

### 2.1 States, initial state and reason

- States: `OK` ("within the defined threshold"), `ALARM` ("outside of the defined threshold"), `INSUFFICIENT_DATA` ("The alarm has just started, the metric is not available, or not enough data is available") ([alarm-evaluation](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-evaluation.html)).
- "When this operation creates an alarm, the alarm state is immediately set to INSUFFICIENT_DATA. For PromQL alarms, the alarm state is instead immediately set to OK. The alarm is then evaluated and its state is set appropriately. Any actions associated with the new state are then executed." (model doc of `com.amazonaws.cloudwatch#PutMetricAlarm`). The initial `StateReason` is `Unchecked: Initial alarm creation` (third-party-recorded; already in [secrets-logs-events-alarms.md 5.1](secrets-logs-events-alarms.md#51-resources)).
- A new alarm on a metric that already has data looks back before its creation: "If data points are missing soon after you create an alarm, and the metric was being reported to CloudWatch before you created the alarm, CloudWatch retrieves the most recent data points from before the alarm was created" ([alarm-evaluation](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-evaluation.html)).
- An alarm may watch a custom metric that does not exist yet; it must name all of the metric's dimensions ([AlarmThatSendsEmail](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/AlarmThatSendsEmail.html)).

### 2.2 Timestamps

`StateTransitionedTimestamp` is "The date and time that the alarm's `StateValue` most recently changed"; `StateUpdatedTimestamp` is "The time stamp of the last update to the value of either the `StateValue` or `EvaluationState` parameters"; `AlarmConfigurationUpdatedTimestamp` is "The time stamp of the last update to the alarm configuration" ([API_MetricAlarm](https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/API_MetricAlarm.html)). Read literally, neither state timestamp moves when an evaluation or a `SetAlarmState` leaves `StateValue` as it was and understudy reports no `EvaluationState`; CWA-05 and CWA-17 recorded exactly that (3.8, 2.3).

### 2.3 Updates, SetAlarmState and the next evaluation

- "When you update an existing alarm, its state is left unchanged, but the update completely overwrites the previous configuration of the alarm." (model doc of `PutMetricAlarm`.)
- `SetAlarmState` "Temporarily sets the state of an alarm for testing purposes. When the updated state differs from the previous value, the action configured for the appropriate state is invoked." ... "Metric alarms returns to their actual state quickly, often within seconds." ([API_SetAlarmState](https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/API_SetAlarmState.html)). The user guide: "This temporary state change lasts only until the next alarm comparison occurs." ([AlarmThatSendsEmail](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/AlarmThatSendsEmail.html)).
- So the revert is the next evaluation (3.1) computing a state from data. With `TreatMissingData` `ignore` and no data the evaluation retains the state (3.5), which is why LocalStack's `SetAlarmState` tests use `ignore` and still see the set state afterwards (`localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L5990-L6078`, third-party-recorded, validated 2025-09-19).
- After `SetAlarmState`, `StateReason` is the caller's text and no `StateReasonData` appears unless the caller sent one (same snapshot, L6020, L6062).
- `SetAlarmState` to the state the alarm is already in changes nothing: AWS answered 200, and after a set to `ALARM` with reason `first` a second set to `ALARM` with reason `second` and data `{"n":2}` left `StateReason` `first`, no `StateReasonData`, both timestamps, one `StateUpdate` item and one notification (CWA-17, recorded 2026-11-03; the retry captures `retry-*` confirm it without actions). This is option (a) of the research's four; the default it replaces was (b). Switch `cloudwatch.alarm.set-same-state` `no-change`.

### 2.4 Warm-up

`WarmUpConfiguration` (`WarmUpPeriodDurationInMinutes` 1 to 2,880, `OnlyStartEvaluatingAfterWarmUpPeriodEnds` default false): during warm-up "the alarm stays in `INSUFFICIENT_DATA` and does not perform alarm actions"; with the flag false, warm-up ends "as soon as it can fill its evaluation window", which "is full when a datapoint exists for the most recent period"; with it true the alarm waits the full duration; "Warm-up applies only once at alarm creation and does not restart"; the evaluation state is `IN_WARM_UP` ([alarm-warm-up](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-warm-up.html)). `IN_WARM_UP` is absent from the pin (1.3).

When warm-up ends with the flag false: the sentence above says the window is full once the most recent period has a datapoint, but the same page's worked example needs every period: a 5-minute, 3-period alarm created at 10:00 on a resource that starts publishing at 10:04 "stays in `INSUFFICIENT_DATA` until a full 15-minute window is available. This happens at about 10:19" (same page). The example holds (option (a), switch `cloudwatch.alarm.warm-up-end` `every-period-has-data`): a 1-minute, 3-period `breaching` alarm with a 30-minute warm-up, data put every 30 seconds from 03:15:55, stayed `INSUFFICIENT_DATA` and went to `ALARM` at 03:18:19 over the bins 03:15, 03:16 and 03:17, well before the 30 minutes (CWA-22, recorded 2026-11-03). What the recording also shows:

- From creation the alarm reads `StateReason` `Alarm is in Warm-Up` and `DescribeAlarms` returns `"EvaluationState": "IN_WARM_UP"`, which the pinned enum lacks (1.3); the field is gone once warm-up ends.
- The transition that ends warm-up has `HistorySummary` `Alarm updated from INSUFFICIENT_DATA to ALARM. State now evaluated with the full set of metrics matching the query` and its `HistoryData` `oldState` carries `"evaluationState":"IN_WARM_UP"` after `stateReason`.
- With `OnlyStartEvaluatingAfterWarmUpPeriodEnds` true nothing is recorded; understudy writes the same texts for it (inferred).

## 3. Evaluation

### 3.1 Cadence and phase

- "For any period of one minute or longer, an alarm is evaluated every minute and the evaluation is based on the window of time defined by the Period and Evaluation Periods. For example, if the Period is 5 minutes (300 seconds) and Evaluation Periods is 1, then at the end of minute 5 the alarm evaluates based on data from minutes 1 to 5. Then at the end of minute 6, the alarm is evaluated based on the data from minutes 2 to 6." ([alarm-evaluation](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-evaluation.html)).
- "If the alarm period is 10 seconds, 20 seconds, or 30 seconds, the alarm is evaluated every 10 seconds." (same page).
- "If the number of evaluation periods multiplied by the length of each evaluation period exceeds one day, the alarm is evaluated once per hour." Such multi-day alarms take "into account only the metrics up to the current hour at the :00 minute" (same page, with a worked example: a failure at 10:02 is not seen by the 10:03 evaluation and is seen at 11:03).
- A wall clock window does not change the cadence: "an alarm with a 1-hour period and a wall clock window evaluates every minute" ([alarm-evaluation-window](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-evaluation-window.html)).
- Phase within the minute: AWS evaluates at an arbitrary second. The `queryDate` values in AWS's examples are 17:04:40.985, 13:56:40.985 and 17:20:48.551 ([cloudwatch-and-eventbridge](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/cloudwatch-and-eventbridge.html)), 12:31:29.591 ([alarms-and-actions-Lambda](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarms-and-actions-Lambda.html)) and 08:34:06.300 ([alarm-actions](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-actions.html)). Which data a minute's evaluation sees does not depend on the second (3.2), so understudy evaluates on the minute boundary of the emulator clock (inferred, switch `cloudwatch.alarm.evaluation-phase`).
- The first evaluation after `PutMetricAlarm` comes within the minute. The model says only "then evaluated". Recordings that describe an alarm right after creating it still see `Unchecked: Initial alarm creation` (`localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L6282`, validated 2026-02-26; L983-L1017, validated 2025-09-19), so it is not synchronous. AWS's metric math example was created at 17:20:03.642 (its `previousState` is `Unchecked: Initial alarm creation` at that time) and first evaluated at 17:20:48.551, 45 seconds later and inside the creation minute ([cloudwatch-and-eventbridge](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/cloudwatch-and-eventbridge.html)). A 60-second alarm created at 03:14:02.667 over an older point was first evaluated at 03:14:34.961, 32.3 seconds later (CWA-03, recorded 2026-11-03). understudy evaluates at the next minute boundary, at most 60 seconds later (switch `cloudwatch.alarm.first-evaluation` `next-boundary`); the second AWS picks is not modelled (`cloudwatch.alarm.evaluation-phase`).
- For 10, 20 and 30 second periods the evaluations come every 10 seconds, but not on the 10-second divisions the research assumed, and the window lags: a 10-second, N = 1 `missing` alarm created at 03:14:00.616 on one high-resolution point in the bin 03:13:50 went to `ALARM` at 03:15:09.443 (69 seconds after creation, evaluating that bin) and to `INSUFFICIENT_DATA` at 03:15:29.444, 9.4 seconds into a division, evaluating only the bin 03:15:00, so the newest bin an evaluation reads starts two periods before the division at or below its `queryDate` (CWA-02, recorded 2026-11-03). From the two transitions the point stayed in range for 7 bins counted from that lagged newest bin. Why the first transition waited 69 seconds is not known: data latency, a slower first evaluation for sub-minute alarms, or both. understudy reads the bins ending at the 10-second division (`cloudwatch.alarm.window-anchor`), which differs; aligning it needs a design for the lag and the range of sub-minute alarms (follow-up).

### 3.2 The window: bins anchored at the evaluation minute

With the default sliding window, "Each time the alarm is evaluated, the window advances by one minute, and the boundaries of the window are not aligned to the wall clock" ([alarm-evaluation](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-evaluation.html)); for example "13:07-14:07 on one evaluation and 13:08-14:08 on the next" for a 1-hour period ([alarm-evaluation-window](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-evaluation-window.html)).

Every example with both an evaluation time and datapoint timestamps fits one rule: let T0 be the evaluation time truncated to the minute; bin k (k = 1 is the newest) covers [T0 - k x Period, T0 - (k - 1) x Period), and a datapoint's timestamp is its bin's start.

| Evaluation time | Period | Datapoint timestamps | T0 | Source |
|---|---|---|---|---|
| 2019-10-02 17:04:40.985 | 300 | 16:59:00 (`startDate` 16:59:00) | 17:04 | aws-doc, [cloudwatch-and-eventbridge](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/cloudwatch-and-eventbridge.html), single metric alarm |
| 2019-10-02 17:20:48.551 | 300 | 17:10:00 (with N = 1, so the 17:15 bin was empty and the range supplied an older one, inferred) | 17:20 | aws-doc, same page, metric math alarm |
| 2025-07-03 08:34:06.300 | 300 | 08:29, 08:24, 08:19 (`startDate` 08:19) | 08:34 | aws-doc, [alarm-actions](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-actions.html) |
| 2023-08-04 12:31:29.591 | 60 | 12:30, 12:29, 12:28, 12:27, 12:26 | 12:31 | aws-doc, [alarms-and-actions-Lambda](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarms-and-actions-Lambda.html) |
| 2017-10-30 13:20:35.831 | 86400 | 29/10/17 13:20:00 | 13:20 | captured, widdix/cloudwatch-alarm-to-slack `fb66af9` handler.js:L59 |
| 2019-02-15 23:53:45.093 | 86400 | 14/02/19 23:53:00 | 23:53 | captured, alerta/alerta `4ac0106` tests/test_webhooks.py:L70 |
| 2018-06-14 14:52:51.039 | 60 | 14/06/18 14:51:00 | 14:52 | captured, linuxacademy/la-aws-security_specialty `9e1e27c` Troubleshooting-Detection-Alerting-Response-Workflow/sns_event.json:L12 |

So a 1-day period is not aligned to midnight and a 5-minute period not to multiples of five minutes. This is `cloudwatch.alarm.window-anchor` `evaluation-minute` (aws-doc for periods of a minute or more). For a multi-day alarm, T0 is the evaluation time truncated to the hour (3.1). A wall clock window instead aligns bins "to the start of the matching calendar period in the selected time zone" and supports only periods 60, 300, 3600, 86400 and 604800 ([alarm-evaluation-window](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-evaluation-window.html)).

The window reads data stored at its resolution: a regular-resolution datum is kept at the minute (`storedAt` in `internal/service/cloudwatch/metrics.go`), so a 10, 20 or 30 second alarm on regular data "does not receive data for the attempts that do not correspond to a one-minute data resolution, and the alarm might often lapse into INSUFFICENT_DATA status" (model doc of `PutMetricAlarmInput$Period`).

### 3.3 Which series and which statistic

- The alarm reads the series with exactly its namespace, metric name and dimension set (the same exact match as `GetMetricStatistics`, 9.1). The alarm's statistic is computed per bin from the stored statistic sets.
- `Unit`: "If you don't specify Unit, CloudWatch retrieves all unit types that have been published for the metric and attempts to evaluate the alarm. ... if the metric is published with multiple types of units and you don't specify a unit, the alarm's behavior is not defined and it behaves unpredictably." A unit that was never published leaves the alarm "stuck in the INSUFFICIENT DATA state" (model doc of `PutMetricAlarmInput$Unit`). Since AWS defines no behavior, understudy reads the first unit by name, as its `GetMetricData` already does (inferred, switch `cloudwatch.alarm.mixed-units`; no recording can settle an undefined behavior).
- Comparison: "The specified statistic value is used as the first operand" (model doc of `ComparisonOperator`): `GreaterThanThreshold` is v > T, `GreaterThanOrEqualToThreshold` v >= T, `LessThanThreshold` v < T, `LessThanOrEqualToThreshold` v <= T. The other three operators are for anomaly detection models only.

### 3.4 Evaluation range, N and M

- Period, Evaluation Periods (N) and Datapoints to Alarm (M): "Datapoints to Alarm is the number of data points within the Evaluation Periods that must be breaching to cause the alarm to go to the ALARM state. The breaching data points don't have to be consecutive, but they must all be within the last number of data points equal to Evaluation Period." ([alarm-evaluation](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-evaluation.html)). M defaults to N (`DatapointsToAlarm` "is used only if you are setting an 'M out of N' alarm", model doc of `PutMetricAlarmInput$DatapointsToAlarm`).
- "Whenever an alarm evaluates whether to change state, CloudWatch attempts to retrieve a higher number of data points than the number specified as Evaluation Periods. The exact number of data points it attempts to retrieve depends on the length of the alarm period and whether it is based on a metric with standard resolution or high resolution. The time frame of the data points that it attempts to retrieve is the evaluation range." A wall clock window retrieves exactly N ([alarms-and-missing-data](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarms-and-missing-data.html)).
- The only documented size: N = 3 with a 5-minute period retrieves 5 (same page, both tables).
- Recorded, from when a lone point left the range (CWA-01, recorded 2026-11-03): with a 60-second period R = N + 5, 6 for N = 1 (a point at 03:07:41 kept the alarm in `ALARM` through the evaluation at T0 03:13 and left at 03:14) and 8 for N = 3 (points at 03:05:13 to 03:07:13: at T0 03:14 the 03:05 bin had left and the 03:06 and 03:07 bins were still counted); with a 300-second period R = N + 2, 3 for N = 1 (a point at 03:07:44 left at T0 03:23). So the number of extra periods depends on the period, option (b) of the research's three. LocalStack's emulator guess N + max(floor(N/3), 2) (emu, `localstack@8b9a79f localstack-core/localstack/services/cloudwatch/alarm_scheduler.py:L249-L251`) fits only the 300-second rows. Switch `cloudwatch.alarm.evaluation-range` `n-plus-5-for-60s,n-plus-2-otherwise`.
- UNVERIFIED: R for periods other than 60 and 300 seconds, for N other than 1 and 3 at 60 seconds, and for high-resolution alarms (CWA-02 suggests 7 for a 10-second N = 1 alarm, counted from a lagged newest bin, 3.1). understudy uses N + 2 for every period but 60 seconds.
- The range is why "CloudWatch alarms might repeatedly re-evaluate the last set of data points for a period of time after the metric has stopped flowing" (same page).

### 3.5 Missing data: the algorithm

Treatments: "`notBreaching` - Missing data points are treated as 'good' and within the threshold; `breaching` - Missing data points are treated as 'bad' and breaching the threshold; `ignore` - The current alarm state is maintained; `missing` - If all data points in the alarm evaluation range are missing, the alarm transitions to INSUFFICIENT_DATA." "The default behavior is `missing`." ([alarms-and-missing-data](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarms-and-missing-data.html)). The three documented cases (same page, verbatim):

1. "If no data points in the evaluation range are missing, CloudWatch evaluates the alarm based on the most recent data points collected. The number of data points evaluated is equal to the Evaluation Periods for the alarm."
2. "If some data points in the evaluation range are missing, but the total number of existing data points that were successfully retrieved from the evaluation range is equal to or more than the alarm's Evaluation Periods, CloudWatch evaluates the alarm state based on the most recent real data points that were successfully retrieved, including the necessary extra data points from farther back in the evaluation range. In this case, the value you set for how to treat missing data is not needed and is ignored."
3. "If some data points in the evaluation range are missing, and the number of actual data points that were retrieved is lower than the alarm's number of Evaluation Periods, CloudWatch fills in the missing data points with the result you specified for how to treat missing data, and then evaluates the alarm. However, all real data points in the evaluation range are included in the evaluation. CloudWatch uses missing data points only as few times as possible."

Premature alarm state (same page): "alarms are designed to always go into ALARM state when the oldest available breaching datapoint during the Evaluation Periods number of data points is at least as old as the value of Datapoints to Alarm. All other more recent data points are breaching or missing." And: "the alarm does not go immediately into ALARM state when the data is either `- - - - X` or `- - - X -` and Datapoints to Alarm is 3."

The algorithm below reproduces all 40 documented cells of the two tables in 3.6 (inferred from them; the rule text alone contradicts table 2 row 3, see section 11). Bins d1 (newest) to dR (oldest), each a value or missing; `real` the bins with a value; b the number of breaching real bins.

```
A. |real| >= N: evaluate the N newest real bins: ALARM if at least M breach, else OK. Treatment unused.
B. |real| = 0:  missing -> INSUFFICIENT_DATA; ignore -> retain; breaching -> ALARM; notBreaching -> OK.
C. 0 < |real| < N:
   breaching:     ALARM if b + (N - |real|) >= M, else OK
   notBreaching:  ALARM if b >= M, else OK
   missing, ignore:
     1. b >= M                                            -> ALARM
     2. some real bin is not breaching                    -> OK
     3. the oldest breaching bin among d1..dR sits at
        position >= M (premature alarm state)             -> missing: ALARM; ignore: retain
     4. otherwise (every real bin breaching, but too
        recent)                                           -> retain
```

"retain" means the state does not change (3.8). CWA-04 (recorded 2026-11-03) re-recorded the 40 documented cells, which all matched, and the two cells beyond them: `- - - - X` (x1) retains under `missing` and `ignore`, the doc's "does not go immediately into ALARM"; `X - - - -` (x2) under `missing` went from OK to ALARM with `1 out of the last 3 datapoints [5.0 (03/11/26 02:50:00)] was greater than the threshold (3.0) (minimum 3 datapoints for OK -> ALARM transition).`, its evaluated bins 03:10 and 03:05 missing and 02:50 (d5) breaching, and stayed ALARM from ALARM. So step 3 reads the whole range, not d1..dN as first written here, and step 4 is only the too-recent case. Switch `cloudwatch.alarm.unaged-breach` `alarm-if-aged-in-range-else-retain`.

### 3.6 Missing data: test vectors

Transcribed from [alarms-and-missing-data](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarms-and-missing-data.html). Data points oldest first, newest on the right; `0` not breaching, `X` breaching, `-` missing; the evaluation range is 5; "Retain current state" is written `retain`.

Table 1: Datapoints to Alarm 3, Evaluation Periods 3.

| Row | Data points | # of data points that must be filled | Missing | Ignore | Breaching | Not breaching |
|---|---|---|---|---|---|---|
| 1 | `0 - X - X` | 0 | OK | OK | OK | OK |
| 2 | `0 - - - -` | 2 | OK | OK | OK | OK |
| 3 | `- - - - -` | 3 | INSUFFICIENT_DATA | retain | ALARM | OK |
| 4 | `0 X X - X` | 0 | ALARM | ALARM | ALARM | ALARM |
| 5 | `- - X - -` | 2 | ALARM | retain | ALARM | OK |

Table 2: Period 5 minutes, Datapoints to Alarm 2, Evaluation Periods 3.

| Row | Data points | # of missing data points | Missing | Ignore | Breaching | Not breaching |
|---|---|---|---|---|---|---|
| 1 | `0 - X - X` | 0 | ALARM | ALARM | ALARM | ALARM |
| 2 | `0 0 X 0 X` | 0 | ALARM | ALARM | ALARM | ALARM |
| 3 | `0 - X - -` | 1 | OK | OK | ALARM | OK |
| 4 | `- - - - 0` | 2 | OK | OK | ALARM | OK |
| 5 | `- - - X -` | 2 | ALARM | retain | ALARM | OK |

Doc commentary that fixes the reasoning: row 2 of table 1 stays OK under breaching "because the one existing data point is not breaching. This is evaluated along with two missing data points which are treated as breaching. The next time this alarm is evaluated, if the data is still missing it will go to ALARM"; in table 2 rows 3 and 4 "the alarm goes to ALARM state only if missing data is treated as breaching".

Every cell of both tables matched when CWA-04 re-recorded them (2026-11-03). Vectors beyond the tables (CWA-04, recorded 2026-11-03; x2 under `missing` differs from the retain this note first inferred):

| Case | Data points | N | M | Missing | Ignore | Breaching | Not breaching |
|---|---|---|---|---|---|---|---|
| x1 | `- - - - X` | 3 | 3 | retain (doc: "does not go immediately into ALARM") | retain | ALARM | OK |
| x2 | `X - - - -` | 3 | 3 | ALARM | retain | ALARM | OK |

### 3.7 Special cases

- `AWS/DynamoDB`: "Alarms that evaluate metrics in the AWS/DynamoDB namespace always ignore missing data even if you choose a different option for TreatMissingData." (model doc of `PutMetricAlarmInput$TreatMissingData`). The user guide says such alarms "default to ignore missing data. You can override this" ([alarms-and-missing-data](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarms-and-missing-data.html)). The user guide holds: an `AWS/DynamoDB` `SystemErrors` alarm sent `breaching`, on an idle table, went to `ALARM` at its first evaluation with `Threshold Crossed: no datapoints were received for 1 period and 1 missing datapoint was treated as [Breaching].`, as its control on a custom namespace did (CWA-21, recorded 2026-11-03). What an `AWS/DynamoDB` alarm sent no `TreatMissingData` does (the guide's "default to ignore") is unrecorded; understudy applies the API's default, `missing`. Switch `cloudwatch.alarm.dynamodb-missing-data` `as-configured`. understudy refuses `PutMetricData` to `AWS/` namespaces (`checkPutMetricData` in `internal/service/cloudwatch/validate.go`), so such an alarm only ever sees missing data.
- `EvaluateLowSampleCountPercentile` (percentile alarms only): `ignore` keeps the state "during periods with too few data points to be statistically significant"; `evaluate` or omitted always evaluates (model doc). Too few means: "For percentiles between 0.5 (inclusive) and 1.00 (exclusive), this setting is used when there are fewer than 10/(1-percentile) data points during the evaluation period. ... fewer than 1000 samples for an alarm on a p99 percentile. For percentiles between 0 and 0.5 (exclusive), the setting is used when there are fewer than 10/percentile data points." ([percentiles-with-low-samples](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/percentiles-with-low-samples.html)). understudy keeps statistic sets, not raw values, so it cannot compute percentiles (12.1).
- Percentiles over statistic sets exist only when "The SampleCount value of the statistic set is 1" or "The Min and the Max values of the statistic set are equal", and "Percentile statistics are not available for metrics when any of the metric values are negative numbers." (model doc of `GetMetricStatistics`).
- Metric alarms on math expressions, Metrics Insights, anomaly detection, PromQL and logs use different rules (state reasons per contributor, `Thresholds Crossed:` with upper and lower bands) and are out of scope ([alarms-and-missing-data](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarms-and-missing-data.html), [cloudwatch-and-eventbridge](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/cloudwatch-and-eventbridge.html)).

### 3.8 What a transition does, and what no transition does

When an evaluation yields a state different from the current one: `StateValue`, `StateReason`, `StateReasonData`, `StateUpdatedTimestamp` and `StateTransitionedTimestamp` change (2.2); a `StateUpdate` history item is written (8.2); when `ActionsEnabled` the new state's actions run (7.1); the "CloudWatch Alarm State Change" event goes to EventBridge (6.2). "An alarm invokes actions only when the alarm changes state. The exception is for alarms with Auto Scaling actions. For Auto Scaling actions, the alarm continues to invoke the action once per minute that the alarm remains in the new state." ([AlarmThatSendsEmail](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/AlarmThatSendsEmail.html)).

An evaluation that yields the current state (or "retain") changes nothing: a `missing` alarm on a metric with no data, which computes `INSUFFICIENT_DATA` every minute, kept `Unchecked: Initial alarm creation`, its `StateUpdatedTimestamp` and no `StateUpdate` item after 240 seconds (CWA-05, recorded 2026-11-03). Switch `cloudwatch.alarm.unchanged-state` `no-update`.

## 4. StateReason and StateReasonData

### 4.1 StateReason texts

| Situation | Text | Source |
|---|---|---|
| Created | `Unchecked: Initial alarm creation` | third-party-recorded ([secrets-logs-events-alarms.md 5.1](secrets-logs-events-alarms.md#51-resources)) |
| `SetAlarmState` | the caller's `StateReason` | third-party-recorded, `localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L6020` |
| N = 1, M unset, breaching | `Threshold Crossed: 1 datapoint [21.5 (<date>)] was greater than the threshold (21.0).` | third-party-recorded, same file L6308 (date masked by the test's transformer), validated 2026-02-26; also `localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch_metrics.snapshot.json:L122` (SampleCount, `[1.0 (<date>)] ... (0.0).`), validated 2024-01-03; captured with the date: `Threshold Crossed: 1 datapoint [3.22 (29/10/17 13:20:00)] was greater than the threshold (1.0).` (widdix/cloudwatch-alarm-to-slack `fb66af9` handler.js:L59) |
| N = 1, `GreaterThanOrEqualToThreshold` | `Threshold Crossed: 1 datapoint [1062305.0 (14/02/19 23:53:00)] was greater than or equal to the threshold (0.0).` | captured, alerta/alerta `4ac0106` tests/test_webhooks.py:L70 |
| N = 1, not breaching `LessThanThreshold` | `Threshold Crossed: 1 datapoint [1.0 (12/02/19 15:44:00)] was not less than the threshold (1.0).` | captured, terraform-aws-modules/terraform-aws-notify-slack `a7765ac` functions/messages/cloudwatch_alarm.json:L11 (the fixture's other fields look hand-edited) |
| M out of N form, breaching | `Threshold Crossed: 1 out of the last 1 datapoints [99.50160229693434 (02/10/19 16:59:00)] was greater than the threshold (50.0) (minimum 1 datapoint for OK -> ALARM transition).` | aws-doc, [cloudwatch-and-eventbridge](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/cloudwatch-and-eventbridge.html); the metric math example is the same form for an `INSUFFICIENT_DATA` -> `ALARM` change, so the label names the direction of the comparison, not the old state |
| M out of N form, not breaching | `Threshold Crossed: 1 out of the last 1 datapoints [0.0666851903306472 (01/10/19 13:46:00)] was not greater than the threshold (50.0) (minimum 1 datapoint for ALARM -> OK transition).` | aws-doc, same page |
| 3 of 3 | `Threshold Crossed: 3 out of the last 3 datapoints [32.6 (03/07/25 08:29:00), 33.8 (03/07/25 08:24:00), 41.0 (03/07/25 08:19:00)] were greater than the threshold (31.0)...` (the page elides the rest) | aws-doc, [alarm-actions](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-actions.html) |
| plural minimum (log alarm, same generator by its wording) | `... were greater than the threshold (100.0) (minimum 3 datapoints for OK -> ALARM transition).` | aws-doc, [alarms-and-actions-Lambda](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarms-and-actions-Lambda.html) |
| No data, `breaching`, N = 2 | `Threshold Crossed: no datapoints were received for 2 periods and 2 missing datapoints were treated as [Breaching].` | third-party-recorded, `localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L1993` and L2048, validated 2025-09-19 |
| No data, `missing` | `Insufficient Data: 5 datapoints were unknown.`; `Insufficient Data: 10 datapoints were unknown.` with `EvaluationPeriods` 10 | aws-doc ([alarms-and-actions-Lambda](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarms-and-actions-Lambda.html)); captured (alerta/alerta `4ac0106` tests/test_webhooks.py:L85) |
| Older shape, 2014 (do not emit) | `Threshold Crossed: 2 datapoints were not greater than the threshold (70.0). The most recent datapoints: [38.958, 40.292].` | aws-doc, aws/aws-cli `e9993cf` awscli/examples/cloudwatch/describe-alarm-history.rst:L16 (2014). The shape is not retired everywhere: AWS's 2025 Metrics Insights contributor example reads `Threshold Crossed: 1 datapoint was less than the threshold (1.0). The most recent datapoint which crossed the threshold: [0.0 (01/12/25 13:34:00)].` ([cloudwatch-and-eventbridge](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/cloudwatch-and-eventbridge.html), [alarm-actions](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-actions.html)); no source after 2014 shows it for a single-metric alarm (CWA-06 would) |

Rules read from these: datapoints are listed newest first, each `value (dd/MM/yy HH:mm:ss)` in UTC; the count word is `datapoint` for 1 and `datapoints` otherwise, and `out of the last N datapoints` is always plural; the verb is `was` for 1 and `were` otherwise; the negated phrase prefixes `not ` (`was not greater than`, `was not less than`); the sentence ends with `.`; the number in `Insufficient Data: N datapoints were unknown.` is N (alerta's sample has N = 10).

Recorded 2026-11-03 (CWA-01, CWA-04, CWA-06 to CWA-10; switch `cloudwatch.alarm.reason-format` `count-form-unless-datapoints-to-alarm`):

- The form follows option (a): the count form without `DatapointsToAlarm` (`1 datapoint [5.0 (...)] was greater than the threshold (3.0).`, `2 datapoints [6.0 (...), 5.0 (...)] were ...`), the M out of N form with it, even when M = N = 1 (`1 out of the last 1 datapoints [...] ... (minimum 1 datapoint for OK -> ALARM transition).`) (CWA-06).
- The listing names the real datapoints that cross in the transition's direction: `2 out of the last 3 datapoints [6.0 (03:13), 5.0 (03:11)]` for an M = 2 of N = 3 alarm over 5.0, 1.0, 6.0 (CWA-06); for OK, the non-breaching ones (`1 out of the last 3 datapoints [1.0 (...)] was not greater than ...`, CWA-04).
- The `OrEqual` phrases: `was greater than or equal to`, `was not greater than or equal to`, `was less than or equal to`, `was not less than or equal to` the threshold (CWA-07).
- Singular texts: `Insufficient Data: 1 datapoint was unknown.` (CWA-01, CWA-02, CWA-11) and `Threshold Crossed: no datapoints were received for 1 period and 1 missing datapoint was treated as [Breaching].` (CWA-08). The `notBreaching` label is `[NonBreaching]` (CWA-08).
- Under `breaching` or `notBreaching`, filled points add a clause after the threshold, in both forms and whichever way the transition goes: `1 datapoint [5.0 (...)] was greater than the threshold (3.0) and 2 missing datapoints were treated as [Breaching].` (CWA-08), `1 out of the last 3 datapoints [1.0 (...)] was not greater than the threshold (3.0) and 2 missing datapoints were treated as [Breaching] (minimum 1 datapoint for ALARM -> OK transition).` (CWA-04). Under `missing` and `ignore` no clause appears.
- With no real datapoint to list, the text counts the received datapoints and the evaluated periods (N): `no datapoints were received for 2 periods and ...` with none, `1 datapoint was received for 3 periods and 2 missing datapoints were treated as [NonBreaching].` and `2 datapoints were received for 3 periods and 1 missing datapoint was treated as [NonBreaching].` with some (CWA-01, CWA-04).
- A listing too long for the reason changes form: a 100 of 100 alarm wrote `Threshold Crossed: 100 out of the last 100 datapoints were greater than the threshold (3.0). The most recent datapoints which crossed the threshold: [<5 newest>] (minimum 100 datapoints for OK -> ALARM transition).`, 327 characters (CWA-10), the shape of the 2014 and Metrics Insights texts above. UNVERIFIED: the count at which this form starts (between 4 and 100 listed datapoints) and its count form; understudy uses it when the inline listing would pass 1023 characters, with the M out of N or count head as the alarm's form gives.
- UNVERIFIED: with more crossing real datapoints than the minimum, whether all are listed (no recording has that case); understudy lists them all.

### 4.2 StateReasonData JSON

Recorded for an evaluation with data (`localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L6408-L6426`, validated 2026-02-26; dates masked):

```
{"version":"1.0","queryDate":<date>,"startDate":<date>,"unit":"Seconds","statistic":"Average","period":10,
 "recentDatapoints":[21.5],"threshold":21.0,
 "evaluatedDatapoints":[{"timestamp":<date>,"sampleCount":2.0,"value":21.5}]}
```

Recorded for an evaluation with no data under `breaching`, N = 2 (same file L2049-L2065, validated 2025-09-19): `version`, `queryDate`, `unit`, `statistic`, `period`, `recentDatapoints: []`, `threshold`, `evaluatedDatapoints: [{"timestamp": ...}, {"timestamp": ...}]`, with no `startDate`.

AWS's own examples, verbatim:
- `{"version":"1.0","queryDate":"2019-10-02T17:04:40.985+0000","startDate":"2019-10-02T16:59:00.000+0000","statistic":"Average","period":300,"recentDatapoints":[99.50160229693434],"threshold":50.0}` ([cloudwatch-and-eventbridge](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/cloudwatch-and-eventbridge.html)).
- `{"version":"1.0","queryDate":"2023-08-04T12:31:29.591+0000","statistic":"Average","period":60,"recentDatapoints":[],"threshold":5.0,"evaluatedDatapoints":[{"timestamp":"2023-08-04T12:30:00.000+0000"},{"timestamp":"2023-08-04T12:29:00.000+0000"},{"timestamp":"2023-08-04T12:28:00.000+0000"},{"timestamp":"2023-08-04T12:27:00.000+0000"},{"timestamp":"2023-08-04T12:26:00.000+0000"}]}` ([alarms-and-actions-Lambda](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarms-and-actions-Lambda.html)).
- `recentDatapoints: [41, 33.8, 32.6]` with `startDate` 08:19 for the reason `[32.6 (... 08:29:00), 33.8 (... 08:24:00), 41.0 (... 08:19:00)]` ([alarm-actions](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-actions.html)).

Rules read from these: keys in the order `version, queryDate, startDate, unit, statistic, period, recentDatapoints, threshold, evaluatedDatapoints` (AWS's order: the snapshot library sorts keys when it records a response and parses JSON-string values such as `StateReasonData` only afterwards, so their keys keep the string's order, [eventbridge-buses.md 4.2](eventbridge-buses.md#42-the-stored-policy-document); AWS's examples above agree for every key but `unit`, which only the recording places); `unit` only when the alarm has a `Unit` (absent for the SampleCount alarm without one, `localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch_metrics.snapshot.json:L123-L138`); `statistic` absent for math alarms (aws-doc example); `startDate` is the oldest used datapoint's timestamp and is absent with no real datapoint; `evaluatedDatapoints` lists bins newest first, a missing bin as `{"timestamp"}` alone and a real one with `sampleCount` and `value`; `queryDate` is the evaluation time.

Recorded 2026-11-03 (CWA-01, CWA-04, CWA-06, CWA-08, CWA-10), every key order and number form above held, and:

- `recentDatapoints` spans the used bins oldest first, a missing bin between two used ones as `null`: `[1.0,null,5.0,null,5.0]` for `0 - X - X` (CWA-04), `[5.0,null,5.0,null,5.0]` for points in d2, d4 and d6 (CWA-10); missing bins outside that span are left out (`[5.0]` for one point in d2 of `- X -`, CWA-08). The used bins are the N newest real ones in case 1 of 3.5 and every real one otherwise; a value listed there need not cross (`[5.0,1.0,6.0]` beside a reason listing 6.0 and 5.0, CWA-06).
- `evaluatedDatapoints` lists only the bins the decision rests on: the real datapoints that cross in the transition's direction, then the newest filled bins that count the same way, up to M for ALARM, N - M + 1 for OK and N for INSUFFICIENT_DATA. The filled bins are the newest missing ones, as many as N less the real bins in the range; under `breaching` they count only toward ALARM, under `notBreaching` only toward OK, under `missing` and `ignore` either way. Examples: `[03:13 6.0, 03:11 5.0]` for the M = 2 of 3 alarm above, leaving out 03:12's 1.0 (CWA-06); `[03:10 (missing), 02:50 1.0]` for table 2 row 3 to OK under `missing` (CWA-04); `[02:50 1.0]` alone for table 1 row 2 to OK under `notBreaching`, where two filled bins were also available (CWA-04); `[03:05, 03:00]` (filled) for table 2 row 4 to ALARM under `breaching` (CWA-04); one bin for an N = 2, M = 2 alarm to OK on no data (CWA-08).
- The 4000-character limit: a 100 of 100 alarm wrote 3753 characters, `recentDatapoints` and `evaluatedDatapoints` holding 45 entries each, 03:13 back to 02:29, with `startDate` 02:29 (CWA-10). UNVERIFIED why 45: one more entry of each would still fit 4000. understudy drops the oldest evaluated entries, then the oldest recent ones, only past 4000.

### 4.3 Formatting

- Dates inside `StateReason`: `dd/MM/yy HH:mm:ss`, UTC, day first. AWS's examples pair `02/10/19 16:59:00` with `queryDate` 2019-10-02, `03/07/25` with 2025-07-03 and `10/06/26` with 2026-06-10 (aws-doc, pages cited in 4.1). LocalStack's comment "(03/01/24 11:36:00)" was written on 2024-01-03 (`localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch_metrics.py:L182`); its regex's name `(MM/DD/YY HH:MM:SS)` is wrong about the order (`test_cloudwatch.py:L1377-L1380`).
- Timestamps inside JSON (`queryDate`, `startDate`, `timestamp`, SNS `StateChangeTime`, Lambda `time`, EventBridge `state.timestamp`): `yyyy-MM-dd'T'HH:mm:ss.SSS+0000` (aws-doc examples above; captured `"StateChangeTime":"2017-10-30T13:20:35.831+0000"`). The EventBridge envelope `time` is second precision with `Z` (6.2).
- Numbers: every verbatim `StateReason`, `StateReasonData` and SNS body sample prints doubles the way Java's `Double.toString` does: a whole number keeps `.0` (`21.0`, `45628.0`, `1062305.0`, `"threshold":50.0`, `"sampleCount":2.0`), others print the shortest round-trip digits (`0.0666851903306472`, `99.50160229693434`); integer fields such as `period` print without a point. The one exception is the alarm-actions page's pretty-printed `stateReasonData` (`"recentDatapoints": [41, 33.8, 32.6]`, `"threshold": 31`, `"sampleCount": 5`), which is hand-edited (it carries `//` comments and an elided reason) while its own `stateReason` prints `41.0` and `(31.0)`; it is not evidence against `.0`. Values at or above 10^7 or below 10^-3 print as `Double.toString` prints them: `1.23456789E7`, `1.23E-4` and the threshold `5.0E-4` in both `StateReason` and `StateReasonData`, and the average `0.15000000000000002` (CWA-09, recorded 2026-11-03). Switch `cloudwatch.alarm.number-format` `java-double`.
- Numbers inside the EventBridge configuration objects (6.2) print the same way: `"threshold":3.0` and `4.0` in a metric alarm's create, update and delete events (CWA-15, recorded 2026-11-03), although AWS's metric alarm examples print whole thresholds without a point (`"threshold": 80` and `70` in the update example, `10000` in the metric math delete example; its log alarm examples print `100.0` and `200.0`, [cloudwatch-and-eventbridge](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/cloudwatch-and-eventbridge.html)). The examples were edited or rendered; the recording wins (section 11).

## 5. SNS notifications

### 5.1 Message body

The documented schema for a metric alarm, field order as AWS lists it ([Notify_Users_Alarm_Changes, alarm-sns-schema](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/Notify_Users_Alarm_Changes.html)): `AlarmName`, `AlarmDescription`, `AWSAccountId`, `AlarmConfigurationUpdatedTimestamp`, `NewStateValue`, `NewStateReason`, `StateChangeTime`, `Region`, `AlarmArn`, `OldStateValue`, `OKActions`, `AlarmActions`, `InsufficientDataActions`, `Trigger {MetricName, Namespace, StatisticType, Statistic, Unit ("string or null"), Dimensions [{value, name}], Period, EvaluationPeriods, DatapointsToAlarm, ComparisonOperator, Threshold, TreatMissingData, EvaluateLowSampleCountPercentile ("string or null")}`, then `AlarmTags` and `AlarmTagsStatus` only with tag propagation. The captured bodies of 2016 to 2019 have the same order for the fields they carry, except inside a `Dimensions` entry: `{"name","value"}` in 2016 (assertible/lambda-cloudwatch-slack `833964f` test/sns-cloudwatch-event.json:L13) and 2017 (widdix/cloudwatch-alarm-to-slack `fb66af9` handler.js:L59), `{"value","name"}` as in the schema in 2019 (alerta/alerta `4ac0106` tests/test_webhooks.py:L70).

Recorded bodies (the test parses the SNS envelope's `Message`; the snapshot sorts keys, so order is not recorded):

- `localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L6297-L6335` (validated 2026-02-26): all schema fields; `"ActionExecutedAfterMuteWindow": false` (L6299), which is not in the schema and not in the 2025 recordings below; `NewStateReason` as 4.1; `Trigger.Statistic` `"AVERAGE"`, `StatisticType` `"Statistic"`, `Unit` `"Seconds"`, `TreatMissingData` `"ignore"`, `EvaluateLowSampleCountPercentile` `""`, `Dimensions` `[{"name":"InstanceId","value":"abc"}]` (lower-case keys), `Threshold` `21.0`; no `DatapointsToAlarm` (the alarm set none).
- Same file L1983-L2020 (validated 2025-09-19): the no-data `breaching` case; `TreatMissingData` `"breaching"`.
- Same file L2482-L2519: after `SetAlarmState`, `NewStateReason` is the caller's text `testing alarm`, `OldStateValue` `INSUFFICIENT_DATA`.
- `localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch_metrics.snapshot.json:L154-L189` (validated 2024-01-03): `Statistic` `"SAMPLE_COUNT"` (L183), `"Unit": null` for an alarm without a unit (L187), `OKActions` `[]`.
- `Region` is the long name (the snapshot masks it as `<region-name-full:1>`; captured values in 5.2).

`Trigger.Statistic` is the statistic in upper snake case: `AVERAGE`, `SAMPLE_COUNT` (recorded), `SUM`, `MAXIMUM` (captured); `MINIMUM` by the same rule (inferred).

Recorded byte for byte (CWA-11, recorded 2026-11-03, `testdata/parity/sqs/CWA-11/receive-*.json`; switch `cloudwatch.alarm.sns.unset-fields`):

- Key order: `AlarmName, AlarmDescription, AWSAccountId, AlarmConfigurationUpdatedTimestamp, NewStateValue, NewStateReason, StateChangeTime, Region, AlarmArn, OldStateValue, ActionExecutedAfterMuteWindow, OKActions, AlarmActions, InsufficientDataActions, Trigger`: `ActionExecutedAfterMuteWindow` (`false`) sits after `OldStateValue`, not at the end.
- `AlarmDescription` is `null` without one; a description keeps its quotes escaped.
- `Trigger.TreatMissingData` is `""` when the alarm sets none, and `EvaluateLowSampleCountPercentile` `""` likewise.
- `Trigger.DatapointsToAlarm` is an integer after `EvaluationPeriods` when set.
- An `ExtendedStatistic` alarm's `Trigger` has `"StatisticType":"ExtendedStatistic","ExtendedStatistic":"p90"` and no `Statistic` key; a standard one `"StatisticType":"Statistic","Statistic":"MAXIMUM"`.
- `Trigger.Dimensions` are sorted by name whatever order they were sent in (`b` then `a` became `[{"value":"1","name":"a"},{"value":"2","name":"b"}]`), each entry `value` before `name`.
- `AlarmConfigurationUpdatedTimestamp` is `2026-11-03T03:15:36.145+0000`, the `StateChangeTime` format.

### 5.2 Subject and Region

No AWS page states the subject. Every captured notification has `<NEW STATE>: "<alarm name>" in <region long name>`:

- `ALARM: "cw-to-slack-Alarm-9THDKWBS1876" in US East (N. Virginia)` (widdix/cloudwatch-alarm-to-slack `fb66af9` handler.js:L58, 2017).
- `ALARM: "bucketbytesAlarm" in EU (Ireland)` and `INSUFFICIENT_DATA: "NotificationCount" in EU (Ireland)` (alerta/alerta `4ac0106` tests/test_webhooks.py:L69, L84, 2019); the second fixture's subject and body name different alarms, so it was edited.
- `ALARM: "awsrds-app-High-DB-Connections" in US West - Oregon` (assertible/lambda-cloudwatch-slack `833964f` test/sns-cloudwatch-event.json:L12, 2016, an older naming).
- `ALARM: "cfst-716-ac22d3f1c22bd4cb8c2a626d6879e183-InvalidSSHLoginAlarm-..." in US East (N. Virginia)` (linuxacademy/la-aws-security_specialty `9e1e27c` .../sns_event.json:L11, 2018), for the alarm `cfst-716-ac22d3f1c22bd4cb8c2a626d6879e183-InvalidSSHLoginAlarm-NNNFH7H1BW17`: exactly 100 characters, the name cut to its first 63 characters plus `...` inside the quotes. SNS documents subjects as under 100 characters ([sns.md 4.1](sns.md#41-publish-input-rules)); CloudWatch's own subject reaches 100, so its limit is at least 100 (captured, one sample).

The body's `Region` carries the same long name (`"Region":"US East (N. Virginia)"`, `"Region":"EU (Ireland)"` in the bodies above). Recorded (CWA-11, CWA-12, 2026-11-03): subjects `ALARM: "<name>" in US East (Ohio)`, `OK: ...` and `INSUFFICIENT_DATA: ...`, `Region` `US East (Ohio)` for us-east-2, and the truncation rule as captured: a 157-character name became `ALARM: "<its first 70 characters>..." in US East (Ohio)`, exactly 100 characters. Switch `cloudwatch.alarm.sns.subject`. UNVERIFIED: today's long names of other regions (AWS has since renamed `EU (Ireland)` to `Europe (Ireland)` in its consoles and documentation; one region per free-plan account, recording-kit.md 1); understudy's table holds the General Reference names.

### 5.3 Delivery

- The SNS envelope around the message, and its SQS and Lambda forms, are [sns.md 5.1](sns.md#51-sqs-envelope-raw-delivery-disabled) and [sns.md 5.4](sns.md#54-lambda-event); CloudWatch publishes with the `Subject` of 5.2, so it appears in the envelope, and with no message attributes (captured `"MessageAttributes": {}`, widdix/cloudwatch-alarm-to-slack `fb66af9` handler.js).
- "CloudWatch guarantees the publication of all alarm notifications to both types of topics. However, even if you use a FIFO topic, in rare cases CloudWatch sends the notifications to the topic out of order. If you use a FIFO topic, the alarm sets the message group ID of the alarm notifications to be a hash of the ARN of the alarm." The topic policy should grant `cloudwatch.amazonaws.com` `SNS:Publish` with `aws:SourceArn` of the alarm ([Notify_Users_Alarm_Changes](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/Notify_Users_Alarm_Changes.html)).
- "CloudWatch doesn't test or validate the actions that you specify, nor does it detect any Amazon EC2 Auto Scaling or Amazon SNS errors resulting from an attempt to invoke nonexistent actions." ([AlarmThatSendsEmail](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/AlarmThatSendsEmail.html)).
- On a FIFO topic without content-based deduplication the notification arrives, with `MessageGroupId` the lower-case hex SHA-256 of the alarm ARN and a `MessageDeduplicationId` that is a v4 UUID matching no digest of the ARN (`1d201fd4-5b5e-482f-a55b-03df10d028ae`), and the history item `Successfully executed action` (CWA-13, recorded 2026-11-03). Switch `cloudwatch.alarm.sns.fifo-group` `sha256-hex-of-alarm-arn,uuid-dedup-id`. AWS's id is presumably random; understudy derives a v4 UUID from the ARN and the id of the call or evaluation that made the transition, so two transitions never share it, frozen clock or not.
- A topic whose policy does not grant `cloudwatch.amazonaws.com` `SNS:Publish` receives nothing; the alarm still transitions, and the history gets an `Action` item `Failed to execute action <topic ARN>` with `"error":"CloudWatch Alarms is not authorized to perform: SNS:Publish on resource:<topic ARN>"` (CWA-23, recorded 2026-11-03). So AWS checks the topic policy (12.4).

## 6. Lambda and EventBridge payloads

### 6.1 Lambda event

Lambda alarm actions take `arn:aws:lambda:region:account-id:function:function-name`, with `:version-number` or `:alias-name` (model doc of `PutMetricAlarmInput$AlarmActions`). "CloudWatch alarms guarantees an asynchronous invocation of the Lambda function for a given state change, except in the following cases: When the function doesn't exist. When CloudWatch is not authorized to invoke the Lambda function. If CloudWatch can't reach the Lambda service or the message is rejected for another reason, CloudWatch retries until the invocation is successful." The resource policy names principal `lambda.alarms.cloudwatch.amazonaws.com` with `AWS:SourceAccount` and optionally `AWS:SourceArn` ([alarms-and-actions-Lambda](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarms-and-actions-Lambda.html)).

Recorded event for a `SetAlarmState` change (`localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L5-L42`, validated 2024-01-03): `{source: "aws.cloudwatch", alarmArn, accountId, time, region, alarmData: {alarmName, state: {value: "ALARM", reason: "testing alarm", timestamp}, previousState: {value: "INSUFFICIENT_DATA", reason: "Unchecked: Initial alarm creation", timestamp}, configuration: {description, metrics: [{id: <uuid>, metricStat: {metric: {namespace, name, dimensions: {}}, period: 10, stat: "Average"}, returnData: true}]}}}`. `region` is the region code (`'region': 'us-east-1'`), `time` has milliseconds and `+0000`, `state.reasonData` appears when there is one, and `metricStat.unit` when the alarm has one (aws-doc example, same page). A single-metric alarm is described as a one-element `metrics` array with a generated `id`, not with its `MetricName` fields.

### 6.2 EventBridge events

"CloudWatch sends events to Amazon EventBridge whenever a CloudWatch alarm is created, updated, deleted, or changes alarm state." "CloudWatch guarantees the delivery of alarm state change events to EventBridge." ([cloudwatch-and-eventbridge](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/cloudwatch-and-eventbridge.html)). From the same page's examples:

- State change: `{"version":"0","id":<uuid>,"detail-type":"CloudWatch Alarm State Change","source":"aws.cloudwatch","account":<account>,"time":"2019-10-02T17:04:40Z","region":"us-east-1","resources":[<alarm ARN>],"detail":{"alarmName", "configuration":{"description", "metrics":[...]}, "previousState":{"reason","reasonData","timestamp","value"}, "state":{"reason","reasonData","timestamp","value"}}}`, with `alarmTags`, `alarmTagsStatus` only with tag propagation (same page). Five of the page's state change examples, dated 2019 to 2025, also carry `muteDetail` (`mutedByArn`, `muteWindowStart`, `muteWindowEnd`) with a 2026 mute window, so those examples were edited; that `muteDetail` appears only for an alarm a mute rule covers is inferred, and understudy, with mute rules unbound (7.1), never emits it. `reasonData` is the `StateReasonData` string (4.2).
- Configuration change: `"detail-type":"CloudWatch Alarm Configuration Change"`, `detail: {alarmName, operation: "create" | "update" | "delete", state: {value, timestamp}, configuration: {...}, previousConfiguration: {...} (update only)}`. The metric alarm update example's `configuration` carries `evaluationPeriods, threshold, comparisonOperator, treatMissingData, metrics[], alarmName, description, actionsEnabled, timestamp, okActions, alarmActions, insufficientDataActions`; the page shows create and delete only for composite, math and log alarms.
- The `id` of the generated metric changes between the update example's `configuration` (`86bfa85f-...`) and `previousConfiguration` (`d6bfa85f-...`).
- "Muting an alarm does not stop CloudWatch from sending alarm events ... to Amazon EventBridge." ([alarm-actions](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-actions.html)).

Recorded 2026-11-03 (CWA-14, CWA-15; the events reached an SQS queue through a rule, `testdata/parity/sqs/CWA-14`, `CWA-15`):

- State change events are sent while `ActionsEnabled` is false; no Lambda action ran (CWA-14). Switch `cloudwatch.alarm.events.actions-disabled` `events-sent`. (The first run lost every event of an alarm created 2 seconds after its rule, which the item read as rule propagation; the re-run after a 60-second wait has them.)
- `DisableAlarmActions` is a configuration update: it sends an `"operation":"update"` configuration change event with `previousConfiguration`, `actionsEnabled` false against true, and moves the configuration `timestamp`, which is `AlarmConfigurationUpdatedTimestamp` (CWA-14). `EnableAlarmActions` is unrecorded and inferred to match.
- The generated metric `id` stays across state changes and across `DisableAlarmActions`, and changes on each `PutMetricAlarm` (CWA-14, CWA-15). Switch `cloudwatch.alarm.events.metric-id` `one-per-configuration`.
- Create, update and delete events of a metric alarm carry the update example's `configuration` fields, with `datapointsToAlarm` after `evaluationPeriods` when set and `description` absent (not null) without an `AlarmDescription`; numbers print as Java doubles (4.3). The `state` object is `{value, timestamp}` on create and update and `{value, reason, timestamp}` on delete.
- A state change detail's keys come in the order `alarmName, state, previousState, configuration`, and its `configuration` is `{metrics, description?}`, description last.
- The Lambda events were not captured: the item's log query found nothing in the function's log group, so 6.1's shape stays LocalStack's (third-party-recorded). understudy gives its `configuration` the state change event's order (inferred).

## 7. Alarm actions

### 7.1 When actions run

- Actions run "only on state transitions, and are not performed again if the condition persists for hours or days", Auto Scaling excepted ([alarm-actions](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-actions.html)). `OKActions`, `AlarmActions` and `InsufficientDataActions` run on a transition "to an OK state from any other state" and so on (model doc of `PutMetricAlarmInput`), up to 5 ARNs each (model `com.amazonaws.cloudwatch#ResourceList`).
- `ActionsEnabled` default true: "Indicates whether actions should be executed during any changes to the alarm state." `DisableAlarmActions`: "When an alarm's actions are disabled, the alarm actions do not execute when the alarm state changes." (model docs). Enabling actions later does not run the current state's actions, since actions belong to transitions (inferred).
- Mute rules (`PutAlarmMuteRule`) mute actions in time windows; `EnableAlarmActions` does not unmute ([alarm-actions](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-actions.html)). They stay unbound in understudy, so `ActionExecutedAfterMuteWindow` is always false.

### 7.2 Action kinds and what understudy can do

| Kind | ARN form (model doc of `PutMetricAlarmInput$AlarmActions`) | AWS behavior | understudy |
|---|---|---|---|
| SNS | `arn:aws:sns:region:account-id:sns-topic-name` | publish 5.1 to the topic, every state | publish through its own SNS (`delivery`), then SNS fans out as usual |
| Lambda | `arn:aws:lambda:region:account-id:function:name[:version or alias]` | asynchronous invoke with 6.1 | asynchronous invoke through its own Lambda |
| EC2 | `arn:aws:automate:region:ec2:stop` (also `terminate`, `reboot`, `recover`), `arn:aws:swf:region:account-id:action/actions/AWS_EC2.InstanceId.Stop/1.0` and the other three | acts on the instance in the alarm's `InstanceId` dimension | no EC2: journal "not run", never claim success |
| Auto Scaling | `arn:aws:autoscaling:region:account-id:scalingPolicy:policy-id:autoScalingGroupName/...:policyName/...` | runs the policy, and again every minute while in the state | no Auto Scaling: journal "not run" |
| Systems Manager | `arn:aws:ssm:region:account-id:opsitem:severity#CATEGORY=category-name`, `arn:aws:ssm-incidents::account-id:responseplan/response-plan-name` | OpsItem or incident, `ALARM` only ([alarm-actions](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-actions.html)) | no OpsCenter or Incident Manager (Parameter Store on the roadmap is a different API): journal "not run" |
| Investigation | `arn:aws:aiops:region:account-id:investigation-group:investigation-group-id` (`AlarmActions` only) | starts an investigation | journal "not run" |

## 8. Alarm history

### 8.1 DescribeAlarmHistory

"If an alarm name is not specified, the histories for either all metric alarms or all composite alarms are returned." "CloudWatch retains the history of an alarm even if you delete the alarm." `AlarmTypes` omitted returns metric alarms only. `ScanBy` `TimestampDescending` returns the newest first ([API_DescribeAlarmHistory](https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/API_DescribeAlarmHistory.html)). "CloudWatch preserves alarm history for 30 days. Each state transition is marked with a unique timestamp. In rare cases, your history might show more than one notification for a state change." ([AlarmThatSendsEmail](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/AlarmThatSendsEmail.html)). AWS's CLI example lists the newest item first without `ScanBy` (aws-doc, aws/aws-cli `e9993cf` awscli/examples/cloudwatch/describe-alarm-history.rst:L12-L27), so the default is descending (inferred).

The page size without `MaxRecords` is 50, with a `NextToken`, newest first (CWA-18, recorded 2026-11-03: 60 `StateUpdate` items, the first page from `o30` back to `a6`). Switch `cloudwatch.alarm.history.page-size` `50`.

### 8.2 StateUpdate items

Recorded (`localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L6336-L6380`, validated 2026-02-26): `AlarmName`, `AlarmType` `"MetricAlarm"`, `HistoryItemType` `"StateUpdate"`, `HistorySummary` `"Alarm updated from INSUFFICIENT_DATA to ALARM"`, `Timestamp`, and `HistoryData` = `{"version":"1.0","oldState":{"stateValue":"INSUFFICIENT_DATA","stateReason":"Unchecked: Initial alarm creation"},"newState":{"stateValue":"ALARM","stateReason":<4.1>,"stateReasonData":{<4.2>}}}`.

`HistoryData` is a string; inside it `stateReasonData` is a nested object, not a string, and is absent for a state set without data: AWS's example `"HistoryData": "{\"version\":\"1.0\",\"oldState\":{\"stateValue\":\"ALARM\",\"stateReason\":\"testing purposes\"},\"newState\":{\"stateValue\":\"OK\",\"stateReason\":\"Threshold Crossed: ...\",\"stateReasonData\":{\"version\":\"1.0\",...,\"threshold\":70.0}}}"` (aws-doc, aws/aws-cli `e9993cf` awscli/examples/cloudwatch/describe-alarm-history.rst:L16). `HistorySummary` is `Alarm updated from <old> to <new>` (recorded and aws-doc).

### 8.3 ConfigurationUpdate and Action items

No public source showed a `ConfigurationUpdate` or `Action` item (LocalStack's one history test filters `HistoryItemType=StateUpdate`, `localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch.py:L1451-L1453`). Recorded 2026-11-03 (CWA-16; also CWA-02, CWA-14, CWA-22, CWA-23):

- `ConfigurationUpdate` on create, update and delete: `HistorySummary` `Alarm "<name>" created`, `... updated`, `... deleted`; `HistoryData` `{"version":"1.0","type":"Create","createdAlarm":{...}}`, `"type":"Update","updatedAlarm":{...},"originalUpdatedFields":{...}` (the changed fields' old values, `alarmConfigurationUpdatedTimestamp` among them) and `"type":"Delete","deletedAlarm":{...}`. The alarm object's keys: `alarmName, alarmArn, alarmConfigurationUpdatedTimestamp, namespace, metricName, extendedStatistic, period, dimensions, threshold, comparisonOperator, evaluationPeriods, treatMissingData, stateValue, stateUpdatedTimestamp, actionsEnabled, alarmActions, insufficientDataActions, okactions` (sic), a standard statistic printed as `"extendedStatistic":"MAXIMUM"`. `DisableAlarmActions` writes an update item too (CWA-14). The delete item stays after the alarm is gone, as the `StateUpdate` items do.
- `Action` on each action run: `Successfully executed action <ARN>` with `{"actionState":"Succeeded","stateUpdateTimestamp":<epoch ms>,"notificationResource":<ARN>,"publishedMessage":"{\"default\":\"<the notification body>\"}"}`; `Failed to execute action <ARN>` with `"actionState":"Failed"`, `"publishedMessage":null`, `"error":"Topic does not exist"` for a missing topic (or the authorization text of 5.3 for a topic that refuses CloudWatch), and `"muteWindowEndTimestamp":null`.

understudy writes neither kind yet and says so in the `DescribeAlarmHistory` coverage note (12.1); writing them is a follow-up.

## 9. GetMetricStatistics

### 9.1 Request rules

From the model docs of `com.amazonaws.cloudwatch#GetMetricStatisticsInput`, identical on [API_GetMetricStatistics](https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/API_GetMetricStatistics.html):

- `StartTime` is inclusive and "evaluated relative to the time that CloudWatch receives the request"; it rounds down to the minute (under 15 days old), to 5 minutes (15 to 63 days) or to the hour (older); "If you set Period to 5, 10, 20, or 30, the start time of your request is rounded down to the nearest time that corresponds to even 5-, 10-, 20-, or 30-second divisions of a minute". This is the rule `roundStart` in `internal/service/cloudwatch/metricdata.go` already applies for `GetMetricData`.
- `EndTime` is exclusive.
- `Period`: "For metrics with regular resolution, a period can be as short as one minute (60 seconds) and must be a multiple of 60. For high-resolution metrics ... the period can be 1, 5, 10, 20, 30, 60, or any multiple of 60." Past 3 hours the period must be a multiple of 60, past 15 days of 300, past 63 days of 3600, "or no data points in that time range is returned" (`periodServes` in `metricdata.go`).
- `Statistics` and `ExtendedStatistics`: "you must specify either Statistics or ExtendedStatistics, but not both"; percentiles are `p0.0` to `p100`.
- `Dimensions`: "If the metric contains multiple dimensions, you must include a value for each dimension. ... If a specific combination of dimensions was not published, you can't retrieve statistics for it. You must specify the same dimensions that were used when the metrics were created." So the match is exact.
- `Unit`: "If you omit Unit, all data that was collected with any unit is returned, along with the corresponding units ... If you specify a unit that does not match the data collected, the results of the operation are null. CloudWatch does not perform unit conversions."

### 9.2 Response

- `Label` is the metric name; no data is `{"Datapoints": [], "Label": "metric_name"}` (`localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L5853-L5854`, validated 2025-09-19).
- One datapoint per period that has data, at the period's start, periods anchored at the rounded `StartTime`: AWS's example asks from `2014-04-08T23:18:00Z` with period 3600 and gets timestamps at `:18:00` of each hour, 23 of the 24 hours, listed out of order (aws-doc, aws/aws-cli `e9993cf` awscli/examples/cloudwatch/get-metric-statistics.rst). "Data points are not returned in chronological order." (model doc of `GetMetricStatistics`.)
- A datapoint carries only the statistics asked for, plus `Timestamp` and `Unit` (the CLI example asks `Maximum` and gets `{Timestamp, Maximum, Unit}`); all five recorded: `{"Average":4.5,"Maximum":9.0,"Minimum":0.0,"SampleCount":10.0,"Sum":45.0,"Timestamp":...,"Unit":"None"}` for values 0 to 9 in one minute (same file L4225-L4236, validated 2025-09-19). Data put without a unit reports `"Unit": "None"`.
- Without `Unit`, data of several units in one period gives one datapoint per unit: values 1 (`Seconds`), 5 (`Count`) and 10 (no unit) at one timestamp give three datapoints with `Average` 1.0, 5.0 and 10.0 and units `Seconds`, `Count`, `None` (same file L4296-L4320, validated 2025-09-19). This differs from `GetMetricData`, which returns one unit and a `MultipleUnits` message (`oneUnit` in `metricdata.go`).
- Retention: "Data points with a period of less than 60 seconds are available for 3 hours", 60 seconds for 15 days, 300 seconds for 63 days, 3600 seconds for 455 days (model doc).

### 9.3 Errors and limits

- "The maximum number of data points returned from a single call is 1,440. If you request more than 1,440 data points, CloudWatch returns an error." (model doc). Recorded for `StartTime` now, `EndTime` now plus one day, `Period` 1: `InvalidParameterCombination`, 400, `You have requested up to 86400 datapoints, which exceeds the limit of 1440. You may reduce the datapoints requested by increasing Period, or decreasing the time range.` (same file L5828-L5836, validated 2025-09-19, identical on the query and CBOR variants). So the count is taken before the 1440 cap and before any data is read, and a period of 1 on a regular metric is not itself an error.
- Recorded for `StartTime` equal to `EndTime`: `InvalidParameterValue`, 400, `The parameter StartTime must be less than the parameter EndTime.` (same file L5841-L5849).
- The same recorded test also asks `StartTime` now (not minute-aligned), `EndTime` now plus one day, `Period` 60, and every protocol variant is accepted with `{"Datapoints": [], "Label": "metric_name"}` (same file L5813, L5852, L5891). Rounding `StartTime` down to the minute first would make that span 1440 periods plus a fraction, so the count is not the ceiling of the rounded span (inferred; it holds unless every run started on a whole minute).
- The count is the floor of the unrounded span over the period: AWS refused 1441 whole periods (`You have requested up to 1441 datapoints, ...`) and accepted exactly 1440, 1440.5 periods from an aligned start, 1440.5 from a start at :30 (1441 once rounded down) and 1439.98 (1440.5 once rounded) (CWA-20, recorded 2026-11-03; its captures are `testdata/parity/cloudwatch/CWA-19/11-*` to `15-*`). Switch `cloudwatch.get-metric-statistics.datapoint-limit` `floor-unrounded`.
- The refusals (CWA-19, recorded 2026-11-03): neither `Statistics` nor `ExtendedStatistics` is `InvalidParameterCombination` 400 `At least one of the parameters Statistics and ExtendedStatistics must be specified.` (not the `MissingParameter` candidate); a `Period` of 7 is `InvalidParameterValue` 400 `The parameter Period must be a value in the set [1, 5, 10, 20, 30].`; 61 and 90 are `InvalidParameterValue` 400 `The parameter Period must be a multiple of 60.` (90 is refused, not answered empty). Both lists together are accepted: 200 with no datapoints, the `PutMetricData` point about one second old; UNVERIFIED whether that point was not yet visible or both lists answer nothing. understudy answers both lists, as `ExtendedStatistics` alone, with 501 (12.7).
- On the JSON wire these errors' `__type` is `com.amazon.coral.service#InvalidParameterCombinationException` and `com.amazon.coral.service#InvalidParameterValueException` (CWA-19), not the `com.amazonaws.cloudwatch.v2010_08_01#` namespace [error-matrix.md](error-matrix.md) finding 4 records for CloudWatch; the success body orders `Datapoints` before `Label` and each datapoint's statistics before `Timestamp`, and prints `5.0` (CWA-19). understudy's codec differs on all three (the replay of `cloudwatch/CWA-19` shows it).

### 9.4 DescribeAlarmsForMetric

"This operation retrieves only standard alarms that are based on the specified metric. It does not return alarms based on math expressions that use the specified metric, or composite alarms"; filters `Statistic`, `ExtendedStatistic`, `Period`, `Unit`; "If the metric has any associated dimensions, you must specify them in order for the call to succeed." ([API_DescribeAlarmsForMetric](https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/API_DescribeAlarmsForMetric.html)). Recorded response: `{"MetricAlarms": [<MetricAlarm with StateReasonData>]}` with no `CompositeAlarms` key (`localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L6381-L6435`, validated 2026-02-26).

## 10. Terraform

`aws_cloudwatch_metric_alarm` (hashicorp/terraform-provider-aws v6.68.0 `internal/service/cloudwatch/metric_alarm.go`):

- Reads with `DescribeAlarms{AlarmNames: [name], AlarmTypes: [MetricAlarm]}` (L524-L531) and flattens configuration fields only: `actions_enabled`, the three action lists, description, operator, `datapoints_to_alarm`, dimensions, `evaluate_low_sample_count_percentiles`, `evaluation_periods`, statistics, metric fields, `period`, `threshold`, `unit`, `treat_missing_data`, `warm_up_configuration` (L905-L974). It never reads `StateValue`, `StateReason`, `StateReasonData` or the state timestamps, so evaluation cannot make a plan differ.
- Always sends `TreatMissingData`, `missing` by default (L335-L339 schema default, L652 `apiObject.TreatMissingData = aws.String(d.Get("treat_missing_data").(string))`), and reads it back defaulting to `missing` when absent (L964-L968). Terraform-made alarms therefore always carry an explicit treatment.
- `actions_enabled` defaults to true and is always sent (L60-L64, L598-L600); `evaluate_low_sample_count_percentiles` is `Computed` (L107-L111), so its absence from `DescribeAlarms` causes no diff.
- Update calls `PutMetricAlarm` only when a non-tag attribute changed (L444-L452); delete treats `ResourceNotFoundException` as done (L467, see [secrets-logs-events-alarms.md 5.4](secrets-logs-events-alarms.md#54-what-terraform-calls-aws_cloudwatch_metric_alarm)).

## 11. Conflicts between sources

| Topic | Sources | Choice |
|---|---|---|
| Premature alarm rule vs table 2 row 3 (`0 - X - -`, M = 2) | The rule text ("If the oldest breaching data point during the evaluation range is at least as old as the value of Datapoints to Alarm, and all of the more recent data points are either breaching or missing, the alarm goes into ALARM state no matter the value of M") predicts ALARM under `missing`; the table says OK | the table, which CWA-04 recorded (OK); 3.5 step C2 reconciles |
| `AWS/DynamoDB` missing data | API reference "always ignore"; user guide "default to ignore ... You can override this" | the user guide: CWA-21 recorded the override (3.7); switch `cloudwatch.alarm.dynamodb-missing-data` `as-configured` |
| StateReason form | count form (2017, 2019, 2024, 2026 samples) and M out of N form (2018 sample, AWS's examples) for the same N | 4.1 option (a), which CWA-06 recorded |
| Event configuration numbers | AWS's metric alarm examples print `"threshold": 80`; CWA-15 recorded `"threshold":3.0` | the recording (4.3) |
| What notifications carry | [alarm-actions](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-actions.html): "Single-metric alarms include both a state reason and detailed state reason data"; the SNS schema and every recorded SNS body have `NewStateReason` and no reason data | SNS bodies follow the schema and recordings; Lambda and EventBridge payloads carry `reasonData` (6.1, 6.2) |
| Evaluation range | one documented size (5 for N = 3, 5-minute period); "depends on the length of the alarm period and whether it is based on a metric with standard resolution or high resolution" | CWA-01 recorded N + 5 at 60 s and N + 2 at 300 s; N + 2 elsewhere, inferred (3.4) |
| Pinned model vs pages | `ActionConfiguration` and `IN_WARM_UP` documented, absent from the pin; AWS returns `IN_WARM_UP` (CWA-22) | follow the pin until it moves (1.3) |
| Warm-up end | [alarm-warm-up](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/alarm-warm-up.html): the window "is full when a datapoint exists for the most recent period"; the same page's example ends warm-up only once all three periods have data (10:04 to about 10:19) | the example, which CWA-22 recorded (2.4) |
| LocalStack date mask | regex named `(MM/DD/YY HH:MM:SS)` | day first, from AWS's examples (4.3) |

## 12. Implications for understudy

### 12.1 Operations and coverage notes

| Operation | Status | Coverage note |
|---|---|---|
| `PutMetricAlarm` | Partial | `alarms on a Statistic with a sliding window are evaluated on the emulator clock; alarms on an ExtendedStatistic, Metrics, a ThresholdMetricId or EvaluationCriteria, or with a WallClockWindow, are stored and never evaluated; SNS and Lambda actions run, and EC2, Auto Scaling, Systems Manager and investigation actions are journaled and never run; EvaluationState is never reported` |
| `SetAlarmState` | Partial | `the state is set and its actions run when it changes; the next evaluation re-derives it, except on alarms that are never evaluated (see PutMetricAlarm)` |
| `EnableAlarmActions`, `DisableAlarmActions` | Full (from StoredOnly) | they now gate actions; EventBridge events are sent either way (6.2) |
| `DescribeAlarms` | Partial, note unchanged | `StateReasonData` is now filled by evaluation |
| `DescribeAlarmHistory` | Partial | `StateUpdate items only, kept 30 days on the emulator clock and after the alarm is deleted; ConfigurationUpdate and Action items are never recorded, and AlarmContributorId selects nothing` (AWS writes both, 8.3; a follow-up) |
| `DescribeAlarmsForMetric` | Full | exact namespace, name and dimension match, optional statistic, period and unit filters (9.4) |
| `GetMetricStatistics` | Partial | `Statistics only: ExtendedStatistics answers 501; points are kept until reset, not aged out by resolution` |
| `PutCompositeAlarm`, `PutAlarmMuteRule` and the other mute rule operations, `PutAnomalyDetector`, `PutLogAlarm` | unbound (501) | composite alarms, mute rules, anomaly bands and log alarms are not evaluated, so binding them would fake behavior |

The guide (`docs/guide/`) must say that understudy publishes no vended `AWS/*` metrics: an alarm on `AWS/SQS NumberOfMessagesSent` sees only missing data, so under `breaching` it goes to ALARM.

### 12.2 Fidelity switches

Recorded 2026-11-03: every alarm switch's item was recorded; the labels and defaults below are the recorded ones. None is replayed: the alarm items wait on evaluations or read the queues deliveries filled, which `internal/parity` cannot drive (CWA-19 and CWA-20 are replayed but differ on the JSON wire form, 9.3).

| Switch | Default | Label | Grounded in | Settled by |
|---|---|---|---|---|
| `cloudwatch.alarm.evaluation-cadence` | `minute,10s-for-high-resolution,hourly-past-one-day` | aws-doc | 3.1 | |
| `cloudwatch.alarm.evaluation-phase` | `minute-boundary` | inferred | 3.1 | |
| `cloudwatch.alarm.first-evaluation` | `next-boundary` | aws-recorded | 3.1 | CWA-03 |
| `cloudwatch.alarm.window-anchor` | `evaluation-minute` | aws-doc | 3.2 | CWA-02 recorded sub-minute periods reading a lagged window, which understudy does not (follow-up) |
| `cloudwatch.alarm.mixed-units` | `first-unit-by-name` | inferred | 3.3 | AWS documents it as undefined |
| `cloudwatch.alarm.evaluation-range` | `n-plus-5-for-60s,n-plus-2-otherwise` | inferred (recorded at 60 and 300 s) | 3.4 | CWA-01 |
| `cloudwatch.alarm.unaged-breach` | `alarm-if-aged-in-range-else-retain` | aws-recorded | 3.5 | CWA-04 |
| `cloudwatch.alarm.unchanged-state` | `no-update` | aws-recorded | 3.8 | CWA-05 |
| `cloudwatch.alarm.set-same-state` | `no-change` | aws-recorded | 2.3 | CWA-17 |
| `cloudwatch.alarm.dynamodb-missing-data` | `as-configured` | aws-recorded | 3.7 | CWA-21 |
| `cloudwatch.alarm.warm-up-end` | `every-period-has-data` | aws-recorded | 2.4 | CWA-22 |
| `cloudwatch.alarm.reason-format` | `count-form-unless-datapoints-to-alarm` | aws-recorded | 4.1 | CWA-06, CWA-07, CWA-08 |
| `cloudwatch.alarm.number-format` | `java-double` | aws-recorded | 4.3 | CWA-09, CWA-15 |
| `cloudwatch.alarm.sns.unset-fields` | `description-null,treat-missing-data-empty,unit-null,low-sample-empty,datapoints-to-alarm-omitted,mute-window-false` | aws-recorded | 5.1 | CWA-11 |
| `cloudwatch.alarm.sns.subject` | `state-quoted-name-in-region-long-name,100-chars` | aws-recorded | 5.2 | CWA-11, CWA-12 |
| `cloudwatch.alarm.sns.fifo-group` | `sha256-hex-of-alarm-arn,uuid-dedup-id` | aws-recorded | 5.3 | CWA-13 |
| `cloudwatch.alarm.events.actions-disabled` | `events-sent` | aws-recorded | 6.2 | CWA-14 |
| `cloudwatch.alarm.events.metric-id` | `one-per-configuration` | aws-recorded | 6.2 | CWA-14, CWA-15 |
| `cloudwatch.alarm.actions.denied` | `dropped` | aws-recorded | 12.4 | CWA-23 |
| `cloudwatch.alarm.history.page-size` | `50` | aws-recorded | 8.1 | CWA-18 |
| `cloudwatch.get-metric-statistics.datapoint-limit` | `floor-unrounded` | aws-recorded | 9.3 | CWA-20 |
| `cloudwatch.get-metric-statistics.order` | `timestamp-ascending,then-unit` | inferred | 9.2 | AWS documents no order |

`cloudwatch.get-metric-data.start-rounding` (existing) also governs `GetMetricStatistics`.

### 12.3 Evaluator design

1. A pure function in `internal/service/cloudwatch`, `evaluate(cfg, bins, current) -> Outcome`, where `bins` is newest first and each bin is a sum type `missing | value(Stats)`, and `Outcome` is `transition{to, reason, reasonData} | retain`. No clock, no store. The 40 cells of 3.6 and the two extra vectors (x1, x2) are table tests; the extra vectors and every switch-dependent case name their switch.
2. Properties (`rapid`): with at least N real bins the treatment has no effect; if `missing` yields ALARM then `breaching` yields ALARM; `notBreaching` yields ALARM only with at least M real breaching bins; `ignore` yields either what `missing` yields or `retain`, and never INSUFFICIENT_DATA; the outcome depends only on bins d1..dR.
3. Bins come from the existing series store with the bucketing `query` in `metricdata.go` already does, started at T0 - R x Period (3.2), so `GetMetricData`, `GetMetricStatistics` and alarms share one aggregation. `GetMetricStatistics` must not reuse `oneUnit` (9.2).
4. Scheduling: one emulator-clock timer per evaluable alarm, due at its next boundary (3.1), armed by `PutMetricAlarm`, disarmed by `DeleteAlarms`, re-armed by `Rearm` after a snapshot import (today `Rearm` is a no-op in `internal/service/cloudwatch/stateful.go`). `understudy clock advance 5m` then runs five evaluations in order, so tests are deterministic; a day of advance is 1440 evaluations per alarm. Each evaluation is the kernel's `TimerFired` journal event and draws one seeded request id, as every timer does; evaluations that change nothing write nothing and emit no further journal event; a transition adds a `StateChange` event whose cause is the evaluation timer, and its deliveries are children.
5. The clock caveat: `PutMetricData` accepts a timestamp inside its window by wall time or by emulator time (`cloudwatch.put-metric-data.timestamp-window`), but alarms read bins on the emulator clock. Data an application stamps with wall time while the emulator clock is frozen or advanced far away lands outside every alarm window. Data put without a `Timestamp` is stamped with the emulator clock and always lines up. The guide must say so.
6. `AlarmState` gains nothing new beyond filling `ReasonData`; the comment "understudy never evaluates an alarm" on `AlarmState` in `internal/service/cloudwatch/state.go` must go. History needs its own store keyed by scope and alarm name that outlives the alarm, pruned at 30 days on the emulator clock.
7. Warm-up: honor `WarmUpConfiguration` as 2.4 describes, ending early with the flag false per `cloudwatch.alarm.warm-up-end`; report no `EvaluationState` (absent from the pin).

### 12.4 Actions and delivery

1. Alarm deliveries cross packages: `internal/delivery`'s closed `Mechanism` set (`internal/delivery/delivery.go`) gains `CloudWatchToSNS` and `CloudWatchToLambda` (wire `cloudwatch-to-sns`, `cloudwatch-to-lambda`), the ADR 0015 amendment of 2026-10-30 and a row in the [ARCHITECTURE.md](../ARCHITECTURE.md) section 16 table.
2. EventBridge events (6.2) need a way into a bus. Options: a new `delivery.Target` variant for an event bus, or an internal `PutEvents` operation as Lambda's logs use `PutLogEventsInternal` (ARCHITECTURE.md 16). The internal operation is smaller and keeps matching inside EventBridge; recommended. Events go to the default bus of the alarm's account and Region, "the EventBridge default event bus, which automatically receives events from AWS services" ([eb-event-bus](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-event-bus.html)).
3. Retry: AWS documents guarantees, not a policy (5.3, 6.1, 6.2). A missing topic or function is journaled and dropped, as CloudWatch "doesn't test or validate the actions" (5.3); Lambda's own asynchronous retries apply after acceptance. No invented retry schedule.
4. Policies: before delivery the access recorder checks the topic policy for `cloudwatch.amazonaws.com` and the function policy for `lambda.alarms.cloudwatch.amazonaws.com` and journals an `Advisory` when AWS would refuse, then delivers (ARCHITECTURE.md 16). Under `iam.enforce=enforce` the refused delivery is dropped after its one attempt, with no retry and no dead-letter queue: AWS states no outcome for a publish its topic refuses ("doesn't ... detect any ... Amazon SNS errors", 5.3) and does not guarantee a Lambda invocation it is not authorized for (6.1). AWS checks the topic policy: a topic granting only the account root received nothing and the alarm recorded a failed `Action` item (5.3; CWA-23, recorded 2026-11-03). Switch `cloudwatch.alarm.actions.denied` `dropped`. Whether a refused Lambda invocation is likewise dropped is unrecorded.
5. Per transition, in this order: state write, history item, then for each ARN of the new state's list in list order, a delivery when `ActionsEnabled`; then the state change event. `PutMetricAlarm` and `DeleteAlarms` emit the configuration change events.
6. Builders are pure functions with golden fixtures owned by the cloudwatch package: SNS subject and body (5.1, 5.2), Lambda event (6.1), the two EventBridge details (6.2), `StateReason` (4.1, 4.3) and `StateReasonData` (4.2). Golden bytes come from the recorded and documented examples above, with recorded values where the snapshot masks none.

### 12.5 Limits and validation texts

- `GetMetricStatistics`: `Statistics` 1 to 5, `ExtendedStatistics` 1 to 10, `Dimensions` up to 30, `Period` at least 1 (model, 1.2); the 1440 limit, `StartTime < EndTime`, statistics and period texts as recorded (9.3; CWA-19), in one constants table.
- `SetAlarmState`: `StateReason` up to 1023, `StateReasonData` up to 4000 and valid JSON (`InvalidFormat` 400 with an inferred message, 1.2).
- Generated `StateReason` and `StateReasonData` must respect 1023 and 4000 characters (CWA-10); `HistoryData` 4095 and `HistorySummary` 255 (model).
- Evaluation period limits already enforced at `PutMetricAlarm` (`checkEvaluationRange` in `validate.go`).

### 12.6 Terraform: apply and an empty second plan

Evaluation writes only state fields and never `Config`, `ActionsEnabled` or `AlarmConfigurationUpdatedTimestamp`, so the `DescribeAlarms` echo Terraform flattens (section 10) is unchanged by any number of evaluations, and a second plan stays empty. Keep echoing `TreatMissingData` exactly as sent and omit `EvaluateLowSampleCountPercentile` when unset. An alarm whose actions name a Terraform-made topic publishes on its first transition, which changes no Terraform-visible state.

### 12.7 What must never be faked

- No action reported as run when understudy cannot run it: EC2, Auto Scaling, Systems Manager and investigation ARNs are journaled as not run, with no success `Action` history item.
- No state computed for alarm kinds understudy cannot evaluate (percentiles, math, Metrics Insights, anomaly bands, PromQL, wall clock windows): they keep the state `PutMetricAlarm` or `SetAlarmState` gave them, and the coverage note says so. A `PutMetricAlarm` for one journals an `Advisory` naming why it will not be evaluated.
- No percentiles from statistic sets (`ExtendedStatistics` answers 501).
- No vended `AWS/*` metrics invented to make alarms on them work.
- No `StateReasonData` values that did not come from stored data, and no `ConfigurationUpdate` or `Action` history items in an invented shape.

## 13. Recording queue

Agents never run these. A maintainer runs them in the recording shell of [recording-kit.md](../recording-kit.md) sections 1 and 2 (`$P`, `$R`, `$A`, `$Q`, `$REC`, `rec`, `recurl`), with `jq` installed. Each case lists what it settles; record `notes.md` per case as the kit describes, one line per expectation (`matches`, `differs: <what>`). Costs use the [CloudWatch pricing page](https://aws.amazon.com/cloudwatch/pricing/): USD 0.10 per standard alarm metric per month and USD 0.30 per custom metric per month, both billed by the hour, so an alarm or metric that lives an hour costs well under a cent; SNS, SQS, Lambda, EventBridge and DynamoDB on demand stay in the free tier at these volumes. CWA-01 waits 45 minutes and CWA-24 six hours: start them first, then run the rest from another recording shell set up the same way, with 13.1's helpers defined there too. Several cases gained steps after the 2026-11-03 session, to settle what it left open; each says which. A rerun may run only a case's setup and its added steps, since `scripts/distill-parity` rewrites only the steps `$REC` holds. Start `$REC/<item>/notes.md` from the committed `testdata/parity/cloudwatch/<item>/notes.md` and add the new lines to it (recording-kit.md 6). In each shell, run 13.1, the cases and the 13.26 teardown in a kit shell of its own: 13.1 sets `FN` and `T`, which other queues set to other values (recording-kit.md 2).

### 13.1 Shared helpers

```sh
export NS="$P/cwa"

cwput() {  # cwput <metric> <seconds ago> <value> [extra datum JSON]: one datapoint in $NS
  aws cloudwatch put-metric-data --namespace "$NS" --metric-data \
    "[{\"MetricName\":\"$1\",\"Timestamp\":$(( $(date +%s) - $2 )),\"Value\":$3${4:+,$4}}]"
}

cwalarm() {  # cwalarm <name> <metric> <period> <N> <M or ''> <treat-missing-data> [more put-metric-alarm args]
  local name=$1 metric=$2 period=$3 n=$4 m=$5 treat=$6
  shift 6
  aws cloudwatch put-metric-alarm --alarm-name "$P-$name" --namespace "$NS" --metric-name "$metric" \
    --statistic Maximum --threshold 3 --comparison-operator GreaterThanThreshold \
    --period "$period" --evaluation-periods "$n" ${m:+--datapoints-to-alarm "$m"} \
    --treat-missing-data "$treat" "$@"
}

cwrec() {  # cwrec <item>/<step> <alarm>: the alarm as described, and its whole history, oldest first
  rec "$1-describe" aws cloudwatch describe-alarms --alarm-names "$P-$2"
  rec "$1-history" aws cloudwatch describe-alarm-history --alarm-name "$P-$2" --scan-by TimestampAscending
}

cwdel() {  # cwdel <alarm...>
  local a
  for a in "$@"; do aws cloudwatch delete-alarms --alarm-names "$P-$a"; done
}

cwdrain() {  # cwdrain <item>/<step> <queue name>: four receives, enough for 40 messages
  local i
  for i in 1 2 3 4; do
    rec "$1-$i" aws sqs receive-message --queue-url "$Q/$2" --max-number-of-messages 10 \
      --wait-time-seconds 10 --attribute-names All --message-system-attribute-names All
  done
}

cwqueue() {  # cwqueue <queue name> <source ARN> <service principal>: let the principal send from the source
  aws sqs set-queue-attributes --queue-url "$Q/$1" --attributes "$(jq -nc \
    --arg q "arn:aws:sqs:$R:$A:$1" --arg s "$2" --arg p "$3" \
    '{Policy: ({Version: "2012-10-17", Statement: [{Effect: "Allow", Principal: {Service: $p},
      Action: "sqs:SendMessage", Resource: $q, Condition: {ArnEquals: {"aws:SourceArn": $s}}}]} | tojson)}')"
}
```

Breaching values are 5 and non-breaching 1 against the threshold 3. Timestamps are Unix seconds, an input form the AWS CLI accepts ([cli-usage-parameters-types](https://docs.aws.amazon.com/cli/latest/userguide/cli-usage-parameters-types.html)), and `date +%s` works with GNU and BSD `date` alike.

The SNS fixture, for CWA-11, CWA-12, CWA-16 and CWA-17:

```sh
T=$(aws sns create-topic --name "$P-cwa" --query TopicArn --output text)
aws sqs create-queue --queue-name "$P-cwa" >/dev/null
cwqueue "$P-cwa" "$T" sns.amazonaws.com
aws sns subscribe --topic-arn "$T" --protocol sqs --notification-endpoint "arn:aws:sqs:$R:$A:$P-cwa" >/dev/null
```

The event fixture, for CWA-14 and CWA-15. It uses R03's `$P-role` (recording-kit.md 3, Tier 1). The 2026-11-03 run had to create that role (trust `lambda.amazonaws.com`, `AWSLambdaBasicExecutionRole`, a 15-second wait, deleted after), build `cwa.zip` with `python3 -I -m zipfile -c cwa.zip cwa.py` where no `zip` was installed, and wait 60 seconds after `put-targets` before creating an alarm: an alarm created 2 seconds after its rule had its first events lost (6.2).

```sh
printf 'import json\ndef handler(event, context):\n    print(json.dumps(event))\n    return {}\n' >cwa.py
zip -q cwa.zip cwa.py
FN=$(aws lambda create-function --function-name "$P-cwa" --runtime python3.13 --handler cwa.handler \
  --role "arn:aws:iam::$A:role/$P-role" --zip-file fileb://cwa.zip --query FunctionArn --output text)
aws lambda wait function-active-v2 --function-name "$P-cwa"
aws sqs create-queue --queue-name "$P-cwa-ev" >/dev/null
RULE=$(aws events put-rule --name "$P-cwa" --query RuleArn --output text --event-pattern \
  "{\"source\":[\"aws.cloudwatch\"],\"resources\":[{\"prefix\":\"arn:aws:cloudwatch:$R:$A:alarm:$P-ev\"}]}")
cwqueue "$P-cwa-ev" "$RULE" events.amazonaws.com
aws events put-targets --rule "$P-cwa" --targets "Id=q,Arn=arn:aws:sqs:$R:$A:$P-cwa-ev"
cwperm() {  # cwperm <alarm>: let the alarm invoke $P-cwa
  aws lambda add-permission --function-name "$P-cwa" --statement-id "$1" --action lambda:InvokeFunction \
    --principal lambda.alarms.cloudwatch.amazonaws.com --source-account "$A" \
    --source-arn "arn:aws:cloudwatch:$R:$A:alarm:$P-$1" >/dev/null
}
```

### 13.2 CWA-01: evaluation range

Settles `cloudwatch.alarm.evaluation-range` (3.4). One breaching point, then nothing: an N = 1 alarm under `missing` goes to ALARM and returns to INSUFFICIENT_DATA when the point leaves the range; the N = 3 alarm under `notBreaching` goes to OK when its oldest point leaves.

```sh
mkdir -p "$REC/CWA-01" && date -u +%FT%TZ >"$REC/CWA-01/start.time"
cwput r1 0 5;                                 cwalarm r1 r1 60 1 '' missing
cwput r3 150 5; cwput r3 90 5; cwput r3 30 5; cwalarm r3 r3 60 3 3 notBreaching
cwput r5 0 5;                                 cwalarm r5 r5 300 1 '' missing
sleep 2700
for a in r1 r3 r5; do cwrec "CWA-01/$a" "$a"; done
```

Expected: a point at time p stays in range while T0 - R x Period <= p, where T0 is an evaluation's `queryDate` truncated to the minute (r3: its oldest point's p). Recorded 2026-11-03: R = 6 for r1, 8 for r3 and 3 for r5 (3.4), not the 3, 5 and 3 the `n-plus-2` default predicted; the r1 transition recorded the singular `Insufficient Data:` text. Teardown: `cwdel r1 r3 r5`. Cost under USD 0.01; 50 minutes, mostly waiting.

### 13.3 CWA-02: sub-minute periods

Settles the high-resolution half of `cloudwatch.alarm.window-anchor` and the 10-second cadence phase (3.1), and the evaluation range of high-resolution alarms (3.4).

```sh
cwput hr 0 5 '"StorageResolution":1'
cwalarm hr hr 10 1 '' missing
cwput hr2 0 5 '"StorageResolution":1'      # added after 2026-11-03
cwalarm hr2 hr2 10 2 '' missing
cwput hr30 0 5 '"StorageResolution":1'     # added after 2026-11-03
cwalarm hr30 hr30 30 1 '' missing
sleep 600
cwrec CWA-02/hr hr; cwrec CWA-02/hr2 hr2; cwrec CWA-02/hr30 hr30
```

Expected: `queryDate` of both transitions on or just after a 10-second division of the minute; `evaluatedDatapoints` timestamps on 10-second divisions; the INSUFFICIENT_DATA transition about (R + 1) x 10 seconds after the point. Recorded 2026-11-03 for hr: the timestamps hold, but both `queryDate`s fell 0.6 seconds before a division, ALARM came 69 seconds after the create and INSUFFICIENT_DATA about 90 seconds after the point, reading a window two periods behind (3.1).

The added alarms settle what hr left open. hr2 (N = 2, M = 2) goes to ALARM once its one point has aged to the second evaluated bin (3.5, case C step 3), and to INSUFFICIENT_DATA when the point leaves the range: the time from the point to that transition, against hr's, gives R for N = 2 at 10 seconds, the 3.4 UNVERIFIED line for high-resolution alarms (N + 6, as hr's 7 for N = 1 would suggest, or another count). hr30 settles the same for a 30-second period, and whether its window lags two periods (60 seconds) as the 10-second window does or a fixed 20 seconds, the 3.1 bullet on sub-minute periods; its `queryDate`s also show whether a 30-second alarm is evaluated every 10 seconds, as the user guide says. Write down, per alarm, the point's timestamp, each transition's `queryDate` and its `evaluatedDatapoints`. Teardown: `cwdel hr hr2 hr30`. Cost under USD 0.01; 12 minutes.

### 13.4 CWA-03: first evaluation

Settles `cloudwatch.alarm.first-evaluation` (3.1).

```sh
cwput fe 30 5
cwalarm fe fe 60 1 '' missing
sleep 180
cwrec CWA-03/fe fe
```

Expected (default `next-boundary`): the first `StateUpdate` item's `Timestamp` is less than 60 seconds after `AlarmConfigurationUpdatedTimestamp`. AWS evaluates at an arbitrary second (3.1), so write down the latency and the `queryDate` second rather than expecting a minute boundary. Teardown: `cwdel fe`. Cost under USD 0.01; 4 minutes.

### 13.5 CWA-04: missing data, documented rows and beyond

Settles `cloudwatch.alarm.unaged-breach` and re-records the 40 cells of 3.6. Each pattern is placed in 300-second bins centred on the next evaluation, which keeps it in place for about two evaluations (3.2); `evaluatedDatapoints` in each first `StateUpdate` item shows the pattern as AWS saw it.

```sh
cwpattern() {  # cwpattern <metric> <5 marks, oldest first: 0 not breaching, X breaching, - missing>
  local i k
  for i in 0 1 2 3 4; do
    k=$((5 - i))
    case "${2:$i:1}" in
      0) cwput "$1" $(( (k - 1) * 300 + 150 )) 1 ;;
      X) cwput "$1" $(( (k - 1) * 300 + 150 )) 5 ;;
    esac
  done
}
cwrow() {  # cwrow <case> <M> <pattern>: the data, then one N = 3 alarm per treatment
  local t
  cwpattern "$1" "$3"
  for t in missing ignore breaching notBreaching; do cwalarm "$1-$t" "$1" 300 3 "$2" "$t"; done
}
cwrow t1r1 3 '0-X-X'; cwrow t1r2 3 '0----'; cwrow t1r3 3 '-----'; cwrow t1r4 3 '0XX-X'; cwrow t1r5 3 '--X--'
cwrow t1x1 3 '----X'; cwrow t1x2 3 'X----'
for c in t1x1 t1x2; do cwalarm "$c-missing-a" "$c" 300 3 3 missing; done
cwrow t2r1 2 '0-X-X'; cwrow t2r2 2 '00X0X'; cwrow t2r3 2 '0-X--'; cwrow t2r4 2 '----0'; cwrow t2r5 2 '---X-'
cwstart() {  # cwstart <state> <alarm...>: put the alarms in a known state, so that "retain" is visible
  local s=$1 a
  shift
  for a in "$@"; do aws cloudwatch set-alarm-state --alarm-name "$P-$a" --state-value "$s" --state-reason "cwa start $s"; done
}
cwstart OK t1r3-ignore t1r5-ignore t2r5-ignore t1x1-ignore t1x2-ignore t1x1-missing t1x2-missing
cwstart ALARM t1x1-missing-a t1x2-missing-a
sleep 240
for c in t1r1 t1r2 t1r3 t1r4 t1r5 t1x1 t1x2 t2r1 t2r2 t2r3 t2r4 t2r5; do
  for t in missing ignore breaching notBreaching; do cwrec "CWA-04/$c-$t" "$c-$t"; done
done
for a in t1x1-missing-a t1x2-missing-a; do cwrec "CWA-04/$a" "$a"; done
```

Every alarm starts in INSUFFICIENT_DATA, where "retain" and "go to INSUFFICIENT_DATA" look alike, so the cells whose answer may be "retain" are first set to a known state: OK for the `ignore` cells and the x1, x2 `missing` cells, and ALARM for a second copy of the x1, x2 `missing` cells. For an x cell under `missing`, the pair reads: both kept, retain; both OK, OK; both INSUFFICIENT_DATA, INSUFFICIENT_DATA; both ALARM, ALARM.

Expected: the tables of 3.6 (rows t1r1 to t2r5) and the x1, x2 rows (t1x1, t1x2). "retain" shows as no `StateUpdate` item after the alarm's last `SetAlarmState`, with `StateReason` still `Unchecked: Initial alarm creation` or `cwa start ...`. Recorded 2026-11-03: every table cell and x1 matched; x2 under `missing` went to ALARM from OK and kept ALARM (3.5). The bins are 300 seconds ending at each evaluation minute, and `evaluatedDatapoints` lists only the bins behind the decision (4.2), not all five. Discard a cell whose first transition's `evaluatedDatapoints` do not match its pattern; a later transition whose `evaluatedDatapoints` show the pattern shifted by a bin comes from the next pattern, not the cell's. Teardown: `for c in t1r1 t1r2 t1r3 t1r4 t1r5 t1x1 t1x2 t2r1 t2r2 t2r3 t2r4 t2r5; do for t in missing ignore breaching notBreaching; do cwdel "$c-$t"; done; done; cwdel t1x1-missing-a t1x2-missing-a`. Cost under USD 0.02 (50 alarms and 11 metrics for under an hour); 25 minutes.

### 13.6 CWA-05: an evaluation that keeps the state

Settles `cloudwatch.alarm.unchanged-state` (3.8).

```sh
cwalarm us us-none 60 1 '' missing
rec CWA-05/at-create aws cloudwatch describe-alarms --alarm-names "$P-us"
sleep 240
cwrec CWA-05/us us
```

Expected (default `no-update`): `StateReason` still `Unchecked: Initial alarm creation`, `StateUpdatedTimestamp` unchanged, no `StateUpdate` item. Teardown: `cwdel us`. Cost under USD 0.01; 5 minutes.

### 13.7 CWA-06: reason form with and without DatapointsToAlarm

Settles `cloudwatch.alarm.reason-format` (4.1).

```sh
cwput f1 30 5;                                cwalarm f-n1 f1 60 1 '' missing; cwalarm f-m1 f1 60 1 1 missing
cwput f2 90 5; cwput f2 30 6;                 cwalarm f-n2 f2 60 2 '' missing; cwalarm f-m2 f2 60 2 2 missing
cwput f3 150 5; cwput f3 90 1; cwput f3 30 6; cwalarm f-m2of3 f3 60 3 2 missing
cwput f4 150 5; cwput f4 90 5; cwput f4 30 6                       # added after 2026-11-03
cwalarm f-m2of3all f4 60 3 2 missing; cwalarm f-m1of3all f4 60 3 1 missing
sleep 180
for a in f-n1 f-m1 f-n2 f-m2 f-m2of3 f-m2of3all f-m1of3all; do cwrec "CWA-06/$a" "$a"; done
```

Expected (default): f-n1 `1 datapoint [5.0 (...)] was greater than the threshold (3.0).`; f-m1 `1 out of the last 1 datapoints [5.0 (...)] was greater than the threshold (3.0) (minimum 1 datapoint for OK -> ALARM transition).`; f-n2 `2 datapoints [6.0 (...), 5.0 (...)] were greater than the threshold (3.0).`; f-m2 and f-m2of3 the `out of the last` form, f-m2of3 `2 out of the last 3 datapoints [6.0 (...), 5.0 (...)] were ...` or with all three values listed (note which). Recorded 2026-11-03: as expected, f-m2of3 listing 6.0 and 5.0 (4.1).

The added f-m2of3all and f-m1of3all have three breaching points, more than their M of 2 and 1, the case no recording has (3.4, and the 4.1 UNVERIFIED line on more crossing datapoints than the minimum). Each settles whether the reason's count and listing hold all three crossing points (`3 out of the last 3 datapoints [6.0 (...), 5.0 (...), 5.0 (...)]`, understudy's choice) or only M of them, and whether `evaluatedDatapoints` stops at M, as 4.2 reads the decision bins. Teardown: `cwdel f-n1 f-m1 f-n2 f-m2 f-m2of3 f-m2of3all f-m1of3all`. Cost under USD 0.01; 5 minutes.

### 13.8 CWA-07: operator phrases

Settles the `OrEqual` phrases of `cloudwatch.alarm.reason-format` (4.1).

```sh
OPS="GreaterThanThreshold GreaterThanOrEqualToThreshold LessThanThreshold LessThanOrEqualToThreshold"
for op in $OPS; do
  for v in 1 3 5; do
    cwput "o-$op-$v" 30 "$v"
    aws cloudwatch put-metric-alarm --alarm-name "$P-o-$op-$v" --namespace "$NS" --metric-name "o-$op-$v" \
      --statistic Maximum --period 60 --evaluation-periods 1 --threshold 3 --comparison-operator "$op" \
      --treat-missing-data missing
  done
done
sleep 180
for op in $OPS; do for v in 1 3 5; do cwrec "CWA-07/$op-$v" "o-$op-$v"; done; done
```

Expected: each operator once breaching and once not; the `not` phrases for both `OrEqual` operators. Teardown: `for op in $OPS; do for v in 1 3 5; do cwdel "o-$op-$v"; done; done`. Cost under USD 0.01; 6 minutes.

### 13.9 CWA-08: no-data and filled texts

Settles the singular and `notBreaching` texts and the mixed real-and-filled text of `cloudwatch.alarm.reason-format` (4.1).

```sh
cwalarm nd-b1 nd-none 60 1 '' breaching
cwalarm nd-b2 nd-none 60 2 '' breaching
cwalarm nd-n2 nd-none 60 2 '' notBreaching
cwput nd-part 30 5; cwalarm nd-p3 nd-part 60 3 '' breaching
sleep 180
for a in nd-b1 nd-b2 nd-n2 nd-p3; do cwrec "CWA-08/$a" "$a"; done
```

Expected: nd-b2 repeats the recorded plural text (control); nd-b1 the singular; nd-n2 `... treated as [NonBreaching].` or another label; nd-p3 the mixed form. The singular `Insufficient Data:` text comes from CWA-01's r1. Teardown: `cwdel nd-b1 nd-b2 nd-n2 nd-p3`. Cost under USD 0.01; 5 minutes.

### 13.10 CWA-09: number formatting

Settles `cloudwatch.alarm.number-format` (4.3).

```sh
cwput big 30 12345678.9; cwalarm num-big big 60 1 '' missing
cwput small 30 0.000123
aws cloudwatch put-metric-alarm --alarm-name "$P-num-small" --namespace "$NS" --metric-name small --statistic Maximum \
  --period 60 --evaluation-periods 1 --threshold 0.0005 --comparison-operator LessThanThreshold --treat-missing-data missing
cwput avg 30 0.1; cwput avg 30 0.2
aws cloudwatch put-metric-alarm --alarm-name "$P-num-avg" --namespace "$NS" --metric-name avg --statistic Average \
  --period 60 --evaluation-periods 1 --threshold 0.1 --comparison-operator GreaterThanThreshold --treat-missing-data missing
sleep 180
for a in num-big num-small num-avg; do cwrec "CWA-09/$a" "$a"; done
```

Expected (default `java-double`): `1.23456789E7`, `1.23E-4` and threshold `5.0E-4`, and `0.15000000000000002`, in both `StateReason` and `StateReasonData`. Teardown: `cwdel num-big num-small num-avg`. Cost under USD 0.01; 5 minutes.

### 13.11 CWA-10: large N

Settles the overflow of 4.2 and which bins `evaluatedDatapoints` lists.

```sh
now=$(date +%s)
d=$(for i in $(seq 1 100); do printf '{"MetricName":"wide","Timestamp":%d,"Value":5},' $((now - i * 60 + 30)); done)
aws cloudwatch put-metric-data --namespace "$NS" --metric-data "[${d%,}]"
cwput gap 30 5; cwput gap 150 5; cwput gap 270 5
cwalarm wide wide 60 100 100 missing
cwalarm gap3 gap 60 3 3 missing
# added after 2026-11-03: the overflow and long-form thresholds, both directions
d=$(for i in $(seq 1 100); do printf '{"MetricName":"low","Timestamp":%d,"Value":1},' $((now - i * 60 + 30)); done)
aws cloudwatch put-metric-data --namespace "$NS" --metric-data "[${d%,}]"
for n in 10 20 40 50 60; do cwalarm "wide$n" wide 60 "$n" "$n" missing; done
cwalarm wide40c wide 60 40 '' missing
cwalarm low40 low 60 40 1 missing; cwalarm low40c low 60 40 '' missing
sleep 180
for a in wide gap3 wide10 wide20 wide40 wide50 wide60 wide40c low40 low40c; do cwrec "CWA-10/$a" "$a"; done
```

Expected: for `wide`, the lengths of `StateReason` (at most 1023) and `StateReasonData` (at most 4000) and how they are cut; for `gap3` (points in bins 1, 3 and 5, case 2 of 3.5), whether `evaluatedDatapoints` lists three bins or five. Recorded 2026-11-03: `wide` wrote the long form (327 characters) and kept 45 entries of each list at 3753 characters (4.1, 4.2).

The added alarms settle what `wide` left open. Write down, for each, the length of `StateReason` and `StateReasonData` and the entry counts of `recentDatapoints` and `evaluatedDatapoints`:

- wide50 and wide60 settle the 4.2 UNVERIFIED line on why 45: 45 entries again for both means a fixed cap of 45; 50 and 60 entries within 4000 characters means the cut depends on N or on the length.
- wide10, wide20 and wide40 (M = N, all breaching) settle the 4.1 UNVERIFIED line on where the long form (`... datapoints were greater than the threshold (3.0). The most recent datapoints which crossed the threshold: [...]`) starts: the first N that writes it. An inline listing runs about 25 characters per datapoint, so wide40's would pass 1023 characters and wide10's would not; a long form at wide10 or wide20 means the threshold is a count, not understudy's 1023 characters.
- wide40c is wide40 without `DatapointsToAlarm`: the count form's long form, which no recording shows.
- low40 (N = 40, M = 1, every point 1.0) goes to OK, which takes N - M + 1 = 40 non-breaching points: the OK direction's long form (`were not greater than`), and its `(minimum 40 datapoints for ALARM -> OK transition)` count. low40c, without `DatapointsToAlarm` (so M = N = 40), shows the count form's OK reason over 40 non-breaching points: whether it lists them all, inline or in the long form, or only the one the decision needs.

Teardown: `cwdel wide gap3 wide10 wide20 wide40 wide50 wide60 wide40c low40 low40c`. Cost under USD 0.01 (10 alarms and 3 metrics for minutes); 6 minutes.

### 13.12 CWA-11: SNS body fields

Settles `cloudwatch.alarm.sns.unset-fields` (5.1). Needs the SNS fixture.

```sh
aws cloudwatch put-metric-alarm --alarm-name "$P-sns-min" --namespace "$NS" --metric-name sns-none \
  --statistic Maximum --period 60 --evaluation-periods 1 --threshold 3 --comparison-operator GreaterThanThreshold \
  --alarm-actions "$T" --insufficient-data-actions "$T"
aws cloudwatch put-metric-alarm --alarm-name "$P-sns-full" --alarm-description 'full "quoted" description' \
  --namespace "$NS" --metric-name sns-none --dimensions Name=b,Value=2 Name=a,Value=1 --statistic Maximum \
  --unit Seconds --period 60 --evaluation-periods 2 --datapoints-to-alarm 1 --threshold 3 \
  --comparison-operator GreaterThanThreshold --treat-missing-data ignore --alarm-actions "$T" --ok-actions "$T"
aws cloudwatch put-metric-alarm --alarm-name "$P-sns-p90" --namespace "$NS" --metric-name sns-none \
  --extended-statistic p90 --evaluate-low-sample-count-percentile ignore --period 60 --evaluation-periods 1 \
  --threshold 3 --comparison-operator GreaterThanThreshold --treat-missing-data ignore --alarm-actions "$T"
for a in sns-min sns-full sns-p90; do
  aws cloudwatch set-alarm-state --alarm-name "$P-$a" --state-value ALARM --state-reason "cwa $a"
done
aws cloudwatch set-alarm-state --alarm-name "$P-sns-full" --state-value OK --state-reason "cwa ok"
sleep 120
cwdrain CWA-11/receive "$P-cwa"
```

Expected: the bytes of each `Message` (key order, `AlarmDescription` null for sns-min, `TreatMissingData` for sns-min, `DatapointsToAlarm` for sns-full, `Trigger` of sns-p90, `Dimensions` order and the key order inside each entry, `AlarmConfigurationUpdatedTimestamp` format, `ActionExecutedAfterMuteWindow` position); sns-min's revert to INSUFFICIENT_DATA after its next evaluation (2.3), with its text and the `INSUFFICIENT_DATA:` subject; the `OK:` subject. Teardown: `cwdel sns-min sns-full sns-p90`. Cost under USD 0.01; 6 minutes.

### 13.13 CWA-12: subject truncation and region name

Settles `cloudwatch.alarm.sns.subject` (5.2). Needs the SNS fixture.

```sh
LONG="$P-$(printf 'n%.0s' $(seq 1 150))"
aws cloudwatch put-metric-alarm --alarm-name "$LONG" --namespace "$NS" --metric-name sns-none --statistic Maximum \
  --period 60 --evaluation-periods 1 --threshold 3 --comparison-operator GreaterThanThreshold \
  --treat-missing-data ignore --alarm-actions "$T"
aws cloudwatch set-alarm-state --alarm-name "$LONG" --state-value ALARM --state-reason long
sleep 30
cwdrain CWA-12/receive "$P-cwa"
```

Expected: the envelope `Subject` (its length, where the name is cut, the `...`) and `Region` for `$R`. Teardown: `aws cloudwatch delete-alarms --alarm-names "$LONG"`. Cost under USD 0.01; 3 minutes.

### 13.14 CWA-13: FIFO topic

Settles `cloudwatch.alarm.sns.fifo-group` (5.3).

```sh
TF=$(aws sns create-topic --name "$P-cwa.fifo" --attributes FifoTopic=true --query TopicArn --output text)
aws sqs create-queue --queue-name "$P-cwa.fifo" --attributes FifoQueue=true >/dev/null
cwqueue "$P-cwa.fifo" "$TF" sns.amazonaws.com
aws sns subscribe --topic-arn "$TF" --protocol sqs --notification-endpoint "arn:aws:sqs:$R:$A:$P-cwa.fifo" >/dev/null
cwalarm fifo fifo-none 60 1 '' ignore --alarm-actions "$TF"
aws cloudwatch set-alarm-state --alarm-name "$P-fifo" --state-value ALARM --state-reason fifo
sleep 30
cwdrain CWA-13/receive "$P-cwa.fifo"
rec CWA-13/history aws cloudwatch describe-alarm-history --alarm-name "$P-fifo"
for h in md5 sha1 sha256; do
  printf %s "arn:aws:cloudwatch:$R:$A:alarm:$P-fifo" | openssl dgst "-$h" >>"$REC/CWA-13/arn-digests.txt"
done
```

Expected: whether the notification arrives at all on a topic without content-based deduplication; if it does, `MessageGroupId` and `MessageDeduplicationId` against the digests. Teardown: `for s in $(aws sns list-subscriptions-by-topic --topic-arn "$TF" --query 'Subscriptions[].SubscriptionArn' --output text); do aws sns unsubscribe --subscription-arn "$s"; done; aws sns delete-topic --topic-arn "$TF"; aws sqs delete-queue --queue-url "$Q/$P-cwa.fifo"; cwdel fifo`. Cost under USD 0.01; 4 minutes.

### 13.15 CWA-14: events while actions are disabled

Settles `cloudwatch.alarm.events.actions-disabled` (6.2). Needs the event fixture.

```sh
cwalarm ev-off ev-none 60 1 '' ignore --alarm-actions "$FN"
cwperm ev-off
aws cloudwatch disable-alarm-actions --alarm-names "$P-ev-off"
aws cloudwatch set-alarm-state --alarm-name "$P-ev-off" --state-value ALARM --state-reason "actions disabled"
sleep 60
cwdrain CWA-14/events "$P-cwa-ev"
rec CWA-14/lambda-logs aws logs filter-log-events --log-group-name "/aws/lambda/$P-cwa" --filter-pattern '"ev-off"'
# added after 2026-11-03: EnableAlarmActions
aws cloudwatch enable-alarm-actions --alarm-names "$P-ev-off"
sleep 60
cwdrain CWA-14/events-after-enable "$P-cwa-ev"
cwrec CWA-14/ev-off ev-off
```

Expected (default `events-sent`): a create configuration event and a state change event for ev-off; no Lambda invocation. Recorded 2026-11-03: as expected, and `DisableAlarmActions` wrote a `ConfigurationUpdate` item and sent an `update` configuration change event (6.2, 8.3).

The added steps settle `EnableAlarmActions`, which 6.2 infers to match `DisableAlarmActions`: the `events-after-enable` drain should hold one `"operation":"update"` configuration change event with `actionsEnabled` true against `previousConfiguration` false and a new configuration `timestamp`, and no state change event; the history (`ev-off-history`) should hold a second `ConfigurationUpdate` item, `Alarm "<name>" updated`, whose `originalUpdatedFields` carry `actionsEnabled` false and the old `alarmConfigurationUpdatedTimestamp` (8.3). Enabling actions changes no state, so no Lambda invocation follows. Teardown: `cwdel ev-off`. Cost under USD 0.01; 6 minutes.

### 13.16 CWA-15: event and Lambda payload shapes

Settles `cloudwatch.alarm.events.metric-id`, the event field sets (6.1, 6.2) and the event half of `cloudwatch.alarm.number-format` (4.3). Needs the event fixture.

```sh
shape() {  # shape <threshold> [more args]: create or update $P-ev-shape
  local th=$1
  shift
  aws cloudwatch put-metric-alarm --alarm-name "$P-ev-shape" --namespace "$NS" --metric-name ev-none \
    --statistic Maximum --period 60 --evaluation-periods 2 --datapoints-to-alarm 1 --threshold "$th" \
    --comparison-operator GreaterThanThreshold --treat-missing-data ignore --alarm-actions "$FN" --ok-actions "$FN" "$@"
}
t15=$(( $(date +%s) * 1000 ))
shape 3
cwperm ev-shape
aws cloudwatch set-alarm-state --alarm-name "$P-ev-shape" --state-value ALARM --state-reason one
aws cloudwatch set-alarm-state --alarm-name "$P-ev-shape" --state-value OK --state-reason two --state-reason-data '{"k":"v"}'
shape 4 --alarm-description described
aws cloudwatch set-alarm-state --alarm-name "$P-ev-shape" --state-value ALARM --state-reason three
aws cloudwatch delete-alarms --alarm-names "$P-ev-shape"
sleep 180
cwdrain CWA-15/events "$P-cwa-ev"
rec CWA-15/lambda-logs aws logs filter-log-events --log-group-name "/aws/lambda/$P-cwa" --start-time "$t15"
rec CWA-15/actions aws cloudwatch describe-alarm-history --alarm-name "$P-ev-shape" \
  --history-item-type Action --scan-by TimestampAscending
```

Expected: create, update and delete configuration events with their `configuration` fields (`datapointsToAlarm` present or not, and `threshold` printed as `3` or `3.0`, 4.3); three state change events and three Lambda events; `configuration.description` absent or null before the update; `metrics[0].id` equal or not across the first two state changes, and across the update. Recorded 2026-11-03: the events held what 6.2 records, but the log read found nothing, so the Lambda payload of 6.1 stays third-party-recorded.

Re-run after 2026-11-03 for the Lambda payload (6.1). The first run read the log group with `--filter-pattern '"ev-shape"'` 90 seconds after the last change and found no line; the read now waits 180 seconds and takes every line logged since `t15`, with no pattern to miss on, and `CWA-15/actions` (the history outlives the alarm, 8.1) tells the causes apart: `Successfully executed action <function ARN>` items with an empty log read mean the lines had not arrived (read again), `Failed to execute action` items carry CloudWatch's error (the permission or the function), and no `Action` item means no action ran. Each `print` line of the function is one event's JSON: settle against 6.1 its keys and their order, `region` as the code, the `time` format, `state.reasonData` on the second change (sent `{"k":"v"}`), and `configuration` against the state change event's order, which understudy uses (6.2, inferred). Teardown: nothing (the alarm is deleted in the script). Cost under USD 0.01; 7 minutes.

### 13.17 CWA-16: ConfigurationUpdate and Action history items

Settles the history items of 8.3 and the `DescribeAlarmHistory` coverage note. Needs the SNS fixture.

```sh
cwalarm hist-miss hist-none 60 1 '' ignore --alarm-actions "arn:aws:sns:$R:$A:$P-missing"
aws cloudwatch set-alarm-state --alarm-name "$P-hist-miss" --state-value ALARM --state-reason "missing topic"
cwalarm hist-ok hist-none 60 1 '' ignore --alarm-actions "$T"
aws cloudwatch set-alarm-state --alarm-name "$P-hist-ok" --state-value ALARM --state-reason delivered
cwalarm hist-ok hist-none 60 1 '' breaching --alarm-actions "$T"
sleep 60
for a in hist-miss hist-ok; do
  rec "CWA-16/$a" aws cloudwatch describe-alarm-history --alarm-name "$P-$a" --scan-by TimestampAscending
done
cwdel hist-miss
rec CWA-16/hist-miss-after-delete aws cloudwatch describe-alarm-history --alarm-name "$P-hist-miss"
```

Expected: `ConfigurationUpdate` items for both creates and the update, `Action` items for the failed and the delivered publish, with their `HistorySummary` and `HistoryData`; hist-miss's history still listed after the delete (8.1). Teardown: `cwdel hist-ok`. Cost under USD 0.01; 3 minutes.

### 13.18 CWA-17: SetAlarmState to the current state

Settles `cloudwatch.alarm.set-same-state` (2.3). Needs the SNS fixture.

```sh
cwalarm same same-none 60 1 '' ignore --alarm-actions "$T"
aws cloudwatch set-alarm-state --alarm-name "$P-same" --state-value ALARM --state-reason first
rec CWA-17/01-after-first aws cloudwatch describe-alarms --alarm-names "$P-same"
sleep 5
aws cloudwatch set-alarm-state --alarm-name "$P-same" --state-value ALARM --state-reason second --state-reason-data '{"n":2}'
rec CWA-17/02-after-second aws cloudwatch describe-alarms --alarm-names "$P-same"
sleep 30
cwrec CWA-17/same same
cwdrain CWA-17/receive "$P-cwa"
```

Expected (default `reason-and-data-only`): `StateReason` `second` and `StateReasonData` `{"n":2}` after the second call, both timestamps unchanged, one `StateUpdate` item, one notification (the page fixes this last). Recorded 2026-11-03: `StateReason` stayed `first` with no `StateReasonData`; the rest held (2.3). Teardown: `cwdel same`. Cost under USD 0.01; 2 minutes.

### 13.19 CWA-18: history page size

Settles `cloudwatch.alarm.history.page-size` (8.1).

```sh
cwalarm page page-none 60 1 '' ignore
for i in $(seq 1 30); do
  aws cloudwatch set-alarm-state --alarm-name "$P-page" --state-value ALARM --state-reason "a$i"; sleep 1
  aws cloudwatch set-alarm-state --alarm-name "$P-page" --state-value OK --state-reason "o$i"; sleep 1
done
rec CWA-18/first-page aws cloudwatch describe-alarm-history --alarm-name "$P-page" \
  --history-item-type StateUpdate --no-paginate
```

Expected: the item count of the first page, whether `NextToken` is present, and newest first. Teardown: `cwdel page`. Cost under USD 0.01; 3 minutes.

### 13.20 CWA-19: GetMetricStatistics validation texts

Settles the texts of 9.3.

```sh
now=$(( $(date +%s) / 60 * 60 ))
cwput g 120 5
gms() {  # gms <step> <args...>: one GetMetricStatistics call on metric g
  local step=$1
  shift
  rec "CWA-19/$step" aws cloudwatch get-metric-statistics --namespace "$NS" --metric-name g "$@"
}
gms 01-both    --start-time $((now - 900)) --end-time "$now" --period 60 --statistics Maximum --extended-statistics p50
gms 02-neither --start-time $((now - 900)) --end-time "$now" --period 60
for p in 7 61 90; do
  gms "03-period-$p" --start-time $((now - 900)) --end-time "$now" --period "$p" --statistics Maximum
done
# added after 2026-11-03: both lists once the point is visible
sleep 120
gms 04-both-after-wait       --start-time $((now - 900)) --end-time "$now" --period 60 --statistics Maximum --extended-statistics p50
gms 05-statistics-after-wait --start-time $((now - 900)) --end-time "$now" --period 60 --statistics Maximum
```

Expected: code, status and message of each refusal; for 90 also whether it is refused or answers with no datapoints. Recorded 2026-11-03: both lists accepted, neither `InvalidParameterCombination`, 7 and 61 and 90 refused with the texts of 9.3.

The added steps settle the 9.3 UNVERIFIED line on both lists: 01-both ran about a second after the `PutMetricData`, so its empty answer may only mean the point was not visible yet. After two minutes, 05 is the control and should return the point (`Maximum` 5.0 at the bin 120 seconds back). Then 04 either returns that datapoint, with which keys (`Maximum` and an `ExtendedStatistics` map holding `p50`, or one of them), or answers no datapoints although the point is visible: AWS answers both lists with nothing. If 05 also returns nothing, the point is still not visible; wait and run both again.

Carry the "Replay (internal/parity)" section of `testdata/parity/cloudwatch/CWA-19/notes.md` into `$REC/CWA-19/notes.md` before distilling (recording-kit.md 6). The section also covers CWA-20's steps, which `gms` writes under `CWA-19`; update its step list for 04 and 05. Teardown: none. Cost under USD 0.01; 5 minutes.

### 13.21 CWA-20: the 1440 datapoint count

Settles `cloudwatch.get-metric-statistics.datapoint-limit` (9.3). Run right after CWA-19 in the same shell (it reuses `gms` and `now`).

```sh
gms 11-span-1441 --start-time $((now - 86460)) --end-time "$now" --period 60 --statistics Maximum
gms 12-span-1440 --start-time $((now - 86400)) --end-time "$now" --period 60 --statistics Maximum
gms 13-half-aligned-start --start-time $((now - 86400)) --end-time $((now + 30)) --period 60 --statistics Maximum
gms 14-half-unaligned-start --start-time $((now - 86430)) --end-time "$now" --period 60 --statistics Maximum
gms 15-under-unless-rounded --start-time $((now - 86369)) --end-time $((now + 30)) --period 60 --statistics Maximum
```

Spans: 13 is 86,430 s from an aligned start (1440.5 periods); 14 is 86,430 s from a start at :30, which rounds to 86,460 s; 15 is 86,399 s, which rounds to 86,430 s. Expected (default `ceil-unrounded`): 11 refused with `up to 1441 datapoints`, 12 accepted, 13 and 14 refused, 15 accepted. The other rules give distinct patterns for 13, 14, 15: floor of the unrounded span accepts all three; ceiling after rounding refuses all three; floor after rounding refuses only 14. Recorded 2026-11-03: all three accepted, the floor of the unrounded span (9.3). `gms` writes these captures under `CWA-19`. Teardown: none. Cost under USD 0.01; 2 minutes.

### 13.22 CWA-21: AWS/DynamoDB missing data

Settles `cloudwatch.alarm.dynamodb-missing-data` (3.7).

```sh
aws dynamodb create-table --table-name "$P-cwa" --attribute-definitions AttributeName=k,AttributeType=S \
  --key-schema AttributeName=k,KeyType=HASH --billing-mode PAY_PER_REQUEST >/dev/null
aws dynamodb wait table-exists --table-name "$P-cwa"
aws cloudwatch put-metric-alarm --alarm-name "$P-ddb" --namespace AWS/DynamoDB --metric-name SystemErrors \
  --dimensions Name=TableName,Value="$P-cwa" Name=Operation,Value=GetItem --statistic Sum --period 60 \
  --evaluation-periods 1 --threshold 1 --comparison-operator GreaterThanThreshold --treat-missing-data breaching
cwalarm ddb-control ddb-none 60 1 '' breaching
# added after 2026-11-03: no TreatMissingData, from a known OK state
aws cloudwatch put-metric-alarm --alarm-name "$P-ddb-default" --namespace AWS/DynamoDB --metric-name SystemErrors \
  --dimensions Name=TableName,Value="$P-cwa" Name=Operation,Value=GetItem --statistic Sum --period 60 \
  --evaluation-periods 1 --threshold 1 --comparison-operator GreaterThanThreshold
aws cloudwatch put-metric-alarm --alarm-name "$P-ddb-default-control" --namespace "$NS" --metric-name ddb-none \
  --statistic Sum --period 60 --evaluation-periods 1 --threshold 1 --comparison-operator GreaterThanThreshold
rec CWA-21/default-at-create aws cloudwatch describe-alarms --alarm-names "$P-ddb-default" "$P-ddb-default-control"
for a in ddb-default ddb-default-control; do
  aws cloudwatch set-alarm-state --alarm-name "$P-$a" --state-value OK --state-reason "cwa start OK"
done
sleep 300
cwrec CWA-21/ddb ddb; cwrec CWA-21/control ddb-control
cwrec CWA-21/default ddb-default; cwrec CWA-21/default-control ddb-default-control
```

`SystemErrors` is chosen because an idle table publishes no data for it, so the alarm sees only missing data. Expected: the control goes to ALARM; ddb stays INSUFFICIENT_DATA with `Unchecked: Initial alarm creation` (default `always-ignore`) or goes to ALARM (the user guide's override). Recorded 2026-11-03: both went to ALARM (3.7). If ddb goes to OK, DynamoDB published a zero and the case is void.

The added alarms settle the 3.7 line on an `AWS/DynamoDB` alarm sent no `TreatMissingData`. On all-missing data, `missing` computes INSUFFICIENT_DATA, the state an alarm is created in, so both start from OK, where the treatments differ: ddb-default stays OK with `cwa start OK` under the user guide's "default to ignore missing data", or goes to INSUFFICIENT_DATA under the API's default `missing`, which understudy applies (`cloudwatch.alarm.dynamodb-missing-data` `as-configured`). The control, on a custom namespace, goes to INSUFFICIENT_DATA (3.5, case B). `default-at-create` also shows whether `DescribeAlarms` lists a `TreatMissingData` for either alarm, and which. Teardown: `cwdel ddb ddb-control ddb-default ddb-default-control; aws dynamodb delete-table --table-name "$P-cwa" >/dev/null`. Cost under USD 0.01; 9 minutes.

### 13.23 CWA-22: end of warm-up

Settles `cloudwatch.alarm.warm-up-end` (2.4). Needs an AWS CLI whose `aws cloudwatch put-metric-alarm help` lists `--warm-up-configuration`.

```sh
aws cloudwatch put-metric-alarm --alarm-name "$P-warm" --namespace "$NS" --metric-name warm --statistic Maximum \
  --period 60 --evaluation-periods 3 --threshold 3 --comparison-operator GreaterThanThreshold \
  --treat-missing-data breaching \
  --warm-up-configuration WarmUpPeriodDurationInMinutes=30,OnlyStartEvaluatingAfterWarmUpPeriodEnds=false
rec CWA-22/00-created aws cloudwatch describe-alarms --alarm-names "$P-warm"
sleep 90
for i in $(seq -w 1 12); do
  cwput warm 0 5
  rec "CWA-22/$i-after-put" aws cloudwatch describe-alarms --alarm-names "$P-warm"
  sleep 30
done
cwrec CWA-22/warm warm
```

`breaching` makes the first evaluation after warm-up go to ALARM, so the transition time marks the end of warm-up; a put every 30 seconds fills every minute. Expected: no transition in the 90 seconds before the first put (warm-up holds a `breaching` alarm with no data); `EvaluationState` `IN_WARM_UP` in the captures until warm-up ends, then the ALARM transition about one minute after the first put under (b), or about three under (a), the default. Recorded 2026-11-03: (a), 2.4 minutes after the first put (2.4). Teardown: `cwdel warm`. Cost under USD 0.01; 9 minutes.

### 13.24 CWA-23: an action to a topic that does not allow CloudWatch

Settles `cloudwatch.alarm.actions.denied` (5.3, 12.4). A topic whose policy grants only the account root, so `cloudwatch.amazonaws.com` has no `SNS:Publish`, subscribed by a queue that accepts the topic.

```sh
TD=$(aws sns create-topic --name "$P-cwa-deny" --query TopicArn --output text)
aws sns set-topic-attributes --topic-arn "$TD" --attribute-name Policy --attribute-value "$(jq -nc --arg t "$TD" --arg a "$A" \
  '{Version: "2012-10-17", Statement: [{Effect: "Allow", Principal: {AWS: ("arn:aws:iam::" + $a + ":root")}, Action: "SNS:Publish", Resource: $t}]}')"
aws sqs create-queue --queue-name "$P-cwa-deny" >/dev/null
cwqueue "$P-cwa-deny" "$TD" sns.amazonaws.com
aws sns subscribe --topic-arn "$TD" --protocol sqs --notification-endpoint "arn:aws:sqs:$R:$A:$P-cwa-deny" >/dev/null
cwalarm deny deny-none 60 1 '' ignore --alarm-actions "$TD"
aws cloudwatch set-alarm-state --alarm-name "$P-deny" --state-value ALARM --state-reason denied
sleep 60
cwdrain CWA-23/receive "$P-cwa-deny"
rec CWA-23/history aws cloudwatch describe-alarm-history --alarm-name "$P-deny"
```

Expected (default `dropped`): no message in the queue, the alarm in ALARM, and at most an `Action` history item saying the publish failed (8.3). A message in the queue means CloudWatch's publish is not checked against the topic policy, and the enforce mode must then deliver. Recorded 2026-11-03: no message, ALARM, and a failed `Action` item (5.3). Teardown: `for s in $(aws sns list-subscriptions-by-topic --topic-arn "$TD" --query 'Subscriptions[].SubscriptionArn' --output text); do aws sns unsubscribe --subscription-arn "$s"; done; aws sns delete-topic --topic-arn "$TD"; aws sqs delete-queue --queue-url "$Q/$P-cwa-deny"; cwdel deny`. Cost under USD 0.01; 3 minutes.

### 13.25 CWA-24: evaluation range for 120, 600 and 3600 second periods

Settles the 3.4 UNVERIFIED line on R for periods other than 60 and 300 seconds (`cloudwatch.alarm.evaluation-range`, where understudy uses N + 2), as CWA-01 did for those two. Added after 2026-11-03.

```sh
mkdir -p "$REC/CWA-24" && date -u +%FT%TZ >"$REC/CWA-24/start.time"
cwput p120 0 5;  cwalarm p120 p120 120 1 '' missing
cwput p600 0 5;  cwalarm p600 p600 600 1 '' missing
cwput p3600 0 5; cwalarm p3600 p3600 3600 1 '' missing
sleep 21600
for a in p120 p600 p3600; do cwrec "CWA-24/$a" "$a"; done
```

Expected: as in CWA-01, each alarm goes to ALARM and returns to INSUFFICIENT_DATA when its one point leaves the range; a point at time p stays in range while T0 - R x Period <= p, T0 being the `queryDate` of an evaluation truncated to the minute, so the last evaluation that still lists the point and the INSUFFICIENT_DATA transition after it give R per period. N + 2 (3) predicts the return after about 6, 30 and 180 minutes; N + 5 (6), the 60-second rule, about 12, 60 and 360. If p3600 is still in ALARM after the six hours, R is above 5 for 3600 seconds: record that, keep the alarm, and run `cwrec CWA-24/p3600 p3600` again later. Teardown: `cwdel p120 p600 p3600`. Cost under USD 0.01 (three alarms and three metrics for six hours); 6 hours, all waiting: start it with CWA-01 and leave its shell open.

### 13.26 Fixture teardown and sweep

```sh
# SNS fixture, after CWA-11, CWA-12, CWA-16 and CWA-17. Subscriptions first: they outlive their topics.
for s in $(aws sns list-subscriptions-by-topic --topic-arn "$T" --query 'Subscriptions[].SubscriptionArn' --output text); do
  aws sns unsubscribe --subscription-arn "$s"
done
aws sns delete-topic --topic-arn "$T"
aws sqs delete-queue --queue-url "$Q/$P-cwa"

# Event fixture, after CWA-14 and CWA-15.
aws events remove-targets --rule "$P-cwa" --ids q
aws events delete-rule --name "$P-cwa"
aws sqs delete-queue --queue-url "$Q/$P-cwa-ev"
aws lambda delete-function --function-name "$P-cwa"
aws logs delete-log-group --log-group-name "/aws/lambda/$P-cwa"
rm -f cwa.py cwa.zip

# Sweep: any alarm a case left behind.
for n in $(aws cloudwatch describe-alarms --alarm-name-prefix "$P-" --query 'MetricAlarms[].AlarmName' --output text); do
  aws cloudwatch delete-alarms --alarm-names "$n"
done
```

Custom metrics in `$P/cwa` cannot be deleted; they stop costing once no data arrives and expire on their own.
