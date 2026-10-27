# Adjustments

This directory is the `dynamodb-streams-lambda-terraform` pattern of [aws-samples/serverless-patterns](https://github.com/aws-samples/serverless-patterns/tree/deb84afac5425dfb4cabbc3c33f7a11fa4a71555/dynamodb-streams-lambda-terraform) at commit `deb84af`, copied with the repository's MIT-0 licence (`LICENSE`). Every change from that commit is below; nothing else differs.

| File | Change | Reason |
|---|---|---|
| `main.tf` | `required_providers.aws.version` `~> 5.0` becomes `6.68.0` | Provider override. The provider must take understudy's endpoint from `AWS_ENDPOINT_URL`, which `scripts/sealed` sets; that is observed with 6.68.0 (`docs/research/tf-messaging.md` 1.2), the version every conformance suite pins and caches. |
| `main.tf` | `profile = "default"` removed from the `provider "aws"` block | Profile. `scripts/sealed` points `AWS_CONFIG_FILE` and `AWS_SHARED_CREDENTIALS_FILE` at `/dev/null` and supplies dummy credentials, so no named profile exists. |
| `main.tf` | the function's `runtime` `nodejs24.x` becomes `nodejs22.x` | Runtime. understudy pins no `nodejs24.x` base image; `nodejs22.x` is the newest Node.js runtime it runs. |

The pattern documents no preparation. Its Testing section ("add an item to the DynamoDB table", then read the function's CloudWatch Logs) is `conformance/patterns/checks/dynamodb-streams-lambda-terraform.sh`.
