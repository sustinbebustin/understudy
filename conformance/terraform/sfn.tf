# Step Functions: an execution role and a Standard state machine of one Pass
# state, with tags. The update step changes the tags; the definition stays,
# since UpdateStateMachine is not built yet (stepfunctions-api.md 11.1).

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

resource "aws_sfn_state_machine" "pass" {
  name     = "${var.name_prefix}-pass"
  role_arn = aws_iam_role.states.arn
  definition = jsonencode({
    Comment = "One Pass state"
    StartAt = "Hello"
    States = {
      Hello = { Type = "Pass", Result = { greeting = "hello" }, End = true }
    }
  })

  tags = merge(
    { Purpose = "tracer" },
    local.updated ? { Stage = "updated" } : {},
  )
}

output "state_machine_arn" {
  value = aws_sfn_state_machine.pass.arn
}
