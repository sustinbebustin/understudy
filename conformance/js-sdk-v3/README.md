# js-sdk-v3 conformance suite

Checks understudy from the outside with the AWS SDK for JavaScript v3 (`@aws-sdk/client-*`, `@aws-sdk/lib-dynamodb`, `@aws-sdk/lib-storage`, `@aws-sdk/s3-request-presigner`), run with `bun test`.

Tests assert AWS's documented behavior (`docs/research/phase1/`), never understudy quirks. A failing test is a finding about understudy.

## Run

```sh
conformance/js-sdk-v3/run.sh                    # everything
conformance/js-sdk-v3/run.sh tests/sqs.test.ts  # one service
conformance/js-sdk-v3/run.sh -t "FIFO"          # by test name
```

`run.sh` builds understudy into `.build/conformance/js-sdk-v3/understudy`, starts it on `127.0.0.1:45663` with a fresh data directory, waits for the health endpoint, runs the suite through `scripts/sealed`, then stops the server and removes its containers. It exits non-zero on failure and keeps the server log in the printed work directory.

Against a server you already run, from this directory:

```sh
../../scripts/sealed --endpoint http://127.0.0.1:<port> bun test --timeout 60000
```

Type-check with `bun run typecheck`.

## Environment

| Variable | Effect |
|---|---|
| `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1` | Runs `dynamodb.test.ts`. Without it that file is skipped: understudy only pulls DynamoDB Local after the caller accepts its licence, and the suite never accepts it for you. |
| `CONFORMANCE_LAMBDA_EXECUTION=0` | Skips `lambda.test.ts`, which runs functions in AWS's Python base image and needs Docker and a first-use image pull. |

The suite refuses any endpoint that is not loopback http.

## Layout

One file per service, so each can be fixed on its own.

| File | Service |
|---|---|
| `tests/sqs.test.ts` | SQS |
| `tests/sns.test.ts` | SNS |
| `tests/s3.test.ts` | S3, including `lib-storage` multipart uploads, default checksums, presigned URLs, virtual-hosted and path-style addressing |
| `tests/dynamodb.test.ts` | DynamoDB, DynamoDB document client |
| `tests/lambda.test.ts` | Lambda |
| `tests/logs.test.ts` | CloudWatch Logs |
| `tests/secretsmanager.test.ts` | Secrets Manager |
| `tests/kms.test.ts` | KMS |
| `tests/iam.test.ts` | IAM |
| `tests/sts.test.ts` | STS |
| `tests/eventbridge.test.ts` | EventBridge |
| `tests/cloudwatch.test.ts` | CloudWatch metrics and alarms |

`support/` holds the connection settings (`env.ts`), shared helpers (`helpers.ts`), independent checksum reference implementations (`checksums.ts`) and a zip writer for Lambda packages (`zip.ts`).
