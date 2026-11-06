# PROTO-05: ExecuteTransaction cancellation

- Date: 2026-11-03 (16:36 UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1 (not used by this item)
- Recipe: smithy-protocol-tests.md 15.6, run by proto.sh with no change to its commands.

## Expected (smithy-protocol-tests.md 15.6, 12.4)

- status 400 `TransactionCanceledException`: matches (`__type` `com.amazonaws.dynamodb.v20120810#TransactionCanceledException`).
- reasons `[ConditionalCheckFailed, None]` in statement order: matches (`[{"Code":"ConditionalCheckFailed","Message":"The conditional request failed"},{"Code":"None"}]`; reason text without a final period, as PROTO-03).
- same message format as PROTO-01: matches; the body is byte-identical to PROTO-01 step 01 (same `x-amz-crc32` 964572741), key `Message`.
- Header note: this answer spells `x-amzn-RequestId`, where TransactWriteItems (PROTO-01..04, 06) spells `x-amzn-requestid`.
