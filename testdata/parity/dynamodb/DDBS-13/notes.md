# DDBS-13: userIdentity casing on a TTL delete (dynamodb-streams.md 12.14)

- Date: 2026-11-03 (first block), 2026-11-04 (readall)
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

| Field | Expected | Result |
|---|---|---|
| first block | TTL on `exp`, item `ttl-1` expired 60 s ago | run 2026-11-03T16:57:35Z on `us1103-ddbs` (TTL status ENABLED) |
| TTL delete | "typically within two days" | `get-item ttl-1` printed `None` at the first check, 2026-11-04T16:31Z; the REMOVE record's `ApproximateCreationDateTime` is 2026-11-03T17:05:31Z, about 8 minutes after the put (16:57:34Z, the INSERT's) |
| readall | every shard from TRIM_HORIZON | 62 GetRecords over the stream's 25 shards (`records-<k>.out`, the shard in `records-<k>.shard`): `records-3` (shard `...-f3b53ca8`, one of the 4 initial shards) holds the INSERT and the TTL REMOVE of `ttl-1`; `records-5` holds DDBS-09's 5 records; every other page is empty |
| userIdentity casing (switch `user-identity-case`) | `{"PrincipalId":"dynamodb.amazonaws.com","Type":"Service"}` or lower case | `{"PrincipalId":"dynamodb.amazonaws.com","Type":"Service"}` (`user-identity.txt`): the model's PascalCase. Only the REMOVE carries `userIdentity`, a top-level member of the record after `eventVersion` on the wire; the INSERT has none |
| REMOVE record | `Keys` and `OldImage` | `eventName REMOVE`, `Keys`, `OldImage` (`exp`, `pk`), no `NewImage`, `SizeBytes 23`, `StreamViewType NEW_AND_OLD_IMAGES`, `eventVersion 1.1` |

Session changes (`# session:`): the shared teardown deleted `us1103-ddbs-plain`, `-st`, `-cl` but kept `us1103-ddbs` for DDBS-13/14/15. The readall ran from `late-ddbs.sh`, which writes each page's shard to `records-<k>.shard`. The parity fixture keeps `records-3` (as `records-3.json`, `records-3-shard.txt`) and `user-identity.txt`; the other 61 pages, empty or DDBS-09's records, are not distilled.
