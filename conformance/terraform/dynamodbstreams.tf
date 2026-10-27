# DynamoDB Streams to Lambda: a table with a stream and a Python function
# reading it through an event source mapping that starts at LATEST
# (dynamodb-streams.md 11.7, lambda-stream-sources.md 9 and 12.8). The
# provider never calls the Streams API, so this checks the DynamoDB side:
# LatestStreamArn and LatestStreamLabel identical on every read, and the
# mapping's stream defaults read back as AWS answers them. The update step
# changes the view type, which the provider applies as a disable and an
# enable (a new stream ARN), so the mapping, whose event_source_arn is
# ForceNew, is replaced.

data "aws_iam_policy_document" "streams_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "streams_exec" {
  name               = "${var.name_prefix}-exec"
  assume_role_policy = data.aws_iam_policy_document.streams_assume.json
}

# AWSLambdaDynamoDBExecutionRole grants exactly the four Streams read
# actions and the logs actions (dynamodb-streams.md 3.2).
resource "aws_iam_role_policy_attachment" "streams_execution" {
  role       = aws_iam_role.streams_exec.name
  policy_arn = "arn:${local.partition}:iam::aws:policy/service-role/AWSLambdaDynamoDBExecutionRole"
}

resource "aws_dynamodb_table" "streamed" {
  name         = "${var.name_prefix}-orders"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "id"

  attribute {
    name = "id"
    type = "S"
  }

  stream_enabled   = true
  stream_view_type = local.updated ? "NEW_IMAGE" : "NEW_AND_OLD_IMAGES"
}

data "archive_file" "streams_handler" {
  type        = "zip"
  output_path = "${var.artifacts_dir}/dynamodbstreams-handler.zip"

  source {
    filename = "handler.py"
    content  = file("${var.functions_dir}/python-handler/handler.py")
  }
}

resource "aws_cloudwatch_log_group" "streams_handler" {
  name              = "/aws/lambda/${var.name_prefix}-handler"
  retention_in_days = 7
}

resource "aws_lambda_function" "streams_handler" {
  function_name    = "${var.name_prefix}-handler"
  role             = aws_iam_role.streams_exec.arn
  runtime          = "python3.13"
  handler          = "handler.handler"
  filename         = data.archive_file.streams_handler.output_path
  source_code_hash = data.archive_file.streams_handler.output_base64sha256
  timeout          = 5

  depends_on = [
    aws_cloudwatch_log_group.streams_handler,
    aws_iam_role_policy_attachment.streams_execution,
  ]
}

# Only starting_position is set: batch_size, maximum_record_age_in_seconds,
# maximum_retry_attempts and parallelization_factor read back as AWS's
# defaults (lambda-stream-sources.md 3.5, 12.8), so the second plan is empty.
resource "aws_lambda_event_source_mapping" "streams_handler" {
  function_name     = aws_lambda_function.streams_handler.arn
  event_source_arn  = aws_dynamodb_table.streamed.stream_arn
  starting_position = "LATEST"
}

output "dynamodbstreams_stream_arn" {
  value = aws_dynamodb_table.streamed.stream_arn
}
