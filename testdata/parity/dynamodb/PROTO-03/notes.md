# PROTO-03: the ConditionalCheckFailed reason text

- Date: 2026-11-03 (16:36 UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1 (not used by this item)
- Recipe: smithy-protocol-tests.md 15.4, run by proto.sh with no change to its commands.

## Expected (smithy-protocol-tests.md 15.4, 12.4)

- one reason: matches.
- reason `{"Code":"ConditionalCheckFailed","Message":"The conditional request failed."}`: differs: AWS sends `{"Code":"ConditionalCheckFailed","Message":"The conditional request failed"}`, with no final period. Exact bytes of the reason: `{"Code":"ConditionalCheckFailed","Message":"The conditional request failed"}`. This agrees with the recorded ConditionalCheckFailedException text (R04 `05-put-ccf.json`) and contradicts the aws-doc code table and DynamoDB Local, which 12.4 took as understudy's default.
- top-level text ends `[ConditionalCheckFailed]`: matches.
