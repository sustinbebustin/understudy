# understudy

understudy is a local AWS emulator: one Go binary, or a Docker image of about 11 MB compressed, that stands in for AWS on a developer machine and in CI. Applications, the AWS SDKs, the AWS CLI and Terraform reach it through the standard `AWS_ENDPOINT_URL` override, with no code changes.

It covers twelve services (SQS, SNS, S3, DynamoDB, Lambda, CloudWatch Logs, Secrets Manager, KMS, IAM, STS, EventBridge and CloudWatch metrics) and tries to get them right rather than cover many. Around them it adds what tests of event-driven systems need: a clock you control, a causal journal of every call and delivery, fault injection, snapshots, and least-privilege policy generation.

## Status

Pre-release. Nothing is published yet: no release, package or image. You build it from a clone.

Coverage is partial and listed operation by operation in [docs/coverage.md](docs/coverage.md). No behavior has been recorded against real AWS yet: the evidence behind it is AWS's documentation, recordings published by other projects, SDK source, and inference, and the coverage page labels each item.

## What sets it apart

- **Fidelity is generated, then checked from outside.** Request and response shapes, error codes and input validation come from AWS's own Smithy models. The protocol layer runs against Smithy's official protocol compliance tests. Behavior is checked with the Go v2, boto3 and JS v3 SDKs, the AWS CLI and Terraform.
- **Nothing is silently faked.** An operation understudy does not implement answers `501 NotImplemented` in AWS's error format and names itself. Settings that are stored but have no effect are listed as such.
- **A controllable clock.** Visibility timeouts, delays, retention, TTL, batching windows, schedules, secret deletion windows and log retention all run on one emulator clock. Freeze it, advance it an hour, or run it 60 times faster. Tests keep real AWS settings and finish in milliseconds.
- **A causal journal.** Every API call, delivery, Lambda invocation and timer is recorded with its cause. `SNS Publish -> SQS SendMessage -> Lambda Invoke -> the function's own calls` is one tree you can query or stream.
- **Fault injection.** Rules by service, operation, resource, account or region inject AWS's own errors, throttling or latency, for the next N calls or with a seeded probability.
- **Snapshots and persistence.** The whole state saves to a deterministic archive you can commit, diff and load. Persistence across restarts is opt-in.
- **Real Lambda execution.** Functions run in AWS's Lambda base images through Docker: zip and container-image functions, warm reuse, hot reload from a directory, SDK calls inside functions routed back to understudy, logs in CloudWatch Logs.
- **Least-privilege policies.** understudy records which IAM actions each principal used, on which resources, and prints a policy built from that record, plus the resource-policy statements its deliveries would need on AWS.
- **Small, fast, offline.** No account, token or telemetry (DynamoDB is the exception; see below). The binary is 39 MiB, ready in about 20 ms and idles at about 34 MiB (`just budgets` on Linux). The only network access is pulling Docker images for Lambda and DynamoDB Local on first use.

## Quick start

With Go 1.27, from a clone of this repository:

```sh
go install ./cmd/understudy
understudy serve
```

`serve` listens on `127.0.0.1:4566` and keeps its state in memory. In another shell, point your tools at it:

```sh
eval "$(understudy env)"
understudy health
aws sqs create-queue --queue-name demo
aws sqs send-message --queue-url http://localhost:4566/000000000000/demo --message-body hello
aws sqs receive-message --queue-url http://localhost:4566/000000000000/demo
```

`understudy env` prints `AWS_ENDPOINT_URL`, dummy credentials and a region, plus a comment for each client on S3 addressing, which no variable carries: at the `127.0.0.1` endpoint it prints, the Go and JS SDKs use path style by themselves, and Terraform needs `skip_requesting_account_id`. Now move time and look at what happened:

```sh
understudy clock freeze
aws sqs send-message --queue-url http://localhost:4566/000000000000/demo --message-body later --delay-seconds 900
understudy clock advance 15m
aws sqs receive-message --queue-url http://localhost:4566/000000000000/demo --max-number-of-messages 10
understudy journal --service sqs
```

