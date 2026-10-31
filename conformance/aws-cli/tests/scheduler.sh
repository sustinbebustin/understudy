#!/usr/bin/env bash
# EventBridge Scheduler: the tracer bullet, a rate() schedule with an SQS
# target read back with the defaults AWS fills, invoked on the emulator
# clock, then deleted.
# Arbiter: docs/research/scheduler.md (sections 4.2, 6.3 and 11.3).

SCHEDULER_TRUST='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"scheduler.amazonaws.com"},"Action":"sts:AssumeRole"}]}'

# advance SECONDS: move emulator time forward and settle; the clock is put
# back when the test's shell exits.
advance() {
  trap 'curl -fsS -X POST -H "Content-Type: application/json" -d "{\"services\":[\"sts\"],\"clock\":true}" "$UE_ADMIN_URL/reset" >/dev/null || true' EXIT
  curl -fsS -X POST -H 'Content-Type: application/json' -d "{\"op\":\"advance\",\"by\":\"$1s\"}" "${UE_ADMIN_URL:?UE_ADMIN_URL unset}/clock" >/dev/null
  curl -fsS "$UE_ADMIN_URL/settle?timeout=30s" | jq -e '.settled' >/dev/null ||
    fail "the emulator did not settle within 30s"
}

# A rate(1 hour) schedule sends its Input to the queue with
# <aws.scheduler.attempt-number> substituted (scheduler.md 6.3, aws-doc
# managing-schedule-context-attributes) within the hour, reads back
# ActionAfterCompletion NONE, RetryPolicy 86400/185 and GroupName default
# although none was sent (tf-acc schedule_test.go:L199-L223; scheduler.md
# 4.2), and is gone after its delete (scheduler.md 4.4).
test_rate_schedule_to_sqs() {
  local queue role name queue_arn role_arn
  queue=$(new_name sch-q)
  aws_ok sqs create-queue --queue-name "$queue"
  local queue_url
  queue_url=$(jqv '.QueueUrl')
  aws_ok sqs get-queue-attributes --queue-url "$queue_url" --attribute-names QueueArn
  queue_arn=$(jqv '.Attributes.QueueArn')
  role=$(new_name sch-role)
  aws_ok iam create-role --role-name "$role" --assume-role-policy-document "$SCHEDULER_TRUST"
  role_arn=$(jqv '.Role.Arn')
  name=$(new_name sch)

  local target
  target=$(jq -cn --arg q "$queue_arn" --arg r "$role_arn" '{Arn: $q, RoleArn: $r, Input: "hello <aws.scheduler.attempt-number>"}')
  aws_ok scheduler create-schedule --name "$name" --schedule-expression 'rate(1 hour)' \
    --flexible-time-window Mode=OFF --target "$target"
  assert_jq_match '.ScheduleArn' "^arn:aws:scheduler:${AWS_REGION}:[0-9]{12}:schedule/default/${name}\$"

  aws_ok scheduler get-schedule --name "$name"
  assert_jq '.Name' "$name"
  assert_jq '.GroupName' default
  assert_jq '.ScheduleExpression' 'rate(1 hour)'
  assert_jq '.ActionAfterCompletion' NONE
  assert_jq '.Target.RetryPolicy.MaximumEventAgeInSeconds' 86400
  assert_jq '.Target.RetryPolicy.MaximumRetryAttempts' 185
  assert_jq '.Target.Input' 'hello <aws.scheduler.attempt-number>'
  assert_jq '.FlexibleTimeWindow.Mode' OFF

  advance 3600
  aws_ok sqs receive-message --queue-url "$queue_url" --max-number-of-messages 10
  assert_jq_true '(.Messages | length) >= 1'
  assert_jq_true '[.Messages[].Body] | all(. == "hello 1")'

  aws_ok scheduler delete-schedule --name "$name"
  aws_error ResourceNotFoundException 404 scheduler get-schedule --name "$name"
}

# sch_setup: a fresh queue and a role Scheduler may assume; sets
# SCH_QUEUE_URL, SCH_QUEUE_ARN and SCH_ROLE_ARN.
sch_setup() {
  aws_ok sqs create-queue --queue-name "$(new_name sch-q)"
  SCH_QUEUE_URL=$(jqv '.QueueUrl')
  aws_ok sqs get-queue-attributes --queue-url "$SCH_QUEUE_URL" --attribute-names QueueArn
  SCH_QUEUE_ARN=$(jqv '.Attributes.QueueArn')
  aws_ok iam create-role --role-name "$(new_name sch-role)" --assume-role-policy-document "$SCHEDULER_TRUST"
  SCH_ROLE_ARN=$(jqv '.Role.Arn')
}

