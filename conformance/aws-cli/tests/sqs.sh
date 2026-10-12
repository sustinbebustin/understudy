#!/usr/bin/env bash
# SQS: queues, attributes, send/receive/delete, visibility, delay, long polling,
# batches, purge, dead-letter queues, FIFO, tags, policies.
# Arbiter: docs/research/phase1/sqs.md.

# Create a standard queue and set QUEUE_URL.
mkq() { # NAME [create-queue args...]
  local name=$1
  shift
  aws_ok sqs create-queue --queue-name "$name" "$@"
  QUEUE_URL=$(jqv '.QueueUrl')
}

md5_of() { printf '%s' "$1" | md5sum | cut -d' ' -f1; }

# sqs_string_attr_md5 NAME VALUE: MD5OfMessageAttributes for one String attribute
# (sqs.md section 6.3: big-endian 4-byte lengths, transport byte 1 for String).
sqs_string_attr_md5() {
  python3 -I - "$1" "$2" <<'PY'
import hashlib, struct, sys
name, value = sys.argv[1:3]
h = hashlib.md5()
for chunk in (name.encode(), b"String"):
    h.update(struct.pack(">I", len(chunk)) + chunk)
h.update(b"\x01")
v = value.encode()
h.update(struct.pack(">I", len(v)) + v)
print(h.hexdigest())
PY
}

test_create_queue_url_shape() {
  local name
  name=$(new_name q-url)
  mkq "$name"
  assert_match "^https?://[^/]+/[0-9]{12}/${name}\$" "$QUEUE_URL" 'QueueUrl'
}

test_create_queue_is_idempotent_for_same_attributes() {
  local name first
  name=$(new_name q-idem)
  mkq "$name" --attributes VisibilityTimeout=45
  first=$QUEUE_URL
  mkq "$name" --attributes VisibilityTimeout=45
  assert_eq "$first" "$QUEUE_URL" 'second CreateQueue returns the same URL'
}

test_create_queue_with_different_attributes_is_queue_already_exists() {
  local name
  name=$(new_name q-diff)
  mkq "$name" --attributes VisibilityTimeout=45
  aws_error QueueAlreadyExists 400 sqs create-queue --queue-name "$name" --attributes VisibilityTimeout=46
  aws_error_message_contains 'A queue already exists with the same name and a different value for attribute VisibilityTimeout'
}

test_get_queue_url_returns_created_queue() {
  local name
  name=$(new_name q-geturl)
  mkq "$name"
  local url=$QUEUE_URL
  aws_ok sqs get-queue-url --queue-name "$name"
  assert_jq '.QueueUrl' "$url"
}

test_get_queue_url_missing_queue() {
  aws_error AWS.SimpleQueueService.NonExistentQueue 400 sqs get-queue-url --queue-name "$(new_name q-none)"
  aws_error_message_contains 'The specified queue does not exist'
}

test_list_queues_filters_by_prefix() {
  local prefix a b
  prefix=$(new_name q-list)
  a="${prefix}-a"
  b="${prefix}-b"
  mkq "$a"
  mkq "$b"
  aws_ok sqs list-queues --queue-name-prefix "$prefix"
  assert_jq '.QueueUrls | length' '2'
  assert_jq_true "[.QueueUrls[] | endswith(\"/$a\") or endswith(\"/$b\")] | all"
}

test_default_queue_attributes() {
  local name acct
  name=$(new_name q-attrs)
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  mkq "$name"
  aws_ok sqs get-queue-attributes --queue-url "$QUEUE_URL" --attribute-names All
  assert_jq '.Attributes.VisibilityTimeout' '30'
  assert_jq '.Attributes.DelaySeconds' '0'
  assert_jq '.Attributes.ReceiveMessageWaitTimeSeconds' '0'
  assert_jq '.Attributes.MessageRetentionPeriod' '345600'
  assert_jq '.Attributes.MaximumMessageSize' '1048576'
  assert_jq '.Attributes.QueueArn' "arn:aws:sqs:${AWS_REGION}:${acct}:${name}"
  assert_jq_match '.Attributes.CreatedTimestamp' '^[0-9]{10}$'
  assert_jq_match '.Attributes.LastModifiedTimestamp' '^[0-9]{10}$'
  assert_jq '.Attributes.ApproximateNumberOfMessages' '0'
}

