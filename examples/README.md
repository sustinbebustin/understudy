# Examples

Small, runnable projects that use understudy the way an application would. Each has a README with the exact commands and a `demo.sh` that runs them.

| Example | What it shows | Needs |
|---|---|---|
| [`python-worker/`](python-worker) | A boto3 SQS worker and its pytest suite, with the emulator clock skipping a visibility timeout | uv, AWS CLI |
| [`sns-sqs-lambda-terraform/`](sns-sqs-lambda-terraform) | SNS -> SQS -> Go Lambda -> S3, deployed with Terraform, with a filter policy, partial batch failures and a dead-letter queue | Docker, Go, Terraform, AWS CLI, jq |
| [`docker-compose/`](docker-compose) | understudy as a compose service next to an app container, with state kept across restarts | Docker with compose, AWS CLI |
| [`github-actions/`](github-actions) | A workflow that starts understudy in CI and runs a test suite against it | a GitHub repository |

## Before you start

`python-worker` and `sns-sqs-lambda-terraform` talk to an understudy running on your machine (`docker-compose` starts its own). From the repository root, with Go 1.27:

```sh
go install ./cmd/understudy
understudy serve
```

Then, in the shell where you run an example:

```sh
eval "$(understudy env)"
```

This sets `AWS_ENDPOINT_URL`, dummy credentials and a region, so every AWS SDK, the AWS CLI and Terraform go to understudy instead of AWS. No example needs an AWS account, and none sends anything to AWS.

## Checking the examples

`examples/check.sh` builds understudy from this repository and runs every example's `demo.sh` against it, sealed off from real AWS by `scripts/sealed`. It exits non-zero when one fails. Run it after changing understudy or an example. Once the images and providers are cached it takes about four minutes, most of it Terraform waiting on SQS queues.

```sh
examples/check.sh                     # every example
examples/check.sh python-worker       # only the ones named
```

Each example's output goes to `.build/examples/logs/<example>.log`, and understudy's log to `.build/examples/logs/server.log`.
