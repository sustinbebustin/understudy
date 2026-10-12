#!/usr/bin/env bash
# Lambda: function control plane, versions, aliases, permissions, tags,
# concurrency, function URLs, event source mappings, Invoke and function logs.
# Arbiter: docs/research/phase1/lambda-api.md (sections 2 to 10) and
# docs/research/phase1/lambda-events.md (sections 1 and 7).
#
# Tests that execute code need Docker and the Python 3.13 Lambda base image;
# they skip when the emulator reports Docker as not enabled.

# shellcheck source=../lambda-helpers.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lambda-helpers.sh"

# statement: the policy statement of the last AddPermission, as JSON.
statement() {
  jqv '.Statement | if type == "string" then fromjson else . end'
}

# ---------------------------------------------------------------------------
# CreateFunction, GetFunction, ListFunctions, DeleteFunction
# ---------------------------------------------------------------------------

test_create_function_response_shape() {
  new_fn
  assert_eq 201 "$AWS_STATUS" 'CreateFunction status'
  assert_jq '.FunctionName' "$FN"
  assert_jq '.FunctionArn' "arn:aws:lambda:${AWS_REGION}:${ACCT}:function:${FN}"
  assert_jq '.Runtime' 'python3.13'
  assert_jq '.Handler' 'index.handler'
  assert_jq '.Role' "$ROLE_ARN"
  assert_jq '.MemorySize' '128'
  assert_jq '.Timeout' '3'
  assert_jq '.PackageType' 'Zip'
  assert_jq '.Version' '$LATEST'
  assert_jq_true '.Architectures == ["x86_64"]'
  assert_jq '.Description' ''
  assert_jq '.EphemeralStorage.Size' '512'
  assert_jq '.TracingConfig.Mode' 'PassThrough'
  assert_jq '.LoggingConfig.LogGroup' "/aws/lambda/${FN}"
  assert_jq_match '.CodeSha256' '^[A-Za-z0-9+/]{43}=$'
  assert_jq_match '.CodeSize' '^[1-9][0-9]*$'
  assert_jq_match '.RevisionId' '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
  assert_jq_match '.LastModified' '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}\+0000$'
}

test_create_function_starts_pending() {
  # lambda-api.md 4.5 and 5.1: the create response is Pending/Creating.
  new_fn
  assert_jq '.State' 'Pending'
  assert_jq '.StateReason' 'The function is being created.'
  assert_jq '.StateReasonCode' 'Creating'
}

test_function_becomes_active() {
  new_fn
  wait_active "$FN"
  assert_jq '.State' 'Active'
  assert_jq '.LastUpdateStatus' 'Successful'
  assert_jq '.StateReason' 'null'
}

test_create_function_duplicate() {
  new_fn
  aws_error ResourceConflictException 409 lambda create-function --function-name "$FN" --runtime python3.13 \
    --handler index.handler --role "$ROLE_ARN" --zip-file "fileb://$UE_TMP/$FN.zip"
  aws_error_message_contains "Function already exist: $FN"
}

test_create_function_malformed_role_arn() {
  local zip="$UE_TMP/badrole.zip"
  make_zip "$zip" index.py "$PY_SOURCE"
  aws_error ValidationException 400 lambda create-function --function-name "$(new_name lambda-badrole)" \
    --runtime python3.13 --handler index.handler --role not-an-arn --zip-file "fileb://$zip"
  aws_error_message_contains "at 'role' failed to satisfy constraint"
}

test_create_function_deprecated_runtime() {
  local zip="$UE_TMP/oldrt.zip"
  make_zip "$zip" index.py "$PY_SOURCE"
  aws_error InvalidParameterValueException 400 lambda create-function --function-name "$(new_name lambda-oldrt)" \
    --runtime go1.x --handler main --role "$(make_role "$(new_name lambda-role)")" --zip-file "fileb://$zip"
  aws_error_message_contains 'The runtime parameter of go1.x is no longer supported for creating or updating AWS Lambda functions.'
}

test_create_function_unknown_runtime() {
  local zip="$UE_TMP/unkrt.zip"
  make_zip "$zip" index.py "$PY_SOURCE"
  aws_error InvalidParameterValueException 400 lambda create-function --function-name "$(new_name lambda-unkrt)" \
    --runtime non-existent-runtime --handler index.handler --role "$(make_role "$(new_name lambda-role)")" \
    --zip-file "fileb://$zip"
  aws_error_message_contains "at 'runtime' failed to satisfy constraint: Member must satisfy enum value set"
}

# s3_package KEY-NAME CONTENT-FILE: put CONTENT-FILE into a new bucket (BUCKET)
# under KEY-NAME.
s3_package() {
  BUCKET=$(new_name lambda-code)
  aws_ok s3api create-bucket --bucket "$BUCKET"
  aws_ok s3api put-object --bucket "$BUCKET" --key "$1" --body "$2"
}

test_create_function_from_s3_package() {
  local zip="$UE_TMP/s3-source.zip"
  make_zip "$zip" index.py "$PY_SOURCE"
  s3_package code/fn.zip "$zip"
  FN=$(new_name lambda-s3)
  aws_ok lambda create-function --function-name "$FN" --runtime python3.13 --handler index.handler \
    --role "$(make_role "$(new_name lambda-role)")" --code "S3Bucket=$BUCKET,S3Key=code/fn.zip"
  assert_jq '.FunctionName' "$FN"
  assert_jq_match '.CodeSha256' '^[A-Za-z0-9+/]{43}=$'
  wait_active "$FN"
}

test_create_function_corrupt_zip_in_s3() {
  # The CLI refuses to upload a corrupt archive itself, so the package comes
  # from S3 (lambda-api.md 4.3).
  local bad="$UE_TMP/corrupt.zip"
  printf 'this is not a zip archive' >"$bad"
  s3_package code/corrupt.zip "$bad"
  aws_error InvalidParameterValueException 400 lambda create-function --function-name "$(new_name lambda-corrupt)" \
    --runtime python3.13 --handler index.handler --role "$(make_role "$(new_name lambda-role)")" \
    --code "S3Bucket=$BUCKET,S3Key=code/corrupt.zip"
  aws_error_message_contains 'Could not unzip uploaded file. Please check your file, then try to upload again.'
}

