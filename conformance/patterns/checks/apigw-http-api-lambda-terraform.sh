# shellcheck shell=bash
# The apigw-http-api-lambda-terraform pattern's documented steps, sourced by
# conformance/patterns/run.sh, which defines pattern_aws and endpoint.
#
#   pattern_check DIR    README, Testing: a GET of the API answers the
#                        function's message for GET / with no query, and a
#                        POST of /pets/dog/1?foo=bar with a JSON body answers
#                        it with the path, the query and the body. The README
#                        calls the apigwy_url output, the API's execute-api
#                        host on amazonaws.com; the check calls the same API
#                        through understudy's path form,
#                        <endpoint>/_understudy/execute-api/<api-id>/<path>
#                        (docs/adr/0020-understudy-namespace.md). The
#                        integration has no payload_format_version, so the
#                        provider sends 1.0 and the function's answer is a 1.0
#                        response (docs/research/apigateway-http.md 5.2, 5.4).

# pattern_call API_ID METHOD PATH [CURL ARGS...] prints the body and, on the
# last line, the status of one call to the API through the path form.
pattern_call() {
  local api_id=$1 method=$2 path=$3
  shift 3
  # shellcheck disable=SC2154 # endpoint is set by run.sh
  curl -sS -X "$method" -w '\n%{http_code}' "$@" "$endpoint/_understudy/execute-api/$api_id$path"
}

pattern_check() {
  local api_id out status body want
  api_id=$(pattern_aws apigatewayv2 get-apis --query "Items[?Name=='apigw-http-lambda'].ApiId" --output text)
  if [[ ! "$api_id" =~ ^[a-z0-9]{10}$ ]]; then
    echo "get-apis found no API named apigw-http-lambda: '$api_id'"
    return 1
  fi

  out=$(pattern_call "$api_id" GET /)
  status=${out##*$'\n'}
  body=${out%$'\n'*}
  want='{"functionName":"test_apigw_integration","method":"GET","rawPath":"/","queryString":null}'
  if [[ "$status" != 200 ]] || ! jq -e --argjson want "$want" \
    '.["message "] | {functionName, method, rawPath, queryString} == $want and (.xForwardedFor | type == "string") and (.timestamp | type == "string")' \
    <<<"$body" >/dev/null 2>&1; then
    echo "GET / answered $status $body, want 200 and a message with $want"
    return 1
  fi

  out=$(pattern_call "$api_id" POST'/pets/dog/1?foo=bar' --header 'Content-Type: application/json' -d '{"key1":"hello", "key2":"World!"}')
  status=${out##*$'\n'}
  body=${out%$'\n'*}
  want='{"functionName":"test_apigw_integration","method":"POST","rawPath":"/pets/dog/1","queryString":{"foo":"bar"},"body":"{\"key1\":\"hello\", \"key2\":\"World!\"}"}'
  if [[ "$status" != 200 ]] || ! jq -e --argjson want "$want" \
    '.["message "] | {functionName, method, rawPath, queryString, body} == $want' \
    <<<"$body" >/dev/null 2>&1; then
    echo "POST /pets/dog/1?foo=bar answered $status $body, want 200 and a message with $want"
    return 1
  fi
}
