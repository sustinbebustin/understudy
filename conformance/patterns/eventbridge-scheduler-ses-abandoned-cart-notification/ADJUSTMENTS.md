# Adjustments

This directory is the `eventbridge-scheduler-ses-abandoned-cart-notification` pattern of [aws-samples/serverless-patterns](https://github.com/aws-samples/serverless-patterns/tree/deb84afac5425dfb4cabbc3c33f7a11fa4a71555/eventbridge-scheduler-ses-abandoned-cart-notification) at commit `deb84af`, copied with the repository's MIT-0 licence (`LICENSE`). Every change from that commit is below; nothing else differs.

| File | Change | Reason |
|---|---|---|
| `main.tf` | the function's `runtime` `python3.14` becomes `python3.13` | Runtime. understudy pins no `python3.14` base image; `python3.13` is the newest Python runtime it runs. |
| `.terraform.lock.hcl` | Added: `hashicorp/aws` 6.32.1 (constraint `~> 6.32.0`, unchanged in `main.tf`) and `hashicorp/archive` 2.8.1, with checksums for linux and darwin on amd64 and arm64 | Provider pin. The pattern ships no lock file. `~> 6.32.0` admits 6.32.0 and 6.32.1; 6.32.1 is the version whose `aws_scheduler_schedule` calls `docs/research/scheduler.md` 11.4 reads. `main.tf` uses `data "archive_file"` without declaring the provider, and 2.8.1 is the version the other patterns pin. |

The pattern keeps its `notification_processor.zip`, which `data "archive_file"` rewrites from `notification_processor.py` on every plan. `conformance/patterns/checks/eventbridge-scheduler-ses-abandoned-cart-notification.sh` verifies the sender and the seed recipient as the README's prerequisites say, writes the README's `-var` answers as `terraform.tfvars` (the suite runs Terraform without input), and after the apply moves the emulator clock past the schedule's `rate(1 hour)`, then checks the one email, the seed record marked notified, and the empty dead-letter queue.

The function's role attaches the AWS managed policy `AWSLambdaBasicExecutionRole`; until managed policies carry their documents in understudy, the attachment grants nothing, so a run under `CONFORMANCE_IAM_ENFORCE=enforce` is not expected to pass.
