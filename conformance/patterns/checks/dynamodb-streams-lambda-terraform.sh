# shellcheck shell=bash
# The dynamodb-streams-lambda-terraform pattern's documented test, sourced by
# conformance/patterns/run.sh, which defines pattern_aws.
#
#   pattern_check DIR    README, Testing: add an item to the UsersIds table,
#                        then find the event the function logged, holding the
#                        item, in its CloudWatch Logs group. The mapping
#                        starts at LATEST, so the item is written after the
#                        apply returned (docs/research/lambda-stream-sources.md
#                        3.2), and its record is an INSERT with the item as
#                        NewImage (docs/research/dynamodb-streams.md 7.8). The
#                        function's console.log line is the event as JSON.

pattern_check() {
  local id group="/aws/lambda/process-usersids-records" events
  id="user-$RANDOM$RANDOM"
  pattern_aws dynamodb put-item --table-name UsersIds --item "{\"UserId\":{\"S\":\"$id\"}}"

  for _ in $(seq 1 60); do
    if events=$(pattern_aws logs filter-log-events --log-group-name "$group" --output json 2>/dev/null) &&
      jq -e --arg id "$id" '
        [.events[].message | (try (split("\t") | last | fromjson) catch null)
          | select(type == "object") | .Records[]?
          | select(.eventName == "INSERT" and .eventSource == "aws:dynamodb"
              and .dynamodb.NewImage.UserId.S == $id)] | length == 1' <<<"$events" >/dev/null; then
      return 0
    fi
    sleep 1
  done
  echo "no INSERT record for UserId $id in $group after 60s; its events: $(head -c 2000 <<<"${events:-none}")"
  return 1
}
