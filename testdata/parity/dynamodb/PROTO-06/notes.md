# PROTO-06: a TransactionConflict reason

- Date: 2026-11-03 (16:36 UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Recipe: smithy-protocol-tests.md 15.7, run by proto.sh. Session change: the "run the loop once more" fallback is scripted behind a check; it did not run, since the first loop produced conflicts.

## Expected (smithy-protocol-tests.md 15.7, 12.4)

- at least one 400 `TransactionCanceledException`: matches (33 of 40 requests answered 400; 7 answered 200 `{}`).
- single reason `{"Code":"TransactionConflict","Message":"Transaction is ongoing for the item."}`: differs: AWS sends `{"Code":"TransactionConflict","Message":"Transaction is ongoing for the item"}`, with no final period (the aws-doc text has one).
- message ending `[TransactionConflict]`: matches ("Transaction cancelled, please refer cancellation reasons for specific reasons [TransactionConflict]").
- Header note: `x-amzn-requestid` and `x-amz-crc32` on both the 400 and 200 answers (lower-case request-id spelling, as PROTO-01).
