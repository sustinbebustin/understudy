# Kinesis Data Streams to Lambda: a provisioned stream with tags, and a
# Python function reading it through an event source mapping that starts at
# LATEST (kinesis.md 10 and 13.6, lambda-stream-sources.md 9 and 12.8). The
# create path depends on IncreaseStreamRetentionPeriod accepting the current
# value (kinesis.md finding 3), and the empty plans on DescribeStreamSummary
# answering EncryptionType NONE, StreamModeDetails PROVISIONED and
# MaxRecordSizeInKiB 1024 (10.3). The update step raises the retention,
# enables a shard-level metric (stored only, 4.12), changes the tags and the
# mapping's batch size; shard_count and stream_mode stay, since resharding and
# UpdateStreamMode answer 501 (13.1).

data "aws_iam_policy_document" "kinesis_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "kinesis_exec" {
  name               = "${var.name_prefix}-exec"
  assume_role_policy = data.aws_iam_policy_document.kinesis_assume.json
}

# AWSLambdaKinesisExecutionRole grants the Kinesis read actions a mapping
# uses and the logs actions (lambda-stream-sources.md 13.1).
resource "aws_iam_role_policy_attachment" "kinesis_execution" {
  role       = aws_iam_role.kinesis_exec.name
  policy_arn = "arn:${local.partition}:iam::aws:policy/service-role/AWSLambdaKinesisExecutionRole"
}

resource "aws_kinesis_stream" "events" {
  name                = "${var.name_prefix}-events"
  shard_count         = 2
  retention_period    = local.updated ? 48 : 24
  shard_level_metrics = local.updated ? ["IncomingBytes"] : []

  tags = local.updated ? {
    team  = "conformance"
    stage = "updated"
    } : {
    team = "conformance"
  }
}

data "archive_file" "kinesis_handler" {
  type        = "zip"
  output_path = "${var.artifacts_dir}/kinesis-handler.zip"

  source {
    filename = "handler.py"
    content  = file("${var.functions_dir}/python-handler/handler.py")
  }
}

resource "aws_cloudwatch_log_group" "kinesis_handler" {
  name              = "/aws/lambda/${var.name_prefix}-handler"
  retention_in_days = 7
}

resource "aws_lambda_function" "kinesis_handler" {
  function_name    = "${var.name_prefix}-handler"
  role             = aws_iam_role.kinesis_exec.arn
  runtime          = "python3.13"
  handler          = "handler.handler"
  filename         = data.archive_file.kinesis_handler.output_path
  source_code_hash = data.archive_file.kinesis_handler.output_base64sha256
  timeout          = 5

  depends_on = [
    aws_cloudwatch_log_group.kinesis_handler,
    aws_iam_role_policy_attachment.kinesis_execution,
  ]
}

# Without a batch_size the provider suppresses the diff at Kinesis's default
# of 100 (kinesis.md 8.2); the other stream settings read back as AWS's
# defaults (lambda-stream-sources.md 3.5, 12.8), so the second plan is empty.
resource "aws_lambda_event_source_mapping" "kinesis_handler" {
  function_name     = aws_lambda_function.kinesis_handler.arn
  event_source_arn  = aws_kinesis_stream.events.arn
  starting_position = "LATEST"
  batch_size        = local.updated ? 50 : null
}

output "kinesis_stream_arn" {
  value = aws_kinesis_stream.events.arn
}
