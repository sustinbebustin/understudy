# CWA-09 (13.10)

Date: 2026-11-03 (run 03:13-03:21Z; CWA-14 and CWA-17 retries 16:36-16:41Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Doc: docs/research/cloudwatch-alarms.md
Fixes (all items): 13.1 helpers verbatim; items ran concurrently in separate background subshells (CWA-04, SNS chain CWA-23/11/12/16/17, event chain CWA-14/15, the rest in parallel). Each item ends with its doc teardown. SQS receives in cwdrain do not delete messages, so a later drain on the same queue shows earlier items messages again.

- 1.23456789E7 in StateReason and StateReasonData: matches.
- 1.23E-4 and threshold 5.0E-4: matches (StateReason "(5.0E-4)", StateReasonData "threshold":5.0E-4).
- 0.15000000000000002: matches (StateReason, recentDatapoints and value).
