#!/usr/bin/env bash
# The SNS -> SQS -> Lambda example, end to end: build the Go worker, deploy
# the pipeline with Terraform, publish three orders, check where each one
# went, then destroy everything. Exits non-zero when an order did not end up
# where it should.
#
# Needs a running understudy with Docker available (it runs the function in
# AWS's Lambda base image) and a shell pointed at it:
#   understudy serve                  # in another terminal
#   eval "$(understudy env)"
#   ./demo.sh
#
# Set KEEP=1 to leave the pipeline deployed for exploring afterwards.
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"

: "${AWS_ENDPOINT_URL:?is not set; start understudy and run: eval \"\$(understudy env)\"}"

# fail MESSAGE stops the demo with a message.
fail() {
  echo "demo: $*" >&2
  exit 1
}

# Build for this machine's architecture, so the function runs natively.
case "$(go env GOARCH)" in
  amd64) goarch=amd64 lambda_arch=x86_64 ;;
  arm64) goarch=arm64 lambda_arch=arm64 ;;
  *) fail "Lambda runs on amd64 or arm64; this machine is $(go env GOARCH)" ;;
esac

echo "==> building the worker for linux/$goarch"
GOOS=linux GOARCH=$goarch CGO_ENABLED=0 \
  go build -C function -tags lambda.norpc -trimpath -o ../build/bootstrap .

echo "==> deploying with Terraform (the AWS provider waits about 25s per SQS queue)"
terraform init -input=false >/dev/null
terraform apply -auto-approve -input=false -var "architecture=$lambda_arch"

if [[ "${KEEP:-}" != 1 ]]; then
  trap 'echo "==> destroying"; terraform destroy -auto-approve -input=false -var "architecture=$lambda_arch" >/dev/null' EXIT
fi

topic=$(terraform output -raw topic_arn)
queue_url=$(terraform output -raw queue_url)
dlq_url=$(terraform output -raw dlq_url)
bucket=$(terraform output -raw bucket)

# publish EVENT_TYPE MESSAGE publishes one message with an event_type attribute.
publish() {
  aws sns publish --topic-arn "$topic" --message "$2" \
    --message-attributes "{\"event_type\": {\"DataType\": \"String\", \"StringValue\": \"$1\"}}" \
    --query MessageId --output text
}

echo "==> publishing three messages"
publish order.created '{"orderId": "1001", "amount": 42.5}' # stored in S3
publish order.cancelled '{"orderId": "1002", "amount": 10}' # dropped by the filter policy
publish order.created 'not an order'                        # fails, then dead-lettered

# settle waits until understudy has nothing in flight: the SNS deliveries,
# the event source mapping's polls and the Lambda invocations.
understudy settle --timeout 2m

echo "==> orders in s3://$bucket"
keys=$(aws s3api list-objects-v2 --bucket "$bucket" --query 'Contents[].Key' --output text)
echo "$keys"
[[ "$keys" == "orders/1001.json" ]] || fail "expected only orders/1001.json in the bucket, found: $keys"

# The failed message is hidden for the queue's 60-second visibility timeout.
# Instead of waiting, move the emulator clock past it: the event source
# mapping receives it again (receive 2), the worker fails it again, and the
# next receive moves it to the dead-letter queue (maxReceiveCount is 2).
echo "==> skipping two visibility timeouts on the emulator clock"
for _ in 1 2; do
  understudy clock advance 61s
  understudy settle --timeout 2m
done

echo "==> the dead-letter queue"
dead=$(aws sqs receive-message --queue-url "$dlq_url" --query 'Messages[].Body' --output text)
echo "$dead"
[[ "$dead" == "not an order" ]] || fail "expected the bad order in the dead-letter queue, found: $dead"
remaining=$(aws sqs get-queue-attributes --queue-url "$queue_url" \
  --attribute-names ApproximateNumberOfMessages ApproximateNumberOfMessagesNotVisible \
  --query 'Attributes.[ApproximateNumberOfMessages, ApproximateNumberOfMessagesNotVisible]' --output text)
[[ "$remaining" == $'0\t0' ]] || fail "expected the orders queue to be empty, found (visible, in flight): $remaining"

# The worker logs JSON, so a filter pattern can match one field.
echo "==> the worker's failure log lines, from CloudWatch Logs"
aws logs filter-log-events --log-group-name /aws/lambda/order-worker \
  --filter-pattern '{ $.msg = "order failed" }' --query 'events[].message' --output text

# The journal records every API call and delivery with what caused it. The
# first of the three publishes above is the oldest of the last three. jq
# reads every line, so understudy never writes to a closed pipe, which
# pipefail would turn into a failure.
first_publish=$(understudy journal --operation sns.Publish --limit 3 --output json | jq -rs '.[0].id')
echo "==> what publishing order 1001 caused, from the journal"
understudy journal trace "$first_publish"

echo "==> done"
