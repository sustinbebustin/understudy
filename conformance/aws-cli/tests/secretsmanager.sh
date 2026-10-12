#!/usr/bin/env bash
# Secrets Manager: secrets, versions and staging labels, idempotency tokens,
# deletion window and restore, resource policies, tags, KMS keys.
# Arbiter: docs/research/phase1/secrets-logs-events-alarms.md section 2.

# mksecret NAME VALUE [create-secret args...]: sets SECRET_NAME, SECRET_ARN, V1.
mksecret() {
  local name=$1 value=$2
  shift 2
  aws_ok secretsmanager create-secret --name "$name" --secret-string "$value" "$@"
  SECRET_NAME=$name
  SECRET_ARN=$(jqv '.ARN')
  V1=$(jqv '.VersionId')
}

test_create_secret_response_and_arn_shape() {
  local name acct
  name=$(new_name sm-create)
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  aws_ok secretsmanager create-secret --name "$name" --secret-string s3cret
  assert_jq '.Name' "$name"
  assert_jq_match '.ARN' "^arn:aws:secretsmanager:${AWS_REGION}:${acct}:secret:${name}-[A-Za-z0-9]{6}\$"
  assert_jq_match '.VersionId' '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
}

test_create_secret_without_value_has_no_version() {
  local name
  name=$(new_name sm-empty)
  aws_ok secretsmanager create-secret --name "$name"
  assert_jq 'has("VersionId")' 'false'
  aws_error ResourceNotFoundException 400 secretsmanager get-secret-value --secret-id "$name"
  aws_error_message_contains 'Secrets Manager can'"'"'t find the specified secret value for staging label: AWSCURRENT'
}

test_create_secret_duplicate_name() {
  local name
  name=$(new_name sm-dup)
  mksecret "$name" one
  aws_error ResourceExistsException 400 secretsmanager create-secret --name "$name" --secret-string two
  aws_error_message_contains "The operation failed because the secret $name already exists."
}

test_create_secret_invalid_name() {
  aws_error ValidationException 400 secretsmanager create-secret --name 'bad name?' --secret-string x
  aws_error_message_contains 'Invalid name. Must be a valid name containing alphanumeric characters, or any of the following: -/_+=.@!'
}

test_create_secret_accepts_documented_name_characters() {
  aws_ok secretsmanager create-secret --name "app/$(new_name sm-chars)/db_pass+x=y.z@w-v" --secret-string x
}

test_create_secret_string_and_binary_are_exclusive() {
  local f
  f=$(mktemp "$UE_TMP/bin.XXXXXX")
  printf 'bytes' >"$f"
  aws_error InvalidParameterException 400 secretsmanager create-secret --name "$(new_name sm-both)" \
    --secret-string x --secret-binary "fileb://$f"
}

test_get_secret_value_by_name_and_arn() {
  mksecret "$(new_name sm-get)" 'plain value'
  aws_ok secretsmanager get-secret-value --secret-id "$SECRET_NAME"
  assert_jq '.SecretString' 'plain value'
  assert_jq '.Name' "$SECRET_NAME"
  assert_jq '.ARN' "$SECRET_ARN"
  assert_jq '.VersionId' "$V1"
  assert_jq_true '.VersionStages == ["AWSCURRENT"]'
  assert_jq_match '.CreatedDate' '^[0-9]{4}-[0-9]{2}-[0-9]{2}T'
  aws_ok secretsmanager get-secret-value --secret-id "$SECRET_ARN"
  assert_jq '.SecretString' 'plain value'
}

test_get_secret_value_binary_round_trip() {
  local name f out
  name=$(new_name sm-bin)
  f=$(mktemp "$UE_TMP/bin.XXXXXX")
  head -c 64 /dev/urandom >"$f"
  aws_ok secretsmanager create-secret --name "$name" --secret-binary "fileb://$f"
  aws_ok secretsmanager get-secret-value --secret-id "$name"
  assert_jq 'has("SecretString")' 'false'
  out=$(jqv '.SecretBinary')
  assert_eq "$(base64 -w0 <"$f")" "$out" 'SecretBinary'
}

test_get_secret_value_missing_secret() {
  aws_error ResourceNotFoundException 400 secretsmanager get-secret-value --secret-id "$(new_name sm-none)"
  aws_error_message_contains "Secrets Manager can't find the specified secret."
}

