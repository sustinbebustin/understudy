#!/usr/bin/env bash
# Step Functions, Standard workflows: create, validate, start a
# Choice/Wait/Pass machine and finish it with an admin clock advance,
# describe, history, list, stop, update, delete; the refusals (JSONata,
# EXPRESS, StartSyncExecution); a Lambda task with Retry and Catch.
# Arbiter: docs/research/stepfunctions-api.md and
# docs/research/stepfunctions-asl.md.

# shellcheck source-path=SCRIPTDIR source=../lambda-helpers.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lambda-helpers.sh"

STATES_TRUST='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"states.amazonaws.com"},"Action":"sts:AssumeRole"}]}'

# A Choice on $.wait between a one-hour Wait and the end.
ROUTE_MACHINE='{"StartAt":"Route","States":{"Route":{"Type":"Choice","Choices":[{"Variable":"$.wait","BooleanEquals":true,"Next":"Hold"}],"Default":"Done"},"Hold":{"Type":"Wait","Seconds":3600,"Next":"Done"},"Done":{"Type":"Pass","Result":{"finished":true},"ResultPath":"$.result","End":true}}}'

# states_role: create a role Step Functions may assume; sets SFN_ROLE.
states_role() {
  aws_ok iam create-role --role-name "$(new_name sfn-role)" --assume-role-policy-document "$STATES_TRUST" --output json
  SFN_ROLE=$(jqv '.Role.Arn')
}

# new_machine DEFINITION: create a machine; sets SM_ARN and SM_NAME.
new_machine() {
  SM_NAME=$(new_name sfn)
  aws_ok stepfunctions create-state-machine --name "$SM_NAME" --definition "$1" --role-arn "$SFN_ROLE"
  SM_ARN=$(jqv '.stateMachineArn')
}

# admin_clock BODY: change the emulator clock through the admin API.
admin_clock() {
  curl -fsS -X POST -H 'Content-Type: application/json' -d "$1" "${UE_ADMIN_URL:?UE_ADMIN_URL unset}/clock" >/dev/null
}

# settle: wait until the emulator has no due work.
settle() {
  curl -fsS "${UE_ADMIN_URL:?UE_ADMIN_URL unset}/settle?timeout=30s" | jq -e '.settled' >/dev/null ||
    fail "the emulator did not settle within 30s"
}

# advance SECONDS: move emulator time forward and settle; the clock is put
# back when the test's shell exits.
advance() {
  trap 'curl -fsS -X POST -H "Content-Type: application/json" -d "{\"services\":[\"sts\"],\"clock\":true}" "$UE_ADMIN_URL/reset" >/dev/null || true' EXIT
  admin_clock "{\"op\":\"advance\",\"by\":\"$1s\"}"
  settle
}

# The recorded ValidateStateMachineDefinition answers
# (stepfunctions-api.md 5.6): OK with no diagnostics, and for {} the
# required-fields diagnostic.
test_validate_state_machine_definition() {
  aws_ok stepfunctions validate-state-machine-definition --definition "$ROUTE_MACHINE"
  assert_jq '.result' OK
  assert_jq '.diagnostics | length' 0
  aws_ok stepfunctions validate-state-machine-definition --definition '{}'
  assert_jq '.result' FAIL
  assert_jq '.diagnostics[0].severity' ERROR
  assert_jq '.diagnostics[0].code' SCHEMA_VALIDATION_FAILED
  assert_jq '.diagnostics[0].message' 'These fields are required: [States, StartAt]'
  assert_jq '.diagnostics[0].location' '/'
}

