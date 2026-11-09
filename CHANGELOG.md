# Changelog

Notable changes to understudy, in the form of [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions follow [Semantic Versioning](https://semver.org/); before 1.0 a minor version may break compatibility. A release's GitHub notes are its section here: `release.yml` refuses a tag without one.

## [Unreleased]

### Fixed

- The Homebrew cask and the Testcontainers modules are published. The 0.1.0 and 0.1.1 releases stopped before the cask, each on its tap token template, so they have binaries and images but no cask, and no module on PyPI, npm or the Go proxy.

## [0.1.1] - 2026-11-09

### Fixed

- A second attempt at publishing the Homebrew cask, which 0.1.0's release stopped before. It stopped at the same step, so 0.1.1 has no cask and no published Testcontainers modules either.

## [0.1.0] - 2026-11-09

### Added

- First release: one binary and a multi-arch image (`ghcr.io/sustinbebustin/understudy`, linux/amd64 and linux/arm64) that stand in for eighteen AWS services through `AWS_ENDPOINT_URL`: SQS, SNS, S3, DynamoDB and DynamoDB Streams, Kinesis Data Streams, Lambda, CloudWatch Logs, Secrets Manager, KMS, IAM, STS, EventBridge, EventBridge Scheduler, CloudWatch, API Gateway HTTP APIs, SSM Parameter Store, Step Functions and SES. Coverage is partial and listed operation by operation in `docs/coverage.md`; an unimplemented operation answers 501.
- Binaries for linux and darwin on amd64 and arm64, and a Homebrew cask: `brew install --cask sustinbebustin/tap/understudy`.
- One emulator clock for every AWS timer, a causal journal of every call and delivery with a web UI, fault injection, snapshots and opt-in persistence.
- Lambda functions run in AWS's base images through Docker, with DynamoDB Local behind DynamoDB. Docker Desktop on macOS is supported, not CI-tested.
- Testcontainers modules for Go (`github.com/sustinbebustin/understudy/testcontainers/go`), Python and Node (`testcontainers-understudy` on PyPI and npm), versioned with understudy, whose default image is the release's.
- `understudy iam policy` prints the least-privilege policy the recorded calls imply, and `--iam-enforce advise|enforce` evaluates identity, resource, trust and key policies.
