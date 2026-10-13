#!/usr/bin/env bash
# EventBridge (default bus): rules, schedule expressions, event patterns,
# targets, input shaping, PutEvents and delivery to SQS and Lambda targets.
# Arbiter: docs/research/phase1/secrets-logs-events-alarms.md section 4 and
# docs/research/phase1/lambda-events.md section 5.

# shellcheck source=../lambda-helpers.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lambda-helpers.sh"

# new_rule [PUT-RULE ARGS...]: create a rule named in RULE (ARN in RULE_ARN).
# Without arguments it matches events whose source is RULE_SOURCE.
new_rule() {
  RULE=$(new_name events-rule)
  RULE_SOURCE="conformance.$RULE"
  if [ "$#" -eq 0 ]; then
    set -- --event-pattern "$(jq -nc --arg s "$RULE_SOURCE" '{source:[$s]}')"
  fi
  aws_ok events put-rule --name "$RULE" "$@"
  RULE_ARN=$(jqv '.RuleArn')
}

# new_target_queue: an SQS queue EventBridge may send to (URL in QUEUE_URL,
# ARN in QUEUE_ARN).
new_target_queue() {
  aws_ok sqs create-queue --queue-name "$(new_name events-q)"
  QUEUE_URL=$(jqv '.QueueUrl')
  aws_ok sqs get-queue-attributes --queue-url "$QUEUE_URL" --attribute-names QueueArn
  QUEUE_ARN=$(jqv '.Attributes.QueueArn')
  local policy attrs="$UE_TMP/queue-attrs.$RANDOM.json"
  policy=$(jq -nc --arg arn "$QUEUE_ARN" \
    '{Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{Service:"events.amazonaws.com"},Action:"sqs:SendMessage",Resource:$arn}]}')
  jq -nc --arg p "$policy" '{Policy:$p}' >"$attrs"
  aws_ok sqs set-queue-attributes --queue-url "$QUEUE_URL" --attributes "file://$attrs"
}

# targets_json ID ARN [EXTRA-JSON-OBJECT]: a one-element Targets array.
targets_json() {
  local extra=${3-}
  [ -n "$extra" ] || extra='{}'
  jq -nc --arg id "$1" --arg arn "$2" --argjson extra "$extra" '[{Id:$id,Arn:$arn} + $extra]'
}

# wait_message QUEUE_URL [SECONDS]: receive one message (body in MSG_BODY) and delete it.
wait_message() {
  local deadline=$((SECONDS + ${2:-30}))
  while :; do
    aws_ok sqs receive-message --queue-url "$1" --wait-time-seconds 2 --max-number-of-messages 1
    if [ "$(jqv '(.Messages // []) | length')" -gt 0 ]; then
      MSG_BODY=$(jqv '.Messages[0].Body')
      aws_ok sqs delete-message --queue-url "$1" --receipt-handle "$(jqv '.Messages[0].ReceiptHandle')"
      return 0
    fi
    [ "$SECONDS" -lt "$deadline" ] || fail "no message reached $1"
  done
}

# put_event SOURCE DETAIL-TYPE DETAIL-JSON: PutEvents one event; EVENT_ID is its id.
put_event() {
  local entry
  entry=$(jq -nc --arg s "$1" --arg t "$2" --arg d "$3" '[{Source:$s,DetailType:$t,Detail:$d}]')
  aws_ok events put-events --entries "$entry"
  assert_jq '.FailedEntryCount' '0'
  EVENT_ID=$(jqv '.Entries[0].EventId')
}

# match PATTERN-JSON DETAIL-JSON: TestEventPattern result (true or false) for an
# event with source test.source and the given detail.
match() {
  local event
  event=$(jq -nc --argjson d "$2" \
    '{id:"7bf73129-1428-4cd3-a780-95db273d1602",account:"111111111111",source:"test.source",time:"2024-01-01T00:00:00Z",region:"us-east-1",resources:[],"detail-type":"Test",detail:$d}')
  aws_ok events test-event-pattern --event-pattern "$1" --event "$event"
  jqv '.Result'
}

# PutRule, DescribeRule, ListRules, EnableRule, DisableRule, DeleteRule

