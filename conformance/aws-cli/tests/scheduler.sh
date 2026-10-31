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
