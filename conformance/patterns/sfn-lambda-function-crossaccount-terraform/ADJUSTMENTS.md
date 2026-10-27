# Adjustments

This directory is the `sfn-lambda-function-crossaccount-terraform` pattern of [aws-samples/serverless-patterns](https://github.com/aws-samples/serverless-patterns/tree/deb84afac5425dfb4cabbc3c33f7a11fa4a71555/sfn-lambda-function-crossaccount-terraform) at commit `deb84af`, copied with the repository's MIT-0 licence (`LICENSE`). Every change from that commit is below; nothing else differs.

| File | Change | Reason |
|---|---|---|
| `main.tf` | `required_providers.aws.version` `~> 4.0` becomes `6.68.0` | Provider override. 6.68.0 is the version every conformance suite pins and caches. |
| `main.tf` | `profile = "default"` removed from the `default` provider; `profile = "crossaccount"` becomes `access_key = "200000900001"` and `secret_key = "understudy"` in the `crossaccount` provider | Profiles. `scripts/sealed` points `AWS_CONFIG_FILE` and `AWS_SHARED_CREDENTIALS_FILE` at `/dev/null` and supplies dummy credentials for the pattern's account, so no named profile exists. understudy takes the account from a 12-digit access key, so the second account is a static key: the state machine and its role are in the pattern's account, the function, its role and its permission in `200000900001`. |
| `lambda-function.tf` | the function's `runtime` `python3.14` becomes `python3.13` | Runtime. understudy pins no `python3.14` base image; `python3.13` is the newest Python runtime it runs. |
| `terraform.tfvars` | added, with `region = "us-east-1"` and `prefix = "understudy"` | Prompts. The README answers `var.region` and `var.prefix` at the prompts of `terraform apply` and `terraform destroy`; the suite runs Terraform without input, so the answers are given as a variables file. |

The README's test starts an execution with `{"payload": "testing"}`; `conformance/patterns/checks/sfn-lambda-function-crossaccount-terraform.sh` does that after the apply, waits for the emulator to settle, and checks the execution SUCCEEDED with the function's answer as the lambda:invoke result's `Payload`.

The suite's `nothing-left` step checks the pattern's account only; the function's account, `200000900001`, is not checked after the destroy.
