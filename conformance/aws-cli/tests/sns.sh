#!/usr/bin/env bash
# SNS: topics, attributes, subscriptions, publish, publish-batch, delivery to SQS
# (enveloped and raw), filter policies, FIFO topics, tags.
# Arbiter: docs/research/phase1/sns.md.

# mktopic NAME [create-topic args...]: sets TOPIC_ARN.
mktopic() {
  local name=$1
  shift
  aws_ok sns create-topic --name "$name" "$@"
  TOPIC_ARN=$(jqv '.TopicArn')
}

# mkqueue NAME [create-queue args...]: sets QUEUE_URL and QUEUE_ARN.
mkqueue() {
  local name=$1
  shift
  aws_ok sqs create-queue --queue-name "$name" "$@"
  QUEUE_URL=$(jqv '.QueueUrl')
  aws_ok sqs get-queue-attributes --queue-url "$QUEUE_URL" --attribute-names QueueArn
  QUEUE_ARN=$(jqv '.Attributes.QueueArn')
}

# allow_topic_to_queue: grant the topic sqs:SendMessage on the queue, as real AWS
# requires before a subscription delivers (QUEUE_URL, QUEUE_ARN, TOPIC_ARN).
allow_topic_to_queue() {
  local policy
  policy=$(jq -nc --arg q "$QUEUE_ARN" --arg t "$TOPIC_ARN" '{
    Version: "2012-10-17",
    Statement: [{Effect: "Allow", Principal: {Service: "sns.amazonaws.com"}, Action: "sqs:SendMessage",
                 Resource: $q, Condition: {ArnEquals: {"aws:SourceArn": $t}}}]}')
  aws_ok sqs set-queue-attributes --queue-url "$QUEUE_URL" \
    --attributes "$(jq -nc --arg p "$policy" '{Policy: $p}')"
}

# subscribe_queue [subscribe attributes...]: subscribe QUEUE_ARN to TOPIC_ARN; sets SUB_ARN.
subscribe_queue() {
  allow_topic_to_queue
  if [ "$#" -gt 0 ]; then
    aws_ok sns subscribe --topic-arn "$TOPIC_ARN" --protocol sqs --notification-endpoint "$QUEUE_ARN" --attributes "$1"
  else
    aws_ok sns subscribe --topic-arn "$TOPIC_ARN" --protocol sqs --notification-endpoint "$QUEUE_ARN"
  fi
  SUB_ARN=$(jqv '.SubscriptionArn')
}

# receive_all QUEUE_URL [extra receive-message args]: poll up to ~6s; leaves the
# messages in AWS_OUT (possibly empty).
receive_all() {
  local url=$1
  shift
  aws_ok sqs receive-message --queue-url "$url" --max-number-of-messages 10 --wait-time-seconds 3 "$@"
}

# expect_no_message QUEUE_URL: nothing arrives within a short poll.
expect_no_message() {
  aws_ok sqs receive-message --queue-url "$1" --max-number-of-messages 10 --wait-time-seconds 2 --message-attribute-names All
  assert_jq '.Messages // [] | length' '0' 'unexpected delivery'
}

test_create_topic_arn_shape() {
  local name acct
  name=$(new_name sns-arn)
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  mktopic "$name"
  assert_eq "arn:aws:sns:${AWS_REGION}:${acct}:${name}" "$TOPIC_ARN" TopicArn
}

test_create_topic_is_idempotent() {
  local name first
  name=$(new_name sns-idem)
  mktopic "$name"
  first=$TOPIC_ARN
  mktopic "$name"
  assert_eq "$first" "$TOPIC_ARN" 'second CreateTopic returns the same ARN'
}

test_create_topic_with_different_attributes_is_rejected() {
  local name
  name=$(new_name sns-diffattr)
  mktopic "$name" --attributes DisplayName=one
  aws_error InvalidParameter 400 sns create-topic --name "$name" --attributes DisplayName=two
  aws_error_message_contains 'Invalid parameter: Attributes Reason: Topic already exists with different attributes'
}

test_create_topic_invalid_name() {
  aws_error InvalidParameter 400 sns create-topic --name 'bad name!'
  aws_error_message_contains 'Invalid parameter: Topic Name'
}