test_create_function_s3_bucket_missing() {
  aws_error InvalidParameterValueException 400 lambda create-function --function-name "$(new_name lambda-nobucket)" \
    --runtime python3.13 --handler index.handler --role "$(make_role "$(new_name lambda-role)")" \
    --code "S3Bucket=$(new_name lambda-nobucket),S3Key=fn.zip"
  aws_error_message_contains 'Error occurred while GetObject. S3 Error Code: NoSuchBucket. S3 Error Message: The specified bucket does not exist'
}

test_create_function_s3_key_missing() {
  local zip="$UE_TMP/s3-other.zip"
  make_zip "$zip" index.py "$PY_SOURCE"
  s3_package code/other.zip "$zip"
  aws_error InvalidParameterValueException 400 lambda create-function --function-name "$(new_name lambda-nokey)" \
    --runtime python3.13 --handler index.handler --role "$(make_role "$(new_name lambda-role)")" \
    --code "S3Bucket=$BUCKET,S3Key=code/missing.zip"
  aws_error_message_contains 'Error occurred while GetObject. S3 Error Code: NoSuchKey. S3 Error Message: The specified key does not exist.'
}

test_create_function_with_settings_and_tags() {
  new_fn --memory-size 256 --timeout 10 --description "conformance fn" --architectures arm64 \
    --environment 'Variables={GREETING=hello,OTHER=world}' --tags env=test,team=core
  assert_jq '.MemorySize' '256'
  assert_jq '.Timeout' '10'
  assert_jq '.Description' 'conformance fn'
  assert_jq_true '.Architectures == ["arm64"]'
  assert_jq_true '.Environment.Variables == {"GREETING":"hello","OTHER":"world"}'
  aws_ok lambda list-tags --resource "$FN_ARN"
  assert_jq_true '.Tags == {"env":"test","team":"core"}'
}

test_get_function_shape() {
  new_fn --tags env=test
  local zip_sha
  zip_sha=$(jqv '.CodeSha256')
  wait_active "$FN"
  aws_ok lambda get-function --function-name "$FN"
  assert_jq '.Configuration.FunctionName' "$FN"
  assert_jq '.Configuration.State' 'Active'
  assert_jq '.Configuration.CodeSha256' "$zip_sha"
  assert_jq '.Code.RepositoryType' 'S3'
  assert_jq_true '.Tags == {"env":"test"}'
  assert_jq '.Concurrency | type' 'null'
}

test_get_function_code_location_is_a_url() {
  # lambda-api.md 6.1: Code.Location is a presigned URL of the package.
  new_fn
  wait_active "$FN"
  aws_ok lambda get-function --function-name "$FN"
  assert_jq_match '.Code.Location' '^https?://'
}

test_get_function_by_arn() {
  new_fn
  aws_ok lambda get-function-configuration --function-name "$FN_ARN"
  assert_jq '.FunctionName' "$FN"
  aws_ok lambda get-function-configuration --function-name "${ACCT}:function:${FN}"
  assert_jq '.FunctionName' "$FN"
}

test_get_function_missing() {
  aws_ok sts get-caller-identity
  local acct name
  acct=$(jqv '.Account')
  name=$(new_name lambda-missing)
  aws_error ResourceNotFoundException 404 lambda get-function --function-name "$name"
  aws_error_message_contains "Function not found: arn:aws:lambda:${AWS_REGION}:${acct}:function:${name}"
}

test_get_function_missing_version() {
  new_fn
  aws_error ResourceNotFoundException 404 lambda get-function --function-name "$FN" --qualifier 10
  aws_error_message_contains "Function not found: ${FN_ARN}:10"
}

test_list_functions_includes_function() {
  new_fn
  aws_ok lambda list-functions
  assert_jq_true "[.Functions[].FunctionName] | index(\"$FN\") != null"
  assert_jq_true "[.Functions[] | select(.FunctionName == \"$FN\")][0].FunctionArn == \"$FN_ARN\""
}

test_list_functions_version_all_rejects_other_values() {
  aws_error ValidationException 400 lambda list-functions --function-version LATEST
  aws_error_message_contains "functionVersion' failed to satisfy constraint: Member must satisfy enum value set: [ALL]"
}

test_delete_function() {
  new_fn
  aws_ok lambda delete-function --function-name "$FN"
  assert_eq 204 "$AWS_STATUS" 'DeleteFunction status'
  aws_error ResourceNotFoundException 404 lambda get-function --function-name "$FN"
}

test_delete_function_missing() {
  aws_error ResourceNotFoundException 404 lambda delete-function --function-name "$(new_name lambda-missing)"
}

test_delete_function_latest_qualifier_is_rejected() {
  new_fn
  aws_error InvalidParameterValueException 400 lambda delete-function --function-name "$FN" --qualifier '$LATEST'
  aws_error_message_contains '$LATEST version cannot be deleted without deleting the function.'
}

# ---------------------------------------------------------------------------
# UpdateFunctionConfiguration, UpdateFunctionCode
# ---------------------------------------------------------------------------

test_update_function_configuration_is_partial() {
  new_fn --memory-size 192 --environment 'Variables={KEEPME=yes}' --description first
  wait_active "$FN"
  aws_ok lambda update-function-configuration --function-name "$FN" --timeout 9
  assert_jq '.Timeout' '9'
  assert_jq '.MemorySize' '192'
  assert_jq '.Description' 'first'
  assert_jq_true '.Environment.Variables == {"KEEPME":"yes"}'
  assert_jq_match '.LastUpdateStatus' '^(InProgress|Successful)$'
}

test_update_function_configuration_replaces_environment() {
  new_fn --environment 'Variables={ALPHA=1,BETA=2}'
  wait_active "$FN"
  aws_ok lambda update-function-configuration --function-name "$FN" --environment 'Variables={GAMMA=3}'
  wait_active "$FN"
  aws_ok lambda get-function-configuration --function-name "$FN"
  assert_jq_true '.Environment.Variables == {"GAMMA":"3"}'
}

test_update_function_configuration_changes_revision_id() {
  new_fn
  wait_active "$FN"
  local before
  before=$(jqv '.RevisionId')
  aws_ok lambda update-function-configuration --function-name "$FN" --description changed
  assert_ne "$before" "$(jqv '.RevisionId')" 'RevisionId after an update'
}

