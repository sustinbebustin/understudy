# CWA-21 (13.22)

Date: 2026-11-03 (run 03:13-03:21Z; CWA-14 and CWA-17 retries 16:36-16:41Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Doc: docs/research/cloudwatch-alarms.md
Fixes (all items): 13.1 helpers verbatim; items ran concurrently in separate background subshells (CWA-04, SNS chain CWA-23/11/12/16/17, event chain CWA-14/15, the rest in parallel). Each item ends with its doc teardown. SQS receives in cwdrain do not delete messages, so a later drain on the same queue shows earlier items messages again.

- control goes to ALARM: matches (03:16:25).
- ddb stays INSUFFICIENT_DATA with "Unchecked: Initial alarm creation" (default always-ignore): differs: went to ALARM at 03:16:34, "Threshold Crossed: no datapoints were received for 1 period and 1 missing datapoint was treated as [Breaching]." (the user guide's override).