test_put_rule_event_pattern_and_describe() {
  aws_ok sts get-caller-identity
  local acct pattern
  acct=$(jqv '.Account')
  pattern='{"source":["app.orders"],"detail-type":["order.created"]}'
  RULE=$(new_name events-rule)
  aws_ok events put-rule --name "$RULE" --event-pattern "$pattern"
  assert_jq '.RuleArn' "arn:aws:events:${AWS_REGION}:${acct}:rule/${RULE}"
  aws_ok events describe-rule --name "$RULE"
  assert_jq '.Name' "$RULE"
  assert_jq '.Arn' "arn:aws:events:${AWS_REGION}:${acct}:rule/${RULE}"
  assert_jq '.EventBusName' 'default'
  assert_jq '.State' 'ENABLED'
  assert_jq '.CreatedBy' "$acct"
  assert_jq_true '(.EventPattern | fromjson) == {"source":["app.orders"],"detail-type":["order.created"]}'
  assert_jq 'has("ScheduleExpression")' 'false'
  assert_jq 'has("Description")' 'false'
}

test_put_rule_schedule_and_describe() {
  RULE=$(new_name events-sched)
  aws_ok events put-rule --name "$RULE" --schedule-expression 'rate(5 minutes)'
  aws_ok events describe-rule --name "$RULE"
  assert_jq '.ScheduleExpression' 'rate(5 minutes)'
  assert_jq 'has("EventPattern")' 'false'
  assert_jq '.State' 'ENABLED'
}

test_put_rule_description_and_state() {
  RULE=$(new_name events-rule)
  aws_ok events put-rule --name "$RULE" --schedule-expression 'rate(1 hour)' --state DISABLED --description 'nightly cleanup'
  aws_ok events describe-rule --name "$RULE"
  assert_jq '.State' 'DISABLED'
  assert_jq '.Description' 'nightly cleanup'
}

test_put_rule_again_replaces_the_rule() {
  RULE=$(new_name events-rule)
  aws_ok events put-rule --name "$RULE" --schedule-expression 'rate(1 hour)' --description first
  aws_ok events put-rule --name "$RULE" --schedule-expression 'rate(2 hours)'
  aws_ok events describe-rule --name "$RULE"
  assert_jq '.ScheduleExpression' 'rate(2 hours)'
  assert_jq 'has("Description")' 'false'
}

test_put_rule_accepts_valid_schedule_expressions() {
  local expr
  for expr in 'rate(1 minute)' 'rate(10 minutes)' 'rate(1 hour)' 'rate(2 days)' \
    'cron(0 10 * * ? *)' 'cron(0/15 * * * ? *)' 'cron(0 18 ? * MON-FRI *)' 'cron(0 8 1 * ? *)' \
    'cron(0 2 ? * SAT#3 *)' 'cron(5,35 14 * * ? *)' 'cron(15 10 ? * 6L 2002-2005)'; do
    aws_ok events put-rule --name "$(new_name events-sched)" --schedule-expression "$expr"
  done
}

test_put_rule_rejects_invalid_schedule_expressions() {
  local expr
  for expr in 'rate(10 seconds)' 'rate(1 minutes)' 'rate(10 minute)' 'rate(0 minutes)' 'rate(-10 minutes)' \
    'rate(10 MINUTES)' 'rate( 10 minutes )' 'rate()' 'rate(foo minutes)' \
    'cron(0 1 * * * *)' 'cron(71 8 1 * ? *)' 'cron(7 20 * * NOT *)' 'cron(INVALID)'; do
    aws_error ValidationException 400 events put-rule --name "$(new_name events-sched)" --schedule-expression "$expr"
    aws_error_message_contains 'Parameter ScheduleExpression is not valid.'
  done
}

test_put_rule_rejects_malformed_event_pattern() {
  aws_error InvalidEventPatternException 400 events put-rule --name "$(new_name events-rule)" --event-pattern '{"source":"aws.ec2"}'
  aws_error_message_contains 'Event pattern is not valid. Reason: '
  aws_error InvalidEventPatternException 400 events put-rule --name "$(new_name events-rule)" --event-pattern '{"source":[]}'
  aws_error InvalidEventPatternException 400 events put-rule --name "$(new_name events-rule)" --event-pattern '["not","an","object"]'
  aws_error_message_contains 'Filter is not an object'
  aws_error InvalidEventPatternException 400 events put-rule --name "$(new_name events-rule)" \
    --event-pattern '{"detail":{"x":[{"EXISTS":true}]}}'
  aws_error_message_contains 'Unrecognized match type EXISTS'
}

