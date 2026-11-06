# CWA-06 (13.7)

Date: 2026-11-03 (run 03:13-03:21Z; CWA-14 and CWA-17 retries 16:36-16:41Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Doc: docs/research/cloudwatch-alarms.md
Fixes (all items): 13.1 helpers verbatim; items ran concurrently in separate background subshells (CWA-04, SNS chain CWA-23/11/12/16/17, event chain CWA-14/15, the rest in parallel). Each item ends with its doc teardown. SQS receives in cwdrain do not delete messages, so a later drain on the same queue shows earlier items messages again.

- f-n1 "1 datapoint [5.0 (...)] was greater than the threshold (3.0).": matches.
- f-m1 "1 out of the last 1 datapoints [5.0 (...)] was greater than the threshold (3.0) (minimum 1 datapoint for OK -> ALARM transition).": matches.
- f-n2 "2 datapoints [6.0 (...), 5.0 (...)] were greater than the threshold (3.0).": matches.
- f-m2 out-of-the-last form: matches ("2 out of the last 2 datapoints [6.0 (...), 5.0 (...)] were greater than the threshold (3.0) (minimum 2 datapoints for OK -> ALARM transition).").
- f-m2of3 out-of-the-last form, which values listed: matches; only the two breaching values are listed: "2 out of the last 3 datapoints [6.0 (03/11/26 03:13:00), 5.0 (03/11/26 03:11:00)] were greater than the threshold (3.0) (minimum 2 datapoints for OK -> ALARM transition)." recentDatapoints [5.0,1.0,6.0]; evaluatedDatapoints only the two breaching bins.
