# Adjustments

This directory is the `lambda-layer-terraform` pattern of [aws-samples/serverless-patterns](https://github.com/aws-samples/serverless-patterns/tree/deb84afac5425dfb4cabbc3c33f7a11fa4a71555/lambda-layer-terraform) at commit `deb84af`, copied with the repository's MIT-0 licence (`LICENSE`). Every change from that commit is below; nothing else differs.

| File | Change | Reason |
|---|---|---|
| `main.tf` | `required_providers.aws.version` `~> 4.8.0` becomes `6.68.0` | Provider override. The provider must take understudy's endpoint from `AWS_ENDPOINT_URL`, which `scripts/sealed` sets; that is observed with 6.68.0 (`docs/research/tf-messaging.md` 1.2), the version every conformance suite pins and caches. |
| `main.tf` | `profile = "default"` removed from the `provider "aws"` block | Profile. `scripts/sealed` points `AWS_CONFIG_FILE` and `AWS_SHARED_CREDENTIALS_FILE` at `/dev/null` and supplies dummy credentials, so no named profile exists. |
| `main.tf` | `python3.14` becomes `python3.13`, in both functions' `runtime` and the layer's `compatible_runtimes` | Runtime. understudy pins no `python3.14` base image; `python3.13` is the newest Python runtime it runs. |
| `docs/` | The 11 README screenshots (`01-install-package.png` to `11-terraform-show.png`) are not copied; `README.md`'s image links point at them in the upstream repository at commit `deb84af` | Repository size. The screenshots show the AWS console and a terminal, not anything the suite runs, and would add about 780 KB of binaries to the history. |

The pattern's own preparation step (README, Deployment Instructions, step 3) installs `mysql-connector-python` into `dependencies/mysql-connector-python/python`. `conformance/patterns/checks/lambda-layer-terraform.sh` does that before the apply, for Python 3.13 on x86_64, and runs the pattern's Testing section after it.
