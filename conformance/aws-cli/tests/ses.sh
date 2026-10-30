#!/usr/bin/env bash
# SES v1: email and domain identities (VerifyEmailIdentity,
# VerifyDomainIdentity, GetIdentityVerificationAttributes,
# GetIdentityNotificationAttributes, ListIdentities, DeleteIdentity), the
# verification link, SendEmail, SendRawEmail and GetSendQuota.
# Arbiter: docs/research/ses.md sections 4, 5, 6, 9.1, 9.2 and 11.3.

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

test_send_email_rejects_an_unverified_source() {
  local source
  source="$(new_name ses-unverified)@example.com"
  # MessageRejected with the documented text naming the Source (aws-doc and
  # capture, ses.md 6.1).
  aws_error MessageRejected 400 ses send-email \
    --from "$source" \
    --destination ToAddresses=success@simulator.amazonses.com \
    --message 'Subject={Data=subject},Body={Text={Data=text}}'
  aws_error_message_contains 'Email address is not verified. The following identities failed the check in region '
  aws_error_message_contains "$source"
}

test_send_email_from_a_verified_address_is_recorded() {
  local addr link id
  addr="$(new_name ses-sender)@example.com"
  aws_ok ses verify-email-identity --email-address "$addr"
  link=$(curl -fsS --get --data-urlencode "to=$addr" "${UE_ADMIN_URL:?UE_ADMIN_URL unset}/ses/messages" |
    jq -r '.messages[0].text.data' | grep -o 'http[^[:space:]]*/_understudy/ses/VerificationLink?token=[A-Za-z0-9_-]*')
  curl -fsS -o /dev/null "$link"
  # A verified sender may mail the mailbox simulator from the sandbox (ses.md
  # 6.1, 6.2); the MessageId is 60 characters (ses.md 4.7).
  aws_ok ses send-email --from "$addr" \
    --destination ToAddresses=success@simulator.amazonses.com \
    --message 'Subject={Data=hello},Body={Text={Data=text}}'
  assert_jq_match '.MessageId' '^[0-9a-f]{16}-[0-9a-f-]{36}-000000$'
  id=$(jq -r '.MessageId' <<<"$AWS_OUT")
  # understudy records the message instead of sending it (ses.md 11.2).
  AWS_OUT=$(curl -fsS --get --data-urlencode "messageId=$id" "$UE_ADMIN_URL/ses/messages")
  assert_jq '.messages[0].kind' 'send'
  assert_jq '.messages[0].subject.data' 'hello'
  assert_jq '.messages[0].simulator["success@simulator.amazonses.com"]' 'success'
  aws_ok ses delete-identity --identity "$addr"
}

test_get_send_quota() {
  # A sandbox account: 200 a day at 1 a second (golden get-quota-0,
  # third-party-recorded 2024-11-27, ses.md 6.3; localstack@8b9a79f
  # tests/aws/services/ses/test_ses.snapshot.json:L905-L918, validated
  # 2024-11-27).
  aws_ok ses get-send-quota
  assert_jq '.Max24HourSend == 200 and .MaxSendRate == 1' 'true'
}

test_notification_attributes_key_a_known_identity() {
  local addr
  addr="$(new_name ses-notif)@example.com"
  aws_ok ses verify-email-identity --email-address "$addr"
  # The aws_ses_email_identity data source needs the key (ses.md 9.1).
  aws_ok ses get-identity-notification-attributes --identities "$addr"
  assert_jq '.NotificationAttributes | keys == ["'"$addr"'"]' 'true'
  aws_ok ses delete-identity --identity "$addr"
}

test_send_raw_email_raises_message_rejected() {
  local raw
  # Golden test_send_email_raises_message_rejected: an unverified raw From
  # and destination answer MessageRejected (third-party-recorded 2025-09-11,
  # ses.md 4.2; localstack@8b9a79f tests/aws/services/ses/test_ses.py:L1162-L1170,
  # validated 2025-09-11).
  raw=$(printf 'From: origin@example.com\r\nTo: invalid@example.com\r\nSubject: test\r\n\r\nThis is the message body.\r\n' | base64 -w0)
  aws_error MessageRejected 400 ses send-raw-email \
    --destinations invalid@example.com --raw-message "{\"Data\":\"$raw\"}"
}

test_send_raw_email_from_a_verified_address_is_recorded() {
  local addr link raw id
  addr="$(new_name ses-raw-sender)@example.com"
  aws_ok ses verify-email-identity --email-address "$addr"
  link=$(curl -fsS --get --data-urlencode "to=$addr" "${UE_ADMIN_URL:?UE_ADMIN_URL unset}/ses/messages" |
    jq -r '.messages[0].text.data' | grep -o 'http[^[:space:]]*/_understudy/ses/VerificationLink?token=[A-Za-z0-9_-]*')
  curl -fsS -o /dev/null "$link"
  # Without Destinations the To header gives the recipients (ses.md 4.2); the
  # mailbox simulator is reachable from the sandbox (ses.md 6.2).
  raw=$(printf 'From: %s\r\nTo: success@simulator.amazonses.com\r\nSubject: raw\r\n\r\nbody\r\n' "$addr" | base64 -w0)
  aws_ok ses send-raw-email --raw-message "{\"Data\":\"$raw\"}"
  assert_jq_match '.MessageId' '^[0-9a-f]{16}-[0-9a-f-]{36}-000000$'
  id=$(jq -r '.MessageId' <<<"$AWS_OUT")
  # Recorded as received, not sent (ses.md 11.2).
  AWS_OUT=$(curl -fsS --get --data-urlencode "messageId=$id" "$UE_ADMIN_URL/ses/messages")
  assert_jq '.messages[0].operation' 'SendRawEmail'
  assert_jq '.messages[0].raw' "$raw"
  assert_jq '.messages[0].envelope | join(",")' 'success@simulator.amazonses.com'
  aws_ok ses delete-identity --identity "$addr"
}