test_get_queue_attributes_without_names_is_empty() {
  mkq "$(new_name q-noattrs)"
  aws_ok sqs get-queue-attributes --queue-url "$QUEUE_URL"
  assert_jq '(.Attributes // {}) | length' '0'
}

test_get_queue_attributes_returns_only_requested() {
  mkq "$(new_name q-some)" --attributes DelaySeconds=3
  aws_ok sqs get-queue-attributes --queue-url "$QUEUE_URL" --attribute-names DelaySeconds VisibilityTimeout
  assert_jq_true '.Attributes == {"DelaySeconds":"3","VisibilityTimeout":"30"}'
}

test_get_queue_attributes_unknown_name() {
  mkq "$(new_name q-badattr)"
  aws_error InvalidAttributeName 400 sqs get-queue-attributes --queue-url "$QUEUE_URL" --attribute-names Foobar
}

test_set_queue_attributes_round_trip() {
  mkq "$(new_name q-set)"
  aws_ok sqs set-queue-attributes --queue-url "$QUEUE_URL" --attributes VisibilityTimeout=77,DelaySeconds=2,MessageRetentionPeriod=3600
  aws_ok sqs get-queue-attributes --queue-url "$QUEUE_URL" --attribute-names VisibilityTimeout DelaySeconds MessageRetentionPeriod
  assert_jq_true '.Attributes == {"VisibilityTimeout":"77","DelaySeconds":"2","MessageRetentionPeriod":"3600"}'
}

test_set_queue_attributes_out_of_range_visibility_timeout() {
  mkq "$(new_name q-badvt)"
  aws_error InvalidAttributeValue 400 sqs set-queue-attributes --queue-url "$QUEUE_URL" --attributes VisibilityTimeout=43201
}

test_send_receive_delete_message() {
  mkq "$(new_name q-srd)"
  local body='hello from the conformance suite'
  aws_ok sqs send-message --queue-url "$QUEUE_URL" --message-body "$body"
  assert_jq_match '.MessageId' '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
  assert_jq '.MD5OfMessageBody' "$(md5_of "$body")"
  aws_ok sqs receive-message --queue-url "$QUEUE_URL"
  assert_jq '.Messages | length' '1'
  assert_jq '.Messages[0].Body' "$body"
  assert_jq '.Messages[0].MD5OfBody' "$(md5_of "$body")"
  assert_jq_match '.Messages[0].ReceiptHandle' '^.+$'
  local handle
  handle=$(jqv '.Messages[0].ReceiptHandle')
  aws_ok sqs delete-message --queue-url "$QUEUE_URL" --receipt-handle "$handle"
  aws_ok sqs get-queue-attributes --queue-url "$QUEUE_URL" --attribute-names ApproximateNumberOfMessages ApproximateNumberOfMessagesNotVisible
  assert_jq_true '.Attributes == {"ApproximateNumberOfMessages":"0","ApproximateNumberOfMessagesNotVisible":"0"}'
}

test_receive_message_empty_queue_has_no_messages_key() {
  mkq "$(new_name q-empty)"
  aws_ok sqs receive-message --queue-url "$QUEUE_URL"
  assert_jq '.Messages // [] | length' '0'
}

test_received_message_is_invisible_for_visibility_timeout() {
  mkq "$(new_name q-vis)"
  aws_ok sqs send-message --queue-url "$QUEUE_URL" --message-body one
  aws_ok sqs receive-message --queue-url "$QUEUE_URL" --visibility-timeout 30
  assert_jq '.Messages | length' '1'
  aws_ok sqs receive-message --queue-url "$QUEUE_URL"
  assert_jq '.Messages // [] | length' '0' 'second receive during the visibility timeout'
  aws_ok sqs get-queue-attributes --queue-url "$QUEUE_URL" --attribute-names ApproximateNumberOfMessagesNotVisible
  assert_jq '.Attributes.ApproximateNumberOfMessagesNotVisible' '1'
}

