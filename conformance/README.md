# conformance

Suites that check understudy from the outside, each a separate module or project with its own dependency manifest. Each `run.sh` sources `common.sh`, builds understudy from this repository, serves it on its own loopback port with a fresh data directory, runs the suite through `scripts/sealed` (so no real AWS setting or credential is used), then stops the server and removes its containers. Every script exits non-zero when a test fails; its header lists the arguments it takes, and each suite's README says more.

| Suite | Client | Entry point |
|---|---|---|
| `go-sdk/` | aws-sdk-go-v2 | `conformance/go-sdk/run.sh` |
| `python-boto3/` | boto3 | `conformance/python-boto3/run.sh` |
| `js-sdk-v3/` | AWS SDK for JavaScript v3 | `conformance/js-sdk-v3/run.sh` |
| `aws-cli/` | the AWS CLI | `conformance/aws-cli/run.sh` |
| `terraform/` | Terraform: apply, a no-change plan, an update, destroy | `conformance/terraform/run.sh` |
| `e2e-pipeline/` | aws-sdk-go-v2 and the `admin` client: an SNS to SQS to Lambda pipeline end to end | `conformance/e2e-pipeline/run.sh` |
| `patterns/` | serverless-patterns as their READMEs run them, through Terraform | `conformance/patterns/run.sh` |
| `iam-loop/` | the aws-cli and go-sdk suites as an IAM user: record under `--iam-enforce advise`, attach the rendered policies, re-run under `enforce` | `conformance/iam-loop/run.sh` |

## Ports and run directories

Every `run.sh` serves understudy on `CONFORMANCE_PORT`, or on its own default below, and exits 2 with `run.sh: CONFORMANCE_PORT='<value>' is not a port number. No test ran.` when the value is not a port number. Each run builds understudy into a directory of its own under `.build/conformance/<suite>/` (`run.XXXXXX`), so two runs of one suite, on different ports, and runs of different suites never share a binary, a log or a data directory. The defaults below are pairwise distinct, derived ports included, so every suite can run at once as it is; two runs of one suite need a `CONFORMANCE_PORT` each, spaced by at least the suite's span.

| Suite | Default port | Also listens on |
|---|---|---|
| `go-sdk/` | 45661 | a TLS listener on a free port the OS picks |
| `python-boto3/` | 45662 | none |
| `js-sdk-v3/` | 45663 | `CONFORMANCE_PORT` + 1 (45664) for TLS, `CONFORMANCE_PORT` + 2 (45665) for the SNS HTTPS receiver |
| `e2e-pipeline/` | 45666 | none |
| `patterns/` | 45667 | none |
| `terraform/` | 45670 | `CONFORMANCE_PORT` + 1 (45671) for the SNS HTTPS receiver, unless `CONFORMANCE_SNS_RECEIVER_PORT` names another |
| `iam-loop/` | 45680 | none; its Terraform suite and examples step runs after its server stops, on the suite's own default port |
| `aws-cli/` | 45690 | with `CONFORMANCE_JOBS=J`, job `I` listens on `CONFORMANCE_PORT + 10*(I-1)`: 45690, 45700, 45710, ... |

aws-cli's jobs are last so their steps of 10 reach no other suite's default.
A terraform or patterns run removes its directory when it passes and keeps it, printing the path, when it fails, as the other suites do. The Terraform provider cache in `.build/conformance/<suite>/plugin-cache` is shared by runs of a suite.

## DynamoDB

understudy serves DynamoDB through Amazon DynamoDB Local, pulled on first use and run under [Amazon's licence terms](https://aws.amazon.com/dynamodb/dynamodblocallicense/). Every `run.sh` runs DynamoDB's tests by default:

| Setting | Effect |
|---|---|
| unset | Runs DynamoDB's tests. After startup, `run.sh` exits 1 unless the health document reports DynamoDB as `enabled`. |
| `CONFORMANCE_DYNAMODB=0` | Skips DynamoDB's tests, each with the reason `DynamoDB opted out (CONFORMANCE_DYNAMODB=0)`; DynamoDB Local never starts. |

With any other value, `run.sh` exits 2 and runs nothing. `run.sh` clears serve's environment of every `UNDERSTUDY_*` and `AWS_*` setting. In the e2e-pipeline suite every worker takes a DynamoDB lock for each SQS record, so under `CONFORMANCE_DYNAMODB=0` the tests that publish to the topic, and so make a worker take a lock, or that read the lock table skip; the rest still run.

## Signature checking

`SIGV4` sets serve's `--sigv4` mode for a run. `python-boto3/run.sh` defaults to `verify`; every other `run.sh` defaults to `parse`, which reads each request's credentials and checks no signature, so a suite proves understudy's signature verification only when it runs under `verify`.

| Suite | CI runs it under | Why |
|---|---|---|
| `python-boto3/` | `verify` (its default) | boto3's signer on every request, plus the tests that take the `sigv4_verify` fixture and skip without it. |
| `aws-cli/` | `parse`, and again under `verify` | The most S3 request shapes of any suite: `aws s3 cp` single-part, multipart and from stdin, `s3 presign`, `s3 sync`, virtual-hosted addressing and the `s3api` calls. The CLI signs with its own copy of botocore, and the `aws s3` transfer tests run a second time under the CRT transfer client, which signs with the AWS Common Runtime's aws-c-auth, a signer no other suite uses (`aws-cli/README.md`). |
| `terraform/` | `parse`, and again under `verify` | The Terraform AWS provider signs with aws-sdk-go-v2, a signer written apart from botocore, across every service the suite applies. |
| `js-sdk-v3/` | `parse`, and again under `verify` | The JavaScript SDK signs with `@smithy/signature-v4`, a third signer. Two STS tests need `parse` and skip under `verify`, so both runs are needed. |
| `go-sdk/`, `e2e-pipeline/` | `parse` (the default) | Both sign with aws-sdk-go-v2, whose verify run is terraform's; either takes `SIGV4=verify` when run by hand. |

A suite's `parse` run stays beside its `verify` run because `parse` is serve's default, the mode most users run.

## Summary

Every `run.sh` ends with a summary on stderr: `run.sh: passed=N failed=N skipped=N`, then one `run.sh: SKIP <test>: <reason>` line per skipped test. When the suite fails but no test does (a build error, a timeout or a crash before any result), a last line says so and names where the output is. A skip never changes the exit status. Suites that drive the AWS CLI or Terraform first run its version command through `scripts/sealed` and exit 2, naming the binary, when it cannot run. Lambda execution and DynamoDB need a Docker daemon.
