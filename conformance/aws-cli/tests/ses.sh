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

test_verification_email_is_recorded() {
  local addr
  addr="$(new_name ses-mail)@example.com"
  aws_ok ses verify-email-identity --email-address "$addr"
  # The verification email comes from no-reply-aws@amazon.com, its subject
  # naming the Region (ses.md 5.2); understudy records it instead of sending
  # it, readable through the admin API (ses.md 11.2).
  AWS_OUT=$(curl -fsS --get --data-urlencode "to=$addr" "${UE_ADMIN_URL:?UE_ADMIN_URL unset}/ses/messages")
  assert_jq '.messages | length' '1'
  assert_jq '.messages[0].kind' 'verification'
  assert_jq '.messages[0].from[0]' 'no-reply-aws@amazon.com'
  assert_jq_match '.messages[0].subject.data' '^Amazon Web Services - Email Address Verification Request in region [a-z0-9-]+$'
  AWS_OUT=$(curl -fsS -X DELETE --get --data-urlencode "to=$addr" "$UE_ADMIN_URL/ses/messages")
  assert_jq '.deleted' '1'
}
