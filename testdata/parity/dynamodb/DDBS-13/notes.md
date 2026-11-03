# DDBS-13: userIdentity casing on a TTL delete (dynamodb-streams.md 12.14)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

| Field | Expected | Result |
|---|---|---|
| first block | TTL on `exp`, item `ttl-1` expired 60 s ago | run 2026-11-03T16:57:35Z on `us1103-ddbs` (TTL status ENABLED) |
| userIdentity casing (switch `user-identity-case`) | `{"PrincipalId":"dynamodb.amazonaws.com","Type":"Service"}` or lower case | deferred (TTL deletes "typically within two days"; check `get-item ttl-1` at least every 20 h, then run the readall block within 24 h of the delete). Table `us1103-ddbs` kept for it |

Session changes (`# session:`): the shared teardown deleted `us1103-ddbs-plain`, `-st`, `-cl` but kept `us1103-ddbs` for DDBS-13/14/15.