test_get_secret_value_unknown_version_id() {
  mksecret "$(new_name sm-nover)" x
  local bogus=00000000-0000-4000-8000-000000000000
  aws_error ResourceNotFoundException 400 secretsmanager get-secret-value --secret-id "$SECRET_NAME" --version-id "$bogus"
  aws_error_message_contains "Secrets Manager can't find the specified secret value for VersionId: $bogus"
}

test_get_secret_value_stage_without_version() {
  mksecret "$(new_name sm-nostage)" x
  aws_error ResourceNotFoundException 400 secretsmanager get-secret-value --secret-id "$SECRET_NAME" --version-stage AWSPREVIOUS
  aws_error_message_contains "Secrets Manager can't find the specified secret value for staging label: AWSPREVIOUS"
}

test_put_secret_value_moves_current_and_previous_stages() {
  mksecret "$(new_name sm-put)" first
  aws_ok secretsmanager put-secret-value --secret-id "$SECRET_NAME" --secret-string second
  local v2
  v2=$(jqv '.VersionId')
  assert_jq_true '.VersionStages == ["AWSCURRENT"]'
  assert_ne "$V1" "$v2" 'a new version'
  aws_ok secretsmanager get-secret-value --secret-id "$SECRET_NAME"
  assert_jq '.SecretString' 'second'
  assert_jq '.VersionId' "$v2"
  aws_ok secretsmanager get-secret-value --secret-id "$SECRET_NAME" --version-stage AWSPREVIOUS
  assert_jq '.SecretString' 'first'
  assert_jq '.VersionId' "$V1"
  aws_ok secretsmanager describe-secret --secret-id "$SECRET_NAME"
  assert_jq_true ".VersionIdsToStages == {\"$V1\":[\"AWSPREVIOUS\"],\"$v2\":[\"AWSCURRENT\"]}"
}

test_third_put_drops_oldest_version_from_stages() {
  mksecret "$(new_name sm-put3)" first
  aws_ok secretsmanager put-secret-value --secret-id "$SECRET_NAME" --secret-string second
  local v2
  v2=$(jqv '.VersionId')
  aws_ok secretsmanager put-secret-value --secret-id "$SECRET_NAME" --secret-string third
  local v3
  v3=$(jqv '.VersionId')
  aws_ok secretsmanager describe-secret --secret-id "$SECRET_NAME"
  assert_jq_true ".VersionIdsToStages == {\"$v2\":[\"AWSPREVIOUS\"],\"$v3\":[\"AWSCURRENT\"]}"
}

test_put_secret_value_with_custom_stage_keeps_current() {
  mksecret "$(new_name sm-custom)" first
  aws_ok secretsmanager put-secret-value --secret-id "$SECRET_NAME" --secret-string candidate --version-stages two one three
  assert_jq_true '.VersionStages == ["one","three","two"]'
  aws_ok secretsmanager get-secret-value --secret-id "$SECRET_NAME"
  assert_jq '.SecretString' 'first' 'AWSCURRENT must not move'
  aws_ok secretsmanager get-secret-value --secret-id "$SECRET_NAME" --version-stage one
  assert_jq '.SecretString' 'candidate'
}

test_put_secret_value_pending_stage_does_not_move_current() {
  mksecret "$(new_name sm-pend)" current
  aws_ok secretsmanager put-secret-value --secret-id "$SECRET_NAME" --secret-string pending --version-stages AWSPENDING
  aws_ok secretsmanager get-secret-value --secret-id "$SECRET_NAME"
  assert_jq '.SecretString' 'current'
  aws_ok secretsmanager get-secret-value --secret-id "$SECRET_NAME" --version-stage AWSPENDING
  assert_jq '.SecretString' 'pending'
}

test_put_secret_value_same_token_same_value_is_idempotent() {
  mksecret "$(new_name sm-idem)" first
  local token=11111111-2222-4333-8444-555555555555
  aws_ok secretsmanager put-secret-value --secret-id "$SECRET_NAME" --secret-string second --client-request-token "$token"
  assert_jq '.VersionId' "$token"
  aws_ok secretsmanager put-secret-value --secret-id "$SECRET_NAME" --secret-string second --client-request-token "$token"
  assert_jq '.VersionId' "$token"
  aws_ok secretsmanager describe-secret --secret-id "$SECRET_NAME"
  assert_jq '.VersionIdsToStages | length' '2'
}

