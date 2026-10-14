#!/usr/bin/env bash
# DynamoDB: table lifecycle, items, conditional writes (the lock-table
# workload), queries, transactions, TTL, tags, continuous backups and
# account/region isolation.
# Arbiter: docs/research/dynamodb-local.md (sections 3 to 7) for the
# shimmed surface, and the DynamoDB API reference it cites for the rest.
#
# DynamoDB runs on DynamoDB Local, pulled on first use; every test skips
# under CONFORMANCE_DYNAMODB=0, and while the emulator reports the service as
# not enabled.

# new_table [CREATE-TABLE ARGS...]: create a PAY_PER_REQUEST table keyed by the
# string LockID (name in TABLE) and wait for it to be ACTIVE. The create
# response is saved in CREATE_OUT.
new_table() {
  require_service dynamodb
  TABLE=$(new_name ddb)
  aws_ok dynamodb create-table --table-name "$TABLE" \
    --attribute-definitions AttributeName=LockID,AttributeType=S \
    --key-schema AttributeName=LockID,KeyType=HASH --billing-mode PAY_PER_REQUEST "$@"
  CREATE_OUT=$AWS_OUT
  wait_table_active "$TABLE"
  load_account
}

# load_account: set ACCT to the caller's account id.
load_account() {
  aws_ok sts get-caller-identity
  ACCT=$(jqv '.Account')
}

# wait_table_active NAME: poll DescribeTable until the table is ACTIVE.
wait_table_active() {
  local deadline=$((SECONDS + 60))
  while :; do
    aws_run dynamodb describe-table --table-name "$1"
    if [ "$AWS_RC" -eq 0 ] && [ "$(jqv '.Table.TableStatus')" = ACTIVE ]; then
      return 0
    fi
    [ "$SECONDS" -lt "$deadline" ] || fail "table $1 never became ACTIVE"
    sleep 1
  done
}

# Table lifecycle

test_create_table_response() {
  new_table
  AWS_OUT=$CREATE_OUT
  assert_jq '.TableDescription.TableName' "$TABLE"
  assert_jq_match '.TableDescription.TableStatus' '^(CREATING|ACTIVE)$'
  assert_jq '.TableDescription.TableArn' "arn:aws:dynamodb:${AWS_REGION}:${ACCT}:table/${TABLE}"
  assert_jq_true '.TableDescription.KeySchema == [{"AttributeName":"LockID","KeyType":"HASH"}]'
  assert_jq_true '.TableDescription.AttributeDefinitions == [{"AttributeName":"LockID","AttributeType":"S"}]'
  assert_jq '.TableDescription.BillingModeSummary.BillingMode' 'PAY_PER_REQUEST'
  assert_jq '.TableDescription.ItemCount' '0'
}

test_describe_table_shape() {
  new_table
  aws_ok dynamodb describe-table --table-name "$TABLE"
  assert_jq '.Table.TableStatus' 'ACTIVE'
  assert_jq '.Table.TableArn' "arn:aws:dynamodb:${AWS_REGION}:${ACCT}:table/${TABLE}"
  assert_jq '.Table.DeletionProtectionEnabled' 'false'
  assert_jq '.Table.BillingModeSummary.BillingMode' 'PAY_PER_REQUEST'
  assert_jq_match '.Table.CreationDateTime' '^[0-9]{4}-[0-9]{2}-[0-9]{2}T'
  # Terraform waits on WarmThroughput.Status (dynamodb-local.md 7.2 item 1).
  assert_jq '.Table.WarmThroughput.Status' 'ACTIVE'
}

test_describe_table_by_arn() {
  new_table
  aws_ok dynamodb describe-table --table-name "arn:aws:dynamodb:${AWS_REGION}:${ACCT}:table/${TABLE}"
  assert_jq '.Table.TableName' "$TABLE"
}

test_create_table_provisioned_throughput() {
  new_table --billing-mode PROVISIONED --provisioned-throughput ReadCapacityUnits=5,WriteCapacityUnits=7
  aws_ok dynamodb describe-table --table-name "$TABLE"
  assert_jq '.Table.ProvisionedThroughput.ReadCapacityUnits' '5'
  assert_jq '.Table.ProvisionedThroughput.WriteCapacityUnits' '7'
}

