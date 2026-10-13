# conformance

Suites that check understudy from the outside, each a separate module or project with its own dependency manifest. Each `run.sh` builds understudy from this repository, serves it on its own loopback port with a fresh data directory, runs the suite through `scripts/sealed` (so no real AWS setting or credential is used), then stops the server and removes its containers. Every script exits non-zero when a test fails; its header lists the arguments it takes, and each suite's README says more.

| Suite | Client | Entry point |
|---|---|---|
| `go-sdk/` | aws-sdk-go-v2 | `conformance/go-sdk/run.sh` |
| `python-boto3/` | boto3 | `conformance/python-boto3/run.sh` |
| `js-sdk-v3/` | AWS SDK for JavaScript v3 | `conformance/js-sdk-v3/run.sh` |
| `aws-cli/` | the AWS CLI | `conformance/aws-cli/run.sh` |
| `terraform/` | Terraform: apply, a no-change plan, an update, destroy | `conformance/terraform/run.sh` |
| `e2e-pipeline/` | the event-driven pipeline end to end | `conformance/e2e-pipeline/run.sh` |

No script accepts the Amazon DynamoDB Local License Agreement for you: every suite runs DynamoDB only when `UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1` is set, and skips it, listing each skip in its summary, otherwise or when `CONFORMANCE_DYNAMODB=0` is set. The e2e-pipeline workers lock in DynamoDB, so that suite needs the licence: `CONFORMANCE_DYNAMODB=0` skips it whole, and without either variable it refuses to start. Lambda execution and DynamoDB need a Docker daemon.
