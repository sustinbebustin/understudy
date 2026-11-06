# Adjustments

This directory is the `kinesis-lambda-terraform` pattern of [aws-samples/serverless-patterns](https://github.com/aws-samples/serverless-patterns/tree/deb84afac5425dfb4cabbc3c33f7a11fa4a71555/kinesis-lambda-terraform) at commit `deb84af`, copied with the repository's MIT-0 licence (`LICENSE`). Every change from that commit is below; nothing else differs.

| File | Change | Reason |
|---|---|---|
| `main.tf` | `required_providers.aws.version` `~> 5.0` becomes `6.68.0` | Provider override. The provider must take understudy's endpoint from `AWS_ENDPOINT_URL`, which `scripts/sealed` sets; that is observed with 6.68.0 (`docs/research/tf-messaging.md` 1.2), the version every conformance suite pins and caches. |

The runtime stays `nodejs22.x`, which understudy runs, and the provider block names no profile.

The pattern documents no preparation. Its Testing section sends records from the hosted Amazon Kinesis Data Generator, a web page that writes to the stream with real AWS credentials, so it cannot reach understudy. `conformance/patterns/checks/kinesis-lambda-terraform.sh` puts records of the generator's documented `Sensor1` template with `aws kinesis put-records` instead, then reads the function's CloudWatch Logs group for each record's `Record payload:` line.
