# ADR 0012: Account from the access key, default account, port and regions

Status: accepted, 2026-10-08.

## Context

understudy derives the account from the access key and supports multi-account and multi-region. Real `AKIA`/`ASIA` keys encode the account with a documented bit layout, and `AKIAIOSFODNN7EXAMPLE` decodes to a real-looking `581039954779`; LocalStack refuses to decode real-looking keys to avoid mapping a developer's production key to a foreign account (`iam-sts-kms.md 2.2 to 2.3`, `sigv4-checksums-endpoints.md 7`). DynamoDB Local accepts only alphanumeric keys. Terraform's `aws_region` data source rejects region names it does not know (`tf-remaining.md 10`). Existing user configurations use port 4566 and account `000000000000`.

## Decision

- `identity.Store.Resolve(key)` in order: a key minted by understudy (`CreateAccessKey`, `AssumeRole`, Lambda execution environment) maps to its principal and account; a 12-digit numeric key is that account's root; anything else, including real-looking keys and documentation keys, is the default account's root. Real-looking keys that understudy did not mint are never decoded; the server logs one warning per key.
- Minted keys are `AKIA`/`ASIA` plus 16 base32 characters with the account in the 48-bit window (top bit set, `account<<7`, random remainder), 40-character secrets, opaque session tokens, so they decode correctly in other tools and satisfy DynamoDB Local's character rule.
- Principal shapes: `Root`, `User`, `AssumedRole{Role, Session, Source}`, `ServicePrincipal`, `Anonymous`. `GetCallerIdentity` renders each exactly (`iam-sts-kms.md` Implications 7). STS `Expiration` is wall time.
- Region comes from the SigV4 credential scope and is validated against the table of real AWS region names; `regions.extra` adds names. IAM and STS accept any region and store per account.
- Defaults: listen `127.0.0.1:4566`, default account `000000000000`, so `AWS_ENDPOINT_URL=http://localhost:4566` with `test`/`test` credentials works unchanged for SDKs, the CLI and Terraform with no `skip_*` flags.

## Alternatives considered

- Decoding every well-formed key: silently selects a foreign account number for a real production key; rejected on safety grounds.
- A reserved understudy prefix (`LSIA`-style): minted keys would not decode in other tools; the store lookup gives the same guarantee without a new prefix.
- moto's default account `123456789012`: LocalStack's `000000000000` is what existing endpoint configurations assume.

## Consequences

- The access recorder and least-privilege generator attribute calls to real principals, including function execution roles.
- The journal links in-function calls to invocations through per-environment minted keys (ADR 0007).
- Users who want a non-AWS region name must add it to the configuration and set `skip_region_validation` in Terraform.

## Amendments (review, 2026-10-08)

- The region source is a sum type: `CredentialScope{Region}` from SigV4, `HostRegion{Region}` from a `<svc>.<region>.<base>` host, `None` for SigV2, presigned-SigV2, unsigned and anonymous requests. Under `None` S3 serves the bucket's home region and sets `x-amz-bucket-region`; non-S3 calls use `us-east-1`. The cross-region rules of ADR 0013 apply only when a region is asserted.
- IAM and STS cells are keyed by `aws.AccountID`, not by a scope with a zeroed region.
- Internal calls run under an explicit `PrincipalMode` (ADR 0004), so the access recorder attributes forward-access KMS calls to the caller and Lambda log writes to the execution role.

## Amendments (pre-tag review, 2026-10-13)

Decided at the pre-tag review: signature verification is built before the first tag.

- Under `sigv4: verify` the edge verifies every credential form it parses (SigV4 header and presigned, SigV2 header and presigned), after the claim and before decode. The secret of a key understudy minted is its own; `accounts.keys` entries carry theirs; the dummy key `test` and every 12-digit account key verify with the dummy secret `test`, so `AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test` and the account-selecting keys keep working; any other unminted key is refused as AWS refuses an unknown key (JSON `UnrecognizedClientException`, Query `InvalidClientTokenId`, S3 `InvalidAccessKeyId`). A minted `ASIA` key must send its session token (header or query) and must not be past its expiry (JSON `UnrecognizedClientException` / `ExpiredTokenException`, Query `InvalidClientTokenId` / `ExpiredToken`, S3 `InvalidToken` / `ExpiredToken`). Presigned URLs expire on the emulator clock with S3's texts and elements, and signed `aws-chunked` uploads verify their chunk and trailer signatures. Static keys come from configuration at every start and are never exported in snapshots. Under `parse` (the default) nothing changes.