test_update_function_configuration_stale_revision_id() {
  new_fn
  wait_active "$FN"
  aws_error PreconditionFailedException 412 lambda update-function-configuration --function-name "$FN" \
    --description x --revision-id 00000000-0000-0000-0000-000000000000
  aws_error_message_contains 'The Revision Id provided does not match the latest Revision Id.'
}

test_update_function_configuration_missing_function() {
  aws_error ResourceNotFoundException 404 lambda update-function-configuration \
    --function-name "$(new_name lambda-missing)" --timeout 5
}

test_update_function_code_changes_sha() {
  new_fn
  local old_sha
  old_sha=$(jqv '.CodeSha256')
  wait_active "$FN"
  local zip="$UE_TMP/$FN-v2.zip"
  make_zip "$zip" index.py "$PY_SOURCE
# second revision
"
  aws_ok lambda update-function-code --function-name "$FN" --zip-file "fileb://$zip"
  assert_ne "$old_sha" "$(jqv '.CodeSha256')" 'CodeSha256 after UpdateFunctionCode'
  assert_jq_match '.LastUpdateStatus' '^(InProgress|Successful)$'
  wait_active "$FN"
  aws_ok lambda get-function-configuration --function-name "$FN"
  assert_ne "$old_sha" "$(jqv '.CodeSha256')" 'CodeSha256 after the update completes'
}

# ---------------------------------------------------------------------------
# Versions and aliases
# ---------------------------------------------------------------------------

test_publish_version() {
  new_fn --description "v1 description"
  wait_active "$FN"
  aws_ok lambda publish-version --function-name "$FN"
  assert_eq 201 "$AWS_STATUS" 'PublishVersion status'
  assert_jq '.Version' '1'
  assert_jq '.FunctionArn' "${FN_ARN}:1"
  assert_jq '.Description' 'v1 description'
}

test_publish_version_unchanged_returns_existing() {
  new_fn
  wait_active "$FN"
  aws_ok lambda publish-version --function-name "$FN"
  local rev
  rev=$(jqv '.RevisionId')
  aws_ok lambda publish-version --function-name "$FN"
  assert_jq '.Version' '1'
  assert_jq '.RevisionId' "$rev"
}

test_publish_version_after_change_increments() {
  new_fn
  wait_active "$FN"
  aws_ok lambda publish-version --function-name "$FN"
  aws_ok lambda update-function-configuration --function-name "$FN" --description second
  wait_active "$FN"
  aws_ok lambda publish-version --function-name "$FN"
  assert_jq '.Version' '2'
  assert_jq '.Description' 'second'
}

test_published_version_is_immutable() {
  new_fn --memory-size 128
  wait_active "$FN"
  aws_ok lambda publish-version --function-name "$FN"
  aws_ok lambda update-function-configuration --function-name "$FN" --memory-size 512
  wait_active "$FN"
  aws_ok lambda get-function-configuration --function-name "$FN" --qualifier 1
  assert_jq '.MemorySize' '128'
  assert_jq '.Version' '1'
  aws_ok lambda get-function-configuration --function-name "$FN"
  assert_jq '.MemorySize' '512'
}

test_publish_version_code_sha_mismatch() {
  new_fn
  wait_active "$FN"
  aws_error InvalidParameterValueException 400 lambda publish-version --function-name "$FN" \
    --code-sha256 AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
  aws_error_message_contains 'is different from current CodeSHA256 in $LATEST'
}

test_list_versions_by_function_order() {
  new_fn
  wait_active "$FN"
  aws_ok lambda publish-version --function-name "$FN"
  aws_ok lambda list-versions-by-function --function-name "$FN"
  assert_jq_true '[.Versions[].Version] == ["$LATEST","1"]'
  assert_jq '.Versions[0].FunctionArn' "${FN_ARN}"':$LATEST'
  assert_jq '.Versions[1].FunctionArn' "${FN_ARN}:1"
}

test_alias_lifecycle() {
  new_fn
  wait_active "$FN"
  aws_ok lambda publish-version --function-name "$FN"
  aws_ok lambda create-alias --function-name "$FN" --name live --function-version 1 --description first
  assert_eq 201 "$AWS_STATUS" 'CreateAlias status'
  assert_jq '.AliasArn' "${FN_ARN}:live"
  assert_jq '.Name' 'live'
  assert_jq '.FunctionVersion' '1'
  assert_jq '.Description' 'first'
  assert_jq_match '.RevisionId' '^[0-9a-f-]{36}$'
  aws_ok lambda get-alias --function-name "$FN" --name live
  assert_jq '.AliasArn' "${FN_ARN}:live"
  aws_ok lambda update-alias --function-name "$FN" --name live --function-version '$LATEST' --description second
  assert_jq '.FunctionVersion' '$LATEST'
  assert_jq '.Description' 'second'
  aws_ok lambda list-aliases --function-name "$FN"
  assert_jq_true '[.Aliases[].Name] == ["live"]'
  aws_ok lambda delete-alias --function-name "$FN" --name live
  assert_eq 204 "$AWS_STATUS" 'DeleteAlias status'
  aws_error ResourceNotFoundException 404 lambda get-alias --function-name "$FN" --name live
}

test_create_alias_duplicate() {
  new_fn
  wait_active "$FN"
  aws_ok lambda publish-version --function-name "$FN"
  aws_ok lambda create-alias --function-name "$FN" --name live --function-version 1
  aws_error ResourceConflictException 409 lambda create-alias --function-name "$FN" --name live --function-version 1
  aws_error_message_contains "Alias already exists: ${FN_ARN}:live"
}

test_get_alias_missing() {
  new_fn
  aws_error ResourceNotFoundException 404 lambda get-alias --function-name "$FN" --name nope
  aws_error_message_contains "Cannot find alias arn: ${FN_ARN}:nope"
}

test_create_alias_to_missing_version() {
  new_fn
  wait_active "$FN"
  aws_error ResourceNotFoundException 404 lambda create-alias --function-name "$FN" --name live --function-version 10
  aws_error_message_contains "Function not found: ${FN_ARN}:10"
}

test_create_alias_all_digit_name_is_rejected() {
  new_fn
  wait_active "$FN"
  aws_ok lambda publish-version --function-name "$FN"
  aws_error ValidationException 400 lambda create-alias --function-name "$FN" --name 123 --function-version 1
}

