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
# update step replaces the schedules through UpdateSchedule (scheduler.md
# 4.3): the rate gains a description; the cron moves its StartDate, widens
# its window to 20 and drops its optional members, which read back at their
# defaults; the group's tags change.

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

# The key the cron schedule names: stored and echoed, never used to
# encrypt (scheduler.md 9, 14.13); the role may decrypt with it.
resource "aws_kms_key" "scheduler" {
  description             = "scheduler payloads"
  deletion_window_in_days = 7
}

data "aws_iam_policy_document" "scheduler_decrypt" {
  statement {
    effect    = "Allow"
    actions   = ["kms:Decrypt"]
    resources = [aws_kms_key.scheduler.arn]
  }
}

resource "aws_iam_role_policy" "scheduler_decrypt" {
  name   = "decrypt"
  role   = aws_iam_role.scheduler.id
  policy = data.aws_iam_policy_document.scheduler_decrypt.json
}

# The update scenario (tf-acc schedule_test.go L409, L467, L556, L932,
# L1338; scheduler.md 4.3, 11.3): UpdateSchedule replaces every
# member, so the members the update drops (description, end_date,
# kms_key_arn, retry_policy, dead_letter_config) read back absent or at
# their defaults (RetryPolicy 86400/185) and the second plan stays empty;
# the window goes from 10 to 20 minutes and StartDate moves.
resource "aws_scheduler_schedule" "cron" {
  name                         = "${var.name_prefix}-cron"
  group_name                   = aws_scheduler_schedule_group.scheduler.name
  schedule_expression          = "cron(0 8 ? * MON-FRI *)"
  schedule_expression_timezone = "Europe/Paris"
  start_date                   = local.updated ? "2099-06-01T00:00:00Z" : "2099-01-01T01:00:00Z"
  end_date                     = local.updated ? null : "2100-01-01T01:02:03Z"
  description                  = local.updated ? null : "weekday mornings"
  action_after_completion      = "DELETE"
  kms_key_arn                  = local.updated ? null : aws_kms_key.scheduler.arn

  flexible_time_window {
    mode                      = "FLEXIBLE"
    maximum_window_in_minutes = local.updated ? 20 : 10
  }

  target {
    arn      = aws_sqs_queue.scheduler_target.arn
    role_arn = aws_iam_role.scheduler.arn
    input    = jsonencode({ at = "<aws.scheduler.scheduled-time>" })

    dynamic "retry_policy" {
      for_each = local.updated ? [] : [1]
      content {
        maximum_event_age_in_seconds = 60
        maximum_retry_attempts       = 1
      }
    }

    dynamic "dead_letter_config" {
      for_each = local.updated ? [] : [1]
      content {
        arn = aws_sqs_queue.scheduler_dlq.arn
      }
    }
  }

  depends_on = [aws_iam_role_policy.scheduler_send, aws_iam_role_policy.scheduler_decrypt]
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
