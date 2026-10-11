// Package cloudwatch implements Amazon CloudWatch metrics and alarms: metric
// alarms and their tags, stored as Terraform needs them, PutMetricData,
// GetMetricData over the stored points and ListMetrics. The handlers are
// written once and served through awsJson1_0, awsQuery and rpcv2Cbor, the
// protocols the registry lists for the service.
//
// Decisions the architecture and the research leave open:
//
//   - Alarms are stored, never evaluated: an alarm's state changes only
//     through SetAlarmState, and its actions never run. Its configuration is
//     the PutMetricAlarm request as sent, which DescribeAlarms echoes, with
//     the three action lists and Dimensions always present (AWS-recorded).
//   - DeleteAlarms, EnableAlarmActions and DisableAlarmActions skip names that
//     match no alarm, so deleting a missing alarm succeeds (gap-G1.md 4).
//     ListTagsForResource of an ARN that names no alarm is an empty list, as
//     AWS records for a deleted one; TagResource and UntagResource of such an
//     ARN are ResourceNotFoundException.
//   - A metric series is a namespace, a name and a dimension set, whatever
//     order a request lists the dimensions in. Its points are kept until
//     reset; they are not aged out by resolution as AWS's retention does.
//     PutMetricData's two-week and two-hour windows accept a timestamp valid
//     by the wall clock or the emulator clock, as Logs does.
//   - GetMetricData rounds StartTime down by its age on the emulator clock, as
//     AWS documents, and starts its periods there; EndTime is exclusive and
//     periods without data are left out. Without a Unit, data of several
//     units answers with the first unit by name and a MultipleUnits message,
//     and a default label adds the statistic and the period only when the
//     request's queries differ in them (both AWS-recorded). Its NextToken is
//     the number of values earlier pages returned. Metric math, Metrics
//     Insights and statistics other than the basic five answer 501.
//   - PutMetricData accepts a SampleCount or Counts of zero, which AWS does
//     not document refusing. A period whose combined SampleCount is zero has
//     no Average, so the Average result leaves it out rather than answer NaN
//     (inferred: no recording shows AWS's answer).
//   - Error texts AWS records are marked so in errors.go and validate.go; the
//     others follow the recorded templates and are marked inferred, so a
//     recording changes one place.
package cloudwatch