test_create_table_duplicate() {
  new_table
  aws_error ResourceInUseException 400 dynamodb create-table --table-name "$TABLE" \
    --attribute-definitions AttributeName=LockID,AttributeType=S --key-schema AttributeName=LockID,KeyType=HASH \
    --billing-mode PAY_PER_REQUEST
}

test_create_table_name_too_short() {
  require_service dynamodb
  aws_error ValidationException 400 dynamodb create-table --table-name ab \
    --attribute-definitions AttributeName=LockID,AttributeType=S --key-schema AttributeName=LockID,KeyType=HASH \
    --billing-mode PAY_PER_REQUEST
}

test_describe_table_missing() {
  require_service dynamodb
  aws_error ResourceNotFoundException 400 dynamodb describe-table --table-name "$(new_name ddb-missing)"
  aws_error_message_contains 'Requested resource not found'
}

test_list_tables_includes_table() {
  new_table
  aws_ok dynamodb list-tables
  assert_jq_true ".TableNames | index(\"$TABLE\") != null"
}

test_delete_table() {
  new_table
  aws_ok dynamodb delete-table --table-name "$TABLE"
  assert_jq '.TableDescription.TableStatus' 'DELETING'
  local deadline=$((SECONDS + 60))
  while :; do
    aws_run dynamodb describe-table --table-name "$TABLE"
    [ "$AWS_RC" -ne 0 ] && break
    [ "$SECONDS" -lt "$deadline" ] || fail "table $TABLE still exists after DeleteTable"
    sleep 1
  done
  grep -qF 'An error occurred (ResourceNotFoundException)' "$AWS_ERR" || fail "expected ResourceNotFoundException after DeleteTable"
}

test_delete_table_missing() {
  require_service dynamodb
  aws_error ResourceNotFoundException 400 dynamodb delete-table --table-name "$(new_name ddb-missing)"
}

test_delete_table_with_deletion_protection() {
  new_table --deletion-protection-enabled
  aws_error ValidationException 400 dynamodb delete-table --table-name "$TABLE"
  aws_error_message_contains 'Resource cannot be deleted as it is currently protected against deletion. Disable deletion protection first.'
  aws_ok dynamodb update-table --table-name "$TABLE" --no-deletion-protection-enabled
  aws_ok dynamodb delete-table --table-name "$TABLE"
}

# Items

test_put_get_delete_item() {
  new_table
  aws_ok dynamodb put-item --table-name "$TABLE" --item '{"LockID":{"S":"a"},"n":{"N":"42"},"flag":{"BOOL":true},"tags":{"SS":["x","y"]},"nested":{"M":{"k":{"L":[{"S":"v"},{"N":"1"}]}}}}'
  assert_jq_true '. == {}'
  aws_ok dynamodb get-item --table-name "$TABLE" --key '{"LockID":{"S":"a"}}' --consistent-read
  assert_jq_true '.Item == {"LockID":{"S":"a"},"n":{"N":"42"},"flag":{"BOOL":true},"tags":{"SS":["x","y"]},"nested":{"M":{"k":{"L":[{"S":"v"},{"N":"1"}]}}}}'
  aws_ok dynamodb delete-item --table-name "$TABLE" --key '{"LockID":{"S":"a"}}'
  aws_ok dynamodb get-item --table-name "$TABLE" --key '{"LockID":{"S":"a"}}' --consistent-read
  assert_jq 'has("Item")' 'false'
}

test_put_item_return_values_all_old() {
  new_table
  aws_ok dynamodb put-item --table-name "$TABLE" --item '{"LockID":{"S":"a"},"v":{"N":"1"}}'
  aws_ok dynamodb put-item --table-name "$TABLE" --item '{"LockID":{"S":"a"},"v":{"N":"2"}}' --return-values ALL_OLD
  assert_jq_true '.Attributes == {"LockID":{"S":"a"},"v":{"N":"1"}}'
}

test_get_item_projection() {
  new_table
  aws_ok dynamodb put-item --table-name "$TABLE" --item '{"LockID":{"S":"a"},"keep":{"S":"yes"},"drop":{"S":"no"}}'
  aws_ok dynamodb get-item --table-name "$TABLE" --key '{"LockID":{"S":"a"}}' --projection-expression 'keep' --consistent-read
  assert_jq_true '.Item == {"keep":{"S":"yes"}}'
}

