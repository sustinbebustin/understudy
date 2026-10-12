#!/usr/bin/env bash
# KMS: keys, aliases, key policies, rotation, tags, key states and scheduled
# deletion, Encrypt/Decrypt/GenerateDataKey/ReEncrypt/GenerateRandom.
# Arbiter: docs/research/phase1/iam-sts-kms.md section 5.

# mkkey [create-key args...]: sets KEY_ID and KEY_ARN.
mkkey() {
  aws_ok kms create-key "$@"
  KEY_ID=$(jqv '.KeyMetadata.KeyId')
  KEY_ARN=$(jqv '.KeyMetadata.Arn')
}

b64() { printf '%s' "$1" | base64 -w0; }

test_create_key_metadata_shape() {
  local acct
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  aws_ok kms create-key --description 'conformance key'
  assert_jq_match '.KeyMetadata.KeyId' '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
  assert_jq '.KeyMetadata.Arn' "arn:aws:kms:${AWS_REGION}:${acct}:key/$(jqv '.KeyMetadata.KeyId')"
  assert_jq '.KeyMetadata.AWSAccountId' "$acct"
  assert_jq '.KeyMetadata.Description' 'conformance key'
  assert_jq '.KeyMetadata.KeyState' 'Enabled'
  assert_jq '.KeyMetadata.Enabled' 'true'
  assert_jq '.KeyMetadata.KeyUsage' 'ENCRYPT_DECRYPT'
  assert_jq '.KeyMetadata.KeySpec' 'SYMMETRIC_DEFAULT'
  assert_jq '.KeyMetadata.Origin' 'AWS_KMS'
  assert_jq '.KeyMetadata.KeyManager' 'CUSTOMER'
  assert_jq '.KeyMetadata.MultiRegion' 'false'
  assert_jq_true '.KeyMetadata.EncryptionAlgorithms == ["SYMMETRIC_DEFAULT"]'
  assert_jq_match '.KeyMetadata.CreationDate' '^[0-9]{4}-'
}

test_create_key_invalid_key_usage() {
  aws_error ValidationException 400 kms create-key --key-usage RANDOM
  aws_error_message_contains "Value 'RANDOM' at 'keyUsage' failed to satisfy constraint: Member must satisfy enum value set: ["
  local value
  for value in ENCRYPT_DECRYPT SIGN_VERIFY GENERATE_VERIFY_MAC; do
    aws_error_message_contains "$value"
  done
}

test_describe_key_by_id_arn_and_alias() {
  mkkey
  local alias
  alias="alias/$(new_name kms-desc)"
  aws_ok kms create-alias --alias-name "$alias" --target-key-id "$KEY_ID"
  local ref
  for ref in "$KEY_ID" "$KEY_ARN" "$alias"; do
    aws_ok kms describe-key --key-id "$ref"
    assert_jq '.KeyMetadata.KeyId' "$KEY_ID" "describe-key by $ref"
  done
}

test_describe_key_missing() {
  aws_error NotFoundException 400 kms describe-key --key-id 11111111-2222-3333-4444-555555555555
  aws_error_message_contains "does not exist"
}

test_list_keys_contains_created_key() {
  mkkey
  aws_ok kms list-keys
  assert_jq_true "[.Keys[].KeyId] | index(\"$KEY_ID\") != null"
  assert_jq_true ".Keys | map(select(.KeyId == \"$KEY_ID\"))[0].KeyArn == \"$KEY_ARN\""
}

test_update_key_description() {
  mkkey --description before
  aws_ok kms update-key-description --key-id "$KEY_ID" --description after
  aws_ok kms describe-key --key-id "$KEY_ID"
  assert_jq '.KeyMetadata.Description' 'after'
}