test_create_topic_fifo_name_needs_suffix() {
  aws_error InvalidParameter 400 sns create-topic --name "$(new_name sns-notfifo)" --attributes FifoTopic=true
  aws_error_message_contains 'Invalid parameter: Topic Name'
}

test_list_topics_contains_created_topic() {
  mktopic "$(new_name sns-list)"
  aws_ok sns list-topics
  assert_jq_true "[.Topics[].TopicArn] | index(\"$TOPIC_ARN\") != null"
}

test_topic_attributes_defaults() {
  local acct
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  mktopic "$(new_name sns-attrs)"
  aws_ok sns get-topic-attributes --topic-arn "$TOPIC_ARN"
  assert_jq '.Attributes.TopicArn' "$TOPIC_ARN"
  assert_jq '.Attributes.Owner' "$acct"
  assert_jq '.Attributes.SubscriptionsConfirmed' '0'
  assert_jq '.Attributes.SubscriptionsPending' '0'
  assert_jq '.Attributes.SubscriptionsDeleted' '0'
  assert_jq '.Attributes.EffectiveDeliveryPolicy | fromjson | has("http")' 'true'
}

test_set_topic_attributes_display_name() {
  mktopic "$(new_name sns-display)"
  aws_ok sns set-topic-attributes --topic-arn "$TOPIC_ARN" --attribute-name DisplayName --attribute-value 'Pipeline events'
  aws_ok sns get-topic-attributes --topic-arn "$TOPIC_ARN"
  assert_jq '.Attributes.DisplayName' 'Pipeline events'
}

test_topic_policy_is_stored() {
  local policy
  mktopic "$(new_name sns-policy)"
  policy=$(jq -nc --arg t "$TOPIC_ARN" '{Version:"2012-10-17",Statement:[{Sid:"allow",Effect:"Allow",Principal:{AWS:"*"},Action:"sns:Publish",Resource:$t}]}')
  aws_ok sns set-topic-attributes --topic-arn "$TOPIC_ARN" --attribute-name Policy --attribute-value "$policy"
  aws_ok sns get-topic-attributes --topic-arn "$TOPIC_ARN"
  assert_jq_true ".Attributes.Policy | fromjson == $policy"
}

test_get_topic_attributes_missing_topic() {
  local acct
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  aws_error NotFound 404 sns get-topic-attributes --topic-arn "arn:aws:sns:${AWS_REGION}:${acct}:$(new_name sns-ghost)"
}

test_delete_topic() {
  mktopic "$(new_name sns-del)"
  aws_ok sns delete-topic --topic-arn "$TOPIC_ARN"
  aws_error NotFound 404 sns get-topic-attributes --topic-arn "$TOPIC_ARN"
}

test_topic_tags() {
  mktopic "$(new_name sns-tags)" --tags Key=env,Value=test
  aws_ok sns tag-resource --resource-arn "$TOPIC_ARN" --tags Key=team,Value=core
  aws_ok sns list-tags-for-resource --resource-arn "$TOPIC_ARN"
  assert_jq_true '[.Tags[] | {(.Key): .Value}] | add == {"env":"test","team":"core"}'
  aws_ok sns untag-resource --resource-arn "$TOPIC_ARN" --tag-keys env
  aws_ok sns list-tags-for-resource --resource-arn "$TOPIC_ARN"
  assert_jq_true '[.Tags[].Key] == ["team"]'
}

test_subscribe_sqs_is_confirmed_immediately() {
  mktopic "$(new_name sns-sub)"
  mkqueue "$(new_name sns-subq)"
  subscribe_queue
  assert_match "^${TOPIC_ARN}:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\$" "$SUB_ARN" SubscriptionArn
  aws_ok sns get-subscription-attributes --subscription-arn "$SUB_ARN"
  assert_jq '.Attributes.Protocol' 'sqs'
  assert_jq '.Attributes.Endpoint' "$QUEUE_ARN"
  assert_jq '.Attributes.TopicArn' "$TOPIC_ARN"
  assert_jq '.Attributes.SubscriptionArn' "$SUB_ARN"
  assert_jq '.Attributes.PendingConfirmation' 'false'
  assert_jq '.Attributes.ConfirmationWasAuthenticated' 'true'
  assert_jq '.Attributes.RawMessageDelivery' 'false'
}

