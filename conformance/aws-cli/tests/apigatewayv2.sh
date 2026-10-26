#!/usr/bin/env bash
# shellcheck disable=SC2016 # API Gateway's $request and $default are literals
# API Gateway HTTP APIs: the apigatewayv2 control plane and Lambda proxy
# invocations through the execute-api path fallback.
# Arbiter: docs/research/apigateway-http.md (sections 2 to 5, 9 and 11).
#
# API Gateway v2's error texts are UNVERIFIED (apigateway-http.md 2.3), so
# each error check is its code and status. The invocation tests need Docker
# and the Python 3.13 Lambda base image; they skip when the emulator reports
# Docker as not enabled.

# shellcheck source=../lambda-helpers.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lambda-helpers.sh"

# new_api: create an HTTP API (id in API_ID) and leave the response in AWS_OUT.
new_api() {
  aws_ok apigatewayv2 create-api --name "$(new_name apigw)" --protocol-type HTTP "$@"
  API_ID=$(jqv '.ApiId')
}

# Create defaults (apigateway-http.md 3.1, create-api.rst L13-L19); the
# endpoint names the emulator, never amazonaws.com (11.3).
test_create_api_defaults() {
  new_api
  assert_jq '.ApiKeySelectionExpression' '$request.header.x-api-key'
  assert_jq '.RouteSelectionExpression' '$request.method $request.path'
  assert_jq '.ProtocolType' HTTP
  assert_jq_match '.ApiEndpoint' "^https?://${API_ID}\\.execute-api\\.${AWS_REGION}\\.[^/]+\$"
  assert_not_contains amazonaws.com "$(jqv '.ApiEndpoint')" ApiEndpoint
  aws_ok apigatewayv2 get-api --api-id "$API_ID"
  assert_jq '.ApiId' "$API_ID"
  aws_ok apigatewayv2 update-api --api-id "$API_ID" --description updated
  aws_ok apigatewayv2 get-apis
  assert_jq_true "[.Items[] | select(.ApiId == \"$API_ID\" and .Description == \"updated\")] | length == 1"
  aws_ok apigatewayv2 delete-api --api-id "$API_ID"
  aws_error NotFoundException 404 apigatewayv2 get-api --api-id "$API_ID"
}

# The ARN is a path label the CLI percent-encodes (apigateway-http.md 2.2);
# tags given at create are GetApi's (3.6).
test_tags_take_the_escaped_arn() {
  new_api --tags team=a
  local arn="arn:aws:apigateway:${AWS_REGION}::/apis/${API_ID}"
  aws_ok apigatewayv2 tag-resource --resource-arn "$arn" --tags env=dev
  aws_ok apigatewayv2 get-tags --resource-arn "$arn"
  assert_jq '.Tags | to_entries | sort_by(.key) | map("\(.key)=\(.value)") | join(",")' 'env=dev,team=a'
  aws_ok apigatewayv2 untag-resource --resource-arn "$arn" --tag-keys team
  aws_ok apigatewayv2 get-api --api-id "$API_ID"
  assert_jq '.Tags | to_entries | map("\(.key)=\(.value)") | join(",")' 'env=dev'
}

# Lambda proxy defaults (apigateway-http.md 3.3), route defaults (3.2), an
# automatic deployment (3.4, 3.5), a manual one to a stage, and the errors:
# a duplicate route key, a timeout outside 50..30000 (2.4), a missing route.
test_routes_integrations_stages_and_deployments() {
  new_api
  aws_ok sts get-caller-identity
  local acct target dep fn_arn
  acct=$(jqv '.Account')
  fn_arn="arn:aws:lambda:${AWS_REGION}:${acct}:function:$(new_name apigw-target)"
  aws_ok apigatewayv2 create-integration --api-id "$API_ID" --integration-type AWS_PROXY \
    --integration-uri "$fn_arn" --payload-format-version 2.0
  assert_jq '.IntegrationMethod' POST
  assert_jq '.ConnectionType' INTERNET
  assert_jq '.TimeoutInMillis' 30000
  assert_jq '.IntegrationUri' "$fn_arn"
  target="integrations/$(jqv '.IntegrationId')"
  aws_ok apigatewayv2 create-route --api-id "$API_ID" --route-key 'GET /pets/{id}' --target "$target"
  assert_jq '.ApiKeyRequired' false
  assert_jq '.AuthorizationType' NONE
  assert_jq '.Target' "$target"

  aws_error ConflictException 409 apigatewayv2 create-route --api-id "$API_ID" --route-key 'GET /pets/{id}'
  aws_error NotFoundException 404 apigatewayv2 get-route --api-id "$API_ID" --route-id zzzzzzz
  aws_error BadRequestException 400 apigatewayv2 update-integration --api-id "$API_ID" \
    --integration-id "${target#integrations/}" --timeout-in-millis 30001

  aws_ok apigatewayv2 create-stage --api-id "$API_ID" --stage-name '$default' --auto-deploy
  dep=$(jqv '.DeploymentId')
  assert_ne "" "$dep" DeploymentId
  assert_jq '.LastDeploymentStatusMessage' "Successfully deployed stage with deployment ID '${dep}'"
  aws_ok apigatewayv2 get-deployments --api-id "$API_ID"
  assert_jq '[.Items[] | "\(.DeploymentId) \(.AutoDeployed) \(.DeploymentStatus)"] | join(",")' "${dep} true DEPLOYED"

  aws_ok apigatewayv2 create-stage --api-id "$API_ID" --stage-name prod
  aws_ok apigatewayv2 create-deployment --api-id "$API_ID" --stage-name prod --description v1
  assert_jq '.AutoDeployed' false
  assert_jq '.DeploymentStatus' DEPLOYED
  dep=$(jqv '.DeploymentId')
  aws_ok apigatewayv2 get-stage --api-id "$API_ID" --stage-name prod
  assert_jq '.DeploymentId' "$dep"
}

