#!/usr/bin/env bash
# DynamoDB Streams: the stream a table's StreamSpecification creates, listed,
# described and read through `aws dynamodbstreams`, which signs for dynamodb.
# Arbiter: docs/research/dynamodb-streams.md (sections 3 to 7 and 11).
#
# DynamoDB runs on DynamoDB Local, pulled on first use; every test skips
# under CONFORMANCE_DYNAMODB=0, and while the emulator reports the service as
# not enabled.

# new_stream_table [VIEW]: create a PAY_PER_REQUEST table keyed by the string
# id with a stream of VIEW (default NEW_AND_OLD_IMAGES), wait for it to be
# ACTIVE, and set TABLE, STREAM_ARN, STREAM_LABEL and ACCT.
new_stream_table() {
  require_service dynamodb
  TABLE=$(new_name ddbs)
  aws_ok dynamodb create-table --table-name "$TABLE" \
    --attribute-definitions AttributeName=id,AttributeType=S \
    --key-schema AttributeName=id,KeyType=HASH --billing-mode PAY_PER_REQUEST \
    --stream-specification "StreamEnabled=true,StreamViewType=${1:-NEW_AND_OLD_IMAGES}"
  STREAM_ARN=$(jqv '.TableDescription.LatestStreamArn')
  STREAM_LABEL=$(jqv '.TableDescription.LatestStreamLabel')
  local deadline=$((SECONDS + 60))
  while :; do
    aws_run dynamodb describe-table --table-name "$TABLE"
    if [ "$AWS_RC" -eq 0 ] && [ "$(jqv '.Table.TableStatus')" = ACTIVE ]; then
      break
    fi
    [ "$SECONDS" -lt "$deadline" ] || fail "table $TABLE never became ACTIVE"
    sleep 1
  done
  aws_ok sts get-caller-identity
  ACCT=$(jqv '.Account')
}

# first_shard: set SHARD and START from DescribeStream of STREAM_ARN.
first_shard() {
  aws_ok dynamodbstreams describe-stream --stream-arn "$STREAM_ARN"
  SHARD=$(jqv '.StreamDescription.Shards[0].ShardId')
  START=$(jqv '.StreamDescription.Shards[0].SequenceNumberRange.StartingSequenceNumber')
}

test_create_table_answers_the_stream() {
  new_stream_table
  # dynamodb-streams.md 4.1: the documented ARN and label forms.
  assert_match '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}$' "$STREAM_LABEL" 'LatestStreamLabel'
  assert_eq "arn:aws:dynamodb:${AWS_REGION}:${ACCT}:table/${TABLE}/stream/${STREAM_LABEL}" "$STREAM_ARN" 'LatestStreamArn'
  # dynamodb-streams.md 9.3: identical on every read.
  aws_ok dynamodb describe-table --table-name "$TABLE"
  assert_jq '.Table.LatestStreamArn' "$STREAM_ARN"
  assert_jq '.Table.LatestStreamLabel' "$STREAM_LABEL"
}

test_list_streams_lists_the_table_stream() {
  new_stream_table
  aws_ok dynamodbstreams list-streams --table-name "$TABLE"
  assert_jq_true ".Streams == [{\"StreamArn\":\"${STREAM_ARN}\",\"TableName\":\"${TABLE}\",\"StreamLabel\":\"${STREAM_LABEL}\"}]"
}

test_describe_stream_one_open_shard() {
  # DDB-SNAP:L708-L735 (third-party-recorded): one open shard, ENABLED.
  new_stream_table
  aws_ok dynamodbstreams describe-stream --stream-arn "$STREAM_ARN"
  assert_jq '.StreamDescription.StreamStatus' 'ENABLED'
  assert_jq '.StreamDescription.StreamViewType' 'NEW_AND_OLD_IMAGES'
  assert_jq '.StreamDescription.TableName' "$TABLE"
  assert_jq '.StreamDescription.Shards | length' '1'
  assert_jq_true '.StreamDescription.Shards[0].SequenceNumberRange | has("EndingSequenceNumber") | not'
}

