# understudy

A local AWS emulator for development machines and CI, built for testing event-driven systems.

understudy is one Go binary, or a Docker image of about 11 MB compressed, that stands in for eighteen AWS services. Applications, the AWS SDKs, the AWS CLI and Terraform reach it through the standard `AWS_ENDPOINT_URL` override, with no code changes.

1. `understudy serve` starts the emulator on `127.0.0.1:4566`.
2. `eval "$(understudy env)"` points every SDK, the AWS CLI and Terraform at it.
3. `understudy clock advance 15m` moves time; `understudy journal` shows every call and delivery that followed.

The result: tests keep real AWS settings (visibility timeouts, delays, TTLs, schedules) and finish in milliseconds, without an account, a token or network access.

> [!NOTE]
> Pre-1.0: a minor version may break compatibility ([CHANGELOG.md](CHANGELOG.md)). Coverage is partial and listed operation by operation in [docs/coverage.md](docs/coverage.md).

## Why understudy

- **A controllable clock.** Everything AWS times (visibility timeouts, delays, TTL, schedules, retention) runs on one emulator clock you can freeze, advance or scale. [Guide](docs/guide/clock.md).
- **A causal journal.** Every call, delivery, Lambda invocation and timer is recorded with its cause, so `SNS Publish -> SQS SendMessage -> Lambda Invoke` is one tree, in the CLI or the [web UI](docs/guide/ui.md). [Guide](docs/guide/journal.md).
- **Nothing silently faked.** Shapes, error codes and validation are generated from AWS's Smithy models. An unimplemented operation answers `501 NotImplemented` and names itself; a setting stored without effect is marked as such.
- **Fault injection.** Rules inject AWS's own errors, throttling or latency by service, operation or resource. [Guide](docs/guide/faults.md).
- **Real Lambda execution.** Functions run in AWS's Lambda base images through Docker, with hot reload, and their SDK calls route back to understudy. [Guide](docs/guide/lambda.md).
- **Least-privilege policies.** understudy records the IAM actions each principal used and prints the policy they imply; `--iam-enforce enforce` makes it deny as AWS would. [Guide](docs/guide/policy.md).
- **Snapshots.** State saves to a deterministic archive you can commit, diff and load. [Guide](docs/guide/snapshots.md).

It is a 44.5 MiB binary that is ready in 17 ms and idles at 41 MiB. These are the `budgets` CI job's measurements on a GitHub-hosted Linux runner (`just budgets`), from the commit of v0.1.2; one run each, so expect them to vary. It sends no telemetry; its only network access is pulling Docker images for Lambda and DynamoDB Local.

## Prerequisites

