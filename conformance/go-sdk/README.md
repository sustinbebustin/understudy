# aws-sdk-go-v2 conformance suite

This suite checks understudy through the real AWS SDK for Go v2 clients, with their default settings. It is a separate Go module (`github.com/sustinbebustin/understudy/conformance/go-sdk`), so the SDK never becomes a dependency of understudy itself.

The tests assert AWS behavior as documented in [`docs/research/`](../../docs/research/). That research is the arbiter. A test never encodes an understudy quirk: when understudy and the research disagree, the test fails and understudy gets fixed. Each test file names the research sections it relies on.

## Running it

```sh
conformance/go-sdk/run.sh                                  # everything (see DynamoDB below)
CONFORMANCE_DYNAMODB=0 conformance/go-sdk/run.sh           # everything but DynamoDB
conformance/go-sdk/run.sh -run TestS3                      # extra arguments go to go test
```

`run.sh` does the following:

1. Refuses to start (exit 2) when `CONFORMANCE_DYNAMODB` is set to anything but `0`.
2. Builds understudy from this repository into `.build/conformance/go-sdk/understudy`.
3. Starts it on `127.0.0.1:45661`, plus a TLS listener on a free port, with a fresh data directory under `.build/conformance/go-sdk/run.*`.
4. Waits for the health endpoint; unless DynamoDB is opted out, exits 1 unless it is `enabled`.
5. Runs `go test -v` through `scripts/sealed`, which strips every inherited `AWS_*` setting, uses dummy credentials and refuses any endpoint that is not loopback. The output is kept in the run directory's `test.log`.
6. Prints a summary on stderr: `run.sh: passed=N failed=N skipped=N`, then a `run.sh: SKIP <test>: <reason>` line per skipped test or subtest, the reason being the first line the test logged after it last started or resumed.
7. Always stops understudy and removes the containers it labelled for this run, including after a failure or an interrupt.

It exits with `go test`'s status, or non-zero when understudy cannot be built or started. On success it deletes the run directory. On failure it keeps the directory and prints its path, so understudy's log, data and test output are there to inspect.

Requirements: Go, `uv` (for `scripts/sealed`) and a Docker Engine. Lambda tests run functions in AWS's Lambda base images, which are pulled on first use.

### DynamoDB

understudy serves DynamoDB through Amazon DynamoDB Local, pulled on first use and run under [Amazon's licence terms](https://aws.amazon.com/dynamodb/dynamodblocallicense/). The DynamoDB tests run by default:

```sh
conformance/go-sdk/run.sh -run TestDynamoDB
```

`CONFORMANCE_DYNAMODB=0` skips them instead, and the summary lists each one.

### Against an understudy you started yourself

```sh
scripts/sealed --endpoint http://127.0.0.1:4566 go test ./...    # from conformance/go-sdk
```

`TestMain` refuses to run unless `AWS_ENDPOINT_URL` is a loopback address. Without a TLS listener (`UNDERSTUDY_TLS_ENDPOINT` plus `AWS_CA_BUNDLE`, which `run.sh` sets), the trailer-checksum tests skip.

## Layout

There is one file per service, named after it, so that one person can own and fix one service at a time:

| File | Service |
|---|---|
| `s3_test.go` | S3 |
| `sqs_test.go` | SQS |
| `sns_test.go` | SNS |
| `lambda_test.go` | Lambda |
| `dynamodb_test.go` | DynamoDB |
| `cloudwatchlogs_test.go` | CloudWatch Logs |
| `secretsmanager_test.go` | Secrets Manager |
| `kms_test.go` | KMS |
| `iam_test.go` | IAM |
| `sts_test.go` | STS |
| `eventbridge_test.go` | EventBridge |
| `cloudwatch_test.go` | CloudWatch metrics and alarms |

`main_test.go` holds the shared setup and helpers:

- the loopback guard;
- `account` and `name` for unique resource names;
- `errAs` for the SDK's generated typed errors, and `apiErr` for error codes the models do not declare;
- `wantStatus` and `wantMessage`;
- `recordingClient`, for asserting what went on the wire.

Each test creates uniquely named resources and deletes them when it ends, so tests run in parallel and never depend on each other or on an earlier run.

## What the suite covers

- Every phase 1 operation that understudy binds, for every service understudy emulates, with the error codes of the common failure paths.
- S3:
  - virtual-hosted (`<bucket>.localhost`) and path-style addressing;
  - the default CRC32 request checksums, as headers over HTTP and as `aws-chunked` trailers over HTTPS;
  - multipart uploads through the transfer manager, with composite checksums and multipart ETags;
  - conditional reads and writes;
  - presigned URLs;
  - SSE-KMS;
  - event notifications to SQS.
- SQS: MD5 validation of bodies and attributes, FIFO queues, long polling, visibility, and dead-letter queues and redrive.
- Lambda: Invoke (RequestResponse, Event and DryRun), function URLs and SQS event source mappings, all in real containers.
- EventBridge: scheduled rules on a running clock. `TestEventBridgeSchedule` waits about a minute for a `rate(1 minute)` rule to fire.
