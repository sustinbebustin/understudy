date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1

Session: helper files under $REC/_work/LDL (# session:).

- four records and four DLQ messages: matches, but not within LDL-03's four drains: they hold 3 records (longerr x, longerr é, timeout) and 3 DLQ messages (longerr é, longerr x, timeout). The `raise` record and its DLQ message arrived in the next drains, LDL-04/02-destination-3 and LDL-04/03-dlq-1 (`ErrorCode` `200`, `ErrorMessage` `ldl intentional failure`). Every record `RetriesExhausted`, count 1. The DLQ and the on-failure destination both received every failure (5.3): matches.
- timeout DLQ `ErrorCode` `200`: matches (Number `200`).
- timeout DLQ `ErrorMessage` `RequestId: <id> Error: Task timed out after 3.00 seconds`: matches (`RequestId: 65f7dea9-... Error: Task timed out after 3.00 seconds`, `RequestID` attribute the same id).
- timeout record `responsePayload` (5.2): `{"errorType":"Sandbox.Timedout","errorMessage":"RequestId: <id> Error: Task timed out after 3.00 seconds"}`; `errorType` comes first, no `requestId` or `stackTrace`; `responseContext` `{"statusCode":200,"executedVersion":"$LATEST","functionError":"Unhandled"}`.
- 1 KB cut, `x`: 1024 bytes, 1024 characters (the full message is 2000 characters; the record's `responsePayload.errorMessage` keeps all 2000).
- 1 KB cut, `é`: 1024 bytes, 512 characters, ends on a whole character (`\xc3\xa9`). 1024 is even, so this case cannot show whether a cut inside a 2-byte character is backed off; the cut is at 1,024 bytes, as 11.3 item 7 assumes.
- DLQ body is the payload as sent: matches (`{"mode":"longerr","c":"é"}` etc.).
