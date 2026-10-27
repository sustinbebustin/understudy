# Adjustments

This directory is the `lambda-ssm-parameter-terraform` pattern of [aws-samples/serverless-patterns](https://github.com/aws-samples/serverless-patterns/tree/deb84afac5425dfb4cabbc3c33f7a11fa4a71555/lambda-ssm-parameter-terraform) at commit `deb84af`, copied with the repository's MIT-0 licence (`LICENSE`). Every change from that commit is below; nothing else differs.

| File | Change | Reason |
|---|---|---|
| `main.tf` | `profile = "default"` removed from the `provider "aws"` block | Profile. `scripts/sealed` points `AWS_CONFIG_FILE` and `AWS_SHARED_CREDENTIALS_FILE` at `/dev/null` and supplies dummy credentials, so no named profile exists. |
| `.terraform.lock.hcl` | Added: `hashicorp/aws` 5.100.0 and `hashicorp/archive` 2.8.1, with checksums for linux and darwin on amd64 and arm64 | Provider pin. The pattern ships no lock file, so `~> 5.0` would resolve to whatever 5.x is newest on the day of the run. 5.100.0 is the version whose `aws_ssm_parameter` calls `docs/research/ssm-parameters.md` 10.4 records. |

The pattern keeps its `~> 5.0` constraint and its `nodejs22.x` runtime. Its Testing section invokes the function with `events/getEvent.json` and `events/putEvent.json`; `conformance/patterns/checks/lambda-ssm-parameter-terraform.sh` does that after the apply.