test_message_becomes_visible_after_visibility_timeout() {
  mkq "$(new_name q-revis)"
  aws_ok sqs send-message --queue-url "$QUEUE_URL" --message-body again
  aws_ok sqs receive-message --queue-url "$QUEUE_URL" --visibility-timeout 1
  assert_jq '.Messages | length' '1'
  sleep 2
  aws_ok sqs receive-message --queue-url "$QUEUE_URL" --attribute-names ApproximateReceiveCount
  assert_jq '.Messages | length' '1'
  assert_jq '.Messages[0].Attributes.ApproximateReceiveCount' '2'
}

test_change_message_visibility_to_zero_makes_message_visible() {
  mkq "$(new_name q-chvis)"
  aws_ok sqs send-message --queue-url "$QUEUE_URL" --message-body m
  aws_ok sqs receive-message --queue-url "$QUEUE_URL" --visibility-timeout 60
  local handle
  handle=$(jqv '.Messages[0].ReceiptHandle')
  aws_ok sqs change-message-visibility --queue-url "$QUEUE_URL" --receipt-handle "$handle" --visibility-timeout 0
  aws_ok sqs receive-message --queue-url "$QUEUE_URL"
  assert_jq '.Messages | length' '1'
}

test_delete_message_with_garbage_receipt_handle() {
  mkq "$(new_name q-badrh)"
  aws_error ReceiptHandleIsInvalid - sqs delete-message --queue-url "$QUEUE_URL" --receipt-handle garbage
}

test_receive_message_attributes_all() {
  mkq "$(new_name q-sysattr)"
  aws_ok sqs send-message --queue-url "$QUEUE_URL" --message-body body
  aws_ok sqs receive-message --queue-url "$QUEUE_URL" --attribute-names All
  assert_jq '.Messages[0].Attributes.ApproximateReceiveCount' '1'
  assert_jq_match '.Messages[0].Attributes.SentTimestamp' '^[0-9]{13}$'
  assert_jq_match '.Messages[0].Attributes.ApproximateFirstReceiveTimestamp' '^[0-9]{13}$'
  assert_jq_match '.Messages[0].Attributes.SenderId' '^.+$'
}

test_message_attributes_and_md5() {
  mkq "$(new_name q-msgattr)"
  aws_ok sqs send-message --queue-url "$QUEUE_URL" --message-body b \
    --message-attributes '{"trace":{"DataType":"String","StringValue":"abc-123"}}'
  assert_jq '.MD5OfMessageAttributes' "$(sqs_string_attr_md5 trace abc-123)"
  aws_ok sqs receive-message --queue-url "$QUEUE_URL" --message-attribute-names All
  assert_jq '.Messages[0].MessageAttributes.trace.StringValue' 'abc-123'
  assert_jq '.Messages[0].MessageAttributes.trace.DataType' 'String'
  assert_jq '.Messages[0].MD5OfMessageAttributes' "$(sqs_string_attr_md5 trace abc-123)"
}

test_receive_without_attribute_names_returns_no_message_attributes() {
  mkq "$(new_name q-noattrnames)"
  aws_ok sqs send-message --queue-url "$QUEUE_URL" --message-body b \
    --message-attributes '{"trace":{"DataType":"String","StringValue":"abc-123"}}'
  aws_ok sqs receive-message --queue-url "$QUEUE_URL"
  assert_jq '.Messages[0].MessageAttributes // {} | length' '0'
  assert_jq '.Messages[0].MD5OfMessageAttributes // "absent"' 'absent'
}