# A rate schedule first fires at its StartDate (aws-doc schedule-types;
# scheduler.md 5.2), its body carrying the schedule ARN, the scheduled
# time, a 16-hex execution id and attempt 1 (aws-doc
# managing-schedule-context-attributes, configuring-schedule-dlq;
# scheduler.md 6.3, 7.2); the time zone and State read back as sent
# (tf-acc schedule_test.go:L1870-L1895; scheduler.md 4.2).
test_start_date_fire_carries_the_context_attributes() {
  sch_setup
  local now start name arn target body
  now=$(curl -fsS "${UE_ADMIN_URL:?UE_ADMIN_URL unset}/clock" | jq -r '.now')
  start=$(date -u -d "@$(($(date -u -d "$now" +%s) + 600))" +%Y-%m-%dT%H:%M:%SZ)
  name=$(new_name sch)
  target=$(jq -cn --arg q "$SCH_QUEUE_ARN" --arg r "$SCH_ROLE_ARN" \
    '{Arn: $q, RoleArn: $r, Input: "<aws.scheduler.schedule-arn>|<aws.scheduler.scheduled-time>|<aws.scheduler.execution-id>|<aws.scheduler.attempt-number>"}')
  aws_ok scheduler create-schedule --name "$name" --schedule-expression 'rate(1 hour)' \
    --schedule-expression-timezone Europe/Paris --state ENABLED --start-date "$start" \
    --flexible-time-window Mode=OFF --target "$target"
  arn=$(jqv '.ScheduleArn')
  aws_ok scheduler get-schedule --name "$name"
  assert_jq '.ScheduleExpressionTimezone' Europe/Paris
  assert_jq '.State' ENABLED

  advance 660
  aws_ok sqs receive-message --queue-url "$SCH_QUEUE_URL" --max-number-of-messages 10
  assert_jq_true '(.Messages | length) == 1'
  body=$(jqv '.Messages[0].Body')
  assert_match "^${arn}\\|${start}\\|[0-9a-f]{16}\\|1\$" "$body" 'the substituted context attributes'
  aws_ok scheduler delete-schedule --name "$name"
}

# UpdateSchedule replaces the schedule: members left out read back their
# defaults (aws-doc UpdateSchedule; tf-acc schedule_test.go:L409, L776,
# L1338; scheduler.md 4.3).
test_update_drops_members_to_their_defaults() {
  sch_setup
  local name target
  name=$(new_name sch)
  target=$(jq -cn --arg q "$SCH_QUEUE_ARN" --arg r "$SCH_ROLE_ARN" '{Arn: $q, RoleArn: $r}')
  aws_ok scheduler create-schedule --name "$name" --schedule-expression 'rate(1 hour)' \
    --description before --start-date 2099-01-01T01:00:00Z --end-date 2100-01-01T01:02:03Z \
    --flexible-time-window Mode=OFF \
    --target "$(jq -c '. + {Input: "x", RetryPolicy: {MaximumEventAgeInSeconds: 60, MaximumRetryAttempts: 1}}' <<<"$target")"
  aws_ok scheduler update-schedule --name "$name" --schedule-expression 'rate(1 hour)' \
    --flexible-time-window Mode=OFF --target "$target"
  aws_ok scheduler get-schedule --name "$name"
  assert_jq_true '(.Description // "") == "" and .StartDate == null and .EndDate == null and .Target.Input == null'
  assert_jq '.Target.RetryPolicy.MaximumEventAgeInSeconds' 86400
  assert_jq '.Target.RetryPolicy.MaximumRetryAttempts' 185
  aws_ok scheduler delete-schedule --name "$name"
}

# ListSchedules returns a Schedules list (localstack@8b9a79f
# test_scheduler.py:L13-L20, validated 2024-06-11); NamePrefix filters,
# and following NextToken with MaxResults 1 yields every match once
# (scheduler.md 4.5; the order is unrecorded, so the union is compared).
test_list_schedules_by_prefix_across_pages() {
  sch_setup
  local prefix target i token seen=()
  prefix=$(new_name ls)
  target=$(jq -cn --arg q "$SCH_QUEUE_ARN" --arg r "$SCH_ROLE_ARN" '{Arn: $q, RoleArn: $r}')
  for i in 0 1 2; do
    aws_ok scheduler create-schedule --name "$prefix-$i" --schedule-expression 'rate(1 hour)' \
      --flexible-time-window Mode=OFF --target "$target"
  done
  token=""
  while :; do
    if [ -n "$token" ]; then
      aws_ok scheduler list-schedules --name-prefix "$prefix" --max-results 1 --no-paginate --next-token "$token"
    else
      aws_ok scheduler list-schedules --name-prefix "$prefix" --max-results 1 --no-paginate
    fi
    assert_jq_true '.Schedules | type == "array"'
    mapfile -t -O "${#seen[@]}" seen < <(jqv '.Schedules[].Name')
    token=$(jqv '.NextToken // ""')
    [ -n "$token" ] || break
  done
  assert_eq "$prefix-0 $prefix-1 $prefix-2" "$(printf '%s\n' "${seen[@]}" | sort | xargs)" 'the names listed across pages'
  for i in 0 1 2; do
    aws_ok scheduler delete-schedule --name "$prefix-$i"
  done
}