test_subscribe_email_is_pending_confirmation() {
  mktopic "$(new_name sns-email)"
  aws_ok sns subscribe --topic-arn "$TOPIC_ARN" --protocol email --notification-endpoint conformance@example.com
  assert_jq '.SubscriptionArn' 'pending confirmation'
  aws_ok sns get-topic-attributes --topic-arn "$TOPIC_ARN"
  assert_jq '.Attributes.SubscriptionsPending' '1'
  assert_jq '.Attributes.SubscriptionsConfirmed' '0'
}

test_subscribe_invalid_protocol() {
  mktopic "$(new_name sns-badproto)"
  aws_error InvalidParameter 400 sns subscribe --topic-arn "$TOPIC_ARN" --protocol test-protocol --notification-endpoint x
  aws_error_message_contains 'Invalid parameter: Amazon SNS does not support this protocol string: test-protocol'
}

test_list_subscriptions_by_topic() {
  mktopic "$(new_name sns-lsub)"
  mkqueue "$(new_name sns-lsubq)"
  subscribe_queue
  aws_ok sns list-subscriptions-by-topic --topic-arn "$TOPIC_ARN"
  assert_jq '.Subscriptions | length' '1'
  assert_jq '.Subscriptions[0].SubscriptionArn' "$SUB_ARN"
  assert_jq '.Subscriptions[0].Protocol' 'sqs'
  assert_jq '.Subscriptions[0].Endpoint' "$QUEUE_ARN"
  assert_jq '.Subscriptions[0].TopicArn' "$TOPIC_ARN"
}

test_unsubscribe_is_idempotent() {
  mktopic "$(new_name sns-unsub)"
  mkqueue "$(new_name sns-unsubq)"
  subscribe_queue
  aws_ok sns unsubscribe --subscription-arn "$SUB_ARN"
  aws_ok sns list-subscriptions-by-topic --topic-arn "$TOPIC_ARN"
  assert_jq '.Subscriptions | length' '0'
  aws_ok sns unsubscribe --subscription-arn "$SUB_ARN"
}

test_publish_returns_message_id() {
  mktopic "$(new_name sns-pub)"
  aws_ok sns publish --topic-arn "$TOPIC_ARN" --message hello
  assert_jq_match '.MessageId' '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
}

test_publish_to_missing_topic() {
  local acct
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  aws_error NotFound 404 sns publish --topic-arn "arn:aws:sns:${AWS_REGION}:${acct}:$(new_name sns-ghost)" --message hello
}

test_publish_empty_subject_is_rejected() {
  mktopic "$(new_name sns-subj)"
  aws_error InvalidParameter 400 sns publish --topic-arn "$TOPIC_ARN" --message hello --subject ''
  aws_error_message_contains 'Invalid parameter: Subject'
}

test_publish_delivers_envelope_to_sqs() {
  mktopic "$(new_name sns-env)"
  mkqueue "$(new_name sns-envq)"
  subscribe_queue
  aws_ok sns publish --topic-arn "$TOPIC_ARN" --message 'hello subscribers' --subject 'greeting' \
    --message-attributes '{"attr1":{"DataType":"Number","StringValue":"99.12"}}'
  local message_id
  message_id=$(jqv '.MessageId')
  receive_all "$QUEUE_URL"
  assert_jq '.Messages | length' '1'
  local body
  body=$(jqv '.Messages[0].Body')
  AWS_OUT=$body
  assert_jq '.Type' 'Notification'
  assert_jq '.MessageId' "$message_id"
  assert_jq '.TopicArn' "$TOPIC_ARN"
  assert_jq '.Subject' 'greeting'
  assert_jq '.Message' 'hello subscribers'
  assert_jq_match '.Timestamp' '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$'
  assert_jq '.SignatureVersion' '1'
  assert_jq_match '.Signature' '^.+$'
  assert_jq_match '.SigningCertURL' '^https?://.+'
  assert_jq_match '.UnsubscribeURL' "SubscriptionArn=${SUB_ARN}\$"
  assert_jq_true '.MessageAttributes.attr1 == {"Type":"Number","Value":"99.12"}'
}

test_publish_envelope_omits_subject_and_attributes_when_absent() {
  mktopic "$(new_name sns-envmin)"
  mkqueue "$(new_name sns-envminq)"
  subscribe_queue
  aws_ok sns publish --topic-arn "$TOPIC_ARN" --message plain
  receive_all "$QUEUE_URL"
  local body
  body=$(jqv '.Messages[0].Body')
  AWS_OUT=$body
  assert_jq_true 'has("Subject") | not'
  assert_jq_true 'has("MessageAttributes") | not'
}

