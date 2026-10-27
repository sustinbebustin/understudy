#!/usr/bin/env bash
# SNS delivery to an http endpoint through the AWS CLI: confirmation by token
# and by SubscribeURL, notifications, and the UnsubscribeURL with its
# UnsubscribeConfirmation. run.sh lists 127.0.0.1:* in sns.httpDelivery.allow,
# so understudy delivers to the receiver each test starts.
# Arbiter: docs/research/sns-http-delivery.md.

# mktopic NAME: sets TOPIC_ARN.
mktopic() {
  aws_ok sns create-topic --name "$1"
  TOPIC_ARN=$(jqv '.TopicArn')
}

# start_receiver: start an HTTP receiver on a free loopback port that answers
# each POST 200 and appends it to RECEIVER_LOG as one JSON line
# {"type", "headers", "body"} (header names in lower case); sets RECEIVER_URL
# and stops the receiver when the test's shell exits.
start_receiver() {
  RECEIVER_LOG="$UE_TMP/sns-http.$$.jsonl"
  local portfile="$UE_TMP/sns-http.$$.port"
  : >"$RECEIVER_LOG"
  python3 -I - "$RECEIVER_LOG" "$portfile" <<'PY' &
import http.server, json, sys

log, portfile = sys.argv[1], sys.argv[2]

class Receiver(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0"))).decode()
        with open(log, "a") as f:
            f.write(json.dumps({"type": self.headers.get("x-amz-sns-message-type", ""),
                                "headers": {k.lower(): v for k, v in self.headers.items()}, "body": body}) + "\n")
        self.send_response(200)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def log_message(self, *args):
        pass

server = http.server.HTTPServer(("127.0.0.1", 0), Receiver)
with open(portfile, "w") as f:
    f.write(str(server.server_address[1]))
server.serve_forever()
PY
  RECEIVER_PID=$!
  trap 'kill "$RECEIVER_PID" 2>/dev/null || true' EXIT
  for _ in $(seq 1 100); do
    [ -s "$portfile" ] && break
    sleep 0.05
  done
  [ -s "$portfile" ] || fail "the receiver did not start"
  RECEIVER_URL="http://127.0.0.1:$(cat "$portfile")/hook"
}

# received TYPE: wait up to 10 s for a POST of TYPE, then put the last one's
# body in AWS_OUT, for jqv and assert_jq, and its headers in RECEIVED_HEADERS.
# The body is taken whole: SNS writes it one member per line
# (sns-http-delivery.md 4.1).
received() {
  local post
  for _ in $(seq 1 100); do
    post=$(jq -cs --arg t "$1" 'map(select(.type == $t)) | last // empty' "$RECEIVER_LOG")
    if [ -n "$post" ]; then
      # shellcheck disable=SC2034 # read by jqv and assert_jq (lib.sh)
      AWS_OUT=$(jq -r '.body' <<<"$post")
      RECEIVED_HEADERS=$(jq -c '.headers' <<<"$post")
      return 0
    fi
    sleep 0.1
  done
  fail "no $1 reached the receiver in 10 s"
}

# header NAME: the value of header NAME (lower case) of the last received POST.
header() {
  jq -r --arg h "$1" '.[$h] // ""' <<<"$RECEIVED_HEADERS"
}

# no_aws_host: no POST the receiver got names an AWS host (8.8; suffixes 7.3).
no_aws_host() {
  if grep -qiE 'amazonaws\.com|amazonaws\.eu|amazonwebservices|api\.aws|\.aws[/:?"\\[:space:]]' "$RECEIVER_LOG"; then
    fail "a POST names an AWS host: $(grep -oiE '.{0,40}(amazonaws|\.aws).{0,40}' "$RECEIVER_LOG" | head -n 1)"
  fi
}

# assert_local_link URL: fail unless a received link is on loopback, so the
# suite never follows one to another host, an AWS one least of all (8.8).
assert_local_link() {
  case "$1" in
    http://127.0.0.1:* | http://localhost:* | 'http://[::1]:'*) ;;
    *) fail "link [$1] is not on loopback; not following it" ;;
  esac
}