test_get_item_without_key_is_validation_error() {
  new_table
  aws_error ValidationException 400 dynamodb get-item --table-name "$TABLE" --key '{}'
}

test_put_item_wrong_key_type_is_validation_error() {
  new_table
  aws_error ValidationException 400 dynamodb put-item --table-name "$TABLE" --item '{"LockID":{"N":"1"}}'
  aws_error_message_contains 'One or more parameter values were invalid: Type mismatch for key'
}

test_put_item_on_missing_table() {
  require_service dynamodb
  aws_error ResourceNotFoundException 400 dynamodb put-item --table-name "$(new_name ddb-missing)" --item '{"LockID":{"S":"a"}}'
}

test_update_item_expressions() {
  new_table
  aws_ok dynamodb update-item --table-name "$TABLE" --key '{"LockID":{"S":"counter"}}' \
    --update-expression 'ADD hits :one SET label = if_not_exists(label, :l)' \
    --expression-attribute-values '{":one":{"N":"1"},":l":{"S":"first"}}' --return-values ALL_NEW
  assert_jq_true '.Attributes == {"LockID":{"S":"counter"},"hits":{"N":"1"},"label":{"S":"first"}}'
  aws_ok dynamodb update-item --table-name "$TABLE" --key '{"LockID":{"S":"counter"}}' \
    --update-expression 'ADD hits :one SET label = if_not_exists(label, :l)' \
    --expression-attribute-values '{":one":{"N":"1"},":l":{"S":"second"}}' --return-values UPDATED_NEW
  assert_jq_true '.Attributes == {"hits":{"N":"2"},"label":{"S":"first"}}'
}

test_update_item_remove_attribute() {
  new_table
  aws_ok dynamodb put-item --table-name "$TABLE" --item '{"LockID":{"S":"a"},"gone":{"S":"x"},"stay":{"S":"y"}}'
  aws_ok dynamodb update-item --table-name "$TABLE" --key '{"LockID":{"S":"a"}}' --update-expression 'REMOVE gone' --return-values ALL_NEW
  assert_jq_true '.Attributes == {"LockID":{"S":"a"},"stay":{"S":"y"}}'
}

# Conditional writes: the lock-table workload

LOCK_CONDITION='attribute_not_exists(LockID) OR ExpiresAt < :now'

# acquire KEY OWNER NOW EXPIRES: put a lock item under LOCK_CONDITION.
acquire() {
  aws_run dynamodb put-item --table-name "$TABLE" \
    --item "{\"LockID\":{\"S\":\"$1\"},\"Owner\":{\"S\":\"$2\"},\"ExpiresAt\":{\"N\":\"$4\"}}" \
    --condition-expression "$LOCK_CONDITION" --expression-attribute-values "{\":now\":{\"N\":\"$3\"}}"
}

test_lock_acquire_on_free_key() {
  new_table
  acquire job-1 worker-a 1000 2000
  [ "$AWS_RC" -eq 0 ] || fail "acquiring a free lock must succeed"
}

test_lock_held_and_unexpired_is_rejected() {
  new_table
  acquire job-1 worker-a 1000 2000
  aws_error ConditionalCheckFailedException 400 dynamodb put-item --table-name "$TABLE" \
    --item '{"LockID":{"S":"job-1"},"Owner":{"S":"worker-b"},"ExpiresAt":{"N":"3000"}}' \
    --condition-expression "$LOCK_CONDITION" --expression-attribute-values '{":now":{"N":"1500"}}'
  aws_error_message_contains 'The conditional request failed'
  aws_ok dynamodb get-item --table-name "$TABLE" --key '{"LockID":{"S":"job-1"}}' --consistent-read
  assert_jq '.Item.Owner.S' 'worker-a'
}

test_lock_expired_can_be_taken_over() {
  new_table
  acquire job-1 worker-a 1000 2000
  acquire job-1 worker-b 2500 3500
  [ "$AWS_RC" -eq 0 ] || fail "taking over an expired lock must succeed"
  aws_ok dynamodb get-item --table-name "$TABLE" --key '{"LockID":{"S":"job-1"}}' --consistent-read
  assert_jq '.Item.Owner.S' 'worker-b'
}

