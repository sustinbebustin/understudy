# Changelog

Notable changes to understudy, in the form of [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions follow [Semantic Versioning](https://semver.org/); before 1.0 a minor version may break compatibility. A release's GitHub notes are its section here: `release.yml` refuses a tag without one.

## [Unreleased]

### Added

- The journal records SQS moves between queues as `redrive` events: a receive that moves messages past a queue's `maxReceiveCount` to its dead-letter queue, and each step of a message move task. Each names both queues, the moved message ids and why, and is a child of the receive or the move task's timer, so `understudy journal --kind redrive` and traces show how a message reached a dead-letter queue. The CLI and the web UI summarize them.

### Changed

- `understudy journal --limit N` fetches the newest events in one request, where it paged through every match. The notice for left-out events no longer gives the total: it reads `showing the newest N matching events; ...`, where it read `showing the newest N of M matching events; ...`.
- Each release pins the Lambda runtime images to the digests public.ecr.aws serves when the release is prepared, and tests them before the tag. Releases 0.1.0 to 0.1.2 shipped the pins of 2026-10-14.
- `scripts/sealed` writes `provider_installation { direct {} }` into Terraform's sealed CLI config, so providers come only from the registry through the plugin cache and a provider in `~/.terraform.d/plugins` can no longer stand in for one.

### Removed

- The `s3.maxObjectBytes` and `dynamodb.registry` configuration keys. `serve` parsed them and then refused to start, because nothing applied either. A file that names them now fails as an unknown key, naming it. S3 already enforces AWS's 5 GiB single-PUT limit, and DynamoDB Local already falls back from Docker Hub to the ECR Public mirror when a pull fails.

### Fixed

- `understudy version` names the version of a binary installed with `go install github.com/sustinbebustin/understudy/cmd/understudy@vX.Y.Z`, where it said `dev`. Without the release's stamp, the version is the module version the go command records, or the git version of a build in a checkout.
- Under `--iam-enforce enforce`, a denied Kinesis call carries its message under `Message`, as AWS does, where it said `message`.
- The Go `admin.Client` no longer tells a caller to start a server that is running. A call whose context ended, such as an `understudytest` `Settle` in a test that timed out, fails with an error naming the call that wraps the context's error, so `errors.Is` sees `context.Canceled` or `context.DeadlineExceeded`. A call the server took and did not answer within the HTTP client's response bound is the new `*admin.NoAnswerError`, exit code 3. Only a failure to reach a server is still `*admin.UnreachableError`.
- Under `--persist`, a periodic checkpoint saves a clock freeze, set or scale, a seeded id draw and a journaled event, where it waited for a state change or shutdown. After a crash, a seeded run no longer draws ids it already drew, and the journal no longer reissues event ids already in `journal.file`. Any API traffic now causes one write per `--persist-every`; an idle server still writes nothing.
- A request-phase latency fault on one delivery's target call, such as SQS `SendMessage` on one queue, delays that delivery alone. Every delivery due after it waited too, so an SNS fan-out to N queues under such a rule took N times the latency.

## [0.1.2] - 2026-11-09

The first complete release.

### Fixed

- The Homebrew cask and the Testcontainers modules are published: `brew install --cask sustinbebustin/tap/understudy`, the Go module `github.com/sustinbebustin/understudy/testcontainers/go`, and `testcontainers-understudy` on PyPI and npm. Use 0.1.2 or later for any of them: 0.1.0 and 0.1.1 have binaries and images only.

## [0.1.1] - 2026-11-09

### Fixed

- A release pipeline fix meant to publish the Homebrew cask, which did not. The binaries and the image are 0.1.0's with a new version; 0.1.1 has no cask and no Testcontainers modules.

## [0.1.0] - 2026-11-09

The release stopped before its Homebrew cask and Testcontainers modules, which are available from 0.1.2.

### Added

- First release: one binary and a multi-arch image (`ghcr.io/sustinbebustin/understudy`, linux/amd64 and linux/arm64) that stand in for eighteen AWS services through `AWS_ENDPOINT_URL`: SQS, SNS, S3, DynamoDB and DynamoDB Streams, Kinesis Data Streams, Lambda, CloudWatch Logs, Secrets Manager, KMS, IAM, STS, EventBridge, EventBridge Scheduler, CloudWatch, API Gateway (HTTP and REST APIs), SSM Parameter Store, Step Functions and SES. Coverage is partial and listed operation by operation in `docs/coverage.md`; an unimplemented operation answers 501.
- Binaries for linux and darwin on amd64 and arm64, and a Homebrew cask: `brew install --cask sustinbebustin/tap/understudy`.
- One emulator clock for every AWS timer, a causal journal of every call and delivery with a web UI, fault injection, snapshots and opt-in persistence.
- Lambda functions run in AWS's base images through Docker, with DynamoDB Local behind DynamoDB. Docker Desktop on macOS is supported, not CI-tested.
- Testcontainers modules for Go (`github.com/sustinbebustin/understudy/testcontainers/go`), Python and Node (`testcontainers-understudy` on PyPI and npm), versioned with understudy, whose default image is the release's.
- `understudy iam policy` prints the least-privilege policy the recorded calls imply, and `--iam-enforce advise|enforce` evaluates identity, resource, trust and key policies.
