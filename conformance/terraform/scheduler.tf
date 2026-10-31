# EventBridge Scheduler: a rate schedule in the default group sending to an
# SQS queue as an execution role Scheduler may assume, whose policy grants
# sqs:SendMessage on the queue (scheduler.md 9, 11.1). The empty plans
# depend on GetSchedule reading back the members as sent with AWS's
# defaults filled: ActionAfterCompletion NONE, RetryPolicy 86400/185, group
# default, no dates, description or KMS key (scheduler.md 4.2, 11.3). The
# update step changes only the queue's tags: UpdateSchedule is not built
# yet, so the schedule itself stays as created.

data "aws_iam_policy_document" "scheduler_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["scheduler.amazonaws.com"]
    }
  }
}

resource "aws_sqs_queue" "scheduler_target" {
  name = "${var.name_prefix}-target"

  tags = local.updated ? { stage = "updated" } : {}
}

resource "aws_iam_role" "scheduler" {
  name               = "${var.name_prefix}-role"
  assume_role_policy = data.aws_iam_policy_document.scheduler_assume.json
}

data "aws_iam_policy_document" "scheduler_send" {
  statement {
    effect    = "Allow"
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.scheduler_target.arn]
  }
}

resource "aws_iam_role_policy" "scheduler_send" {
  name   = "send"
  role   = aws_iam_role.scheduler.id
  policy = data.aws_iam_policy_document.scheduler_send.json
}

resource "aws_scheduler_schedule" "rate" {
  name                = "${var.name_prefix}-rate"
  schedule_expression = "rate(1 hour)"

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = aws_sqs_queue.scheduler_target.arn
    role_arn = aws_iam_role.scheduler.arn
    input    = "hello <aws.scheduler.attempt-number>"
  }

  depends_on = [aws_iam_role_policy.scheduler_send]
}

output "scheduler_schedule_arn" {
  value = aws_scheduler_schedule.rate.arn
}