test_conditional_check_failure_returns_old_item() {
  new_table
  acquire job-1 worker-a 1000 2000
  aws_error ConditionalCheckFailedException 400 dynamodb put-item --table-name "$TABLE" \
    --item '{"LockID":{"S":"job-1"},"Owner":{"S":"worker-b"},"ExpiresAt":{"N":"3000"}}' \
    --condition-expression "$LOCK_CONDITION" --expression-attribute-values '{":now":{"N":"1500"}}' \
    --return-values-on-condition-check-failure ALL_OLD
  assert_contains '"Owner"' "$(grep -F 'Response body:' -A3 "$AWS_ERR" | tr '\n' ' ')" 'error body carries the existing item'
}

test_delete_item_conditional() {
  new_table
  acquire job-1 worker-a 1000 2000
  aws_error ConditionalCheckFailedException 400 dynamodb delete-item --table-name "$TABLE" \
    --key '{"LockID":{"S":"job-1"}}' --condition-expression '#o = :o' \
    --expression-attribute-names '{"#o":"Owner"}' --expression-attribute-values '{":o":{"S":"worker-b"}}'
  aws_ok dynamodb delete-item --table-name "$TABLE" --key '{"LockID":{"S":"job-1"}}' \
    --condition-expression '#o = :o' --expression-attribute-names '{"#o":"Owner"}' \
    --expression-attribute-values '{":o":{"S":"worker-a"}}'
}

test_update_item_condition_expression() {
  new_table
  aws_ok dynamodb put-item --table-name "$TABLE" --item '{"LockID":{"S":"a"},"rev":{"N":"1"}}'
  aws_error ConditionalCheckFailedException 400 dynamodb update-item --table-name "$TABLE" --key '{"LockID":{"S":"a"}}' \
    --update-expression 'SET rev = :next' --condition-expression 'rev = :expected' \
    --expression-attribute-values '{":next":{"N":"3"},":expected":{"N":"2"}}'
  aws_ok dynamodb update-item --table-name "$TABLE" --key '{"LockID":{"S":"a"}}' \
    --update-expression 'SET rev = :next' --condition-expression 'rev = :expected' \
    --expression-attribute-values '{":next":{"N":"2"},":expected":{"N":"1"}}'
}

# Query, Scan, batch and transactions

# new_events_table: a table with a composite key (pk HASH, sk RANGE) and ten
# items pk=p, sk=s00..s09 plus one item under pk=q.
new_events_table() {
  require_service dynamodb
  TABLE=$(new_name ddb-comp)
  aws_ok dynamodb create-table --table-name "$TABLE" \
    --attribute-definitions AttributeName=pk,AttributeType=S AttributeName=sk,AttributeType=S \
    --key-schema AttributeName=pk,KeyType=HASH AttributeName=sk,KeyType=RANGE --billing-mode PAY_PER_REQUEST
  wait_table_active "$TABLE"
  local i
  for i in 0 1 2 3 4 5 6 7 8 9; do
    aws_ok dynamodb put-item --table-name "$TABLE" --item "{\"pk\":{\"S\":\"p\"},\"sk\":{\"S\":\"s0$i\"},\"n\":{\"N\":\"$i\"}}"
  done
  aws_ok dynamodb put-item --table-name "$TABLE" --item '{"pk":{"S":"q"},"sk":{"S":"s00"},"n":{"N":"100"}}'
}

test_query_key_condition() {
  new_events_table
  aws_ok dynamodb query --table-name "$TABLE" --key-condition-expression 'pk = :p AND sk BETWEEN :a AND :b' \
    --expression-attribute-values '{":p":{"S":"p"},":a":{"S":"s02"},":b":{"S":"s04"}}'
  assert_jq '.Count' '3'
  assert_jq_true '[.Items[].sk.S] == ["s02","s03","s04"]'
}

test_query_descending_with_filter_and_limit() {
  new_events_table
  aws_ok dynamodb query --table-name "$TABLE" --key-condition-expression 'pk = :p' --filter-expression 'n >= :min' \
    --expression-attribute-values '{":p":{"S":"p"},":min":{"N":"5"}}' --no-scan-index-forward --limit 3
  assert_jq '.ScannedCount' '3'
  assert_jq_true '[.Items[].n.N] == ["9","8","7"]'
  assert_jq_true '.LastEvaluatedKey == {"pk":{"S":"p"},"sk":{"S":"s07"}}'
}

