#!/usr/bin/env bash
# Step Functions Lambda tasks without Retry or Catch: a lambda:invoke Task
# and a function ARN Task against a real Python function.
# Arbiter: docs/research/stepfunctions-api.md 9.2, 9.4 and
# docs/research/stepfunctions-asl.md 8.1, 11.3.

# The helpers (states_role, new_machine, settle) and the Lambda helpers.
# shellcheck source=sfn.sh
source "$(dirname "${BASH_SOURCE[0]}")/sfn.sh"

# task_fixture: a function, an execution role and a role policy that lets
# the role invoke the function (stepfunctions-api.md 9.5).
task_fixture() {
  require_docker
  new_fn --timeout 10
  wait_active "$FN"
  states_role
  aws_ok iam put-role-policy --role-name "${SFN_ROLE##*/}" --policy-name invoke \
    --policy-document "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"lambda:InvokeFunction\",\"Resource\":\"$FN_ARN\"}]}"
}

# lambda:invoke's result is {ExecutedVersion, Payload, SdkHttpMetadata,
# SdkResponseMetadata, StatusCode} with the function's output as Payload and
# the recorded header names (stepfunctions-api.md 9.2; localstack@8b9a79f
# v2/services/test_lambda_task_service.snapshot.json:L67-L114).
test_lambda_invoke_task() {
  task_fixture
  new_machine "$(jq -nc --arg fn "$FN_ARN" '{StartAt:"Call",States:{Call:{Type:"Task",Resource:"arn:aws:states:::lambda:invoke",Parameters:{FunctionName:$fn,"Payload.$":"$"},End:true}}}')"
  aws_ok stepfunctions start-execution --state-machine-arn "$SM_ARN" --input '{"k":"v"}'
  local exec_arn
  exec_arn=$(jqv '.executionArn')
  settle
  aws_ok stepfunctions describe-execution --execution-arn "$exec_arn"
  assert_jq '.status' SUCCEEDED
  assert_jq '.output | fromjson | keys | join(",")' 'ExecutedVersion,Payload,SdkHttpMetadata,SdkResponseMetadata,StatusCode'
  assert_jq '.output | fromjson | .Payload.echo.k' v
  assert_jq '.output | fromjson | .StatusCode' 200
  assert_jq '.output | fromjson | .SdkHttpMetadata.HttpHeaders | keys | join(",")' \
    'Connection,Content-Length,Content-Type,Date,X-Amz-Executed-Version,X-Amzn-Trace-Id,x-amzn-Remapped-Content-Length,x-amzn-RequestId'
  aws_ok stepfunctions get-execution-history --execution-arn "$exec_arn"
  assert_jq '[.events[].type] | join(",")' 'ExecutionStarted,TaskStateEntered,TaskScheduled,TaskStarted,TaskSucceeded,TaskStateExited,ExecutionSucceeded'
}

# A function ARN Task whose function raises fails the execution with the
# function's errorType and its error payload as cause (stepfunctions-asl.md
# 8.1; localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L13709).
test_function_arn_task_error() {
  task_fixture
  new_machine "$(jq -nc --arg fn "$FN_ARN" '{StartAt:"Call",States:{Call:{Type:"Task",Resource:$fn,End:true}}}')"
  aws_ok stepfunctions start-execution --state-machine-arn "$SM_ARN" --input '{"action":"fail"}'
  local exec_arn
  exec_arn=$(jqv '.executionArn')
  settle
  aws_ok stepfunctions describe-execution --execution-arn "$exec_arn"
  assert_jq '.status' FAILED
  assert_jq '.error' Exception
  assert_jq '.cause | fromjson | .errorMessage' 'Handler fails'
  aws_ok stepfunctions get-execution-history --execution-arn "$exec_arn"
  assert_jq '[.events[].type] | join(",")' 'ExecutionStarted,TaskStateEntered,LambdaFunctionScheduled,LambdaFunctionStarted,LambdaFunctionFailed,ExecutionFailed'
}