test_put_secret_value_same_token_different_value_is_rejected() {
  mksecret "$(new_name sm-idemdiff)" first
  local token=11111111-2222-4333-8444-555555555556
  aws_ok secretsmanager put-secret-value --secret-id "$SECRET_NAME" --secret-string second --client-request-token "$token"
  aws_error ResourceExistsException 400 secretsmanager put-secret-value --secret-id "$SECRET_NAME" --secret-string other --client-request-token "$token"
}

test_put_secret_value_unknown_secret() {
  aws_error ResourceNotFoundException 400 secretsmanager put-secret-value --secret-id "$(new_name sm-putnone)" --secret-string x
}

test_update_secret_description_does_not_create_version() {
  mksecret "$(new_name sm-upddesc)" value
  aws_ok secretsmanager update-secret --secret-id "$SECRET_NAME" --description 'new description'
  assert_jq 'has("VersionId")' 'false'
  assert_jq '.ARN' "$SECRET_ARN"
  aws_ok secretsmanager describe-secret --secret-id "$SECRET_NAME"
  assert_jq '.Description' 'new description'
  assert_jq '.VersionIdsToStages | length' '1'
}

test_update_secret_with_value_creates_version() {
  mksecret "$(new_name sm-updval)" first
  aws_ok secretsmanager update-secret --secret-id "$SECRET_NAME" --secret-string second
  local v2
  v2=$(jqv '.VersionId')
  assert_ne "$V1" "$v2"
  aws_ok secretsmanager get-secret-value --secret-id "$SECRET_NAME"
  assert_jq '.SecretString' 'second'
}

test_update_secret_version_stage_moves_label() {
  mksecret "$(new_name sm-uvs)" first
  aws_ok secretsmanager put-secret-value --secret-id "$SECRET_NAME" --secret-string second
  local v2
  v2=$(jqv '.VersionId')
  aws_ok secretsmanager update-secret-version-stage --secret-id "$SECRET_NAME" --version-stage AWSCURRENT \
    --move-to-version-id "$V1" --remove-from-version-id "$v2"
  assert_jq '.Name' "$SECRET_NAME"
  aws_ok secretsmanager get-secret-value --secret-id "$SECRET_NAME"
  assert_jq '.SecretString' 'first'
}

test_describe_secret_shape() {
  mksecret "$(new_name sm-desc)" value --description 'a secret' --tags Key=env,Value=test
  aws_ok secretsmanager describe-secret --secret-id "$SECRET_NAME"
  assert_jq '.Name' "$SECRET_NAME"
  assert_jq '.ARN' "$SECRET_ARN"
  assert_jq '.Description' 'a secret'
  assert_jq_true '.Tags == [{"Key":"env","Value":"test"}]'
  assert_jq_match '.CreatedDate' '^[0-9]{4}-'
  assert_jq_match '.LastChangedDate' '^[0-9]{4}-'
  assert_jq 'has("DeletedDate")' 'false'
  assert_jq 'has("KmsKeyId")' 'false' 'the default key is not reported'
}

test_describe_secret_missing() {
  aws_error ResourceNotFoundException 400 secretsmanager describe-secret --secret-id "$(new_name sm-descnone)"
}

test_list_secrets_and_name_filter() {
  local prefix a b
  prefix=$(new_name sm-list)
  a="${prefix}-a"
  b="${prefix}-b"
  mksecret "$a" 1
  mksecret "$b" 2
  mksecret "$(new_name sm-other)" 3
  aws_ok secretsmanager list-secrets --filters "Key=name,Values=$prefix"
  assert_jq '[.SecretList[].Name] | sort | join(",")' "$a,$b"
  assert_jq_true '.SecretList[0] | has("SecretVersionsToStages")'
}

test_list_secrets_excludes_secrets_scheduled_for_deletion() {
  mksecret "$(new_name sm-listdel)" 1
  aws_ok secretsmanager delete-secret --secret-id "$SECRET_NAME" --recovery-window-in-days 7
  aws_ok secretsmanager list-secrets --filters "Key=name,Values=$SECRET_NAME"
  assert_jq '.SecretList | length' '0'
  aws_ok secretsmanager list-secrets --filters "Key=name,Values=$SECRET_NAME" --include-planned-deletion
  assert_jq '.SecretList | length' '1'
}

