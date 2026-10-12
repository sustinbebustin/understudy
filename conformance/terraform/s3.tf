# S3: a bucket with a public access block, BucketOwnerEnforced ownership,
# SSE-KMS default encryption with a bucket key, versioning, a lifecycle
# configuration, a bucket policy and an event notification to SQS. The update
# step changes tags, lifecycle rules, the policy and the notification filter.

# Fixture: the KMS key the bucket encrypts with.
resource "aws_kms_key" "s3_objects" {
  description             = "${var.name_prefix} bucket encryption"
  deletion_window_in_days = 7
}

# Fixture: the queue object-created events go to, and a policy that lets the
# bucket send to it.
resource "aws_sqs_queue" "s3_uploads" {
  name = "${var.name_prefix}-uploads"
}

resource "aws_sqs_queue_policy" "s3_uploads" {
  queue_url = aws_sqs_queue.s3_uploads.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "AllowBucketNotifications"
        Effect    = "Allow"
        Principal = { Service = "s3.amazonaws.com" }
        Action    = "sqs:SendMessage"
        Resource  = aws_sqs_queue.s3_uploads.arn
        Condition = {
          ArnEquals    = { "aws:SourceArn" = aws_s3_bucket.artifacts.arn }
          StringEquals = { "aws:SourceAccount" = local.account_id }
        }
      },
    ]
  })
}

resource "aws_s3_bucket" "artifacts" {
  bucket        = "${var.name_prefix}-artifacts-${var.run_id}"
  force_destroy = true

  tags = merge(
    { Purpose = "artifacts" },
    local.updated ? { Stage = "updated" } : {},
  )
}

resource "aws_s3_bucket_public_access_block" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = aws_kms_key.s3_objects.arn
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_versioning" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  rule {
    id     = "expire-tmp"
    status = "Enabled"

    filter {
      prefix = "tmp/"
    }

    expiration {
      days = local.updated ? 3 : 7
    }
  }

  rule {
    id     = "trim-noncurrent"
    status = "Enabled"

    filter {}

    noncurrent_version_expiration {
      noncurrent_days = local.updated ? 60 : 30
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = local.updated ? 2 : 1
    }
  }

  # The noncurrent-version rule only means something once versioning is on.
  depends_on = [aws_s3_bucket_versioning.artifacts]
}

resource "aws_s3_bucket_policy" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = concat(
      [
        {
          Sid       = "DenyInsecureTransport"
          Effect    = "Deny"
          Principal = "*"
          Action    = "s3:*"
          Resource  = [aws_s3_bucket.artifacts.arn, "${aws_s3_bucket.artifacts.arn}/*"]
          Condition = { Bool = { "aws:SecureTransport" = "false" } }
        },
      ],
      local.updated ? [
        {
          Sid       = "AllowAccountRead"
          Effect    = "Allow"
          Principal = { AWS = "arn:${local.partition}:iam::${local.account_id}:root" }
          Action    = ["s3:GetObject", "s3:ListBucket"]
          Resource  = [aws_s3_bucket.artifacts.arn, "${aws_s3_bucket.artifacts.arn}/*"]
        },
      ] : [],
    )
  })

  # Write the policy after the public access block, so block_public_policy
  # already applies when the policy is checked.
  depends_on = [aws_s3_bucket_public_access_block.artifacts]
}

resource "aws_s3_bucket_notification" "artifacts" {
  bucket = aws_s3_bucket.artifacts.id

  queue {
    id            = "uploads"
    queue_arn     = aws_sqs_queue.s3_uploads.arn
    events        = local.updated ? ["s3:ObjectCreated:*", "s3:ObjectRemoved:*"] : ["s3:ObjectCreated:*"]
    filter_prefix = "uploads/"
    filter_suffix = local.updated ? ".json" : null
  }

  depends_on = [aws_sqs_queue_policy.s3_uploads]
}

output "s3_bucket" {
  value = aws_s3_bucket.artifacts.bucket
}
