# CWA-20 (13.21)

Date: 2026-11-03 (run 03:13-03:21Z; CWA-14 and CWA-17 retries 16:36-16:41Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Doc: docs/research/cloudwatch-alarms.md
Fixes (all items): 13.1 helpers verbatim; items ran concurrently in separate background subshells (CWA-04, SNS chain CWA-23/11/12/16/17, event chain CWA-14/15, the rest in parallel). Each item ends with its doc teardown. SQS receives in cwdrain do not delete messages, so a later drain on the same queue shows earlier items messages again.

Captures are under CWA-19/11-* .. 15-* (the recipe's gms writes there).
- 11 refused with "up to 1441 datapoints": matches (400 InvalidParameterCombinationException "You have requested up to 1441 datapoints, which exceeds the limit of 1440. You may reduce the datapoints requested by increasing Period, or decreasing the time range.").
- 12 accepted: matches.
- 13 refused: differs: accepted (200).
- 14 refused: differs: accepted (200).
- 15 accepted: matches.
- Pattern: all three of 13/14/15 accepted, the floor-of-the-unrounded-span rule.
