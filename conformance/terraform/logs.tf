# CloudWatch Logs: a log group per trace store, with retention and tags. The
# update step changes the retention and the tags.

resource "aws_cloudwatch_log_group" "traces" {
  name              = "/${var.name_prefix}/traces"
  retention_in_days = local.updated ? 30 : 14

  tags = merge(
    { Purpose = "traces" },
    local.updated ? { Stage = "updated" } : {},
  )
}

resource "aws_cloudwatch_log_group" "audit" {
  name = "/${var.name_prefix}/audit"
}

output "log_group_arn" {
  value = aws_cloudwatch_log_group.traces.arn
}
