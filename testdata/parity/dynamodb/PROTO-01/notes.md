# PROTO-01: top-level message key of TransactionCanceledException

- Date: 2026-11-03 (16:36 UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1 (not used by this item)
- Recipe: smithy-protocol-tests.md 15.2, run by proto.sh with no change to its commands.

## Expected (smithy-protocol-tests.md 15.2, 12.4)

- status 400 with x-amzn-RequestId and x-amz-crc32: matches (400 Bad Request; both headers present). Note: the TransactWriteItems answer spells the header `x-amzn-requestid` (all lower case) on the wire, where 12.4's sample and the R04 PutItem fixtures show `x-amzn-RequestId`; ExecuteTransaction (PROTO-05) answers `x-amzn-RequestId`.
- `__type` `com.amazonaws.dynamodb.v20120810#TransactionCanceledException`: matches.
- message under `Message` (not `message`): matches (`"Message"`; understudy's inferred default in 14 item 9 is confirmed).
- text ends `[ConditionalCheckFailed, None]` (01) and `[None, ConditionalCheckFailed]` (02): matches. Full text: "Transaction cancelled, please refer cancellation reasons for specific reasons [ConditionalCheckFailed, None]".
- Body member order (not an expectation, noted for the codec): `__type`, `CancellationReasons`, `Message`; 12.4's sample puts `Message` before `CancellationReasons`.
- The ConditionalCheckFailed reason reads "The conditional request failed" with no final period (see PROTO-03).
