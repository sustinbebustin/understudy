date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
Session: helper files under $REC/_work/LDL (# session:).

Capture gap: 4 invokes (ok, ok, ok, raise); the two drains hold 3 events (the Failure and two Successes). The fourth (a Success) was not received before the teardown deleted $P-ldl-eb.

- `source` `lambda`: matches (all 3).
- `detail-type` per condition: matches (`Lambda Function Invocation Result - Success` for Success, `Lambda Function Invocation Result - Failure` for RetriesExhausted).
- `resources` `[<bus arn>, <function arn>:$LATEST]` in every event: matches in all 3 (`arn:aws:events:us-east-2:<A>:event-bus/default`, then `arn:aws:lambda:us-east-2:<A>:function:us1103-ldl-ebf:$LATEST`); `lambda.destination.eventbridge-resources-order` = `bus-then-function`.
- event key order as delivered to SQS: `version, id, detail-type, source, account, time, region, resources, detail`; `detail` keys in record order (`version, timestamp, requestContext, requestPayload, responseContext, responsePayload`), not sorted. `time` is second precision (`2026-11-03T17:05:43Z`), detail `timestamp` milliseconds.
- `detail` bytes: compact throughout, including `responsePayload` (`{"ok":true,"mode":"ok"}`), unlike the SQS destination body, which keeps the function's spaced bytes (LDL-02). Whether Lambda or EventBridge re-serializes is not visible from here.
