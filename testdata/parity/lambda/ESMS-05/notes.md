# ESMS-05: destination checks, FIFO, removal echo (lambda-stream-sources.md 13.6)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

| Step | Expected | Result |
|---|---|---|
| 01-03 missing queue, topic, bucket (Kinesis) | - | n/a (free-plan SCP: Kinesis denied) |
| 04 DynamoDB + FIFO queue OnFailure | refused or accepted (params page says "standard") | refused: 400 `InvalidParameterValueException`, `{"Type":"User","message":"FIFO-type Amazon SQS queues aren't supported as a destination."}` |
| 05-11 Lambda/kafka destinations, create, clear, empty-object (Kinesis) | - | n/a (free-plan SCP: Kinesis denied) |

Session changes (`# session:` in the queue script): helper files under `$WORK/ESMS-src` instead of `$REC/ESMS-src`; no Kinesis stream created or deleted (free-plan SCP), and `kput`, `kputs`, `kmap`, `ts` left undefined; `until_state`, `esm_rm` and the create-function retry give up after 5 minutes / 12 tries instead of looping forever; only the DynamoDB or SQS steps of this item ran, with the `mode` line the recipe sets before its Kinesis half where needed; item teardown runs at the end of the item.
