# shellcheck shell=bash
# The kinesis-lambda-terraform pattern's documented test, sourced by
# conformance/patterns/run.sh, which defines pattern_aws.
#
#   pattern_check DIR    README, Testing: send records of the Sensor1
#                        template to sample-stream, then find each record's
#                        payload in the function's CloudWatch Logs group. The
#                        README sends them from the hosted Kinesis Data
#                        Generator, which writes to real AWS, so this puts
#                        them with `aws kinesis put-records` (ADJUSTMENTS.md).
#                        The mapping starts at LATEST, so the records are
#                        written after the apply returned
#                        (docs/research/lambda-stream-sources.md 3.2); the
#                        function decodes each record's base64 data
#                        (docs/research/kinesis.md 8.1) and logs it as
#                        "Record payload: <json>".

pattern_check() {
  local group="/aws/lambda/sample-lambda" events marker entries="" i
  marker=$((RANDOM * 32768 + RANDOM))
  for i in 1 2 3; do
    local payload data
    payload=$(jq -cn --argjson id "$((marker + i))" --argjson t "$((10 + i))" '{sensorId: $id, currentTemperature: $t, status: "OK"}')
    data=$(printf '%s' "$payload" | base64 -w0)
    entries+="Data=$data,PartitionKey=sensor-$i "
  done
  # shellcheck disable=SC2086 # one word per entry
  pattern_aws kinesis put-records --stream-name sample-stream --cli-binary-format base64 --records $entries >/dev/null

  for _ in $(seq 1 60); do
    if events=$(pattern_aws logs filter-log-events --log-group-name "$group" --filter-pattern '"Record payload"' --output json 2>/dev/null) &&
      jq -e --argjson m "$marker" '
        [.events[].message | split("\t") | last | rtrimstr("\n") | ltrimstr("Record payload: ")
          | (try fromjson catch null) | select(type == "object") | .sensorId
          | select(. > $m and . <= $m + 3)] | sort == [$m + 1, $m + 2, $m + 3]' <<<"$events" >/dev/null; then
      return 0
    fi
    sleep 1
  done
  echo "the three Sensor1 records (sensorId $((marker + 1)) to $((marker + 3))) never all reached $group after 60s; its events: $(head -c 2000 <<<"${events:-none}")"
  return 1
}

# pattern_expected_denials prints the denials AWS makes too, for the
# no-denials step under CONFORMANCE_IAM_ENFORCE=enforce.
pattern_expected_denials() {
  # aws_lambda_event_source_mapping does not depend on
  # aws_iam_role_policy_attachment (main.tf), so Terraform can create the
  # mapping before the role holds its Kinesis policy. CreateEventSourceMapping
  # then checks the execution role's access to the stream and AWS refuses it
  # ("execution role does not have permissions"), which the provider retries
  # until the attachment lands (iam-enforcement-surfaces.md 4.3, tf-compute.md 2.3).
  printf '%s\n' 'for arn:aws:sts::[0-9]{12}:assumed-role/lambda-role/[^ ]+ calling kinesis:DescribeStreamSummary on arn:aws:kinesis:[^ ]+:stream/sample-stream$'
}
