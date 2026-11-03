# CWA-10 (13.11)

Date: 2026-11-03 (run 03:13-03:21Z; CWA-14 and CWA-17 retries 16:36-16:41Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Doc: docs/research/cloudwatch-alarms.md
Fixes (all items): 13.1 helpers verbatim; items ran concurrently in separate background subshells (CWA-04, SNS chain CWA-23/11/12/16/17, event chain CWA-14/15, the rest in parallel). Each item ends with its doc teardown. SQS receives in cwdrain do not delete messages, so a later drain on the same queue shows earlier items messages again.

- wide StateReason length (at most 1023) and how it is cut: matches (327 chars, not cut by length); form: "Threshold Crossed: 100 out of the last 100 datapoints were greater than the threshold (3.0). The most recent datapoints which crossed the threshold: [5 values] (minimum 100 datapoints for OK -> ALARM transition)."
- wide StateReasonData length (at most 4000) and how it is cut: matches (3753 chars); recentDatapoints and evaluatedDatapoints each hold 45 entries (03:13 back to 02:29), startDate 02:29, not 100.
- gap3 evaluatedDatapoints three bins or five: three (03:13, 03:11, 03:09); recentDatapoints [5.0,null,5.0,null,5.0].
