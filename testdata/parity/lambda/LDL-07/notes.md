date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
Session: helper files under $REC/_work/LDL (# session:); the fetched object is $REC/_work/LDL/ldl07-record.json.

- key `aws/lambda/async/$P-ldl-s3/YYYY/MM/DD/YYYY-MM-DDTHH.MM.SS-<uuid>` in UTC near the invoke time: matches. Key `aws/lambda/async/us1103-ldl-s3/2026/11/03/2026-11-03T16.52.53-fdc93dec-996a-4141-9306-9a64b74461b0`; invoked 16:52:52.509Z, record `timestamp` 16:52:53.365Z, so the key's seconds are the record timestamp's, truncated, UTC. UUID: lower-case, hyphenated, version 4. Function name without qualifier.
- `Content-Type`: `application/octet-stream` (not JSON); `Content-Length` 657; `x-amz-server-side-encryption` AES256 (bucket default); no user metadata.
- body: the compact record, same shape as the SQS record (condition `RetriesExhausted`, count 1, `responsePayload` the error object).
- S3 `OnSuccess` text (4.5): matches understudy's generic text: 01 is 400 `InvalidParameterValueException`, body `{"Type":"User","message":"The provided destination config DestinationConfig(onSuccess=OnSuccess(destination=arn:aws:s3:::us1103-ldl-b), onFailure=null) is invalid."}`.
- 02 S3 `OnFailure`: 200, accepted.