test_describe_stream_missing() {
  # STREAMS-SNAP:L90-L104 (third-party-recorded).
  new_stream_table
  local missing=${STREAM_ARN/\/stream\//x\/stream\/}
  aws_error ResourceNotFoundException 400 dynamodbstreams describe-stream --stream-arn "$missing"
  aws_error_message_contains "Requested resource not found: Stream: ${missing} not found"
}

test_writes_reach_the_stream() {
  new_stream_table
  first_shard
  aws_ok dynamodb put-item --table-name "$TABLE" --item '{"id":{"S":"a"},"v":{"S":"1"}}'
  aws_ok dynamodb update-item --table-name "$TABLE" --key '{"id":{"S":"a"}}' \
    --update-expression 'SET v = :v' --expression-attribute-values '{":v":{"S":"2"}}'
  aws_ok dynamodb delete-item --table-name "$TABLE" --key '{"id":{"S":"a"}}'
  aws_ok dynamodbstreams get-shard-iterator --stream-arn "$STREAM_ARN" --shard-id "$SHARD" --shard-iterator-type TRIM_HORIZON
  local it
  it=$(jqv '.ShardIterator')
  # dynamodb-streams.md 4.4: "<stream ARN>|1|<token>".
  assert_eq "${STREAM_ARN}|1|" "${it:0:$((${#STREAM_ARN} + 3))}" 'shard iterator prefix'
  aws_ok dynamodbstreams get-records --shard-iterator "$it"
  # dynamodb-streams.md 7.1, 7.2 (NEW_AND_OLD_IMAGES row); eventVersion 1.1
  # (DDB-SNAP:L761).
  assert_jq_true '[.Records[].eventName] == ["INSERT","MODIFY","REMOVE"]'
  assert_jq_true "[.Records[] | .eventSource == \"aws:dynamodb\" and .eventVersion == \"1.1\" and .awsRegion == \"${AWS_REGION}\" and (has(\"userIdentity\") | not)] | all"
  assert_jq_true '[.Records[].dynamodb | has("NewImage")] == [true,true,false]'
  assert_jq_true '[.Records[].dynamodb | has("OldImage")] == [false,true,true]'
  # The first record is above StartingSequenceNumber (DDB-TEST:L1993-L1994);
  # numbers increase. jq compares the digit strings by length, then text.
  assert_jq_true "[\"${START}\", .Records[].dynamodb.SequenceNumber] | [range(1; length) as \$i | (.[\$i] | length) as \$l | (.[\$i - 1] | length) as \$p | (\$l > \$p) or (\$l == \$p and .[\$i] > .[\$i - 1])] | all"
}

test_disable_closes_the_shard() {
  new_stream_table
  first_shard
  aws_ok dynamodb put-item --table-name "$TABLE" --item '{"id":{"S":"a"}}'
  aws_ok dynamodb update-table --table-name "$TABLE" --stream-specification StreamEnabled=false
  # dynamodb-streams.md 5.3: a disable closes the shard; LatestStreamArn stays (9.3).
  local deadline=$((SECONDS + 60))
  while :; do
    aws_ok dynamodbstreams describe-stream --stream-arn "$STREAM_ARN"
    [ "$(jqv '.StreamDescription.StreamStatus')" = DISABLED ] && break
    [ "$SECONDS" -lt "$deadline" ] || fail "stream $STREAM_ARN never became DISABLED"
    sleep 1
  done
  assert_jq_true '.StreamDescription.Shards[0].SequenceNumberRange | has("EndingSequenceNumber")'
  aws_ok dynamodb describe-table --table-name "$TABLE"
  assert_jq '.Table.LatestStreamArn' "$STREAM_ARN"
  assert_jq_true '.Table | has("StreamSpecification") | not'
  aws_ok dynamodbstreams get-shard-iterator --stream-arn "$STREAM_ARN" --shard-id "$SHARD" --shard-iterator-type TRIM_HORIZON
  aws_ok dynamodbstreams get-records --shard-iterator "$(jqv '.ShardIterator')"
  assert_jq '.Records | length' '1'
  assert_jq_true 'has("NextShardIterator") | not'
}
