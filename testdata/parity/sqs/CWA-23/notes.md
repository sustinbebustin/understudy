# CWA-23 (13.24)

Date: 2026-11-03 (run 03:13-03:21Z; CWA-14 and CWA-17 retries 16:36-16:41Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Doc: docs/research/cloudwatch-alarms.md
Fixes (all items): 13.1 helpers verbatim; items ran concurrently in separate background subshells (CWA-04, SNS chain CWA-23/11/12/16/17, event chain CWA-14/15, the rest in parallel). Each item ends with its doc teardown. SQS receives in cwdrain do not delete messages, so a later drain on the same queue shows earlier items messages again.

- no message in the queue: matches (four empty receives).
- alarm in ALARM: matches.
- at most an Action item saying the publish failed: matches ("Failed to execute action arn:aws:sns:us-east-2:<acct>:us1103-cwa-deny"; HistoryData error "CloudWatch Alarms is not authorized to perform: SNS:Publish on resource:arn:aws:sns:us-east-2:<acct>:us1103-cwa-deny", publishedMessage null).
