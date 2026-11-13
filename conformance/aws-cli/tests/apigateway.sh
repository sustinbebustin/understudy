#!/usr/bin/env bash
# API Gateway REST APIs: the apigateway control plane, MOCK and Lambda proxy
# invocations through the execute-api path fallback.
# Arbiter: docs/research/apigateway-rest.md (sections 1 to 6).
#
# Error texts are asserted only where LocalStack's AWS-validated snapshots
# record them (apigateway-rest.md 2.4); the AWS_PROXY test needs Docker and
# the Python 3.13 Lambda base image, and skips when the emulator reports
# Docker as not enabled.

# shellcheck source-path=SCRIPTDIR source=../lambda-helpers.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lambda-helpers.sh"

# new_rest_api: create a REST API (id in API_ID, root resource in ROOT_ID)
# and leave the response in AWS_OUT.
new_rest_api() {
  aws_ok apigateway create-rest-api --name "$(new_name apigw1)" "$@"
  API_ID=$(jqv '.id')
  ROOT_ID=$(jqv '.rootResourceId')
}

# rest_url PATH: the path fallback URL of PATH on the API
# (/_understudy/execute-api/<api-id>/<stage>/<rest>, apigateway-rest.md 4.1).
rest_url() {
  printf '%s/_understudy/execute-api/%s%s' "$AWS_ENDPOINT_URL" "$API_ID" "$1"
}

# fetch METHOD PATH: request PATH of the API; the status lands in
# HTTP_STATUS, the body in HTTP_BODY and the headers in HTTP_HEADERS.
fetch() {
  HTTP_BODY="$UE_TMP/apigw1-body"
  HTTP_HEADERS="$UE_TMP/apigw1-headers"
  HTTP_STATUS=$(curl -sS -X "$1" -o "$HTTP_BODY" -D "$HTTP_HEADERS" -w '%{http_code}' "$(rest_url "$2")")
}

# Create defaults (apigateway-rest.md 2.3: apiKeySource HEADER,
# disableExecuteApiEndpoint false, an EDGE endpoint) and the root resource
# "/" with no parentId (3.1); an id is case-sensitive and a deleted or
# upper-cased one is NotFoundException 404 with the recorded text (2.4).
test_rest_api_lifecycle() {
  new_rest_api --description first
  assert_jq '.apiKeySource' HEADER
  assert_jq '.disableExecuteApiEndpoint' false
  assert_jq '.endpointConfiguration.types | join(",")' EDGE
  aws_ok apigateway get-resources --rest-api-id "$API_ID"
  assert_jq '[.items[] | "\(.id) \(.path) \(.parentId // "-")"] | join(",")' "$ROOT_ID / -"
  aws_ok apigateway update-rest-api --rest-api-id "$API_ID" \
    --patch-operations op=replace,path=/description,value=second
  assert_jq '.description' second
  aws_ok apigateway get-rest-apis
  assert_jq_true "[.items[] | select(.id == \"$API_ID\" and .description == \"second\")] | length == 1"
  aws_ok sts get-caller-identity
  local acct upper
  acct=$(jqv '.Account')
  upper=$(printf '%s' "$API_ID" | tr '[:lower:]' '[:upper:]')
  if [ "$upper" != "$API_ID" ]; then
    aws_error NotFoundException 404 apigateway get-rest-api --rest-api-id "$upper"
    aws_error_message_contains "Invalid API identifier specified ${acct}:${upper}"
  fi
  aws_ok apigateway delete-rest-api --rest-api-id "$API_ID"
  aws_error NotFoundException 404 apigateway get-rest-api --rest-api-id "$API_ID"
  aws_error_message_contains "Invalid API identifier specified ${acct}:${API_ID}"
}