test_describe_rule_missing() {
  local name
  name=$(new_name events-missing)
  aws_error ResourceNotFoundException 400 events describe-rule --name "$name"
  aws_error_message_contains "Rule ${name} does not exist on EventBus default."
}

test_list_rules_by_prefix() {
  local prefix
  prefix=$(new_name events-list)
  aws_ok events put-rule --name "${prefix}-b" --event-pattern '{"source":["b"]}'
  aws_ok events put-rule --name "${prefix}-a" --schedule-expression 'rate(1 hour)' --state DISABLED
  aws_ok events list-rules --name-prefix "$prefix"
  assert_jq_true '[.Rules[].Name] | sort == ["'"$prefix"'-a","'"$prefix"'-b"]'
  assert_jq_true '[.Rules[] | select(.Name == "'"$prefix"'-a")][0] | .State == "DISABLED" and .ScheduleExpression == "rate(1 hour)" and .EventBusName == "default" and (.Arn | test(":rule/'"$prefix"'-a$"))'
  assert_jq_true '[.Rules[] | select(.Name == "'"$prefix"'-b")][0] | has("EventPattern")'
}

test_list_rules_pagination() {
  local prefix
  prefix=$(new_name events-page)
  aws_ok events put-rule --name "${prefix}-1" --schedule-expression 'rate(1 hour)'
  aws_ok events put-rule --name "${prefix}-2" --schedule-expression 'rate(1 hour)'
  aws_ok events put-rule --name "${prefix}-3" --schedule-expression 'rate(1 hour)'
  aws_ok events list-rules --name-prefix "$prefix" --no-paginate --limit 2
  assert_jq '.Rules | length' '2'
  assert_jq_match '.NextToken' '^.+$'
  local token
  token=$(jqv '.NextToken')
  aws_ok events list-rules --name-prefix "$prefix" --no-paginate --limit 2 --next-token "$token"
  assert_jq '.Rules | length' '1'
  assert_jq 'has("NextToken")' 'false'
}

test_enable_and_disable_rule() {
  new_rule
  aws_ok events disable-rule --name "$RULE"
  aws_ok events describe-rule --name "$RULE"
  assert_jq '.State' 'DISABLED'
  aws_ok events enable-rule --name "$RULE"
  aws_ok events describe-rule --name "$RULE"
  assert_jq '.State' 'ENABLED'
}

test_delete_rule() {
  new_rule
  aws_ok events delete-rule --name "$RULE"
  aws_error ResourceNotFoundException 400 events describe-rule --name "$RULE"
}

test_delete_rule_with_targets_is_rejected() {
  new_rule
  new_target_queue
  aws_ok events put-targets --rule "$RULE" --targets "$(targets_json t1 "$QUEUE_ARN")"
  aws_error ValidationException 400 events delete-rule --name "$RULE"
  aws_error_message_contains "Rule can't be deleted since it has targets."
  aws_ok events remove-targets --rule "$RULE" --ids t1
  aws_ok events delete-rule --name "$RULE"
}

test_rule_tags() {
  RULE=$(new_name events-tags)
  aws_ok events put-rule --name "$RULE" --schedule-expression 'rate(1 hour)' --tags Key=env,Value=test Key=team,Value=core
  RULE_ARN=$(jqv '.RuleArn')
  aws_ok events list-tags-for-resource --resource-arn "$RULE_ARN"
  assert_jq_true '[.Tags[] | {(.Key): .Value}] | add == {"env":"test","team":"core"}'
  aws_ok events tag-resource --resource-arn "$RULE_ARN" --tags Key=extra,Value=1
  aws_ok events untag-resource --resource-arn "$RULE_ARN" --tag-keys env
  aws_ok events list-tags-for-resource --resource-arn "$RULE_ARN"
  assert_jq_true '[.Tags[] | {(.Key): .Value}] | add == {"team":"core","extra":"1"}'
}

test_tag_resource_missing_rule() {
  aws_ok sts get-caller-identity
  local name
  name=$(new_name events-missing)
  aws_error ResourceNotFoundException 400 events tag-resource \
    --resource-arn "arn:aws:events:${AWS_REGION}:$(jqv '.Account'):rule/${name}" --tags Key=a,Value=b
  aws_error_message_contains "Rule ${name} does not exist on EventBus default."
}

# PutTargets, ListTargetsByRule, RemoveTargets

