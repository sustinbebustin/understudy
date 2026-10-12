#!/usr/bin/env bash
# IAM: roles, inline and managed policies, attachments, tags, instance profiles, users.
# Arbiter: docs/research/phase1/iam-sts-kms.md sections 2 and 3.

POLICY_DOC='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}'
POLICY_DOC_V2='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject"],"Resource":"*"}]}'

test_create_role_response_shape() {
  local role acct
  role=$(new_name iam-role)
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  aws_ok iam create-role --role-name "$role" --assume-role-policy-document "$LAMBDA_TRUST"
  assert_jq '.Role.RoleName' "$role"
  assert_jq '.Role.Path' '/'
  assert_jq '.Role.Arn' "arn:aws:iam::${acct}:role/${role}"
  assert_jq_match '.Role.RoleId' '^AROA[A-Z0-9]{17}$'
  assert_jq_match '.Role.CreateDate' '^[0-9]{4}-[0-9]{2}-[0-9]{2}T'
}

test_create_role_trust_policy_round_trips() {
  # The wire form is URL-encoded; the CLI decodes it, so the decoded JSON must equal what was sent.
  local role
  role=$(new_name iam-trust)
  aws_ok iam create-role --role-name "$role" --assume-role-policy-document "$LAMBDA_TRUST"
  assert_jq_true ".Role.AssumeRolePolicyDocument == $LAMBDA_TRUST"
  aws_ok iam get-role --role-name "$role"
  assert_jq_true ".Role.AssumeRolePolicyDocument == $LAMBDA_TRUST"
}

test_create_role_default_max_session_duration() {
  # Terraform reads this back; a role created without it reports 3600 (iam-sts-kms.md section 3.2).
  local role
  role=$(new_name iam-dur)
  aws_ok iam create-role --role-name "$role" --assume-role-policy-document "$LAMBDA_TRUST"
  aws_ok iam get-role --role-name "$role"
  assert_jq '.Role.MaxSessionDuration' '3600'
}

test_create_role_with_path() {
  local role acct
  role=$(new_name iam-path)
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  aws_ok iam create-role --role-name "$role" --path /service-role/ --assume-role-policy-document "$LAMBDA_TRUST"
  assert_jq '.Role.Path' '/service-role/'
  assert_jq '.Role.Arn' "arn:aws:iam::${acct}:role/service-role/${role}"
}

test_create_role_duplicate_is_entity_already_exists() {
  local role
  role=$(new_name iam-dup)
  aws_ok iam create-role --role-name "$role" --assume-role-policy-document "$LAMBDA_TRUST"
  aws_error EntityAlreadyExists 409 iam create-role --role-name "$role" --assume-role-policy-document "$LAMBDA_TRUST"
}

test_create_role_malformed_trust_policy() {
  local role
  role=$(new_name iam-bad)
  aws_error MalformedPolicyDocument 400 iam create-role --role-name "$role" --assume-role-policy-document 'not json'
}

test_get_role_missing_is_no_such_entity() {
  local role
  role=$(new_name iam-missing)
  aws_error NoSuchEntity 404 iam get-role --role-name "$role"
  aws_error_message_contains "The role with name ${role} cannot be found."
}

test_list_roles_contains_created_role() {
  local role
  role=$(new_name iam-list)
  aws_ok iam create-role --role-name "$role" --assume-role-policy-document "$LAMBDA_TRUST"
  aws_ok iam list-roles
  assert_jq_true "[.Roles[].RoleName] | index(\"$role\") != null"
}

test_update_assume_role_policy() {
  local role other
  role=$(new_name iam-upd)
  other='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"events.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
  aws_ok iam create-role --role-name "$role" --assume-role-policy-document "$LAMBDA_TRUST"
  aws_ok iam update-assume-role-policy --role-name "$role" --policy-document "$other"
  aws_ok iam get-role --role-name "$role"
  assert_jq_true ".Role.AssumeRolePolicyDocument == $other"
}

