# Step Functions: an execution role and a Standard state machine of a Choice
# routing to a Pass or a Succeed state, with tags and logging to a log group through the ":*" ARN
# DescribeLogGroups gives (stepfunctions-api.md 5.1). The update step changes
# the definition and the tags (stepfunctions-api.md 11.1).

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
    Comment = "A Choice routing to a Pass state or a Succeed state"
    StartAt = "Route"
    States = {
      Route = {
        Type = "Choice"
        Choices = [
          {
            And  = [{ Variable = "$.name", IsPresent = true }, { Not = { Variable = "$.name", StringMatches = "test-*" } }]
            Next = "Hello"
          },
        ]
        Default = "Done"
      }
      Hello = { Type = "Pass", Result = { greeting = local.updated ? "hello again" : "hello" }, End = true }
      Done  = { Type = "Succeed" }
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

output "state_machine_arn" {
  value = aws_sfn_state_machine.pass.arn
}
