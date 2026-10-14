# python-boto3 conformance suite

boto3 and pytest tests that check understudy from the outside. Each test asserts AWS behavior as documented in `docs/research/phase1/`, not understudy's current output. A failure means understudy differs from AWS, or the research is silent and the test needs a decision.

## Run

```sh
CONFORMANCE_DYNAMODB=0 conformance/python-boto3/run.sh               # both environments, DynamoDB skipped
CONFORMANCE_DYNAMODB=0 conformance/python-boto3/run.sh -k sqs -x     # extra arguments go to pytest
SUITE_GROUPS=modern CONFORMANCE_DYNAMODB=0 conformance/python-boto3/run.sh
UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1 conformance/python-boto3/run.sh  # DynamoDB too
```

`run.sh` serves with `--sigv4 verify` by default (`SIGV4=verify`), so every request's signature is checked and the signature tests run. `SIGV4=parse` serves without verification and skips them.

`run.sh` builds understudy into `.build/conformance/python-boto3/understudy`, serves it on `127.0.0.1:45662` with a fresh data directory, waits for the health endpoint, runs pytest through `scripts/sealed`, then stops the server and runs `understudy cleanup` on its data directory. It keeps each environment's pytest output in the work directory as `test-<group>.log` and ends with a summary on stderr, from pytest's short summary: `run.sh: passed=N failed=N skipped=N` over both environments, then a `run.sh: SKIP <group> <file>:<line>: <reason>` line per skipped test. It exits non-zero when any environment fails. On failure it keeps the work directory (server log, data and test output) and prints its path.

The DynamoDB tests need the DynamoDB Local container, whose licence is yours to accept, so `run.sh` exits 2 before building unless exactly one of these is set:

- `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1` accepts the Amazon DynamoDB Local License Agreement for the run; serve reads it from its environment. `run.sh` exits 1 unless the health document then reports DynamoDB as `enabled`.
- `CONFORMANCE_DYNAMODB=0` skips the tests marked `dynamodb` (`tests/conftest.py`) with the reason `DynamoDB opted out (CONFORMANCE_DYNAMODB=0)`, and DynamoDB Local never starts.

Docker is required for the DynamoDB and Lambda tests.

To run against a server you started yourself:

```sh
scripts/sealed --endpoint http://127.0.0.1:<port> uv run --frozen pytest tests/test_sqs.py
```

The suite refuses to start unless `AWS_ENDPOINT_URL` is a loopback address.

## Environments

Two dependency groups in `pyproject.toml`, resolved into one `uv.lock`:

| Group | boto3 | Runs | SQS wire protocol |
|---|---|---|---|
| `modern` (default) | current | every test file | JSON |
| `legacy-query` | 1.34.89 | `tests/test_sqs.py` | Query |

1.34.89 is the last botocore that speaks the Query protocol to SQS (`sqs.md` 2.2). It installs and runs on Python 3.14. The pytest header prints the protocol in use.

## Layout

One file per service, so a fixer can own one service at a time.

| File | Covers |
|---|---|
| `tests/test_s3.py` | `upload_fileobj` and `download_fileobj` with multipart, `list_objects_v2` paginators, presigned URLs, checksums, conditional requests, addressing styles |
| `tests/test_sqs.py` | send, receive and delete batches, visibility on the emulator clock, FIFO, DLQ and redrive, attribute MD5 |
| `tests/test_sns.py` | publish and batch, filter policies in both scopes, raw and enveloped delivery |
| `tests/test_dynamodb.py` | conditional writes (lock-table pattern), transactions, queries, TTL on the emulator clock |
| `tests/test_secretsmanager.py` | versions and stages, deletion window, restore |
| `tests/test_kms.py` | keys, aliases, rotation, encrypt, decrypt, data keys |
| `tests/test_logs.py` | `filter_log_events` pattern syntax, `get_log_events`, retention |
| `tests/test_lambda.py` | invoke, log tail, SQS event source mappings, function URLs |
| `tests/test_sts.py` | `get_caller_identity`, `assume_role` |

`tests/conftest.py` holds the clients and cleanup fixtures and the `dynamodb` marker; `tests/support.py` holds the admin API client (clock, settle, reset) and shared helpers. Tests that need time to pass move the emulator clock through the admin API rather than sleeping.

Tests marked `docker` need the Docker daemon.
