# SQS: standard and FIFO queues with dead-letter queues, redrive and redrive
# allow policies, and a queue policy. The update step changes timing
# attributes, the redrive and allow policies, the policy statement and tags.

locals {
  sqs_orders_name     = "${var.name_prefix}-orders"
  sqs_orders_dlq_name = "${var.name_prefix}-orders-dlq"
  sqs_orders_arn      = "arn:${local.partition}:sqs:${local.region}:${local.account_id}:${local.sqs_orders_name}"
}

resource "aws_sqs_queue" "orders_dlq" {
  name                      = local.sqs_orders_dlq_name
  message_retention_seconds = 1209600
  sqs_managed_sse_enabled   = true

  # Only the orders queue may use this queue as its dead-letter queue; the
  # update opens it to every queue in the account.
  redrive_allow_policy = local.updated ? jsonencode({
    redrivePermission = "allowAll"
    }) : jsonencode({
    redrivePermission = "byQueue"
    sourceQueueArns   = [local.sqs_orders_arn]
  })

  tags = {
    Role = "dead-letter"
  }
}

resource "aws_sqs_queue" "orders" {
  name                       = local.sqs_orders_name
  delay_seconds              = local.updated ? 5 : 0
  max_message_size           = 262144
  message_retention_seconds  = local.updated ? 86400 : 345600
  receive_wait_time_seconds  = 20
  visibility_timeout_seconds = local.updated ? 120 : 60
  sqs_managed_sse_enabled    = true

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.orders_dlq.arn
    maxReceiveCount     = local.updated ? 3 : 5
  })

  tags = merge(
    { Role = "work" },
    local.updated ? { Stage = "updated" } : {},
  )
}

resource "aws_sqs_queue_policy" "orders" {
  queue_url = aws_sqs_queue.orders.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "AllowAccountSend"
        Effect    = "Allow"
        Principal = { AWS = "arn:${local.partition}:iam::${local.account_id}:root" }
        Action    = local.updated ? ["sqs:SendMessage", "sqs:GetQueueAttributes"] : ["sqs:SendMessage"]
        Resource  = aws_sqs_queue.orders.arn
      },
    ]
  })
}

resource "aws_sqs_queue" "ledger_dlq" {
  name                        = "${var.name_prefix}-ledger-dlq.fifo"
  fifo_queue                  = true
  content_based_deduplication = true
  message_retention_seconds   = 1209600
}

resource "aws_sqs_queue" "ledger" {
  name                        = "${var.name_prefix}-ledger.fifo"
  fifo_queue                  = true
  content_based_deduplication = local.updated ? false : true
  deduplication_scope         = "messageGroup"
  fifo_throughput_limit       = "perMessageGroupId"
  visibility_timeout_seconds  = local.updated ? 90 : 45

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.ledger_dlq.arn
    maxReceiveCount     = 4
  })

  tags = {
    Role = "ledger"
  }
}

output "sqs_orders_url" {
  value = aws_sqs_queue.orders.url
}

output "sqs_ledger_url" {
  value = aws_sqs_queue.ledger.url
}