test_message_delay_hides_message() {
  mkq "$(new_name q-delay)"
  aws_ok sqs send-message --queue-url "$QUEUE_URL" --message-body later --delay-seconds 30
  aws_ok sqs receive-message --queue-url "$QUEUE_URL"
  assert_jq '.Messages // [] | length' '0'
  aws_ok sqs get-queue-attributes --queue-url "$QUEUE_URL" --attribute-names ApproximateNumberOfMessagesDelayed ApproximateNumberOfMessages
  assert_jq_true '.Attributes == {"ApproximateNumberOfMessagesDelayed":"1","ApproximateNumberOfMessages":"0"}'
}

test_queue_delay_seconds_applies_to_new_messages() {
  mkq "$(new_name q-qdelay)" --attributes DelaySeconds=30
  aws_ok sqs send-message --queue-url "$QUEUE_URL" --message-body later
  aws_ok sqs receive-message --queue-url "$QUEUE_URL"
  assert_jq '.Messages // [] | length' '0'
}

test_long_poll_on_empty_queue_waits() {
  mkq "$(new_name q-lp)"
  local start end
  start=$(now_ms)
  aws_ok sqs receive-message --queue-url "$QUEUE_URL" --wait-time-seconds 2
  end=$(now_ms)
  assert_jq '.Messages // [] | length' '0'
  [ $((end - start)) -ge 1800 ] || fail "long poll returned after $((end - start)) ms, expected about 2000 ms"
}

test_long_poll_returns_as_soon_as_a_message_arrives() {
  mkq "$(new_name q-lpwake)"
  local url=$QUEUE_URL start end
  (sleep 1 && aws sqs send-message --queue-url "$url" --message-body wake >/dev/null 2>&1) &
  start=$(now_ms)
  aws_ok sqs receive-message --queue-url "$url" --wait-time-seconds 15
  end=$(now_ms)
  wait
  assert_jq '.Messages | length' '1'
  assert_jq '.Messages[0].Body' 'wake'
  [ $((end - start)) -lt 10000 ] || fail "long poll took $((end - start)) ms after a message arrived"
}

test_receive_max_number_of_messages_above_ten() {
  mkq "$(new_name q-max11)"
  aws_error InvalidParameterValue 400 sqs receive-message --queue-url "$QUEUE_URL" --max-number-of-messages 11
  aws_error_message_contains 'Value 11 for parameter MaxNumberOfMessages is invalid. Reason: Must be between 1 and 10, if provided.'
}

test_receive_wait_time_above_twenty() {
  mkq "$(new_name q-wait21)"
  aws_error InvalidParameterValue 400 sqs receive-message --queue-url "$QUEUE_URL" --wait-time-seconds 21
  aws_error_message_contains 'Value 21 for parameter WaitTimeSeconds is invalid. Reason: Must be >= 0 and <= 20, if provided.'
}

test_send_message_batch() {
  mkq "$(new_name q-batch)"
  aws_ok sqs send-message-batch --queue-url "$QUEUE_URL" --entries \
    '[{"Id":"a","MessageBody":"first"},{"Id":"b","MessageBody":"second"}]'
  assert_jq '.Successful | map(.Id) | sort | join(",")' 'a,b'
  assert_jq '.Failed // [] | length' '0'
  assert_jq ".Successful | map(select(.Id==\"a\"))[0].MD5OfMessageBody" "$(md5_of first)"
  aws_ok sqs receive-message --queue-url "$QUEUE_URL" --max-number-of-messages 10
  assert_jq '.Messages | map(.Body) | sort | join(",")' 'first,second'
}

test_send_message_batch_too_many_entries() {
  mkq "$(new_name q-batch11)"
  local entries i
  entries='['
  for i in 1 2 3 4 5 6 7 8 9 10 11; do
    entries+="{\"Id\":\"e$i\",\"MessageBody\":\"m$i\"},"
  done
  entries="${entries%,}]"
  aws_error AWS.SimpleQueueService.TooManyEntriesInBatchRequest 400 sqs send-message-batch --queue-url "$QUEUE_URL" --entries "$entries"
}