test_raw_message_delivery_sends_message_as_body() {
  mktopic "$(new_name sns-raw)"
  mkqueue "$(new_name sns-rawq)"
  subscribe_queue '{"RawMessageDelivery":"true"}'
  aws_ok sns publish --topic-arn "$TOPIC_ARN" --message '{"order":42}' --subject dropped \
    --message-attributes '{"kind":{"DataType":"String","StringValue":"order"},"count":{"DataType":"Number","StringValue":"111"}}'
  receive_all "$QUEUE_URL" --message-attribute-names All
  assert_jq '.Messages | length' '1'
  assert_jq '.Messages[0].Body' '{"order":42}'
  assert_jq '.Messages[0].MessageAttributes.kind.DataType' 'String'
  assert_jq '.Messages[0].MessageAttributes.kind.StringValue' 'order'
  assert_jq '.Messages[0].MessageAttributes.count.DataType' 'Number'
  assert_jq '.Messages[0].MessageAttributes.count.StringValue' '111'
}

test_set_subscription_raw_message_delivery_after_subscribe() {
  mktopic "$(new_name sns-rawset)"
  mkqueue "$(new_name sns-rawsetq)"
  subscribe_queue
  aws_ok sns set-subscription-attributes --subscription-arn "$SUB_ARN" --attribute-name RawMessageDelivery --attribute-value true
  aws_ok sns get-subscription-attributes --subscription-arn "$SUB_ARN"
  assert_jq '.Attributes.RawMessageDelivery' 'true'
  aws_ok sns publish --topic-arn "$TOPIC_ARN" --message rawbody
  receive_all "$QUEUE_URL"
  assert_jq '.Messages[0].Body' 'rawbody'
}

test_set_subscription_raw_message_delivery_invalid_value() {
  mktopic "$(new_name sns-rawbad)"
  mkqueue "$(new_name sns-rawbadq)"
  subscribe_queue
  aws_error InvalidParameter 400 sns set-subscription-attributes --subscription-arn "$SUB_ARN" \
    --attribute-name RawMessageDelivery --attribute-value wrongvalue
  aws_error_message_contains 'Invalid parameter: RawMessageDelivery: Invalid value [wrongvalue]. Must be true or false.'
}

test_set_subscription_attributes_unknown_name() {
  mktopic "$(new_name sns-subattr)"
  mkqueue "$(new_name sns-subattrq)"
  subscribe_queue
  aws_error InvalidParameter 400 sns set-subscription-attributes --subscription-arn "$SUB_ARN" \
    --attribute-name Bogus --attribute-value x
  aws_error_message_contains 'Invalid parameter: AttributeName'
}

test_filter_policy_on_message_attributes() {
  mktopic "$(new_name sns-fpa)"
  mkqueue "$(new_name sns-fpaq-match)"
  local match_url=$QUEUE_URL
  subscribe_queue '{"RawMessageDelivery":"true","FilterPolicy":"{\"event\":[\"order_created\"],\"amount\":[{\"numeric\":[\">\",100]}]}"}'
  mkqueue "$(new_name sns-fpaq-all)"
  local all_url=$QUEUE_URL
  subscribe_queue '{"RawMessageDelivery":"true"}'
  aws_ok sns publish --topic-arn "$TOPIC_ARN" --message small \
    --message-attributes '{"event":{"DataType":"String","StringValue":"order_created"},"amount":{"DataType":"Number","StringValue":"50"}}'
  aws_ok sns publish --topic-arn "$TOPIC_ARN" --message big \
    --message-attributes '{"event":{"DataType":"String","StringValue":"order_created"},"amount":{"DataType":"Number","StringValue":"150"}}'
  aws_ok sns publish --topic-arn "$TOPIC_ARN" --message other \
    --message-attributes '{"event":{"DataType":"String","StringValue":"order_deleted"},"amount":{"DataType":"Number","StringValue":"500"}}'
  receive_all "$match_url"
  assert_jq '.Messages | map(.Body) | join(",")' 'big' 'filtered subscription'
  receive_all "$all_url"
  assert_jq '.Messages | map(.Body) | sort | join(",")' 'big,other,small' 'unfiltered subscription'
}

