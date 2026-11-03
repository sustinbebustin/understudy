# CWA-22 (13.23)

Date: 2026-11-03 (run 03:13-03:21Z; CWA-14 and CWA-17 retries 16:36-16:41Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Doc: docs/research/cloudwatch-alarms.md
Fixes (all items): 13.1 helpers verbatim; items ran concurrently in separate background subshells (CWA-04, SNS chain CWA-23/11/12/16/17, event chain CWA-14/15, the rest in parallel). Each item ends with its doc teardown. SQS receives in cwdrain do not delete messages, so a later drain on the same queue shows earlier items messages again.

CLI help lists --warm-up-configuration.
- no transition in the 90 s before the first put: matches (StateReason "Alarm is in Warm-Up", INSUFFICIENT_DATA).
- EvaluationState IN_WARM_UP until warm-up ends: matches (captures 00-05; absent from 06 on).
- ALARM about one minute after the first put (b) or about three (a): (a), about 2.4 min: first put 03:15:55, ALARM 03:18:19 with 3 real datapoints (03:15, 03:16, 03:17); history summary "Alarm updated from INSUFFICIENT_DATA to ALARM. State now evaluated with the full set of metrics matching the query". Warm-up ended well before the 30 configured minutes.