test_alias_lifecycle() {
  mkkey
  local alias acct
  alias="alias/$(new_name kms-al)"
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  aws_ok kms create-alias --alias-name "$alias" --target-key-id "$KEY_ID"
  aws_ok kms list-aliases
  assert_jq_true ".Aliases | map(select(.AliasName == \"$alias\")) | length == 1"
  assert_jq ".Aliases | map(select(.AliasName == \"$alias\"))[0].TargetKeyId" "$KEY_ID"
  assert_jq ".Aliases | map(select(.AliasName == \"$alias\"))[0].AliasArn" "arn:aws:kms:${AWS_REGION}:${acct}:${alias}"
  aws_ok kms list-aliases --key-id "$KEY_ID"
  assert_jq '.Aliases | length' '1'
  aws_ok kms delete-alias --alias-name "$alias"
  aws_error NotFoundException 400 kms describe-key --key-id "$alias"
}

test_alias_must_start_with_alias_prefix() {
  mkkey
  aws_error ValidationException 400 kms create-alias --alias-name "no-prefix-$(new_name kms)" --target-key-id "$KEY_ID"
  aws_error_message_contains 'Alias must start with the prefix "alias/".'
}

test_alias_duplicate_is_already_exists() {
  mkkey
  local alias
  alias="alias/$(new_name kms-dupal)"
  aws_ok kms create-alias --alias-name "$alias" --target-key-id "$KEY_ID"
  # The KMS model binds AlreadyExistsException to HTTP 409.
  aws_error AlreadyExistsException 409 kms create-alias --alias-name "$alias" --target-key-id "$KEY_ID"
}

test_update_alias_retargets_key() {
  mkkey
  local first=$KEY_ID alias
  mkkey
  local second=$KEY_ID
  alias="alias/$(new_name kms-upd)"
  aws_ok kms create-alias --alias-name "$alias" --target-key-id "$first"
  aws_ok kms update-alias --alias-name "$alias" --target-key-id "$second"
  aws_ok kms describe-key --key-id "$alias"
  assert_jq '.KeyMetadata.KeyId' "$second"
}

test_alias_for_missing_key() {
  aws_error NotFoundException 400 kms create-alias --alias-name "alias/$(new_name kms-ghost)" \
    --target-key-id 11111111-2222-3333-4444-555555555555
}

test_encrypt_decrypt_round_trip() {
  mkkey
  local plain='the quick brown fox'
  aws_ok kms encrypt --key-id "$KEY_ID" --plaintext "$(b64 "$plain")"
  assert_jq '.KeyId' "$KEY_ARN"
  assert_jq '.EncryptionAlgorithm' 'SYMMETRIC_DEFAULT'
  local blob
  blob=$(jqv '.CiphertextBlob')
  assert_ne "$(b64 "$plain")" "$blob" 'ciphertext differs from plaintext'
  aws_ok kms decrypt --ciphertext-blob "$blob"
  assert_jq '.KeyId' "$KEY_ARN"
  assert_jq '.Plaintext' "$(b64 "$plain")"
}

test_encrypt_is_randomised() {
  mkkey
  aws_ok kms encrypt --key-id "$KEY_ID" --plaintext "$(b64 same)"
  local first
  first=$(jqv '.CiphertextBlob')
  aws_ok kms encrypt --key-id "$KEY_ID" --plaintext "$(b64 same)"
  assert_ne "$first" "$(jqv '.CiphertextBlob')" 'two encryptions of the same plaintext'
}

test_encrypt_through_alias_and_decrypt_with_key_id() {
  mkkey
  local alias
  alias="alias/$(new_name kms-enc)"
  aws_ok kms create-alias --alias-name "$alias" --target-key-id "$KEY_ID"
  aws_ok kms encrypt --key-id "$alias" --plaintext "$(b64 via-alias)"
  local blob
  blob=$(jqv '.CiphertextBlob')
  aws_ok kms decrypt --ciphertext-blob "$blob" --key-id "$KEY_ID"
  assert_jq '.Plaintext' "$(b64 via-alias)"
}

