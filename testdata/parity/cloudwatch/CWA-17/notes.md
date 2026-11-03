# CWA-17 (13.18)

Date: 2026-11-03 (run 03:13-03:21Z; CWA-14 and CWA-17 retries 16:36-16:41Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Doc: docs/research/cloudwatch-alarms.md
Fixes (all items): 13.1 helpers verbatim; items ran concurrently in separate background subshells (CWA-04, SNS chain CWA-23/11/12/16/17, event chain CWA-14/15, the rest in parallel). Each item ends with its doc teardown. SQS receives in cwdrain do not delete messages, so a later drain on the same queue shows earlier items messages again.

Retry 16:41Z (captures retry-*, no actions) confirmed the second SetAlarmState returned HTTP 200 and changed nothing.
- StateReason "second" after the second call: differs: StateReason stays "first".
- StateReasonData {"n":2} after the second call: differs: no StateReasonData.
- both timestamps unchanged: matches.
- one StateUpdate item: matches.
- one notification: matches.
