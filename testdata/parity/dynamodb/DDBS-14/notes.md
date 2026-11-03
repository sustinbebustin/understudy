# DDBS-14: initial shard count and rollover (dynamodb-streams.md 12.15)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

| Field | Expected | Result |
|---|---|---|
| shard count at 0 h; rollover by hour 6 | (LocalStack: often 4) | deferred (6 h unattended, over the 2 h limit). Side observation from DDBS-05/07/09/17: every new on-demand stream this session opened with 4 shards |
