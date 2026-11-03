# CWA-14 (13.15)

Date: 2026-11-03 (run 03:13-03:21Z; CWA-14 and CWA-17 retries 16:36-16:41Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Doc: docs/research/cloudwatch-alarms.md
Fixes (all items): 13.1 helpers verbatim; items ran concurrently in separate background subshells (CWA-04, SNS chain CWA-23/11/12/16/17, event chain CWA-14/15, the rest in parallel). Each item ends with its doc teardown. SQS receives in cwdrain do not delete messages, so a later drain on the same queue shows earlier items messages again.

Fixes: $P-role did not exist; created as a Lambda execution role (trust lambda.amazonaws.com, AWSLambdaBasicExecutionRole), 15 s wait, deleted at the end. No zip binary: cwa.zip built with "python3 -I -m zipfile -c cwa.zip cwa.py". create-function wrapped in a retry loop for role propagation (first try succeeded).
First run (03:14Z): no events at all for ev-off within 60 s + drain (the rule had been created 2 s before the alarm); the later ev-off delete event (03:16:05) did arrive. Treated as rule propagation, so the item was re-run at 16:37Z with a 60 s wait after the fixture (captures retry-*). Lambda log query in both runs: ResourceNotFoundException "The specified log group does not exist." (the function never ran).
- create configuration event for ev-off: matches (retry).
- state change event for ev-off: matches (retry). An extra configuration event, operation "update", came from DisableAlarmActions; the history also shows a ConfigurationUpdate "updated" item for it.
- no Lambda invocation: matches.
