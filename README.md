# understudy

A local AWS emulator for development machines and CI, built for testing event-driven systems.

understudy is one Go binary, or a Docker image of about 11 MB compressed, that stands in for twelve AWS services. Applications, the AWS SDKs, the AWS CLI and Terraform reach it through the standard `AWS_ENDPOINT_URL` override, with no code changes.

1. `understudy serve` starts the emulator on `127.0.0.1:4566`.
2. `eval "$(understudy env)"` points every SDK, the AWS CLI and Terraform at it.
3. `understudy clock advance 15m` moves time; `understudy journal` shows every call and delivery that followed.

The result: tests keep real AWS settings (visibility timeouts, delays, TTLs, schedules) and finish in milliseconds, without an account, a token or network access.

> [!NOTE]
> Pre-release. No release, package or image is published; build from a clone. Coverage is partial and listed operation by operation in [docs/coverage.md](docs/coverage.md).

## Contents

- [Why understudy](#why-understudy)
- [Prerequisites](#prerequisites)
- [Install](#install)
- [Quick start](#quick-start)
- [Services](#services)
- [Commands](#commands)
- [DynamoDB and its licence](#dynamodb-and-its-licence)
- [Choosing an emulator](#choosing-an-emulator)
- [Limitations](#limitations)
- [Documentation](#documentation)

## Why understudy

- **Fidelity generated from AWS's models.** Request and response shapes, error codes and input validation come from AWS's Smithy models. The protocol layer runs against Smithy's official compliance tests, and behavior is checked with the Go v2, boto3 and JS v3 SDKs, the AWS CLI and Terraform.
- **Nothing silently faked.** An unimplemented operation answers `501 NotImplemented` in AWS's error format and names itself. Settings that are stored but have no effect are marked as such.
- **A controllable clock.** Visibility timeouts, delays, retention, TTL, batching windows, schedules, secret deletion windows and log retention all run on one emulator clock. Freeze it, advance it an hour, or run it 60 times faster.
- **A causal journal.** Every API call, delivery, Lambda invocation and timer is recorded with its cause, so `SNS Publish -> SQS SendMessage -> Lambda Invoke -> the function's own calls` is one tree you can query or follow.
- **Fault injection.** Rules by service, operation, resource, account or region inject AWS's own errors, throttling or latency, for the next N calls or with a seeded probability.
- **Snapshots and persistence.** State saves to a deterministic archive you can commit, diff and load. Persistence across restarts is opt-in.
- **Real Lambda execution.** Functions run in AWS's Lambda base images through Docker: zip and image functions, warm reuse, hot reload from a directory, SDK calls routed back to understudy, logs in CloudWatch Logs.
- **Least-privilege policies.** understudy records which IAM actions each principal used, on which resources, and prints the policy that record implies.
- **Small and offline.** A 39 MiB binary, ready in about 20 ms, idling at about 34 MiB (`just budgets` on Linux). No telemetry; the only network access is pulling Docker images for Lambda and DynamoDB Local.

## Prerequisites

- [Go](https://go.dev/dl/) 1.27 -- to build from source.
- [Docker Engine](https://docs.docker.com/engine/) -- only for Lambda and DynamoDB. Everything else runs without it. `understudy doctor` checks what this machine provides.
- [just](https://just.systems) -- optional; runs the repository's recipes, such as `just image`.

## Install

### From source

```sh
go install ./cmd/understudy
understudy version
```

This puts `understudy` in `$(go env GOPATH)/bin`.

### Docker

```sh
just image
docker run --rm -p 127.0.0.1:4566:4566 understudy:dev
```

Inside the container understudy listens on `0.0.0.0:4566` without an admin token, so publishing the port on `127.0.0.1` only is what keeps it private. To keep state across restarts, mount a volume on `/data` and set `UNDERSTUDY_PERSIST=/data`.

To run Lambda functions from the container on Linux, mount the Docker socket and give the container user the socket's group:

```sh
docker run --rm -p 127.0.0.1:4566:4566 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  --group-add "$(stat -c %g /var/run/docker.sock)" \
  understudy:dev
```

Mounting the socket grants root on the host to whoever controls understudy; see [SECURITY.md](SECURITY.md).

understudy finds Docker the way the Docker CLI does: `DOCKER_HOST`, else `/var/run/docker.sock`; `--docker` (`UNDERSTUDY_DOCKER`) overrides both. A `tcp://` daemon works too, such as a docker:dind service in CI: plain TCP by default, and TLS with the client certificates `ca.pem`, `cert.pem` and `key.pem` from `DOCKER_CERT_PATH` when `DOCKER_TLS_VERIFY` is set. On a daemon on another host, functions reach understudy at its address on the daemon's network, so understudy must run on that network; the [CI guide](docs/guide/ci.md#docker-in-docker) shows the setup. `understudy doctor` names the daemon, the transport and the detected topology.

## Quick start

1. Start the emulator. State is kept in memory until it stops.

   ```sh
   understudy serve
   ```

2. In another shell, point your tools at it and check it is ready.

   ```sh
   eval "$(understudy env)"
   understudy health
   ```

   `understudy env` prints `AWS_ENDPOINT_URL`, dummy credentials and a region, plus comments on settings no variable carries, such as Terraform's `skip_requesting_account_id`.

3. Send and receive a message.

   ```sh
   aws sqs create-queue --queue-name demo
   aws sqs send-message --queue-url http://localhost:4566/000000000000/demo --message-body hello
   aws sqs receive-message --queue-url http://localhost:4566/000000000000/demo
   ```

4. Freeze time, send a delayed message, and skip ahead.

   ```sh
   understudy clock freeze
   aws sqs send-message --queue-url http://localhost:4566/000000000000/demo --message-body later --delay-seconds 900
   understudy clock advance 15m
   aws sqs receive-message --queue-url http://localhost:4566/000000000000/demo --max-number-of-messages 10
   ```

   The receive returns both `hello`, whose 30-second visibility timeout ran out during the advance, and `later`, whose 15-minute delay did. Nothing waited.

5. See what happened.

   ```sh
   understudy journal --service sqs
   ```

[Getting started](docs/guide/getting-started.md) continues with accounts, regions and resetting state.

## Services

From [docs/coverage.md](docs/coverage.md), which `understudy coverage` regenerates. "Stored only" operations accept, store and return their input with no runtime effect; "unbound" ones answer 501.

| Service | Operations | Full | Stored only | Partial | Unbound |
|---|---|---|---|---|---|
| SQS | 23 | 21 | 2 | 0 | 0 |
| SNS | 42 | 12 | 2 | 5 | 23 |
| S3 | 112 | 14 | 47 | 21 | 30 |
| DynamoDB | 58 | 6 | 2 | 18 | 32 |
| Lambda | 88 | 25 | 2 | 14 | 47 |
| CloudWatch Logs | 118 | 9 | 0 | 9 | 100 |
| Secrets Manager | 23 | 12 | 1 | 2 | 8 |
| KMS | 54 | 19 | 3 | 5 | 27 |
| IAM | 180 | 42 | 6 | 16 | 116 |
| STS | 11 | 1 | 0 | 2 | 8 |
| EventBridge | 57 | 9 | 0 | 8 | 40 |
| CloudWatch | 55 | 1 | 2 | 9 | 43 |

The implemented operations are the ones an event-driven pipeline uses: SNS fan-out with filter policies to SQS, SQS with dead-letter queues feeding Lambda, S3 with SSE-KMS, a DynamoDB lock table with TTL, Secrets Manager, CloudWatch Logs, EventBridge schedules, and the IAM, KMS and CloudWatch resources Terraform declares around them.

Behavior the models do not fix rests on evidence that the coverage page labels item by item. Real AWS was recorded for the backlog R01 to R18 (`testdata/parity/`), so 45 of the 232 evidence items are `aws-recorded`; the rest come from AWS's documentation, recordings published by other projects, SDK source, and inference. Known differences from AWS are listed in [the differences page](docs/guide/differences.md).

## Commands

| Command | What it does |
|---|---|
| `understudy serve` | Run the emulator until interrupted |
| `understudy env` | Print the environment that points a client at understudy; `--sdk`, `--format`, `--account` |
| `understudy health` | Report readiness and each service's state; `--wait 30s` blocks until ready |
| `understudy settle` | Wait until no deliveries, invocations or polls are pending |
| `understudy clock` | `now`, `freeze`, `resume`, `advance`, `set`, `scale` |
| `understudy journal` | Query or `--follow` calls, deliveries, invocations, timers and faults |
| `understudy faults` | List, add and remove fault-injection rules |
| `understudy snapshot` | Save, load, inspect and compare state archives |
| `understudy reset` | Drop state: all of it, or named services, accounts and regions |
| `understudy resources` | List every resource by service, account and region |
| `understudy iam` | Show the actions principals used and the least-privilege policy they need |
| `understudy coverage` | Report protocol compliance, operation coverage and evidence |
| `understudy doctor` | Check Docker, networking, `*.localhost`, binfmt and hot-reload paths |
| `understudy cleanup` | Remove containers and networks a crashed run left behind |

`understudy <command> --help` lists every flag. Exit codes: `0` success, `1` failed, `2` usage error, `3` not reachable, `4` condition not met (such as `health --wait` timing out), `5` admin token refused.

Settings come from flags, then `UNDERSTUDY_*` variables, then `understudy.json`; [docs/configuration.md](docs/configuration.md) lists every one.

## DynamoDB and its licence

DynamoDB is served by Amazon DynamoDB Local, run in Docker and pulled on the first DynamoDB call. understudy logs a one-line notice naming its licence when it first starts DynamoDB Local: at `serve` when the image is already present, otherwise on that first call.

DynamoDB Local runs under the [Amazon DynamoDB Local License Agreement](https://aws.amazon.com/dynamodb/dynamodblocallicense/), not Apache-2.0. That licence requires an AWS account in good standing, which makes DynamoDB the one service that needs an AWS account. To turn it off, set `"dynamodb": {"enabled": false}` in `understudy.json`; DynamoDB calls then answer 501 and DynamoDB Local is never pulled.

understudy never bundles, mirrors, re-tags, saves, pushes or patches the DynamoDB Local image. Do not run understudy as a multi-user service with DynamoDB Local behind it.

## Choosing an emulator

- **LocalStack** when you need services understudy does not emulate, such as ECS, RDS, Step Functions, API Gateway, Cognito or CloudFormation deploys, or enforced IAM policies.
- **moto** for Python unit tests that should not leave the process.
- **Real AWS** for final validation. Emulators can pass where the cloud fails, and that applies to understudy too.
- **understudy** when your system lives in the twelve services above and you want fast, deterministic, free tests of how they interact, including time, failures and permissions.

## Limitations

Behavior that differs from AWS in ways a test can observe is listed in [docs/guide/differences.md](docs/guide/differences.md). The broad limits:

- **Access control is recorded, not enforced.** IAM, resource and key policies are stored; nothing denies a call, and a delivery AWS would deny goes through with an `advisory` journal event. `understudy iam policy` grants more than needed in places; review it as the [policy guide](docs/guide/policy.md#review-before-use) describes.
- **Signatures are parsed, not verified**, unless you start with `--sigv4 verify`. Even then every client may sign with the public dummy pair `test`/`test`, so a signature is not access control.
- **Lambda function code can reach the admin API** wherever functions call the main listener: when understudy runs in a container, on Docker Desktop, or when `--listen` already binds the address functions reach (`0.0.0.0`, or the bridge gateway). It can reset state, change the clock and read the journal. [SECURITY.md](SECURITY.md) describes the mitigations and where they stop.
- **Lambda needs a Docker Engine** and is tested on Linux. Docker Desktop on macOS is supported by design but untested; rootless Docker and Podman are not supported; Windows works through WSL2 only. Functions of the other architecture need a binfmt handler (`understudy doctor` checks). Functions run against understudy's own Lambda Runtime API, not AWS's Runtime Interface Emulator ([ADR 0010](docs/adr/0010-lambda-hosted-runtime-api.md)). Destinations are stored, not delivered, and layers are refused.
- **DynamoDB** inherits DynamoDB Local's documented differences, such as no throughput limits and no parallel scan. Expired items are deleted as soon as the clock passes their TTL, where AWS takes up to a few days.
- **SNS** stores HTTP/S and email subscriptions and their confirmation flow but sends them nothing; SMS, mobile push and Firehose subscriptions answer 501.
- **S3** has no SSE-C, Object Lock retention or MFA delete.
- **CloudWatch** alarms are stored and never evaluated. `GetMetricStatistics` answers 501, and `GetMetricData` has no metric math.
- **EventBridge** has only the default bus (`CreateEventBus` answers 501). `terraform import` of an SQS queue fails on the queue URL form.
- **Snapshots** use format version 0: archives from one build may be refused by another until the first release. The TLS listener's certificate is new at every start.

## Documentation

- [Guide](docs/guide/README.md) -- getting started, configuration, SDKs and the CLI, Terraform, Lambda, the clock, fault injection, the journal, snapshots, policy generation and CI.
- [Configuration reference](docs/configuration.md) -- every flag, variable and file key.
- [Coverage](docs/coverage.md) -- every operation, fidelity switch and evidence item.
- [Architecture](docs/design/ARCHITECTURE.md) and [ADRs](docs/adr/) -- the design and its decisions.
- [Examples](examples/) -- runnable projects: a boto3 worker, SNS -> SQS -> Lambda with Terraform, Docker Compose, GitHub Actions.

## Contributing and security

[CONTRIBUTING.md](CONTRIBUTING.md) covers setup, the rules every change follows, adding an operation or a service, and the test layers. Report vulnerabilities as [SECURITY.md](SECURITY.md) describes, not in public issues.

## License

Apache-2.0; see [LICENSE](LICENSE) and [NOTICE](NOTICE). DynamoDB Local and the Lambda base images are pulled at runtime under their own terms and are not part of understudy.