test_put_targets_and_list() {
  new_rule
  new_target_queue
  aws_ok events put-targets --rule "$RULE" --targets "$(targets_json first "$QUEUE_ARN" '{"Input":"{\"fixed\":true}"}')"
  assert_jq '.FailedEntryCount' '0'
  assert_jq_true '.FailedEntries == []'
  aws_ok events list-targets-by-rule --rule "$RULE"
  assert_jq '.Targets | length' '1'
  assert_jq '.Targets[0].Id' 'first'
  assert_jq '.Targets[0].Arn' "$QUEUE_ARN"
  assert_jq '.Targets[0].Input' '{"fixed":true}'
}

test_put_targets_same_id_replaces_target() {
  new_rule
  new_target_queue
  local first_arn=$QUEUE_ARN
  aws_ok events put-targets --rule "$RULE" --targets "$(targets_json same "$first_arn")"
  new_target_queue
  aws_ok events put-targets --rule "$RULE" --targets "$(targets_json same "$QUEUE_ARN")"
  aws_ok events list-targets-by-rule --rule "$RULE"
  assert_jq '.Targets | length' '1'
  assert_jq '.Targets[0].Arn' "$QUEUE_ARN"
}

test_remove_targets() {
  new_rule
  new_target_queue
  aws_ok events put-targets --rule "$RULE" --targets "$(targets_json gone "$QUEUE_ARN")"
  aws_ok events remove-targets --rule "$RULE" --ids gone
  assert_jq '.FailedEntryCount' '0'
  aws_ok events list-targets-by-rule --rule "$RULE"
  assert_jq '.Targets | length' '0'
}

test_put_targets_at_most_five_per_rule() {
  new_rule
  new_target_queue
  aws_ok events put-targets --rule "$RULE" --targets "$(jq -nc --arg arn "$QUEUE_ARN" '[range(1; 6) | {Id: "t\(.)", Arn: $arn}]')"
  assert_jq '.FailedEntryCount' '0'
  aws_error LimitExceededException 400 events put-targets --rule "$RULE" --targets "$(targets_json t6 "$QUEUE_ARN")"
  aws_error_message_contains 'The requested resource exceeds the maximum number allowed.'
}

test_put_targets_input_and_input_path_are_exclusive() {
  new_rule
  new_target_queue
  aws_error ValidationException 400 events put-targets --rule "$RULE" \
    --targets "$(targets_json both "$QUEUE_ARN" '{"Input":"{}","InputPath":"$.detail"}')"
  aws_error_message_contains 'Only one of Input, InputPath, or InputTransformer must be provided for target both.'
}

test_put_targets_undefined_placeholder() {
  new_rule
  new_target_queue
  aws_error ValidationException 400 events put-targets --rule "$RULE" \
    --targets "$(targets_json tx "$QUEUE_ARN" '{"InputTransformer":{"InputPathsMap":{"a":"$.detail.a"},"InputTemplate":"{\"x\":<notdefinedkey>}"}}')"
  aws_error_message_contains 'InputTemplate for target tx contains invalid placeholder notdefinedkey.'
}

test_put_targets_invalid_target_id() {
  new_rule
  new_target_queue
  aws_error ValidationException 400 events put-targets --rule "$RULE" --targets "$(targets_json '!@#$@!#$' "$QUEUE_ARN")"
  aws_error_message_contains "at 'targets.1.member.id' failed to satisfy constraint: Member must satisfy regular expression pattern: [\\.\\-_A-Za-z0-9]+"
}

test_put_targets_missing_rule() {
  new_target_queue
  local name
  name=$(new_name events-missing)
  aws_error ResourceNotFoundException 400 events put-targets --rule "$name" --targets "$(targets_json t1 "$QUEUE_ARN")"
  aws_error_message_contains "Rule ${name} does not exist on EventBus default."
}

# TestEventPattern: the matching grammar

test_event_pattern_exact_match_and_or() {
  assert_eq true "$(match '{"detail":{"state":["running","pending"]}}' '{"state":"pending"}')"
  assert_eq false "$(match '{"detail":{"state":["running","pending"]}}' '{"state":"stopped"}')"
}

test_event_pattern_fields_combine_with_and() {
  assert_eq true "$(match '{"source":["test.source"],"detail":{"a":[1],"b":["x"]}}' '{"a":1,"b":"x"}')"
  assert_eq false "$(match '{"source":["test.source"],"detail":{"a":[1],"b":["x"]}}' '{"a":1,"b":"y"}')"
}

