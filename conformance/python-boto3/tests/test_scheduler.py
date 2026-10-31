"""EventBridge Scheduler (docs/research/scheduler.md): the tracer bullet, a rate()
schedule with an SQS target read back with the defaults AWS fills, invoked on the
emulator clock, then deleted."""

from __future__ import annotations

import datetime
import json
import re
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


def _queue_arn(sqs: Any, url: str) -> str:
    return sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["QueueArn"])["Attributes"]["QueueArn"]


def _emulator_now(clock: Admin) -> datetime.datetime:
    """Emulator time from GET /clock, to the second."""
    whole = clock.clock()["now"].split(".")[0].removesuffix("Z")
    return datetime.datetime.fromisoformat(whole).replace(tzinfo=datetime.UTC)


def test_start_date_fire_carries_the_context_attributes(
    scheduler: Any, sqs: Any, make_queue: Callable[..., str], role: str, clock: Admin, cleanup: Any
) -> None:
    """A rate schedule first fires at its StartDate (aws-doc schedule-types; scheduler.md 5.2),
    its body carrying the schedule ARN, the scheduled time, a 16-hex execution id and attempt 1
    (aws-doc managing-schedule-context-attributes, configuring-schedule-dlq; scheduler.md 6.3,
    7.2); the time zone, State and StartDate read back as sent (tf-acc schedule_test.go:L365-L419,
    L1870-L1895; scheduler.md 4.2, 5.5)."""
    url = make_queue("sch-ctx")
    start = _emulator_now(clock) + datetime.timedelta(minutes=10)
    name = uniq("sch")
    created = scheduler.create_schedule(
        Name=name,
        ScheduleExpression="rate(1 hour)",
        ScheduleExpressionTimezone="Europe/Paris",
        State="ENABLED",
        StartDate=start,
        FlexibleTimeWindow={"Mode": "OFF"},
        Target={
            "Arn": _queue_arn(sqs, url),
            "RoleArn": role,
            "Input": "<aws.scheduler.schedule-arn>|<aws.scheduler.scheduled-time>|<aws.scheduler.execution-id>|<aws.scheduler.attempt-number>",
        },
    )
    cleanup.add(scheduler.delete_schedule, Name=name)
    got = scheduler.get_schedule(Name=name)
    assert got["ScheduleExpressionTimezone"] == "Europe/Paris"
    assert got["State"] == "ENABLED"
    assert got["StartDate"] == start

    clock.advance(11 * 60)
    messages = sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=10).get("Messages", [])
    assert len(messages) == 1, messages
    arn, scheduled, execution, attempt = messages[0]["Body"].split("|")
    assert arn == created["ScheduleArn"]
    assert scheduled == start.strftime("%Y-%m-%dT%H:%M:%SZ")
    assert re.fullmatch(r"[0-9a-f]{16}", execution), execution
    assert attempt == "1"


def test_update_drops_members_to_their_defaults(scheduler: Any, sqs: Any, make_queue: Callable[..., str], role: str, cleanup: Any) -> None:
    """UpdateSchedule replaces the schedule: members left out read back their defaults
    (aws-doc UpdateSchedule; tf-acc schedule_test.go:L409, L776, L1338; scheduler.md 4.3)."""
    target = {"Arn": _queue_arn(sqs, make_queue("sch-up")), "RoleArn": role}
    name = uniq("sch")
    scheduler.create_schedule(
        Name=name,
        ScheduleExpression="rate(1 hour)",
        Description="before",
        StartDate=datetime.datetime(2099, 1, 1, 1, tzinfo=datetime.UTC),
        EndDate=datetime.datetime(2100, 1, 1, 1, 2, 3, tzinfo=datetime.UTC),
        FlexibleTimeWindow={"Mode": "OFF"},
        Target={**target, "Input": "x", "RetryPolicy": {"MaximumEventAgeInSeconds": 60, "MaximumRetryAttempts": 1}},
    )
    cleanup.add(scheduler.delete_schedule, Name=name)
    scheduler.update_schedule(Name=name, ScheduleExpression="rate(1 hour)", FlexibleTimeWindow={"Mode": "OFF"}, Target=target)
    got = scheduler.get_schedule(Name=name)
    for member in ("Description", "StartDate", "EndDate"):
        assert not got.get(member), (member, got.get(member))
    assert "Input" not in got["Target"]
    assert got["Target"]["RetryPolicy"] == {"MaximumEventAgeInSeconds": 86400, "MaximumRetryAttempts": 185}


