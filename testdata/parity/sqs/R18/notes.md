# R18

- Date: 2026-10-14T07:06Z
- Region: us-east-2 (free-plan project account)
- aws-cli/2.37.13 Python/3.15.0 Linux/6.8.0-142-generic source/x86_64.ubuntu.24
- curl 8.22.0

## Expected

Compared against backlog R18 / sigv4-checksums-endpoints.md 9 bullet 1 and understudy's code as read on 2026-10-14 (not run).

- 1 presign X-Amz-Expires=604801: differs: AWS 400 AuthorizationQueryParametersError "X-Amz-Expires must be less than a week (in seconds); that is, the given X-Amz-Expires must be less than 604800 seconds" (01-presign-expires-604801.http); understudy same code/status, text "X-Amz-Expires must be less than a week (in seconds) that is 604800" (internal/auth/parse.go:484)
- 1 presign X-Amz-Expires=0: differs: AWS 403 AccessDenied "Request has expired" with X-Amz-Expires=0, Expires=<X-Amz-Date>, ServerTime (01-presign-expires-0.http); understudy 400 AuthorizationQueryParametersError "X-Amz-Expires must be at least 1 second; got 0" (internal/auth/parse.go:481-482)
- 1 presign X-Amz-Expires=-1: matches (400 AuthorizationQueryParametersError "X-Amz-Expires must be non-negative"; internal/auth/parse.go:480, internal/service/s3/auth.go:61-62)
- 1 presign expired by 1 s: matches (403 AccessDenied "Request has expired", elements X-Amz-Expires, Expires, ServerTime in that order; internal/auth/verify.go:270-272, 342-351)
- 1 presign X-Amz-Date 16 min in future: differs: AWS 403 AccessDenied "Request is not yet valid" with elements X-Amz-Date (epoch milliseconds, 1791962573000), Expires (X-Amz-Date + X-Amz-Expires, ISO 8601), ServerTime (01-presign-date-16min-future.http); understudy 403 RequestTimeTooSkewed "The difference between the request time and the current time is too large." with RequestTime/ServerTime/MaxAllowedSkewMilliseconds (internal/auth/verify.go:267-268, 315-318)
- 1 presign unknown X-Amz-Algorithm: differs: AWS 400 AuthorizationQueryParametersError `X-Amz-Algorithm only supports "AWS4-HMAC-SHA256 and AWS4-ECDSA-P256-SHA256"` (01-presign-unknown-algorithm.http); understudy same code/status, text `X-Amz-Algorithm only supports "AWS4-HMAC-SHA256", not "AWS4-HMAC-SHA512"` (internal/auth/parse.go:244-245)
- 2 S3 header-signed 14 min skew: matches (200; 15-minute window, internal/auth/verify.go:19, 299-310)
- 2 S3 header-signed 16 min skew: matches (403 RequestTimeTooSkewed, same message, RequestTime as sent, ServerTime ISO 8601 Z, MaxAllowedSkewMilliseconds 900000; internal/auth/verify.go:329-337)
- 3 window for SQS/SNS/STS/DynamoDB: matches (6 min accepted, 16 min refused: 15 minutes for every service, not 5; internal/auth/verify.go:19)
- 3 skew message text: matches ("Signature expired: <req> is now earlier than <now-15m> (<now> - 15 min.)" in all four; internal/auth/verify.go:304)
- 3 DynamoDB skew status/__type: matches (400 com.amazon.coral.service#InvalidSignatureException, key "message"; internal/awserr/catalogue.go:71-73)
- 3 DynamoDB x-amz-crc32 on the error: differs: AWS sends x-amz-crc32: 1524702311 (CRC-32 of the error body) on the 400 (03-dynamodb-skew-16min.http); understudy's edge-rendered errors carry no x-amz-crc32 (only internal/service/dynamodb/shims.go:199-215 and rewrite.go:73-103 set it, on service answers)
- 3 SQS (JSON) skew status: differs: AWS 403 with x-amzn-query-error "SignatureDoesNotMatch;Sender", __type com.amazon.coral.service#InvalidSignatureException (03-sqs-json-skew-16min.http); understudy 400 (JSON family vocabulary, internal/auth/verify.go:92-97 -> internal/awserr/catalogue.go:72); __type, message key and query-error header match
- 3 SNS (Query) skew: matches (403, Type Sender, Code SignatureDoesNotMatch; internal/auth/verify.go:101-106, internal/awserr/catalogue.go:89-91)
- 3 STS (Query) skew: matches (403 SignatureDoesNotMatch Sender, same vocabulary)
- 4 presigned PUT with x-amz-checksum-crc32 in the query (wrong value and right value): matches: AWS ignores the query checksum, 200 both, stores and returns only x-amz-checksum-crc64nvme M3eFcAZSQlc= + FULL_OBJECT (04-presign-put-crc32-*.http); understudy decodes checksum members from the sent headers only (internal/edge/edge.go:413, codec.DecodeHead on x.r), so parseDeclared sees none and defaults to CRC64NVME (internal/service/s3/checksum_single.go:81, 88-114). Code reading only; HoistedHeader (internal/auth/canonical.go:122) does copy the parameter into kernel.Call.Header, which no checksum path reads today
- 5 aws-chunked + Expect: 100-continue + bad signature (re-run 2026-10-14 with a single Content-Length): S3 answers 403 Forbidden before the body is sent (0.09 s after the headers, no 100 Continue), so signature rejection happens on the headers alone. Settles the open interaction in wire-rest.md. understudy: not compared at the socket level.

