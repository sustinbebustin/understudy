# DynamoDB: the lock table of the target workload, on-demand, with TTL on
# ExpiresAt, point-in-time recovery and tags. The update step changes the
# tags and the PITR recovery period; TTL is left alone because AWS limits TTL
# changes to one per hour.

resource "aws_dynamodb_table" "locks" {
  name         = "${var.name_prefix}-locks"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "LockID"

  attribute {
    name = "LockID"
    type = "S"
  }

  ttl {
    attribute_name = "ExpiresAt"
    enabled        = true
  }

  point_in_time_recovery {
    enabled                 = true
    recovery_period_in_days = local.updated ? 14 : 35
  }

  tags = merge(
    { Purpose = "locks" },
    local.updated ? { Stage = "updated" } : {},
  )
}

output "dynamodb_table_arn" {
  value = aws_dynamodb_table.locks.arn
}
