// Package cloudwatch implements Amazon CloudWatch metrics and alarms: metric
// alarms and their tags, stored as Terraform needs them,
// DescribeAlarmsForMetric, PutMetricData, GetMetricData and
// GetMetricStatistics over the stored points and ListMetrics. The handlers are
// written once and served through awsJson1_0, awsQuery and rpcv2Cbor, the
// protocols the registry lists for the service.
//
// Decisions the architecture and the research leave open:
//
//   - An alarm on one Statistic with a sliding window is evaluated by its
//     own emulator-clock timer at each minute boundary (ten seconds for a
//     sub-minute period, an hour past a day of periods), never at
//     PutMetricAlarm, so clock advance runs one evaluation per boundary in
//     order (cloudwatch-alarms.md 3.1, 12.3). A snapshot keeps no timers:
//     Rearm arms each evaluable alarm's next one. Other alarm kinds are
//     stored with an Advisory and never evaluated (cloudwatch-alarms.md
//     12.7); their state changes only through SetAlarmState. A transition
//     writes the state, journals a StateChange, runs the new state's actions
//     when ActionsEnabled and appends a StateUpdate history item. A create,
//     update, delete, EnableAlarmActions or DisableAlarmActions appends a
//     ConfigurationUpdate item (cloudwatch-alarms.md 8.3). History lives in
//     its own store, outlives the alarm and is dropped after 30 days of
//     emulator time (cloudwatch-alarms.md 8.1).
//   - SNS and Lambda actions are outbox deliveries (CloudWatchToSNS,
//     CloudWatchToLambda) as cloudwatch.amazonaws.com and
//     lambda.alarms.cloudwatch.amazonaws.com, attempted once with no retry
//     schedule and no dead-letter queue: AWS documents guarantees, not a
//     policy (cloudwatch-alarms.md 12.4). EC2, Auto Scaling, Systems
//     Manager and investigation actions are journaled as not run. The
//     state change event, and the configuration change events of
//     PutMetricAlarm and DeleteAlarms, go to the default event bus through
//     EventBridge's PutServiceEventInternal after the write commits,
//     whatever ActionsEnabled is; a kernel without EventBridge skips them
//     (cloudwatch-alarms.md 6.2). A new alarm starts in INSUFFICIENT_DATA, a
//     PromQL alarm in OK (AWS-documented), both with the recorded initial
//     reason. Its configuration is the PutMetricAlarm request as sent, which
//     DescribeAlarms echoes, with the three action lists and Dimensions
//     always present (AWS-recorded).
//   - PutMetricAlarm and GetMetricData accept the extended statistics of
//     the grammar AWS's statistics definitions give (validExtendedStat).
//     The 10% to 90% bound the PutMetricAlarm page puts on alarm ranges is
//     not enforced: the definitions use TM(2%:98%), and refusing what AWS
//     may accept is the costlier error (inferred). An alarm's Metrics
//     array returns data from exactly one entry, an absent ReturnData
//     counting as true, except on an anomaly detection alarm, whose metric
//     and band both return data as the documented example does (inferred).
//   - PutMetricAlarm refuses DatapointsToAlarm greater than
//     EvaluationPeriods with InvalidParameterCombination, as M out of N
//     cannot count more data points than it evaluates (the alarm
//     evaluation documentation; the text is inferred). Without MaxRecords,
//     DescribeAlarms pages by 50, a size AWS does not document (inferred).
//   - Dimension names and values of PutMetricData, PutMetricAlarm,
//     GetMetricData and GetMetricStatistics hold only printable ASCII with a non-space character,
//     and a name does not start with a colon (the Dimension
//     documentation; the text is inferred).
//   - DeleteAlarms, EnableAlarmActions and DisableAlarmActions skip names
//     that match no alarm, so deleting a missing alarm succeeds
//     (wire-cbor.md 3.5). ListTagsForResource of an ARN that names no alarm
//     is an empty list, as AWS records for a deleted one; TagResource and
//     UntagResource of such an ARN are ResourceNotFoundException.
//   - A metric series is a namespace, a name and a dimension set, whatever
//     order a request lists the dimensions in. Its points are kept until
//     reset at the resolution they were put at, a regular metric's
//     timestamp rounded down to the minute and a high-resolution one's to
//     the second (AWS-documented); they are not aged out to coarser
//     resolutions as AWS's retention does, but ListMetrics leaves out a
//     series that has not reported data for two weeks of emulator time, as
//     AWS documents. PutMetricData's two-week and two-hour windows accept a
//     timestamp valid by the wall clock or the emulator clock, as Logs does.
//   - A datum with both Values and StatisticValues is accepted, as AWS
//     accepts it (AWS-recorded), and stores its StatisticValues alone
//     (inferred: no recording shows which form AWS keeps).
//   - GetMetricData rounds StartTime down by its age on the emulator clock,
//     or to a sub-minute period's division of the minute, as AWS
//     documents, and starts its periods there; a query whose period is
//     not a multiple of the minute, five minutes or the hour its StartTime's
//     age calls for reads no data, as AWS documents. EndTime is exclusive
//     and periods without data are left out. Without a Unit, data of several
//     units answers with the first unit by name and a MultipleUnits message,
//     and a default label adds the statistic and the period only when the
//     request's queries differ in them (both AWS-recorded). Its NextToken is
//     the number of values earlier pages returned. Metric math, Metrics
//     Insights and valid statistics other than the basic five answer 501.
//   - GetMetricStatistics reads periods as GetMetricData does, through the
//     same series read and bucketing that alarm evaluation uses, but answers
//     one datapoint per period and unit, never narrowing to one unit, sorted
//     by timestamp, then unit, an order AWS does not document. Its
//     1440-datapoint limit counts the ceiling of the span over the StartTime
//     as sent (inferred, cloudwatch-alarms.md 9.3). ExtendedStatistics
//     answer 501: percentiles need the samples, and points keep statistic
//     sets.
//   - PutMetricData accepts a SampleCount or Counts of zero, which AWS does
//     not document refusing. A period whose combined SampleCount is zero has
//     no Average, so the Average result or datapoint field leaves it out
//     rather than answer NaN
//     (inferred: no recording shows AWS's answer).
//   - Error texts AWS records are marked so in errors.go and validate.go; the
//     others follow the recorded templates and are marked inferred, so a
//     recording changes one place.
package cloudwatch