test_decrypt_requires_matching_encryption_context() {
  mkkey
  aws_ok kms encrypt --key-id "$KEY_ID" --plaintext "$(b64 ctx)" --encryption-context purpose=test
  local blob
  blob=$(jqv '.CiphertextBlob')
  aws_ok kms decrypt --ciphertext-blob "$blob" --encryption-context purpose=test
  assert_jq '.Plaintext' "$(b64 ctx)"
  aws_error InvalidCiphertextException 400 kms decrypt --ciphertext-blob "$blob"
  aws_error InvalidCiphertextException 400 kms decrypt --ciphertext-blob "$blob" --encryption-context purpose=other
}

test_decrypt_with_other_key_id_is_incorrect_key() {
  mkkey
  local first=$KEY_ID
  mkkey
  aws_ok kms encrypt --key-id "$first" --plaintext "$(b64 x)"
  aws_error IncorrectKeyException 400 kms decrypt --ciphertext-blob "$(jqv '.CiphertextBlob')" --key-id "$KEY_ID"
}

test_decrypt_garbage_is_invalid_ciphertext() {
  aws_error InvalidCiphertextException 400 kms decrypt --ciphertext-blob "$(b64 'this is not a ciphertext blob')"
}

test_encrypt_plaintext_over_4096_bytes() {
  mkkey
  local f
  f=$(mktemp "$UE_TMP/big.XXXXXX")
  head -c 4097 /dev/zero >"$f"
  aws_error ValidationException 400 kms encrypt --key-id "$KEY_ID" --plaintext "fileb://$f"
  aws_error_message_contains "Value at 'plaintext' failed to satisfy constraint: Member must have length less than or equal to 4096"
}

test_encrypt_with_missing_key() {
  aws_error NotFoundException 400 kms encrypt --key-id 11111111-2222-3333-4444-555555555555 --plaintext "$(b64 x)"
}

test_generate_data_key_aes_256() {
  mkkey
  aws_ok kms generate-data-key --key-id "$KEY_ID" --key-spec AES_256
  assert_jq '.KeyId' "$KEY_ARN"
  assert_eq 32 "$(jqv '.Plaintext' | base64 -d | wc -c)" 'AES_256 data key length'
  local plain blob
  plain=$(jqv '.Plaintext')
  blob=$(jqv '.CiphertextBlob')
  aws_ok kms decrypt --ciphertext-blob "$blob"
  assert_jq '.Plaintext' "$plain"
}

test_generate_data_key_aes_128_and_number_of_bytes() {
  mkkey
  aws_ok kms generate-data-key --key-id "$KEY_ID" --key-spec AES_128
  assert_eq 16 "$(jqv '.Plaintext' | base64 -d | wc -c)" 'AES_128 data key length'
  aws_ok kms generate-data-key --key-id "$KEY_ID" --number-of-bytes 48
  assert_eq 48 "$(jqv '.Plaintext' | base64 -d | wc -c)" 'NumberOfBytes data key length'
}

test_generate_data_key_requires_key_spec_or_number_of_bytes() {
  mkkey
  aws_error ValidationException 400 kms generate-data-key --key-id "$KEY_ID"
  aws_error ValidationException 400 kms generate-data-key --key-id "$KEY_ID" --key-spec AES_256 --number-of-bytes 32
}

test_generate_data_key_with_encryption_context() {
  mkkey
  aws_ok kms generate-data-key --key-id "$KEY_ID" --key-spec AES_256 --encryption-context a=b
  local blob
  blob=$(jqv '.CiphertextBlob')
  aws_error InvalidCiphertextException 400 kms decrypt --ciphertext-blob "$blob"
  aws_ok kms decrypt --ciphertext-blob "$blob" --encryption-context a=b
}

test_generate_data_key_without_plaintext() {
  mkkey
  aws_ok kms generate-data-key-without-plaintext --key-id "$KEY_ID" --key-spec AES_256
  assert_jq 'has("Plaintext")' 'false'
  assert_jq '.KeyId' "$KEY_ARN"
  aws_ok kms decrypt --ciphertext-blob "$(jqv '.CiphertextBlob')"
  assert_eq 32 "$(jqv '.Plaintext' | base64 -d | wc -c)"
}

