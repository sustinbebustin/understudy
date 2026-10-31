# js-sdk-v3 conformance suite

Checks understudy from the outside with the AWS SDK for JavaScript v3 (`@aws-sdk/client-*`, `@aws-sdk/lib-dynamodb`, `@aws-sdk/lib-storage`, `@aws-sdk/s3-request-presigner`), run with `bun test`.

Tests assert AWS's documented behavior (`docs/research/`), never understudy quirks. A failing test is a finding about understudy.

## Run

```sh
conformance/js-sdk-v3/run.sh                               # everything
CONFORMANCE_DYNAMODB=0 conformance/js-sdk-v3/run.sh        # everything but DynamoDB
conformance/js-sdk-v3/run.sh tests/sqs.test.ts             # one service
conformance/js-sdk-v3/run.sh -t "FIFO"                     # by test name
```

`run.sh` exits 2 before building when `CONFORMANCE_DYNAMODB` is set to anything but `0`. It builds understudy into `.build/conformance/js-sdk-v3/understudy`, starts it on `127.0.0.1:45663` with a fresh data directory, waits for the health endpoint, runs the suite through `scripts/sealed`, then stops the server and removes its containers. It keeps bun's output in the work directory's `test.log` and its JUnit report in `junit.xml`, and ends with a summary on stderr: `run.sh: passed=N failed=N skipped=N` from bun's counts, then a `run.sh: SKIP <file> > <describe> > <test>: <reason>` line per skipped test. bun records no skip reason, so `run.sh` names it from the setting that skips each file. It exits non-zero on failure and keeps the server log in the printed work directory.

Against a server you already run, from this directory:

```sh
../../scripts/sealed --endpoint http://127.0.0.1:<port> bun test --timeout 60000
```

Type-check with `bun run typecheck`.

## Environment

| Variable | Effect |
|---|---|
| `CONFORMANCE_DYNAMODB=0` | Skips `dynamodb.test.ts`, `dynamodbstreams.test.ts` and the stream mapping case of `lambda.test.ts`, each test with the reason `DynamoDB opted out (CONFORMANCE_DYNAMODB=0)`. Unset, the file runs on DynamoDB Local, pulled on first use, and `run.sh` exits 1 unless the health document reports DynamoDB as `enabled`. |
| `CONFORMANCE_LAMBDA_EXECUTION=0` | Skips `lambda.test.ts`, the function tests of `lambda-layers.test.ts` and the invocation tests of `apigatewayv2.test.ts` and `apigateway.test.ts`, which run functions in AWS's Python base image and need Docker and a first-use image pull. |

The suite refuses any endpoint that is not loopback http.

`tests/sns-http.test.ts` needs `openssl` on `PATH` and the settings `run.sh` gives it: understudy serves TLS on `127.0.0.1:45664` (`CONFORMANCE_TLS_ENDPOINT`, trusted through `NODE_EXTRA_CA_CERTS`) and delivers SNS http and https subscriptions only to the test's HTTPS receiver on `127.0.0.1:45665` (`CONFORMANCE_SNS_RECEIVER`, with the key and certificate `run.sh` makes, `CONFORMANCE_SNS_RECEIVER_KEY` and `CONFORMANCE_SNS_RECEIVER_CERT`). It checks the messages with AWS's `sns-validator` and with `openssl dgst -verify`.

## Layout

One file per service, so each can be fixed on its own.

| File | Service |
|---|---|
| `tests/sqs.test.ts` | SQS |
| `tests/sns.test.ts` | SNS |
| `tests/sns-http.test.ts` | SNS https delivery, checked by `sns-validator` and `openssl` |
| `tests/s3.test.ts` | S3, including `lib-storage` multipart uploads, default checksums, presigned URLs, virtual-hosted and path-style addressing |
| `tests/dynamodb.test.ts` | DynamoDB, DynamoDB document client |
| `tests/dynamodbstreams.test.ts` | DynamoDB Streams |
| `tests/kinesis.test.ts` | Kinesis Data Streams, on an HTTP/1.1 request handler (the client's Node default opens HTTP/2, which understudy does not speak) |
| `tests/lambda.test.ts` | Lambda |
| `tests/lambda-layers.test.ts` | Lambda layers and asynchronous invocation destinations |
| `tests/logs.test.ts` | CloudWatch Logs |
| `tests/secretsmanager.test.ts` | Secrets Manager |
| `tests/kms.test.ts` | KMS |
| `tests/iam.test.ts` | IAM |
| `tests/sts.test.ts` | STS |
| `tests/eventbridge.test.ts` | EventBridge |
| `tests/cloudwatch.test.ts` | CloudWatch metrics and alarms |
| `tests/apigatewayv2.test.ts` | API Gateway HTTP APIs: the control plane and Lambda proxy invocations through the execute-api path fallback |
| `tests/apigateway.test.ts` | API Gateway REST APIs: the control plane and its recorded refusals, a MOCK method and Lambda proxy invocations through the execute-api path fallback |

`support/` holds the connection settings (`env.ts`), shared helpers (`helpers.ts`), independent checksum reference implementations (`checksums.ts`), a zip writer for Lambda packages (`zip.ts`), the Kinesis client and stream fixture (`kinesis.ts`) and the types of `sns-validator` (`sns-validator.d.ts`).
