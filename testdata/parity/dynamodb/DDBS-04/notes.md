# DDBS-04: iterator expiry, reuse and NextShardIterator lifetime (dynamodb-streams.md 12.5)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

The shard read (`SH`, the stream's first shard) held no records, so every 200 is `Records: []` with a `NextShardIterator`. All answers carry `x-amz-crc32`.

| Step | Expected | Result |
|---|---|---|
| 01 use | 200 | matches |
| 02 reuse the same iterator (switch `iterator-reuse` default `allowed`) | 200 if reuse is allowed | matches `allowed`: 200 |
| 03 NextShardIterator used at 14 min | 200 | matches |
| 04 unused GetShardIterator iterator at 16 min | 400 ExpiredIteratorException with its text | 400, but the namespace differs from every other Streams error: `{"__type":"com.amazonaws.dynamodb.streams.v20120810#ExpiredIteratorException","message":"Shard iterator has expired"}`. DDBS-01's ResourceNotFoundException uses `com.amazonaws.dynamodb.v20120810#`, so the single `dev.understudy#errorNamespace` overlay (11.3) is wrong for this error. Local: `com.amazonaws.dynamodb.v20120810#ExpiredIteratorException` with `"Message":""` |
| 05 NextShardIterator from step 03 at 16 min | 200 if its lifetime starts at its own return; 400 if from the chain's first GetShardIterator | 200: each NextShardIterator gets its own 15 minutes (as 11.1 item 4 implements) |

Session changes: none to these steps; the shared setup stops before the cases when the stream ARN is missing (`# session:`).