- [Docker Engine](https://docs.docker.com/engine/) -- only for Lambda and DynamoDB. Everything else runs without it. `understudy doctor` checks what this machine provides.
- [Go](https://go.dev/dl/) 1.27 -- only to build from source.
- [just](https://just.systems) -- optional; runs the repository's recipes, such as `just image`.

## Install

Binaries are built for linux and darwin on amd64 and arm64. Each channel follows the [releases](https://github.com/sustinbebustin/understudy/releases); `understudy version` prints what you have.

### Homebrew (macOS and Linux)

```sh
brew install --cask sustinbebustin/tap/understudy
```

### Binaries

Each release has a `tar.gz` per platform and `checksums.txt`:

```sh
gh release download --repo sustinbebustin/understudy --pattern '*_linux_amd64.tar.gz' --pattern checksums.txt
sha256sum --check --ignore-missing checksums.txt
tar -xzf understudy_*_linux_amd64.tar.gz understudy
```

The darwin binaries are not notarized. Homebrew clears the quarantine flag; for a download by hand, run `xattr -d com.apple.quarantine ./understudy`.

### Go

```sh
go install github.com/sustinbebustin/understudy/cmd/understudy@latest
```

### Docker

```sh
docker run --rm -p 127.0.0.1:4566:4566 ghcr.io/sustinbebustin/understudy
```

The image is tagged with each version and `latest`, for linux/amd64 and linux/arm64. Inside the container understudy listens on `0.0.0.0:4566` without an admin token, so publishing the port on `127.0.0.1` only is what keeps it private. To keep state across restarts, mount a volume on `/data` and set `UNDERSTUDY_PERSIST=/data`.

To run Lambda functions from the container on Linux, mount the Docker socket and give the container user the socket's group:

```sh
docker run --rm -p 127.0.0.1:4566:4566 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  --group-add "$(stat -c %g /var/run/docker.sock)" \
  ghcr.io/sustinbebustin/understudy
```

On Docker Desktop the socket is owned by `root:root`, so use `--group-add 0`. Mounting the socket grants root on the host to whoever controls understudy; see [SECURITY.md](SECURITY.md). For a remote or docker:dind daemon, see the [CI guide](docs/guide/ci.md#docker-in-docker).

### Test suites

Go, Python and Node suites can start the image themselves with the [Testcontainers modules](docs/guide/testcontainers-modules.md) in [testcontainers/](testcontainers/). Go tests can also run understudy in process, without a container, with [`understudytest`](docs/guide/sdks.md#go-tests-with-understudytest).

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

   `understudy env` prints `AWS_ENDPOINT_URL`, dummy credentials and a region.

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

   Or open the `ui` URL from `serve`'s banner to watch the same events as causal trees.

[Getting started](docs/guide/getting-started.md) continues with accounts, regions and resetting state.

## Services

Counts are from [docs/coverage.md](docs/coverage.md), which lists every operation. "Stored only" operations accept, store and return their input with no runtime effect; "unbound" ones answer 501.

| Service | Operations | Full | Stored only | Partial | Unbound |
|---|---|---|---|---|---|
| SQS | 23 | 15 | 0 | 8 | 0 |
| SNS | 42 | 12 | 0 | 7 | 23 |
| S3 | 112 | 22 | 39 | 25 | 26 |
| DynamoDB (with Streams) | 62 | 6 | 2 | 23 | 31 |
| Kinesis Data Streams | 45 | 0 | 2 | 19 | 24 |
| Lambda | 88 | 30 | 2 | 18 | 38 |
| CloudWatch Logs | 118 | 9 | 0 | 9 | 100 |
| Secrets Manager | 23 | 1 | 0 | 14 | 8 |
| KMS | 54 | 16 | 2 | 9 | 27 |
| IAM | 180 | 10 | 1 | 53 | 116 |
| STS | 11 | 1 | 0 | 3 | 7 |
| EventBridge | 57 | 6 | 1 | 15 | 35 |
| EventBridge Scheduler | 12 | 0 | 0 | 12 | 0 |
| CloudWatch | 55 | 4 | 0 | 11 | 40 |
| API Gateway (HTTP and REST APIs) | 227 | 0 | 2 | 68 | 157 |
| SSM (Parameter Store) | 152 | 0 | 0 | 13 | 139 |
| Step Functions | 37 | 7 | 0 | 9 | 21 |
| SES (v1 and v2) | 187 | 20 | 8 | 14 | 145 |

The implemented operations are the ones an event-driven pipeline uses: API Gateway in front of Lambda, SNS fan-out to SQS, queues and DynamoDB or Kinesis streams feeding Lambda, Step Functions workflows, EventBridge rules and schedules, and the IAM, KMS, S3, secrets, parameters and logs around them. Each service has a page in the [guide](docs/guide/README.md).

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
| `understudy ses` | List the messages SES recorded instead of sending them |
| `understudy lambda hot-reload` | Run a function's code from a host directory, reloaded on every change |
| `understudy images pull` | Pull the pinned Lambda runtime images now, as a CI warm-up |
| `understudy coverage` | Report protocol compliance, operation coverage and evidence |
| `understudy doctor` | Check Docker, networking, `*.localhost`, binfmt and hot-reload paths |
| `understudy cleanup` | Remove containers and networks a crashed run left behind |
| `understudy version` | Print the version |

`understudy <command> --help` lists every flag. Exit codes: `0` success, `1` failed, `2` usage error, `3` not reachable, `4` condition not met (such as `health --wait` timing out), `5` admin token refused.

Settings come from flags, then `UNDERSTUDY_*` variables, then `understudy.json`; [docs/configuration.md](docs/configuration.md) lists every one.

## DynamoDB and its licence

DynamoDB is served by Amazon DynamoDB Local, run in Docker and pulled on the first DynamoDB call. It runs under the [Amazon DynamoDB Local License Agreement](https://aws.amazon.com/dynamodb/dynamodblocallicense/), not Apache-2.0. That licence requires an AWS account in good standing, which makes DynamoDB the one service that needs an AWS account.

To turn it off, set `"dynamodb": {"enabled": false}` in `understudy.json`; DynamoDB calls then answer 501 and DynamoDB Local is never pulled. understudy never bundles, mirrors, re-tags, saves, pushes or patches the image. Do not run understudy as a multi-user service with DynamoDB Local behind it.

## Choosing an emulator

- **understudy** when your system lives in the eighteen services above and you want fast, deterministic, free tests of how they interact, including time, failures and permissions.
- **LocalStack** when you need services understudy does not emulate, such as ECS, RDS, Cognito, CloudFormation deploys, Step Functions Express workflows or API Gateway WebSocket APIs.
- **moto** for Python unit tests that should not leave the process.
- **Real AWS** for final validation. Emulators can pass where the cloud fails, and that applies to understudy too.

## Limitations

Every difference from AWS a test can observe is listed by service in [docs/guide/differences.md](docs/guide/differences.md). The ones to know before you start:

- **Access control is off by default.** IAM, resource and key policies are stored and recorded but deny nothing. `--iam-enforce advise` journals each denial AWS would issue and `enforce` answers with it; the [policy guide](docs/guide/policy.md#what-is-not-enforced) lists what is not evaluated even then.
- **Signatures are parsed, not verified**, unless you start with `--sigv4 verify`. Even then every client may sign with the public dummy pair `test`/`test`, so a signature is not access control.
- **Lambda function code can reach the admin API** in some setups, where it can reset state, change the clock and read the journal. [SECURITY.md](SECURITY.md) says which, and the mitigations.
- **Lambda needs a Docker Engine** and is CI-tested on Linux only. Rootless Docker and Podman are not supported; Windows works through WSL2. See the [Lambda guide](docs/guide/lambda.md#limitations).
- **SES sends no mail.** An accepted send is recorded for `understudy ses messages`, and no delivery, bounce or complaint event follows.
- **Snapshots use format version 0**: archives from one build may be refused by another.

## Documentation

- [Guide](docs/guide/README.md) -- task-oriented pages for each service and feature, SDKs, Terraform, CI and Testcontainers.
- [Configuration reference](docs/configuration.md) -- every flag, variable and file key.
- [Coverage](docs/coverage.md) -- every operation, fidelity switch and evidence item.
- [Architecture](docs/ARCHITECTURE.md) and [ADRs](docs/adr/) -- the design and its decisions.
- [Examples](examples/) -- runnable projects: a boto3 worker, SNS -> SQS -> Lambda with Terraform, Docker Compose, GitHub Actions.

## Development

```sh
just verify       # gofmt check, vet, golangci-lint, go test -race
just test-docker  # Lambda, DynamoDB Local, container networking
```

[CONTRIBUTING.md](CONTRIBUTING.md) covers setup, the rules every change follows, and adding an operation or a service. Report vulnerabilities as [SECURITY.md](SECURITY.md) describes, not in public issues.

## License

Apache-2.0; see [LICENSE](LICENSE) and [NOTICE](NOTICE). The AWS managed policy catalogue embedded from [third_party/aws-managed-policies](third_party/aws-managed-policies) is AWS documentation under its own terms: MIT-0 for the policy documents and CC BY-SA 4.0 for the other fields. DynamoDB Local and the Lambda base images are pulled at runtime under their own terms and are not part of understudy.
