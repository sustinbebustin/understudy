# ESMS-03: members of the other source type (lambda-stream-sources.md 13.4)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

Each refused: 400, `x-amzn-ErrorType: InvalidParameterValueException`, body `{"Type":"User","message":...}`. Doc 3.4 plausible: refusal with IPVE texts like the recorded SQS one; understudy today ignores these members silently on an SQS mapping (`createMapping`), so each refusal is a gap in understudy.

| Step | Result |
|---|---|
| 01 SQS + StartingPosition | refused: "StartingPosition is not valid for SQS event sources." |
| 02 SQS + ParallelizationFactor | refused: "Unsupported ParallelizationFactor parameter for given event source mapping type." |
| 03 SQS + BisectBatchOnFunctionError | refused: "Unsupported BisectBatchOnFunctionError parameter for given event source mapping type." |
| 04 SQS + MaximumRetryAttempts | refused: "Unsupported MaximumRetryAttempts parameter for given event source mapping type." |
| 05 SQS + MaximumRecordAgeInSeconds | refused: "Unsupported MaximumRecordAgeInSeconds parameter for given event source mapping type." |
| 06 SQS + TumblingWindowInSeconds | refused: "Unsupported TumblingWindowInSeconds parameter for given event source mapping type." |
| 07 SQS + DestinationConfig.OnFailure | refused: "Unsupported DestinationConfig parameter for given event source mapping type." (the recorded OnSuccess text) |
| 08-11 Kinesis + ScalingConfig, ProvisionedPollerConfig, OnSuccess, Queues | n/a (free-plan SCP: Kinesis denied; no stream) |

Session changes (`# session:` in the queue script): helper files under `$WORK/ESMS-src` instead of `$REC/ESMS-src`; no Kinesis stream created or deleted (free-plan SCP), and `kput`, `kputs`, `kmap`, `ts` left undefined; `until_state`, `esm_rm` and the create-function retry give up after 5 minutes / 12 tries instead of looping forever; only the DynamoDB or SQS steps of this item ran, with the `mode` line the recipe sets before its Kinesis half where needed; item teardown runs at the end of the item.
