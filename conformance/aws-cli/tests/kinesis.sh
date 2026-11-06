#!/usr/bin/env bash
# Kinesis Data Streams: streams, shards, tags and records through `aws kinesis`.
# Arbiter: docs/research/kinesis.md (sections 3 to 6, 10 and 13).

# new_stream [SHARDS]: create a PROVISIONED stream of SHARDS shards (default
# 1), and set STREAM, STREAM_ARN and ACCT.
new_stream() {
  STREAM=$(new_name kin)
  aws_ok kinesis create-stream --stream-name "$STREAM" --shard-count "${1:-1}"
  aws_ok sts get-caller-identity
  ACCT=$(jqv '.Account')
  STREAM_ARN="arn:aws:kinesis:${AWS_REGION}:${ACCT}:stream/${STREAM}"
  wait_stream_active
}

# wait_stream_active: poll DescribeStreamSummary until STREAM is ACTIVE
# (kinesis.md 4.2: CreateStream returns while the stream is CREATING).
wait_stream_active() {
  local deadline=$((SECONDS + 60))
  while :; do
    aws_run kinesis describe-stream-summary --stream-name "$STREAM"
    if [ "$AWS_RC" -eq 0 ] && [ "$(jqv '.StreamDescriptionSummary.StreamStatus')" = ACTIVE ]; then
      break
    fi
    [ "$SECONDS" -lt "$deadline" ] || fail "stream $STREAM never became ACTIVE"
    sleep 1
  done
}

test_describe_stream_summary() {
  new_stream 2
  aws_ok kinesis describe-stream-summary --stream-name "$STREAM"
  # kinesis.md 3.1: the ARN form; 4.7: 24 hours by default; 4.4: the
  # required members; 10.3: Terraform's basic acceptance test reads
  # PROVISIONED, two open shards and no metrics for this configuration.
  assert_jq '.StreamDescriptionSummary.StreamName' "$STREAM"
  assert_jq '.StreamDescriptionSummary.StreamARN' "$STREAM_ARN"
  assert_jq '.StreamDescriptionSummary.RetentionPeriodHours' '24'
  assert_jq '.StreamDescriptionSummary.OpenShardCount' '2'
  assert_jq '.StreamDescriptionSummary.StreamModeDetails.StreamMode' 'PROVISIONED'
  assert_jq_true '[.StreamDescriptionSummary.EnhancedMonitoring[].ShardLevelMetrics[]] == []'
  assert_jq_true '.StreamDescriptionSummary | has("StreamCreationTimestamp")'
}

test_create_without_shard_count_has_four_shards() {
  # kinesis.md 4.1 and finding 4 (aws-recorded, LS test_kinesis.snapshot.json:L2-L47).
  STREAM=$(new_name kin-od)
  aws_ok kinesis create-stream --stream-name "$STREAM"
  wait_stream_active
  assert_jq '.StreamDescriptionSummary.OpenShardCount' '4'
  aws_ok kinesis list-shards --stream-name "$STREAM"
  assert_jq '.Shards | length' '4'
  assert_jq_true '[.Shards[].ShardId] == ["shardId-000000000000","shardId-000000000001","shardId-000000000002","shardId-000000000003"]'
}

test_three_shards_split_the_hash_space_evenly() {
  # kinesis.md 3.3: the documented 3-shard stream (DescribeStream example);
  # 3.2: ids numbered from 0; 3.4: an open shard has no EndingSequenceNumber.
  new_stream 3
  aws_ok kinesis list-shards --stream-name "$STREAM"
  assert_jq_true '[.Shards[] | [.ShardId, .HashKeyRange.StartingHashKey, .HashKeyRange.EndingHashKey]] == [
    ["shardId-000000000000","0","113427455640312821154458202477256070484"],
    ["shardId-000000000001","113427455640312821154458202477256070485","226854911280625642308916404954512140969"],
    ["shardId-000000000002","226854911280625642308916404954512140970","340282366920938463463374607431768211455"]]'
  assert_jq_true '[.Shards[].SequenceNumberRange | has("StartingSequenceNumber") and (has("EndingSequenceNumber") | not)] | all'
}

test_describe_missing_stream() {
  # kinesis.md finding 5 (aws-recorded over CBOR, LS test_kinesis.snapshot.json:L230-L237):
  # the name and account, even when addressed by ARN.
  aws_ok sts get-caller-identity
  ACCT=$(jqv '.Account')
  local missing
  missing=$(new_name kin-missing)
  aws_error ResourceNotFoundException 400 kinesis describe-stream --stream-arn "arn:aws:kinesis:${AWS_REGION}:${ACCT}:stream/${missing}"
  aws_error_message_contains "Stream ${missing} under account ${ACCT} not found."
}

test_create_duplicate_in_use() {
  # kinesis.md 4.1: a duplicate name is ResourceInUseException (inferred from
  # the model's error list; the text is kinesalite's, KIN-05).
  new_stream
  aws_error ResourceInUseException 400 kinesis create-stream --stream-name "$STREAM" --shard-count 1
}

test_tags_for_stream() {
  # kinesis.md 4.10 (aws-recorded, LS test_kinesis.snapshot.json:L49-L57).
  new_stream
  aws_ok kinesis add-tags-to-stream --stream-name "$STREAM" --tags foo=bar
  aws_ok kinesis list-tags-for-stream --stream-name "$STREAM"
  assert_jq_true '.Tags == [{"Key":"foo","Value":"bar"}]'
  assert_jq '.HasMoreTags' 'false'
  # kinesis.md 4.10: the resource-scoped API reads the same tags.
  aws_ok kinesis list-tags-for-resource --resource-arn "$STREAM_ARN"
  assert_jq_true '.Tags == [{"Key":"foo","Value":"bar"}]'
  aws_ok kinesis remove-tags-from-stream --stream-name "$STREAM" --tag-keys foo
  aws_ok kinesis list-tags-for-stream --stream-name "$STREAM"
  assert_jq_true '.Tags == []'
}

