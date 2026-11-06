# DDBS-07: the last page of a closed shard (dynamodb-streams.md 12.8)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

| Step | Expected | Result |
|---|---|---|
| 01 every shard has EndingSequenceNumber | yes | matches: `DISABLED`, 4 shards, all with `EndingSequenceNumber`; the three empty shards have Ending = Starting, the one with the record has Ending (22 digits) > Starting (21 digits) |
| last page per shard: NextShardIterator absent or JSON null | switch `closed-shard-next-iterator` default `absent` | matches `absent`: each shard answered in one page (`{"Records":[]}` or the one INSERT) with no `NextShardIterator` member in the raw body |
| 02 LATEST on a closed shard | iterator or error | 200 with a `ShardIterator` (`<stream arn>|1|...`) |

Session changes: none to these steps; the shared setup stops before the cases when the stream ARN is missing (`# session:`).
