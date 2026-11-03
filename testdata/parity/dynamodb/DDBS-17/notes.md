# DDBS-17: the stream of a deleted table (dynamodb-streams.md 12.18)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

| Step | Expected | Result |
|---|---|---|
| 02 DescribeStream after delete | `StreamStatus: DISABLED` with closed shards | matches: DISABLED, all 4 shards with `EndingSequenceNumber` |
| 03 ListStreams for the deleted table | lists the stream | matches: one stream listed |
| readall | returns the INSERT | matches: one INSERT (pk a) on one shard; GetShardIterator/GetRecords work on the deleted table's stream |

Session changes: none to these steps; the shared setup stops before the cases when the stream ARN is missing (`# session:`).