def test_list_schedules_by_prefix_across_pages(scheduler: Any, sqs: Any, make_queue: Callable[..., str], role: str, cleanup: Any) -> None:
    """ListSchedules returns a Schedules list (localstack@8b9a79f test_scheduler.py:L13-L20,
    validated 2024-06-11); NamePrefix filters, and following NextToken with MaxResults 1 yields
    every match once (scheduler.md 4.5; the order is unrecorded, so the union is compared)."""
    target = {"Arn": _queue_arn(sqs, make_queue("sch-ls")), "RoleArn": role}
    prefix = uniq("ls")
    names = {f"{prefix}-{i}" for i in range(3)}
    for n in names:
        scheduler.create_schedule(Name=n, ScheduleExpression="rate(1 hour)", FlexibleTimeWindow={"Mode": "OFF"}, Target=target)
        cleanup.add(scheduler.delete_schedule, Name=n)
    seen: list[str] = []
    args: dict[str, Any] = {"NamePrefix": prefix, "MaxResults": 1}
    while True:
        page = scheduler.list_schedules(**args)
        assert isinstance(page["Schedules"], list)
        seen += [s["Name"] for s in page["Schedules"]]
        if "NextToken" not in page:
            break
        args["NextToken"] = page["NextToken"]
    assert sorted(seen) == sorted(names)


def test_recorded_expression_refusal_and_past_at(scheduler: Any, sqs: Any, make_queue: Callable[..., str], role: str, cleanup: Any) -> None:
    """rate(10 seconds) is refused with "Invalid Schedule Expression <expr>." (G3:
    localstack@8b9a79f test_scheduler.snapshot.json:L127-L270, validated 2025-01-26); a past
    at() is accepted (G4: snapshot L303-L314, validated 2025-02-02; scheduler.md 5.1)."""
    target = {"Arn": _queue_arn(sqs, make_queue("sch-at")), "RoleArn": role}
    with raises("ValidationException") as info:
        scheduler.create_schedule(Name=uniq("sch"), ScheduleExpression="rate(10 seconds)", FlexibleTimeWindow={"Mode": "OFF"}, Target=target)
    assert status_of(info.value) == 400
    assert info.value.response["Error"]["Message"] == "Invalid Schedule Expression rate(10 seconds)."
    name = uniq("sch")
    created = scheduler.create_schedule(Name=name, ScheduleExpression="at(2022-12-31T23:59:59)", FlexibleTimeWindow={"Mode": "OFF"}, Target=target)
    cleanup.add(scheduler.delete_schedule, Name=name)
    assert created["ScheduleArn"].endswith(":schedule/default/" + name)


DEAD_LETTER_ATTRIBUTES = ("ERROR_CODE", "ERROR_MESSAGE", "EXECUTION_ID", "IS_PAYLOAD_TRUNCATED", "RETRY_ATTEMPTS", "SCHEDULED_TIME", "SCHEDULE_ARN", "TARGET_ARN")


def test_deleted_target_queue_dead_letters(
    scheduler: Any, sqs: Any, make_queue: Callable[..., str], role: str, clock: Admin, cleanup: Any
) -> None:
    """A schedule whose target queue was deleted, with MaximumRetryAttempts 0, sends its Input
    to the dead-letter queue with the String attributes the dead-letter page lists, ERROR_CODE
    AWS.SimpleQueueService.NonExistentQueue (aws-doc configuring-schedule-dlq; scheduler.md
    7.2). RETRY_ATTEMPTS and EXHAUSTED_RETRY_CONDITION depend on
    scheduler.retry.permanent-errors and are asserted in Go tests; DeadLetterConfig reads back
    as sent (4.2)."""
    gone_url = sqs.create_queue(QueueName=uniq("sch-gone"))["QueueUrl"]
    gone_arn = _queue_arn(sqs, gone_url)
    sqs.delete_queue(QueueUrl=gone_url)
    dlq_url = make_queue("sch-dlq")
    dlq_arn = _queue_arn(sqs, dlq_url)
    at = _emulator_now(clock) + datetime.timedelta(minutes=10)
    name = uniq("sch")
    created = scheduler.create_schedule(
        Name=name,
        ScheduleExpression=f"at({at.strftime('%Y-%m-%dT%H:%M:%S')})",
        FlexibleTimeWindow={"Mode": "OFF"},
        Target={
            "Arn": gone_arn,
            "RoleArn": role,
            "Input": "Hello, world!",
            "RetryPolicy": {"MaximumRetryAttempts": 0},
            "DeadLetterConfig": {"Arn": dlq_arn},
        },
    )
    cleanup.add(scheduler.delete_schedule, Name=name)
    got = scheduler.get_schedule(Name=name)
    assert got["Target"]["DeadLetterConfig"] == {"Arn": dlq_arn}
    assert got["Target"]["RetryPolicy"]["MaximumRetryAttempts"] == 0

    clock.advance(11 * 60)
    messages = sqs.receive_message(QueueUrl=dlq_url, MaxNumberOfMessages=10, MessageAttributeNames=["All"]).get("Messages", [])
    assert len(messages) == 1, messages
    attrs = messages[0]["MessageAttributes"]
    assert messages[0]["Body"] == "Hello, world!"
    for attr in DEAD_LETTER_ATTRIBUTES:
        assert attrs[attr]["DataType"] == "String", (attr, attrs.get(attr))
    assert attrs["ERROR_CODE"]["StringValue"] == "AWS.SimpleQueueService.NonExistentQueue"
    assert re.fullmatch(r"arn:aws:scheduler:::aws-sdk:[a-z0-9-]+:[a-zA-Z]+", attrs["TARGET_ARN"]["StringValue"])
    assert attrs["SCHEDULE_ARN"]["StringValue"] == created["ScheduleArn"]
    assert attrs["SCHEDULED_TIME"]["StringValue"] == at.strftime("%Y-%m-%dT%H:%M:%SZ")


