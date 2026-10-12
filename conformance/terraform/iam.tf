# IAM: a role with a path, a customer managed policy, an inline role policy,
# and attachments of both a customer managed and an AWS managed policy. The
# update step changes the role description, session duration and tags, adds a
# statement to the managed policy (a new default version) and rewrites the
# inline policy.

data "aws_iam_policy_document" "worker_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "worker" {
  name                 = "${var.name_prefix}-worker"
  path                 = "/service-role/"
  description          = local.updated ? "Pipeline worker (updated)" : "Pipeline worker"
  assume_role_policy   = data.aws_iam_policy_document.worker_assume.json
  max_session_duration = local.updated ? 7200 : 3600

  tags = merge(
    { Purpose = "worker" },
    local.updated ? { Stage = "updated" } : {},
  )
}

resource "aws_iam_policy" "queue_access" {
  name        = "${var.name_prefix}-queue-access"
  path        = "/pipeline/"
  description = "Consume the work queue"

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = concat(
      [
        {
          Sid      = "ConsumeQueue"
          Effect   = "Allow"
          Action   = ["sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:GetQueueAttributes"]
          Resource = "arn:${local.partition}:sqs:${local.region}:${local.account_id}:${var.name_prefix}-*"
        },
      ],
      local.updated ? [
        {
          Sid      = "ChangeVisibility"
          Effect   = "Allow"
          Action   = "sqs:ChangeMessageVisibility"
          Resource = "arn:${local.partition}:sqs:${local.region}:${local.account_id}:${var.name_prefix}-*"
        },
      ] : [],
    )
  })

  tags = {
    Purpose = "worker"
  }
}

resource "aws_iam_role_policy" "worker_secrets" {
  name = "read-secrets"
  role = aws_iam_role.worker.id

  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = local.updated ? ["secretsmanager:GetSecretValue", "secretsmanager:DescribeSecret"] : ["secretsmanager:GetSecretValue"]
        Resource = "arn:${local.partition}:secretsmanager:${local.region}:${local.account_id}:secret:${var.name_prefix}/*"
      },
    ]
  })
}

resource "aws_iam_role_policy_attachment" "worker_queue_access" {
  role       = aws_iam_role.worker.name
  policy_arn = aws_iam_policy.queue_access.arn
}

resource "aws_iam_role_policy_attachment" "worker_basic_execution" {
  role       = aws_iam_role.worker.name
  policy_arn = "arn:${local.partition}:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

output "iam_role_arn" {
  value = aws_iam_role.worker.arn
}
