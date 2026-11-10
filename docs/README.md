# understudy documentation

| To | Read |
|---|---|
| Use understudy | The [guide](guide/README.md): one task-oriented page per service and feature |
| Look up a setting | [configuration.md](configuration.md): every flag, variable and file key |
| See what an operation supports | [coverage.md](coverage.md): every operation, fidelity switch and evidence item, generated from the binary |
| Know where it differs from AWS | [guide/differences.md](guide/differences.md) |
| Understand the design | [ARCHITECTURE.md](ARCHITECTURE.md), and the decisions behind it in [adr/](adr/) |
| Know what AWS does, and how we know | The [research notes](#research-notes) in [research/](research/) |
| See what comes next | [ROADMAP.md](ROADMAP.md) |
| Change understudy | [CONTRIBUTING.md](../CONTRIBUTING.md), with the domain's terms in [GLOSSARY.md](../GLOSSARY.md) |

For maintainers: [release-runbook.md](release-runbook.md) cuts a release, [release-checklist.md](release-checklist.md) is the manual macOS run that precedes one, and [recording-kit.md](recording-kit.md) records real AWS exchanges for `testdata/parity/`.

## Research notes

Each note in `research/` records what AWS does in one area, with a source for every claim. The notes are the specification understudy is built and tested against: tests assert what a note records, never what understudy happens to do.

- **Citations.** Code cites a note as `<note>.md <section>`, for example `(sqs.md 6.1)`: the file name and the number of a section heading. Section numbers are therefore permanent. Add sections at the end of a note or as new subsections; never renumber.
- **Evidence.** A note that tags its claims defines the tags near its top. Across the notes, the ranking is the coverage page's: recorded from real AWS, recorded by another project, AWS documentation, SDK source, inference. A claim no source settles is marked `UNVERIFIED`.
- **Dates.** A note states when it was researched and is not rewritten as understudy changes. What understudy does today is on the coverage page; a note's "implications for understudy" sections are the design input of their date.
- **"Phase 1".** The notes written first use this for the scope they were researched for: the twelve services understudy started with (SQS, SNS, S3, DynamoDB, Lambda, CloudWatch, CloudWatch Logs, EventBridge, Secrets Manager, KMS, IAM and STS) and the Go v2, boto3 and JavaScript v3 SDKs, the AWS CLI and Terraform as clients.
- **Recording sections.** Many notes end with the commands that record their open questions against real AWS. A maintainer runs those by hand with the [recording kit](recording-kit.md); automation and coding agents never do.

| Area | Notes |
|---|---|
| Wire protocols and signing | [wire-json-query](research/wire-json-query.md), [wire-rest](research/wire-rest.md), [wire-cbor](research/wire-cbor.md), [sigv4-checksums-endpoints](research/sigv4-checksums-endpoints.md), [error-matrix](research/error-matrix.md) |
| Models and code generation | [smithy-models](research/smithy-models.md), [smithy-protocol-tests](research/smithy-protocol-tests.md), [go-toolchain](research/go-toolchain.md) |
| Messaging | [sqs](research/sqs.md), [sns](research/sns.md), [sns-http-delivery](research/sns-http-delivery.md), [ses](research/ses.md) |
| Events and schedules | [eventbridge-buses](research/eventbridge-buses.md), [scheduler](research/scheduler.md), [matching-engines](research/matching-engines.md), [secrets-logs-events-alarms](research/secrets-logs-events-alarms.md), [cloudwatch-alarms](research/cloudwatch-alarms.md), [logs-filter-patterns](research/logs-filter-patterns.md) |
| Storage | [s3](research/s3.md), [s3-ssec-object-lock](research/s3-ssec-object-lock.md), [s3-checksum-residuals-and-record-backlog](research/s3-checksum-residuals-and-record-backlog.md), [ssm-parameters](research/ssm-parameters.md) |
| DynamoDB and streams | [dynamodb-local](research/dynamodb-local.md), [dynamodb-local-terms](research/dynamodb-local-terms.md), [dynamodb-streams](research/dynamodb-streams.md), [kinesis](research/kinesis.md) |
| Lambda | [lambda-api](research/lambda-api.md), [lambda-events](research/lambda-events.md), [lambda-stream-sources](research/lambda-stream-sources.md), [lambda-destinations-layers](research/lambda-destinations-layers.md), [lambda-runtime-contract](research/lambda-runtime-contract.md), [containers](research/containers.md) |
| API Gateway and Step Functions | [apigateway-http](research/apigateway-http.md), [apigateway-rest](research/apigateway-rest.md), [stepfunctions-api](research/stepfunctions-api.md), [stepfunctions-asl](research/stepfunctions-asl.md) |
| Identity and permissions | [iam-sts-kms](research/iam-sts-kms.md), [iam-evaluation](research/iam-evaluation.md), [iam-enforcement-surfaces](research/iam-enforcement-surfaces.md) |
| Terraform and test tooling | [tf-compute](research/tf-compute.md), [tf-messaging](research/tf-messaging.md), [tf-storage](research/tf-storage.md), [tf-remaining](research/tf-remaining.md), [testcontainers](research/testcontainers.md) |