test_role_tags() {
  local role
  role=$(new_name iam-tag)
  aws_ok iam create-role --role-name "$role" --assume-role-policy-document "$LAMBDA_TRUST" --tags Key=env,Value=test
  aws_ok iam tag-role --role-name "$role" --tags Key=team,Value=core
  aws_ok iam list-role-tags --role-name "$role"
  assert_jq_true '[.Tags[] | {(.Key): .Value}] | add == {"env":"test","team":"core"}'
  aws_ok iam untag-role --role-name "$role" --tag-keys env
  aws_ok iam list-role-tags --role-name "$role"
  assert_jq_true '[.Tags[].Key] == ["team"]'
}

test_inline_role_policy_lifecycle() {
  local role pol
  role=$(new_name iam-inl)
  pol=inline-one
  aws_ok iam create-role --role-name "$role" --assume-role-policy-document "$LAMBDA_TRUST"
  aws_ok iam put-role-policy --role-name "$role" --policy-name "$pol" --policy-document "$POLICY_DOC"
  aws_ok iam get-role-policy --role-name "$role" --policy-name "$pol"
  assert_jq '.RoleName' "$role"
  assert_jq '.PolicyName' "$pol"
  assert_jq_true ".PolicyDocument == $POLICY_DOC"
  aws_ok iam list-role-policies --role-name "$role"
  assert_jq_true ".PolicyNames == [\"$pol\"]"
  aws_ok iam delete-role-policy --role-name "$role" --policy-name "$pol"
  aws_error NoSuchEntity 404 iam get-role-policy --role-name "$role" --policy-name "$pol"
}

test_managed_policy_response_shape() {
  local name acct
  name=$(new_name iam-pol)
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  aws_ok iam create-policy --policy-name "$name" --policy-document "$POLICY_DOC"
  assert_jq '.Policy.PolicyName' "$name"
  assert_jq '.Policy.Arn' "arn:aws:iam::${acct}:policy/${name}"
  assert_jq_match '.Policy.PolicyId' '^ANPA[A-Z0-9]{17}$'
  assert_jq '.Policy.DefaultVersionId' 'v1'
  assert_jq '.Policy.AttachmentCount' '0'
  assert_jq '.Policy.IsAttachable' 'true'
}

test_managed_policy_version_document_round_trips() {
  local name arn
  name=$(new_name iam-polver)
  aws_ok iam create-policy --policy-name "$name" --policy-document "$POLICY_DOC"
  arn=$(jqv '.Policy.Arn')
  aws_ok iam get-policy-version --policy-arn "$arn" --version-id v1
  assert_jq_true ".PolicyVersion.Document == $POLICY_DOC"
  assert_jq '.PolicyVersion.IsDefaultVersion' 'true'
}

test_create_policy_version_set_as_default() {
  local name arn
  name=$(new_name iam-polv2)
  aws_ok iam create-policy --policy-name "$name" --policy-document "$POLICY_DOC"
  arn=$(jqv '.Policy.Arn')
  aws_ok iam create-policy-version --policy-arn "$arn" --policy-document "$POLICY_DOC_V2" --set-as-default
  assert_jq '.PolicyVersion.VersionId' 'v2'
  aws_ok iam get-policy --policy-arn "$arn"
  assert_jq '.Policy.DefaultVersionId' 'v2'
  aws_ok iam list-policy-versions --policy-arn "$arn"
  assert_jq_true '[.Versions[].VersionId] | sort == ["v1","v2"]'
}

