// Package cloudwatch implements Amazon CloudWatch metrics and alarms: metric
// alarms and their tags, stored as Terraform needs them, PutMetricData,
// GetMetricData over the stored points and ListMetrics. The handlers are
// written once and served through awsJson1_0, awsQuery and rpcv2Cbor, the
// protocols the registry lists for the service.
//
// Decisions the architecture and the research leave open:
//
//   - Alarms are stored, never evaluated: an alarm's state changes only
//     through SetAlarmState, and its actions never run. A new alarm starts
//     in INSUFFICIENT_DATA, a PromQL alarm in OK (AWS-documented), both
//     with the recorded initial reason. Its configuration is the
//     PutMetricAlarm request as sent, which DescribeAlarms echoes, with the
//     three action lists and Dimensions always present (AWS-recorded).
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
//   - Dimension names and values of PutMetricData, PutMetricAlarm and
//     GetMetricData hold only printable ASCII with a non-space character,
//     and a name does not start with a colon (the Dimension
//     documentation; the text is inferred).
//   - DeleteAlarms, EnableAlarmActions and DisableAlarmActions skip names that
//     match no alarm, so deleting a missing alarm succeeds (wire-cbor.md 3.5).
//     ListTagsForResource of an ARN that names no alarm is an empty list, as
//     AWS records for a deleted one; TagResource and UntagResource of such an
//     ARN are ResourceNotFoundException.
//   - A metric series is a namespace, a name and a dimension set, whatever
//     order a request lists the dimensions in. Its points are kept until
//     reset at the resolution they were put at, a regular metric's
//     timestamp rounded down to the minute and a high-resolution one's to
//     the second (AWS-documented); they are not aged out to coarser
//     resolutions as AWS's retention does, but ListMetrics leaves out a
//     series that has not reported data for two weeks of emulator time, as
//     AWS documents. PutMetricData's
//     two-week and two-hour windows accept a timestamp valid by the wall
//     clock or the emulator clock, as Logs does.
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
//   - PutMetricData accepts a SampleCount or Counts of zero, which AWS does
//     not document refusing. A period whose combined SampleCount is zero has
//     no Average, so the Average result leaves it out rather than answer NaN
//     (inferred: no recording shows AWS's answer).
//   - Error texts AWS records are marked so in errors.go and validate.go; the
//     others follow the recorded templates and are marked inferred, so a
//     recording changes one place.
package cloudwatch
