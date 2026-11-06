date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1

Session: helper files under $REC/_work/LDL (# session:).

Capture gap: 02-receive returned only the `ok` record. The `none` and `text` records were received by the next drains of the same queue, in LDL-04/02-destination-1 (`text`) and LDL-04/02-destination-2 (`none`); the lines below use those raw bodies.

- raw `Body` spacing: differs from 11.4's "compact until LDL-02": the envelope Lambda builds is compact (`{"version":"1.0","timestamp":...,"requestContext":{...},...}`, no spaces), but `requestPayload` is the payload bytes as sent, not re-serialized: `"requestPayload":{"mode": "ok",  "pad": [1, 2]}` keeps the double space and the spaces after `:` and `,`. `responsePayload` is likewise the function's response bytes as the runtime produced them (Python `json.dumps`): `"responsePayload":{"ok": true, "mode": "ok"}`.
- key order: matches 3.1 / 11.4 (`version, timestamp, requestContext{requestId, functionArn, condition, approximateInvokeCount}, requestPayload, responseContext{statusCode, executedVersion}, responsePayload`).
- escaping: nothing to escape in these payloads; no `\/` or `\u` forms seen.
- `responsePayload` for `none`: `null` (key present, value `null`), `"responsePayload":null`.
- `responsePayload` for `text`: `"plain text"` (the Python runtime's JSON encoding of the returned string; the record embeds it as a JSON string).
- `timestamp`: after the invocation ended, about 0.1 s later: `ok` logged t 16:37:43.274Z, END 16:37:43.275Z, record 16:37:43.393Z; `none` t 16:37:42.380Z, record 16:37:42.477Z; `text` t 16:37:42.709Z, record 16:37:42.822Z. So the record-creation instant, not the invoke instant.
- `requestId` equals the 202's `x-amzn-RequestId`: matches for all three (none acd309fc-..., text ad873200-..., ok 3f5e1529-...).
