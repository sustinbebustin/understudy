# Lambda: a Go worker on provided.al2023 fed by an SQS event source mapping
# with partial batch responses, and a Python function exposed through a
# function URL and invokable by an SNS topic. Both are zip packages with log
# groups managed here. The Python function uses a layer shared with its own
# account, and its asynchronous invocations go to an SQS queue on success
# and an SNS topic on failure. The update step changes memory, timeout,
# environment, description and the Python code, the mapping's batching, the
# URL's CORS, the layer (a new version, which the function then uses) and
# the asynchronous invocation configuration.

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

# The shared layer: every argument forces a new version, so the update
# publishes version 2 and the function moves to it
# (docs/research/lambda-destinations-layers.md 9.3).
data "archive_file" "python_layer" {
  type        = "zip"
  source_dir  = "${var.functions_dir}/python-layer"
  output_path = "${var.artifacts_dir}/lambda-python-layer.zip"
}

resource "aws_lambda_layer_version" "shared" {
  layer_name               = "${var.name_prefix}-shared"
  description              = local.updated ? "Shared helpers (updated)" : "Shared helpers"
  filename                 = data.archive_file.python_layer.output_path
  source_code_hash         = data.archive_file.python_layer.output_base64sha256
  compatible_runtimes      = local.updated ? ["python3.13", "python3.12"] : ["python3.13"]
  compatible_architectures = ["x86_64"]
}

# The provider reads Statement[0] and turns the principal back into the
# account id, so the policy must render it as {"AWS": "arn:...:root"}
# (docs/research/lambda-destinations-layers.md 9.4).
resource "aws_lambda_layer_version_permission" "shared" {
  layer_name     = aws_lambda_layer_version.shared.layer_arn
  version_number = aws_lambda_layer_version.shared.version
  statement_id   = "same-account"
  action         = "lambda:GetLayerVersion"
  principal      = local.account_id
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
  layers           = [aws_lambda_layer_version.shared.arn]

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

# Fixtures: where the Python function's asynchronous invocations go.
resource "aws_sqs_queue" "lambda_success" {
  name = "${var.name_prefix}-on-success"
}

resource "aws_sns_topic" "lambda_failure" {
  name = "${var.name_prefix}-on-failure"
}

# Lambda delivers to destinations as the execution role, which needs to
# reach each one (docs/research/lambda-destinations-layers.md 2.5).
resource "aws_iam_role_policy" "lambda_destinations" {
  name = "${var.name_prefix}-destinations"
  role = aws_iam_role.lambda_exec.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      { Effect = "Allow", Action = "sqs:SendMessage", Resource = aws_sqs_queue.lambda_success.arn },
      { Effect = "Allow", Action = "sns:Publish", Resource = aws_sns_topic.lambda_failure.arn },
    ]
  })
}

resource "aws_lambda_function_event_invoke_config" "python_api" {
  function_name                = aws_lambda_function.python_api.function_name
  maximum_retry_attempts       = local.updated ? 1 : 0
  maximum_event_age_in_seconds = local.updated ? 600 : 300

  destination_config {
    on_success {
      destination = aws_sqs_queue.lambda_success.arn
    }

    on_failure {
      destination = aws_sns_topic.lambda_failure.arn
    }
  }

  depends_on = [aws_iam_role_policy.lambda_destinations]
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
