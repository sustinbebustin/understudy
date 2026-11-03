# CWA-13 (13.14)

Date: 2026-11-03 (run 03:13-03:21Z; CWA-14 and CWA-17 retries 16:36-16:41Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Doc: docs/research/cloudwatch-alarms.md
Fixes (all items): 13.1 helpers verbatim; items ran concurrently in separate background subshells (CWA-04, SNS chain CWA-23/11/12/16/17, event chain CWA-14/15, the rest in parallel). Each item ends with its doc teardown. SQS receives in cwdrain do not delete messages, so a later drain on the same queue shows earlier items messages again.

- notification arrives on a FIFO topic without content-based deduplication: matches, it arrives (history "Successfully executed action").
- MessageGroupId against digests: equals the SHA-256 hex of the alarm ARN (b2cb7014...4480).
- MessageDeduplicationId against digests: matches none; it is a UUID (1d201fd4-5b5e-482f-a55b-03df10d028ae).
