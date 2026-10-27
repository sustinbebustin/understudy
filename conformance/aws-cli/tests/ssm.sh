#!/usr/bin/env bash
# SSM Parameter Store: String and SecureString, versions, selectors, batch
# and path reads, history and labels, tags, deletes.
# Arbiter: docs/research/ssm-parameters.md; recorded shapes from
# localstack@8b9a79f tests/aws/services/ssm/test_ssm.snapshot.json.

# ssm_arn NAME: the parameter ARN of a hierarchical NAME (ssm-parameters.md 3.3).
ssm_arn() {
  local acct
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  printf 'arn:aws:ssm:%s:%s:parameter%s' "$AWS_REGION" "$acct" "$1"
}

# assert_jq_json FILTER JSON: the compact jq value of FILTER must equal JSON.
assert_jq_json() {
  local actual want
  actual=$(_out_json | jq -c "$1" 2>/dev/null || echo "<jq failed>")
  want=$(printf '%s' "$2" | jq -c .)
  if [ "$actual" != "$want" ]; then
    fail "jq '$1': expected $want, got $actual"
  fi
}

test_ssm_string_put_get_overwrite_and_version() {
  local name arn ask
  name="/$(new_name ssm)/string"
  arn=$(ssm_arn "$name")
  # test_ssm.snapshot.json:L19-L26
  aws_ok ssm put-parameter --name "$name" --type String --value one
  assert_jq '.Tier' Standard
  assert_jq '.Version' 1
  # test_ssm.snapshot.json:L27-L56
  for ask in "$name" "$arn"; do
    aws_ok ssm get-parameter --name "$ask"
    assert_jq '.Parameter.Name' "$name"
    assert_jq '.Parameter.Value' one
    assert_jq '.Parameter.ARN' "$arn"
    assert_jq '.Parameter.DataType' text
    assert_jq '.Parameter | has("Selector")' false
  done
  # ssm-parameters.md 7.1
  aws_error ParameterAlreadyExists 400 ssm put-parameter --name "$name" --type String --value two
  # ssm-parameters.md 6.1, 6.3
  aws_ok ssm put-parameter --name "$name" --value two --overwrite
  assert_jq '.Version' 2
  aws_ok ssm get-parameter --name "$name:1"
  assert_jq '.Parameter.Value' one
  assert_jq '.Parameter.Selector' ':1'
  aws_ok ssm delete-parameter --name "$name"
}

test_ssm_get_after_delete_is_parameter_not_found() {
  local name
  name="/$(new_name ssm)/gone"
  aws_ok ssm put-parameter --name "$name" --type String --value v
  aws_ok ssm delete-parameter --name "$name"
  # localstack@8b9a79f tests/aws/services/cloudformation/resources/test_ssm.snapshot.json:L105-L114
  aws_error ParameterNotFound 400 ssm get-parameter --name "$name"
}

test_ssm_secure_string_and_kms_decrypt() {
  local name arn blob
  name="/$(new_name ssm)/secure"
  arn=$(ssm_arn "$name")
  aws_ok ssm put-parameter --name "$name" --type SecureString --value hunter2
  aws_ok ssm get-parameter --name "$name" --with-decryption
  assert_jq '.Parameter.Value' hunter2
  aws_ok ssm get-parameter --name "$name"
  assert_jq_true '.Parameter.Value != "hunter2"'
  # ssm-parameters.md 5.2: KMS Decrypt with PARAMETER_ARN opens the value.
  blob=$(mktemp "$UE_TMP/ssm-blob.XXXXXX")
  jqv '.Parameter.Value' | base64 -d >"$blob"
  aws_ok kms decrypt --ciphertext-blob "fileb://$blob" --encryption-context "PARAMETER_ARN=$arn"
  assert_eq hunter2 "$(jqv '.Plaintext' | base64 -d)" "KMS Decrypt of the SecureString ciphertext"
  # ssm-parameters.md 5.4
  aws_ok ssm describe-parameters --parameter-filters "Key=Name,Option=Equals,Values=$name"
  assert_jq_json '[.Parameters[].KeyId]' '["alias/aws/ssm"]'
  aws_ok ssm delete-parameter --name "$name"
}

