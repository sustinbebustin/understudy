# An event-driven order pipeline, deployed to understudy with Terraform:
#
#   SNS topic "orders"
#     -> SQS queue "orders"   (raw delivery, filter: event_type = order.created)
#          -> Lambda "order-worker" (Go, provided.al2023, event source mapping
#             with ReportBatchItemFailures)
#               -> S3 bucket: orders/<orderId>.json
#     failed messages, after 2 receives -> SQS queue "orders-dlq"
#
# The same configuration deploys to AWS: only the provider block below is
# set up for a local endpoint.

terraform {
  required_version = ">= 1.6.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.68.0"
    }
    archive = {
      source  = "hashicorp/archive"
      version = "2.7.1"
    }
  }
}

# The provider takes its endpoint and credentials from the environment
# (AWS_ENDPOINT_URL, AWS_ACCESS_KEY_ID, ...), which `understudy env` prints.
# The skip flags stop it from calling the real STS and instance metadata
# services, and path-style S3 keeps bucket names out of the hostname.
provider "aws" {
  region = var.region

  s3_use_path_style           = true
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true
}

variable "region" {
  description = "Region to deploy to."
  type        = string
  default     = "us-east-1"
}

variable "architecture" {
  description = "Lambda architecture of the worker: x86_64, or arm64 on an ARM machine. It must match the GOARCH the bootstrap was built for."
  type        = string
  default     = "x86_64"

  validation {
    condition     = contains(["x86_64", "arm64"], var.architecture)
    error_message = "architecture must be x86_64 or arm64."
  }
}

variable "bucket_name" {
  description = "Bucket the worker writes orders to. S3 bucket names are global on AWS, so pick your own there."
  type        = string
  default     = "understudy-example-orders"
}

# --- Messaging --------------------------------------------------------------

resource "aws_sns_topic" "orders" {
  name = "orders"
}

resource "aws_sqs_queue" "orders_dlq" {
  name = "orders-dlq"
}

resource "aws_sqs_queue" "orders" {
  name = "orders"

  # AWS recommends at least six times the function timeout for a queue that
  # feeds a Lambda function, so a batch still running is not redelivered.
  visibility_timeout_seconds = 60

  # A message that fails twice moves to the dead-letter queue.
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.orders_dlq.arn
    maxReceiveCount     = 2
  })
}

# Lets the topic, and only the topic, send to the queue.
resource "aws_sqs_queue_policy" "orders" {
  queue_url = aws_sqs_queue.orders.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "sns.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = aws_sqs_queue.orders.arn
      Condition = { ArnEquals = { "aws:SourceArn" = aws_sns_topic.orders.arn } }
    }]
  })
}

# Raw delivery puts the published message in the SQS body as is, instead of
# wrapping it in SNS's JSON envelope. The filter policy drops every message
# whose event_type attribute is not order.created.
resource "aws_sns_topic_subscription" "orders" {
  topic_arn            = aws_sns_topic.orders.arn
  protocol             = "sqs"
  endpoint             = aws_sqs_queue.orders.arn
  raw_message_delivery = true

  filter_policy = jsonencode({
    event_type = ["order.created"]
  })

  depends_on = [aws_sqs_queue_policy.orders]
}

# --- Storage ----------------------------------------------------------------

resource "aws_s3_bucket" "orders" {
  bucket = var.bucket_name

  # Lets `terraform destroy` remove the bucket with the orders still in it.
  force_destroy = true
}

# --- The worker -------------------------------------------------------------

resource "aws_iam_role" "worker" {
  name = "order-worker"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "lambda.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

# What the worker needs: read and delete from its queue, write its orders,
# and write its logs. understudy stores IAM policies but does not enforce
# them, so a missing permission shows up only on AWS.
resource "aws_iam_role_policy" "worker" {
  name = "order-worker"
  role = aws_iam_role.worker.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:GetQueueAttributes"]
        Resource = aws_sqs_queue.orders.arn
      },
      {
        Effect   = "Allow"
        Action   = "s3:PutObject"
        Resource = "${aws_s3_bucket.orders.arn}/orders/*"
      },
      {
        Effect   = "Allow"
        Action   = ["logs:CreateLogStream", "logs:PutLogEvents"]
        Resource = "${aws_cloudwatch_log_group.worker.arn}:*"
      },
    ]
  })
}

resource "aws_cloudwatch_log_group" "worker" {
  name              = "/aws/lambda/order-worker"
  retention_in_days = 7
}

# The bootstrap binary is built before `terraform apply` (see the README);
# this zips it.
data "archive_file" "worker" {
  type        = "zip"
  source_file = "${path.module}/build/bootstrap"
  output_path = "${path.module}/build/worker.zip"
}

resource "aws_lambda_function" "worker" {
  function_name    = "order-worker"
  role             = aws_iam_role.worker.arn
  runtime          = "provided.al2023"
  handler          = "bootstrap"
  architectures    = [var.architecture]
  filename         = data.archive_file.worker.output_path
  source_code_hash = data.archive_file.worker.output_base64sha256
  timeout          = 10

  environment {
    variables = {
      ORDERS_BUCKET = aws_s3_bucket.orders.bucket
    }
  }

  depends_on = [aws_cloudwatch_log_group.worker, aws_iam_role_policy.worker]
}

# Polls the queue and invokes the worker with up to 10 messages at a time.
# ReportBatchItemFailures lets the worker fail single messages: only the
# ones it names return to the queue.
resource "aws_lambda_event_source_mapping" "worker" {
  function_name           = aws_lambda_function.worker.arn
  event_source_arn        = aws_sqs_queue.orders.arn
  batch_size              = 10
  function_response_types = ["ReportBatchItemFailures"]
}

output "topic_arn" {
  value = aws_sns_topic.orders.arn
}

output "queue_url" {
  value = aws_sqs_queue.orders.url
}

output "dlq_url" {
  value = aws_sqs_queue.orders_dlq.url
}

output "bucket" {
  value = aws_s3_bucket.orders.bucket
}
