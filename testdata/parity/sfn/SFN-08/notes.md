# SFN-08 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-api.md 14.9
- account ids masked as <acct> below

## Expected vs AWS

- 01 nonexistent role (sfn.create.role-validation, default ARN syntax only): matches -- 200, machine created (no role check at create)
- 02 role without states.amazonaws.com trust: matches -- 200, machine created
- 03 malformed roleArn: 400 `InvalidArn` `Invalid Role Arn: 'not-an-arn'` (note: `Invalid Role Arn`, not the `Invalid Arn: 'Invalid ARN prefix: ...'` form of 3.2)
- 04 bad machine name (UNVERIFIED 4.2): matches -- 400 `InvalidName` `Invalid Name: 'us1103 v4'`
- 05 two definition errors (multi-diagnostic form, UNVERIFIED 5.1): only the first is reported -- 400 `InvalidDefinition` `Invalid State Machine Definition: 'SCHEMA_VALIDATION_FAILED: These fields are required: [StartAt] at /'` (the unknown `Type` is not mentioned)
- 06 bad execution name (UNVERIFIED 4.2): matches -- 400 `InvalidName` `Invalid Name: 'bad name'`
- 07 input over 262,144 (UNVERIFIED 4.3, inferred Coral ValidationException without the value): matches -- 400, `__type` `com.amazon.coral.validate#ValidationException` (Coral namespace, not the swf one), `1 validation error detected: Value at 'input' failed to satisfy constraint: Member must have size less than or equal to 262144 bytes in UTF-8 encoding` (value omitted; constraint wording "size ... bytes in UTF-8 encoding", not the usual "length")
- 08 missing KMS key (UNVERIFIED 5.1): 400 `InvalidEncryptionConfiguration` `Key 'arn:aws:kms:us-east-2:<acct>:key/00000000-0000-0000-0000-000000000000' does not exist (Service: Kms, Status Code: 400, Request ID: <uuid>) (SDK Attempt Count: 1)` -- the Java SDK v2 cause form with the `(SDK Attempt Count: 1)` suffix

## Replay (internal/parity)

`scenario.json` replays steps 03 to 05; internal/parity/baseline.json holds the outcomes.

- Not replayed: 01 and 02, whose success bodies differ by member order alone; 06 and 07, which start executions of a machine an earlier step created; 08, since a CUSTOMER_MANAGED_KMS_KEY encryptionConfiguration answers 501.