test_ssm_get_parameters_with_a_missing_name() {
  local base
  base="/$(new_name ssm)"
  aws_ok ssm put-parameter --name "$base/a" --type String --value v
  # ssm-parameters.md 7.3
  aws_ok ssm get-parameters --names "$base/a" "$base/missing"
  assert_jq_json '[.Parameters[].Name]' "[\"$base/a\"]"
  assert_jq_json '.InvalidParameters' "[\"$base/missing\"]"
  aws_ok ssm delete-parameter --name "$base/a"
}

test_ssm_get_parameters_by_path_recursive_across_pages() {
  local base i
  base="/$(new_name ssm)"
  for i in 0 1 2 3 4; do
    aws_ok ssm put-parameter --name "$base/l$((i % 2))/p$i" --type String --value v
  done
  # ssm-parameters.md 7.4: the CLI pages through every result.
  aws_ok ssm get-parameters-by-path --path "$base" --recursive --page-size 2
  assert_jq_json '[.Parameters[].Name] | sort' "[\"$base/l0/p0\",\"$base/l0/p2\",\"$base/l0/p4\",\"$base/l1/p1\",\"$base/l1/p3\"]"
  for i in 0 1 2 3 4; do
    aws_ok ssm delete-parameter --name "$base/l$((i % 2))/p$i"
  done
}

test_ssm_history_and_labels() {
  local name
  name="/$(new_name ssm)/labeled"
  aws_ok ssm put-parameter --name "$name" --type String --value v1
  aws_ok ssm put-parameter --name "$name" --value v2 --overwrite
  # ssm-parameters.md 6.2
  aws_ok ssm label-parameter-version --name "$name" --parameter-version 1 --labels stable 1bad
  assert_jq '.ParameterVersion' 1
  assert_jq_json '.InvalidLabels' '["1bad"]'
  aws_ok ssm get-parameter --name "$name:stable"
  assert_jq '.Parameter.Value' v1
  assert_jq '.Parameter.Selector' ':stable'
  # ssm-parameters.md 7.6
  aws_ok ssm get-parameter-history --name "$name"
  assert_jq_json '[.Parameters[].Version]' '[1,2]'
  assert_jq_json '.Parameters[0].Labels' '["stable"]'
  aws_ok ssm unlabel-parameter-version --name "$name" --parameter-version 1 --labels stable absent
  assert_jq_json '.RemovedLabels' '["stable"]'
  assert_jq_json '.InvalidLabels' '["absent"]'
  aws_ok ssm delete-parameter --name "$name"
}

test_ssm_tags() {
  local name
  name="/$(new_name ssm)/tagged"
  # ssm-parameters.md 7.9
  aws_ok ssm put-parameter --name "$name" --type String --value v --tags Key=env,Value=dev
  aws_ok ssm add-tags-to-resource --resource-type Parameter --resource-id "$name" --tags Key=env,Value=prod Key=team,Value=a
  aws_ok ssm remove-tags-from-resource --resource-type Parameter --resource-id "$name" --tag-keys team
  aws_ok ssm list-tags-for-resource --resource-type Parameter --resource-id "$name"
  assert_jq_json '.TagList' '[{"Key":"env","Value":"prod"}]'
  aws_ok ssm delete-parameter --name "$name"
}

test_ssm_delete_parameters() {
  local base
  base="/$(new_name ssm)"
  aws_ok ssm put-parameter --name "$base/b" --type String --value v
  aws_ok ssm put-parameter --name "$base/a" --type String --value v
  # ssm-parameters.md 7.7: deleted names alphabetical, missing ones listed.
  aws_ok ssm delete-parameters --names "$base/b" "$base/missing" "$base/a"
  assert_jq_json '.DeletedParameters' "[\"$base/a\",\"$base/b\"]"
  assert_jq_json '.InvalidParameters' "[\"$base/missing\"]"
}