test_list_secret_version_ids() {
  mksecret "$(new_name sm-lsv)" first
  aws_ok secretsmanager put-secret-value --secret-id "$SECRET_NAME" --secret-string second
  local v2
  v2=$(jqv '.VersionId')
  aws_ok secretsmanager list-secret-version-ids --secret-id "$SECRET_NAME"
  assert_jq '.Name' "$SECRET_NAME"
  assert_jq '[.Versions[].VersionId] | sort | join(",")' "$(printf '%s\n%s\n' "$V1" "$v2" | sort | paste -sd, -)"
}

test_delete_secret_schedules_deletion_with_default_window() {
  mksecret "$(new_name sm-del)" value
  aws_ok secretsmanager delete-secret --secret-id "$SECRET_NAME"
  assert_jq '.Name' "$SECRET_NAME"
  assert_jq '.ARN' "$SECRET_ARN"
  local delta
  delta=$(($(date -d "$(jqv '.DeletionDate')" +%s) - $(date +%s)))
  { [ "$delta" -gt $((29 * 86400)) ] && [ "$delta" -le $((30 * 86400 + 60)) ]; } || fail "DeletionDate is ${delta}s away, expected about 30 days"
  aws_ok secretsmanager describe-secret --secret-id "$SECRET_NAME"
  assert_jq_match '.DeletedDate' '^[0-9]{4}-'
}

test_delete_secret_custom_recovery_window() {
  mksecret "$(new_name sm-delwin)" value
  aws_ok secretsmanager delete-secret --secret-id "$SECRET_NAME" --recovery-window-in-days 7
  local delta
  delta=$(($(date -d "$(jqv '.DeletionDate')" +%s) - $(date +%s)))
  { [ "$delta" -gt $((6 * 86400)) ] && [ "$delta" -le $((7 * 86400 + 60)) ]; } || fail "DeletionDate is ${delta}s away, expected about 7 days"
}

test_delete_secret_recovery_window_out_of_range() {
  mksecret "$(new_name sm-delbad)" value
  aws_error InvalidParameterException 400 secretsmanager delete-secret --secret-id "$SECRET_NAME" --recovery-window-in-days 3
}

test_delete_secret_window_and_force_are_exclusive() {
  mksecret "$(new_name sm-delboth)" value
  aws_error InvalidParameterException 400 secretsmanager delete-secret --secret-id "$SECRET_NAME" \
    --recovery-window-in-days 7 --force-delete-without-recovery
}

test_secret_scheduled_for_deletion_rejects_reads_and_writes() {
  mksecret "$(new_name sm-deleted)" value
  aws_ok secretsmanager delete-secret --secret-id "$SECRET_NAME" --recovery-window-in-days 7
  aws_error InvalidRequestException 400 secretsmanager get-secret-value --secret-id "$SECRET_NAME"
  aws_error_message_contains 'You can'"'"'t perform this operation on the secret because it was marked for deletion.'
  aws_error InvalidRequestException 400 secretsmanager put-secret-value --secret-id "$SECRET_NAME" --secret-string x
  aws_error InvalidRequestException 400 secretsmanager tag-resource --secret-id "$SECRET_NAME" --tags Key=a,Value=b
}

test_create_secret_with_name_scheduled_for_deletion() {
  mksecret "$(new_name sm-recreate)" value
  aws_ok secretsmanager delete-secret --secret-id "$SECRET_NAME" --recovery-window-in-days 7
  aws_error InvalidRequestException 400 secretsmanager create-secret --name "$SECRET_NAME" --secret-string again
  aws_error_message_contains 'You can'"'"'t create this secret because a secret with this name is already scheduled for deletion.'
}

test_restore_secret() {
  mksecret "$(new_name sm-restore)" value
  aws_ok secretsmanager delete-secret --secret-id "$SECRET_NAME" --recovery-window-in-days 7
  aws_ok secretsmanager restore-secret --secret-id "$SECRET_NAME"
  assert_jq '.Name' "$SECRET_NAME"
  assert_jq '.ARN' "$SECRET_ARN"
  aws_ok secretsmanager describe-secret --secret-id "$SECRET_NAME"
  assert_jq 'has("DeletedDate")' 'false'
  aws_ok secretsmanager get-secret-value --secret-id "$SECRET_NAME"
  assert_jq '.SecretString' 'value'
}