test_re_encrypt_moves_ciphertext_to_destination_key() {
  mkkey
  local source_id=$KEY_ID source_arn=$KEY_ARN
  mkkey
  local dest_id=$KEY_ID dest_arn=$KEY_ARN
  aws_ok kms encrypt --key-id "$source_id" --plaintext "$(b64 moved)"
  aws_ok kms re-encrypt --ciphertext-blob "$(jqv '.CiphertextBlob')" --destination-key-id "$dest_id"
  assert_jq '.SourceKeyId' "$source_arn"
  assert_jq '.KeyId' "$dest_arn"
  aws_ok kms decrypt --ciphertext-blob "$(jqv '.CiphertextBlob')"
  assert_jq '.KeyId' "$dest_arn"
  assert_jq '.Plaintext' "$(b64 moved)"
}

test_generate_random() {
  aws_ok kms generate-random --number-of-bytes 24
  assert_eq 24 "$(jqv '.Plaintext' | base64 -d | wc -c)" 'random byte count'
}

test_disabled_key_rejects_cryptographic_operations() {
  mkkey
  aws_ok kms disable-key --key-id "$KEY_ID"
  aws_ok kms describe-key --key-id "$KEY_ID"
  assert_jq '.KeyMetadata.KeyState' 'Disabled'
  assert_jq '.KeyMetadata.Enabled' 'false'
  aws_error DisabledException 400 kms encrypt --key-id "$KEY_ID" --plaintext "$(b64 x)"
  aws_error_message_contains "$KEY_ARN is disabled."
  aws_error DisabledException 400 kms generate-data-key --key-id "$KEY_ID" --key-spec AES_256
  aws_ok kms enable-key --key-id "$KEY_ID"
  aws_ok kms encrypt --key-id "$KEY_ID" --plaintext "$(b64 x)"
}

test_disabled_key_cannot_decrypt_existing_ciphertext() {
  mkkey
  aws_ok kms encrypt --key-id "$KEY_ID" --plaintext "$(b64 x)"
  local blob
  blob=$(jqv '.CiphertextBlob')
  aws_ok kms disable-key --key-id "$KEY_ID"
  aws_error DisabledException 400 kms decrypt --ciphertext-blob "$blob"
}

test_schedule_key_deletion() {
  mkkey
  aws_ok kms schedule-key-deletion --key-id "$KEY_ID" --pending-window-in-days 7
  assert_jq '.KeyId' "$KEY_ARN"
  assert_jq '.KeyState' 'PendingDeletion'
  assert_jq '.PendingWindowInDays' '7'
  local delta
  delta=$(($(date -d "$(jqv '.DeletionDate')" +%s) - $(date +%s)))
  { [ "$delta" -gt $((6 * 86400)) ] && [ "$delta" -le $((7 * 86400 + 60)) ]; } || fail "DeletionDate is ${delta}s away, expected about 7 days"
  aws_ok kms describe-key --key-id "$KEY_ID"
  assert_jq '.KeyMetadata.KeyState' 'PendingDeletion'
  assert_jq '.KeyMetadata.Enabled' 'false'
  assert_jq_match '.KeyMetadata.DeletionDate' '^[0-9]{4}-'
}

test_schedule_key_deletion_default_window_is_thirty_days() {
  mkkey
  aws_ok kms schedule-key-deletion --key-id "$KEY_ID"
  assert_jq '.PendingWindowInDays' '30'
}

test_schedule_key_deletion_window_out_of_range() {
  mkkey
  aws_error ValidationException 400 kms schedule-key-deletion --key-id "$KEY_ID" --pending-window-in-days 6
  aws_error ValidationException 400 kms schedule-key-deletion --key-id "$KEY_ID" --pending-window-in-days 31
}

test_pending_deletion_key_rejects_cryptographic_operations() {
  mkkey
  aws_ok kms schedule-key-deletion --key-id "$KEY_ID" --pending-window-in-days 7
  aws_error KMSInvalidStateException 400 kms encrypt --key-id "$KEY_ID" --plaintext "$(b64 x)"
  aws_error_message_contains "$KEY_ARN is pending deletion (or pending replica deletion)."
}