# A subscription is pending until confirmed (2.2); ConfirmSubscription with
# the confirmation's Token confirms it (2.3); a publish then arrives as a
# Notification with the members of 4.2 and the headers of 3.2.
test_http_subscription_confirmed_by_token_receives() {
  start_receiver
  mktopic "$(new_name sns-http-token)"
  aws_ok sns subscribe --topic-arn "$TOPIC_ARN" --protocol http --notification-endpoint "$RECEIVER_URL" --return-subscription-arn
  local sub_arn
  sub_arn=$(jqv '.SubscriptionArn')
  aws_ok sns get-subscription-attributes --subscription-arn "$sub_arn"
  assert_jq '.Attributes.PendingConfirmation' 'true'

  received SubscriptionConfirmation
  local token
  token=$(jqv '.Token')
  aws_ok sns confirm-subscription --topic-arn "$TOPIC_ARN" --token "$token"
  assert_jq '.SubscriptionArn' "$sub_arn"
  aws_ok sns get-subscription-attributes --subscription-arn "$sub_arn"
  assert_jq '.Attributes.PendingConfirmation' 'false'

  aws_ok sns publish --topic-arn "$TOPIC_ARN" --subject greeting --message hello-token
  local message_id
  message_id=$(jqv '.MessageId')
  received Notification
  assert_jq '.Type' 'Notification'
  assert_jq '.MessageId' "$message_id"
  assert_jq '.TopicArn' "$TOPIC_ARN"
  assert_jq '.Subject' 'greeting'
  assert_jq '.Message' 'hello-token'
  assert_jq '.SignatureVersion' '1'
  # The UnsubscribeURL's query is AWS's (4.6), the ARN not percent-encoded;
  # its path is under understudy's /_understudy/sns/ prefix (8.3 item 3).
  local unsubscribe_url
  unsubscribe_url=$(jqv '.UnsubscribeURL')
  case "$unsubscribe_url" in
    *"?Action=Unsubscribe&SubscriptionArn=$sub_arn") ;;
    *) fail "UnsubscribeURL [$unsubscribe_url] does not end with ?Action=Unsubscribe&SubscriptionArn=$sub_arn" ;;
  esac
  assert_eq "$message_id" "$(header x-amz-sns-message-id)" x-amz-sns-message-id
  assert_eq "$TOPIC_ARN" "$(header x-amz-sns-topic-arn)" x-amz-sns-topic-arn
  assert_eq "$sub_arn" "$(header x-amz-sns-subscription-arn)" x-amz-sns-subscription-arn
  assert_eq 'text/plain; charset=UTF-8' "$(header content-type)" Content-Type
  assert_eq 'gzip,deflate' "$(header accept-encoding)" Accept-Encoding
  assert_eq '' "$(header x-amz-sns-rawdelivery)" x-amz-sns-rawdelivery
  no_aws_host
  aws_ok sns unsubscribe --subscription-arn "$sub_arn"
}

# An unsigned GET of SubscribeURL confirms (2.3); an unsigned GET of a
# notification's UnsubscribeURL answers 200 UnsubscribeResponse, and the
# endpoint then gets an UnsubscribeConfirmation (2.4, third-party-recorded)
# with a new token and a SubscribeURL to re-confirm (4.4).
test_http_subscription_confirmed_by_link_unsubscribes_by_link() {
  start_receiver
  mktopic "$(new_name sns-http-link)"
  aws_ok sns subscribe --topic-arn "$TOPIC_ARN" --protocol http --notification-endpoint "$RECEIVER_URL" --return-subscription-arn
  local sub_arn
  sub_arn=$(jqv '.SubscriptionArn')

  received SubscriptionConfirmation
  assert_jq '.Type' 'SubscriptionConfirmation'
  assert_jq '.TopicArn' "$TOPIC_ARN"
  assert_eq '' "$(header x-amz-sns-subscription-arn)" "the SubscriptionConfirmation's x-amz-sns-subscription-arn"
  local confirm_token link answer
  confirm_token=$(jqv '.Token')
  link=$(jqv '.SubscribeURL')
  assert_local_link "$link"
  answer=$(curl -fsS "$link")
  assert_contains '<ConfirmSubscriptionResponse' "$answer" "the SubscribeURL answer"
  assert_contains "<SubscriptionArn>$sub_arn</SubscriptionArn>" "$answer" "the SubscribeURL answer"
  aws_ok sns get-subscription-attributes --subscription-arn "$sub_arn"
  assert_jq '.Attributes.PendingConfirmation' 'false'

  aws_ok sns publish --topic-arn "$TOPIC_ARN" --message hello-link
  received Notification
  assert_jq '.Message' 'hello-link'
  link=$(jqv '.UnsubscribeURL')
  assert_local_link "$link"
  answer=$(curl -fsS "$link")
  assert_contains '<UnsubscribeResponse' "$answer" "the UnsubscribeURL answer"

  received UnsubscribeConfirmation
  assert_jq '.Type' 'UnsubscribeConfirmation'
  assert_jq '.TopicArn' "$TOPIC_ARN"
  assert_jq '.Message' "$(printf 'You have chosen to deactivate subscription %s.\nTo cancel this operation and restore the subscription, visit the SubscribeURL included in this message.' "$sub_arn")"
  # shellcheck disable=SC2016 # $t is jq's variable
  assert_jq_true '(.Token | length > 0) and (.Token as $t | .SubscribeURL | endswith("&Token=" + $t))'
  assert_jq_true 'has("UnsubscribeURL") | not'
  assert_ne "$confirm_token" "$(jqv '.Token')" "the UnsubscribeConfirmation's Token"
  assert_eq "$sub_arn" "$(header x-amz-sns-subscription-arn)" x-amz-sns-subscription-arn
  assert_eq "$TOPIC_ARN" "$(header x-amz-sns-topic-arn)" x-amz-sns-topic-arn
  assert_eq 'text/plain; charset=UTF-8' "$(header content-type)" Content-Type
  no_aws_host
}
