# Terraform

The Terraform AWS provider reads `AWS_ENDPOINT_URL`, so a configuration written for AWS applies to understudy with two provider settings. The goal for every resource type understudy supports: `apply` succeeds, a second `plan` shows no changes, and `destroy` leaves nothing behind. [conformance/terraform](../../conformance/terraform/) checks exactly that for each service with provider 6.68.0.

## Provider

```hcl
terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

# The endpoint and credentials come from 'understudy env'.
provider "aws" {
  s3_use_path_style          = true
  skip_requesting_account_id = true
}
```

`skip_requesting_account_id = true` is required at the `http://127.0.0.1:4566` endpoint `understudy env` prints: with an account id, the provider reads S3 bucket tags through S3 Control at `<account>.<endpoint host>`, which cannot resolve for an IP address, and the refresh fails. `s3_use_path_style` is optional there, since the provider addresses S3 path style at an IP address anyway. The provider's startup `sts:GetCallerIdentity` needs no skip setting: understudy answers it with the account the access key selects. If `*.localhost` names resolve on your machine (`understudy doctor` says) and you set `AWS_ENDPOINT_URL=http://localhost:4566` yourself, neither setting is needed.

## Apply, plan, destroy

```sh
eval "$(understudy env --sdk terraform)"
terraform init
terraform apply -auto-approve
terraform plan -detailed-exitcode
terraform destroy -auto-approve
```

`plan -detailed-exitcode` exits 0 when nothing would change. An exit of 2 against understudy, for a resource type it supports, is a fidelity bug worth reporting with the plan output.

A small configuration this was run with: an SQS queue with a dead-letter queue, an SNS topic with a filtered raw-delivery subscription to the queue, a KMS key, and an S3 bucket encrypted with it by default.

```hcl
resource "aws_sqs_queue" "dlq" {
  name = "orders-dlq"
}

resource "aws_sqs_queue" "orders" {
  name = "orders"
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq.arn
    maxReceiveCount     = 3
  })
}

resource "aws_sns_topic" "events" {
  name = "events"
}

resource "aws_sns_topic_subscription" "orders" {
  topic_arn            = aws_sns_topic.events.arn
  protocol             = "sqs"
  endpoint             = aws_sqs_queue.orders.arn
  raw_message_delivery = true
  filter_policy        = jsonencode({ kind = ["order"] })
}

resource "aws_kms_key" "data" {
  description = "bucket key"
}

resource "aws_s3_bucket" "data" {
  bucket = "orders-data"
}

resource "aws_s3_bucket_server_side_encryption_configuration" "data" {
  bucket = aws_s3_bucket.data.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = aws_kms_key.data.arn
    }
  }
}
```

[examples/sns-sqs-lambda-terraform](../../examples/sns-sqs-lambda-terraform/) adds a Lambda function fed by the queue.

## Why some resources take seconds

understudy answers every call at once, and resources are in their final state before the creating call returns (`Active` functions, `ENABLED` TTL, readable bucket subresources). What remains is the provider's own polling, which it does against any backend:

| Resource | Provider behavior | Time |
|---|---|---|
| `aws_sqs_queue` create or update | polls `GetQueueAttributes` until six reads in a row match | about 25 s |
| `aws_sqs_queue` destroy | polls until fifteen reads in a row find no queue | about 42 s |
| `aws_s3_bucket_lifecycle_configuration` create or update | waits 10 s, then needs ten identical reads 5 s apart | about 55 s |

The provider retries throttling errors up to 25 times, which matters when you [inject faults](faults.md) into a Terraform run.

## Accounts

Give each workspace or test its own account with a 12-digit access key, so runs never see each other's resources:

```sh
eval "$(understudy env --sdk terraform --account 100000000001)"
```

## Known gaps

- `terraform import` of an SQS queue fails: the provider accepts only `https://sqs.<region>.<host>/...` queue URLs, and understudy's are `http://localhost:4566/...`. Create, read and destroy work.
- Resource types outside the twelve services fail with the 501 the provider receives for their first call.
- Deleted KMS keys stay listed in `PendingDeletion` until their deletion window passes on the [emulator clock](clock.md), as on AWS.
