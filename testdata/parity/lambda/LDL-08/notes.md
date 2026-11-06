date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
Session: helper files under $REC/_work/LDL (# session:). All refusals are HTTP 400, header `x-amzn-ErrorType: InvalidParameterValueException`, body `{"Type":"User","message":...}`. `<A>` = account id.

- 01 missing queue: refused, `The destination ARN arn:aws:sqs:us-east-2:<A>:us1103-ldl-nosuch is invalid.` (matches understudy's `lambda.destination.config-existence-check` text for SQS).
- 02 missing topic: refused, `The destination ARN arn:aws:sns:us-east-2:<A>:us1103-ldl-nosuch is invalid.` (matches understudy).
- 03 missing bus: differs from understudy's default `all`: AWS accepts a missing event bus, 200, `OnFailure.Destination` echoed.
- 04 missing bucket: differs from understudy's text: refused, but with `Error occurred while ListBucket. S3 Error Code: 404 Not Found. S3 Error Message: Not Found` (not `The destination ARN ... is invalid.`).
- 05 FIFO queue: differs from 2.2's inference (accepted at configuration): refused, `The provided destination config DestinationConfig(onSuccess=null, onFailure=OnFailure(destination=arn:aws:sqs:us-east-2:<A>:us1103-ldl-f.fifo)) is invalid.`
- 06 both sides: 200.
- 08 whether `OnSuccess` survived 07: it survived. Differs from understudy (replaces both sides when `DestinationConfig` is present, REPO config.go:L517): after Update with only `OnFailure` (SNS topic), 07's response and 08 show `OnSuccess` still `$P-ldl-dest` and `OnFailure` the topic. Update merges per side.
- 09 DLQ missing queue: `Error occurred while SendMessage. SQS Error Code: AWS.SimpleQueueService.NonExistentQueue. SQS Error Message: The specified queue does not exist.`
- 10 DLQ FIFO queue: `Invalid dead letter queue ARN: FIFO SQS queue is not supported for dead letter configuration`
- 11 DLQ event bus: `Invalid dead letter queue ARN: The service specified by the TargetArn is not supported for dead letter configuration.`
- 12 no permission, topic: differs in wording from the provider comment in 2.3 (`The function's execution role ...`): `The function execution role does not have permissions to call Publish on arn:aws:sns:us-east-2:<A>:us1103-ldl-topic` (no `'s`, no final period).
- 13 no permission, queue: `The function execution role does not have permissions to call SendMessage on arn:aws:sqs:us-east-2:<A>:us1103-ldl-dest` (no final period).
- 14 no permission, DLQ: `The provided execution role does not have permissions to call SendMessage on SQS` (matches the family TF function.go:L1575 retries on; names the service, not the ARN).