# rate(10 seconds) is refused with "Invalid Schedule Expression <expr>."
# (G3: localstack@8b9a79f test_scheduler.snapshot.json:L127-L270,
# validated 2025-01-26); a past at() is accepted (G4: snapshot L303-L314,
# validated 2025-02-02; scheduler.md 5.1).
test_recorded_expression_refusal_and_past_at() {
  sch_setup
  local name target
  target=$(jq -cn --arg q "$SCH_QUEUE_ARN" --arg r "$SCH_ROLE_ARN" '{Arn: $q, RoleArn: $r}')
  aws_error ValidationException 400 scheduler create-schedule --name "$(new_name sch)" \
    --schedule-expression 'rate(10 seconds)' --flexible-time-window Mode=OFF --target "$target"
  aws_error_message_contains 'Invalid Schedule Expression rate(10 seconds).'
  name=$(new_name sch)
  aws_ok scheduler create-schedule --name "$name" --schedule-expression 'at(2022-12-31T23:59:59)' \
    --flexible-time-window Mode=OFF --target "$target"
  assert_jq_match '.ScheduleArn' ":schedule/default/${name}\$"
  aws_ok scheduler delete-schedule --name "$name"
}

# A schedule whose target queue was deleted, with MaximumRetryAttempts 0,
# sends its Input to the dead-letter queue with the String attributes the
# dead-letter page lists, ERROR_CODE AWS.SimpleQueueService.NonExistentQueue
# (aws-doc configuring-schedule-dlq; scheduler.md 7.2). RETRY_ATTEMPTS and
# EXHAUSTED_RETRY_CONDITION depend on scheduler.retry.permanent-errors and
# are asserted in Go tests; DeadLetterConfig reads back as sent (4.2).
test_deleted_target_queue_dead_letters() {
  sch_setup
  local gone_url gone_arn now at name target
  aws_ok sqs create-queue --queue-name "$(new_name sch-gone)"
  gone_url=$(jqv '.QueueUrl')
  aws_ok sqs get-queue-attributes --queue-url "$gone_url" --attribute-names QueueArn
  gone_arn=$(jqv '.Attributes.QueueArn')
  aws_ok sqs delete-queue --queue-url "$gone_url"
  now=$(curl -fsS "${UE_ADMIN_URL:?UE_ADMIN_URL unset}/clock" | jq -r '.now')
  at=$(date -u -d "@$(($(date -u -d "$now" +%s) + 600))" +%Y-%m-%dT%H:%M:%S)
  name=$(new_name sch)
  target=$(jq -cn --arg q "$gone_arn" --arg r "$SCH_ROLE_ARN" --arg d "$SCH_QUEUE_ARN" \
    '{Arn: $q, RoleArn: $r, Input: "Hello, world!", RetryPolicy: {MaximumRetryAttempts: 0}, DeadLetterConfig: {Arn: $d}}')
  aws_ok scheduler create-schedule --name "$name" --schedule-expression "at($at)" \
    --flexible-time-window Mode=OFF --target "$target"
  aws_ok scheduler get-schedule --name "$name"
  assert_jq '.Target.DeadLetterConfig.Arn' "$SCH_QUEUE_ARN"
  assert_jq '.Target.RetryPolicy.MaximumRetryAttempts' 0

  advance 660
  aws_ok sqs receive-message --queue-url "$SCH_QUEUE_URL" --max-number-of-messages 10 --message-attribute-names All
  assert_jq_true '(.Messages | length) == 1'
  assert_jq '.Messages[0].Body' 'Hello, world!'
  assert_jq '.Messages[0].MessageAttributes.ERROR_CODE.StringValue' AWS.SimpleQueueService.NonExistentQueue
  assert_jq_true '.Messages[0].MessageAttributes as $a
    | ["ERROR_CODE","ERROR_MESSAGE","EXECUTION_ID","IS_PAYLOAD_TRUNCATED","RETRY_ATTEMPTS","SCHEDULED_TIME","SCHEDULE_ARN","TARGET_ARN"]
    | all(. as $n | $a[$n].DataType == "String")'
  assert_jq_match '.Messages[0].MessageAttributes.TARGET_ARN.StringValue' '^arn:aws:scheduler:::aws-sdk:[a-z0-9-]+:[a-zA-Z]+$'
  assert_jq_match '.Messages[0].MessageAttributes.SCHEDULE_ARN.StringValue' ":schedule/default/${name}\$"
  assert_jq '.Messages[0].MessageAttributes.SCHEDULED_TIME.StringValue' "${at}Z"
  aws_ok scheduler delete-schedule --name "$name"
}

# A universal (aws-sdk) target answers 501 NotImplemented and nothing is
# stored (CLAUDE.md invariant 3; spec "Scope").
test_universal_target_is_not_implemented() {
  sch_setup
  local name target
  name=$(new_name sch)
  target=$(jq -cn --arg r "$SCH_ROLE_ARN" '{Arn: "arn:aws:scheduler:::aws-sdk:sqs:sendMessage", RoleArn: $r, Input: "{}"}')
  aws_error NotImplemented 501 scheduler create-schedule --name "$name" --schedule-expression 'rate(1 hour)' \
    --flexible-time-window Mode=OFF --target "$target"
  aws_error ResourceNotFoundException 404 scheduler get-schedule --name "$name"
}
