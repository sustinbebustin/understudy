# SFN-07 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-api.md 14.8
- account ids masked as <acct> below

## Expected vs AWS

- 01 create Express with logging (ARN with `:*`): 200, created
- validation stream (sfn.logs.validation-stream, default: created at create): differs: 02 `describe-log-streams` right after create returned `{"logStreams":[]}` (no `log_stream_created_by_aws_to_validate_log_delivery_subscriptions`)
- `:*` question (UNVERIFIED 5.1): an ARN without `:*` is refused -- 400 `InvalidLoggingConfiguration` `Invalid Logging Configuration: Log Group ARN must be provided with '*' qualifier.`
- 04 start Express: 200, `express:` ARN with two UUIDs (`express:us1103-exp:<uuid>:<uuid>`)
- stream name (sfn.logs.stream-name): n/a (not observed) -- 05 still `{"logStreams":[]}` 60 s after the start, so 06 `get-log-events` on stream `None` answered 400 `ResourceNotFoundException` `The specified log stream does not exist.`; delivery did not happen within 60 s (cause unknown; `describe-resource-policies` after teardown lists no CloudWatch Logs resource policy in the account, so Step Functions apparently did not add the AWSLogDeliveryWrite policy, or removed it). Rerun with a longer wait to settle.
- event_timestamp format (sfn.logs.event-timestamp): n/a (no log events delivered, see above)
