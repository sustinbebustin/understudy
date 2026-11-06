# DDBS-01: error identity of Streams errors (dynamodb-streams.md 12.2)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

| Step | Expected | Result |
|---|---|---|
| 01 DescribeStream, stream missing | 400; note `__type`, member, text, `x-amz-crc32` | matches: 400 `{"__type":"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException","message":"Requested resource not found: Stream: <stream arn> not found"}`, x-amz-crc32 present. Settles 3.3: DynamoDB namespace (as the `dev.understudy#errorNamespace` overlay assumes) and lower-case `message` (Local: `Message`) |
| 02 GetShardIterator, stream missing | 400 | matches: `com.amazonaws.dynamodb.v20120810#ResourceNotFoundException`, `message` "Requested resource not found: Stream does not exist", crc32 present |
| 03 GetShardIterator, shard missing | 400 | matches: same namespace, "Requested resource not found: Shard does not exist", crc32 present |
| 04 GetRecords, garbage iterator | 400 | matches: `{"__type":"com.amazon.coral.validate#ValidationException","message":"Invalid ShardIterator"}`, crc32 present (Local's text and namespace, lower-case member) |
| x-amz-crc32 on 400 | present (3.1) | matches: present on all four (Local omits it on errors) |

Session changes: none to these steps; the shared setup stops before the cases when the stream ARN is missing (`# session:`).
