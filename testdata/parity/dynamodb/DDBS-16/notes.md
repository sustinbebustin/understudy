# DDBS-16: AT_TIMESTAMP edges (dynamodb-streams.md 12.17)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

| Step | Expected | Result |
|---|---|---|
| 01 future | ValidationException (text) | matches: 400 `com.amazon.coral.validate#ValidationException` "One or more parameter values were invalid: Timestamp parameter cannot be greater than the current timestamp" |
| 02 before the stream | an iterator | matches: 200 `ShardIterator` |
| 03 AT_TIMESTAMP without Timestamp | text or acceptance | 400 ValidationException "One or more parameter values were invalid: Valid timestamp must be provided when the iterator type is AT_TIMESTAMP" |
| 04 Timestamp with TRIM_HORIZON | text or acceptance | 400 ValidationException "One or more parameter values were invalid: Timestamp should not be passed for this iterator type" |

Session changes: none to these steps; the shared setup stops before the cases when the stream ARN is missing (`# session:`).