test_query_pagination_with_exclusive_start_key() {
  new_events_table
  aws_ok dynamodb query --table-name "$TABLE" --key-condition-expression 'pk = :p' \
    --expression-attribute-values '{":p":{"S":"p"}}' --limit 4 --no-paginate
  assert_jq '.Count' '4'
  local key
  key=$(jqv '.LastEvaluatedKey' | jq -c .)
  aws_ok dynamodb query --table-name "$TABLE" --key-condition-expression 'pk = :p' \
    --expression-attribute-values '{":p":{"S":"p"}}' --exclusive-start-key "$key" --no-paginate
  assert_jq '.Count' '6'
  assert_jq_true '[.Items[].sk.S] == ["s04","s05","s06","s07","s08","s09"]'
  assert_jq 'has("LastEvaluatedKey")' 'false'
}

test_scan_with_filter() {
  new_events_table
  aws_ok dynamodb scan --table-name "$TABLE" --filter-expression 'n > :min' --expression-attribute-values '{":min":{"N":"7"}}'
  assert_jq '.Count' '3'
  assert_jq '.ScannedCount' '11'
}

test_query_global_secondary_index() {
  require_service dynamodb
  TABLE=$(new_name ddb-gsi)
  aws_ok dynamodb create-table --table-name "$TABLE" \
    --attribute-definitions AttributeName=id,AttributeType=S AttributeName=owner,AttributeType=S \
    --key-schema AttributeName=id,KeyType=HASH --billing-mode PAY_PER_REQUEST \
    --global-secondary-indexes 'IndexName=by-owner,KeySchema=[{AttributeName=owner,KeyType=HASH}],Projection={ProjectionType=ALL}'
  wait_table_active "$TABLE"
  aws_ok dynamodb put-item --table-name "$TABLE" --item '{"id":{"S":"1"},"owner":{"S":"ann"}}'
  aws_ok dynamodb put-item --table-name "$TABLE" --item '{"id":{"S":"2"},"owner":{"S":"bob"}}'
  aws_ok dynamodb query --table-name "$TABLE" --index-name by-owner --key-condition-expression '#o = :o' \
    --expression-attribute-names '{"#o":"owner"}' --expression-attribute-values '{":o":{"S":"ann"}}'
  assert_jq_true '[.Items[].id.S] == ["1"]'
  load_account
  aws_ok dynamodb describe-table --table-name "$TABLE"
  assert_jq '.Table.GlobalSecondaryIndexes[0].IndexName' 'by-owner'
  assert_jq '.Table.GlobalSecondaryIndexes[0].IndexArn' "arn:aws:dynamodb:${AWS_REGION}:${ACCT}:table/${TABLE}/index/by-owner"
}

test_batch_write_and_batch_get() {
  new_table
  aws_ok dynamodb batch-write-item --request-items "{\"$TABLE\":[{\"PutRequest\":{\"Item\":{\"LockID\":{\"S\":\"a\"}}}},{\"PutRequest\":{\"Item\":{\"LockID\":{\"S\":\"b\"}}}},{\"PutRequest\":{\"Item\":{\"LockID\":{\"S\":\"c\"}}}}]}"
  assert_jq_true '.UnprocessedItems == {}'
  aws_ok dynamodb batch-get-item --request-items "{\"$TABLE\":{\"Keys\":[{\"LockID\":{\"S\":\"a\"}},{\"LockID\":{\"S\":\"c\"}},{\"LockID\":{\"S\":\"zzz\"}}],\"ConsistentRead\":true}}"
  assert_jq_true "[.Responses[\"$TABLE\"][].LockID.S] | sort == [\"a\",\"c\"]"
  aws_ok dynamodb batch-write-item --request-items "{\"$TABLE\":[{\"DeleteRequest\":{\"Key\":{\"LockID\":{\"S\":\"a\"}}}}]}"
  aws_ok dynamodb scan --table-name "$TABLE" --consistent-read
  assert_jq '.Count' '2'
}

