# IAM: a role with a path, a customer managed policy, an inline role policy,
# and attachments of both a customer managed and an AWS managed policy, the
# latter looked up by name through the aws_iam_policy data source, which lists
# every policy, then reads the one named and its default version. Its ARN,
# path and actions are the ones the AWS Managed Policy Reference Guide
# publishes (iam-evaluation.md 10.3). The update step changes the role
# description, session duration and tags, adds a statement to the managed
# policy (a new default version) and rewrites the inline policy.

data "aws_iam_policy" "basic_execution" {
  name = "AWSLambdaBasicExecutionRole"

  lifecycle {
    postcondition {
      condition     = self.arn == "arn:${local.partition}:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole" && self.path == "/service-role/" && contains(jsondecode(self.policy).Statement[0].Action, "logs:CreateLogGroup")
      error_message = "data.aws_iam_policy.basic_execution: want arn:${local.partition}:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole on path /service-role/ allowing logs:CreateLogGroup, the guide's AWSLambdaBasicExecutionRole (iam-evaluation.md 10.3); got ${self.arn} on ${self.path}. The policy comes from understudy's catalogue in third_party/aws-managed-policies."
    }
  }
}

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
  policy_arn = data.aws_iam_policy.basic_execution.arn
}

output "iam_role_arn" {
  value = aws_iam_role.worker.arn
}