# Create and describe with the recorded defaults (stepfunctions-api.md
# 5.2), then a run that waits an hour on the emulator clock
# (stepfunctions-asl.md 7.1), its history (11.2 rules 1 and 2), the
# listings, an update with a revisionId (stepfunctions-api.md 5.3), a stop
# (6.4) and a delete (5.4).
test_state_machine_lifecycle() {
  states_role
  new_machine "$ROUTE_MACHINE"
  aws_ok stepfunctions describe-state-machine --state-machine-arn "$SM_ARN"
  assert_jq '.definition' "$ROUTE_MACHINE"
  assert_jq '.status' ACTIVE
  assert_jq '.type' STANDARD
  assert_jq '.loggingConfiguration.level' OFF
  assert_jq '.loggingConfiguration.includeExecutionData' false
  assert_jq '.tracingConfiguration.enabled' false
  assert_jq '.encryptionConfiguration.type' AWS_OWNED_KEY
  assert_jq 'has("revisionId")' false

  aws_ok stepfunctions start-execution --state-machine-arn "$SM_ARN" --name waits --input '{"wait":true}'
  local exec_arn
  exec_arn=$(jqv '.executionArn')
  settle
  aws_ok stepfunctions describe-execution --execution-arn "$exec_arn"
  assert_jq '.status' RUNNING
  advance 3600
  aws_ok stepfunctions describe-execution --execution-arn "$exec_arn"
  assert_jq '.status' SUCCEEDED
  assert_jq '.output | fromjson | .result.finished' true
  aws_ok stepfunctions get-execution-history --execution-arn "$exec_arn"
  assert_jq '.events[0].type' ExecutionStarted
  assert_jq '.events[0].id' 1
  assert_jq '.events[0].previousEventId' 0
  assert_jq '.events[1].type' ChoiceStateEntered
  assert_jq '.events[1].previousEventId' 0
  assert_jq '.events[-1].type' ExecutionSucceeded

  aws_ok stepfunctions start-execution --state-machine-arn "$SM_ARN" --name stopped --input '{"wait":true}'
  local stopped
  stopped=$(jqv '.executionArn')
  settle
  aws_ok stepfunctions stop-execution --execution-arn "$stopped"
  settle
  aws_ok stepfunctions describe-execution --execution-arn "$stopped"
  assert_jq '.status' ABORTED

  aws_ok stepfunctions list-executions --state-machine-arn "$SM_ARN"
  assert_jq '.executions | length' 2
  aws_ok stepfunctions list-state-machines
  assert_jq_true "[.stateMachines[].stateMachineArn] | index(\"$SM_ARN\") != null"

  aws_ok stepfunctions update-state-machine --state-machine-arn "$SM_ARN" --definition "${ROUTE_MACHINE/3600/60}"
  assert_jq_match '.revisionId' '^[0-9a-f-]{36}$'

  aws_ok stepfunctions delete-state-machine --state-machine-arn "$SM_ARN"
  settle
  aws_error StateMachineDoesNotExist 400 stepfunctions describe-state-machine --state-machine-arn "$SM_ARN"
  aws_error_message_contains "State Machine Does Not Exist: '$SM_ARN'"
}

# The refusals: JSONata is InvalidDefinition with UNSUPPORTED_BY_UNDERSTUDY
# (stepfunctions-asl.md 10.4); EXPRESS and StartSyncExecution answer 501
# (stepfunctions-api.md 13.1). StartSyncExecution goes out with host-prefix
# injection off, or the CLI would send it to sync-127.0.0.1 (3.3).
test_refusals() {
  states_role
  aws_error InvalidDefinition 400 stepfunctions create-state-machine --name "$(new_name sfn)" --role-arn "$SFN_ROLE" \
    --definition '{"QueryLanguage":"JSONata","StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}'
  aws_error_message_contains "Invalid State Machine Definition: 'UNSUPPORTED_BY_UNDERSTUDY: "
  aws_error NotImplemented 501 stepfunctions create-state-machine --name "$(new_name sfn)" --role-arn "$SFN_ROLE" \
    --definition "$ROUTE_MACHINE" --type EXPRESS
  new_machine "$ROUTE_MACHINE"
  export AWS_DISABLE_HOST_PREFIX_INJECTION=true
  aws_error NotImplemented 501 stepfunctions start-sync-execution --state-machine-arn "$SM_ARN"
}

# A lambda:invoke Task whose function raises, retried once after a second
# of emulator time and then caught: the error is the function's errorType
# and the cause its error payload (stepfunctions-asl.md 8.1-8.4); a retry
# adds a second TaskScheduled and nothing else (11.2 rule 7). The function
# ARN Task after it returns the function's output (stepfunctions-api.md 9.2).
test_lambda_task_retry_and_catch() {
  require_docker
  new_fn --timeout 10
  wait_active "$FN"
  states_role
  local role_name=${SFN_ROLE##*/}
  aws_ok iam put-role-policy --role-name "$role_name" --policy-name invoke \
    --policy-document "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"lambda:InvokeFunction\",\"Resource\":\"$FN_ARN\"}]}"
  local def
  def=$(jq -nc --arg fn "$FN_ARN" '{StartAt:"Call",States:{
    Call:{Type:"Task",Resource:"arn:aws:states:::lambda:invoke",Parameters:{FunctionName:$fn,Payload:{action:"fail"}},
      Retry:[{ErrorEquals:["States.ALL"],IntervalSeconds:1,MaxAttempts:1}],
      Catch:[{ErrorEquals:["States.ALL"],ResultPath:"$.error",Next:"Echo"}],Next:"Echo"},
    Echo:{Type:"Task",Resource:$fn,End:true}}}')
  new_machine "$def"
  aws_ok stepfunctions start-execution --state-machine-arn "$SM_ARN" --input '{"k":"v"}'
  local exec_arn
  exec_arn=$(jqv '.executionArn')
  settle
  advance 2
  aws_ok stepfunctions describe-execution --execution-arn "$exec_arn"
  assert_jq '.status' SUCCEEDED
  assert_jq '.output | fromjson | .echo.k' v
  assert_jq '.output | fromjson | .echo.error.Error' Exception
  assert_jq '.output | fromjson | .echo.error.Cause | fromjson | .errorMessage' 'Handler fails'
  assert_jq '.output | fromjson | .echo.error.Cause | fromjson | .errorType' Exception
  aws_ok stepfunctions get-execution-history --execution-arn "$exec_arn"
  assert_jq '[.events[] | select(.type == "TaskScheduled")] | length' 2
}
