# DDBS-03: ListStreams for a table that does not exist (dynamodb-streams.md 12.4)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

| Step | Expected | Result |
|---|---|---|
| 01 ListStreams, table missing | `{"Streams":[]}` or 400 ResourceNotFoundException; switch `list-unknown-table` default `empty` | matches `empty`: 200 `{"Streams":[]}` |
| 02 ListStreams, table without stream | most likely `{"Streams":[]}` | matches: 200 `{"Streams":[]}` |

Session changes: none to these steps; the shared setup stops before the cases when the stream ARN is missing (`# session:`).
