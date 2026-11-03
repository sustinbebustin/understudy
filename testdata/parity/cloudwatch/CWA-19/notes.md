# CWA-19 (13.20)

Date: 2026-11-03 (run 03:13-03:21Z; CWA-14 and CWA-17 retries 16:36-16:41Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Doc: docs/research/cloudwatch-alarms.md
Fixes (all items): 13.1 helpers verbatim; items ran concurrently in separate background subshells (CWA-04, SNS chain CWA-23/11/12/16/17, event chain CWA-14/15, the rest in parallel). Each item ends with its doc teardown. SQS receives in cwdrain do not delete messages, so a later drain on the same queue shows earlier items messages again.

- both Statistics and ExtendedStatistics: differs: accepted, 200, {"Datapoints": []} (doc candidate: InvalidParameterCombination refusal).
- neither: differs from the doc candidate (MissingParameter "Must specify either Statistics or ExtendedStatistics."): 400, __type com.amazon.coral.service#InvalidParameterCombinationException, "At least one of the parameters Statistics and ExtendedStatistics must be specified."
- period 7: matches InvalidParameterValue: 400, InvalidParameterValueException, "The parameter Period must be a value in the set [1, 5, 10, 20, 30]."
- period 61: matches InvalidParameterValue: 400, "The parameter Period must be a multiple of 60."
- period 90 refused or empty: refused, 400 InvalidParameterValueException "The parameter Period must be a multiple of 60."