# ---------------------------------------------------------------------------
# Resource policy: AddPermission, GetPolicy, RemovePermission
# ---------------------------------------------------------------------------

test_add_permission_service_principal() {
  new_fn
  aws_ok lambda add-permission --function-name "$FN" --statement-id sns-invoke --action lambda:InvokeFunction \
    --principal sns.amazonaws.com --source-arn "arn:aws:sns:${AWS_REGION}:${ACCT}:some-topic" --source-account "$ACCT"
  assert_eq 201 "$AWS_STATUS" 'AddPermission status'
  local stmt
  stmt=$(statement)
  AWS_OUT=$stmt
  assert_jq '.Sid' 'sns-invoke'
  assert_jq '.Effect' 'Allow'
  assert_jq '.Principal.Service' 'sns.amazonaws.com'
  assert_jq '.Action' 'lambda:InvokeFunction'
  assert_jq '.Resource' "$FN_ARN"
  assert_jq '.Condition.ArnLike["AWS:SourceArn"]' "arn:aws:sns:${AWS_REGION}:${ACCT}:some-topic"
  assert_jq '.Condition.StringEquals["AWS:SourceAccount"]' "$ACCT"
}

test_add_permission_without_conditions_has_no_condition_block() {
  new_fn
  aws_ok lambda add-permission --function-name "$FN" --statement-id acct --action lambda:InvokeFunction --principal "$ACCT"
  local stmt
  stmt=$(statement)
  AWS_OUT=$stmt
  assert_jq '.Principal.AWS' "arn:aws:iam::${ACCT}:root"
  assert_jq 'has("Condition")' 'false'
}

test_get_policy_document() {
  new_fn
  aws_ok lambda add-permission --function-name "$FN" --statement-id first --action lambda:InvokeFunction --principal s3.amazonaws.com
  aws_ok lambda add-permission --function-name "$FN" --statement-id second --action lambda:InvokeFunction --principal events.amazonaws.com
  aws_ok lambda get-policy --function-name "$FN"
  assert_jq_match '.RevisionId' '^[0-9a-f-]{36}$'
  local policy
  policy=$(jqv '.Policy')
  AWS_OUT=$policy
  assert_jq '.Version' '2012-10-17'
  assert_jq '.Id' 'default'
  assert_jq_true '[.Statement[].Sid] == ["first","second"]'
}

test_add_permission_changes_policy_revision_id() {
  new_fn
  aws_ok lambda add-permission --function-name "$FN" --statement-id first --action lambda:InvokeFunction --principal s3.amazonaws.com
  aws_ok lambda get-policy --function-name "$FN"
  local before
  before=$(jqv '.RevisionId')
  aws_ok lambda add-permission --function-name "$FN" --statement-id second --action lambda:InvokeFunction --principal events.amazonaws.com
  aws_ok lambda get-policy --function-name "$FN"
  assert_ne "$before" "$(jqv '.RevisionId')" 'policy RevisionId after AddPermission'
}

test_add_permission_duplicate_statement_id() {
  new_fn
  aws_ok lambda add-permission --function-name "$FN" --statement-id dup --action lambda:InvokeFunction --principal s3.amazonaws.com
  aws_error ResourceConflictException 409 lambda add-permission --function-name "$FN" --statement-id dup \
    --action lambda:InvokeFunction --principal s3.amazonaws.com
  aws_error_message_contains 'The statement id (dup) provided already exists. Please provide a new statement id, or remove the existing statement.'
}

test_add_permission_latest_qualifier_is_rejected() {
  new_fn
  aws_error InvalidParameterValueException 400 lambda add-permission --function-name "$FN" --qualifier '$LATEST' \
    --statement-id latest --action lambda:InvokeFunction --principal s3.amazonaws.com
  aws_error_message_contains 'We currently do not support adding policies for $LATEST.'
}

test_add_permission_statement_id_pattern() {
  new_fn
  aws_error ValidationException 400 lambda add-permission --function-name "$FN" --statement-id example.com \
    --action lambda:InvokeFunction --principal s3.amazonaws.com
}

test_add_permission_missing_function() {
  aws_error ResourceNotFoundException 404 lambda add-permission --function-name "$(new_name lambda-missing)" \
    --statement-id x --action lambda:InvokeFunction --principal s3.amazonaws.com
}

test_remove_permission() {
  new_fn
  aws_ok lambda add-permission --function-name "$FN" --statement-id gone --action lambda:InvokeFunction --principal s3.amazonaws.com
  aws_ok lambda remove-permission --function-name "$FN" --statement-id gone
  assert_eq 204 "$AWS_STATUS" 'RemovePermission status'
  aws_error ResourceNotFoundException 404 lambda get-policy --function-name "$FN"
}

test_remove_permission_unknown_statement() {
  new_fn
  aws_ok lambda add-permission --function-name "$FN" --statement-id keep --action lambda:InvokeFunction --principal s3.amazonaws.com
  aws_error ResourceNotFoundException 404 lambda remove-permission --function-name "$FN" --statement-id non-existent
  aws_error_message_contains 'Statement non-existent is not found in resource policy.'
}

test_get_policy_without_policy() {
  new_fn
  aws_error ResourceNotFoundException 404 lambda get-policy --function-name "$FN"
}

# ---------------------------------------------------------------------------
# Tags and concurrency
# ---------------------------------------------------------------------------

test_tag_untag_list_tags() {
  new_fn
  aws_ok lambda tag-resource --resource "$FN_ARN" --tags a=1,b=2
  assert_eq 204 "$AWS_STATUS" 'TagResource status'
  aws_ok lambda tag-resource --resource "$FN_ARN" --tags b=3,c=4
  aws_ok lambda list-tags --resource "$FN_ARN"
  assert_jq_true '.Tags == {"a":"1","b":"3","c":"4"}'
  aws_ok lambda untag-resource --resource "$FN_ARN" --tag-keys a c
  assert_eq 204 "$AWS_STATUS" 'UntagResource status'
  aws_ok lambda list-tags --resource "$FN_ARN"
  assert_jq_true '.Tags == {"b":"3"}'
  aws_ok lambda get-function --function-name "$FN"
  assert_jq_true '.Tags == {"b":"3"}'
}

test_tag_resource_missing_function() {
  aws_ok sts get-caller-identity
  aws_error ResourceNotFoundException 404 lambda tag-resource \
    --resource "arn:aws:lambda:${AWS_REGION}:$(jqv '.Account'):function:$(new_name lambda-missing)" --tags a=1
}