test_force_delete_removes_secret_immediately_and_frees_the_name() {
  mksecret "$(new_name sm-force)" value
  local old_arn=$SECRET_ARN
  aws_ok secretsmanager delete-secret --secret-id "$SECRET_NAME" --force-delete-without-recovery
  aws_error ResourceNotFoundException 400 secretsmanager describe-secret --secret-id "$SECRET_NAME"
  aws_ok secretsmanager create-secret --name "$SECRET_NAME" --secret-string again
  assert_ne "$old_arn" "$(jqv '.ARN')" 'a recreated secret gets a new ARN suffix'
}

test_force_delete_of_missing_secret_succeeds() {
  aws_ok secretsmanager delete-secret --secret-id "$(new_name sm-forcenone)" --force-delete-without-recovery
}

test_secret_tags() {
  mksecret "$(new_name sm-tags)" value --tags Key=env,Value=test
  aws_ok secretsmanager tag-resource --secret-id "$SECRET_NAME" --tags Key=team,Value=core
  aws_ok secretsmanager describe-secret --secret-id "$SECRET_NAME"
  assert_jq_true '[.Tags[] | {(.Key): .Value}] | add == {"env":"test","team":"core"}'
  aws_ok secretsmanager untag-resource --secret-id "$SECRET_NAME" --tag-keys env
  aws_ok secretsmanager describe-secret --secret-id "$SECRET_NAME"
  assert_jq_true '[.Tags[].Key] == ["team"]'
}

test_resource_policy_stored() {
  mksecret "$(new_name sm-policy)" value
  local policy
  policy=$(jq -nc --arg a "$SECRET_ARN" '{Version:"2012-10-17",Statement:[{Sid:"AllowRead",Effect:"Allow",Principal:{AWS:"arn:aws:iam::000000000000:root"},Action:"secretsmanager:GetSecretValue",Resource:$a}]}')
  aws_ok secretsmanager get-resource-policy --secret-id "$SECRET_NAME"
  assert_jq 'has("ResourcePolicy")' 'false' 'no policy attached yet'
  aws_ok secretsmanager put-resource-policy --secret-id "$SECRET_NAME" --resource-policy "$policy"
  assert_jq '.Name' "$SECRET_NAME"
  aws_ok secretsmanager get-resource-policy --secret-id "$SECRET_NAME"
  assert_jq_true ".ResourcePolicy | fromjson == $policy"
  aws_ok secretsmanager delete-resource-policy --secret-id "$SECRET_NAME"
  aws_ok secretsmanager get-resource-policy --secret-id "$SECRET_NAME"
  assert_jq 'has("ResourcePolicy")' 'false'
}

test_resource_policy_malformed() {
  mksecret "$(new_name sm-badpol)" value
  aws_error MalformedPolicyDocumentException 400 secretsmanager put-resource-policy --secret-id "$SECRET_NAME" --resource-policy 'not json'
}

test_secret_encrypted_with_customer_kms_key() {
  aws_ok kms create-key
  local key_arn
  key_arn=$(jqv '.KeyMetadata.Arn')
  mksecret "$(new_name sm-kms)" value --kms-key-id "$key_arn"
  aws_ok secretsmanager describe-secret --secret-id "$SECRET_NAME"
  assert_jq '.KmsKeyId' "$key_arn"
  aws_ok secretsmanager get-secret-value --secret-id "$SECRET_NAME"
  assert_jq '.SecretString' 'value'
}

test_secret_with_disabled_kms_key_cannot_be_read() {
  aws_ok kms create-key
  local key_id key_arn
  key_id=$(jqv '.KeyMetadata.KeyId')
  key_arn=$(jqv '.KeyMetadata.Arn')
  mksecret "$(new_name sm-kmsoff)" value --kms-key-id "$key_arn"
  aws_ok kms disable-key --key-id "$key_id"
  aws_error DecryptionFailure 400 secretsmanager get-secret-value --secret-id "$SECRET_NAME"
}