# The recorded refusals of the control plane (apigateway-rest.md 2.4): an
# empty description, a duplicate sibling, a child of a greedy resource and
# an unknown integration type.
test_control_plane_refusals() {
  aws_error BadRequestException 400 apigateway create-rest-api --name "$(new_name apigw1)" --description ''
  aws_error_message_contains 'Description cannot be an empty string'
  new_rest_api
  aws_ok apigateway create-resource --rest-api-id "$API_ID" --parent-id "$ROOT_ID" --path-part pets
  aws_error ConflictException 409 apigateway create-resource --rest-api-id "$API_ID" --parent-id "$ROOT_ID" --path-part pets
  aws_error_message_contains 'Another resource with the same parent already has this name: pets'
  aws_ok apigateway create-resource --rest-api-id "$API_ID" --parent-id "$ROOT_ID" --path-part '{proxy+}'
  local greedy
  greedy=$(jqv '.id')
  aws_error BadRequestException 400 apigateway create-resource --rest-api-id "$API_ID" --parent-id "$greedy" --path-part child
  aws_error_message_contains 'Cannot create a child of a resource with a greedy path variable: {proxy+}'
  aws_ok apigateway put-method --rest-api-id "$API_ID" --resource-id "$ROOT_ID" --http-method GET --authorization-type NONE
  aws_error ValidationException 400 apigateway put-integration --rest-api-id "$API_ID" --resource-id "$ROOT_ID" \
    --http-method GET --type HTTPS_PROXY
}

# A MOCK method with a literal request template selects its status and a
# literal response template is the body (apigateway-rest.md 6.3); an
# unknown path or method is 403 Missing Authentication Token (finding 3); a
# deployment a stage points at cannot be deleted (finding 12).
test_mock_method_and_deployments() {
  new_rest_api
  aws_ok apigateway create-resource --rest-api-id "$API_ID" --parent-id "$ROOT_ID" --path-part items
  local items dep
  items=$(jqv '.id')
  aws_ok apigateway put-method --rest-api-id "$API_ID" --resource-id "$items" --http-method GET --authorization-type NONE
  aws_ok apigateway put-method-response --rest-api-id "$API_ID" --resource-id "$items" --http-method GET --status-code 200
  aws_ok apigateway put-integration --rest-api-id "$API_ID" --resource-id "$items" --http-method GET --type MOCK \
    --request-templates '{"application/json": "{\"statusCode\": 200}"}'
  assert_jq '.passthroughBehavior' WHEN_NO_MATCH
  assert_jq '.timeoutInMillis' 29000
  assert_jq '.httpMethod // "absent"' absent
  aws_ok apigateway put-integration-response --rest-api-id "$API_ID" --resource-id "$items" --http-method GET \
    --status-code 200 --response-templates '{"application/json": "{\"mock\": true}"}'
  aws_ok apigateway create-deployment --rest-api-id "$API_ID" --stage-name s
  dep=$(jqv '.id')
  aws_ok apigateway get-stages --rest-api-id "$API_ID"
  assert_jq '[.item[] | "\(.stageName) \(.deploymentId) \(.cacheClusterStatus) \(.tracingEnabled)"] | join(",")' \
    "s $dep NOT_AVAILABLE false"

  fetch GET /s/items
  assert_eq 200 "$HTTP_STATUS" "GET /s/items status ($(cat "$HTTP_BODY"))"
  assert_eq '{"mock":true}' "$(jq -c . "$HTTP_BODY")" "GET /s/items body"
  fetch GET /s/nothing
  assert_eq 403 "$HTTP_STATUS" "GET /s/nothing status"
  assert_eq '{"message":"Missing Authentication Token"}' "$(jq -c . "$HTTP_BODY")" "GET /s/nothing body"
  grep -qi '^x-amzn-errortype: MissingAuthenticationTokenException' "$HTTP_HEADERS" ||
    fail "no x-amzn-ErrorType MissingAuthenticationTokenException: $(cat "$HTTP_HEADERS")"
  fetch POST /s/items
  assert_eq 403 "$HTTP_STATUS" "POST /s/items status"

  aws_error BadRequestException 400 apigateway delete-deployment --rest-api-id "$API_ID" --deployment-id "$dep"
  aws_error_message_contains 'Active stages pointing to this deployment must be moved or deleted'
  aws_ok apigateway delete-stage --rest-api-id "$API_ID" --stage-name s
  aws_ok apigateway delete-deployment --rest-api-id "$API_ID" --deployment-id "$dep"
}