test_reserved_concurrency() {
  new_fn
  aws_ok lambda put-function-concurrency --function-name "$FN" --reserved-concurrent-executions 5
  assert_jq '.ReservedConcurrentExecutions' '5'
  aws_ok lambda get-function-concurrency --function-name "$FN"
  assert_jq '.ReservedConcurrentExecutions' '5'
  aws_ok lambda get-function --function-name "$FN"
  assert_jq '.Concurrency.ReservedConcurrentExecutions' '5'
  aws_ok lambda delete-function-concurrency --function-name "$FN"
  assert_eq 204 "$AWS_STATUS" 'DeleteFunctionConcurrency status'
  aws_ok lambda get-function-concurrency --function-name "$FN"
  assert_jq 'has("ReservedConcurrentExecutions")' 'false'
}

test_reserved_concurrency_on_version_is_rejected() {
  new_fn
  wait_active "$FN"
  aws_ok lambda publish-version --function-name "$FN"
  aws_error InvalidParameterValueException 400 lambda put-function-concurrency --function-name "${FN}:1" \
    --reserved-concurrent-executions 1
  aws_error_message_contains 'This operation is permitted on Lambda functions only.'
}

# ---------------------------------------------------------------------------
# Function URLs
# ---------------------------------------------------------------------------

test_function_url_config_lifecycle() {
  new_fn
  aws_ok lambda create-function-url-config --function-name "$FN" --auth-type NONE
  assert_eq 201 "$AWS_STATUS" 'CreateFunctionUrlConfig status'
  assert_jq_match '.FunctionUrl' "^https?://[a-z0-9]+\\.lambda-url\\.${AWS_REGION}\\."
  assert_jq '.FunctionArn' "$FN_ARN"
  assert_jq '.AuthType' 'NONE'
  assert_jq_match '.CreationTime' '^[0-9]{4}-[0-9]{2}-[0-9]{2}T'
  local url
  url=$(jqv '.FunctionUrl')
  [ "${#url}" -ge 40 ] && [ "${#url}" -le 100 ] || fail "FunctionUrl [$url] is not 40 to 100 characters long"
  aws_ok lambda get-function-url-config --function-name "$FN"
  assert_jq '.FunctionUrl' "$url"
  assert_jq_match '.LastModifiedTime' '^[0-9]{4}-[0-9]{2}-[0-9]{2}T'
  aws_ok lambda update-function-url-config --function-name "$FN" --auth-type AWS_IAM
  assert_jq '.AuthType' 'AWS_IAM'
  assert_jq '.FunctionUrl' "$url"
  aws_ok lambda list-function-url-configs --function-name "$FN"
  assert_jq_true '[.FunctionUrlConfigs[].FunctionUrl] == ["'"$url"'"]'
  aws_ok lambda delete-function-url-config --function-name "$FN"
  assert_eq 204 "$AWS_STATUS" 'DeleteFunctionUrlConfig status'
  aws_error ResourceNotFoundException 404 lambda get-function-url-config --function-name "$FN"
}

test_function_url_config_defaults_to_buffered_invoke_mode() {
  # AWS's create response omits InvokeMode when the request does not set it
  # (LocalStack's AWS-recorded test_url_config_lifecycle url_creation;
  # lambda-events.md 7.1); the reads report the BUFFERED default.
  new_fn
  aws_ok lambda create-function-url-config --function-name "$FN" --auth-type NONE
  assert_jq '.InvokeMode' 'null'
  aws_ok lambda get-function-url-config --function-name "$FN"
  assert_jq '.InvokeMode' 'BUFFERED'
}

test_function_url_config_with_cors() {
  new_fn
  aws_ok lambda create-function-url-config --function-name "$FN" --auth-type NONE \
    --cors 'AllowOrigins=https://example.com,AllowMethods=GET,POST,MaxAge=600'
  assert_jq_true '.Cors.AllowOrigins == ["https://example.com"]'
  assert_jq_true '.Cors.AllowMethods == ["GET","POST"]'
  assert_jq '.Cors.MaxAge' '600'
}

test_function_url_config_duplicate() {
  new_fn
  aws_ok lambda create-function-url-config --function-name "$FN" --auth-type NONE
  aws_error ResourceConflictException 409 lambda create-function-url-config --function-name "$FN" --auth-type NONE
  aws_error_message_contains "FunctionUrlConfig exists for this Lambda function"
}

test_function_url_config_missing_function() {
  aws_error ResourceNotFoundException 404 lambda create-function-url-config \
    --function-name "$(new_name lambda-missing)" --auth-type NONE
}

test_function_url_qualifier_must_be_an_alias() {
  new_fn
  wait_active "$FN"
  aws_ok lambda publish-version --function-name "$FN"
  aws_error ValidationException 400 lambda create-function-url-config --function-name "$FN" --auth-type NONE --qualifier 1
}