test_schedule_key_deletion_twice() {
  mkkey
  aws_ok kms schedule-key-deletion --key-id "$KEY_ID" --pending-window-in-days 7
  aws_error KMSInvalidStateException 400 kms schedule-key-deletion --key-id "$KEY_ID" --pending-window-in-days 7
  aws_error_message_contains 'is pending deletion'
}

test_cancel_key_deletion_leaves_key_disabled() {
  mkkey
  aws_ok kms schedule-key-deletion --key-id "$KEY_ID" --pending-window-in-days 7
  aws_ok kms cancel-key-deletion --key-id "$KEY_ID"
  assert_jq '.KeyId' "$KEY_ARN"
  aws_ok kms describe-key --key-id "$KEY_ID"
  assert_jq '.KeyMetadata.KeyState' 'Disabled'
  aws_ok kms enable-key --key-id "$KEY_ID"
  aws_ok kms encrypt --key-id "$KEY_ID" --plaintext "$(b64 back)"
}

test_cancel_key_deletion_when_not_pending() {
  mkkey
  aws_error KMSInvalidStateException 400 kms cancel-key-deletion --key-id "$KEY_ID"
  aws_error_message_contains "$KEY_ARN is not pending deletion (or pending replica deletion)."
}

test_key_rotation_status_and_toggle() {
  mkkey
  aws_ok kms get-key-rotation-status --key-id "$KEY_ID"
  assert_jq '.KeyRotationEnabled' 'false'
  aws_ok kms enable-key-rotation --key-id "$KEY_ID"
  aws_ok kms get-key-rotation-status --key-id "$KEY_ID"
  assert_jq '.KeyRotationEnabled' 'true'
  aws_ok kms disable-key-rotation --key-id "$KEY_ID"
  aws_ok kms get-key-rotation-status --key-id "$KEY_ID"
  assert_jq '.KeyRotationEnabled' 'false'
}

test_enable_key_rotation_on_disabled_key() {
  mkkey
  aws_ok kms disable-key --key-id "$KEY_ID"
  aws_error DisabledException 400 kms enable-key-rotation --key-id "$KEY_ID"
}

test_default_key_policy() {
  local acct
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  mkkey
  aws_ok kms list-key-policies --key-id "$KEY_ID"
  assert_jq_true '.PolicyNames == ["default"]'
  aws_ok kms get-key-policy --key-id "$KEY_ID" --policy-name default
  assert_jq_true ".Policy | fromjson | .Statement | map(select(.Principal.AWS == \"arn:aws:iam::${acct}:root\" and .Action == \"kms:*\" and .Effect == \"Allow\")) | length >= 1"
}

test_put_key_policy_round_trip() {
  local acct policy
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  mkkey
  policy=$(jq -nc --arg a "arn:aws:iam::${acct}:root" '{Version:"2012-10-17",Id:"custom",Statement:[{Sid:"Enable IAM User Permissions",Effect:"Allow",Principal:{AWS:$a},Action:"kms:*",Resource:"*"}]}')
  aws_ok kms put-key-policy --key-id "$KEY_ID" --policy-name default --policy "$policy"
  aws_ok kms get-key-policy --key-id "$KEY_ID" --policy-name default
  assert_jq_true ".Policy | fromjson == $policy"
}

test_put_key_policy_malformed() {
  mkkey
  aws_error MalformedPolicyDocumentException 400 kms put-key-policy --key-id "$KEY_ID" --policy-name default --policy 'not json'
}

test_key_tags() {
  mkkey --tags TagKey=env,TagValue=test
  aws_ok kms tag-resource --key-id "$KEY_ID" --tags TagKey=team,TagValue=core
  aws_ok kms list-resource-tags --key-id "$KEY_ID"
  assert_jq_true '[.Tags[] | {(.TagKey): .TagValue}] | add == {"env":"test","team":"core"}'
  aws_ok kms untag-resource --key-id "$KEY_ID" --tag-keys env
  aws_ok kms list-resource-tags --key-id "$KEY_ID"
  assert_jq_true '[.Tags[].TagKey] == ["team"]'
}
