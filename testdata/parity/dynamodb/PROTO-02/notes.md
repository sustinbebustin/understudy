# PROTO-02: the bytes of a None reason

- Date: 2026-11-03 (16:36 UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1 (not used by this item)
- Recipe: smithy-protocol-tests.md 15.3, run by proto.sh with no change to its commands.

## Expected (smithy-protocol-tests.md 15.3, 12.4)

- `CancellationReasons` has three entries: matches.
- first and third exactly `{"Code":"None"}`, no `Message` key, no `"Message":null`: matches (understudy's default `{"Code":"None"}` is confirmed).
- text ends `[None, ConditionalCheckFailed, None]`: matches.
