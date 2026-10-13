# Python worker with boto3

A small SQS worker written with boto3, and the pytest suite that tests it against understudy. Each job names a text file in S3. The worker counts the file's lines and words, uploads the counts with a managed upload (`upload_fileobj`), and deletes the message. A job it cannot finish stays on the queue. SQS makes it visible again after the visibility timeout and, after `maxReceiveCount` receives, moves it to the dead-letter queue, as on AWS.

The worker has no understudy-specific code: boto3 reads `AWS_ENDPOINT_URL`, so the same file runs against AWS in production.

| File | What it is |
|---|---|
| `worker.py` | The worker: `python worker.py --queue-url URL [--drain]` |
| `tests/test_worker.py` | The tests. Each gets its own bucket and queues, and one uses the emulator clock to skip a 30-second visibility timeout |
| `tests/conftest.py` | boto3 clients, and a guard that refuses to run unless `AWS_ENDPOINT_URL` is a loopback URL |
| `demo.sh` | The commands below, in one script |

## Run it

You need understudy (from the repository root, `go install ./cmd/understudy`), [uv](https://docs.astral.sh/uv/) and the AWS CLI. Docker is not needed.

Start understudy in one terminal:

```sh
understudy serve
```

In another terminal, from this directory, point the shell at understudy and run the demo:

```sh
eval "$(understudy env)"
./demo.sh
```

`demo.sh` runs these commands, which you can also type one by one:

```sh
aws s3api create-bucket --bucket uploads
queue_url=$(aws sqs create-queue --queue-name word-count-jobs --query QueueUrl --output text)
printf 'the quick brown fox\njumps over the lazy dog\n' | aws s3 cp - s3://uploads/docs/fox.txt
aws sqs send-message --queue-url "$queue_url" --message-body '{"bucket": "uploads", "key": "docs/fox.txt"}'
uv run worker.py --queue-url "$queue_url" --drain
aws s3 cp s3://uploads/results/docs/fox.txt.json -
uv run pytest
```

The worker logs one line per message, then the result is printed:

```text
INFO message 138f2577-...: wrote results/docs/fox.txt.json
INFO done 1, left for retry 0
{"source": "s3://uploads/docs/fox.txt", "lines": 2, "words": 9}
```

Then pytest reports `10 passed`. You can run `demo.sh` again: it reuses the bucket and the queue.

## Testing with the emulator clock

`test_a_bad_job_reaches_the_dead_letter_queue` keeps the production settings of the queue: a 30-second visibility timeout and two receives before the dead-letter queue. It still takes only seconds, because it never sleeps. It moves understudy's clock past the timeout with one call to the admin API:

```python
body = json.dumps({"op": "advance", "by": "31s"}).encode()
req = urllib.request.Request(
    f"{endpoint}/_understudy/v1/clock", data=body, method="POST", headers={"Content-Type": "application/json"}
)
urllib.request.urlopen(req)
```

Every visibility timeout, delay and retention period in understudy runs on that clock. The CLI equivalent is `understudy clock advance 31s`.

## Running the tests in CI

[`../github-actions`](../github-actions) has a GitHub Actions workflow that starts understudy and runs this suite.
