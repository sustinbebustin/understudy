# CloudWatch metrics: an alarm on the age of the oldest message in a queue
# that notifies an SNS topic. The update step changes the threshold, the
# evaluation window, the missing-data treatment and the tags.

# Fixture: the topic the alarm notifies.
resource "aws_sns_topic" "cloudwatch_alerts" {
  name = "${var.name_prefix}-alerts"
}

resource "aws_cloudwatch_metric_alarm" "queue_backlog" {
  alarm_name          = "${var.name_prefix}-queue-backlog"
  alarm_description   = local.updated ? "Oldest work message older than 10 minutes" : "Oldest work message older than 5 minutes"
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateAgeOfOldestMessage"
  statistic           = "Maximum"
  period              = 60
  evaluation_periods  = local.updated ? 10 : 5
  datapoints_to_alarm = local.updated ? 8 : 3
  threshold           = local.updated ? 600 : 300
  comparison_operator = "GreaterThanOrEqualToThreshold"
  treat_missing_data  = local.updated ? "notBreaching" : "missing"
  unit                = "Seconds"

  dimensions = {
    QueueName = "${var.name_prefix}-work"
  }

  alarm_actions = [aws_sns_topic.cloudwatch_alerts.arn]
  ok_actions    = local.updated ? [aws_sns_topic.cloudwatch_alerts.arn] : []

  tags = merge(
    { Purpose = "backlog" },
    local.updated ? { Stage = "updated" } : {},
  )
}

output "alarm_arn" {
  value = aws_cloudwatch_metric_alarm.queue_backlog.arn
}
