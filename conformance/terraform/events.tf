# EventBridge: a scheduled rule on the default bus that invokes a Python
# function with a constant input, and the permission that lets the rule invoke
# it. A rate rule fires at its creation (testdata/parity/events/R14/02-arrivals.jsonl),
# which understudy delivers to the rule's first target, so the target waits for
# the permission; after that the schedules are far enough apart that the rule
# does not fire again during a run. The update step changes the schedule,
# description and target input.
#
# Custom buses (docs/research/eventbridge-buses.md 8, 10.6): a tagged bus
# with a pattern rule and an SQS target addressed through the bus ARN and a
# whole-bus policy, and a second bus with one permission statement. The
# update step changes the bus description, tags and log config, the rule's
# pattern and description, the target's input path, the policy's principal,
# and the permission's principal from an account to "*". Nothing touches the
# default bus, which a write would store and the leftover check would list.

data "aws_iam_policy_document" "events_lambda_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

# Fixture: the scheduled function, its role and its log group.
resource "aws_iam_role" "events_task" {
  name               = "${var.name_prefix}-task"
  assume_role_policy = data.aws_iam_policy_document.events_lambda_assume.json
}

resource "aws_iam_role_policy_attachment" "events_task_basic_execution" {
  role       = aws_iam_role.events_task.name
  policy_arn = "arn:${local.partition}:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

data "archive_file" "events_task" {
  type        = "zip"
  source_file = "${var.functions_dir}/python-handler/handler.py"
  output_path = "${var.artifacts_dir}/events-task.zip"
}

resource "aws_cloudwatch_log_group" "events_task" {
  name              = "/aws/lambda/${var.name_prefix}-nightly-report"
  retention_in_days = 7
}

resource "aws_lambda_function" "events_task" {
  function_name    = "${var.name_prefix}-nightly-report"
  role             = aws_iam_role.events_task.arn
  runtime          = "python3.13"
  handler          = "handler.handler"
  filename         = data.archive_file.events_task.output_path
  source_code_hash = data.archive_file.events_task.output_base64sha256

  depends_on = [
    aws_cloudwatch_log_group.events_task,
    aws_iam_role_policy_attachment.events_task_basic_execution,
  ]
}

resource "aws_cloudwatch_event_rule" "nightly" {
  name                = "${var.name_prefix}-nightly"
  description         = local.updated ? "Weekly report" : "Daily report"
  schedule_expression = local.updated ? "cron(0 3 ? * MON *)" : "rate(1 day)"

  tags = merge(
    { Purpose = "reports" },
    local.updated ? { Stage = "updated" } : {},
  )
}

resource "aws_cloudwatch_event_target" "nightly" {
  rule      = aws_cloudwatch_event_rule.nightly.name
  target_id = "report"
  arn       = aws_lambda_function.events_task.arn

  input = jsonencode({
    source = "conformance.schedule"
    report = local.updated ? "weekly" : "daily"
  })

  retry_policy {
    maximum_event_age_in_seconds = 3600
    maximum_retry_attempts       = local.updated ? 4 : 2
  }

  depends_on = [aws_lambda_permission.events_task_from_rule]
}

resource "aws_lambda_permission" "events_task_from_rule" {
  statement_id  = "AllowEventBridgeInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.events_task.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.nightly.arn
}

output "events_rule_arn" {
  value = aws_cloudwatch_event_rule.nightly.arn
}

resource "aws_cloudwatch_event_bus" "orders" {
  name        = "${var.name_prefix}-orders"
  description = local.updated ? "Order events (updated)" : "Order events"

  # NONE and OFF are read back as sent (TF-ACC bus_test.go L335-L339,
  # eventbridge-buses.md 8.1).
  dynamic "log_config" {
    for_each = local.updated ? [1] : []
    content {
      include_detail = "NONE"
      level          = "OFF"
    }
  }

  tags = merge(
    { Purpose = "orders" },
    local.updated ? { Stage = "updated" } : {},
  )
}

# Fixture: the queue the custom-bus rule sends to.
resource "aws_sqs_queue" "events_orders" {
  name = "${var.name_prefix}-orders"
}

resource "aws_cloudwatch_event_rule" "orders" {
  name           = "${var.name_prefix}-orders"
  event_bus_name = aws_cloudwatch_event_bus.orders.arn
  description    = local.updated ? "Paid orders" : "Placed orders"

  event_pattern = jsonencode({
    source        = ["conformance.orders"]
    "detail-type" = [local.updated ? "Order Paid" : "Order Placed"]
  })

  tags = { Purpose = "orders" }
}

resource "aws_cloudwatch_event_target" "orders" {
  event_bus_name = aws_cloudwatch_event_bus.orders.arn
  rule           = aws_cloudwatch_event_rule.orders.name
  target_id      = "queue"
  arn            = aws_sqs_queue.events_orders.arn
  input_path     = local.updated ? "$.detail.order" : "$.detail"
}

data "aws_iam_policy_document" "events_orders_bus" {
  statement {
    sid       = "AllowPartnerPutEvents"
    effect    = "Allow"
    actions   = ["events:PutEvents"]
    resources = [aws_cloudwatch_event_bus.orders.arn]

    principals {
      type        = "AWS"
      identifiers = [local.updated ? "arn:${local.partition}:iam::222222222222:root" : "arn:${local.partition}:iam::111111111111:root"]
    }
  }
}

resource "aws_cloudwatch_event_bus_policy" "orders" {
  event_bus_name = aws_cloudwatch_event_bus.orders.name
  policy         = data.aws_iam_policy_document.events_orders_bus.json
}

resource "aws_cloudwatch_event_bus" "partners" {
  name = "${var.name_prefix}-partners"
}

resource "aws_cloudwatch_event_permission" "partners" {
  event_bus_name = aws_cloudwatch_event_bus.partners.name
  statement_id   = "PartnerAccount"
  principal      = local.updated ? "*" : "111111111111"
  action         = "events:PutEvents"
}

output "events_orders_rule_arn" {
  value = aws_cloudwatch_event_rule.orders.arn
}
