# DDBS-02: validation texts and the GetRecords Limit error class (dynamodb-streams.md 12.3)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

All twelve are 400 `com.amazon.coral.validate#ValidationException` with member `message` and `x-amz-crc32`.

| Step | Expected (6.5; Local in parentheses) | Result |
|---|---|---|
| 01 DescribeStream `StreamArn` "short" | 400 (Local: not found, no length check) | differs: `ValidationException` "Invalid StreamArn" (not a Coral length text, not not-found) |
| 02 DescribeStream Limit 101 | 400 (Local: "One or more parameter values were invalid: Limit cannot be greater than the maximum: 100") | matches Local's text |
| 03 DescribeStream Limit 0 | 400 (Local: "1 validation error detected: Value '0' at 'limit' failed to satisfy constraint: Member must have value greater than or equal to 1") | differs: "1 validation error detected: Value at 'limit' failed to satisfy constraint: Member must have value greater than or equal to 1" (no `'0'`) |
| 04 ListStreams Limit 101 | 400 (Local: "... Limit cannot be greater than the maximum allowed size: 100") | matches Local's text |
| 05 AT_SEQUENCE_NUMBER without SequenceNumber | 400 (Local: "One or more parameter values were invalid: Invalid SequenceNumber") | differs: "One or more parameter values were invalid: SequenceNumber cannot be null" |
| 06 TRIM_HORIZON with SequenceNumber | 400 (Local: "... SequenceNumber should not be passed for this iterator type") | matches Local's text |
| 07 ShardIteratorType BOGUS | 400 (Local: 500 InternalFailure) | differs from Local: 400 "1 validation error detected: Value at 'shardIteratorType' failed to satisfy constraint: Member must satisfy enum value set: [AFTER_SEQUENCE_NUMBER, LATEST, AT_TIMESTAMP, AT_SEQUENCE_NUMBER, TRIM_HORIZON]" (no value quoted; enum includes AT_TIMESTAMP, in that order) |
| 08 ShardIteratorType missing | 400 (Local: 500) | differs from Local: 400 "1 validation error detected: Value at 'shardIteratorType' failed to satisfy constraint: Member must not be null" |
| 09 non-numeric SequenceNumber | 400 (Local: 500) | differs from Local: 400 "Invalid SequenceNumber for the shard" (no "One or more parameter values were invalid:" prefix) |
| 10 ShardId "x" | 400 (Local: "Shard does not exist") | differs from Local: 400 "1 validation error detected: Value at 'shardId' failed to satisfy constraint: Member must have length greater than or equal to 28" (AWS validates ShardId length before lookup; StreamArn length is not reported this way, see 01) |
| 11 GetRecords Limit 1001: class | `LimitExceededException` (doc) or `ValidationException` (Local) | matches Local: `ValidationException` "One or more parameter values were invalid: Limit should be less than or equal to 1000"; not LimitExceededException |
| 12 GetRecords Limit 0 | 400 | differs from Local: "1 validation error detected: Value at 'limit' failed to satisfy constraint: Member must have value greater than or equal to 1" (no `'0'`) |

Session changes: none to these steps; the shared setup stops before the cases when the stream ARN is missing (`# session:`).
