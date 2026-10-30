#!/usr/bin/env bash
# SES v2: email identities (CreateEmailIdentity, GetEmailIdentity,
# ListEmailIdentities, DeleteEmailIdentity), the tag operations on an
# identity ARN, GetAccount, and SendEmail with Simple, Raw and Template content. This
# CLI sends ListEmailIdentities as POST /v2/email/list-identities (ses.md
# 3.2).
# Arbiter: docs/research/ses.md sections 2.3, 4.4, 5.4, 6 and 9.3.

test_domain_identity_lifecycle() {
  local domain tokens arn
  domain="$(new_name sesv2-domain).example.org"
  aws_ok sesv2 create-email-identity --email-identity "$domain" --tags Key=env,Value=dev
  assert_jq '.IdentityType' 'DOMAIN'
  assert_jq '.VerifiedForSendingStatus' 'false'
  # The ses.md 5.4 domain column: PENDING, three stable tokens, RSA_2048_BIT.
  aws_ok sesv2 get-email-identity --email-identity "$domain"
  assert_jq '.VerificationStatus' 'PENDING'
  assert_jq '.DkimAttributes.Status' 'PENDING'
  assert_jq '.DkimAttributes.Tokens | length' '3'
  assert_jq '.DkimAttributes.NextSigningKeyLength' 'RSA_2048_BIT'
  assert_jq '.DkimAttributes.CurrentSigningKeyLength' 'RSA_2048_BIT'
  tokens=$(jq -c '.DkimAttributes.Tokens' <<<"$AWS_OUT")
  aws_ok sesv2 get-email-identity --email-identity "$domain"
  assert_jq '.DkimAttributes.Tokens | tojson' "$tokens"
  # Listed regardless of status (ses.md 5.4).
  aws_ok sesv2 list-email-identities --page-size 1000
  assert_jq "[.EmailIdentities[] | select(.IdentityName == \"$domain\")] | length" '1'
  # Tags through the tag operations on the identity ARN (ses.md 5.4, 9.3).
  arn="arn:aws:ses:${AWS_REGION}:000000000000:identity/$domain"
  aws_ok sesv2 tag-resource --resource-arn "$arn" --tags Key=team,Value=mail
  aws_ok sesv2 untag-resource --resource-arn "$arn" --tag-keys env
  aws_ok sesv2 list-tags-for-resource --resource-arn "$arn"
  assert_jq '.Tags | tojson' '[{"Key":"team","Value":"mail"}]'
  aws_ok sesv2 delete-email-identity --email-identity "$domain"
  # NotFoundException, 404 per the model (ses.md 3.3), which Terraform reads
  # as gone (ses.md 9.3).
  aws_error NotFoundException 404 sesv2 get-email-identity --email-identity "$domain"
}

test_address_is_shared_with_v1() {
  local addr
  addr="$(new_name sesv2-address)@example.com"
  aws_ok sesv2 create-email-identity --email-identity "$addr"
  # The ses.md 5.4 email column: a DKIM block with NOT_STARTED and no tokens.
  aws_ok sesv2 get-email-identity --email-identity "$addr"
  assert_jq '.DkimAttributes.Status' 'NOT_STARTED'
  assert_jq '.DkimAttributes.Tokens // [] | length' '0'
  assert_jq '.DkimAttributes.NextSigningKeyLength' 'RSA_1024_BIT'
  # One store behind both APIs (ses.md 2.3, switch ses.store.shared-v1-v2).
  aws_ok ses get-identity-verification-attributes --identities "$addr"
  assert_jq ".VerificationAttributes[\"$addr\"].VerificationStatus" 'Pending'
  aws_ok sesv2 delete-email-identity --email-identity "$addr"
}

test_get_account_reports_the_sandbox() {
  # ProductionAccessEnabled false is the sandbox (model, ses.md 6.3), with
  # the recorded sandbox quota of 200 and 1 (ses.md 6.3).
  aws_ok sesv2 get-account
  assert_jq '.ProductionAccessEnabled' 'false'
  assert_jq '.SendQuota.Max24HourSend == 200' 'true'
  assert_jq '.SendQuota.MaxSendRate == 1' 'true'
}