test_event_pattern_prefix_and_suffix() {
  assert_eq true "$(match '{"detail":{"file":[{"prefix":"img-"}]}}' '{"file":"img-1.png"}')"
  assert_eq false "$(match '{"detail":{"file":[{"prefix":"img-"}]}}' '{"file":"doc-1.png"}')"
  assert_eq true "$(match '{"detail":{"file":[{"suffix":".png"}]}}' '{"file":"img-1.png"}')"
  assert_eq false "$(match '{"detail":{"file":[{"suffix":".png"}]}}' '{"file":"img-1.gif"}')"
}

test_event_pattern_anything_but() {
  assert_eq true "$(match '{"detail":{"state":[{"anything-but":["stopped"]}]}}' '{"state":"running"}')"
  assert_eq false "$(match '{"detail":{"state":[{"anything-but":["stopped"]}]}}' '{"state":"stopped"}')"
}

test_event_pattern_numeric() {
  assert_eq true "$(match '{"detail":{"n":[{"numeric":[">",0,"<=",5]}]}}' '{"n":5}')"
  assert_eq false "$(match '{"detail":{"n":[{"numeric":[">",0,"<=",5]}]}}' '{"n":6}')"
  assert_eq false "$(match '{"detail":{"n":[{"numeric":["=",300]}]}}' '{"n":"300"}')"
}

test_event_pattern_exists() {
  assert_eq true "$(match '{"detail":{"opt":[{"exists":true}]}}' '{"opt":"x"}')"
  assert_eq false "$(match '{"detail":{"opt":[{"exists":true}]}}' '{"other":"x"}')"
  assert_eq true "$(match '{"detail":{"opt":[{"exists":false}]}}' '{"other":"x"}')"
}

test_event_pattern_or_operator() {
  assert_eq true "$(match '{"detail":{"$or":[{"a":[1]},{"b":[2]}]}}' '{"b":2}')"
  assert_eq false "$(match '{"detail":{"$or":[{"a":[1]},{"b":[2]}]}}' '{"a":2,"b":1}')"
}