test_transact_write_items_commits_all() {
  new_table
  aws_ok dynamodb transact-write-items --transact-items "[{\"Put\":{\"TableName\":\"$TABLE\",\"Item\":{\"LockID\":{\"S\":\"a\"}}}},{\"Put\":{\"TableName\":\"$TABLE\",\"Item\":{\"LockID\":{\"S\":\"b\"}}}}]"
  aws_ok dynamodb scan --table-name "$TABLE" --consistent-read
  assert_jq '.Count' '2'
}

test_transact_write_items_cancelled_writes_nothing() {
  new_table
  aws_ok dynamodb put-item --table-name "$TABLE" --item '{"LockID":{"S":"exists"}}'
  aws_error TransactionCanceledException 400 dynamodb transact-write-items --transact-items "[{\"Put\":{\"TableName\":\"$TABLE\",\"Item\":{\"LockID\":{\"S\":\"new\"}}}},{\"Put\":{\"TableName\":\"$TABLE\",\"Item\":{\"LockID\":{\"S\":\"exists\"}},\"ConditionExpression\":\"attribute_not_exists(LockID)\"}}]"
  aws_error_message_contains 'Transaction cancelled, please refer cancellation reasons for specific reasons [None, ConditionalCheckFailed]'
  aws_ok dynamodb get-item --table-name "$TABLE" --key '{"LockID":{"S":"new"}}' --consistent-read
  assert_jq 'has("Item")' 'false'
}

# TTL

test_ttl_disabled_by_default() {
  new_table
  aws_ok dynamodb describe-time-to-live --table-name "$TABLE"
  assert_jq '.TimeToLiveDescription.TimeToLiveStatus' 'DISABLED'
  assert_jq '.TimeToLiveDescription | has("AttributeName")' 'false'
}

test_ttl_enable_and_describe() {
  new_table
  aws_ok dynamodb update-time-to-live --table-name "$TABLE" --time-to-live-specification Enabled=true,AttributeName=ExpiresAt
  assert_jq '.TimeToLiveSpecification.Enabled' 'true'
  assert_jq '.TimeToLiveSpecification.AttributeName' 'ExpiresAt'
  aws_ok dynamodb describe-time-to-live --table-name "$TABLE"
  assert_jq_match '.TimeToLiveDescription.TimeToLiveStatus' '^(ENABLING|ENABLED)$'
  assert_jq '.TimeToLiveDescription.AttributeName' 'ExpiresAt'
}

test_ttl_disable() {
  new_table
  aws_ok dynamodb update-time-to-live --table-name "$TABLE" --time-to-live-specification Enabled=true,AttributeName=ExpiresAt
  aws_ok dynamodb update-time-to-live --table-name "$TABLE" --time-to-live-specification Enabled=false,AttributeName=ExpiresAt
  assert_jq '.TimeToLiveSpecification.Enabled' 'false'
  aws_ok dynamodb describe-time-to-live --table-name "$TABLE"
  assert_jq_match '.TimeToLiveDescription.TimeToLiveStatus' '^(DISABLING|DISABLED)$'
}

test_ttl_on_missing_table() {
  require_service dynamodb
  aws_error ResourceNotFoundException 400 dynamodb describe-time-to-live --table-name "$(new_name ddb-missing)"
}

test_ttl_keeps_unexpired_items() {
  new_table
  aws_ok dynamodb update-time-to-live --table-name "$TABLE" --time-to-live-specification Enabled=true,AttributeName=ExpiresAt
  local future=$(($(date +%s) + 86400))
  aws_ok dynamodb put-item --table-name "$TABLE" --item "{\"LockID\":{\"S\":\"future\"},\"ExpiresAt\":{\"N\":\"$future\"}}"
  sleep 15
  aws_ok dynamodb get-item --table-name "$TABLE" --key '{"LockID":{"S":"future"}}' --consistent-read
  assert_jq '.Item.LockID.S' 'future'
}

# Tags

test_create_table_with_tags() {
  new_table --tags Key=env,Value=test Key=team,Value=core
  aws_ok dynamodb list-tags-of-resource --resource-arn "arn:aws:dynamodb:${AWS_REGION}:${ACCT}:table/${TABLE}"
  assert_jq_true '[.Tags[] | {(.Key): .Value}] | add == {"env":"test","team":"core"}'
}

