# AWS CLI conformance suite

Smoke tests that drive understudy with the AWS CLI v2 and assert what AWS documents. The arbiter is `docs/research/`, never understudy's behavior: a failing test is either an understudy gap or a wrong reading of the research, and either is worth a look.

## Run it

```sh
conformance/aws-cli/run.sh                                 # every service
CONFORMANCE_DYNAMODB=0 conformance/aws-cli/run.sh          # every service but DynamoDB
conformance/aws-cli/run.sh sqs sns                         # only these services (test file names)
UE_ONLY='dlq|redrive' conformance/aws-cli/run.sh sqs       # only tests whose name matches
```

`run.sh` builds understudy into `.build/conformance/aws-cli/understudy`, starts it on `127.0.0.1:45664` with a fresh data directory, waits for `/_understudy/v1/health`, runs the suite through `scripts/sealed`, then stops the server and removes the containers it labelled. It ends with a summary on stderr, `run.sh: passed=N failed=N skipped=N` and a `run.sh: SKIP <service>/<test>: <reason>` line per skip, read from `results.tsv`. It exits non-zero when any test fails, or when the build or the server start fails. On failure it keeps the work directory (server log, data, one log per test) and prints its path.

Needs `aws`, `jq`, `curl`, `python3` and Go on `PATH`. Before building, `run.sh` runs `aws --version` through `scripts/sealed` and exits 2, naming the binary and its exit status, when the first `aws` on `PATH` cannot run. Tests that run Lambda code also need Docker and the `public.ecr.aws/lambda/python:3.13` image; they skip when the emulator reports Docker as not enabled.

DynamoDB runs on DynamoDB Local, pulled on first use, and `run.sh` exits 1 unless DynamoDB reports `enabled` after startup. `CONFORMANCE_DYNAMODB=0` skips every DynamoDB test with the reason `DynamoDB opted out (CONFORMANCE_DYNAMODB=0)`; any other value exits 2 before building.

## Layout

| Path | Purpose |
|---|---|
| `run.sh` | build, serve, run, always clean up |
| `run-suite.sh` | the runner: executes every `test_*` function in `tests/*.sh` in its own shell with a timeout, prints PASS, FAIL or SKIP and records each in `results.tsv` (a skip with its reason) |
| `lib.sh` | assertions (`assert_eq`, `assert_jq`, `aws_error`, ...) and probes (`require_service`, which also honours `CONFORMANCE_DYNAMODB=0`, and `require_docker`) |
| `lambda-helpers.sh` | function fixtures shared by `tests/lambda.sh` and `tests/events.sh` |
| `tests/<service>.sh` | one file per service: `cloudwatch`, `dynamodb`, `events`, `iam`, `kms`, `lambda`, `logs`, `s3`, `secretsmanager`, `sns`, `sqs`, `sts` |

A fixer owns one service by owning one file in `tests/`.

## Writing a test

A test is a function named `test_<behavior>` in `tests/<service>.sh`. The runner sources `lib.sh` and the file, then runs the function under `set -eEuo pipefail`, so the first failed command or assertion ends that test and the rest carry on.

- Create resources under names from `new_name PREFIX` so a run never collides with an earlier one.
- Call the CLI through `aws_ok ...` (must succeed) or `aws_error CODE STATUS ...` (must fail with that error code and HTTP status). Both keep the last call's stdout in `AWS_OUT`, so `assert_jq FILTER EXPECTED` works on it. On failure the test prints the command, exit status, HTTP status, error line and body.
- Cite the research file and section in the file header or the comment above a non-obvious expectation.
- `skip "reason"` ends a test as skipped; use it only for an environment that cannot run the test, never to hide a failure.
