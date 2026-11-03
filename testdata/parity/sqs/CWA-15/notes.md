# CWA-15 (13.16)

Date: 2026-11-03 (run 03:13-03:21Z; CWA-14 and CWA-17 retries 16:36-16:41Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Doc: docs/research/cloudwatch-alarms.md
Fixes (all items): 13.1 helpers verbatim; items ran concurrently in separate background subshells (CWA-04, SNS chain CWA-23/11/12/16/17, event chain CWA-14/15, the rest in parallel). Each item ends with its doc teardown. SQS receives in cwdrain do not delete messages, so a later drain on the same queue shows earlier items messages again.

Fixes: same event fixture fixes as CWA-14.
- create, update and delete configuration events with configuration fields: matches.
- datapointsToAlarm present or not: present (1) in create, update, delete configuration.
- threshold printed 3 or 3.0: 3.0 (and 4.0 after the update).
- three state change events and three Lambda events: matches (ALARM, OK with reasonData {"k":"v"}, ALARM; three log lines matching ev-shape).
- configuration.description absent or null before the update: absent (key missing in create and in the first two state changes and Lambda payloads); "described" after.
- metrics[0].id across the first two state changes: equal (c2d0cfb4-1516-36f2-5a1f-d2a9f14c031b).
- metrics[0].id across the update: changes (88d0cfb4-194c-e040-2fb6-2645907f54cb).
