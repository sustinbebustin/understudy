# DDBS-15: TrimmedDataAccessException (dynamodb-streams.md 12.16)

- Date: 2026-11-04
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

Target: DDBS-09's first record, sequence number `12100000714580876880150` in shard `shardId-00000001793723721237-7672acf5`, one of the stream's 4 initial shards. The record was written 2026-11-03T16:38:48Z; the shard closed 2026-11-03T21:04:37Z, when its child was stamped (DDBS-14).

| Field | Expected | Result |
|---|---|---|
| try 1, 2026-11-04T17:47:58Z (25.2 h after the record) | 400 TrimmedDataAccessException with its text | 01 200 with an iterator; 02 TRIM_HORIZON GetRecords returns all 5 of the shard's records, DDBS-09's first record first, and no `NextShardIterator` (closed shard) |
| try 2, 18:48:02Z (26.2 h) | | the same |
| try 3, 19:48:07Z (27.2 h) | | the same |
| try 4, 20:48:11Z (28.2 h) | | the same |
| try 5, 21:48:15Z (29.2 h; the shard closed 24.7 h before) | | the same |
| try 6, 22:48:22Z (30.2 h; the shard closed 25.7 h before): 01 AT_SEQUENCE_NUMBER | | 400 `{"__type":"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException","message":"Requested resource not found: Shard does not exist"}`: the whole closed shard is gone, as DDBS-14's 6 h describe shows |
| try 6: 02 TRIM_HORIZON GetRecords | | 400 `com.amazon.coral.validate#ValidationException` "Invalid ShardIterator": a script artifact. The script's CLI GetShardIterator for TRIM_HORIZON failed on the missing shard and the request sent `{"ShardIterator":""}`; it says nothing about the shard |
| TrimmedDataAccessException | | never observed: no record was trimmed from a shard; AWS removed the closed shard whole instead |

`DDBS-15/` holds try 6; tries 1 to 5 are in `$REC/_work/DDBS-15-try-<n>`.

Session changes (`# session:`): run hourly from `late-ddbs.sh` until 01 answered other than 200. The recipe's `"${f%.out}.shard"` is DDBS-09's `-shard.txt` file.
