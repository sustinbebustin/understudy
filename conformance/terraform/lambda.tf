# Lambda: a Go worker on provided.al2023 fed by an SQS event source mapping
# with partial batch responses, and a Python function exposed through a
# function URL and invokable by an SNS topic. Both are zip packages with log
# groups managed here. The update step changes memory, timeout, environment,
# description and the Python code, the mapping's batching, and the URL's CORS.

data "aws_iam_policy_document" "lambda_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "lambda_exec" {
  name               = "${var.name_prefix}-exec"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume.json
}

resource "aws_iam_role_policy_attachment" "lambda_basic_execution" {
  role       = aws_iam_role.lambda_exec.name
  policy_arn = "arn:${local.partition}:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

resource "aws_iam_role_policy_attachment" "lambda_sqs_execution" {
  role       = aws_iam_role.lambda_exec.name
  policy_arn = "arn:${local.partition}:iam::aws:policy/service-role/AWSLambdaSQSQueueExecutionRole"
}

# Fixture: the work queue and its dead-letter queue the worker consumes.
resource "aws_sqs_queue" "lambda_work_dlq" {
  name = "${var.name_prefix}-work-dlq"
}

resource "aws_sqs_queue" "lambda_work" {
  name = "${var.name_prefix}-work"
  # AWS recommends at least six times the function timeout for an event
  # source queue.
  visibility_timeout_seconds = 180

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.lambda_work_dlq.arn
    maxReceiveCount     = 3
  })
}

# Fixture: the topic allowed to invoke the Python function.
resource "aws_sns_topic" "lambda_trigger" {
  name = "${var.name_prefix}-trigger"
}

data "archive_file" "go_worker" {
  type        = "zip"
  source_file = "${var.artifacts_dir}/go-worker/bootstrap"
  output_path = "${var.artifacts_dir}/lambda-go-worker.zip"
}

data "archive_file" "python_api" {
  type        = "zip"
  output_path = "${var.artifacts_dir}/lambda-python-api-${var.step}.zip"

  source {
    filename = "handler.py"
    # The update deploys new code: the same handler with a marker line.
    content = local.updated ? "${file("${var.functions_dir}/python-handler/handler.py")}\nDEPLOYMENT = \"update\"\n" : file("${var.functions_dir}/python-handler/handler.py")
  }
}

resource "aws_cloudwatch_log_group" "go_worker" {
  name              = "/aws/lambda/${var.name_prefix}-go-worker"
  retention_in_days = 7
}

resource "aws_cloudwatch_log_group" "python_api" {
  name              = "/aws/lambda/${var.name_prefix}-python-api"
  retention_in_days = 7
}

resource "aws_lambda_function" "go_worker" {
  function_name    = "${var.name_prefix}-go-worker"
  description      = local.updated ? "Queue worker (updated)" : "Queue worker"
  role             = aws_iam_role.lambda_exec.arn
  runtime          = "provided.al2023"
  handler          = "bootstrap"
  architectures    = ["x86_64"]
  filename         = data.archive_file.go_worker.output_path
  source_code_hash = data.archive_file.go_worker.output_base64sha256
  memory_size      = local.updated ? 256 : 128
  timeout          = 30

  environment {
    variables = merge(
      { LOG_LEVEL = "info" },
      local.updated ? { FEATURE_FLAGS = "partial-batch" } : {},
    )
  }

  tags = {
    Purpose = "worker"
  }

  depends_on = [
    aws_cloudwatch_log_group.go_worker,
    aws_iam_role_policy_attachment.lambda_basic_execution,
    aws_iam_role_policy_attachment.lambda_sqs_execution,
  ]
}

resource "aws_lambda_function" "python_api" {
  function_name    = "${var.name_prefix}-python-api"
  description      = "Webhook status API"
  role             = aws_iam_role.lambda_exec.arn
  runtime          = "python3.13"
  handler          = "handler.handler"
  filename         = data.archive_file.python_api.output_path
  source_code_hash = data.archive_file.python_api.output_base64sha256
  memory_size      = 128
  timeout          = local.updated ? 10 : 5

  environment {
    variables = {
      GREETING = local.updated ? "hello again" : "hello"
    }
  }

  depends_on = [
    aws_cloudwatch_log_group.python_api,
    aws_iam_role_policy_attachment.lambda_basic_execution,
  ]
}

resource "aws_lambda_event_source_mapping" "go_worker" {
  function_name                      = aws_lambda_function.go_worker.arn
  event_source_arn                   = aws_sqs_queue.lambda_work.arn
  batch_size                         = local.updated ? 25 : 10
  maximum_batching_window_in_seconds = local.updated ? 5 : 0
  function_response_types            = ["ReportBatchItemFailures"]

  scaling_config {
    maximum_concurrency = local.updated ? 5 : 2
  }
}

resource "aws_lambda_function_url" "python_api" {
  function_name      = aws_lambda_function.python_api.function_name
  authorization_type = "NONE"

  cors {
    allow_origins = local.updated ? ["https://example.com", "https://admin.example.com"] : ["https://example.com"]
    allow_methods = ["GET", "POST"]
    allow_headers = ["content-type"]
    max_age       = local.updated ? 600 : 300
  }
}

resource "aws_lambda_permission" "python_api_from_sns" {
  statement_id  = "AllowSNSInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.python_api.function_name
  principal     = "sns.amazonaws.com"
  source_arn    = aws_sns_topic.lambda_trigger.arn
}

output "lambda_function_url" {
  value = aws_lambda_function_url.python_api.function_url
}
