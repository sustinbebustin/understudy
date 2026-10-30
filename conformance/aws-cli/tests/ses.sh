#!/usr/bin/env bash
# SES v1: email and domain identities (VerifyEmailIdentity,
# VerifyDomainIdentity, GetIdentityVerificationAttributes, ListIdentities,
# DeleteIdentity) and the verification link.
# Arbiter: docs/research/ses.md sections 5, 9.1, 9.2 and 11.3.

test_domain_identity_token_is_stable() {
  local domain token
  domain="$(new_name ses-domain).example.org"
  aws_ok ses verify-domain-identity --domain "$domain"
  # 44 base64 characters, the encoding of 32 bytes (ses.md 5.3).
  assert_jq_match '.VerificationToken' '^[A-Za-z0-9+/]{43}=$'
  token=$(jq -r '.VerificationToken' <<<"$AWS_OUT")
  # The same token on a repeat and on every read (ses.md 9.2).
  aws_ok ses verify-domain-identity --domain "$domain"
  assert_jq '.VerificationToken' "$token"
  aws_ok ses get-identity-verification-attributes --identities "$domain"
  assert_jq ".VerificationAttributes[\"$domain\"].VerificationStatus" 'Pending'
  assert_jq ".VerificationAttributes[\"$domain\"].VerificationToken" "$token"
  # Listed regardless of status, under its type only (ses.md 5.3).
  aws_ok ses list-identities --identity-type Domain
  assert_jq "[.Identities[] | select(. == \"$domain\")] | length" '1'
  aws_ok ses list-identities --identity-type EmailAddress
  assert_jq "[.Identities[] | select(. == \"$domain\")] | length" '0'
  aws_ok ses delete-identity --identity "$domain"
}

test_verification_link_verifies() {
  local addr link
  addr="$(new_name ses-link)@example.com"
  aws_ok ses verify-email-identity --email-address "$addr"
  # Opening the link in the recorded verification email verifies the
  # address, as the owner's click does on AWS (ses.md 5.2, 11.3 item 1).
  link=$(curl -fsS --get --data-urlencode "to=$addr" "${UE_ADMIN_URL:?UE_ADMIN_URL unset}/ses/messages" |
    jq -r '.messages[0].text.data' | grep -o 'http[^[:space:]]*/_understudy/ses/VerificationLink?token=[A-Za-z0-9_-]*')
  curl -fsS -o /dev/null "$link"
  aws_ok ses get-identity-verification-attributes --identities "$addr"
  assert_jq ".VerificationAttributes[\"$addr\"].VerificationStatus" 'Success'
  aws_ok ses delete-identity --identity "$addr"
}

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

test_send_email_rejects_an_invalid_tag_name() {
  # Tag validation runs before the sender check: an unverified Source with an
  # invalid tag name answers the recorded text (third-party-recorded
  # 2024-07-30, ses.md 4.5; localstack@8b9a79f
  # tests/aws/services/ses/test_ses.snapshot.json:L592-L735, validated
  # 2024-07-30).
  aws_error InvalidParameterValue 400 ses send-email \
    --from "$(new_name ses-unverified)@example.com" \
    --destination ToAddresses=success@example.com \
    --message 'Subject={Data=subject},Body={Text={Data=text}}' \
    --tags 'Name=test_invalid_name:123,Value=test'
  aws_error_message_contains "Invalid tag name <test_invalid_name:123>: only alphanumeric ASCII characters, '_',  '-' are allowed."
}
