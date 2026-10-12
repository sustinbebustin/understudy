#!/usr/bin/env bash
# Lambda fixtures shared by the test files that need a running function
# (tests/lambda.sh, tests/events.sh). Sourced after lib.sh; never executed.

# A Python handler that logs its event and can fail, sleep, report its
# environment, or answer a function URL request (payload format 2.0).
PY_SOURCE='import json
import os
import time


def handler(event, context):
    print("conformance-log-line " + json.dumps(event))
    if isinstance(event, dict) and event.get("version") == "2.0" and "requestContext" in event:
        return {
            "statusCode": 201,
            "headers": {"content-type": "application/json", "x-conformance": "yes"},
            "body": json.dumps({
                "rawPath": event["rawPath"],
                "method": event["requestContext"]["http"]["method"],
                "query": event.get("queryStringParameters"),
                "body": event.get("body"),
            }),
        }
    action = event.get("action") if isinstance(event, dict) else None
    if action == "fail":
        raise Exception("Handler fails")
    if action == "sleep":
        time.sleep(event["seconds"])
    if action == "env":
        return {k: os.environ.get(k) for k in event["keys"]}
    return {
        "echo": event,
        "function_name": context.function_name,
        "function_version": context.function_version,
    }
'

# new_fn [CREATE-FUNCTION ARGS...]: create a Python 3.13 function (name in FN,
# ARN in FN_ARN, account in ACCT, role ARN in ROLE_ARN) and leave the create
# response in AWS_OUT.
new_fn() {
  FN=$(new_name lambda-fn)
  aws_ok sts get-caller-identity
  ACCT=$(jqv '.Account')
  ROLE_ARN=$(make_role "$(new_name lambda-role)")
  local zip="$UE_TMP/$FN.zip"
  make_zip "$zip" index.py "$PY_SOURCE"
  aws_ok lambda create-function --function-name "$FN" --runtime python3.13 --handler index.handler \
    --role "$ROLE_ARN" --zip-file "fileb://$zip" "$@"
  FN_ARN=$(jqv '.FunctionArn')
}

# wait_config NAME JQ-FILTER EXPECTED [SECONDS]: poll GetFunctionConfiguration
# until the filter yields EXPECTED.
wait_config() {
  local deadline=$((SECONDS + ${4:-120}))
  while :; do
    aws_run lambda get-function-configuration --function-name "$1"
    if [ "$AWS_RC" -eq 0 ] && [ "$(jqv "$2")" = "$3" ]; then
      return 0
    fi
    [ "$SECONDS" -lt "$deadline" ] || fail "function $1: $2 never became [$3]"
    sleep 1
  done
}

# wait_active NAME: the function is Active and its last update Successful.
wait_active() {
  wait_config "$1" .State Active 180
  wait_config "$1" .LastUpdateStatus Successful 60
}

# invoke_ok NAME PAYLOAD [EXTRA ARGS...]: invoke and require the call to succeed.
# The response metadata stays in AWS_OUT; the payload is in INVOKE_OUT.
invoke_ok() {
  local name=$1 payload=$2
  shift 2
  INVOKE_OUT=$(mktemp "$UE_TMP/invoke.XXXXXX")
  aws_ok lambda invoke --function-name "$name" --cli-binary-format raw-in-base64-out \
    --payload "$payload" "$@" "$INVOKE_OUT"
}

# invoke_payload: the payload of the last invoke_ok as compact JSON.
invoke_payload() {
  jq -c . "$INVOKE_OUT"
}

# wait_logs GROUP TERM [SECONDS]: poll FilterLogEvents until an event contains TERM.
wait_logs() {
  local deadline=$((SECONDS + ${3:-60}))
  while :; do
    aws_run logs filter-log-events --log-group-name "$1" --filter-pattern "\"$2\""
    if [ "$AWS_RC" -eq 0 ] && [ "$(jqv '.events | length')" -gt 0 ]; then
      return 0
    fi
    [ "$SECONDS" -lt "$deadline" ] || fail "no log event containing [$2] appeared in $1"
    sleep 1
  done
}
