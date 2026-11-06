# PROTO-04: Item in a reason under ALL_OLD

- Date: 2026-11-03 (16:36 UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1 (not used by this item)
- Recipe: smithy-protocol-tests.md 15.5, run by proto.sh with no change to its commands.

## Expected (smithy-protocol-tests.md 15.5, 12.4)

- first reason carries `"Item":{"pk":{"S":"held"},"v":{"N":"1"}}` beside `Code` and `Message`: matches in content; the bytes differ in order: AWS sends `{"Code":"ConditionalCheckFailed","Item":{"v":{"N":"1"},"pk":{"S":"held"}},"Message":"The conditional request failed"}`, i.e. `Item` between `Code` and `Message`, attributes `v` before `pk`, and the reason text without a final period (PROTO-03).
- top-level message key compared with PROTO-01: the same, `Message` (an Item in a reason does not change it).
- second reason `{"Code":"None"}`, text ends `[ConditionalCheckFailed, None]`: matches.