test_attach_and_detach_managed_policy() {
  local role pname arn
  role=$(new_name iam-att)
  pname=$(new_name iam-attpol)
  aws_ok iam create-role --role-name "$role" --assume-role-policy-document "$LAMBDA_TRUST"
  aws_ok iam create-policy --policy-name "$pname" --policy-document "$POLICY_DOC"
  arn=$(jqv '.Policy.Arn')
  aws_ok iam attach-role-policy --role-name "$role" --policy-arn "$arn"
  aws_ok iam list-attached-role-policies --role-name "$role"
  assert_jq_true ".AttachedPolicies == [{\"PolicyName\":\"$pname\",\"PolicyArn\":\"$arn\"}]"
  aws_ok iam get-policy --policy-arn "$arn"
  assert_jq '.Policy.AttachmentCount' '1'
  aws_ok iam detach-role-policy --role-name "$role" --policy-arn "$arn"
  aws_ok iam list-attached-role-policies --role-name "$role"
  assert_jq '.AttachedPolicies | length' '0'
}

test_delete_attached_policy_is_delete_conflict() {
  local role pname arn
  role=$(new_name iam-delconf)
  pname=$(new_name iam-delconfpol)
  aws_ok iam create-role --role-name "$role" --assume-role-policy-document "$LAMBDA_TRUST"
  aws_ok iam create-policy --policy-name "$pname" --policy-document "$POLICY_DOC"
  arn=$(jqv '.Policy.Arn')
  aws_ok iam attach-role-policy --role-name "$role" --policy-arn "$arn"
  aws_error DeleteConflict 409 iam delete-policy --policy-arn "$arn"
}

test_delete_role_with_attachment_is_delete_conflict() {
  local role pname arn
  role=$(new_name iam-roledel)
  pname=$(new_name iam-roledelpol)
  aws_ok iam create-role --role-name "$role" --assume-role-policy-document "$LAMBDA_TRUST"
  aws_ok iam create-policy --policy-name "$pname" --policy-document "$POLICY_DOC"
  arn=$(jqv '.Policy.Arn')
  aws_ok iam attach-role-policy --role-name "$role" --policy-arn "$arn"
  aws_error DeleteConflict 409 iam delete-role --role-name "$role"
  aws_ok iam detach-role-policy --role-name "$role" --policy-arn "$arn"
  aws_ok iam delete-role --role-name "$role"
  aws_error NoSuchEntity 404 iam get-role --role-name "$role"
}

test_create_policy_duplicate_is_entity_already_exists() {
  local name
  name=$(new_name iam-poldup)
  aws_ok iam create-policy --policy-name "$name" --policy-document "$POLICY_DOC"
  aws_error EntityAlreadyExists 409 iam create-policy --policy-name "$name" --policy-document "$POLICY_DOC"
}

test_instance_profile_with_role() {
  local role prof
  role=$(new_name iam-ipr)
  prof=$(new_name iam-ip)
  aws_ok iam create-role --role-name "$role" --assume-role-policy-document "$LAMBDA_TRUST"
  aws_ok iam create-instance-profile --instance-profile-name "$prof"
  assert_jq '.InstanceProfile.InstanceProfileName' "$prof"
  assert_jq_match '.InstanceProfile.InstanceProfileId' '^AIPA[A-Z0-9]{17}$'
  aws_ok iam add-role-to-instance-profile --instance-profile-name "$prof" --role-name "$role"
  aws_ok iam get-instance-profile --instance-profile-name "$prof"
  assert_jq_true ".InstanceProfile.Roles | map(.RoleName) == [\"$role\"]"
  aws_ok iam list-instance-profiles-for-role --role-name "$role"
  assert_jq_true ".InstanceProfiles | map(.InstanceProfileName) == [\"$prof\"]"
}

test_user_lifecycle() {
  local user acct
  user=$(new_name iam-user)
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  aws_ok iam create-user --user-name "$user"
  assert_jq '.User.Arn' "arn:aws:iam::${acct}:user/${user}"
  assert_jq_match '.User.UserId' '^AIDA[A-Z0-9]{17}$'
  aws_ok iam get-user --user-name "$user"
  assert_jq '.User.UserName' "$user"
  aws_ok iam delete-user --user-name "$user"
  aws_error NoSuchEntity 404 iam get-user --user-name "$user"
}