test_filter_policy_attribute_round_trips() {
  mktopic "$(new_name sns-fprt)"
  mkqueue "$(new_name sns-fprtq)"
  subscribe_queue '{"FilterPolicy":"{\"color\":[\"red\",\"blue\"]}"}'
  aws_ok sns get-subscription-attributes --subscription-arn "$SUB_ARN"
  assert_jq_true '.Attributes.FilterPolicy | fromjson == {"color":["red","blue"]}'
  assert_jq '.Attributes.FilterPolicyScope' 'MessageAttributes'
}

test_filter_policy_on_message_body() {
  mktopic "$(new_name sns-fpb)"
  mkqueue "$(new_name sns-fpbq)"
  subscribe_queue '{"RawMessageDelivery":"true","FilterPolicyScope":"MessageBody","FilterPolicy":"{\"detail\":{\"status\":[\"failed\"]}}"}'
  aws_ok sns publish --topic-arn "$TOPIC_ARN" --message '{"detail":{"status":"ok"}}'
  aws_ok sns publish --topic-arn "$TOPIC_ARN" --message '{"detail":{"status":"failed"}}'
  aws_ok sns publish --topic-arn "$TOPIC_ARN" --message 'not json at all'
  receive_all "$QUEUE_URL"
  assert_jq '.Messages | map(.Body) | join("|")' '{"detail":{"status":"failed"}}'
}

test_filter_policy_exists_and_prefix_operators() {
  mktopic "$(new_name sns-fpop)"
  mkqueue "$(new_name sns-fpopq)"
  subscribe_queue '{"RawMessageDelivery":"true","FilterPolicy":"{\"store\":[{\"prefix\":\"eu-\"}],\"vip\":[{\"exists\":true}]}"}'
  aws_ok sns publish --topic-arn "$TOPIC_ARN" --message hit \
    --message-attributes '{"store":{"DataType":"String","StringValue":"eu-west"},"vip":{"DataType":"String","StringValue":"y"}}'
  aws_ok sns publish --topic-arn "$TOPIC_ARN" --message no-vip \
    --message-attributes '{"store":{"DataType":"String","StringValue":"eu-west"}}'
  aws_ok sns publish --topic-arn "$TOPIC_ARN" --message wrong-prefix \
    --message-attributes '{"store":{"DataType":"String","StringValue":"us-east"},"vip":{"DataType":"String","StringValue":"y"}}'
  receive_all "$QUEUE_URL"
  assert_jq '.Messages | map(.Body) | join(",")' 'hit'
}

test_invalid_filter_policy_is_rejected() {
  mktopic "$(new_name sns-fpbad)"
  mkqueue "$(new_name sns-fpbadq)"
  allow_topic_to_queue
  aws_error InvalidParameter 400 sns subscribe --topic-arn "$TOPIC_ARN" --protocol sqs --notification-endpoint "$QUEUE_ARN" \
    --attributes '{"FilterPolicy":"{\"color\":[{\"bogus-operator\":1}]}"}'
}

test_subscription_redrive_policy_round_trips() {
  mktopic "$(new_name sns-rdp)"
  mkqueue "$(new_name sns-rdpq)"
  subscribe_queue
  local dlq_arn
  mkqueue "$(new_name sns-rdpdlq)"
  dlq_arn=$QUEUE_ARN
  aws_ok sns set-subscription-attributes --subscription-arn "$SUB_ARN" --attribute-name RedrivePolicy \
    --attribute-value "{\"deadLetterTargetArn\":\"${dlq_arn}\"}"
  aws_ok sns get-subscription-attributes --subscription-arn "$SUB_ARN"
  assert_jq_true ".Attributes.RedrivePolicy | fromjson == {\"deadLetterTargetArn\":\"${dlq_arn}\"}"
}

test_publish_batch() {
  mktopic "$(new_name sns-batch)"
  mkqueue "$(new_name sns-batchq)"
  subscribe_queue '{"RawMessageDelivery":"true"}'
  aws_ok sns publish-batch --topic-arn "$TOPIC_ARN" --publish-batch-request-entries \
    '[{"Id":"a","Message":"first"},{"Id":"b","Message":"second"}]'
  assert_jq '.Successful | map(.Id) | sort | join(",")' 'a,b'
  assert_jq '.Failed // [] | length' '0'
  assert_jq_match '.Successful[0].MessageId' '^[0-9a-f]{8}-[0-9a-f]{4}-'
  receive_all "$QUEUE_URL"
  assert_jq '.Messages | map(.Body) | sort | join(",")' 'first,second'
}

