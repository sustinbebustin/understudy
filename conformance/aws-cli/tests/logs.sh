#!/usr/bin/env bash
# CloudWatch Logs: groups, streams, retention, PutLogEvents, GetLogEvents,
# FilterLogEvents (terms, JSON patterns, space-delimited patterns), tail, tags.
# Arbiter: docs/research/phase1/secrets-logs-events-alarms.md section 3 and
# docs/research/phase1/logs-filter-patterns.md.

# mkgroup [stream]: sets GROUP and optionally STREAM.
mkgroup() {
  GROUP="/conformance/$(new_name logs)"
  aws_ok logs create-log-group --log-group-name "$GROUP"
  if [ "${1-}" = stream ]; then
    STREAM=$(new_name stream)
    aws_ok logs create-log-stream --log-group-name "$GROUP" --log-stream-name "$STREAM"
  fi
}

# put_events MESSAGE...: write each message as its own event, one millisecond
# apart starting a minute ago. Sets T0 (the first timestamp, in ms).
put_events() {
  T0=$(($(now_ms) - 60000))
  local events="[" i=0 m
  for m in "$@"; do
    events+="$(jq -nc --argjson t $((T0 + i)) --arg m "$m" '{timestamp:$t,message:$m}'),"
    i=$((i + 1))
  done
  aws_ok logs put-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" --log-events "${events%,}]"
}

test_create_log_group_and_describe() {
  local acct
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  mkgroup
  aws_ok logs describe-log-groups --log-group-name-prefix "$GROUP"
  assert_jq '.logGroups | length' '1'
  assert_jq '.logGroups[0].logGroupName' "$GROUP"
  assert_jq '.logGroups[0].arn' "arn:aws:logs:${AWS_REGION}:${acct}:log-group:${GROUP}:*"
  assert_jq '.logGroups[0].logGroupArn' "arn:aws:logs:${AWS_REGION}:${acct}:log-group:${GROUP}"
  assert_jq '.logGroups[0].storedBytes' '0'
  assert_jq '.logGroups[0].metricFilterCount' '0'
  assert_jq '.logGroups[0].logGroupClass' 'STANDARD'
  assert_jq '.logGroups[0] | has("retentionInDays")' 'false' 'new groups never expire'
  assert_jq_match '.logGroups[0].creationTime' '^[0-9]{13}$'
}

test_create_log_group_duplicate() {
  mkgroup
  aws_error ResourceAlreadyExistsException 400 logs create-log-group --log-group-name "$GROUP"
  aws_error_message_contains 'The specified log group already exists'
}

test_describe_log_groups_prefix_and_pattern_are_exclusive() {
  mkgroup
  aws_error InvalidParameterException 400 logs describe-log-groups --log-group-name-prefix /conformance --log-group-name-pattern logs
  aws_error_message_contains 'LogGroup name prefix and LogGroup name pattern are mutually exclusive parameters.'
}

