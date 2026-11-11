# APIGW2-10: failures and limits (apigateway-http.md 14.11)

- Date: 2026-11-03
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu)

## Expected (13.4 `apigatewayv2.lambda.failure-answers`, 5.6, 4.7)

- Function error, 2.0 and 1.0 (01, 02; 500 `{"message":"Internal Server Error"}`): matches: `500 Internal Server Error`, `Content-Type: application/json`, body `{"message":"Internal Server Error"}` (35 bytes). It is not REST's 502 or the "Internal server error" spelling.
- Integration timeout (03; 503 `{"message":"Service Unavailable"}`): matches: `503 Service Unavailable`, `{"message":"Service Unavailable"}`.
- Body over 10 MB (13.4: refuses over 10,485,760 bytes): differs: 10,000,001 bytes (04) is already refused, so the limit is 10 MB decimal (10,000,000). Answer: `100 Continue`, then `413 Request Entity Too Large` with `{"message":"Request Entity Too Large"}` (38 bytes), `Content-Type: application/json` and `apigw-requestid`.
- Body over 10 MiB (05): `413` with the same body, but differs from 13.3 item 8 (gateway answers carry Content-Type and apigw-requestid): this answer has no `Content-Type`, only `Content-Length: 38` and `apigw-requestid` (lower case, where the gateway's answers send `Apigw-Requestid`). A front layer refuses it before the gateway.
- Event over Lambda's 6 MB (06, 7,000,000-byte text body; 13.4: 413 Request Entity Too Large): matches: `413` `{"message":"Request Entity Too Large"}` with `Content-Type: application/json` and `apigw-requestid`.
- Headers over 10 KB (07; 4.5 unverified): `431 Request Header Fields Too Large` with `{"message":"Request Header Fields Too Large"}` (45 bytes). It has `apigw-requestid` (lower case) and no `Content-Type`.
