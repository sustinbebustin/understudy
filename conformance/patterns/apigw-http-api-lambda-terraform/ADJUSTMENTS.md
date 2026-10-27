# Adjustments

This directory is the `apigw-http-api-lambda-terraform` pattern of [aws-samples/serverless-patterns](https://github.com/aws-samples/serverless-patterns/tree/deb84afac5425dfb4cabbc3c33f7a11fa4a71555/apigw-http-api-lambda-terraform) at commit `deb84af`, copied with the repository's MIT-0 licence (`LICENSE`). Every change from that commit is below; nothing else differs.

| File | Change | Reason |
|---|---|---|
| `main.tf` | `required_providers.aws.version` `~> 6` becomes `6.68.0` | Provider override. 6.68.0 is the version every conformance suite pins and caches, and the one `docs/guide/apigateway.md` was run with. |
| `main.tf` | `profile = "default"` removed from the `provider "aws"` block | Profile. `scripts/sealed` points `AWS_CONFIG_FILE` and `AWS_SHARED_CREDENTIALS_FILE` at `/dev/null` and supplies dummy credentials, so no named profile exists. |
| `main.tf` | `s3_use_path_style`, `skip_credentials_validation` and `skip_requesting_account_id`, each `true`, added to the `provider "aws"` block | Provider override. The settings `docs/guide/terraform.md` gives for an IP-literal endpoint: without them the provider reads the bucket's tags through S3 Control at `<account>.127.0.0.1`, which cannot resolve. |
| `main.tf` | `aws_lambda_permission.api_gw.source_arn` `"${aws_apigatewayv2_api.lambda.execution_arn}/*/*"` becomes `"arn:aws:execute-api:${var.aws_region}:${data.aws_caller_identity.current.account_id}:${aws_apigatewayv2_api.lambda.id}/*/*"`, and `data "aws_caller_identity" "current" {}` is added after it | Provider override, its consequence. With `skip_credentials_validation` the provider has no account id, so `execution_arn` reads `arn:aws:execute-api:us-east-1::<api-id>`, no invocation's source ARN matches the permission, and every call answers 500 (`docs/research/apigateway-http.md` 9.7). `docs/guide/apigateway.md` (Terraform) gives this source ARN for that reason. On AWS the two values are equal. |
| `main.tf` | the function's `runtime` `python3.14` becomes `python3.13` | Runtime. understudy pins no `python3.14` base image; `python3.13` is the newest Python runtime it runs. |
