date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1

Session: helper files under $REC/_work/LDL (# session:).

- invoked asynchronously (two `START` lines about 60 s apart, same request id): matches (`lambda.destination.lambda-invocation-type` = `Event`, 4.3). START 16:50:11.996Z and 16:51:05.193Z (53 s apart), both `RequestId: cc4b4e16-...`, the destination's own `MaximumRetryAttempts 1` retry. The destination's request id differs from the source event's (`1d6747fd-...`).
- logged `event` (the record as a Lambda payload): the record as an object (logged through Python `json.dumps`, so the bytes are not Lambda's): `{"version": "1.0", "timestamp": "2026-11-03T16:50:11.655Z", "requestContext": {"requestId": "1d6747fd-...", "functionArn": "arn:aws:lambda:us-east-2:<A>:function:us1103-ldl-src:$LATEST", "condition": "RetriesExhausted", "approximateInvokeCount": 1}, "requestPayload": {"mode": "raise"}, "responseContext": {"statusCode": 200, "executedVersion": "$LATEST", "functionError": "Unhandled"}, "responsePayload": {"errorMessage": "ldl intentional failure", "errorType": "Exception", "requestId": "1d6747fd-...", "stackTrace": [...]}}`. Same key order as the SQS record.
