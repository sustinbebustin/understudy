# Step Functions: an execution role and a Standard state machine of a Choice
# routing to a Wait then a Parallel whose branches are a Pass and an inline
# Map, or to a Succeed state, under a top-level TimeoutSeconds, with tags
# and logging to a log group through the ":*" ARN
# DescribeLogGroups gives (stepfunctions-api.md 5.1); and a Lambda-task
# machine that invokes a Python function through arn:aws:states:::lambda:invoke
# as the execution role, which a role policy lets invoke it
# (stepfunctions-api.md 9.5), with tags and logging to its own log group. The
# update step changes both definitions and the tags (stepfunctions-api.md
# 11.1).

data "aws_iam_policy_document" "states_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["states.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "states" {
  name               = "${var.name_prefix}-states"
  assume_role_policy = data.aws_iam_policy_document.states_assume.json
}

resource "aws_cloudwatch_log_group" "states" {
  name = "/aws/vendedlogs/states/${var.name_prefix}-pass"
}

resource "aws_sfn_state_machine" "pass" {
  name     = "${var.name_prefix}-pass"
  role_arn = aws_iam_role.states.arn
  definition = jsonencode({
    Comment        = "A Choice routing to a Wait then a Parallel of a Pass and a Map, or to a Succeed state"
    StartAt        = "Route"
    TimeoutSeconds = 3600
    States = {
      Route = {
        Type = "Choice"
        Choices = [
          {
            And  = [{ Variable = "$.name", IsPresent = true }, { Not = { Variable = "$.name", StringMatches = "test-*" } }]
            Next = "Pause"
          },
        ]
        Default = "Done"
      }
      Pause = { Type = "Wait", SecondsPath = "$.delay", Next = "Fan" }
      Fan = {
        Type = "Parallel"
        Branches = [
          {
            StartAt = "Hello"
            States = {
              Hello = { Type = "Pass", Result = { greeting = local.updated ? "hello again" : "hello" }, End = true }
            }
          },
          {
            StartAt = "Each"
            States = {
              Each = {
                Type           = "Map"
                ItemsPath      = "$.items"
                MaxConcurrency = 2
                ItemSelector   = { "item.$" = "$$.Map.Item.Value", "index.$" = "$$.Map.Item.Index" }
                ItemProcessor = {
                  ProcessorConfig = { Mode = "INLINE" }
                  StartAt         = "Item"
                  States          = { Item = { Type = "Pass", End = true } }
                }
                End = true
              }
            }
          },
        ]
        End = true
      }
      Done = { Type = "Succeed" }
    }
  })

  logging_configuration {
    level                  = "ALL"
    include_execution_data = true
    log_destination        = "${aws_cloudwatch_log_group.states.arn}:*"
  }

  tags = merge(
    { Purpose = "tracer" },
    local.updated ? { Stage = "updated" } : {},
  )
}

data "aws_iam_policy_document" "states_lambda_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

# Fixture: the function the Task invokes and its role.
resource "aws_iam_role" "states_worker" {
  name               = "${var.name_prefix}-states-worker"
  assume_role_policy = data.aws_iam_policy_document.states_lambda_assume.json
}

data "archive_file" "states_worker" {
  type        = "zip"
  source_file = "${var.functions_dir}/python-handler/handler.py"
  output_path = "${var.artifacts_dir}/states-worker.zip"
}

resource "aws_lambda_function" "states_worker" {
  function_name    = "${var.name_prefix}-states-worker"
  role             = aws_iam_role.states_worker.arn
  runtime          = "python3.13"
  handler          = "handler.handler"
  filename         = data.archive_file.states_worker.output_path
  source_code_hash = data.archive_file.states_worker.output_base64sha256
}

# The execution role may invoke the function (stepfunctions-api.md 9.5).
resource "aws_iam_role_policy" "states_invoke" {
  name = "invoke-worker"
  role = aws_iam_role.states.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = "lambda:InvokeFunction"
      Resource = [aws_lambda_function.states_worker.arn, "${aws_lambda_function.states_worker.arn}:*"]
    }]
  })
}

resource "aws_cloudwatch_log_group" "states_task" {
  name = "/aws/vendedlogs/states/${var.name_prefix}-task"
}

resource "aws_sfn_state_machine" "task" {
  name     = "${var.name_prefix}-task"
  role_arn = aws_iam_role.states.arn
  definition = jsonencode({
    Comment = "A Lambda task through lambda:invoke"
    StartAt = "Work"
    States = {
      Work = {
        Type     = "Task"
        Resource = "arn:${local.partition}:states:::lambda:invoke"
        Parameters = {
          FunctionName = aws_lambda_function.states_worker.arn
          "Payload.$"  = "$"
        }
        ResultSelector = { "body.$" = "$.Payload" }
        TimeoutSeconds = local.updated ? 120 : 60
        End            = true
      }
    }
  })

  logging_configuration {
    level                  = "ERROR"
    include_execution_data = false
    log_destination        = "${aws_cloudwatch_log_group.states_task.arn}:*"
  }

  tags = merge(
    { Purpose = "lambda-task" },
    local.updated ? { Stage = "updated" } : {},
  )

  depends_on = [aws_iam_role_policy.states_invoke]
}

output "state_machine_arn" {
  value = aws_sfn_state_machine.pass.arn
}

output "task_state_machine_arn" {
  value = aws_sfn_state_machine.task.arn
}
