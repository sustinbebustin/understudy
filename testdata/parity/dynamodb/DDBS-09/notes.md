# DDBS-09: eventID and sequence-number formats (dynamodb-streams.md 12.10)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

| Field | Expected | Result |
|---|---|---|
| three records on one shard (INSERT, MODIFY, REMOVE) | yes | matches: all three on one of the stream's 4 shards; the other 3 shards empty |
| eventID; switch `event-id` `hex32` | length and alphabet | matches `hex32`: 32 lower-case hex digits each (e.g. `289e8535...`) |
| sequence numbers; switch `sequence-number` `decimal-21` (21 digits, first record one above StartingSequenceNumber) | lengths; first > Starting | differs: 23 digits each (`121000...`, `122000...`, `124000...`, increasing, not contiguous) while that shard's `StartingSequenceNumber` has 21 digits (`100003...`); the first record is far above Starting, not one above it |
| eventVersion | 1.1 | `1.1` |
| shard ids | `shardId-<20 digits>-<8 hex>` | matches |

Session changes: none to these steps; the shared setup stops before the cases when the stream ARN is missing (`# session:`).
