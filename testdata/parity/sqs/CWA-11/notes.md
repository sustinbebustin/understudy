# CWA-11 (13.12)

Date: 2026-11-03 (run 03:13-03:21Z; CWA-14 and CWA-17 retries 16:36-16:41Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Doc: docs/research/cloudwatch-alarms.md
Fixes (all items): 13.1 helpers verbatim; items ran concurrently in separate background subshells (CWA-04, SNS chain CWA-23/11/12/16/17, event chain CWA-14/15, the rest in parallel). Each item ends with its doc teardown. SQS receives in cwdrain do not delete messages, so a later drain on the same queue shows earlier items messages again.

- Message key order: recorded: AlarmName, AlarmDescription, AWSAccountId, AlarmConfigurationUpdatedTimestamp, NewStateValue, NewStateReason, StateChangeTime, Region, AlarmArn, OldStateValue, ActionExecutedAfterMuteWindow, OKActions, AlarmActions, InsufficientDataActions, Trigger.
- AlarmDescription null for sns-min: matches.
- TreatMissingData for sns-min: recorded "" (empty string); EvaluateLowSampleCountPercentile "" too.
- DatapointsToAlarm for sns-full: present (1), after EvaluationPeriods.
- Trigger of sns-p90: StatisticType "ExtendedStatistic", ExtendedStatistic "p90", no Statistic key, EvaluateLowSampleCountPercentile "ignore". Standard alarms carry Statistic "MAXIMUM".
- Dimensions order and key order: sorted by name [{"value":"1","name":"a"},{"value":"2","name":"b"}] (value before name); input order was b, a.
- AlarmConfigurationUpdatedTimestamp format: "2026-11-03T03:15:36.145+0000".
- ActionExecutedAfterMuteWindow position: after OldStateValue, before OKActions; false.
- sns-min revert to INSUFFICIENT_DATA after next evaluation: matches (03:16:21.865, 43 s after the set; NewStateReason "Insufficient Data: 1 datapoint was unknown."; Subject "INSUFFICIENT_DATA: \"us1103-sns-min\" in US East (Ohio)").
- OK subject: matches ("OK: \"us1103-sns-full\" in US East (Ohio)").
