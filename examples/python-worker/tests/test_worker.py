"""The worker against understudy: the same calls it makes against AWS.

Each test gets its own bucket and queues, so tests never see each other's
messages and a failed test leaves nothing behind that breaks the next one.
"""

from __future__ import annotations

import json
import os
import urllib.request
import uuid
from collections.abc import Iterator
from dataclasses import dataclass
from typing import Any

import pytest

import worker

# The work queue's visibility timeout and receive limit: the values a
# production queue might have. The clock fixture makes waiting them out
# instant.
VISIBILITY_TIMEOUT_SECONDS = 30
MAX_RECEIVE_COUNT = 2


@dataclass(frozen=True)
class Queues:
    work: str
    dead_letter: str


class Clock:
    """understudy's emulator clock, through its admin API. Advancing it fires
    every timer due on the way, such as visibility timeouts, at once."""

    def __init__(self, endpoint: str) -> None:
        self._url = f"{endpoint}/_understudy/v1/clock"

    def advance(self, seconds: int) -> None:
        headers = {"Content-Type": "application/json"}
        # A server started with UNDERSTUDY_ADMIN_TOKEN wants it on admin calls.
        if token := os.environ.get("UNDERSTUDY_ADMIN_TOKEN"):
            headers["Authorization"] = f"Bearer {token}"
        body = json.dumps({"op": "advance", "by": f"{seconds}s"}).encode()
        req = urllib.request.Request(self._url, data=body, headers=headers, method="POST")
        with urllib.request.urlopen(req, timeout=10) as resp:
            resp.read()


@pytest.fixture
def clock(endpoint: str) -> Clock:
    return Clock(endpoint)


@pytest.fixture
def bucket(s3: Any) -> Iterator[str]:
    name = f"worker-test-{uuid.uuid4().hex[:12]}"
    s3.create_bucket(Bucket=name)
    yield name
    for page in s3.get_paginator("list_objects_v2").paginate(Bucket=name):
        for obj in page.get("Contents", []):
            s3.delete_object(Bucket=name, Key=obj["Key"])
    s3.delete_bucket(Bucket=name)


@pytest.fixture
def queues(sqs: Any) -> Iterator[Queues]:
    suffix = uuid.uuid4().hex[:12]
    dlq_url = sqs.create_queue(QueueName=f"worker-test-dlq-{suffix}")["QueueUrl"]
    dlq_arn = sqs.get_queue_attributes(QueueUrl=dlq_url, AttributeNames=["QueueArn"])["Attributes"]["QueueArn"]
    work_url = sqs.create_queue(
        QueueName=f"worker-test-{suffix}",
        Attributes={
            "VisibilityTimeout": str(VISIBILITY_TIMEOUT_SECONDS),
            "RedrivePolicy": json.dumps({"deadLetterTargetArn": dlq_arn, "maxReceiveCount": MAX_RECEIVE_COUNT}),
        },
    )["QueueUrl"]
    yield Queues(work=work_url, dead_letter=dlq_url)
    sqs.delete_queue(QueueUrl=work_url)
    sqs.delete_queue(QueueUrl=dlq_url)


def test_counts_words_and_deletes_the_job(s3: Any, sqs: Any, bucket: str, queues: Queues) -> None:
    s3.put_object(Bucket=bucket, Key="docs/note.txt", Body=b"one two three\nfour five\n")
    sqs.send_message(QueueUrl=queues.work, MessageBody=json.dumps({"bucket": bucket, "key": "docs/note.txt"}))

    tally = worker.run(sqs, s3, queues.work, drain=True)

    assert tally == worker.Tally(done=1, retried=0)
    result = json.loads(s3.get_object(Bucket=bucket, Key="results/docs/note.txt.json")["Body"].read())
    assert result == {"source": f"s3://{bucket}/docs/note.txt", "lines": 2, "words": 5}
    attrs = sqs.get_queue_attributes(QueueUrl=queues.work, AttributeNames=["All"])["Attributes"]
    assert attrs["ApproximateNumberOfMessages"] == "0"
    assert attrs["ApproximateNumberOfMessagesNotVisible"] == "0"


def test_a_job_naming_a_missing_object_is_left_for_retry(s3: Any, sqs: Any, bucket: str, queues: Queues) -> None:
    sqs.send_message(QueueUrl=queues.work, MessageBody=json.dumps({"bucket": bucket, "key": "missing.txt"}))

    tally = worker.run(sqs, s3, queues.work, drain=True)

    assert tally == worker.Tally(done=0, retried=1)
    attrs = sqs.get_queue_attributes(QueueUrl=queues.work, AttributeNames=["ApproximateNumberOfMessagesNotVisible"])
    assert attrs["Attributes"]["ApproximateNumberOfMessagesNotVisible"] == "1"


def test_a_bad_job_reaches_the_dead_letter_queue(s3: Any, sqs: Any, queues: Queues, clock: Clock) -> None:
    sqs.send_message(QueueUrl=queues.work, MessageBody="not a job")

    # Each receive hides the message for the visibility timeout. Advancing
    # the emulator clock past it brings the message back at once, so the test
    # keeps the production timeout and still takes no wall time.
    for _ in range(MAX_RECEIVE_COUNT):
        assert worker.run(sqs, s3, queues.work, drain=True) == worker.Tally(done=0, retried=1)
        clock.advance(VISIBILITY_TIMEOUT_SECONDS + 1)

    # The receive after maxReceiveCount moves the message to the dead-letter
    # queue instead of returning it.
    assert worker.run(sqs, s3, queues.work, drain=True) == worker.Tally()
    dead = sqs.receive_message(QueueUrl=queues.dead_letter, WaitTimeSeconds=0).get("Messages", [])
    assert [m["Body"] for m in dead] == ["not a job"]


@pytest.mark.parametrize(
    "body",
    ["", "[]", "not a job", '{"bucket": "b"}', '{"bucket": "", "key": "k"}', '{"bucket": 1, "key": "k"}'],
)
def test_parse_rejects_bodies_that_are_not_jobs(body: str) -> None:
    assert isinstance(worker.Job.parse(body), worker.Retry)


def test_parse_reads_a_job() -> None:
    assert worker.Job.parse('{"bucket": "b", "key": "k"}') == worker.Job(bucket="b", key="k")
