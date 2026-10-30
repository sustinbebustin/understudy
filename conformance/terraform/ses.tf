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

# SES v2 identities over the same store: an address and a domain, each
# tagged; the update step changes the domain's tags in place (TagResource,
# UntagResource). The provider reads DkimAttributes, which must be non-null
# with stable tokens, and the tags through ListTagsForResource
# (docs/research/ses.md 9.3).
resource "aws_sesv2_email_identity" "address" {
  email_identity = "${var.name_prefix}-v2@example.com"
  tags = {
    env = "dev"
  }
}

resource "aws_sesv2_email_identity" "domain" {
  email_identity = "${var.name_prefix}-v2.example.org"
  tags           = local.updated ? { env = "prod", team = "mail" } : { env = "dev" }
}

output "sesv2_domain_arn" {
  value = aws_sesv2_email_identity.domain.arn
}

output "ses_email_identity_arn" {
  value = aws_ses_email_identity.sender.arn
}

output "ses_domain_verification_token" {
  value = aws_ses_domain_identity.verified.verification_token
}