def test_at_with_delete_is_gone_after_firing(scheduler: Any, sqs: Any, make_queue: Callable[..., str], role: str, clock: Admin) -> None:
    """A one-time at() with ActionAfterCompletion DELETE reads DELETE back, invokes its target
    once, and is gone after it has (aws-doc managing-schedule-delete; scheduler.md 8)."""
    url = make_queue("sch-q")
    at = _emulator_now(clock) + datetime.timedelta(minutes=10)
    name = uniq("sch")
    scheduler.create_schedule(
        Name=name,
        ScheduleExpression=f"at({at.strftime('%Y-%m-%dT%H:%M:%S')})",
        ActionAfterCompletion="DELETE",
        FlexibleTimeWindow={"Mode": "OFF"},
        Target={"Arn": _queue_arn(sqs, url), "RoleArn": role, "Input": "once"},
    )
    assert scheduler.get_schedule(Name=name)["ActionAfterCompletion"] == "DELETE"

    clock.advance(11 * 60)
    messages = sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=10).get("Messages", [])
    assert [m["Body"] for m in messages] == ["once"]
    with raises("ResourceNotFoundException"):
        scheduler.get_schedule(Name=name)


def test_group_with_tags_and_members(scheduler: Any, sqs: Any, make_queue: Callable[..., str], role: str) -> None:
    """A schedule group created with a tag reads ACTIVE with its ARN (tf-acc
    schedule_group_test.go:L25-L72), lists its tag, and untagged lists "Tags": [] (G2:
    localstack@8b9a79f test_scheduler.snapshot.json:L19-L30); deleting it deletes its
    schedules, after which the group and the schedule answer 404 (aws-doc
    managing-schedule-group-delete; scheduler.md 3.2-3.4)."""
    group = uniq("sch-g")
    arn = scheduler.create_schedule_group(Name=group, Tags=[{"Key": "TagKey", "Value": "TagValue"}])["ScheduleGroupArn"]
    assert re.fullmatch(rf"arn:aws:scheduler:[a-z0-9-]+:\d{{12}}:schedule-group/{group}", arn)
    got = scheduler.get_schedule_group(Name=group)
    assert (got["Arn"], got["Name"], got["State"]) == (arn, group, "ACTIVE")
    assert scheduler.list_tags_for_resource(ResourceArn=arn)["Tags"] == [{"Key": "TagKey", "Value": "TagValue"}]
    scheduler.untag_resource(ResourceArn=arn, TagKeys=["TagKey"])
    assert scheduler.list_tags_for_resource(ResourceArn=arn)["Tags"] == []

    url = make_queue("sch-q")
    scheduler.create_schedule(
        Name="member",
        GroupName=group,
        ScheduleExpression="rate(1 hour)",
        FlexibleTimeWindow={"Mode": "OFF"},
        Target={"Arn": _queue_arn(sqs, url), "RoleArn": role},
    )
    scheduler.delete_schedule_group(Name=group)
    with raises("ResourceNotFoundException") as info:
        scheduler.get_schedule_group(Name=group)
    assert status_of(info.value) == 404
    with raises("ResourceNotFoundException"):
        scheduler.get_schedule(Name="member", GroupName=group)


def test_universal_target_is_not_implemented(scheduler: Any, role: str) -> None:
    """A universal (aws-sdk) target answers 501 NotImplemented and nothing is stored
    (CLAUDE.md invariant 3; spec "Scope")."""
    name = uniq("sch")
    target = {"Arn": "arn:aws:scheduler:::aws-sdk:sqs:sendMessage", "RoleArn": role, "Input": "{}"}
    with raises("NotImplemented") as info:
        scheduler.create_schedule(Name=name, ScheduleExpression="rate(1 hour)", FlexibleTimeWindow={"Mode": "OFF"}, Target=target)
    assert status_of(info.value) == 501
    with raises("ResourceNotFoundException"):
        scheduler.get_schedule(Name=name)