test_send_message_batch_duplicate_ids() {
  mkq "$(new_name q-batchdup)"
  aws_error AWS.SimpleQueueService.BatchEntryIdsNotDistinct 400 sqs send-message-batch --queue-url "$QUEUE_URL" --entries \
    '[{"Id":"a","MessageBody":"x"},{"Id":"a","MessageBody":"y"}]'
}

test_delete_message_batch() {
  mkq "$(new_name q-delbatch)"
  aws_ok sqs send-message-batch --queue-url "$QUEUE_URL" --entries \
    '[{"Id":"a","MessageBody":"first"},{"Id":"b","MessageBody":"second"}]'
  aws_ok sqs receive-message --queue-url "$QUEUE_URL" --max-number-of-messages 10
  local entries
  entries=$(jqv '[.Messages | to_entries[] | {Id: ("m" + (.key|tostring)), ReceiptHandle: .value.ReceiptHandle}]')
  aws_ok sqs delete-message-batch --queue-url "$QUEUE_URL" --entries "$entries"
  assert_jq '.Successful | length' '2'
  assert_jq '.Failed // [] | length' '0'
}

test_purge_queue_removes_messages() {
  mkq "$(new_name q-purge)"
  aws_ok sqs send-message --queue-url "$QUEUE_URL" --message-body gone
  aws_ok sqs purge-queue --queue-url "$QUEUE_URL"
  aws_ok sqs receive-message --queue-url "$QUEUE_URL"
  assert_jq '.Messages // [] | length' '0'
}

test_second_purge_within_sixty_seconds_is_rejected() {
  mkq "$(new_name q-purge2)"
  aws_ok sqs purge-queue --queue-url "$QUEUE_URL"
  aws_error AWS.SimpleQueueService.PurgeQueueInProgress 403 sqs purge-queue --queue-url "$QUEUE_URL"
}

test_delete_queue() {
  local name
  name=$(new_name q-del)
  mkq "$name"
  aws_ok sqs delete-queue --queue-url "$QUEUE_URL"
  aws_error AWS.SimpleQueueService.NonExistentQueue 400 sqs get-queue-url --queue-name "$name"
}

test_queue_tags() {
  mkq "$(new_name q-tags)" --tags env=test
  aws_ok sqs tag-queue --queue-url "$QUEUE_URL" --tags team=core
  aws_ok sqs list-queue-tags --queue-url "$QUEUE_URL"
  assert_jq_true '.Tags == {"env":"test","team":"core"}'
  aws_ok sqs untag-queue --queue-url "$QUEUE_URL" --tag-keys env
  aws_ok sqs list-queue-tags --queue-url "$QUEUE_URL"
  assert_jq_true '.Tags == {"team":"core"}'
}

test_queue_policy_is_stored() {
  local name acct policy
  name=$(new_name q-policy)
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  mkq "$name"
  policy="{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Principal\":\"*\",\"Action\":\"sqs:SendMessage\",\"Resource\":\"arn:aws:sqs:${AWS_REGION}:${acct}:${name}\"}]}"
  aws_ok sqs set-queue-attributes --queue-url "$QUEUE_URL" --attributes "{\"Policy\":$(printf '%s' "$policy" | jq -Rs .)}"
  aws_ok sqs get-queue-attributes --queue-url "$QUEUE_URL" --attribute-names Policy
  assert_jq_true ".Attributes.Policy | fromjson == $policy"
}

