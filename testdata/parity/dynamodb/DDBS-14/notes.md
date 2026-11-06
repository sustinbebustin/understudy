# DDBS-14: initial shard count and rollover (dynamodb-streams.md 12.15)

- Date: 2026-11-04 (stream created 2026-11-03T16:35:17Z)
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

| Field | Expected | Result |
|---|---|---|
| shard count at creation | (LocalStack: often 4) | 4: the stream's 4 parentless shards carry ids stamped 2026-11-03T16:35:20Z to 16:35:21Z (the 20 digits of `shardId-<20 digits>-<8 hex>` are the creation instant in epoch milliseconds, zero-padded: `00000001793723721087`). Side observation from DDBS-05/07/09/17: every new on-demand stream this session opened with 4 shards |
| shard count at 0 h (2026-11-04T16:32:05Z, the stream about 24 h old) and hourly to 6 h | | 25, 26, 28, 28, 29, 30, 27 (`01-describe-0h`, `02-describe-{1..6}h`); exactly 4 shards are open in every describe |
| rollover by hour 6 | whether shards gain `EndingSequenceNumber` and children | yes: 4 lineages, each a chain of single children (`ParentShardId`); a closed shard has `EndingSequenceNumber`. A child's id is stamped 3.5 to 4.5 h after its parent's (27 intervals, mean 4.1 h), with or without writes (one `put-item` per hour from 1 h) |
| closed shards kept | | the 4 initial shards (closed 2026-11-03T20:12Z to 21:04Z, when their children were stamped) are listed at 5 h (2026-11-04T21:33Z) and gone at 6 h (22:33Z): removed whole 24.5 to 26.3 h after closing, 29 to 30 h after creation. Their children, closed from 23:55Z on Nov 3, are still listed at 6 h |
| StreamStatus | ENABLED | ENABLED in every describe |

Session changes (`# session:`): run from `late-ddbs.sh`; the 0 h describe was recorded at 2026-11-04T16:32:05Z (its `X-Amz-Date`), when the session resumed, not at the stream's creation.
