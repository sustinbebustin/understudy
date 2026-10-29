#!/usr/bin/env bash
# SES v1: email identities (VerifyEmailIdentity, GetIdentityVerificationAttributes,
# DeleteIdentity).
# Arbiter: docs/research/ses.md sections 5 and 9.1.

test_verify_email_identity_starts_pending() {
  local addr
  addr="$(new_name ses-pending)@example.com"
  aws_ok ses verify-email-identity --email-address "$addr"
  assert_eq 200 "$AWS_STATUS" 'VerifyEmailIdentity status'
  # Pending until the owner opens the link (ses.md 5.2), keyed by the request
  # string (ses.md 9.1).
  aws_ok ses get-identity-verification-attributes --identities "$addr"
  assert_jq ".VerificationAttributes[\"$addr\"].VerificationStatus" 'Pending'
}

test_delete_identity_removes_the_key() {
  local addr
  addr="$(new_name ses-delete)@example.com"
  aws_ok ses verify-email-identity --email-address "$addr"
  aws_ok ses delete-identity --identity "$addr"
  # Terraform reads a missing key as gone (ses.md 9.1).
  aws_ok ses get-identity-verification-attributes --identities "$addr"
  assert_jq '.VerificationAttributes | length' '0'
}
