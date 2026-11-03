# DDBS-12: DeleteItem of an absent key (dynamodb-streams.md 12.13)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

| Field | Expected | Result |
|---|---|---|
| INSERT for marker-12; no REMOVE for never-written; switch `delete-absent-item` `no-record` | as stated | matches: INSERT marker-12 present, no REMOVE for never-written (records seen: INSERT/MODIFY/REMOVE a, INSERT k, INSERT marker-12) |

Session changes (`# session:`): the jq result goes to `$WORK/DDBS-12.txt` instead of the terminal, so no helper file lands in an item directory.
