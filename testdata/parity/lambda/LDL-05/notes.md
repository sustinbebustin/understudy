date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1

Session: helper files under $REC/_work/LDL (# session:).

- small record arrives as the envelope's `Message`: matches (02; the compact record string, `requestPayload` `{"mode":"ok"}`, `responsePayload` `{"ok": true, "mode": "ok"}`).
- record `Subject`: none; the envelope has no `Subject` key (keys `Message, MessageId, Signature, SignatureVersion, SigningCertURL, Timestamp, TopicArn, Type, UnsubscribeURL`).
- record `MessageAttributes`: none; the envelope has no `MessageAttributes` key. Matches 11.3 item 5 ("no subject").
- 01 the 300 KB invoke: 202, accepted.
- 300 KB record, nothing (delivery failure) or a record without `requestPayload` (payload dropped): differs from both alternatives and from the switch default `lambda.destination.sns-oversize` = `delivery-failure` (4.2, 10.1): a record was delivered (03) with BOTH payloads dropped and a new `deliveryError` object after `responseContext`: `{"version":"1.0","timestamp":"...","requestContext":{...,"condition":"Success","approximateInvokeCount":1},"responseContext":{"statusCode":200,"executedVersion":"$LATEST"},"deliveryError":{"statusCode":413,"errorCode":"RequestEntityTooLargeException","errorMessage":"The total destination payload is too large. RequestPayload size: 300026 bytes, ResponsePayload size: 26 bytes"}}`. This happened on `OnSuccess`, although the model's text names only `OnFailure`.
- step 04 the original payload as `Message`: matches (`{"mode":"raise"}`).
- step 04 `RequestID`, `ErrorCode`, `ErrorMessage` in the envelope's `MessageAttributes` with types `String`, `Number`, `String`: matches (`RequestID` String `<id>`, `ErrorCode` Number `200`, `ErrorMessage` String `ldl intentional failure`). No `Subject`.
