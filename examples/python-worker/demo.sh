#!/usr/bin/env bash
# The python-worker example, end to end: create a bucket and a queue, queue a
# job, run the worker until the queue is empty, print its result, then run
# the tests.
#
# Needs a running understudy and a shell pointed at it:
#   understudy serve                  # in another terminal
#   eval "$(understudy env)"
#   ./demo.sh
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")"

: "${AWS_ENDPOINT_URL:?is not set; start understudy and run: eval \"\$(understudy env)\"}"

echo "==> creating the bucket and the queue"
aws s3api create-bucket --bucket uploads >/dev/null
queue_url=$(aws sqs create-queue --queue-name word-count-jobs --query QueueUrl --output text)

echo "==> uploading a document and queueing a job for it"
printf 'the quick brown fox\njumps over the lazy dog\n' | aws s3 cp - s3://uploads/docs/fox.txt
aws sqs send-message --queue-url "$queue_url" \
  --message-body '{"bucket": "uploads", "key": "docs/fox.txt"}' >/dev/null

echo "==> running the worker until the queue is empty"
uv run worker.py --queue-url "$queue_url" --drain

echo "==> the result"
aws s3 cp s3://uploads/results/docs/fox.txt.json -
echo

echo "==> the tests"
uv run pytest
