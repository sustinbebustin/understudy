# Adjustments

This directory is the `s3-lambda-ses-terraform` pattern of [aws-samples/serverless-patterns](https://github.com/aws-samples/serverless-patterns/tree/deb84afac5425dfb4cabbc3c33f7a11fa4a71555/s3-lambda-ses-terraform) at commit `deb84af`, copied with the repository's MIT-0 licence (`LICENSE`). Every change from that commit is below; nothing else differs.

| File | Change | Reason |
|---|---|---|
| `deploy.tf` | A `terraform { required_providers { aws = { source = "hashicorp/aws", version = "6.68.0" } } }` block is added before the `provider "aws"` block | Provider override. The pattern pins no provider version. 6.68.0 is the version every conformance suite pins and caches. |
| `deploy.tf` | `s3_use_path_style`, `skip_credentials_validation` and `skip_requesting_account_id`, each `true`, added to the `provider "aws"` block | Provider override. The settings `docs/guide/terraform.md` gives for an IP-literal endpoint: without them the provider reads the bucket's tags through S3 Control at `<account>.127.0.0.1`, which cannot resolve. |
| `deploy.tf` | `python3.14` becomes `python3.13`, in the function's `runtime` and the layer's `compatible_runtimes` | Runtime. understudy pins no `python3.14` base image; `python3.13` is the newest Python runtime it runs. |
| `src/urlshortner.zip` | Not copied. `src/urlshortner/python/pyshorteners.py`, written for understudy, takes its place, and `conformance/patterns/checks/s3-lambda-ses-terraform.sh` zips it into `src/urlshortner.zip` before the apply | Layer. The upstream zip bundles pyshorteners 1.0.1 (GPLv3+) and certifi 2022.6.15 (MPL-2.0), which this Apache-2.0 repository cannot carry (ADR 0016 rules 2 and 3). `emailSender.py` imports `pyshorteners` when it loads (L1) and calls it only on its URL-shortener path (L102), for an object over 25 MB, which needs an outside service. The stub's `Shortener` raises, so a run that takes that path fails instead of reaching the network. |

The pattern's `provider "aws"` block names no profile, so no profile is removed.

## What the suite supplies

The README asks for an SES-verified sender and a receiver (How it works). `pattern_prepare` writes `terraform.tfvars` with `aws_region = "us-east-1"`, the sender `sender@s3-lambda-ses.example.org`, which `conformance/patterns/understudy.json` lists in `ses.verifiedIdentities`, and the receiver `success@simulator.amazonses.com`, a mailbox simulator address the sandbox accepts (`docs/research/ses.md` 6.1, 6.2). It then verifies the sender with `aws ses verify-email-identity`, the pattern's prerequisite, and `pattern_cleanup` deletes that identity after the destroy, since Terraform does not manage it.

## Known failure

`plan-no-changes` fails, and fails against AWS too. `random_id.rng` keeps `first = "${timestamp()}"`; Terraform evaluates `timestamp()` anew on every plan, so the plan always replaces `random_id.rng` and, through its `hex`, the role, its policy, the function, the permission and the bucket notification. This is the pattern's own Terraform, not an AWS or understudy behaviour, so it is left as copied.