test_dead_letter_queue_receives_message_after_max_receive_count() {
  local dlq_url dlq_arn
  mkq "$(new_name q-dlq)"
  dlq_url=$QUEUE_URL
  aws_ok sqs get-queue-attributes --queue-url "$dlq_url" --attribute-names QueueArn
  dlq_arn=$(jqv '.Attributes.QueueArn')
  local redrive
  redrive=$(jq -nc --arg arn "$dlq_arn" '{deadLetterTargetArn:$arn,maxReceiveCount:1}')
  mkq "$(new_name q-src)" --attributes "{\"VisibilityTimeout\":\"1\",\"RedrivePolicy\":$(printf '%s' "$redrive" | jq -Rs .)}"
  local src_url=$QUEUE_URL
  aws_ok sqs send-message --queue-url "$src_url" --message-body poison
  aws_ok sqs receive-message --queue-url "$src_url"
  assert_jq '.Messages | length' '1'
  sleep 2
  # The next receive finds the message over maxReceiveCount and moves it instead of returning it.
  aws_ok sqs receive-message --queue-url "$src_url"
  assert_jq '.Messages // [] | length' '0' 'source queue after the receive count is exceeded'
  aws_ok sqs receive-message --queue-url "$dlq_url" --wait-time-seconds 5
  assert_jq '.Messages | length' '1'
  assert_jq '.Messages[0].Body' 'poison'
}

test_redrive_policy_round_trips() {
  local dlq_arn redrive
  mkq "$(new_name q-rdlq)"
  aws_ok sqs get-queue-attributes --queue-url "$QUEUE_URL" --attribute-names QueueArn
  dlq_arn=$(jqv '.Attributes.QueueArn')
  redrive=$(jq -nc --arg arn "$dlq_arn" '{deadLetterTargetArn:$arn,maxReceiveCount:3}')
  mkq "$(new_name q-rsrc)" --attributes "{\"RedrivePolicy\":$(printf '%s' "$redrive" | jq -Rs .)}"
  aws_ok sqs get-queue-attributes --queue-url "$QUEUE_URL" --attribute-names RedrivePolicy
  assert_jq_true ".Attributes.RedrivePolicy | fromjson == {\"deadLetterTargetArn\":\"$dlq_arn\",\"maxReceiveCount\":3}"
}

test_list_dead_letter_source_queues() {
  local dlq_url dlq_arn redrive src_url
  mkq "$(new_name q-ldlq)"
  dlq_url=$QUEUE_URL
  aws_ok sqs get-queue-attributes --queue-url "$dlq_url" --attribute-names QueueArn
  dlq_arn=$(jqv '.Attributes.QueueArn')
  redrive=$(jq -nc --arg arn "$dlq_arn" '{deadLetterTargetArn:$arn,maxReceiveCount:2}')
  mkq "$(new_name q-lsrc)" --attributes "{\"RedrivePolicy\":$(printf '%s' "$redrive" | jq -Rs .)}"
  src_url=$QUEUE_URL
  aws_ok sqs list-dead-letter-source-queues --queue-url "$dlq_url"
  assert_jq_true ".queueUrls == [\"$src_url\"]"
}

test_fifo_queue_name_requires_fifo_suffix() {
  aws_error InvalidParameterValue 400 sqs create-queue --queue-name "$(new_name q-notfifo)" --attributes FifoQueue=true
}

test_fifo_queue_attributes() {
  mkq "$(new_name q-fifo).fifo" --attributes FifoQueue=true
  aws_ok sqs get-queue-attributes --queue-url "$QUEUE_URL" --attribute-names All
  assert_jq '.Attributes.FifoQueue' 'true'
  assert_jq '.Attributes.DeduplicationScope' 'queue'
  assert_jq '.Attributes.FifoThroughputLimit' 'perQueue'
}

test_fifo_send_requires_message_group_id() {
  mkq "$(new_name q-fifogrp).fifo" --attributes FifoQueue=true
  aws_error MissingParameter 400 sqs send-message --queue-url "$QUEUE_URL" --message-body x --message-deduplication-id d1
  aws_error_message_contains 'The request must contain the parameter MessageGroupId.'
}

test_fifo_send_requires_deduplication_id_without_content_based_dedup() {
  mkq "$(new_name q-fifodd).fifo" --attributes FifoQueue=true
  aws_error InvalidParameterValue 400 sqs send-message --queue-url "$QUEUE_URL" --message-body x --message-group-id g
  aws_error_message_contains 'The queue should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly'
}

