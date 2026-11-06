# shellcheck shell=bash
# The eventbridge-scheduler-ses-abandoned-cart-notification pattern's
# documented steps, sourced by conformance/patterns/run.sh, which defines
# pattern_aws, binary, endpoint, accounts and current_pattern.
#
#   pattern_prepare DIR  README, Prerequisites: verify the sender and, in the
#                        sandbox, the seed record's recipient
#                        recipient@example.com with `ses
#                        verify-email-identity`; both are in
#                        ses.verifiedIdentities of
#                        conformance/patterns/understudy.json, so they verify
#                        at once (docs/research/ses.md 6.1). Then
#                        Deployment Instructions: the README's -var answers
#                        (region us-east-1, prefix cartnotify, the sender's
#                        identity ARN in the pattern's account) written as
#                        terraform.tfvars, since the suite runs Terraform
#                        without input.
#   pattern_check DIR    How it works: the schedule (`rate(1 hour)`) invokes
#                        the function, which emails cust-001, the one seed
#                        record with an abandoned cart not yet notified, and
#                        marks it notified. The check moves the emulator
#                        clock 1 hour and settles, then wants exactly one
#                        SendEmail from the sender to recipient@example.com
#                        with the function's subject, cust-001's
#                        NotificationSent "true" (README, Testing 3), and
#                        the dead-letter queue empty. Whether a fire comes at
#                        the write instant too is not asserted: the second
#                        run finds cust-001 notified and sends nothing
#                        (docs/research/scheduler.md 14.3.4). It then puts
#                        cust-001 back as main.tf seeds it, so
#                        plan-no-changes measures understudy.
#   pattern_cleanup DIR  Deletes the two identities the preparation created,
#                        which Terraform does not manage.

eventbridge_ses_sender=noreply@eventbridge-scheduler-ses.example.org
eventbridge_ses_recipient=recipient@example.com

pattern_prepare() {
  local dir=$1 id status
  for id in "$eventbridge_ses_sender" "$eventbridge_ses_recipient"; do
    pattern_aws ses verify-email-identity --email-address "$id"
    status=$(pattern_aws ses get-identity-verification-attributes --identities "$id" |
      jq -r --arg id "$id" '.VerificationAttributes[$id].VerificationStatus')
    if [[ "$status" != "Success" ]]; then
      echo "the identity $id is $status, want Success: is it in ses.verifiedIdentities of conformance/patterns/understudy.json?"
      return 1
    fi
  done
  # shellcheck disable=SC2154 # accounts and current_pattern are set by run.sh
  printf 'aws_region = "us-east-1"\nprefix = "cartnotify"\nses_identity_arn = "arn:aws:ses:us-east-1:%s:identity/%s"\nsender_email = "%s"\n' \
    "${accounts[$current_pattern]}" "$eventbridge_ses_sender" "$eventbridge_ses_sender" >"$dir/terraform.tfvars"
}

pattern_check() {
  local dir=$1 sends notified dlq visible
  # The first invocation starts the function's container; give the settle
  # up to 5 minutes for it.
  # shellcheck disable=SC2154 # binary and endpoint are set by run.sh
  "$binary" clock advance --endpoint "$endpoint" --settle --settle-timeout 5m 1h || {
    echo "understudy clock advance 1h --settle failed"
    return 1
  }

  sends=$("$binary" ses messages --endpoint "$endpoint" --account "${accounts[$current_pattern]}" --output json |
    jq '[.messages[] | select(.kind == "send" and .operation == "SendEmail")]')
  if [[ "$(jq length <<<"$sends")" != 1 ]]; then
    echo "understudy recorded $(jq length <<<"$sends") SendEmail messages, want 1; the function's log group /aws/lambda/cartnotify-notification-processor says why: $(jq -c '[.[] | {from, envelope}]' <<<"$sends")"
    return 1
  fi
  if ! jq -e --arg from "$eventbridge_ses_sender" --arg to "$eventbridge_ses_recipient" '
      .[0] | .from == [$from] and .envelope == [$to]
        and .subject.data == "Jane Doe, you left something in your cart!"' <<<"$sends" >/dev/null; then
    echo "the message is not from $eventbridge_ses_sender to $eventbridge_ses_recipient with cust-001's subject: $(jq -c '.[0] | {from, envelope, subject}' <<<"$sends")"
    return 1
  fi

  notified=$(pattern_aws dynamodb get-item --table-name cartnotify-abandoned-carts \
    --key '{"CustomerId": {"S": "cust-001"}}' | jq -r '.Item.NotificationSent.S')
  if [[ "$notified" != "true" ]]; then
    echo "cust-001's NotificationSent is $notified, want true"
    return 1
  fi

  dlq=$(pattern_aws sqs get-queue-url --queue-name cartnotify-cart-notify-dlq | jq -r .QueueUrl)
  visible=$(pattern_aws sqs get-queue-attributes --queue-url "$dlq" \
    --attribute-names ApproximateNumberOfMessages ApproximateNumberOfMessagesNotVisible |
    jq -r '.Attributes | (.ApproximateNumberOfMessages | tonumber) + (.ApproximateNumberOfMessagesNotVisible | tonumber)')
  if [[ "$visible" != 0 ]]; then
    echo "the dead-letter queue holds $visible messages, want 0: $(pattern_aws sqs receive-message --queue-url "$dlq" --max-number-of-messages 10 --message-attribute-names All | head -c 2000)"
    return 1
  fi

  # Not in the README: put cust-001 back as main.tf seeds it, so the plan
  # that follows measures understudy rather than the function's own write
  # (NotificationSent "true" and NotifiedAt). On AWS that write would plan
  # a change too.
  local item
  # shellcheck disable=SC2154 # sealed is set by run.sh
  item=$("$sealed" --endpoint "$endpoint" --access-key-id "${accounts[$current_pattern]}" terraform -chdir="$dir" show -json |
    jq -r '.values.root_module.resources[] | select(.address == "aws_dynamodb_table_item.test_user_abandoned") | .values.item')
  pattern_aws dynamodb put-item --table-name cartnotify-abandoned-carts --item "$item" >/dev/null
}

pattern_cleanup() {
  pattern_aws ses delete-identity --identity "$eventbridge_ses_sender"
  pattern_aws ses delete-identity --identity "$eventbridge_ses_recipient"
}
