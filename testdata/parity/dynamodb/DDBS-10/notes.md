# DDBS-10: the wire form of ApproximateCreationDateTime (dynamodb-streams.md 12.11)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

No call; reads DDBS-09's raw bodies (`DDBS-09/records-*.http`).

| Field | Expected | Result |
|---|---|---|
| spelling (integral, `.0`, three decimals or exponent); understudy writes the codec's plain epoch-seconds number (11.3) | one spelling | differs from understudy: exponent form, whole seconds: `1.793723928E9`, `1.793723929E9` (as DynamoDB's own CreationDateTime and the GetRecords doc example) |

Session changes (`# session:`): the jq result goes to `$WORK/DDBS-10.txt` instead of the terminal, so no helper file lands in an item directory.
