# DDBS-06: UpdateTable stream error texts (dynamodb-streams.md 12.7)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

All five: 400 `com.amazon.coral.validate#ValidationException`, member `message`.

| Step | Expected (5.1; Local in parentheses) | Result |
|---|---|---|
| 01 enable again | "Table already has an enabled stream" | differs: text has a suffix: "Table already has an enabled stream: TableName: <table>" |
| 02 enable with another view type | ValidationException (Local: "Table already has an enabled stream") | differs: "Table already has an enabled stream: TableName: <table>" |
| 03 enable without view type | ValidationException (Local: "Invalid StreamViewType: Stream ViewType must not be null or empty when enabling streams.") | differs: "One or more parameter values were invalid: If stream is being enabled then UpdateViewType is required" |
| 04 disable with no stream | ValidationException (Local: "Table has no stream to disable") | differs: "Table has no stream to disable: TableName: <table>" |
| 05 disable with view type | ValidationException (Local: "Invalid StreamViewType: Stream StreamEnabled was false but a StreamViewType was present.") | differs: "One or more parameter values were invalid: If stream is being disabled, then UpdateViewType must not be specified" |

Session changes: none to these steps; the shared setup stops before the cases when the stream ARN is missing (`# session:`).
