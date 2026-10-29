# SES: an email identity. The update step changes the address, which the
# provider replaces (VerifyEmailIdentity, then DeleteIdentity of the old
# one). The identity stays Pending, which the provider accepts: it reads
# only whether the key is present (docs/research/ses.md 9.1).

resource "aws_ses_email_identity" "sender" {
  email = local.updated ? "${var.name_prefix}-updated@example.com" : "${var.name_prefix}@example.com"
}

output "ses_email_identity_arn" {
  value = aws_ses_email_identity.sender.arn
}
