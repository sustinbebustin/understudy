#!/usr/bin/env bash
# SES v2: email identities (CreateEmailIdentity, GetEmailIdentity,
# ListEmailIdentities, DeleteEmailIdentity), the tag operations on an
# identity ARN, and GetAccount. This CLI sends ListEmailIdentities as POST
# /v2/email/list-identities (ses.md 3.2).
# Arbiter: docs/research/ses.md sections 2.3, 5.4, 6.3 and 9.3.

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