test_describe_log_groups_pattern_is_substring_match() {
  mkgroup
  local needle=${GROUP##*/}
  aws_ok logs describe-log-groups --log-group-name-pattern "$needle"
  assert_jq_true "[.logGroups[].logGroupName] == [\"$GROUP\"]"
}

test_create_log_group_with_tags() {
  GROUP="/conformance/$(new_name logs-tags)"
  aws_ok logs create-log-group --log-group-name "$GROUP" --tags env=test,team=core
  aws_ok logs list-tags-log-group --log-group-name "$GROUP"
  assert_jq_true '.tags == {"env":"test","team":"core"}'
}

test_log_group_tags_via_both_api_families() {
  local acct
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  mkgroup
  aws_ok logs tag-log-group --log-group-name "$GROUP" --tags legacy=yes
  aws_ok logs tag-resource --resource-arn "arn:aws:logs:${AWS_REGION}:${acct}:log-group:${GROUP}" --tags modern=yes
  aws_ok logs list-tags-log-group --log-group-name "$GROUP"
  assert_jq_true '.tags == {"legacy":"yes","modern":"yes"}'
  aws_ok logs list-tags-for-resource --resource-arn "arn:aws:logs:${AWS_REGION}:${acct}:log-group:${GROUP}"
  assert_jq_true '.tags == {"legacy":"yes","modern":"yes"}'
  aws_ok logs untag-log-group --log-group-name "$GROUP" --tags legacy
  aws_ok logs list-tags-for-resource --resource-arn "arn:aws:logs:${AWS_REGION}:${acct}:log-group:${GROUP}"
  assert_jq_true '.tags == {"modern":"yes"}'
}

test_delete_log_group() {
  mkgroup
  aws_ok logs delete-log-group --log-group-name "$GROUP"
  aws_ok logs describe-log-groups --log-group-name-prefix "$GROUP"
  assert_jq '.logGroups | length' '0'
  aws_error ResourceNotFoundException 400 logs delete-log-group --log-group-name "$GROUP"
}

test_retention_policy_set_and_clear() {
  mkgroup
  aws_ok logs put-retention-policy --log-group-name "$GROUP" --retention-in-days 7
  aws_ok logs describe-log-groups --log-group-name-prefix "$GROUP"
  assert_jq '.logGroups[0].retentionInDays' '7'
  aws_ok logs delete-retention-policy --log-group-name "$GROUP"
  aws_ok logs describe-log-groups --log-group-name-prefix "$GROUP"
  assert_jq '.logGroups[0] | has("retentionInDays")' 'false'
}

test_retention_policy_rejects_values_outside_the_allowed_set() {
  mkgroup
  aws_error InvalidParameterException 400 logs put-retention-policy --log-group-name "$GROUP" --retention-in-days 8
}

test_retention_policy_accepts_every_documented_value() {
  mkgroup
  local days
  for days in 1 3 5 7 14 30 60 90 120 150 180 365 400 545 731 1096 1827 2192 2557 2922 3288 3653; do
    aws_ok logs put-retention-policy --log-group-name "$GROUP" --retention-in-days "$days"
  done
}

test_create_log_stream_and_describe() {
  local acct
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  mkgroup stream
  aws_ok logs describe-log-streams --log-group-name "$GROUP"
  assert_jq '.logStreams | length' '1'
  assert_jq '.logStreams[0].logStreamName' "$STREAM"
  assert_jq '.logStreams[0].arn' "arn:aws:logs:${AWS_REGION}:${acct}:log-group:${GROUP}:log-stream:${STREAM}"
  assert_jq '.logStreams[0].storedBytes' '0'
  assert_jq_match '.logStreams[0].creationTime' '^[0-9]{13}$'
}

test_create_log_stream_duplicate() {
  mkgroup stream
  aws_error ResourceAlreadyExistsException 400 logs create-log-stream --log-group-name "$GROUP" --log-stream-name "$STREAM"
  aws_error_message_contains 'The specified log stream already exists'
}

test_create_log_stream_in_missing_group() {
  aws_error ResourceNotFoundException 400 logs create-log-stream --log-group-name "/conformance/$(new_name nogroup)" --log-stream-name s
  aws_error_message_contains 'The specified log group does not exist.'
}

test_describe_log_streams_prefix_and_ordering() {
  mkgroup
  local s
  for s in beta alpha gamma-1 gamma-2; do
    aws_ok logs create-log-stream --log-group-name "$GROUP" --log-stream-name "$s"
  done
  aws_ok logs describe-log-streams --log-group-name "$GROUP"
  assert_jq '[.logStreams[].logStreamName] | join(",")' 'alpha,beta,gamma-1,gamma-2' 'streams ordered by name'
  aws_ok logs describe-log-streams --log-group-name "$GROUP" --log-stream-name-prefix gamma
  assert_jq '[.logStreams[].logStreamName] | join(",")' 'gamma-1,gamma-2'
  aws_ok logs describe-log-streams --log-group-name "$GROUP" --descending
  assert_jq '[.logStreams[].logStreamName] | join(",")' 'gamma-2,gamma-1,beta,alpha'
}

test_delete_log_stream() {
  mkgroup stream
  aws_ok logs delete-log-stream --log-group-name "$GROUP" --log-stream-name "$STREAM"
  aws_ok logs describe-log-streams --log-group-name "$GROUP"
  assert_jq '.logStreams | length' '0'
  aws_error ResourceNotFoundException 400 logs delete-log-stream --log-group-name "$GROUP" --log-stream-name "$STREAM"
}

test_put_log_events_returns_token_and_updates_stream() {
  mkgroup stream
  put_events one two three
  assert_jq_match '.nextSequenceToken' '^.+$'
  assert_jq 'has("rejectedLogEventsInfo")' 'false'
  aws_ok logs describe-log-streams --log-group-name "$GROUP"
  assert_jq '.logStreams[0].firstEventTimestamp' "$T0"
  assert_jq '.logStreams[0].lastEventTimestamp' "$((T0 + 2))"
  assert_jq_match '.logStreams[0].lastIngestionTime' '^[0-9]{13}$'
}

test_put_log_events_unknown_stream() {
  mkgroup
  aws_error ResourceNotFoundException 400 logs put-log-events --log-group-name "$GROUP" --log-stream-name nope \
    --log-events "timestamp=$(now_ms),message=x"
  aws_error_message_contains 'The specified log stream does not exist.'
}

test_put_log_events_unknown_group() {
  aws_error ResourceNotFoundException 400 logs put-log-events --log-group-name "/conformance/$(new_name nogroup)" --log-stream-name s \
    --log-events "timestamp=$(now_ms),message=x"
  aws_error_message_contains 'The specified log group does not exist.'
}

test_put_log_events_must_be_chronological() {
  mkgroup stream
  local now
  now=$(now_ms)
  aws_error InvalidParameterException 400 logs put-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" \
    --log-events "timestamp=$now,message=later" "timestamp=$((now - 1000)),message=earlier"
  aws_error_message_contains 'Log events in a single PutLogEvents request must be in chronological order.'
}

test_put_log_events_rejects_events_older_than_fourteen_days() {
  mkgroup stream
  local old
  old=$(($(now_ms) - 30 * 86400 * 1000))
  aws_ok logs put-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" --log-events "timestamp=$old,message=ancient"
  assert_jq '.rejectedLogEventsInfo.tooOldLogEventEndIndex' '1'
  aws_ok logs get-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" --start-from-head
  assert_jq '.events | length' '0'
}

test_put_log_events_rejects_events_more_than_two_hours_ahead() {
  mkgroup stream
  local future
  future=$(($(now_ms) + 86400 * 1000))
  aws_ok logs put-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" --log-events "timestamp=$future,message=future"
  assert_jq '.rejectedLogEventsInfo.tooNewLogEventStartIndex' '0'
}

test_put_log_events_stores_valid_events_next_to_rejected_ones() {
  mkgroup stream
  local old now
  now=$(now_ms)
  old=$((now - 30 * 86400 * 1000))
  aws_ok logs put-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" \
    --log-events "timestamp=$old,message=ancient" "timestamp=$((now - 1000)),message=fresh"
  assert_jq '.rejectedLogEventsInfo.tooOldLogEventEndIndex' '1'
  aws_ok logs get-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" --start-from-head
  assert_jq '[.events[].message] | join(",")' 'fresh'
}

test_get_log_events_returns_events_with_ingestion_time() {
  mkgroup stream
  put_events alpha beta gamma
  aws_ok logs get-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" --start-from-head
  assert_jq '[.events[].message] | join(",")' 'alpha,beta,gamma'
  assert_jq '[.events[].timestamp] | join(",")' "$T0,$((T0 + 1)),$((T0 + 2))"
  assert_jq_match '.events[0].ingestionTime' '^[0-9]{13}$'
}

test_get_log_events_default_is_newest_page_in_ascending_order() {
  mkgroup stream
  put_events e1 e2 e3 e4 e5
  aws_ok logs get-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" --limit 2
  assert_jq '[.events[].message] | join(",")' 'e4,e5' 'startFromHead=false returns the newest events'
}

test_get_log_events_limit_with_start_from_head() {
  mkgroup stream
  put_events e1 e2 e3 e4 e5
  aws_ok logs get-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" --start-from-head --limit 2
  assert_jq '[.events[].message] | join(",")' 'e1,e2'
}

test_get_log_events_time_range_start_inclusive_end_exclusive() {
  mkgroup stream
  put_events e1 e2 e3 e4 e5
  aws_ok logs get-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" --start-from-head \
    --start-time "$((T0 + 1))" --end-time "$((T0 + 3))"
  assert_jq '[.events[].message] | join(",")' 'e2,e3'
}

test_get_log_events_forward_token_pagination() {
  mkgroup stream
  put_events e1 e2 e3 e4 e5
  aws_ok logs get-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" --start-from-head --limit 2 --no-paginate
  assert_jq '[.events[].message] | join(",")' 'e1,e2'
  assert_jq_match '.nextForwardToken' '^f/[0-9]+$'
  assert_jq_match '.nextBackwardToken' '^b/[0-9]+$'
  local token
  token=$(jqv '.nextForwardToken')
  aws_ok logs get-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" --start-from-head --limit 2 --next-token "$token" --no-paginate
  assert_jq '[.events[].message] | join(",")' 'e3,e4'
  token=$(jqv '.nextForwardToken')
  aws_ok logs get-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" --start-from-head --limit 2 --next-token "$token" --no-paginate
  assert_jq '[.events[].message] | join(",")' 'e5'
  token=$(jqv '.nextForwardToken')
  aws_ok logs get-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" --start-from-head --limit 2 --next-token "$token" --no-paginate
  assert_jq '.events | length' '0'
  assert_jq '.nextForwardToken' "$token" 'at the end of the stream the same token comes back'
}

test_get_log_events_invalid_token() {
  mkgroup stream
  aws_error InvalidParameterException 400 logs get-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" --next-token bogus --no-paginate
  aws_error_message_contains 'The specified nextToken is invalid.'
}

test_get_log_events_limit_above_maximum() {
  mkgroup stream
  aws_error InvalidParameterException 400 logs get-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" --limit 10001 --no-paginate
  aws_error_message_contains "Value '10001' at 'limit' failed to satisfy constraint: Member must have value less than or equal to 10000"
}

test_get_log_events_unknown_stream() {
  mkgroup
  aws_error ResourceNotFoundException 400 logs get-log-events --log-group-name "$GROUP" --log-stream-name nope
  aws_error_message_contains 'The specified log stream does not exist.'
}

test_put_log_events_multibyte_and_json_messages_round_trip() {
  mkgroup stream
  put_events 'héllo wörld ✓' '{"level":"info","note":"quote \" and \\ backslash"}'
  aws_ok logs get-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" --start-from-head
  assert_jq '.events[0].message' 'héllo wörld ✓'
  assert_jq '.events[1].message' '{"level":"info","note":"quote \" and \\ backslash"}'
}

# --------------------------------------------------------------------------
# FilterLogEvents
# --------------------------------------------------------------------------

# filter_messages ARGS...: run filter-log-events; AWS_OUT holds the response.
filtered() { jqv '[.events[].message] | join("|")'; }

test_filter_log_events_without_pattern_returns_everything_in_time_order() {
  mkgroup stream
  put_events one two three
  aws_ok logs filter-log-events --log-group-name "$GROUP"
  assert_eq 'one|two|three' "$(filtered)"
  assert_jq_match '.events[0].eventId' '^[0-9]+$'
  assert_jq '.events[0].logStreamName' "$STREAM"
  assert_jq '.events[0].timestamp' "$T0"
  assert_jq_match '.events[0].ingestionTime' '^[0-9]{13}$'
}

test_filter_log_events_term_is_case_sensitive() {
  mkgroup stream
  put_events 'ERROR disk full' 'error lowercase' 'INFO fine'
  aws_ok logs filter-log-events --log-group-name "$GROUP" --filter-pattern ERROR
  assert_eq 'ERROR disk full' "$(filtered)"
}

test_filter_log_events_multiple_terms_are_anded() {
  mkgroup stream
  put_events 'ERROR timeout db' 'ERROR disk full' 'INFO timeout'
  aws_ok logs filter-log-events --log-group-name "$GROUP" --filter-pattern 'ERROR timeout'
  assert_eq 'ERROR timeout db' "$(filtered)"
}

test_filter_log_events_optional_terms_are_ored() {
  mkgroup stream
  put_events 'ERROR a' 'WARN b' 'INFO c'
  aws_ok logs filter-log-events --log-group-name "$GROUP" --filter-pattern '?ERROR ?WARN'
  assert_eq 'ERROR a|WARN b' "$(filtered)"
}

test_filter_log_events_exact_phrase() {
  mkgroup stream
  put_events 'INTERNAL SERVER ERROR happened' 'SERVER INTERNAL ERROR happened'
  aws_ok logs filter-log-events --log-group-name "$GROUP" --filter-pattern '"INTERNAL SERVER ERROR"'
  assert_eq 'INTERNAL SERVER ERROR happened' "$(filtered)"
}

test_filter_log_events_excluded_term() {
  mkgroup stream
  put_events 'ERROR keep' 'ERROR ARGUMENTS drop'
  aws_ok logs filter-log-events --log-group-name "$GROUP" --filter-pattern 'ERROR -ARGUMENTS'
  assert_eq 'ERROR keep' "$(filtered)"
}

test_filter_log_events_match_all_pattern() {
  mkgroup stream
  put_events a b
  aws_ok logs filter-log-events --log-group-name "$GROUP" --filter-pattern '" "'
  assert_eq 'a|b' "$(filtered)"
}

test_filter_log_events_json_string_equality() {
  mkgroup stream
  put_events '{"level":"error","code":500}' '{"level":"info","code":200}' 'plain text'
  aws_ok logs filter-log-events --log-group-name "$GROUP" --filter-pattern '{ $.level = "error" }'
  assert_eq '{"level":"error","code":500}' "$(filtered)"
}

test_filter_log_events_json_numeric_comparison() {
  mkgroup stream
  put_events '{"latency":50}' '{"latency":250}' '{"latency":900}'
  aws_ok logs filter-log-events --log-group-name "$GROUP" --filter-pattern '{ $.latency > 100 && $.latency <= 250 }'
  assert_eq '{"latency":250}' "$(filtered)"
}

test_filter_log_events_json_compound_precedence() {
  mkgroup stream
  put_events '{"a":1,"b":9}' '{"a":2,"b":2}' '{"a":3,"b":3}'
  aws_ok logs filter-log-events --log-group-name "$GROUP" --filter-pattern '{ $.a = 1 || $.a = 2 && $.b = 3 }'
  assert_eq '{"a":1,"b":9}' "$(filtered)" '&& binds tighter than ||'
}

test_filter_log_events_json_nested_and_array_selectors() {
  mkgroup stream
  put_events '{"user":{"id":7},"tags":["x","y"]}' '{"user":{"id":8},"tags":["z"]}'
  aws_ok logs filter-log-events --log-group-name "$GROUP" --filter-pattern '{ $.user.id = 7 && $.tags[1] = "y" }'
  assert_eq '{"user":{"id":7},"tags":["x","y"]}' "$(filtered)"
}

test_filter_log_events_json_exists_and_null() {
  mkgroup stream
  put_events '{"k":null}' '{"k":1}' '{"other":1}'
  aws_ok logs filter-log-events --log-group-name "$GROUP" --filter-pattern '{ $.k IS NULL }'
  assert_eq '{"k":null}' "$(filtered)"
  aws_ok logs filter-log-events --log-group-name "$GROUP" --filter-pattern '{ $.k NOT EXISTS }'
  assert_eq '{"other":1}' "$(filtered)"
}

test_filter_log_events_json_wildcard_string() {
  mkgroup stream
  put_events '{"ip":"10.0.0.5"}' '{"ip":"192.168.0.1"}'
  aws_ok logs filter-log-events --log-group-name "$GROUP" --filter-pattern '{ $.ip = 10.0.0.* }'
  assert_eq '{"ip":"10.0.0.5"}' "$(filtered)"
}

test_filter_log_events_space_delimited() {
  mkgroup stream
  put_events '127.0.0.1 alice [10/Oct/2026:13:55:36] "GET /index.html" 200 1043' \
    '127.0.0.1 bob [10/Oct/2026:13:55:37] "GET /missing.html" 404 12' \
    '127.0.0.1 carol [10/Oct/2026:13:55:38] "GET /gone.html" 410 12'
  aws_ok logs filter-log-events --log-group-name "$GROUP" \
    --filter-pattern '[ip, user, timestamp, request, status_code = 4*, bytes]'
  assert_eq '127.0.0.1 bob [10/Oct/2026:13:55:37] "GET /missing.html" 404 12|127.0.0.1 carol [10/Oct/2026:13:55:38] "GET /gone.html" 410 12' "$(filtered)"
  aws_ok logs filter-log-events --log-group-name "$GROUP" \
    --filter-pattern '[ip, user, timestamp, request, status_code = 404 || status_code = 410, bytes]'
  assert_jq '.events | length' '2'
}

test_filter_log_events_time_range_and_stream_selection() {
  mkgroup
  local s1="s-one" s2="s-two"
  aws_ok logs create-log-stream --log-group-name "$GROUP" --log-stream-name "$s1"
  aws_ok logs create-log-stream --log-group-name "$GROUP" --log-stream-name "$s2"
  local t0
  t0=$(($(now_ms) - 60000))
  aws_ok logs put-log-events --log-group-name "$GROUP" --log-stream-name "$s1" \
    --log-events "timestamp=$t0,message=one-a" "timestamp=$((t0 + 20)),message=one-b"
  aws_ok logs put-log-events --log-group-name "$GROUP" --log-stream-name "$s2" \
    --log-events "timestamp=$((t0 + 10)),message=two-a" "timestamp=$((t0 + 30)),message=two-b"
  aws_ok logs filter-log-events --log-group-name "$GROUP"
  assert_eq 'one-a|two-a|one-b|two-b' "$(filtered)" 'streams are interleaved by timestamp'
  aws_ok logs filter-log-events --log-group-name "$GROUP" --log-stream-names "$s2"
  assert_eq 'two-a|two-b' "$(filtered)"
  aws_ok logs filter-log-events --log-group-name "$GROUP" --log-stream-name-prefix s-o
  assert_eq 'one-a|one-b' "$(filtered)"
  aws_ok logs filter-log-events --log-group-name "$GROUP" --start-time "$((t0 + 10))" --end-time "$((t0 + 25))"
  assert_eq 'two-a|one-b' "$(filtered)"
}

test_filter_log_events_pagination_by_limit() {
  mkgroup stream
  put_events e1 e2 e3 e4 e5
  aws_ok logs filter-log-events --log-group-name "$GROUP" --limit 2 --no-paginate
  assert_eq 'e1|e2' "$(filtered)"
  local token
  token=$(jqv '.nextToken')
  assert_ne null "$token" 'a full page carries a nextToken'
  aws_ok logs filter-log-events --log-group-name "$GROUP" --limit 2 --no-paginate --next-token "$token"
  assert_eq 'e3|e4' "$(filtered)"
}

test_filter_log_events_invalid_token() {
  mkgroup stream
  aws_error InvalidParameterException 400 logs filter-log-events --log-group-name "$GROUP" --next-token bogus --no-paginate
  aws_error_message_contains 'The specified nextToken is invalid.'
}

test_filter_log_events_stream_names_and_prefix_are_exclusive() {
  mkgroup stream
  aws_error InvalidParameterException 400 logs filter-log-events --log-group-name "$GROUP" \
    --log-stream-names "$STREAM" --log-stream-name-prefix s
}

test_filter_log_events_unknown_group() {
  aws_error ResourceNotFoundException 400 logs filter-log-events --log-group-name "/conformance/$(new_name nogroup)"
  aws_error_message_contains 'The specified log group does not exist.'
}

# --------------------------------------------------------------------------
# aws logs tail
# --------------------------------------------------------------------------

# tail_short GROUP [ARGS...]: `aws logs tail --format short` with the leading
# timestamp of each line removed, so AWS_OUT holds only the messages.
tail_short() {
  aws_ok logs tail "$@" --format short
  AWS_OUT=$(printf '%s\n' "$AWS_OUT" | sed -E 's/^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:]+ //')
}

test_logs_tail_prints_recent_events() {
  mkgroup stream
  local now
  now=$(now_ms)
  aws_ok logs put-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" \
    --log-events "timestamp=$((now - 3000)),message=first line" "timestamp=$((now - 2000)),message=second line"
  tail_short "$GROUP" --since 5m
  assert_eq $'first line\nsecond line' "$AWS_OUT"
}

test_logs_tail_detailed_format_names_the_stream() {
  mkgroup stream
  aws_ok logs put-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" \
    --log-events "timestamp=$(($(now_ms) - 1000)),message=detailed line"
  aws_ok logs tail "$GROUP" --since 5m --format detailed
  assert_contains "$STREAM" "$AWS_OUT"
  assert_contains 'detailed line' "$AWS_OUT"
}

test_logs_tail_filter_pattern_and_stream_selection() {
  mkgroup
  local a="tail-a" b="tail-b" now
  now=$(now_ms)
  aws_ok logs create-log-stream --log-group-name "$GROUP" --log-stream-name "$a"
  aws_ok logs create-log-stream --log-group-name "$GROUP" --log-stream-name "$b"
  aws_ok logs put-log-events --log-group-name "$GROUP" --log-stream-name "$a" \
    --log-events "timestamp=$((now - 3000)),message=ERROR in a" "timestamp=$((now - 2900)),message=INFO in a"
  aws_ok logs put-log-events --log-group-name "$GROUP" --log-stream-name "$b" \
    --log-events "timestamp=$((now - 2800)),message=ERROR in b"
  tail_short "$GROUP" --since 5m --filter-pattern ERROR
  assert_eq $'ERROR in a\nERROR in b' "$AWS_OUT"
  tail_short "$GROUP" --since 5m --log-stream-names "$b"
  assert_eq 'ERROR in b' "$AWS_OUT"
  tail_short "$GROUP" --since 5m --log-stream-name-prefix tail-a
  assert_eq $'ERROR in a\nINFO in a' "$AWS_OUT"
}

test_logs_tail_since_excludes_older_events() {
  mkgroup stream
  local now
  now=$(now_ms)
  aws_ok logs put-log-events --log-group-name "$GROUP" --log-stream-name "$STREAM" \
    --log-events "timestamp=$((now - 600000)),message=ten minutes ago" "timestamp=$((now - 1000)),message=just now"
  tail_short "$GROUP" --since 1m
  assert_eq 'just now' "$AWS_OUT"
}