test_send_email_rejects_an_unverified_from() {
  local from
  from="$(new_name sesv2-unverified)@example.com"
  # FromEmailAddress "has to be verified" (model, ses.md 4.4); the text is
  # the documented one (ses.md 6.1).
  aws_error MessageRejected 400 sesv2 send-email \
    --from-email-address "$from" \
    --destination ToAddresses=success@simulator.amazonses.com \
    --content 'Simple={Subject={Data=subject},Body={Text={Data=text}}}'
  aws_error_message_contains 'Email address is not verified. The following identities failed the check in region '
  aws_error_message_contains "$from"
}

test_send_email_simple_and_raw_from_a_verified_address_are_recorded() {
  local addr link raw id
  addr="$(new_name sesv2-sender)@example.com"
  aws_ok sesv2 create-email-identity --email-identity "$addr"
  link=$(curl -fsS --get --data-urlencode "to=$addr" "${UE_ADMIN_URL:?UE_ADMIN_URL unset}/ses/messages" |
    jq -r '.messages[0].text.data' | grep -o 'http[^[:space:]]*/_understudy/ses/VerificationLink?token=[A-Za-z0-9_-]*')
  curl -fsS -o /dev/null "$link"
  # A verified sender may mail the mailbox simulator from the sandbox
  # (ses.md 6.1, 6.2); understudy records instead of sending (ses.md 11.2).
  aws_ok sesv2 send-email --from-email-address "$addr" \
    --destination ToAddresses=success@simulator.amazonses.com \
    --content 'Simple={Subject={Data=hello},Body={Text={Data=text}},Headers=[{Name=X-Custom,Value=one}]}'
  assert_jq_match '.MessageId' '^[0-9a-f]{16}-[0-9a-f-]{36}-000000$'
  id=$(jq -r '.MessageId' <<<"$AWS_OUT")
  AWS_OUT=$(curl -fsS --get --data-urlencode "messageId=$id" "$UE_ADMIN_URL/ses/messages")
  assert_jq '.messages[0].api' 'sesv2'
  assert_jq '.messages[0].subject.data' 'hello'
  assert_jq '.messages[0].headers | tojson' '[{"name":"X-Custom","value":"one"}]'
  # Raw content without a Destination: the To header gives the recipients
  # (ses.md 4.2, switch ses.raw.envelope).
  raw=$(printf 'From: %s\r\nTo: success@simulator.amazonses.com\r\nSubject: raw\r\n\r\nbody\r\n' "$addr" | base64 -w0)
  aws_ok sesv2 send-email --content "{\"Raw\":{\"Data\":\"$raw\"}}"
  id=$(jq -r '.MessageId' <<<"$AWS_OUT")
  AWS_OUT=$(curl -fsS --get --data-urlencode "messageId=$id" "$UE_ADMIN_URL/ses/messages")
  assert_jq '.messages[0].raw' "$raw"
  assert_jq '.messages[0].envelope | join(",")' 'success@simulator.amazonses.com'
  aws_ok sesv2 delete-email-identity --email-identity "$addr"
}

test_send_email_template_content_inline_is_rendered() {
  local addr link id
  addr="$(new_name sesv2-tmpl-sender)@example.com"
  aws_ok sesv2 create-email-identity --email-identity "$addr"
  link=$(curl -fsS --get --data-urlencode "to=$addr" "${UE_ADMIN_URL:?UE_ADMIN_URL unset}/ses/messages" |
    jq -r '.messages[0].text.data' | grep -o 'http[^[:space:]]*/_understudy/ses/VerificationLink?token=[A-Za-z0-9_-]*')
  curl -fsS -o /dev/null "$link"
  # Inline TemplateContent supports "only simple substitutions" (model,
  # ses.md 4.4); understudy renders {{name}} (ses.md 11.6 item 6).
  aws_ok sesv2 send-email --from-email-address "$addr" \
    --destination ToAddresses=success@simulator.amazonses.com \
    --content '{"Template":{"TemplateContent":{"Subject":"Hi {{name}}","Text":"Dear {{name}}"},"TemplateData":"{\"name\":\"x\"}"}}'
  assert_jq_match '.MessageId' '^[0-9a-f]{16}-[0-9a-f-]{36}-000000$'
  id=$(jq -r '.MessageId' <<<"$AWS_OUT")
  AWS_OUT=$(curl -fsS --get --data-urlencode "messageId=$id" "$UE_ADMIN_URL/ses/messages")
  assert_jq '.messages[0].api' 'sesv2'
  assert_jq '.messages[0].subject.data' 'Hi x'
  assert_jq '.messages[0].text.data' 'Dear x'
  aws_ok sesv2 delete-email-identity --email-identity "$addr"
}
