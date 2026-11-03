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

## Replay (internal/parity)

`scenario.json` replays these steps and CWA-20's (11-span-1441 to 15-under-unless-rounded, which the recipe's `gms` wrote here) in recorded order; internal/parity/baseline.json holds the outcomes.

- Inferred: the advance of 483h14m20s before 01-both, which moves the replay's clock from its epoch (2026-10-14) to the first step's X-Amz-Date, so the requests' StartTime and EndTime fall where the recording put them.
- Not made up: the setup's PutMetricData, the recipe's `cwput g 120 5`, at the 03:12:00 timestamp 12-span-1440 to 15-under-unless-rounded return.
- Replay: 01-both differs: AWS accepts both statistics lists (200, no datapoints; whether the point, put about a second earlier, was not yet visible or both lists answer nothing is UNVERIFIED); understudy answers 501, as for ExtendedStatistics alone, since it cannot compute percentiles.
- Replay: every other step matches on status, error code and message, and differs on the JSON wire form alone: AWS's error `__type` is `com.amazon.coral.service#<Exception>` where understudy writes `com.amazonaws.cloudwatch.v2010_08_01#<Exception>`, and its GetMetricStatistics body orders Datapoints before Label and Maximum before Timestamp and prints `5.0`, where understudy writes `5`.
