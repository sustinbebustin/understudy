# Secrets Manager: a secret encrypted with a customer managed KMS key, with a
# JSON secret value. The update step changes the description, the tags and
# the secret value, which stages a new AWSCURRENT version.

# Fixture: the key the secret is encrypted with.
resource "aws_kms_key" "secrets" {
  description             = "${var.name_prefix} secrets"
  deletion_window_in_days = 7
}

resource "aws_secretsmanager_secret" "webhook" {
  name                    = "${var.name_prefix}/webhook-signing"
  description             = local.updated ? "Webhook signing key (rotated)" : "Webhook signing key"
  kms_key_id              = aws_kms_key.secrets.arn
  recovery_window_in_days = 0

  tags = merge(
    { Purpose = "webhooks" },
    local.updated ? { Stage = "updated" } : {},
  )
}

resource "aws_secretsmanager_secret_version" "webhook" {
  secret_id = aws_secretsmanager_secret.webhook.id

  secret_string = jsonencode({
    signing_key = local.updated ? "whsec-conformance-2" : "whsec-conformance-1"
    algorithm   = "HMAC-SHA256"
  })
}

output "secret_arn" {
  value = aws_secretsmanager_secret.webhook.arn
}