The receive returns both messages at once: `hello`, whose 30-second visibility timeout ran out during the advance, and `later`, whose 15-minute delay did. Nothing waited.

### Docker

Build the image and publish its port on loopback only:

```sh
just image
docker run --rm -p 127.0.0.1:4566:4566 understudy:dev
```

The image is about 11 MB compressed, measured on linux/amd64 with `docker save understudy:dev | gzip | wc -c`. The release workflow, which runs on a version tag, builds the same image for linux/amd64 and linux/arm64 from the release binaries and publishes nothing; it does not measure the image.

To run Lambda functions from the container, mount the Docker socket and give the container user the socket's group (Linux):

```sh
docker run --rm -p 127.0.0.1:4566:4566 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  --group-add "$(stat -c %g /var/run/docker.sock)" \
  understudy:dev
```

Inside the container understudy listens on `0.0.0.0:4566` without an admin token, so the published port is what keeps it private. Mount a volume on `/data` and set `UNDERSTUDY_PERSIST=/data` to keep state across restarts.

### DynamoDB

DynamoDB is served by Amazon DynamoDB Local, run in Docker and pulled on first use. understudy logs a one-line notice when it first starts. It runs under the [Amazon DynamoDB Local License Agreement](https://aws.amazon.com/dynamodb/dynamodblocallicense/), not Apache-2.0. That licence requires an AWS account in good standing, which makes DynamoDB the one service that needs an AWS account. To turn DynamoDB off, set `"dynamodb": {"enabled": false}` in the configuration file; its calls then answer 501 and DynamoDB Local is never pulled.

understudy never bundles, mirrors, re-tags, saves, pushes or patches the DynamoDB Local image. It is a local tool for one user, not a hosted service: do not run understudy as a multi-user service with DynamoDB Local behind it.

## Service coverage

From [docs/coverage.md](docs/coverage.md), which `understudy coverage` regenerates. "Stored only" operations accept, store and return their input with no runtime effect; "unbound" ones answer 501.

| Service | Operations in the model | Full | Stored only | Partial | Unbound |
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

The implemented operations are the ones an event-driven pipeline uses: SNS fan-out with filter policies to SQS, SQS with dead-letter queues feeding Lambda, S3 with SSE-KMS, a DynamoDB lock table with TTL, Secrets Manager, CloudWatch Logs, EventBridge schedules and the IAM, KMS and CloudWatch resources Terraform declares around them.

## How it compares

The landscape as of October 2026, from [docs/research/localstack-vs-aws-tooling.md](docs/research/localstack-vs-aws-tooling.md), which cites its sources.

| | understudy | LocalStack for AWS | moto | AWS's own tooling |
|---|---|---|---|---|
| Licence and cost | Apache-2.0, free | Commercial use of current releases needs a paid plan (from $39 per user per month); the free Hobby plan is non-commercial; the open-source repository was archived in March 2026 | Apache-2.0, free | Free |
| Form | One binary or image; no account or token | Docker image; an auth token is required | Python library with in-process mocks, plus a standalone server | SAM CLI, the Lambda Runtime Interface Emulator, DynamoDB Local and a few single-purpose images |
| Services | 12 | About 100, depth varying by service | Many; see its implementation coverage page | No multi-service emulator; with SAM local, a function's calls to other services reach real AWS |
| Clock, journal, faults, snapshots, policy generation | All included | Persistence and Cloud Pods (Base plan and up), App Inspector (10 stored operations on Hobby, 1,000 on Base), chaos engineering (Ultimate add-on or Enterprise), IAM Policy Stream (Ultimate) | State-transition delays, a request recorder for replay, opt-in basic IAM checks | Fault Injection Service works on real resources only, billed per action-minute |
| IAM enforcement | No; access is recorded, not enforced | Yes, Base plan and up | Basic, opt-in | Real AWS only |

Choose by what you need:

- **LocalStack** when you need breadth (ECS, RDS, Step Functions, API Gateway, Cognito, CloudFormation and CDK deploys) or IAM enforcement, and its licence fits.
- **moto** for Python unit tests that should not leave the process, or for services understudy does not have.
- **AWS's own tooling and real accounts** for final validation. AWS documents that emulators can pass where the cloud fails, and that applies to understudy too.
- **understudy** when your system lives in the twelve services above and you want fast, deterministic, free tests of how they interact, including time, failures and permissions.

## Limitations

- Twelve services, with many operations unbound; see the coverage table.
- No behavior is recorded against real AWS yet; see [Status](#status).
- IAM policies, resource policies and key policies are stored, not enforced: nothing denies a call, and a delivery AWS would deny goes through with an `advisory` event in the journal. `iam policy` grants more than a principal needs in places; review it as the [policy guide](docs/guide/policy.md#review-before-use) describes.
- Request signatures are parsed, not verified, unless you start with `--sigv4 verify`; without it a presigned URL is accepted whatever its signature or expiry. Even under `--sigv4 verify` every client may sign with the public dummy pair `test`/`test`, so a signature is not access control.
- When understudy runs in a container or on Docker Desktop, Lambda function code can reach the whole admin API: it can reset state, change the clock and read the journal. In a container, `UNDERSTUDY_ADMIN_TOKEN` with `--allow-unauthenticated-aws-api` closes that. On Docker Desktop, `serve` refuses a token on the default loopback listener, so the exposure stays there. Listening beyond loopback with the token and `--allow-unauthenticated-aws-api` should close the admin API to functions, but opens the AWS API to every peer on your network, and is untested on Docker Desktop; see [SECURITY.md](SECURITY.md).
- SNS stores HTTP/S and email subscriptions and their confirmation flow, but sends them nothing; SMS, mobile push and Firehose subscriptions answer 501. Lambda destinations are stored and not delivered, and layers are refused. S3 has no SSE-C, Object Lock retention or MFA delete.
- DynamoDB runs on DynamoDB Local and inherits its documented differences from the service, such as no throughput limits and no parallel scan. understudy deletes an expired item as soon as the clock passes its TTL, where AWS takes up to a few days.
- DynamoDB does not work when understudy runs on the host under Docker Desktop yet: understudy reaches DynamoDB Local at its bridge network address, which only a Linux host can route.
- Lambda needs a Docker Engine. It is tested on Linux. Docker Desktop on macOS is supported by design but not yet tested; rootless Docker and rootless Podman are not supported; Windows works through WSL2 only. On Linux, functions of the other architecture need a binfmt handler, which `understudy doctor` checks for. Functions run against a Lambda Runtime API that understudy serves itself, not AWS's Runtime Interface Emulator ([ADR 0010](docs/adr/0010-lambda-hosted-runtime-api.md)).
- CloudWatch alarms are stored and never evaluated. `GetMetricStatistics` answers 501, and `GetMetricData` has no metric math.
- `terraform import` of an SQS queue fails on the queue URL form, and EventBridge has only the default bus (`CreateEventBus` answers 501).
- The snapshot format is version 0: archives from one build may be refused by another until the first release. The TLS listener's certificate is new at every start.

## Documentation

The [guide](docs/guide/README.md) covers getting started, configuration, each SDK and the CLI, Terraform, Lambda, the clock, fault injection, the journal, snapshots and persistence, policy generation and CI. [docs/configuration.md](docs/configuration.md) is the reference for every setting, [docs/coverage.md](docs/coverage.md) for every operation, and [docs/design/ARCHITECTURE.md](docs/design/ARCHITECTURE.md) with [docs/adr/](docs/adr/) for the design. Worked examples are in [examples/](examples/).

## Contributing and security

See [CONTRIBUTING.md](CONTRIBUTING.md) for how to add an operation or a service, and the test layers. Report vulnerabilities as [SECURITY.md](SECURITY.md) describes, not in public issues.

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE). DynamoDB Local and the Lambda base images are pulled at runtime under their own terms and are not part of understudy.
