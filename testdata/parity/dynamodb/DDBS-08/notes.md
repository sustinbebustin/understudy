# DDBS-08: ListStreams order, disabled streams, paging (dynamodb-streams.md 12.9)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

| Step | Expected | Result |
|---|---|---|
| 02 list both streams, newest first; switch `list-order` `newest-first` | disabled DDBS-05 stream listed; newest first | matches: [16:38:26.296 (new), 16:36:05.068 (disabled)]; no `LastEvaluatedStreamArn` |
| 03 Limit 1 | how `LastEvaluatedStreamArn` pages (Local: the older stream first) | the newest stream, `LastEvaluatedStreamArn` = that stream (differs from Local's older-first page) |
| 04 page 2 | - | the older stream, and `LastEvaluatedStreamArn` = the older stream again, so the last full page still carries a non-empty `LastEvaluatedStreamArn` (as the doc allows) |

Session changes: none to these steps; the shared setup stops before the cases when the stream ARN is missing (`# session:`).
