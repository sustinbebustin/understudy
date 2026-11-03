# ESMS-02: BatchSize above 10 with window 0 on streams (lambda-stream-sources.md 13.3)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

| Step | Expected | Result |
|---|---|---|
| 01, 02, 04 Kinesis creates | accepted (`sqs-only`) or the SQS text | n/a (free-plan SCP: Kinesis denied; no stream) |
| 03 DynamoDB, BatchSize 11, window 0 | accepted means switch `batch-window-rule` = `sqs-only` | accepted: 202, `BatchSize 11`, `MaximumBatchingWindowInSeconds 0`, `State Creating` -> `sqs-only` for DynamoDB streams. Raw body also shows every unset member as JSON `null` (`FilterCriteria`, `ScalingConfig`, `Queues`, `StartingPositionTimestamp`, ...) and `DestinationConfig {"OnSuccess":null,"OnFailure":{"Destination":null}}`; 3.5's recorded shape (from SDK-parsed snapshots) omits them |
| 05-06 Kinesis update to 11 | - | n/a (free-plan SCP) |

Session changes (`# session:` in the queue script): helper files under `$WORK/ESMS-src` instead of `$REC/ESMS-src`; no Kinesis stream created or deleted (free-plan SCP), and `kput`, `kputs`, `kmap`, `ts` left undefined; `until_state`, `esm_rm` and the create-function retry give up after 5 minutes / 12 tries instead of looping forever; only the DynamoDB or SQS steps of this item ran, with the `mode` line the recipe sets before its Kinesis half where needed; item teardown runs at the end of the item.
