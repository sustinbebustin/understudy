"""A word-count worker: a small boto3 SQS consumer.

Each job is an SQS message whose body names a text object in S3:

    {"bucket": "uploads", "key": "docs/readme.txt"}

The worker reads the object, counts its lines and words, writes the counts
next to it as results/<key>.json with a managed upload, and deletes the
message. A job it cannot finish stays on the queue: SQS makes it visible
again when its visibility timeout ends and, once the queue's maxReceiveCount
is reached, moves it to the dead-letter queue.

Nothing here is specific to understudy. boto3 reads AWS_ENDPOINT_URL, so the
same code runs against understudy on a laptop or in CI and against AWS in
production.

    python worker.py --queue-url "$QUEUE_URL"           # poll until stopped
    python worker.py --queue-url "$QUEUE_URL" --drain   # stop when the queue is empty
"""

from __future__ import annotations

import argparse
import io
import json
import logging
import sys
from dataclasses import dataclass
from typing import Any

import boto3
from botocore.exceptions import ClientError

log = logging.getLogger("worker")


@dataclass(frozen=True)
class Job:
    """A parsed job: the S3 object to count."""

    bucket: str
    key: str

    @staticmethod
    def parse(body: str) -> Job | Retry:
        """Read a message body, or say why it is not a job."""
        try:
            data = json.loads(body)
        except json.JSONDecodeError as e:
            return Retry(f"the body is not JSON: {e}")
        if not isinstance(data, dict):
            return Retry("the body is not a JSON object")
        bucket, key = data.get("bucket"), data.get("key")
        if not isinstance(bucket, str) or not isinstance(key, str) or not bucket or not key:
            return Retry('the body needs non-empty string fields "bucket" and "key"')
        return Job(bucket=bucket, key=key)

    @property
    def result_key(self) -> str:
        return f"results/{self.key}.json"


@dataclass(frozen=True)
class Done:
    """The job finished; its message can be deleted."""

    result_key: str


@dataclass(frozen=True)
class Retry:
    """The job did not finish; its message stays for SQS to redeliver."""

    reason: str


Outcome = Done | Retry


@dataclass
class Tally:
    """What one run of the worker did."""

    done: int = 0
    retried: int = 0


def process(s3: Any, job: Job) -> Outcome:
    """Count the words of one object and upload the result."""
    try:
        text = s3.get_object(Bucket=job.bucket, Key=job.key)["Body"].read().decode("utf-8")
    except ClientError as e:
        return Retry(f"reading s3://{job.bucket}/{job.key}: {e.response['Error']['Code']}")
    except UnicodeDecodeError as e:
        return Retry(f"s3://{job.bucket}/{job.key} is not UTF-8 text: {e}")

    result = {
        "source": f"s3://{job.bucket}/{job.key}",
        "lines": len(text.splitlines()),
        "words": len(text.split()),
    }
    # upload_fileobj is boto3's managed upload: one PutObject for a small
    # body, a multipart upload for a large one.
    try:
        s3.upload_fileobj(
            io.BytesIO(json.dumps(result).encode()),
            job.bucket,
            job.result_key,
            ExtraArgs={"ContentType": "application/json"},
        )
    except ClientError as e:
        return Retry(f"writing s3://{job.bucket}/{job.result_key}: {e.response['Error']['Code']}")
    return Done(job.result_key)


def handle(s3: Any, body: str) -> Outcome:
    """Parse and process one message body."""
    job = Job.parse(body)
    if isinstance(job, Retry):
        return job
    return process(s3, job)


def run(sqs: Any, s3: Any, queue_url: str, *, drain: bool) -> Tally:
    """Receive and process jobs. With drain, return once a receive comes back
    empty; otherwise poll until interrupted."""
    tally = Tally()
    while True:
        # Long polling: the call waits up to WaitTimeSeconds for a message
        # instead of returning empty at once. A drain waits only briefly.
        resp = sqs.receive_message(
            QueueUrl=queue_url,
            MaxNumberOfMessages=10,
            WaitTimeSeconds=1 if drain else 20,
            MessageSystemAttributeNames=["ApproximateReceiveCount"],
        )
        messages = resp.get("Messages", [])
        if not messages and drain:
            return tally
        for msg in messages:
            receives = msg.get("Attributes", {}).get("ApproximateReceiveCount", "?")
            match handle(s3, msg["Body"]):
                case Done(result_key):
                    sqs.delete_message(QueueUrl=queue_url, ReceiptHandle=msg["ReceiptHandle"])
                    log.info("message %s: wrote %s", msg["MessageId"], result_key)
                    tally.done += 1
                case Retry(reason):
                    # Not deleting is the retry: the message comes back after
                    # the visibility timeout, and the redrive policy moves it
                    # to the dead-letter queue after maxReceiveCount receives.
                    log.warning("message %s (receive %s): left for retry: %s", msg["MessageId"], receives, reason)
                    tally.retried += 1


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description="Count the words of the S3 objects named by SQS jobs.")
    parser.add_argument("--queue-url", required=True, help="URL of the job queue")
    parser.add_argument("--drain", action="store_true", help="exit once the queue has no visible message")
    args = parser.parse_args(argv)
    # boto3's own INFO lines (which credentials and endpoint it found) stay
    # quiet; the worker's are shown.
    logging.basicConfig(level=logging.WARNING, format="%(levelname)s %(message)s")
    log.setLevel(logging.INFO)

    sqs = boto3.client("sqs")
    s3 = boto3.client("s3")
    try:
        tally = run(sqs, s3, args.queue_url, drain=args.drain)
    except KeyboardInterrupt:
        return 130
    log.info("done %d, left for retry %d", tally.done, tally.retried)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
