# shellcheck shell=bash
# The eventbridge-schedule-to-lambda-terraform-go pattern's documented steps,
# sourced by conformance/patterns/run.sh, which defines pattern_aws, binary
# and endpoint.
#
#   pattern_prepare DIR  README, Deployment Instructions 3-5: in src/, build
#                        the handler with `GOOS=linux GOARCH=amd64 go build
#                        -o bootstrap main.go`. The README's `go get` is left
#                        out: go.mod and go.sum pin aws-lambda-go v1.40.0,
#                        which the build fetches from the Go module proxy,
#                        and `go get` could move that pin. Its `zip` step is
#                        left out too: main.tf zips src/bootstrap itself
#                        (data "archive_file") and never reads the README's
#                        myFunction.zip.
#   pattern_check DIR    README, Testing: the function's execution logs show
#                        the schedule invoking it. The schedule is
#                        `rate(5 minute)`, so the check moves the emulator
#                        clock 5 minutes, settles, and wants at least one
#                        line `Received event: {"input":"This message was
#                        sent using EventBridge Scheduler!"}` in
#                        /aws/lambda/EventBridgeScheduleTarget: the target's
#                        Input, which src/main.go prints as JSON. Whether a
#                        line already exists at the write instant is not
#                        asserted (docs/research/scheduler.md 14.3.4, open
#                        until SCH-09 is recorded).

eventbridge_schedule_group=/aws/lambda/EventBridgeScheduleTarget
eventbridge_schedule_line='Received event: {"input":"This message was sent using EventBridge Scheduler!"}'

pattern_prepare() {
  local dir=$1
  (cd -- "$dir/src" && GOOS=linux GOARCH=amd64 go build -o bootstrap main.go)
}

pattern_check() {
  local events=""
  # The invocation's first run starts the function's container; give the
  # settle up to 5 minutes for it.
  # shellcheck disable=SC2154 # binary and endpoint are set by run.sh
  "$binary" clock advance --endpoint "$endpoint" --settle --settle-timeout 5m 5m || {
    echo "understudy clock advance 5m --settle failed"
    return 1
  }
  for _ in $(seq 1 30); do
    if events=$(pattern_aws logs filter-log-events --log-group-name "$eventbridge_schedule_group" --output json 2>/dev/null) &&
      jq -e --arg line "$eventbridge_schedule_line" \
        '[.events[].message | rtrimstr("\n") | select(. == $line)] | length >= 1' <<<"$events" >/dev/null; then
      return 0
    fi
    sleep 1
  done
  echo "no line '$eventbridge_schedule_line' in $eventbridge_schedule_group after advancing 5 minutes; its events: $(head -c 2000 <<<"${events:-none}")"
  return 1
}

# No pattern_expected_denials: AWS makes no denial here. The function's
# execution role (main.tf PublishLogsPolicy) grants logs:* only on
# log-group:/aws/lambda/EventBridgeScheduleTarget:*, and a statement on the
# :* form allows logs:CreateLogGroup on the group as well as the stream
# actions: AWS created a function's log group under such a role
# (testdata/parity/logs/IAM-ENF/42-fn-log-groups.json; IAM-ENF-42,
# secrets-logs-events-alarms.md 3.10).
