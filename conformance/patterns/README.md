# Serverless-patterns conformance suite

Runs patterns from [aws-samples/serverless-patterns](https://github.com/aws-samples/serverless-patterns) against understudy as their READMEs say to run them against AWS. Each pattern is copied from commit `deb84af` into `conformance/patterns/<pattern>/` with its MIT-0 `LICENSE`, and changed only as its `ADJUSTMENTS.md` lists (runtime, architecture, profile, provider override), each change with its reason.

## Running

```sh
conformance/patterns/run.sh                          # every pattern
conformance/patterns/run.sh lambda-layer-terraform   # only these
```

`run.sh` builds understudy into `.build/conformance/patterns/understudy`, starts it on `127.0.0.1:45667` with a fresh temporary data directory and no `UNDERSTUDY_*` or `AWS_*` setting from your shell, and runs each pattern, in a copy of its directory under `.build/conformance/patterns/run/work/`, through:

1. `prepare`: the pattern's documented preparation, such as installing a layer's dependencies;
2. `init` and `apply`;
3. `check`: the pattern's documented test;
4. `plan-no-changes`: a plan that must show no changes;
5. `destroy`, then `nothing-left`: the state is empty and understudy lists nothing in the pattern's account but what AWS keeps after a destroy: the `/aws/lambda/<function>` log group of each function the pattern created, which Lambda makes when the function first runs and `DeleteFunction` leaves in place (`docs/research/lambda-api.md` 6.4, `docs/research/tf-compute.md` 5).

Each pattern runs in an account of its own. Every Terraform and AWS CLI call goes through `scripts/sealed`, so no real AWS setting or credential is used. The run prints one line per step and ends with a summary on stderr (`run.sh: passed=N failed=N skipped=N`, then a `run.sh: SKIP <pattern>/<step>: <reason>` line per skip). Logs are under `.build/conformance/patterns/run/logs/<pattern>/`.

Requirements: Go, Terraform 1.16 or later, the AWS CLI, Docker (patterns run Lambda functions), `uv`, `jq`, `curl` and GNU `timeout`. The first run downloads the providers from the Terraform registry and the pattern's dependencies from PyPI.

| Variable | Effect |
|---|---|
| `CONFORMANCE_PHASE_TIMEOUT` | Seconds before one Terraform command is interrupted (default 900). |
| `CONFORMANCE_PORT` | Port understudy listens on (default 45667). |
| `SIGV4` | serve's `--sigv4` mode (default `parse`). |
| `CONFORMANCE_IAM_ENFORCE` | serve's `--iam-enforce` mode (default `off`). Every pattern acts as its account's root, so under `enforce` only resource policies and deliveries can deny; the journal's errors are kept in `logs/journal-errors.json`. |

## Layout

| Path | Contents |
|---|---|
| `<pattern>/` | The pattern as copied, with `ADJUSTMENTS.md`. |
| `checks/<pattern>.sh` | The pattern's documented preparation and test, as `pattern_prepare DIR` and `pattern_check DIR`. |
| `run.sh` | Build, serve, run every step, always clean up. |

## Patterns

| Pattern | What it proves | Documented check |
|---|---|---|
| `apigw-http-api-lambda-terraform` | An HTTP API with a CORS configuration, a `$default` auto-deploy stage with access logs, a `$default` route and a Lambda `AWS_PROXY` integration in payload format 1.0 (`docs/research/apigateway-http.md` 5.2, 9) | A GET of `/` and a POST of `/pets/dog/1?foo=bar`, through `/_understudy/execute-api/<api-id>/`, answer the function's message. Fails today: the function reads `event["headers"]["X-Forwarded-For"]`, and understudy lowercases header names as the doc says, so the function raises `KeyError` and the call answers 500 (`docs/research/apigateway-http.md` 5.2, UNVERIFIED until APIGW2-08 is recorded). |
| `dynamodb-streams-lambda-terraform` | A table with `stream_enabled` and `NEW_AND_OLD_IMAGES`, and an `aws_lambda_event_source_mapping` at `LATEST` on its `stream_arn` (`docs/research/dynamodb-streams.md` 9.3, `docs/research/lambda-stream-sources.md` 3.2) | An item put after the apply reaches the function as an `INSERT` record carrying the item, which the function logs to its CloudWatch Logs group (`docs/research/dynamodb-streams.md` 7.8). |
| `kinesis-lambda-terraform` | A provisioned `aws_kinesis_stream` (`docs/research/kinesis.md` 10.3), a `nodejs22.x` function, an identity policy that grants the Kinesis read actions on the stream, and an `aws_lambda_event_source_mapping` at `LATEST` (`docs/research/lambda-stream-sources.md` 3.2) | Records of the generator's `Sensor1` template, put after the apply with `aws kinesis put-records` in place of the hosted Kinesis Data Generator (see its `ADJUSTMENTS.md`), reach the function, which logs each decoded payload to its CloudWatch Logs group (`docs/research/kinesis.md` 8.1). Under `CONFORMANCE_IAM_ENFORCE=enforce` the journal shows one refused `CreateEventSourceMapping`, which the provider retries: the mapping does not depend on the policy attachment, so it can be created before the role holds its policy. |
| `lambda-layer-terraform` | `aws_lambda_layer_version`, and a function using it beside one without it (`docs/research/lambda-destinations-layers.md` 6, 7, 8) | The function with the layer lists the layer's files under `/opt`; the one without fails with `Unable to import module 'app': No module named 'mysql'`. |
| `lambda-ssm-parameter-terraform` | An `aws_ssm_parameter` String on provider 5.100.0, and a `nodejs22.x` function calling `GetParameter` and `PutParameter` with `Overwrite` (`docs/research/ssm-parameters.md` 6.1, 10.4) | The GET event answers the parameter as created, version 1; the PUT event answers `{"Version": 2, "Tier": "Standard"}`; the GET event again answers the new value, version 2. The check then puts the configured value back, so `plan-no-changes` measures understudy rather than the PUT event's drift. |
| `sfn-lambda-function-crossaccount-terraform` | A Standard state machine whose `arn:aws:states:::lambda:invoke` Task calls a function in a second account, allowed by an `aws_lambda_permission` naming the execution role (`docs/research/stepfunctions-api.md` 9.2) | An execution started with `{"payload": "testing"}` succeeds with the function's answer as the result's `Payload`. |
