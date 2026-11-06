# shellcheck shell=bash
# The s3-lambda-ses-terraform pattern's documented steps, sourced by
# conformance/patterns/run.sh, which defines pattern_aws, binary, endpoint,
# accounts and current_pattern.
#
#   pattern_prepare DIR  README, How it works: "provide SES verified email
#                        and sender email ID values" and verify the sender.
#                        Writes terraform.tfvars (the region and both
#                        addresses: the sender, which
#                        conformance/patterns/understudy.json lists in
#                        ses.verifiedIdentities, and the mailbox simulator's
#                        success address, which the sandbox accepts,
#                        docs/research/ses.md 6.1, 6.2), verifies the sender with
#                        `ses verify-email-identity`, and builds the layer
#                        src/urlshortner.zip from the stub
#                        src/urlshortner/python/pyshorteners.py (ADJUSTMENTS.md).
#   pattern_check DIR    README, Testing: `aws s3 cp ./src/example.txt
#                        s3://<bucket>`. The function attaches the 169-byte
#                        file and calls SendRawEmail; understudy records one
#                        SendRawEmail whose MIME names the attachment
#                        example.txt.
#   pattern_cleanup DIR  Deletes the sender identity the preparation created,
#                        which Terraform does not manage.

s3_lambda_ses_sender=sender@s3-lambda-ses.example.org
s3_lambda_ses_receiver=success@simulator.amazonses.com

pattern_prepare() {
  local dir=$1
  printf 'aws_region = "us-east-1"\nsender_email = "%s"\nreceiver_email = "%s"\n' \
    "$s3_lambda_ses_sender" "$s3_lambda_ses_receiver" >"$dir/terraform.tfvars"
  (cd -- "$dir/src/urlshortner" && uv run --quiet --no-project python -m zipfile -c ../urlshortner.zip python)
  pattern_aws ses verify-email-identity --email-address "$s3_lambda_ses_sender"
  local status
  status=$(pattern_aws ses get-identity-verification-attributes --identities "$s3_lambda_ses_sender" |
    jq -r --arg id "$s3_lambda_ses_sender" '.VerificationAttributes[$id].VerificationStatus')
  if [[ "$status" != "Success" ]]; then
    echo "the sender $s3_lambda_ses_sender is $status, want Success: is it in ses.verifiedIdentities of conformance/patterns/understudy.json?"
    return 1
  fi
}

# s3_lambda_ses_sends prints the pattern account's recorded SendRawEmail
# messages as a JSON array.
s3_lambda_ses_sends() {
  # shellcheck disable=SC2154 # binary, endpoint, accounts and current_pattern are set by run.sh
  "$binary" ses messages --endpoint "$endpoint" --account "${accounts[$current_pattern]}" --output json |
    jq '[.messages[] | select(.kind == "send" and .operation == "SendRawEmail")]'
}

pattern_check() {
  local dir=$1 bucket sends=""
  bucket=$(pattern_aws s3api list-buckets | jq -r '.Buckets[0].Name // empty')
  if [[ -z "$bucket" ]]; then
    echo "the apply created no bucket"
    return 1
  fi
  (cd -- "$dir" && pattern_aws s3 cp ./src/example.txt "s3://$bucket")

  # The upload invokes the function asynchronously; give it up to 120
  # seconds, which covers a cold start of its container.
  for _ in $(seq 1 120); do
    sends=$(s3_lambda_ses_sends)
    [[ "$(jq length <<<"$sends")" != 0 ]] && break
    sleep 1
  done
  if [[ "$(jq length <<<"$sends")" != 1 ]]; then
    echo "understudy recorded $(jq length <<<"$sends") SendRawEmail messages, want 1; the function's log group /aws/lambda/LambdaSES-* says why"
    return 1
  fi
  local from to
  from=$(jq -r '.[0].from | join(",")' <<<"$sends")
  to=$(jq -r '.[0].envelope | join(",")' <<<"$sends")
  if [[ "$from" != "$s3_lambda_ses_sender" || "$to" != "$s3_lambda_ses_receiver" ]]; then
    echo "the message is from $from to $to, want from $s3_lambda_ses_sender to $s3_lambda_ses_receiver"
    return 1
  fi
  # emailSender.py (L43) names the part with Content-Disposition
  # attachment; filename="example.txt".
  if ! jq -r '.[0].raw | @base64d' <<<"$sends" | grep -q 'Content-Disposition: attachment; filename="example.txt"'; then
    echo "the message's MIME carries no attachment named example.txt: $(jq -r '.[0].raw | @base64d' <<<"$sends" | head -c 800)"
    return 1
  fi

  # The function role's least-privilege policy (GET /iam/policy) grants the
  # action ses:SendRawEmail (docs/research/ses.md 2.1) on the sender's
  # identity ARN, which the send touches (docs/research/ses.md 5.1, 11.2).
  local role identity policy
  role=$(pattern_aws lambda list-functions | jq -r '.Functions[0].Role')
  identity="arn:aws:ses:us-east-1:${accounts[$current_pattern]}:identity/$s3_lambda_ses_sender"
  policy=$("$binary" iam policy --endpoint "$endpoint" --principal "$role" --output json)
  if ! jq -e --arg id "$identity" '[.document.Statement[]
      | select(([.Action] | flatten | index("ses:SendRawEmail")) and ([.Resource] | flatten | index($id)))]
      | length > 0' <<<"$policy" >/dev/null; then
    echo "iam policy for $role does not allow ses:SendRawEmail on $identity: $(jq -c .document <<<"$policy")"
    return 1
  fi
}

pattern_cleanup() {
  pattern_aws ses delete-identity --identity "$s3_lambda_ses_sender"
}
