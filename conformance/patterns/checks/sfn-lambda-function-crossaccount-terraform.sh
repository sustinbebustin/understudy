# shellcheck shell=bash
# The sfn-lambda-function-crossaccount-terraform pattern's documented test,
# sourced by conformance/patterns/run.sh, which defines pattern_aws and
# endpoint.
#
#   pattern_check DIR    README, Testing: start an execution of the state
#                        machine with {"payload": "testing"}. Its one Task
#                        invokes, through arn:aws:states:::lambda:invoke, the
#                        function the second account owns, which returns
#                        {"statusCode": 200, "body": "\"Hello from Lambda in
#                        account B!\""}. The check waits for the emulator to
#                        settle, then wants the execution SUCCEEDED with that
#                        Payload and StatusCode 200 in the lambda:invoke
#                        result (docs/research/stepfunctions-api.md 9.2).

pattern_check() {
  local machine execution described status want
  machine=$(pattern_aws stepfunctions list-state-machines \
    --query "stateMachines[?name=='understudy-my-state-machine'].stateMachineArn" --output text)
  if [[ "$machine" != arn:aws:states:* ]]; then
    echo "list-state-machines found no machine named understudy-my-state-machine: '$machine'"
    return 1
  fi

  execution=$(pattern_aws stepfunctions start-execution --state-machine-arn "$machine" \
    --input '{ "payload": "testing"}' --query executionArn --output text)

  # The function's first run starts its container, so allow more than the
  # other suites' 30 seconds.
  # shellcheck disable=SC2154 # endpoint is set by run.sh
  if ! curl -fsS "$endpoint/_understudy/v1/settle?timeout=120s" | jq -e '.settled' >/dev/null; then
    echo "the emulator did not settle within 120s of start-execution"
    return 1
  fi

  described=$(pattern_aws stepfunctions describe-execution --execution-arn "$execution")
  status=$(jq -r '.status' <<<"$described")
  if [[ "$status" != SUCCEEDED ]]; then
    echo "execution $execution is $status, want SUCCEEDED: $(jq -c '{error, cause}' <<<"$described")"
    return 1
  fi
  want='{"statusCode":200,"body":"\"Hello from Lambda in account B!\""}'
  if ! jq -e --argjson want "$want" '.output | fromjson | .Payload == $want and .StatusCode == 200' <<<"$described" >/dev/null; then
    echo "execution output is $(jq -r '.output' <<<"$described"), want StatusCode 200 and Payload $want"
    return 1
  fi
}
