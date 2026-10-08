# understudy

understudy is a local AWS emulator: a single Go binary that stands in for AWS on a developer machine and in CI. Applications, the AWS SDKs, the AWS CLI and Terraform reach it through the standard `AWS_ENDPOINT_URL` override with no code changes. It aims for fidelity on a small set of services (SQS, SNS, S3, DynamoDB, Lambda, CloudWatch Logs, Secrets Manager, KMS, IAM, STS, EventBridge and CloudWatch metrics) rather than shallow coverage of many, and adds a controllable clock, a causal event journal, fault injection and snapshots.

understudy is pre-release. The repository currently holds the project scaffold and the package skeleton; no service is implemented yet, and nothing has been published as a release, a package or an image.

To check a change, install Go 1.27, [golangci-lint](https://golangci-lint.run) 2.13 and [just](https://just.systems), then run `just verify`. It checks formatting, runs `go vet` and `golangci-lint`, and runs the tests with the race detector. `just --list` shows the other recipes. The design lives in `docs/design/ARCHITECTURE.md` and the decisions behind it in `docs/adr/`.