test_fifo_deduplicates_by_deduplication_id() {
  mkq "$(new_name q-fifodedup).fifo" --attributes FifoQueue=true
  aws_ok sqs send-message --queue-url "$QUEUE_URL" --message-body one --message-group-id g --message-deduplication-id same
  local first
  first=$(jqv '.MessageId')
  aws_ok sqs send-message --queue-url "$QUEUE_URL" --message-body two --message-group-id g --message-deduplication-id same
  assert_jq '.MessageId' "$first" 'duplicate send returns the original MessageId'
  aws_ok sqs receive-message --queue-url "$QUEUE_URL" --max-number-of-messages 10
  assert_jq '.Messages | length' '1'
  assert_jq '.Messages[0].Body' 'one'
}

test_fifo_content_based_deduplication() {
  mkq "$(new_name q-fifocb).fifo" --attributes FifoQueue=true,ContentBasedDeduplication=true
  aws_ok sqs send-message --queue-url "$QUEUE_URL" --message-body identical --message-group-id g
  aws_ok sqs send-message --queue-url "$QUEUE_URL" --message-body identical --message-group-id g
  aws_ok sqs receive-message --queue-url "$QUEUE_URL" --max-number-of-messages 10
  assert_jq '.Messages | length' '1'
}

test_fifo_preserves_order_within_message_group() {
  mkq "$(new_name q-fifoord).fifo" --attributes FifoQueue=true,ContentBasedDeduplication=true
  local i
  for i in 1 2 3; do
    aws_ok sqs send-message --queue-url "$QUEUE_URL" --message-body "msg-$i" --message-group-id g
  done
  aws_ok sqs receive-message --queue-url "$QUEUE_URL" --max-number-of-messages 10 --attribute-names SequenceNumber MessageGroupId
  assert_jq '.Messages | map(.Body) | join(",")' 'msg-1,msg-2,msg-3'
  assert_jq '.Messages | map(.Attributes.MessageGroupId) | unique | join(",")' 'g'
}

test_fifo_group_is_blocked_while_a_message_is_in_flight() {
  mkq "$(new_name q-fifoblk).fifo" --attributes FifoQueue=true,ContentBasedDeduplication=true
  aws_ok sqs send-message --queue-url "$QUEUE_URL" --message-body first --message-group-id g
  aws_ok sqs send-message --queue-url "$QUEUE_URL" --message-body second --message-group-id g
  aws_ok sqs receive-message --queue-url "$QUEUE_URL" --visibility-timeout 30
  assert_jq '.Messages | map(.Body) | join(",")' 'first'
  aws_ok sqs receive-message --queue-url "$QUEUE_URL" --visibility-timeout 30
  assert_jq '.Messages // [] | length' '0' 'second message of a group with an in-flight message'
}

test_fifo_send_message_returns_sequence_number() {
  mkq "$(new_name q-fifoseq).fifo" --attributes FifoQueue=true,ContentBasedDeduplication=true
  aws_ok sqs send-message --queue-url "$QUEUE_URL" --message-body s --message-group-id g
  assert_jq_match '.SequenceNumber' '^[0-9]{10,}$'
}

test_oversized_message_is_rejected() {
  mkq "$(new_name q-big)" --attributes MaximumMessageSize=1024
  local f
  f=$(mktemp "$UE_TMP/body.XXXXXX")
  head -c 1025 /dev/zero | tr '\0' 'x' >"$f"
  aws_error InvalidParameterValue 400 sqs send-message --queue-url "$QUEUE_URL" --message-body "file://$f"
  aws_error_message_contains 'Message must be shorter than 1024 bytes.'
}

test_send_to_missing_queue() {
  aws_ok sts get-caller-identity
  local acct
  acct=$(jqv '.Account')
  local base=${AWS_ENDPOINT_URL%/}
  aws_error AWS.SimpleQueueService.NonExistentQueue 400 sqs send-message \
    --queue-url "${base}/${acct}/$(new_name q-ghost)" --message-body x
}