test_event_pattern_wildcard_and_ignore_case() {
  assert_eq true "$(match '{"detail":{"path":[{"wildcard":"dir/*.png"}]}}' '{"path":"dir/a.png"}')"
  assert_eq false "$(match '{"detail":{"path":[{"wildcard":"dir/*.png"}]}}' '{"path":"other/a.png"}')"
  assert_eq true "$(match '{"detail":{"name":[{"equals-ignore-case":"ALICE"}]}}' '{"name":"alice"}')"
}

test_event_pattern_cidr() {
  assert_eq true "$(match '{"detail":{"ip":[{"cidr":"10.0.0.0/24"}]}}' '{"ip":"10.0.0.17"}')"
  assert_eq false "$(match '{"detail":{"ip":[{"cidr":"10.0.0.0/24"}]}}' '{"ip":"10.0.1.17"}')"
}

test_event_pattern_null_and_empty_string_differ() {
  assert_eq true "$(match '{"detail":{"v":[null]}}' '{"v":null}')"
  assert_eq false "$(match '{"detail":{"v":[null]}}' '{"v":""}')"
  assert_eq true "$(match '{"detail":{"v":[""]}}' '{"v":""}')"
}

test_event_pattern_array_values_match_by_intersection() {
  assert_eq true "$(match '{"detail":{"tags":["b"]}}' '{"tags":["a","b"]}')"
  assert_eq false "$(match '{"detail":{"tags":["c"]}}' '{"tags":["a","b"]}')"
}

# PutEvents and delivery

test_put_events_delivers_full_event_to_sqs() {
  aws_ok sts get-caller-identity
  local acct
  acct=$(jqv '.Account')
  new_rule
  new_target_queue
  aws_ok events put-targets --rule "$RULE" --targets "$(targets_json q "$QUEUE_ARN")"
  put_event "$RULE_SOURCE" order.created '{"orderId":42,"state":"NEW"}'
  wait_message "$QUEUE_URL"
  AWS_OUT=$MSG_BODY
  assert_jq '.version' '0'
  assert_jq '.id' "$EVENT_ID"
  assert_jq '."detail-type"' 'order.created'
  assert_jq '.source' "$RULE_SOURCE"
  assert_jq '.account' "$acct"
  assert_jq '.region' "$AWS_REGION"
  assert_jq_match '.time' '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$'
  assert_jq_true '.resources == []'
  assert_jq_true '.detail == {"orderId":42,"state":"NEW"}'
}

test_put_events_response_shape() {
  aws_ok events put-events --entries '[{"Source":"conformance.shape","DetailType":"t","Detail":"{}"},{"Source":"conformance.shape","DetailType":"t","Detail":"{\"a\":1}"}]'
  assert_jq '.FailedEntryCount' '0'
  assert_jq '.Entries | length' '2'
  assert_jq_match '.Entries[0].EventId' '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
  assert_jq_true '.Entries[0].EventId != .Entries[1].EventId'
}

test_put_events_carries_resources_to_the_target() {
  new_rule
  new_target_queue
  aws_ok events put-targets --rule "$RULE" --targets "$(targets_json q "$QUEUE_ARN")"
  aws_ok events put-events --entries "$(jq -nc --arg s "$RULE_SOURCE" \
    '[{Source:$s,DetailType:"t",Detail:"{}",Resources:["arn:aws:ec2:us-east-1:111111111111:instance/i-1","arn:aws:ec2:us-east-1:111111111111:instance/i-2"]}]')"
  wait_message "$QUEUE_URL"
  AWS_OUT=$MSG_BODY
  assert_jq_true '.resources == ["arn:aws:ec2:us-east-1:111111111111:instance/i-1","arn:aws:ec2:us-east-1:111111111111:instance/i-2"]'
}

test_put_events_event_not_matching_is_not_delivered() {
  new_rule
  new_target_queue
  aws_ok events put-targets --rule "$RULE" --targets "$(targets_json q "$QUEUE_ARN")"
  put_event "other.$RULE" order.created '{}'
  sleep 3
  aws_ok sqs receive-message --queue-url "$QUEUE_URL" --wait-time-seconds 2
  assert_jq '(.Messages // []) | length' '0'
}

test_put_events_disabled_rule_does_not_deliver() {
  new_rule
  new_target_queue
  aws_ok events put-targets --rule "$RULE" --targets "$(targets_json q "$QUEUE_ARN")"
  aws_ok events disable-rule --name "$RULE"
  put_event "$RULE_SOURCE" order.created '{}'
  sleep 3
  aws_ok sqs receive-message --queue-url "$QUEUE_URL" --wait-time-seconds 2
  assert_jq '(.Messages // []) | length' '0'
}

test_target_constant_input_replaces_the_event() {
  new_rule
  new_target_queue
  aws_ok events put-targets --rule "$RULE" --targets "$(targets_json q "$QUEUE_ARN" '{"Input":"{\"constant\":true}"}')"
  put_event "$RULE_SOURCE" t '{"x":1}'
  wait_message "$QUEUE_URL"
  assert_eq '{"constant":true}' "$(printf '%s' "$MSG_BODY" | jq -c .)" 'message body'
}

test_target_input_path_selects_a_subtree() {
  new_rule
  new_target_queue
  aws_ok events put-targets --rule "$RULE" --targets "$(targets_json q "$QUEUE_ARN" '{"InputPath":"$.detail"}')"
  put_event "$RULE_SOURCE" t '{"payload":{"sf_id":"baz"},"n":1}'
  wait_message "$QUEUE_URL"
  assert_eq '{"payload":{"sf_id":"baz"},"n":1}' "$(printf '%s' "$MSG_BODY" | jq -c .)" 'message body'
}

test_target_input_path_to_a_string_keeps_json_quotes() {
  new_rule
  new_target_queue
  aws_ok events put-targets --rule "$RULE" --targets "$(targets_json q "$QUEUE_ARN" '{"InputPath":"$.detail.payload.sf_id"}')"
  put_event "$RULE_SOURCE" t '{"payload":{"sf_id":"baz"}}'
  wait_message "$QUEUE_URL"
  assert_eq '"baz"' "$MSG_BODY" 'message body'
}

test_target_input_transformer() {
  new_rule
  new_target_queue
  local tx
  tx='{"InputTransformer":{"InputPathsMap":{"state":"$.detail.state","src":"$.source"},"InputTemplate":"{\"state\":<state>,\"from\":\"<src>\",\"rule\":\"<aws.events.rule-name>\"}"}}'
  aws_ok events put-targets --rule "$RULE" --targets "$(targets_json q "$QUEUE_ARN" "$tx")"
  put_event "$RULE_SOURCE" t '{"state":"running"}'
  wait_message "$QUEUE_URL"
  assert_eq "{\"state\":\"running\",\"from\":\"$RULE_SOURCE\",\"rule\":\"$RULE\"}" "$(printf '%s' "$MSG_BODY" | jq -c .)" 'message body'
}

test_put_events_failed_entries() {
  aws_ok events put-events --entries '[{"Source":"conformance.fail","DetailType":"t","Detail":"{}"},{"Source":"conformance.fail","Detail":"{}"},{"Source":"conformance.fail","DetailType":"t","Detail":"not json"},{"DetailType":"t","Detail":"{}"}]'
  assert_eq 200 "$AWS_STATUS" 'status'
  assert_jq '.FailedEntryCount' '3'
  assert_jq_match '.Entries[0].EventId' '^[0-9a-f-]{36}$'
  assert_jq '.Entries[1].ErrorCode' 'InvalidArgument'
  assert_jq '.Entries[1].ErrorMessage' 'Parameter DetailType is not valid. Reason: DetailType is a required argument.'
  assert_jq '.Entries[2].ErrorCode' 'MalformedDetail'
  assert_jq '.Entries[2].ErrorMessage' 'Detail is malformed.'
  assert_jq '.Entries[3].ErrorCode' 'InvalidArgument'
  assert_jq '.Entries[3].ErrorMessage' 'Parameter Source is not valid. Reason: Source is a required argument.'
}

test_put_events_more_than_ten_entries() {
  local entries
  entries=$(jq -nc '[range(11) | {Source:"conformance.many",DetailType:"t",Detail:"{}"}]')
  aws_error ValidationException 400 events put-events --entries "$entries"
  aws_error_message_contains "at 'entries' failed to satisfy constraint: Member must have length less than or equal to 10"
}

test_put_events_to_a_missing_bus_is_accepted() {
  aws_ok events put-events --entries "$(jq -nc --arg b "$(new_name events-nobus)" \
    '[{Source:"conformance.nobus",DetailType:"t",Detail:"{}",EventBusName:$b}]')"
  assert_jq '.FailedEntryCount' '0'
  assert_jq_match '.Entries[0].EventId' '^[0-9a-f-]{36}$'
}

test_put_events_lambda_target_is_invoked() {
  require_docker
  new_fn
  wait_active "$FN"
  new_rule
  aws_ok lambda add-permission --function-name "$FN" --statement-id events --action lambda:InvokeFunction \
    --principal events.amazonaws.com --source-arn "$RULE_ARN"
  aws_ok events put-targets --rule "$RULE" --targets "$(targets_json fn "$FN_ARN")"
  assert_jq '.FailedEntryCount' '0'
  put_event "$RULE_SOURCE" order.created '{"marker":"events-lambda-marker-6b13"}'
  wait_logs "/aws/lambda/$FN" events-lambda-marker-6b13 120
  aws_ok logs filter-log-events --log-group-name "/aws/lambda/$FN" --filter-pattern '"events-lambda-marker-6b13"'
  AWS_OUT=$(jqv '[.events[].message | select(startswith("conformance-log-line"))][0] | ltrimstr("conformance-log-line ")')
  assert_jq '.id' "$EVENT_ID"
  assert_jq '.source' "$RULE_SOURCE"
  assert_jq '."detail-type"' 'order.created'
  assert_jq '.detail.marker' 'events-lambda-marker-6b13'
}

test_scheduled_rule_delivers_scheduled_event() {
  aws_ok sts get-caller-identity
  local acct
  acct=$(jqv '.Account')
  new_target_queue
  RULE=$(new_name events-tick)
  aws_ok events put-rule --name "$RULE" --schedule-expression 'rate(1 minute)'
  RULE_ARN=$(jqv '.RuleArn')
  aws_ok events put-targets --rule "$RULE" --targets "$(targets_json q "$QUEUE_ARN")"
  wait_message "$QUEUE_URL" 150
  AWS_OUT=$MSG_BODY
  assert_jq '.version' '0'
  assert_jq_match '.id' '^[0-9a-f-]{36}$'
  assert_jq '."detail-type"' 'Scheduled Event'
  assert_jq '.source' 'aws.events'
  assert_jq '.account' "$acct"
  assert_jq '.region' "$AWS_REGION"
  assert_jq_match '.time' '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$'
  assert_jq_true ".resources == [\"$RULE_ARN\"]"
  assert_jq_true '.detail == {}'
}
