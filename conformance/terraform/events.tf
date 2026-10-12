# EventBridge: a scheduled rule on the default bus that invokes a Python
# function with a constant input, and the permission that lets the rule invoke
# it. The schedules are far enough apart that the rule does not fire during a
# run. The update step changes the schedule, description and target input.

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
