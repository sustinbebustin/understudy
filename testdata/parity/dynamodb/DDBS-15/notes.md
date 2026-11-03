# DDBS-15: TrimmedDataAccessException (dynamodb-streams.md 12.16)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

| Field | Expected | Result |
|---|---|---|
| 01 AT_SEQUENCE_NUMBER at DDBS-09's first record | 400 TrimmedDataAccessException with its text | deferred (run at least 25 h after DDBS-09, i.e. after 2026-11-04T17:40Z; table `us1103-ddbs` kept) |
