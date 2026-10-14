# e2e-pipeline

The phase 1 event-driven pipeline, end to end, as a Go test module that talks to a running understudy through aws-sdk-go-v2 and the `admin` client. Tests assert AWS behavior as documented in `docs/research/phase1/`; where the research leaves a value unverified, the test does not assert it.

## Run

```sh
UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1 conformance/e2e-pipeline/run.sh  # the whole suite
CONFORMANCE_DYNAMODB=0 conformance/e2e-pipeline/run.sh                 # the tests that need no DynamoDB
CONFORMANCE_DYNAMODB=0 conformance/e2e-pipeline/run.sh -run TestSQS    # extra arguments go to go test
```

`run.sh` builds understudy from this repository into `.build/conformance/e2e-pipeline/understudy`, serves it on `127.0.0.1:45666` with a fresh data directory under `.build/conformance/e2e-pipeline/run.*` (`--persist`), a frozen clock, seed 1 and no `UNDERSTUDY_*` or `AWS_*` setting from your shell but the licence variable, waits for `/_understudy/v1/health`, and runs `go test -v` through `scripts/sealed`, so no real AWS setting or credential is used. The output is kept in the run directory's `test.log`, and the run ends with a summary on stderr: `run.sh: passed=N failed=N skipped=N`, then a `run.sh: SKIP <test>: <reason>` line per skipped test. On exit it always stops the server, runs `understudy cleanup` on the data directory, removes the containers and images labeled with the run's `understudy.dev/e2e-run` label, and exits with the suite's status. It removes the run directory on success and keeps it, printing its path, on failure.

Requirements: Go, Docker, and network access on first use to pull `public.ecr.aws/lambda/provided:al2023`, `public.ecr.aws/lambda/python:3.13` and `amazon/dynamodb-local`. Every worker takes a DynamoDB lock for each SQS record, and DynamoDB Local's licence is yours to accept, so `run.sh` exits 2 before building unless exactly one of these is set:

- `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1` accepts the Amazon DynamoDB Local License Agreement for the run; serve reads it from its environment, and `run.sh` exits 1 unless DynamoDB then reports `enabled`.
- `CONFORMANCE_DYNAMODB=0` provisions the pipeline without its lock table. Every test that publishes to the topic, and so makes a worker take a lock, or that reads the lock table skips (`requireDynamoDB`) with the reason `DynamoDB opted out (CONFORMANCE_DYNAMODB=0)`; the rest run, and DynamoDB Local never starts.

The suite also runs against any fresh understudy on a loopback endpoint: `scripts/sealed --endpoint http://127.0.0.1:<port> go test ./...` from this directory. It refuses an endpoint that is not loopback, and expects an empty emulator with a frozen clock.

## The pipeline

`TestMain` builds three workers and provisions the pipeline once (`pipeline_test.go`):

- An SNS topic fans out to three SQS queues, each with a dead-letter queue and a queue policy for the topic, and to a tap queue with no filter:
  - `go-worker`: attribute filter `{"kind":["order"]}`, raw delivery, visibility 60 s, maxReceiveCount 3, batch size 10, no window, `ReportBatchItemFailures`.
  - `py-worker`: body filter `{"priority":[{"numeric":[">=",5]}]}`, enveloped delivery, visibility 120 s, maxReceiveCount 5, batch size 25, 20 s batching window.
  - `image-worker`: attribute filter `{"kind":["refund"],"amount":[{"numeric":[">",100]}]}`, raw delivery, visibility 90 s, maxReceiveCount 5, batch size 5.
- The workers are a Go `provided.al2023` zip (`functions/goworker`), a Python 3.13 zip (`functions/pyworker`) and a container image built from the Python base image (`functions/imageworker`, tagged with an ECR-shaped name). For each record they read the secret, take a DynamoDB lock with `attribute_not_exists(LockID) OR ExpiresAt < :now` whose expiry is the record's `SentTimestamp` plus 300 s (so it lives on the emulator clock), write the record to the SSE-KMS bucket and log a JSON line. The Go worker also writes a log stream per message and reports payloads marked `fail` in `batchItemFailures`.
- The image worker also serves a function URL and the scheduled events of an EventBridge rule the events test creates.

Emulator time moves only when a test advances the clock, so visibility timeouts, batching windows, TTL and schedules run at their real settings in seconds. Tests settle (`/settle`) before reading results, and poll with a wall-time bound for work such as container cold starts.

## Tests

One file per service, so a fixer can own one service at a time; the platform features have a file each.

| File | Covers |
|---|---|
| `sns_test.go` | filter policies in both scopes, the SQS envelope, subscription attributes |
| `sqs_test.go` | partial batch failures redriven to the DLQ after maxReceiveCount, redrive policy attributes |
| `lambda_test.go` | the three packages, mapping settings, the SQS event record, batching window and batch size, the container image, the function URL (payload 2.0) |
| `dynamodb_test.go` | the conditional lock, TTL expiry on the emulator clock |
| `s3_test.go` | SSE-KMS by bucket default, ListObjectsV2 with a delimiter |
| `kms_test.go` | alias, Encrypt and Decrypt, a disabled key blocking SSE-KMS reads |
| `secretsmanager_test.go` | workers read AWSCURRENT, PutSecretValue moves the stages |
| `logs_test.go` | Lambda logs with JSON filter patterns, the per-message stream |
| `events_test.go` | a `rate(5 minutes)` rule invoking the image worker |
| `journal_test.go` | causal links from Publish to the function's downstream calls, and for every message of a batch |
| `faults_test.go` | throttling the function URL and the mapping's invoke |
| `snapshot_test.go` | snapshot, reset, restore, and the pipeline running again |

`harness_test.go` holds the connection, clients, wait helpers and `requireDynamoDB`; `pipeline_test.go` builds the artifacts and provisions the pipeline. A test that needs the pipeline fails with the provisioning error when it could not be built. If the snapshot test fails, it resets and provisions the pipeline again so the tests after it still run.
