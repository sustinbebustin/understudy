# SNS: a topic with a topic policy, fanned out to SQS through two
# subscriptions: one with a MessageBody-scoped filter policy and raw delivery,
# one with an attribute-scoped filter policy and the JSON envelope. The update
# step changes the display name, the topic policy, both filter policies and
# the raw delivery flag.

resource "aws_sns_topic" "events" {
  name         = "${var.name_prefix}-events"
  display_name = local.updated ? "Webhook events v2" : "Webhook events"

  tags = merge(
    { Purpose = "webhooks" },
    local.updated ? { Stage = "updated" } : {},
  )
}

resource "aws_sns_topic_policy" "events" {
  arn = aws_sns_topic.events.arn

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "AllowAccountPublish"
        Effect    = "Allow"
        Principal = { AWS = "arn:${local.partition}:iam::${local.account_id}:root" }
        Action    = local.updated ? ["SNS:Publish", "SNS:GetTopicAttributes"] : ["SNS:Publish"]
        Resource  = aws_sns_topic.events.arn
      },
    ]
  })
}

# Fixture: the queues the subscriptions deliver to, with a policy that lets
# the topic send to them.
resource "aws_sqs_queue" "sns_body_filtered" {
  name = "${var.name_prefix}-body-filtered"
}

resource "aws_sqs_queue" "sns_attribute_filtered" {
  name = "${var.name_prefix}-attribute-filtered"
}

resource "aws_sqs_queue_policy" "sns_body_filtered" {
  queue_url = aws_sqs_queue.sns_body_filtered.id
  policy    = data.aws_iam_policy_document.sns_to_sqs["body_filtered"].json
}

resource "aws_sqs_queue_policy" "sns_attribute_filtered" {
  queue_url = aws_sqs_queue.sns_attribute_filtered.id
  policy    = data.aws_iam_policy_document.sns_to_sqs["attribute_filtered"].json
}

data "aws_iam_policy_document" "sns_to_sqs" {
  for_each = {
    body_filtered      = aws_sqs_queue.sns_body_filtered.arn
    attribute_filtered = aws_sqs_queue.sns_attribute_filtered.arn
  }

  statement {
    sid       = "AllowTopicSend"
    effect    = "Allow"
    actions   = ["sqs:SendMessage"]
    resources = [each.value]

    principals {
      type        = "Service"
      identifiers = ["sns.amazonaws.com"]
    }

    condition {
      test     = "ArnEquals"
      variable = "aws:SourceArn"
      values   = [aws_sns_topic.events.arn]
    }
  }
}

resource "aws_sns_topic_subscription" "body_filtered" {
  topic_arn            = aws_sns_topic.events.arn
  protocol             = "sqs"
  endpoint             = aws_sqs_queue.sns_body_filtered.arn
  raw_message_delivery = local.updated ? false : true
  filter_policy_scope  = "MessageBody"

  filter_policy = local.updated ? jsonencode({
    detail = {
      type   = ["order.created", "order.updated"]
      amount = [{ numeric = [">=", 100] }]
    }
    }) : jsonencode({
    detail = {
      type = ["order.created"]
    }
  })

  depends_on = [aws_sqs_queue_policy.sns_body_filtered]
}

resource "aws_sns_topic_subscription" "attribute_filtered" {
  topic_arn = aws_sns_topic.events.arn
  protocol  = "sqs"
  endpoint  = aws_sqs_queue.sns_attribute_filtered.arn

  filter_policy = local.updated ? jsonencode({
    source = [{ prefix = "billing." }]
    }) : jsonencode({
    source = ["billing.invoice", "billing.refund"]
  })

  depends_on = [aws_sqs_queue_policy.sns_attribute_filtered]
}

output "sns_topic_arn" {
  value = aws_sns_topic.events.arn
}
