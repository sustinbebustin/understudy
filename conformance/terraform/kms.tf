# KMS: a symmetric key with rotation, an explicit key policy and tags, and an
# alias. The update step changes the description, the rotation period, the
# key policy, the tags and points the alias at a second key.

resource "aws_kms_key" "app" {
  description             = local.updated ? "Application data key (rotated yearly)" : "Application data key"
  deletion_window_in_days = 7
  enable_key_rotation     = true
  rotation_period_in_days = local.updated ? 365 : 90

  policy = jsonencode({
    Version = "2012-10-17"
    Id      = "app-key-policy"
    Statement = concat(
      [
        {
          Sid       = "EnableRootAccess"
          Effect    = "Allow"
          Principal = { AWS = "arn:${local.partition}:iam::${local.account_id}:root" }
          Action    = "kms:*"
          Resource  = "*"
        },
      ],
      local.updated ? [
        {
          Sid       = "AllowS3ViaService"
          Effect    = "Allow"
          Principal = { AWS = "arn:${local.partition}:iam::${local.account_id}:root" }
          Action    = ["kms:Decrypt", "kms:GenerateDataKey"]
          Resource  = "*"
          Condition = { StringEquals = { "kms:ViaService" = "s3.${local.region}.amazonaws.com" } }
        },
      ] : [],
    )
  })

  tags = merge(
    { Purpose = "app-data" },
    local.updated ? { Stage = "updated" } : {},
  )
}

resource "aws_kms_key" "app_next" {
  description             = "Application data key, next generation"
  deletion_window_in_days = 7
}

resource "aws_kms_alias" "app" {
  name          = "alias/${var.name_prefix}-app"
  target_key_id = local.updated ? aws_kms_key.app_next.key_id : aws_kms_key.app.key_id
}

output "kms_key_arn" {
  value = aws_kms_key.app.arn
}
