# DDBS-11: SizeBytes for fractional numbers and nested types (dynamodb-streams.md 12.12)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

| Field | Expected | Result |
|---|---|---|
| SizeBytes | `["INSERT",40]` (Local) or `["INSERT",39]` (documented rule) | 40: agrees with Local, differs from the documented item-size rule (39); supports 7.4's base-100 digit-pair reading |

Session changes (`# session:`): the jq result goes to `$WORK/DDBS-11.txt` instead of the terminal, so no helper file lands in an item directory.