test_tag_untag_resource() {
  new_table
  local arn="arn:aws:dynamodb:${AWS_REGION}:${ACCT}:table/${TABLE}"
  aws_ok dynamodb tag-resource --resource-arn "$arn" --tags Key=a,Value=1 Key=b,Value=2
  aws_ok dynamodb untag-resource --resource-arn "$arn" --tag-keys a
  aws_ok dynamodb list-tags-of-resource --resource-arn "$arn"
  assert_jq_true '[.Tags[] | {(.Key): .Value}] | add == {"b":"2"}'
}

test_list_tags_without_tags_is_empty() {
  new_table
  aws_ok dynamodb list-tags-of-resource --resource-arn "arn:aws:dynamodb:${AWS_REGION}:${ACCT}:table/${TABLE}"
  assert_jq_true '.Tags == []'
}

# Continuous backups

test_describe_continuous_backups_defaults() {
  new_table
  aws_ok dynamodb describe-continuous-backups --table-name "$TABLE"
  assert_jq '.ContinuousBackupsDescription.ContinuousBackupsStatus' 'ENABLED'
  assert_jq '.ContinuousBackupsDescription.PointInTimeRecoveryDescription.PointInTimeRecoveryStatus' 'DISABLED'
}

test_update_continuous_backups_enables_pitr() {
  new_table
  aws_ok dynamodb update-continuous-backups --table-name "$TABLE" --point-in-time-recovery-specification PointInTimeRecoveryEnabled=true
  assert_jq '.ContinuousBackupsDescription.PointInTimeRecoveryDescription.PointInTimeRecoveryStatus' 'ENABLED'
  aws_ok dynamodb describe-continuous-backups --table-name "$TABLE"
  assert_jq '.ContinuousBackupsDescription.PointInTimeRecoveryDescription.PointInTimeRecoveryStatus' 'ENABLED'
  assert_jq_match '.ContinuousBackupsDescription.PointInTimeRecoveryDescription.EarliestRestorableDateTime' '^[0-9]{4}-'
  assert_jq_match '.ContinuousBackupsDescription.PointInTimeRecoveryDescription.LatestRestorableDateTime' '^[0-9]{4}-'
}

test_describe_continuous_backups_missing_table() {
  require_service dynamodb
  aws_error TableNotFoundException 400 dynamodb describe-continuous-backups --table-name "$(new_name ddb-missing)"
}

# Account and region isolation

test_tables_are_isolated_per_region() {
  new_table
  aws_ok --region eu-west-1 dynamodb list-tables
  assert_jq_true ".TableNames | index(\"$TABLE\") == null"
  aws_error ResourceNotFoundException 400 --region eu-west-1 dynamodb describe-table --table-name "$TABLE"
  aws_ok --region eu-west-1 dynamodb create-table --table-name "$TABLE" \
    --attribute-definitions AttributeName=LockID,AttributeType=S --key-schema AttributeName=LockID,KeyType=HASH \
    --billing-mode PAY_PER_REQUEST
  assert_jq '.TableDescription.TableArn' "arn:aws:dynamodb:eu-west-1:${ACCT}:table/${TABLE}"
}

test_tables_are_isolated_per_account() {
  new_table
  AWS_ACCESS_KEY_ID=222222222222 aws_ok dynamodb list-tables
  assert_jq_true ".TableNames | index(\"$TABLE\") == null"
  AWS_ACCESS_KEY_ID=222222222222 aws_ok dynamodb create-table --table-name "$TABLE" \
    --attribute-definitions AttributeName=LockID,AttributeType=S --key-schema AttributeName=LockID,KeyType=HASH \
    --billing-mode PAY_PER_REQUEST
  assert_jq '.TableDescription.TableArn' "arn:aws:dynamodb:${AWS_REGION}:222222222222:table/${TABLE}"
}

test_items_are_isolated_per_region() {
  new_table
  aws_ok dynamodb put-item --table-name "$TABLE" --item '{"LockID":{"S":"only-here"}}'
  aws_ok --region eu-west-1 dynamodb create-table --table-name "$TABLE" \
    --attribute-definitions AttributeName=LockID,AttributeType=S --key-schema AttributeName=LockID,KeyType=HASH \
    --billing-mode PAY_PER_REQUEST
  aws_ok --region eu-west-1 dynamodb scan --table-name "$TABLE" --consistent-read
  assert_jq '.Count' '0'
}
