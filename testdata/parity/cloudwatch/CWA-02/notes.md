# CWA-02 (13.3)

Date: 2026-11-03 (run 03:13-03:21Z; CWA-14 and CWA-17 retries 16:36-16:41Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Doc: docs/research/cloudwatch-alarms.md
Fixes (all items): 13.1 helpers verbatim; items ran concurrently in separate background subshells (CWA-04, SNS chain CWA-23/11/12/16/17, event chain CWA-14/15, the rest in parallel). Each item ends with its doc teardown. SQS receives in cwdrain do not delete messages, so a later drain on the same queue shows earlier items messages again.

- queryDate of both transitions on or just after a 10-second division: differs: 03:15:09.443 (ALARM) and 03:15:29.444 (INSUFFICIENT_DATA), 9.4 s into a division, 0.6 s before the next one.
- evaluatedDatapoints timestamps on 10-second divisions: matches (03:13:50, then 03:15:00).
- INSUFFICIENT_DATA transition about (R + 1) x 10 s after the point: differs: point put about 03:13:59 (bin 03:13:50); ALARM came 03:15:09 (69 s after creation, evaluating the 79-s-old bin 03:13:50); INSUFFICIENT_DATA came 03:15:29, about 90 s after the point, reason "Insufficient Data: 1 datapoint was unknown." evaluating only bin 03:15:00.
