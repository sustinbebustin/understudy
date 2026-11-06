# shellcheck shell=bash
# The lambda-ssm-parameter-terraform pattern's documented test, sourced by
# conformance/patterns/run.sh, which defines pattern_aws.
#
#   pattern_check DIR    README, Testing: invoke SSMParameterFunction with the
#                        GET event, which answers the parameter as created
#                        (value {"key1":"value1"}, version 1); with the PUT
#                        event, which answers {"Version":2,"Tier":"Standard"};
#                        and with the GET event again, which answers the PUT
#                        event's body as the value, version 2. The function
#                        calls GetParameter and PutParameter with Overwrite
#                        (src/app.js; docs/research/ssm-parameters.md 6.1,
#                        10.4).

# pattern_invoke DIR EVENT prints the function's answer to events/EVENT.json,
# or prints why the invocation failed and returns non-zero.
pattern_invoke() {
  local dir=$1 event=$2 out
  out=$(mktemp "$dir/invoke.XXXXXX")
  pattern_aws lambda invoke --function-name SSMParameterFunction --cli-binary-format raw-in-base64-out \
    --payload "fileb://$dir/events/$event.json" "$out" >"$out.meta"
  if [[ "$(jq -r '.FunctionError // empty' "$out.meta")" != "" ]]; then
    echo "the $event event failed: $(cat "$out")"
    return 1
  fi
  cat "$out"
}

# pattern_expect EVENT ANSWER FILTER [WANT] succeeds when the function
# answered statusCode 200 and FILTER, which may name WANT as $want, holds for
# its body, parsed; otherwise it prints the answer and returns non-zero.
pattern_expect() {
  local event=$1 answer=$2 filter=$3 want=${4:-}
  if ! jq -e --arg want "$want" ".statusCode == 200 and (.body | fromjson | $filter)" <<<"$answer" >/dev/null 2>&1; then
    echo "the $event event answered $answer, want statusCode 200 and a body where $filter"
    return 1
  fi
}

# shellcheck disable=SC2016 # $want and $metadata belong to jq, not the shell
pattern_check() {
  # created is main.tf's aws_ssm_parameter value.
  local dir=$1 answer put_value created='{"key1":"value1"}'
  put_value=$(jq -r .body "$dir/events/putEvent.json")

  answer=$(pattern_invoke "$dir" getEvent) || {
    echo "$answer"
    return 1
  }
  pattern_expect getEvent "$answer" \
    '.Parameter | .Name == "ExampleParameterName" and .Type == "String" and .Value == $want and .Version == 1' "$created" ||
    return 1

  answer=$(pattern_invoke "$dir" putEvent) || {
    echo "$answer"
    return 1
  }
  # The JavaScript SDK adds its own $metadata to every output it returns.
  pattern_expect putEvent "$answer" 'del(.["$metadata"]) == {"Version": 2, "Tier": "Standard"}' || return 1

  answer=$(pattern_invoke "$dir" getEvent) || {
    echo "$answer"
    return 1
  }
  pattern_expect getEvent "$answer" '.Parameter | .Value == $want and .Version == 2' "$put_value" ||
    return 1

  # Not in the README: put the configured value back, so the plan that
  # follows measures understudy rather than the PUT event's drift. On AWS
  # that drift would plan a change too.
  pattern_aws ssm put-parameter --name ExampleParameterName --type String --overwrite \
    --value "$created" >/dev/null
}
