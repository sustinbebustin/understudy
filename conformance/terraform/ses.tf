# SES: an email identity and a domain identity. The update step changes the
# address, which the provider replaces (VerifyEmailIdentity, then
# DeleteIdentity of the old one). The email identity stays Pending, which
# the provider accepts: it reads only whether the key is present
# (docs/research/ses.md 9.1). The domain is listed in run.sh's
# ses.verifiedIdentities, so it starts Success and its verification
# resource completes at once instead of waiting out its timeout
# (docs/research/ses.md 9.2, 11.7).

resource "aws_ses_email_identity" "sender" {
  email = local.updated ? "${var.name_prefix}-updated@example.com" : "${var.name_prefix}@example.com"
}

resource "aws_ses_domain_identity" "verified" {
  domain = "ses-verified.example.org"
}

resource "aws_ses_domain_identity_verification" "verified" {
  domain = aws_ses_domain_identity.verified.domain
}

output "ses_email_identity_arn" {
  value = aws_ses_email_identity.sender.arn
}

output "ses_domain_verification_token" {
  value = aws_ses_domain_identity.verified.verification_token
}
