# conformance

Suites that check understudy from the outside, each a separate module or project with its own dependency manifest. Each `run.sh` sources `common.sh`, builds understudy from this repository, serves it on its own loopback port with a fresh data directory, runs the suite through `scripts/sealed` (so no real AWS setting or credential is used), then stops the server and removes its containers. Every script exits non-zero when a test fails; its header lists the arguments it takes, and each suite's README says more.

| Suite | Client | Entry point |
|---|---|---|
| `go-sdk/` | aws-sdk-go-v2 | `conformance/go-sdk/run.sh` |
| `python-boto3/` | boto3 | `conformance/python-boto3/run.sh` |
| `js-sdk-v3/` | AWS SDK for JavaScript v3 | `conformance/js-sdk-v3/run.sh` |
| `aws-cli/` | the AWS CLI | `conformance/aws-cli/run.sh` |
| `terraform/` | Terraform: apply, a no-change plan, an update, destroy | `conformance/terraform/run.sh` |
| `e2e-pipeline/` | the event-driven pipeline end to end | `conformance/e2e-pipeline/run.sh` |

## DynamoDB

understudy serves DynamoDB through Amazon DynamoDB Local, pulled on first use and run under [Amazon's licence terms](https://aws.amazon.com/dynamodb/dynamodblocallicense/). Every `run.sh` runs DynamoDB's tests by default:

| Setting | Effect |
|---|---|
| unset | Runs DynamoDB's tests. After startup, `run.sh` exits 1 unless the health document reports DynamoDB as `enabled`. |
| `CONFORMANCE_DYNAMODB=0` | Skips DynamoDB's tests, each with the reason `DynamoDB opted out (CONFORMANCE_DYNAMODB=0)`; DynamoDB Local never starts. |

With any other value, `run.sh` exits 2 and runs nothing. `run.sh` clears serve's environment of every `UNDERSTUDY_*` and `AWS_*` setting. In the e2e-pipeline suite every worker takes a DynamoDB lock for each SQS record, so under `CONFORMANCE_DYNAMODB=0` the tests that publish to the topic, and so make a worker take a lock, or that read the lock table skip; the rest still run.

## Summary

Every `run.sh` ends with a summary on stderr: `run.sh: passed=N failed=N skipped=N`, then one `run.sh: SKIP <test>: <reason>` line per skipped test. When the suite fails but no test does (a build error, a timeout or a crash before any result), a last line says so and names where the output is. A skip never changes the exit status. Suites that drive the AWS CLI or Terraform first run its version command through `scripts/sealed` and exit 2, naming the binary, when it cannot run. Lambda execution and DynamoDB need a Docker daemon.