# invoke_url FUNCTION-URL PATH: request PATH on the function URL by its Host
# header. Sets URL_STATUS, URL_BODY (file) and URL_HEADERS (file).
invoke_url() {
  local host=${1#*://}
  host=${host%%/*}
  URL_BODY=$(mktemp "$UE_TMP/urlbody.XXXXXX")
  URL_HEADERS=$(mktemp "$UE_TMP/urlhdrs.XXXXXX")
  URL_STATUS=$(curl -sS -o "$URL_BODY" -D "$URL_HEADERS" -w '%{http_code}' -H "Host: $host" "${@:3}" "${AWS_ENDPOINT_URL}$2")
}

test_function_url_without_permission_is_forbidden() {
  require_docker
  new_fn
  wait_active "$FN"
  aws_ok lambda create-function-url-config --function-name "$FN" --auth-type NONE
  invoke_url "$(jqv '.FunctionUrl')" /
  assert_eq 403 "$URL_STATUS" 'status without a resource policy'
  assert_contains 'Forbidden. For troubleshooting Function URL authorization issues, see: https://docs.aws.amazon.com/lambda/latest/dg/urls-auth.html' \
    "$(cat "$URL_BODY")" 'body'
}

test_function_url_iam_auth_rejects_unsigned_requests() {
  require_docker
  new_fn
  wait_active "$FN"
  aws_ok lambda create-function-url-config --function-name "$FN" --auth-type AWS_IAM
  invoke_url "$(jqv '.FunctionUrl')" /
  assert_eq 403 "$URL_STATUS" 'status of an unsigned request to an AWS_IAM URL'
}

test_function_url_invocation_payload_v2() {
  require_docker
  new_fn
  wait_active "$FN"
  aws_ok lambda create-function-url-config --function-name "$FN" --auth-type NONE
  local url
  url=$(jqv '.FunctionUrl')
  aws_ok lambda add-permission --function-name "$FN" --statement-id FunctionURLAllowPublicAccess \
    --action lambda:InvokeFunctionUrl --principal '*' --function-url-auth-type NONE
  aws_ok lambda add-permission --function-name "$FN" --statement-id FunctionURLInvokeAllowPublicAccess \
    --action lambda:InvokeFunction --principal '*' --invoked-via-function-url
  invoke_url "$url" '/some/path?x=1&y=two' -X POST -H 'content-type: text/plain' --data 'hello url'
  assert_eq 201 "$URL_STATUS" 'status returned by the function'
  assert_contains 'x-conformance: yes' "$(tr -d '\r' <"$URL_HEADERS" | tr 'A-Z' 'a-z')" 'response headers (lowercased)'
  AWS_OUT=$(cat "$URL_BODY")
  assert_jq '.rawPath' '/some/path'
  assert_jq '.method' 'POST'
  assert_jq_true '.query == {"x":"1","y":"two"}'
  assert_jq '.body' 'hello url'
}

# ---------------------------------------------------------------------------
# Invoke
# ---------------------------------------------------------------------------

test_invoke_request_response() {
  require_docker
  new_fn
  wait_active "$FN"
  invoke_ok "$FN" '{"hello":"world"}'
  assert_jq '.StatusCode' '200'
  assert_jq '.ExecutedVersion' '$LATEST'
  assert_jq 'has("FunctionError")' 'false'
  local out
  out=$(invoke_payload)
  assert_eq '{"echo":{"hello":"world"},"function_name":"'"$FN"'","function_version":"$LATEST"}' "$out" 'payload'
}

test_invoke_event_returns_202_and_runs() {
  require_docker
  new_fn
  wait_active "$FN"
  invoke_ok "$FN" '{"marker":"async-marker-9f3a"}' --invocation-type Event
  assert_jq '.StatusCode' '202'
  [ ! -s "$INVOKE_OUT" ] || fail "an Event invoke returned a payload: $(cat "$INVOKE_OUT")"
  wait_logs "/aws/lambda/$FN" async-marker-9f3a 90
}

test_invoke_dry_run() {
  require_docker
  new_fn
  wait_active "$FN"
  invoke_ok "$FN" '{}' --invocation-type DryRun
  assert_jq '.StatusCode' '204'
  [ ! -s "$INVOKE_OUT" ] || fail "a DryRun returned a payload: $(cat "$INVOKE_OUT")"
}

test_invoke_function_error() {
  require_docker
  new_fn
  wait_active "$FN"
  invoke_ok "$FN" '{"action":"fail"}'
  assert_jq '.StatusCode' '200'
  assert_jq '.FunctionError' 'Unhandled'
  local out
  out=$(invoke_payload)
  AWS_OUT=$out
  assert_jq '.errorMessage' 'Handler fails'
  assert_jq '.errorType' 'Exception'
  assert_jq_true '.stackTrace | length > 0'
}

test_invoke_timeout() {
  require_docker
  new_fn --timeout 1
  wait_active "$FN"
  invoke_ok "$FN" '{"action":"sleep","seconds":5}'
  assert_jq '.StatusCode' '200'
  assert_jq '.FunctionError' 'Unhandled'
  local out
  out=$(invoke_payload)
  AWS_OUT=$out
  assert_jq '.errorType' 'Sandbox.Timedout'
  assert_jq_match '.errorMessage' '^RequestId: [0-9a-f-]{36} Error: Task timed out after 1\.00 seconds$'
}

test_invoke_log_type_tail() {
  require_docker
  new_fn
  wait_active "$FN"
  invoke_ok "$FN" '{"tail":"tail-marker-77"}' --log-type Tail
  local log
  log=$(jqv '.LogResult' | base64 -d)
  assert_match '^START RequestId: [0-9a-f-]{36} Version: \$LATEST' "$log" 'log tail'
  assert_contains 'conformance-log-line' "$log" 'log tail'
  assert_contains 'tail-marker-77' "$log" 'log tail'
  assert_match 'END RequestId: [0-9a-f-]{36}' "$log" 'log tail'
  assert_match 'REPORT RequestId: [0-9a-f-]{36}	Duration: [0-9.]+ ms	Billed Duration: [0-9]+ ms	Memory Size: 128 MB' "$log" 'log tail'
  [ "$(printf '%s' "$log" | wc -c)" -le 4096 ] || fail "LogResult is longer than 4 KB"
}

test_invoke_report_line_has_max_memory_used() {
  # lambda-api.md 9.7: every REPORT line carries Max Memory Used.
  require_docker
  new_fn
  wait_active "$FN"
  invoke_ok "$FN" '{}' --log-type Tail
  assert_match 'REPORT RequestId: [0-9a-f-]{36}	Duration: [0-9.]+ ms	Billed Duration: [0-9]+ ms	Memory Size: 128 MB	Max Memory Used: [0-9]+ MB' \
    "$(jqv '.LogResult' | base64 -d)" 'REPORT line'
}

test_invoke_environment_and_context() {
  require_docker
  new_fn --memory-size 256 --environment 'Variables={GREETING=hello}'
  wait_active "$FN"
  invoke_ok "$FN" '{"action":"env","keys":["GREETING","AWS_LAMBDA_FUNCTION_NAME","AWS_LAMBDA_FUNCTION_VERSION","AWS_LAMBDA_FUNCTION_MEMORY_SIZE","AWS_REGION","_HANDLER","LAMBDA_TASK_ROOT","AWS_LAMBDA_LOG_GROUP_NAME"]}'
  local out
  out=$(invoke_payload)
  AWS_OUT=$out
  assert_jq '.GREETING' 'hello'
  assert_jq '.AWS_LAMBDA_FUNCTION_NAME' "$FN"
  assert_jq '.AWS_LAMBDA_FUNCTION_VERSION' '$LATEST'
  assert_jq '.AWS_LAMBDA_FUNCTION_MEMORY_SIZE' '256'
  assert_jq '.AWS_REGION' "$AWS_REGION"
  assert_jq '._HANDLER' 'index.handler'
  assert_jq '.LAMBDA_TASK_ROOT' '/var/task'
  assert_jq '.AWS_LAMBDA_LOG_GROUP_NAME' "/aws/lambda/$FN"
}

test_invoke_missing_function() {
  aws_ok sts get-caller-identity
  local acct name out
  acct=$(jqv '.Account')
  name=$(new_name lambda-missing)
  out=$(mktemp "$UE_TMP/invoke.XXXXXX")
  aws_error ResourceNotFoundException 404 lambda invoke --function-name "$name" --cli-binary-format raw-in-base64-out \
    --payload '{}' "$out"
  aws_error_message_contains "Function not found: arn:aws:lambda:${AWS_REGION}:${acct}:function:${name}"
}

test_invoke_malformed_json_payload() {
  require_docker
  new_fn
  wait_active "$FN"
  local out
  out=$(mktemp "$UE_TMP/invoke.XXXXXX")
  aws_error InvalidRequestContentException 400 lambda invoke --function-name "$FN" --cli-binary-format raw-in-base64-out \
    --payload 'this is {not json' "$out"
  aws_error_message_contains 'Could not parse request body into json'
}

test_invoke_alias_reports_executed_version() {
  require_docker
  new_fn
  wait_active "$FN"
  aws_ok lambda publish-version --function-name "$FN"
  aws_ok lambda create-alias --function-name "$FN" --name live --function-version 1
  invoke_ok "$FN" '{}' --qualifier live
  assert_jq '.ExecutedVersion' '1'
  local out
  out=$(invoke_payload)
  AWS_OUT=$out
  assert_jq '.function_version' '1'
}

test_invoke_uses_updated_code() {
  require_docker
  new_fn
  wait_active "$FN"
  invoke_ok "$FN" '{}'
  local zip="$UE_TMP/$FN-v2.zip"
  make_zip "$zip" index.py 'def handler(event, context):
    return {"generation": 2}
'
  aws_ok lambda update-function-code --function-name "$FN" --zip-file "fileb://$zip"
  wait_active "$FN"
  invoke_ok "$FN" '{}'
  assert_eq '{"generation":2}' "$(invoke_payload)" 'payload after UpdateFunctionCode'
}

test_invoke_published_version_keeps_old_code() {
  require_docker
  new_fn
  wait_active "$FN"
  aws_ok lambda publish-version --function-name "$FN"
  local zip="$UE_TMP/$FN-v2.zip"
  make_zip "$zip" index.py 'def handler(event, context):
    return {"generation": 2}
'
  aws_ok lambda update-function-code --function-name "$FN" --zip-file "fileb://$zip"
  wait_active "$FN"
  invoke_ok "$FN" '{}' --qualifier 1
  assert_jq '.ExecutedVersion' '1'
  local out
  out=$(invoke_payload)
  AWS_OUT=$out
  assert_jq '.function_version' '1'
  invoke_ok "$FN" '{}'
  assert_eq '{"generation":2}' "$(invoke_payload)" 'payload from $LATEST'
}

test_invoke_reserved_concurrency_zero_throttles() {
  require_docker
  new_fn
  wait_active "$FN"
  aws_ok lambda put-function-concurrency --function-name "$FN" --reserved-concurrent-executions 0
  local out
  out=$(mktemp "$UE_TMP/invoke.XXXXXX")
  AWS_MAX_ATTEMPTS=1 aws_error TooManyRequestsException 429 lambda invoke --function-name "$FN" \
    --cli-binary-format raw-in-base64-out --payload '{}' "$out"
  aws_error_message_contains 'Rate Exceeded.'
}

# ---------------------------------------------------------------------------
# Function logs in CloudWatch Logs
# ---------------------------------------------------------------------------

test_invoke_writes_function_logs() {
  require_docker
  new_fn
  wait_active "$FN"
  invoke_ok "$FN" '{"marker":"cwl-marker-5d21"}'
  wait_logs "/aws/lambda/$FN" cwl-marker-5d21 60
  aws_ok logs describe-log-groups --log-group-name-prefix "/aws/lambda/$FN"
  assert_jq_true "[.logGroups[].logGroupName] == [\"/aws/lambda/$FN\"]"
  aws_ok logs describe-log-streams --log-group-name "/aws/lambda/$FN"
  assert_jq_true '.logStreams | length >= 1'
  assert_jq_match '.logStreams[0].logStreamName' '^[0-9]{4}/[0-9]{2}/[0-9]{2}/\[\$LATEST\][0-9a-f]{32}$'
}

test_invoke_log_lines_have_start_end_report() {
  require_docker
  new_fn
  wait_active "$FN"
  invoke_ok "$FN" '{"marker":"lines-marker-8e44"}'
  wait_logs "/aws/lambda/$FN" "REPORT RequestId" 60
  aws_ok logs filter-log-events --log-group-name "/aws/lambda/$FN"
  local messages
  messages=$(jqv '[.events[].message] | join("")')
  assert_match 'START RequestId: [0-9a-f-]{36} Version: \$LATEST' "$messages" 'platform START line'
  assert_contains 'lines-marker-8e44' "$messages" 'function output'
  assert_match 'END RequestId: [0-9a-f-]{36}' "$messages" 'platform END line'
  assert_match 'REPORT RequestId: [0-9a-f-]{36}	Duration: ' "$messages" 'platform REPORT line'
}

# ---------------------------------------------------------------------------
# Event source mappings (SQS)
# ---------------------------------------------------------------------------

# new_queue: create an SQS queue; sets QUEUE_URL and QUEUE_ARN.
new_queue() {
  aws_ok sqs create-queue --queue-name "$(new_name lambda-esm-q)"
  QUEUE_URL=$(jqv '.QueueUrl')
  aws_ok sqs get-queue-attributes --queue-url "$QUEUE_URL" --attribute-names QueueArn
  QUEUE_ARN=$(jqv '.Attributes.QueueArn')
}

test_event_source_mapping_create_response() {
  new_fn
  new_queue
  aws_ok lambda create-event-source-mapping --function-name "$FN" --event-source-arn "$QUEUE_ARN"
  assert_eq 202 "$AWS_STATUS" 'CreateEventSourceMapping status'
  assert_jq '.BatchSize' '10'
  assert_jq '.MaximumBatchingWindowInSeconds' '0'
  assert_jq '.EventSourceArn' "$QUEUE_ARN"
  assert_jq '.FunctionArn' "$FN_ARN"
  assert_jq '.StateTransitionReason' 'USER_INITIATED'
  assert_jq_true '.FunctionResponseTypes == []'
  assert_jq_match '.UUID' '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
  assert_jq "(.EventSourceMappingArn)" "arn:aws:lambda:${AWS_REGION}:${ACCT}:event-source-mapping:$(jqv '.UUID')"
}

test_event_source_mapping_starts_creating() {
  # lambda-events.md 1.5: Create answers State Creating; a later Get shows Enabled.
  new_fn
  new_queue
  aws_ok lambda create-event-source-mapping --function-name "$FN" --event-source-arn "$QUEUE_ARN"
  assert_jq '.State' 'Creating'
}

test_event_source_mapping_becomes_enabled() {
  new_fn
  new_queue
  aws_ok lambda create-event-source-mapping --function-name "$FN" --event-source-arn "$QUEUE_ARN"
  local uuid
  uuid=$(jqv '.UUID')
  local deadline=$((SECONDS + 60))
  while :; do
    aws_ok lambda get-event-source-mapping --uuid "$uuid"
    [ "$(jqv '.State')" = Enabled ] && break
    [ "$SECONDS" -lt "$deadline" ] || fail "mapping $uuid never became Enabled"
    sleep 1
  done
  aws_ok lambda list-event-source-mappings --function-name "$FN"
  assert_jq_true "[.EventSourceMappings[].UUID] == [\"$uuid\"]"
}

test_event_source_mapping_disabled_at_creation() {
  new_fn
  new_queue
  aws_ok lambda create-event-source-mapping --function-name "$FN" --event-source-arn "$QUEUE_ARN" --no-enabled
  assert_jq '.State' 'Disabled'
}

test_event_source_mapping_batch_size_above_ten_needs_batching_window() {
  new_fn
  new_queue
  aws_error InvalidParameterValueException 400 lambda create-event-source-mapping --function-name "$FN" \
    --event-source-arn "$QUEUE_ARN" --batch-size 11
  aws_error_message_contains 'Maximum batch window in seconds must be greater than 0 if maximum batch size is greater than 10'
}

test_event_source_mapping_duplicate() {
  new_fn
  new_queue
  aws_ok lambda create-event-source-mapping --function-name "$FN" --event-source-arn "$QUEUE_ARN"
  local uuid
  uuid=$(jqv '.UUID')
  aws_error ResourceConflictException 409 lambda create-event-source-mapping --function-name "$FN" --event-source-arn "$QUEUE_ARN"
  aws_error_message_contains "An event source mapping with SQS arn (\" ${QUEUE_ARN} \") and function (\" ${FN} \") already exists. Please update or delete the existing mapping with UUID ${uuid}"
}

test_event_source_mapping_missing_function() {
  new_queue
  aws_error InvalidParameterValueException 400 lambda create-event-source-mapping \
    --function-name "$(new_name lambda-missing)" --event-source-arn "$QUEUE_ARN"
  aws_error_message_contains 'Function does not exist'
}

test_event_source_mapping_delete() {
  new_fn
  new_queue
  aws_ok lambda create-event-source-mapping --function-name "$FN" --event-source-arn "$QUEUE_ARN"
  local uuid
  uuid=$(jqv '.UUID')
  aws_ok lambda delete-event-source-mapping --uuid "$uuid"
  assert_eq 202 "$AWS_STATUS" 'DeleteEventSourceMapping status'
  assert_jq '.State' 'Deleting'
  local deadline=$((SECONDS + 60))
  while :; do
    aws_run lambda get-event-source-mapping --uuid "$uuid"
    [ "$AWS_RC" -ne 0 ] && break
    [ "$SECONDS" -lt "$deadline" ] || fail "mapping $uuid still exists after DeleteEventSourceMapping"
    sleep 1
  done
  grep -qF 'An error occurred (ResourceNotFoundException)' "$AWS_ERR" || fail "expected ResourceNotFoundException for a deleted mapping"
}

test_event_source_mapping_delivers_sqs_messages() {
  require_docker
  new_fn
  wait_active "$FN"
  new_queue
  aws_ok lambda create-event-source-mapping --function-name "$FN" --event-source-arn "$QUEUE_ARN"
  aws_ok sqs send-message --queue-url "$QUEUE_URL" --message-body 'esm-body-marker-31c8'
  local message_id
  message_id=$(jqv '.MessageId')
  wait_logs "/aws/lambda/$FN" esm-body-marker-31c8 120
  aws_ok logs filter-log-events --log-group-name "/aws/lambda/$FN" --filter-pattern '"esm-body-marker-31c8"'
  local record
  record=$(jqv '[.events[].message | select(startswith("conformance-log-line"))][0] | ltrimstr("conformance-log-line ")')
  AWS_OUT=$record
  assert_jq '.Records[0].eventSource' 'aws:sqs'
  assert_jq '.Records[0].eventSourceARN' "$QUEUE_ARN"
  assert_jq '.Records[0].awsRegion' "$AWS_REGION"
  assert_jq '.Records[0].body' 'esm-body-marker-31c8'
  assert_jq '.Records[0].messageId' "$message_id"
  assert_jq '.Records[0].md5OfBody' "$(printf '%s' 'esm-body-marker-31c8' | md5sum | cut -d' ' -f1)"
  # A successful batch deletes its messages: the queue drains.
  local deadline=$((SECONDS + 60))
  while :; do
    aws_ok sqs get-queue-attributes --queue-url "$QUEUE_URL" \
      --attribute-names ApproximateNumberOfMessages ApproximateNumberOfMessagesNotVisible
    [ "$(jqv '.Attributes.ApproximateNumberOfMessages')" = 0 ] && [ "$(jqv '.Attributes.ApproximateNumberOfMessagesNotVisible')" = 0 ] && break
    [ "$SECONDS" -lt "$deadline" ] || fail "the processed message was not deleted from the queue"
    sleep 1
  done
}