# A valid request understudy does not emulate (the unit's conflict row 4).
test_websocket_api_is_not_implemented() {
  aws_error NotImplemented 501 apigatewayv2 create-api --name "$(new_name apigw-ws)" --protocol-type WEBSOCKET \
    --route-selection-expression '$request.body.action'
}

# Answers an API Gateway Lambda proxy request with what it saw, in payload
# format 1.0 or 2.0 (apigateway-http.md 5.1, 5.2), as a structured response
# both formats read (5.3, 5.4).
APIGW_SOURCE='import json


def handler(event, context):
    ctx = event.get("requestContext", {})
    method = event.get("httpMethod") or ctx.get("http", {}).get("method")
    return {
        "statusCode": 200,
        "headers": {"content-type": "application/json", "x-conformance": "yes"},
        "body": json.dumps({"version": event.get("version"), "method": method, "stage": ctx.get("stage")}),
    }
'

# invoke_through_api VERSION: a Lambda proxy route of the $default stage in
# payload format VERSION, reached through the path fallback
# /_understudy/execute-api/<api-id>/ (apigateway-http.md 11.3), with the
# permission "${execution_arn}/*/*" for apigateway.amazonaws.com (5.5, 9.6).
invoke_through_api() {
  require_docker
  local version=$1 fn role zip fn_arn acct body headers status
  fn=$(new_name apigw-fn)
  aws_ok sts get-caller-identity
  acct=$(jqv '.Account')
  role=$(make_role "$(new_name apigw-role)")
  zip="$UE_TMP/$fn.zip"
  make_zip "$zip" index.py "$APIGW_SOURCE"
  aws_ok lambda create-function --function-name "$fn" --runtime python3.13 --handler index.handler \
    --role "$role" --zip-file "fileb://$zip"
  fn_arn=$(jqv '.FunctionArn')
  wait_active "$fn"

  new_api
  aws_ok apigatewayv2 create-integration --api-id "$API_ID" --integration-type AWS_PROXY \
    --integration-uri "$fn_arn" --payload-format-version "$version"
  aws_ok apigatewayv2 create-route --api-id "$API_ID" --route-key 'GET /hello' --target "integrations/$(jqv '.IntegrationId')"
  aws_ok apigatewayv2 create-stage --api-id "$API_ID" --stage-name '$default' --auto-deploy
  aws_ok lambda add-permission --function-name "$fn" --statement-id apigateway --action lambda:InvokeFunction \
    --principal apigateway.amazonaws.com --source-arn "arn:aws:execute-api:${AWS_REGION}:${acct}:${API_ID}/*/*"

  body="$UE_TMP/apigw-body.$version"
  headers="$UE_TMP/apigw-headers.$version"
  status=$(curl -sS -o "$body" -D "$headers" -w '%{http_code}' "${AWS_ENDPOINT_URL}/_understudy/execute-api/${API_ID}/hello")
  assert_eq 200 "$status" "GET /hello status ($(cat "$body"))"
  grep -qi '^x-conformance: yes' "$headers" || fail "no x-conformance header from the function: $(cat "$headers")"
  grep -qi '^apigw-requestid: .' "$headers" || fail "no apigw-requestid header: $(cat "$headers")"
  assert_eq "{\"version\":\"$version\",\"method\":\"GET\",\"stage\":\"\$default\"}" "$(jq -c . "$body")" "the function's view"
}

test_invokes_a_payload_1_0_integration() {
  invoke_through_api 1.0
}

test_invokes_a_payload_2_0_integration() {
  invoke_through_api 2.0
}