test_publish_batch_too_many_entries() {
  mktopic "$(new_name sns-batch11)"
  local entries i
  entries='['
  for i in 1 2 3 4 5 6 7 8 9 10 11; do
    entries+="{\"Id\":\"e$i\",\"Message\":\"m$i\"},"
  done
  entries="${entries%,}]"
  aws_error TooManyEntriesInBatchRequest 400 sns publish-batch --topic-arn "$TOPIC_ARN" --publish-batch-request-entries "$entries"
  aws_error_message_contains 'The batch request contains more entries than permissible.'
}

test_publish_batch_duplicate_ids() {
  mktopic "$(new_name sns-batchdup)"
  aws_error BatchEntryIdsNotDistinct 400 sns publish-batch --topic-arn "$TOPIC_ARN" --publish-batch-request-entries \
    '[{"Id":"a","Message":"x"},{"Id":"a","Message":"y"}]'
}

test_fifo_topic_requires_message_group_id() {
  mktopic "$(new_name sns-fifo).fifo" --attributes FifoTopic=true,ContentBasedDeduplication=true
  aws_error InvalidParameter 400 sns publish --topic-arn "$TOPIC_ARN" --message x
  aws_error_message_contains 'Invalid parameter: The MessageGroupId parameter is required for FIFO topics'
}

test_fifo_topic_requires_deduplication_id() {
  mktopic "$(new_name sns-fifodd).fifo" --attributes FifoTopic=true
  aws_error InvalidParameter 400 sns publish --topic-arn "$TOPIC_ARN" --message x --message-group-id g
  aws_error_message_contains 'The topic should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly'
}

test_fifo_topic_delivers_to_fifo_queue_in_order() {
  mktopic "$(new_name sns-fifod).fifo" --attributes FifoTopic=true,ContentBasedDeduplication=true
  mkqueue "$(new_name sns-fifodq).fifo" --attributes FifoQueue=true,ContentBasedDeduplication=true
  subscribe_queue '{"RawMessageDelivery":"true"}'
  local i
  for i in 1 2 3; do
    aws_ok sns publish --topic-arn "$TOPIC_ARN" --message "event-$i" --message-group-id g
    assert_jq_match '.SequenceNumber' '^[0-9]+$'
  done
  receive_all "$QUEUE_URL"
  assert_jq '.Messages | map(.Body) | join(",")' 'event-1,event-2,event-3'
}

test_fifo_topic_envelope_has_sequence_number_and_no_signature() {
  mktopic "$(new_name sns-fifoenv).fifo" --attributes FifoTopic=true,ContentBasedDeduplication=true
  mkqueue "$(new_name sns-fifoenvq).fifo" --attributes FifoQueue=true,ContentBasedDeduplication=true
  subscribe_queue
  aws_ok sns publish --topic-arn "$TOPIC_ARN" --message enveloped --message-group-id g
  receive_all "$QUEUE_URL"
  local body
  body=$(jqv '.Messages[0].Body')
  AWS_OUT=$body
  assert_jq '.Type' 'Notification'
  assert_jq '.Message' 'enveloped'
  assert_jq_match '.SequenceNumber' '^[0-9]+$'
  assert_jq_true 'has("Signature") | not'
  assert_jq_true 'has("SignatureVersion") | not'
  assert_jq_true 'has("SigningCertURL") | not'
}

test_fifo_topic_deduplicates_by_deduplication_id() {
  mktopic "$(new_name sns-fifodedup).fifo" --attributes FifoTopic=true
  mkqueue "$(new_name sns-fifodedupq).fifo" --attributes FifoQueue=true,ContentBasedDeduplication=true
  subscribe_queue '{"RawMessageDelivery":"true"}'
  aws_ok sns publish --topic-arn "$TOPIC_ARN" --message one --message-group-id g --message-deduplication-id same
  aws_ok sns publish --topic-arn "$TOPIC_ARN" --message two --message-group-id g --message-deduplication-id same
  receive_all "$QUEUE_URL"
  assert_jq '.Messages | map(.Body) | join(",")' 'one'
}
