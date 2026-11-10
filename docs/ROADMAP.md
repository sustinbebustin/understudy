# Roadmap

Where understudy goes next, in priority order. Nothing here is scheduled.

The order follows from what understudy is for: fast, deterministic, free tests of how AWS services interact, including time, failures and permissions. Its bet is fidelity, so evidence for what it already does comes before new services, and depth in the services an event-driven pipeline uses comes before breadth. What exists today is in the [README](../README.md) and, operation by operation, on the [coverage page](coverage.md).

## Evidence

- **Replay more of the recordings.** `internal/parity` replays part of what is recorded under `testdata/parity/`. The rest needs replay scenarios, including the S3 and Lambda REST steps and the Docker steps the replayer skips.
- **Retire what the recordings settle.** Each `inferred` overlay entry and fidelity switch that a recording now answers becomes recorded behavior, and where the recording contradicts understudy, understudy changes. The coverage page lists them under "pending retirement".

## Permissions

- **A green least-privilege loop.** `conformance/iam-loop` runs each suite as a user holding only its generated policy. The S3 and Lambda groups still fail, with and without enforcement, where a test needs `*.localhost` to resolve.
- **The gaps in evaluation:** IAM groups, KMS grants, the policy simulator and S3 Block Public Access. The [policy guide](guide/policy.md#what-is-not-enforced) lists every one.

## Depth in the services that exist

- **API Gateway:** authorizers, REST request validators and models, Velocity mapping templates, and direct integrations with SQS and EventBridge.
- **Step Functions:** Task resources other than Lambda (SQS, SNS, DynamoDB, EventBridge, nested executions), JSONata and variables, Express workflows, task tokens and activities, Distributed Map, and EventBridge rules that start executions.
- **Stream sources for Lambda:** `ParallelizationFactor` above 1, `BisectBatchOnFunctionError`, tumbling windows, event filters and S3 on-failure destinations. For Kinesis itself: resharding, enhanced fan-out and encryption.
- **EventBridge Scheduler:** universal targets.
- **SES:** configuration sets and receipt rules.

## Platform

- SigV4A and signed streaming uploads from the SDKs other than Go.
- A write-ahead log for persistence, in place of whole-state checkpoints.
- A TLS certificate that is reused under `--persist`.
- An admin-token boundary between function containers and the admin API when understudy runs in a container.
- Wildcard DNS for function containers.

[ARCHITECTURE.md section 21](ARCHITECTURE.md#21-extension-points) names the seam each of these plugs into.

## Later, and why not sooner

- **ECS.** High demand and a large surface. Fargate `RunTask` and services could reuse the Lambda launcher and land logs in CloudWatch Logs, but a faithful ECS needs VPC, load balancers and service discovery to mean anything in a test, so most of it would be stored-only.
- **RDS.** The engine cannot be emulated. The realistic shape is a control plane over a Postgres or MySQL container, as DynamoDB uses DynamoDB Local. Most teams already run the database from Compose, so the gain is a clean Terraform plan and connection details.
- **Cognito, EC2 and VPC.** Cognito is a token issuer and a hosted UI, a product of its own. EC2 and VPC only make sense stored-only, for Terraform.
- **CloudFormation and CDK deploys.** The largest adoption lever and the largest build. It belongs after the services it would create exist.

## How items get in

A service enters with research in `docs/research/`, a pinned model, an overlay with evidence, a conformance file per client and a Terraform lifecycle, as [CONTRIBUTING.md](../CONTRIBUTING.md#adding-a-service) describes. A platform feature enters with an ADR. Anything that would make understudy fake what it does not do stays out.