# Tags on the REST API's ARN, a path label the CLI percent-encodes
# (apigateway-rest.md 3.9, 3.10); tags given at create are GetRestApi's.
test_tags_take_the_escaped_arn() {
  new_rest_api --tags team=a
  local arn="arn:aws:apigateway:${AWS_REGION}::/restapis/${API_ID}"
  aws_ok apigateway tag-resource --resource-arn "$arn" --tags env=dev
  aws_ok apigateway get-tags --resource-arn "$arn"
  assert_jq '.tags | to_entries | sort_by(.key) | map("\(.key)=\(.value)") | join(",")' 'env=dev,team=a'
  aws_ok apigateway untag-resource --resource-arn "$arn" --tag-keys team
  aws_ok apigateway get-rest-api --rest-api-id "$API_ID"
  assert_jq '.tags | to_entries | map("\(.key)=\(.value)") | join(",")' 'env=dev'
}

# A valid request understudy does not emulate.
test_import_rest_api_is_not_implemented() {
  local spec="$UE_TMP/apigw1-openapi.json"
  printf '%s' '{"openapi":"3.0.1","info":{"title":"t","version":"1"},"paths":{}}' >"$spec"
  aws_error NotImplemented 501 apigateway import-rest-api --body "fileb://$spec"
}

# Answers an API Gateway Lambda proxy request with what it saw in the REST
# 1.0 event (apigateway-rest.md 5.1), as a proxy response (5.2).
APIGW1_SOURCE='import json


def handler(event, context):
    ctx = event["requestContext"]
    return {
        "statusCode": 201,
        "headers": {"content-type": "application/json", "x-conformance": "yes"},
        "body": json.dumps({
            "resource": event["resource"],
            "path": event["path"],
            "method": event["httpMethod"],
            "stage": ctx["stage"],
            "id": event["pathParameters"]["id"],
        }),
    }
'

# An AWS_PROXY method on /items/{id} reaches a real function through the
# path fallback, with the permission "${execution_arn}/*/*" for
# apigateway.amazonaws.com (apigateway-rest.md 5.4), and its proxy response
# is the answer (5.2).
test_invokes_a_lambda_proxy_method() {
  require_docker
  local fn role zip fn_arn acct items item uri
  fn=$(new_name apigw1-fn)
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  role=$(make_role "$(new_name apigw1-role)")
  zip="$UE_TMP/$fn.zip"
  make_zip "$zip" index.py "$APIGW1_SOURCE"
  aws_ok lambda create-function --function-name "$fn" --runtime python3.13 --handler index.handler \
    --role "$role" --zip-file "fileb://$zip"
  fn_arn=$(jqv '.FunctionArn')
  wait_active "$fn"

  new_rest_api
  aws_ok apigateway create-resource --rest-api-id "$API_ID" --parent-id "$ROOT_ID" --path-part items
  items=$(jqv '.id')
  aws_ok apigateway create-resource --rest-api-id "$API_ID" --parent-id "$items" --path-part '{id}'
  item=$(jqv '.id')
  aws_ok apigateway put-method --rest-api-id "$API_ID" --resource-id "$item" --http-method GET --authorization-type NONE
  uri="arn:aws:apigateway:${AWS_REGION}:lambda:path/2015-03-31/functions/${fn_arn}/invocations"
  aws_ok apigateway put-integration --rest-api-id "$API_ID" --resource-id "$item" --http-method GET \
    --type AWS_PROXY --integration-http-method POST --uri "$uri"
  aws_ok lambda add-permission --function-name "$fn" --statement-id apigateway --action lambda:InvokeFunction \
    --principal apigateway.amazonaws.com --source-arn "arn:aws:execute-api:${AWS_REGION}:${acct}:${API_ID}/*/*"
  aws_ok apigateway create-deployment --rest-api-id "$API_ID" --stage-name live

  fetch GET /live/items/42
  assert_eq 201 "$HTTP_STATUS" "GET /live/items/42 status ($(cat "$HTTP_BODY"))"
  grep -qi '^x-conformance: yes' "$HTTP_HEADERS" || fail "no x-conformance header from the function: $(cat "$HTTP_HEADERS")"
  grep -qi '^x-amzn-requestid: .' "$HTTP_HEADERS" || fail "no x-amzn-RequestId header: $(cat "$HTTP_HEADERS")"
  assert_eq '{"resource":"/items/{id}","path":"/items/42","method":"GET","stage":"live","id":"42"}' \
    "$(jq -c . "$HTTP_BODY")" "the function's view"
}
