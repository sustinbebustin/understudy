# EventBridge Scheduler: a tagged schedule group, and a rate schedule in
# the default group sending to an SQS queue as an execution role Scheduler
# may assume, whose policy grants
# sqs:SendMessage on the queue (scheduler.md 9, 11.1). The empty plans
# depend on GetSchedule reading back the members as sent with AWS's
# defaults filled: ActionAfterCompletion NONE, RetryPolicy 86400/185, group
# default, no dates, description or KMS key (scheduler.md 4.2, 11.3); and a
# cron schedule in Europe/Paris with dates, a description, a FLEXIBLE 10
# window, ActionAfterCompletion DELETE, a retry policy, a dead-letter queue
# and Input, echoed as sent with the dates to the second (tf-acc
# schedule_test.go:L288-L302, L365-L419, L421-L478, L1174-L1233,
# L1292-L1349, L1870-L1895). The
# update step replaces both schedules through UpdateSchedule (scheduler.md
# 4.3): the rate gains a description, the cron moves its StartDate and
# retry policy.

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

# The dead-letter queue of the cron and bus schedules: a standard queue
# the role may send to (aws-doc configuring-schedule-dlq; scheduler.md
# 7.2). A bus target names one too, as the census pattern
# eventbridge-schedule-to-eventbridge-terraform does (scheduler.md 14.5.3).
resource "aws_sqs_queue" "scheduler_dlq" {
  name = "${var.name_prefix}-dlq"
}

resource "aws_iam_role" "scheduler" {
  name               = "${var.name_prefix}-role"
  assume_role_policy = data.aws_iam_policy_document.scheduler_assume.json
}

data "aws_iam_policy_document" "scheduler_send" {
  statement {
    effect    = "Allow"
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.scheduler_target.arn, aws_sqs_queue.scheduler_dlq.arn]
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
  description         = local.updated ? "hourly" : null

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

# A tagged schedule group holding the cron schedule: created ACTIVE, its
# tags (with the provider's default_tags) listed back, changed in place by
# TagResource and UntagResource on update, and deleted with its schedule
# (tf-acc schedule_group_test.go:L25-L72, L174-L232; scheduler.md 3.2-3.4,
# 11.2).
resource "aws_scheduler_schedule_group" "scheduler" {
  name = "${var.name_prefix}-group"

  tags = local.updated ? { stage = "updated" } : { stage = "created", team = "conformance" }
}

resource "aws_scheduler_schedule" "cron" {
  name                         = "${var.name_prefix}-cron"
  group_name                   = aws_scheduler_schedule_group.scheduler.name
  schedule_expression          = "cron(0 8 ? * MON-FRI *)"
  schedule_expression_timezone = "Europe/Paris"
  start_date                   = local.updated ? "2099-06-01T00:00:00Z" : "2099-01-01T01:00:00Z"
  end_date                     = "2100-01-01T01:02:03Z"
  description                  = "weekday mornings"
  action_after_completion      = "DELETE"

  flexible_time_window {
    mode                      = "FLEXIBLE"
    maximum_window_in_minutes = 10
  }

  target {
    arn      = aws_sqs_queue.scheduler_target.arn
    role_arn = aws_iam_role.scheduler.arn
    input    = jsonencode({ at = "<aws.scheduler.scheduled-time>" })

    retry_policy {
      maximum_event_age_in_seconds = local.updated ? 120 : 60
      maximum_retry_attempts       = 1
    }

    dead_letter_config {
      arn = aws_sqs_queue.scheduler_dlq.arn
    }
  }

  depends_on = [aws_iam_role_policy.scheduler_send]
}

# An SNS topic target and a default-bus target with eventbridge_parameters
# (scheduler.md 6.1, 6.4): the empty plans depend on the parameter block
# reading back as sent and on no other block appearing (scheduler.md 4.2).
resource "aws_sns_topic" "scheduler_target" {
  name = "${var.name_prefix}-topic"
}

data "aws_iam_policy_document" "scheduler_publish" {
  statement {
    effect    = "Allow"
    actions   = ["sns:Publish"]
    resources = [aws_sns_topic.scheduler_target.arn]
  }

  statement {
    effect    = "Allow"
    actions   = ["events:PutEvents"]
    resources = ["arn:aws:events:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:event-bus/default"]
  }
}

resource "aws_iam_role_policy" "scheduler_publish" {
  name   = "publish"
  role   = aws_iam_role.scheduler.id
  policy = data.aws_iam_policy_document.scheduler_publish.json
}

resource "aws_scheduler_schedule" "topic" {
  name                = "${var.name_prefix}-topic"
  schedule_expression = "rate(1 day)"

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = aws_sns_topic.scheduler_target.arn
    role_arn = aws_iam_role.scheduler.arn
  }

  depends_on = [aws_iam_role_policy.scheduler_publish]
}

resource "aws_scheduler_schedule" "bus" {
  name                = "${var.name_prefix}-bus"
  schedule_expression = "rate(1 day)"

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = "arn:aws:events:${data.aws_region.current.region}:${data.aws_caller_identity.current.account_id}:event-bus/default"
    role_arn = aws_iam_role.scheduler.arn
    input    = jsonencode({ tick = "<aws.scheduler.attempt-number>" })

    eventbridge_parameters {
      source      = "conformance.scheduler"
      detail_type = local.updated ? "tock" : "tick"
    }

    dead_letter_config {
      arn = aws_sqs_queue.scheduler_dlq.arn
    }
  }

  depends_on = [aws_iam_role_policy.scheduler_publish, aws_iam_role_policy.scheduler_send]
}

# A Step Functions state machine target, the role allowed
# states:StartExecution on it (scheduler.md 6.4, 9): the empty plans
# depend on the target reading back as sent with no parameter block
# (scheduler.md 4.2).
data "aws_iam_policy_document" "scheduler_states_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["states.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "scheduler_states" {
  name               = "${var.name_prefix}-states"
  assume_role_policy = data.aws_iam_policy_document.scheduler_states_assume.json
}

resource "aws_sfn_state_machine" "scheduler_target" {
  name     = "${var.name_prefix}-machine"
  role_arn = aws_iam_role.scheduler_states.arn
  definition = jsonencode({
    StartAt = "Done"
    States  = { Done = { Type = "Pass", End = true } }
  })
}

data "aws_iam_policy_document" "scheduler_start" {
  statement {
    effect    = "Allow"
    actions   = ["states:StartExecution"]
    resources = [aws_sfn_state_machine.scheduler_target.arn]
  }
}

resource "aws_iam_role_policy" "scheduler_start" {
  name   = "start"
  role   = aws_iam_role.scheduler.id
  policy = data.aws_iam_policy_document.scheduler_start.json
}

resource "aws_scheduler_schedule" "machine" {
  name                = "${var.name_prefix}-machine"
  schedule_expression = "rate(1 day)"

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = aws_sfn_state_machine.scheduler_target.arn
    role_arn = aws_iam_role.scheduler.arn
    input    = jsonencode({ attempt = local.updated ? "<aws.scheduler.attempt-number>" : "first" })
  }

  depends_on = [aws_iam_role_policy.scheduler_start]
}

output "scheduler_schedule_arn" {
  value = aws_scheduler_schedule.rate.arn
}
