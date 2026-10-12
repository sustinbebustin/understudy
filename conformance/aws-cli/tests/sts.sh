#!/usr/bin/env bash
# STS: GetCallerIdentity and AssumeRole.
# Arbiter: docs/research/phase1/iam-sts-kms.md sections 4 and 7.

test_get_caller_identity_shape() {
  aws_ok sts get-caller-identity
  assert_jq_match '.Account' '^[0-9]{12}$'
  assert_jq_match '.UserId' '^.+$'
  assert_jq_match '.Arn' '^arn:aws:iam::[0-9]{12}:'
}

test_get_caller_identity_static_key_is_root() {
  # iam-sts-kms.md item 7: root or unknown static keys -> arn:aws:iam::<acct>:root, UserId = account id.
  aws_ok sts get-caller-identity
  local acct
  acct=$(jqv '.Account')
  assert_jq '.Arn' "arn:aws:iam::${acct}:root"
  assert_jq '.UserId' "$acct"
}

test_get_caller_identity_account_matches_iam_arns() {
  aws_ok sts get-caller-identity
  local acct
  acct=$(jqv '.Account')
  local role
  role=$(new_name sts-acct)
  aws_ok iam create-role --role-name "$role" --assume-role-policy-document "$LAMBDA_TRUST"
  assert_jq '.Role.Arn' "arn:aws:iam::${acct}:role/${role}"
}

test_assume_role_returns_temporary_credentials() {
  local role arn session
  role=$(new_name sts-assume)
  session=session-one
  arn=$(make_role "$role")
  aws_ok sts get-caller-identity
  local acct
  acct=$(jqv '.Account')
  aws_ok sts assume-role --role-arn "$arn" --role-session-name "$session"
  assert_jq_match '.Credentials.AccessKeyId' '^ASIA[A-Z0-9]{16,}$'
  assert_jq_match '.Credentials.SecretAccessKey' '^.{40}$'
  assert_jq_match '.Credentials.SessionToken' '^.+$'
  assert_ne "" "$(date -d "$(jqv '.Credentials.Expiration')" +%s)" 'Expiration parses as a time'
  [ "$(date -d "$(jqv '.Credentials.Expiration')" +%s)" -gt "$(date +%s)" ] || fail "Expiration is not in the future"
  assert_jq '.AssumedRoleUser.Arn' "arn:aws:sts::${acct}:assumed-role/${role}/${session}"
  assert_jq_match '.AssumedRoleUser.AssumedRoleId' "^AROA[A-Z0-9]{17}:${session}\$"
}

test_assume_role_default_duration_is_one_hour() {
  local role arn
  role=$(new_name sts-dur)
  arn=$(make_role "$role")
  aws_ok sts assume-role --role-arn "$arn" --role-session-name dur
  # Expiration is wall time + 3600s (iam-sts-kms.md item 8); allow generous skew.
  local ttl
  ttl=$(($(date -d "$(jqv '.Credentials.Expiration')" +%s) - $(date +%s)))
  { [ "$ttl" -gt 3500 ] && [ "$ttl" -le 3610 ]; } || fail "credential lifetime is ${ttl}s, expected about 3600s"
}

test_assume_role_credentials_identify_assumed_role() {
  local role arn acct
  role=$(new_name sts-who)
  arn=$(make_role "$role")
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  aws_ok sts assume-role --role-arn "$arn" --role-session-name who
  local ak sk st
  ak=$(jqv '.Credentials.AccessKeyId')
  sk=$(jqv '.Credentials.SecretAccessKey')
  st=$(jqv '.Credentials.SessionToken')
  AWS_ACCESS_KEY_ID=$ak AWS_SECRET_ACCESS_KEY=$sk AWS_SESSION_TOKEN=$st aws_ok sts get-caller-identity
  assert_jq '.Arn' "arn:aws:sts::${acct}:assumed-role/${role}/who"
  assert_jq '.Account' "$acct"
}

test_assume_role_unknown_role_is_access_denied() {
  # iam-sts-kms.md 4.2: a role that does not exist fails with the common AccessDenied (403).
  local acct
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  aws_error AccessDenied 403 sts assume-role \
    --role-arn "arn:aws:iam::${acct}:role/$(new_name no-such-role)" --role-session-name no-such-role-session
  aws_error_message_contains 'is not authorized to perform: sts:AssumeRole'
}

test_assume_role_session_name_pattern_is_validated() {
  local role arn
  role=$(new_name sts-badname)
  arn=$(make_role "$role")
  aws_error ValidationError 400 sts assume-role --role-arn "$arn" --role-session-name 'bad name!'
  aws_error_message_contains "roleSessionName' failed to satisfy constraint"
}

test_assume_role_duration_above_role_maximum_is_rejected() {
  local role arn
  role=$(new_name sts-maxdur)
  arn=$(make_role "$role")
  aws_error ValidationError 400 sts assume-role --role-arn "$arn" --role-session-name long-duration --duration-seconds 7200
  aws_error_message_contains 'exceeds the MaxSessionDuration'
}
