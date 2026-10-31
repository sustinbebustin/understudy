"""EventBridge Scheduler (docs/research/scheduler.md): the tracer bullet, a rate()
schedule with an SQS target read back with the defaults AWS fills, invoked on the
emulator clock, then deleted."""

from __future__ import annotations

import json
from collections.abc import Callable
from typing import Any

import pytest

from support import Admin, raises, status_of, uniq

SCHEDULER_TRUST = json.dumps(
    {
        "Version": "2012-10-17",
        "Statement": [{"Effect": "Allow", "Principal": {"Service": "scheduler.amazonaws.com"}, "Action": "sts:AssumeRole"}],
    }
)


@pytest.fixture(scope="module")
def scheduler(make_client: Callable[..., Any]) -> Any:
    return make_client("scheduler")


@pytest.fixture
def role(iam: Any, cleanup: Any) -> str:
    name = uniq("sch-role")
    arn = iam.create_role(RoleName=name, AssumeRolePolicyDocument=SCHEDULER_TRUST)["Role"]["Arn"]
    cleanup.add(iam.delete_role, RoleName=name)
    return arn


def test_rate_schedule_to_sqs(
    scheduler: Any, sqs: Any, make_queue: Callable[..., str], role: str, clock: Admin, cleanup: Any
) -> None:
    """GetSchedule fills ActionAfterCompletion NONE, RetryPolicy 86400/185 and GroupName
    default (tf-acc schedule_test.go:L199-L223; scheduler.md 4.2); the Input reaches the
    queue with <aws.scheduler.attempt-number> substituted within the hour (aws-doc
    managing-schedule-context-attributes; scheduler.md 6.3); a deleted schedule is gone
    (scheduler.md 4.4)."""
    url = make_queue("sch-q")
    queue_arn = sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["QueueArn"])["Attributes"]["QueueArn"]
    name = uniq("sch")
    created = scheduler.create_schedule(
        Name=name,
        ScheduleExpression="rate(1 hour)",
        FlexibleTimeWindow={"Mode": "OFF"},
        Target={"Arn": queue_arn, "RoleArn": role, "Input": "hello <aws.scheduler.attempt-number>"},
    )
    cleanup.add(scheduler.delete_schedule, Name=name)
    assert created["ScheduleArn"].endswith(":schedule/default/" + name)

    got = scheduler.get_schedule(Name=name)
    assert got["GroupName"] == "default"
    assert got["ActionAfterCompletion"] == "NONE"
    assert got["Target"]["RetryPolicy"] == {"MaximumEventAgeInSeconds": 86400, "MaximumRetryAttempts": 185}
    assert got["Target"]["Input"] == "hello <aws.scheduler.attempt-number>"

    clock.advance(3600)
    messages = sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=10).get("Messages", [])
    assert messages, "the queue received nothing within the hour"
    assert {m["Body"] for m in messages} == {"hello 1"}

    scheduler.delete_schedule(Name=name)
    with raises("ResourceNotFoundException") as info:
        scheduler.get_schedule(Name=name)
    assert status_of(info.value) == 404
