# CWA-04 (13.5)

Date: 2026-11-03 (run 03:13-03:21Z; CWA-14 and CWA-17 retries 16:36-16:41Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Doc: docs/research/cloudwatch-alarms.md
Fixes (all items): 13.1 helpers verbatim; items ran concurrently in separate background subshells (CWA-04, SNS chain CWA-23/11/12/16/17, event chain CWA-14/15, the rest in parallel). Each item ends with its doc teardown. SQS receives in cwdrain do not delete messages, so a later drain on the same queue shows earlier items messages again.

Point bins: 300 s bins ending at the evaluation minute (e.g. 02:50 .. 03:10); evaluatedDatapoints list only the bins behind the decision, not all five. Later transitions at 03:17:35-03:17:47 (t1r2-missing, t1r2-breaching, t1x2-missing, t1x2-missing-a) show the pattern shifted and are discarded as the doc says.
- Table 1 row 1 (0-X-X; OK OK OK OK): matches.
- Table 1 row 2 (0----; OK OK OK OK): matches.
- Table 1 row 3 (-----; INSUFFICIENT_DATA retain ALARM OK): matches (missing kept "Unchecked: Initial alarm creation"; ignore kept "cwa start OK").
- Table 1 row 4 (0XX-X; ALARM x4): matches.
- Table 1 row 5 (--X--; ALARM retain ALARM OK): matches.
- Table 2 row 1 (0-X-X; ALARM x4): matches.
- Table 2 row 2 (00X0X; ALARM x4): matches.
- Table 2 row 3 (0-X--; OK OK ALARM OK): matches.
- Table 2 row 4 (----0; OK OK ALARM OK): matches.
- Table 2 row 5 (---X-; ALARM retain ALARM OK): matches.
- x1 (----X; retain retain ALARM OK): matches (missing: OK copy stayed OK and ALARM copy stayed ALARM, no evaluation StateUpdate).
- x2 (X----) missing, retain: differs: the copy started at OK went to ALARM at 03:15:35 with "Threshold Crossed: 1 out of the last 3 datapoints [5.0 (03/11/26 02:50:00)] was greater than the threshold (3.0) (minimum 3 datapoints for OK -> ALARM transition)." (evaluated bins 03:10=-, 03:05=-, 02:50=5.0); the copy started at ALARM had no StateUpdate (stayed ALARM). The pair reads ALARM.
- x2 ignore retain, breaching ALARM, notBreaching OK: matches.