test_retention_equal_value_accepted() {
  # kinesis.md finding 3: Terraform increases to the current value on every
  # create, so AWS accepts it (inferred; KIN-06). 4.7: an increase reads back.
  new_stream
  aws_ok kinesis increase-stream-retention-period --stream-name "$STREAM" --retention-period-hours 24
  aws_ok kinesis increase-stream-retention-period --stream-name "$STREAM" --retention-period-hours 48
  aws_ok kinesis describe-stream-summary --stream-name "$STREAM"
  assert_jq '.StreamDescriptionSummary.RetentionPeriodHours' '48'
  aws_ok kinesis decrease-stream-retention-period --stream-name "$STREAM" --retention-period-hours 24
  aws_ok kinesis describe-stream-summary --stream-name "$STREAM"
  assert_jq '.StreamDescriptionSummary.RetentionPeriodHours' '24'
}

test_empty_shard_has_no_records() {
  # kinesis.md 5.5 (aws-recorded, LS test_kinesis.py:L552-L575): Records []
  # and, on an open shard, a NextShardIterator every time.
  new_stream
  aws_ok kinesis get-shard-iterator --stream-name "$STREAM" --shard-id shardId-000000000000 --shard-iterator-type TRIM_HORIZON
  aws_ok kinesis get-records --shard-iterator "$(jqv '.ShardIterator')"
  assert_jq_true '.Records == []'
  assert_jq_true 'has("NextShardIterator")'
}

test_put_and_get_records() {
  new_stream
  aws_ok kinesis put-record --stream-name "$STREAM" --partition-key k1 --data aGVsbG8= --cli-binary-format base64
  # kinesis.md 5.1: ShardId and SequenceNumber; 3.4: a decimal string.
  assert_jq '.ShardId' 'shardId-000000000000'
  assert_jq_match '.SequenceNumber' '^[1-9][0-9]*$'
  local first
  first=$(jqv '.SequenceNumber')
  # kinesis.md 5.1: empty data is legal (aws-recorded, LS test_kinesis.snapshot.json:L58-L67).
  aws_ok kinesis put-record --stream-name "$STREAM" --partition-key k2 --data '' --cli-binary-format base64
  # kinesis.md 5.2: one result per entry, in request order.
  aws_ok kinesis put-records --stream-name "$STREAM" --cli-binary-format base64 \
    --records Data=b25l,PartitionKey=k3 Data=dHdv,PartitionKey=k4
  assert_jq '.FailedRecordCount // 0' '0'
  assert_jq '.Records | length' '2'
  assert_jq_true '[.Records[].ShardId] == ["shardId-000000000000","shardId-000000000000"]'
  aws_ok kinesis get-shard-iterator --stream-name "$STREAM" --shard-id shardId-000000000000 --shard-iterator-type TRIM_HORIZON
  aws_ok kinesis get-records --shard-iterator "$(jqv '.ShardIterator')"
  assert_jq_true '[.Records[] | [.PartitionKey, .Data]] == [["k1","aGVsbG8="],["k2",""],["k3","b25l"],["k4","dHdv"]]'
  assert_jq '.Records[0].SequenceNumber' "$first"
  # kinesis.md 4.11 (aws-recorded, LS test_kinesis.snapshot.json:L58-L88):
  # records of an unencrypted stream carry no EncryptionType.
  assert_jq_true '[.Records[] | has("EncryptionType") | not] | all'
  # kinesis.md 3.4: sequence numbers increase within a shard. jq compares the
  # digit strings by length, then text.
  # shellcheck disable=SC2016 # $i, $l and $p are jq variables
  assert_jq_true '[.Records[].SequenceNumber] | [range(1; length) as $i | (.[$i] | length) as $l | (.[$i - 1] | length) as $p | ($l > $p) or ($l == $p and .[$i] > .[$i - 1])] | all'
  # kinesis.md 5.4: AFTER_SEQUENCE_NUMBER starts after the given record.
  aws_ok kinesis get-shard-iterator --stream-name "$STREAM" --shard-id shardId-000000000000 \
    --shard-iterator-type AFTER_SEQUENCE_NUMBER --starting-sequence-number "$first"
  aws_ok kinesis get-records --shard-iterator "$(jqv '.ShardIterator')"
  assert_jq_true '[.Records[].PartitionKey] == ["k2","k3","k4"]'
}

test_delete_stream() {
  # kinesis.md 4.3 and 10.1: once deleted, DescribeStreamSummary answers
  # ResourceNotFoundException.
  new_stream
  aws_ok kinesis delete-stream --stream-name "$STREAM"
  local deadline=$((SECONDS + 60))
  while :; do
    aws_run kinesis describe-stream-summary --stream-name "$STREAM"
    [ "$AWS_RC" -ne 0 ] && break
    [ "$SECONDS" -lt "$deadline" ] || fail "stream $STREAM was never deleted"
    sleep 1
  done
  aws_error ResourceNotFoundException 400 kinesis describe-stream-summary --stream-name "$STREAM"
}

test_resharding_not_implemented() {
  # kinesis.md 13.1 and 13.9: resharding is not emulated, so it answers 501
  # rather than being accepted and ignored.
  new_stream
  aws_error NotImplemented 501 kinesis split-shard --stream-name "$STREAM" --shard-to-split shardId-000000000000 \
    --new-starting-hash-key 170141183460469231731687303715884105728
  aws_error_message_contains 'understudy does not implement kinesis.SplitShard'
}
