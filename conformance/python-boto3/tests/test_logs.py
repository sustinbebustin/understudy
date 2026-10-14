"""CloudWatch Logs (docs/research/secrets-logs-events-alarms.md section 3 and
logs-filter-patterns.md): groups, streams, PutLogEvents, GetLogEvents, FilterLogEvents with the
real pattern syntax, retention on the emulator clock and tags."""

from __future__ import annotations

import json
import re
import time
from collections.abc import Callable
from typing import Any

import pytest

from support import Admin, raises, status_of, uniq

DAY_MS = 86_400_000


def now_ms() -> int:
    return int(time.time() * 1000)


@pytest.fixture
def make_group(logs: Any, cleanup: Any) -> Callable[..., str]:
    def make(prefix: str = "/conf/logs", **args: Any) -> str:
        name = uniq(prefix)
        logs.create_log_group(logGroupName=name, **args)
        cleanup.add(logs.delete_log_group, logGroupName=name)
        return name

    return make


@pytest.fixture
def make_stream(logs: Any) -> Callable[[str, str], str]:
    def make(group: str, name: str = "stream") -> str:
        logs.create_log_stream(logGroupName=group, logStreamName=name)
        return name

    return make


def put(logs: Any, group: str, stream: str, events: list[tuple[int, str]]) -> dict[str, Any]:
    return logs.put_log_events(
        logGroupName=group,
        logStreamName=stream,
        logEvents=[{"timestamp": ts, "message": msg} for ts, msg in events],
    )


def filtered(logs: Any, group: str, pattern: str | None = None, **kwargs: Any) -> list[str]:
    """Messages FilterLogEvents returns across every page."""
    args: dict[str, Any] = {"logGroupName": group, **kwargs}
    if pattern is not None:
        args["filterPattern"] = pattern
    return [e["message"] for page in logs.get_paginator("filter_log_events").paginate(**args) for e in page["events"]]


def test_create_and_describe_log_group(logs: Any, make_group: Callable[..., str]) -> None:
    name = make_group()
    [group] = logs.describe_log_groups(logGroupNamePrefix=name)["logGroups"]
    assert group["logGroupName"] == name
    assert re.fullmatch(rf"arn:aws:logs:us-east-1:\d{{12}}:log-group:{re.escape(name)}", group["logGroupArn"])
    assert group["arn"] == group["logGroupArn"] + ":*"
    assert group["storedBytes"] == 0
    assert group["metricFilterCount"] == 0
    assert group["logGroupClass"] == "STANDARD"
    assert "retentionInDays" not in group
    assert abs(group["creationTime"] - now_ms()) < 120_000


def test_duplicate_and_missing_groups(logs: Any, make_group: Callable[..., str]) -> None:
    name = make_group()
    with raises("ResourceAlreadyExistsException") as exc:
        logs.create_log_group(logGroupName=name)
    assert status_of(exc.value) == 400
    assert exc.value.response["Error"]["Message"] == "The specified log group already exists"

    with raises("ResourceNotFoundException") as exc:
        logs.describe_log_streams(logGroupName=uniq("/conf/missing"))
    assert exc.value.response["Error"]["Message"] == "The specified log group does not exist."


def test_describe_log_groups_prefix_and_pattern_are_mutually_exclusive(logs: Any) -> None:
    with raises("InvalidParameterException") as exc:
        logs.describe_log_groups(logGroupNamePrefix="/a", logGroupNamePattern="b")
    assert exc.value.response["Error"]["Message"] == "LogGroup name prefix and LogGroup name pattern are mutually exclusive parameters."


def test_describe_log_groups_paginates_by_prefix(logs: Any, make_group: Callable[..., str]) -> None:
    prefix = uniq("/conf/page")
    names = sorted(make_group(f"{prefix}/g{i}") for i in range(5))
    seen = [
        g["logGroupName"]
        for page in logs.get_paginator("describe_log_groups").paginate(logGroupNamePrefix=prefix, PaginationConfig={"PageSize": 2})
        for g in page["logGroups"]
    ]
    assert sorted(seen) == names


def test_streams_are_created_listed_and_validated(logs: Any, make_group: Callable[..., str], make_stream: Callable[[str, str], str]) -> None:
    group = make_group()
    make_stream(group, "app-1")
    make_stream(group, "app-2")
    make_stream(group, "db-1")

    with raises("ResourceAlreadyExistsException") as exc:
        logs.create_log_stream(logGroupName=group, logStreamName="app-1")
    assert exc.value.response["Error"]["Message"] == "The specified log stream already exists"

    streams = logs.describe_log_streams(logGroupName=group, logStreamNamePrefix="app-")["logStreams"]
    assert [s["logStreamName"] for s in streams] == ["app-1", "app-2"]
    first = streams[0]
    assert first["arn"] == f"arn:aws:logs:us-east-1:{first['arn'].split(':')[4]}:log-group:{group}:log-stream:app-1"
    assert first["storedBytes"] == 0
    assert "firstEventTimestamp" not in first and "lastEventTimestamp" not in first

    with raises("InvalidParameterException"):
        logs.describe_log_streams(logGroupName=group, logStreamNamePrefix="app-", orderBy="LastEventTime")


def test_describe_log_streams_orders_by_last_event_time(
    logs: Any, make_group: Callable[..., str], make_stream: Callable[[str, str], str]
) -> None:
    group = make_group()
    base = now_ms() - 60_000
    for i, name in enumerate(["a", "b", "c"]):
        make_stream(group, name)
        put(logs, group, name, [(base + (2 - i) * 1000, "x")])
    ordered = logs.describe_log_streams(logGroupName=group, orderBy="LastEventTime", descending=True)["logStreams"]
    # Stream "a" holds the newest event, "c" the oldest.
    assert [s["logStreamName"] for s in ordered] == ["a", "b", "c"]
    assert [s["lastEventTimestamp"] for s in ordered] == sorted((s["lastEventTimestamp"] for s in ordered), reverse=True)


def test_deleting_groups_and_streams(logs: Any, make_group: Callable[..., str], make_stream: Callable[[str, str], str]) -> None:
    group = make_group()
    make_stream(group, "s")
    logs.delete_log_stream(logGroupName=group, logStreamName="s")
    with raises("ResourceNotFoundException"):
        logs.delete_log_stream(logGroupName=group, logStreamName="s")
    logs.delete_log_group(logGroupName=group)
    with raises("ResourceNotFoundException"):
        logs.delete_log_group(logGroupName=group)


def test_put_log_events_returns_a_sequence_token_and_stores_events(
    logs: Any, make_group: Callable[..., str], make_stream: Callable[[str, str], str]
) -> None:
    group = make_group()
    make_stream(group, "s")
    t = now_ms()
    out = put(logs, group, "s", [(t, "first"), (t + 1, "second"), (t + 1, "third")])
    assert out["nextSequenceToken"]
    assert "rejectedLogEventsInfo" not in out
    events = logs.get_log_events(logGroupName=group, logStreamName="s", startFromHead=True)["events"]
    assert [(e["timestamp"], e["message"]) for e in events] == [(t, "first"), (t + 1, "second"), (t + 1, "third")]
    assert all(abs(e["ingestionTime"] - now_ms()) < 120_000 for e in events)


def test_put_log_events_ignores_the_sequence_token(
    logs: Any, make_group: Callable[..., str], make_stream: Callable[[str, str], str]
) -> None:
    group = make_group()
    make_stream(group, "s")
    t = now_ms()
    put(logs, group, "s", [(t, "a")])
    logs.put_log_events(
        logGroupName=group, logStreamName="s", logEvents=[{"timestamp": t + 1, "message": "b"}], sequenceToken="bogus-token"
    )
    assert filtered(logs, group) == ["a", "b"]


def test_put_log_events_to_a_missing_stream_or_group(logs: Any, make_group: Callable[..., str]) -> None:
    group = make_group()
    with raises("ResourceNotFoundException") as exc:
        put(logs, group, "nope", [(now_ms(), "x")])
    assert exc.value.response["Error"]["Message"] == "The specified log stream does not exist."
    with raises("ResourceNotFoundException") as exc:
        put(logs, uniq("/conf/nogroup"), "s", [(now_ms(), "x")])
    assert exc.value.response["Error"]["Message"] == "The specified log group does not exist."


def test_put_log_events_must_be_chronological(logs: Any, make_group: Callable[..., str], make_stream: Callable[[str, str], str]) -> None:
    group = make_group()
    make_stream(group, "s")
    t = now_ms()
    with raises("InvalidParameterException") as exc:
        put(logs, group, "s", [(t, "newer"), (t - 2 * DAY_MS, "older")])
    assert exc.value.response["Error"]["Message"] == "Log events in a single PutLogEvents request must be in chronological order."


def test_too_old_events_are_rejected_and_valid_ones_stored(
    logs: Any, make_group: Callable[..., str], make_stream: Callable[[str, str], str]
) -> None:
    group = make_group()
    make_stream(group, "s")
    t = now_ms()
    out = put(logs, group, "s", [(t - 30 * DAY_MS, "ancient"), (t - 29 * DAY_MS, "old"), (t, "fresh")])
    assert out["rejectedLogEventsInfo"]["tooOldLogEventEndIndex"] == 2, "the end index is exclusive"
    assert filtered(logs, group) == ["fresh"]


def test_too_new_events_are_rejected(logs: Any, make_group: Callable[..., str], make_stream: Callable[[str, str], str]) -> None:
    group = make_group()
    make_stream(group, "s")
    t = now_ms()
    out = put(logs, group, "s", [(t, "now"), (t + DAY_MS, "tomorrow")])
    assert out["rejectedLogEventsInfo"]["tooNewLogEventStartIndex"] == 1, "the start index is inclusive"
    assert filtered(logs, group) == ["now"]


@pytest.fixture
def five_events(logs: Any, make_group: Callable[..., str], make_stream: Callable[[str, str], str]) -> tuple[str, int]:
    group = make_group()
    make_stream(group, "s")
    base = now_ms() - 10_000
    put(logs, group, "s", [(base + i, f"m{i}") for i in range(5)])
    return group, base


def test_get_log_events_from_head_pages_forward(logs: Any, five_events: tuple[str, int]) -> None:
    group, _ = five_events
    first = logs.get_log_events(logGroupName=group, logStreamName="s", startFromHead=True, limit=2)
    assert [e["message"] for e in first["events"]] == ["m0", "m1"]
    assert first["nextForwardToken"].startswith("f/") and first["nextBackwardToken"].startswith("b/")

    second = logs.get_log_events(logGroupName=group, logStreamName="s", startFromHead=True, limit=2, nextToken=first["nextForwardToken"])
    assert [e["message"] for e in second["events"]] == ["m2", "m3"]
    third = logs.get_log_events(logGroupName=group, logStreamName="s", startFromHead=True, limit=2, nextToken=second["nextForwardToken"])
    assert [e["message"] for e in third["events"]] == ["m4"]

    # At the end of the stream the same token comes back.
    end = logs.get_log_events(logGroupName=group, logStreamName="s", startFromHead=True, limit=2, nextToken=third["nextForwardToken"])
    assert end["events"] == [] and end["nextForwardToken"] == third["nextForwardToken"]


def test_get_log_events_defaults_to_the_newest_events(logs: Any, five_events: tuple[str, int]) -> None:
    group, _ = five_events
    page = logs.get_log_events(logGroupName=group, logStreamName="s", limit=2)
    assert sorted(e["message"] for e in page["events"]) == ["m3", "m4"]


def test_get_log_events_time_range_is_start_inclusive_end_exclusive(logs: Any, five_events: tuple[str, int]) -> None:
    group, base = five_events
    events = logs.get_log_events(
        logGroupName=group, logStreamName="s", startFromHead=True, startTime=base + 1, endTime=base + 3
    )["events"]
    assert [e["message"] for e in events] == ["m1", "m2"]


def test_get_log_events_rejects_bad_tokens_and_limits(logs: Any, five_events: tuple[str, int]) -> None:
    group, _ = five_events
    with raises("InvalidParameterException") as exc:
        logs.get_log_events(logGroupName=group, logStreamName="s", nextToken="f/not-a-token")
    assert exc.value.response["Error"]["Message"] == "The specified nextToken is invalid."
    with raises("InvalidParameterException") as exc:
        logs.get_log_events(logGroupName=group, logStreamName="s", limit=10001)
    assert exc.value.response["Error"]["Message"] == (
        "1 validation error detected: Value '10001' at 'limit' failed to satisfy constraint: "
        "Member must have value less than or equal to 10000"
    )


TEXT_EVENTS = [
    "ERROR failed to connect to db",
    "INFO started",
    "WARN slow request ARGUMENTS",
    "ERROR ARGUMENTS missing",
]
JSON_EVENTS = [
    '{"level":"error","status":500,"user":{"id":7}}',
    '{"level":"info","status":200,"user":{"id":8}}',
    '{"level":"error","status":404,"user":{"id":9}}',
]
ACCESS_EVENTS = [
    '127.0.0.1 - frank [10/Oct/2000:13:55:36 -0700] "GET /apache_pb.gif HTTP/1.0" 200 2326',
    '127.0.0.2 - alice [10/Oct/2000:13:55:37 -0700] "GET /missing.html HTTP/1.0" 404 209',
    '10.0.0.9 - bob [10/Oct/2000:13:55:38 -0700] "POST /api HTTP/1.0" 500 87',
]


@pytest.fixture
def mixed_group(logs: Any, make_group: Callable[..., str], make_stream: Callable[[str, str], str]) -> str:
    group = make_group()
    base = now_ms() - 60_000
    for stream, messages in (("text", TEXT_EVENTS), ("json", JSON_EVENTS), ("access", ACCESS_EVENTS)):
        make_stream(group, stream)
        put(logs, group, stream, [(base + i, m) for i, m in enumerate(messages)])
    return group


def test_filter_without_a_pattern_returns_everything_in_time_order(logs: Any, mixed_group: str) -> None:
    messages = filtered(logs, mixed_group)
    assert sorted(messages) == sorted(TEXT_EVENTS + JSON_EVENTS + ACCESS_EVENTS)
    out = logs.filter_log_events(logGroupName=mixed_group)
    stamps = [e["timestamp"] for e in out["events"]]
    assert stamps == sorted(stamps)
    event = out["events"][0]
    assert {"eventId", "ingestionTime", "logStreamName", "message", "timestamp"} <= set(event)
    assert out.get("searchedLogStreams", []) == []


@pytest.mark.parametrize(
    ("pattern", "expected"),
    [
        pytest.param("ERROR", [TEXT_EVENTS[0], TEXT_EVENTS[3]], id="term"),
        # Lower case "error" appears only in the JSON events; the upper case ERROR events do not match.
        pytest.param("error", [JSON_EVENTS[0], JSON_EVENTS[2]], id="terms-are-case-sensitive"),
        pytest.param("ERROR ARGUMENTS", [TEXT_EVENTS[3]], id="terms-are-anded"),
        pytest.param("?ERROR ?ARGUMENTS", [TEXT_EVENTS[0], TEXT_EVENTS[2], TEXT_EVENTS[3]], id="optional-terms-are-ored"),
        pytest.param("ERROR -ARGUMENTS", [TEXT_EVENTS[0]], id="excluded-term"),
        pytest.param('"slow request"', [TEXT_EVENTS[2]], id="quoted-phrase"),
        pytest.param("?ERROR ?ARGUMENTS slow", [TEXT_EVENTS[2]], id="optional-terms-are-ignored-beside-plain-terms"),
        pytest.param("%^ERROR%", [TEXT_EVENTS[0], TEXT_EVENTS[3]], id="regex-anchored"),
        pytest.param("%AR.UMENTS%", [TEXT_EVENTS[2], TEXT_EVENTS[3]], id="regex-search"),
    ],
)
def test_text_filter_patterns(logs: Any, mixed_group: str, pattern: str, expected: list[str]) -> None:
    assert sorted(filtered(logs, mixed_group, pattern)) == sorted(expected)


def test_a_single_space_matches_every_event(logs: Any, mixed_group: str) -> None:
    assert len(filtered(logs, mixed_group, " ")) == len(TEXT_EVENTS + JSON_EVENTS + ACCESS_EVENTS)


@pytest.mark.parametrize(
    ("pattern", "expected"),
    [
        pytest.param('{ $.level = "error" }', [JSON_EVENTS[0], JSON_EVENTS[2]], id="string-equals"),
        pytest.param('{ $.level != "error" }', [JSON_EVENTS[1]], id="string-not-equals"),
        pytest.param('{ $.level = "err*" }', [JSON_EVENTS[0], JSON_EVENTS[2]], id="string-wildcard"),
        pytest.param("{ $.status >= 500 }", [JSON_EVENTS[0]], id="numeric-comparison"),
        pytest.param("{ $.status = 404 }", [JSON_EVENTS[2]], id="numeric-equals"),
        pytest.param('{ $.level = "error" && $.status = 404 }', [JSON_EVENTS[2]], id="and"),
        pytest.param("{ $.user.id = 8 || $.user.id = 9 }", [JSON_EVENTS[1], JSON_EVENTS[2]], id="or-on-nested-path"),
        pytest.param('{ ($.level = "info") || ($.status = 404) }', [JSON_EVENTS[1], JSON_EVENTS[2]], id="parentheses"),
        pytest.param('{ $.level = "error" || $.level = "info" && $.status = 404 }', [JSON_EVENTS[0], JSON_EVENTS[2]], id="and-binds-tighter-than-or"),
        pytest.param("{ $.user.id = * }", JSON_EVENTS, id="exists-via-wildcard"),
        pytest.param("{ $.missing NOT EXISTS }", JSON_EVENTS, id="not-exists"),
        pytest.param("{ $.level = %^err% }", [JSON_EVENTS[0], JSON_EVENTS[2]], id="regex"),
    ],
)
def test_json_filter_patterns(logs: Any, mixed_group: str, pattern: str, expected: list[str]) -> None:
    # JSON patterns never match a message that is not JSON, so the text and access events stay out.
    assert sorted(filtered(logs, mixed_group, pattern)) == sorted(expected)


@pytest.mark.parametrize(
    ("pattern", "expected"),
    [
        pytest.param("[ip, user, username, timestamp, request, status_code=404, bytes]", [ACCESS_EVENTS[1]], id="equals"),
        pytest.param("[ip, user, username, timestamp, request, status_code=4*, bytes]", [ACCESS_EVENTS[1]], id="wildcard"),
        pytest.param("[ip, user, username, timestamp, request, status_code >= 400, bytes]", ACCESS_EVENTS[1:], id="numeric"),
        pytest.param("[ip, user, username, timestamp, request, status_code=404 || status_code=500, bytes]", ACCESS_EVENTS[1:], id="or"),
        pytest.param("[..., request=*.html*, status_code, bytes]", [ACCESS_EVENTS[1]], id="ellipsis-and-wildcard"),
        pytest.param("[ip=%^127\\.0\\.0\\.[1-9]$%, user, username, timestamp, request, status_code, bytes]", ACCESS_EVENTS[:2], id="regex-field"),
        pytest.param("[ip, user, username=bob, timestamp, request, status_code, bytes]", [ACCESS_EVENTS[2]], id="named-field-equals"),
    ],
)
def test_space_delimited_filter_patterns(logs: Any, mixed_group: str, pattern: str, expected: list[str]) -> None:
    assert sorted(filtered(logs, mixed_group, pattern)) == sorted(expected)


def test_a_malformed_pattern_is_rejected(logs: Any, mixed_group: str) -> None:
    for pattern in ("{ $.level = ", "{ $.level = \"x\" && }", "[ip,, user"):
        with raises("InvalidParameterException") as exc:
            logs.filter_log_events(logGroupName=mixed_group, filterPattern=pattern)
        assert status_of(exc.value) == 400, pattern


def test_filter_can_target_streams(logs: Any, mixed_group: str) -> None:
    assert sorted(filtered(logs, mixed_group, logStreamNames=["json"])) == sorted(JSON_EVENTS)
    assert sorted(filtered(logs, mixed_group, logStreamNamePrefix="a")) == sorted(ACCESS_EVENTS)
    assert sorted(filtered(logs, mixed_group, "ERROR", logStreamNames=["text", "json"])) == sorted([TEXT_EVENTS[0], TEXT_EVENTS[3]])
    with raises("InvalidParameterException"):
        logs.filter_log_events(logGroupName=mixed_group, logStreamNames=["json"], logStreamNamePrefix="j")


def test_filter_time_range_is_inclusive_on_both_ends(logs: Any, make_group: Callable[..., str], make_stream: Callable[[str, str], str]) -> None:
    group = make_group()
    make_stream(group, "s")
    base = now_ms() - 10_000
    put(logs, group, "s", [(base + i, f"m{i}") for i in range(5)])
    assert filtered(logs, group, startTime=base + 1, endTime=base + 3) == ["m1", "m2", "m3"]


def test_filter_pages_are_capped_by_limit_and_end_without_a_token(
    logs: Any, make_group: Callable[..., str], make_stream: Callable[[str, str], str]
) -> None:
    group = make_group()
    make_stream(group, "s")
    base = now_ms() - 60_000
    put(logs, group, "s", [(base + i, f"m{i:02d}") for i in range(25)])

    first = logs.filter_log_events(logGroupName=group, limit=20)
    assert len(first["events"]) == 20
    assert first["nextToken"]
    second = logs.filter_log_events(logGroupName=group, limit=20, nextToken=first["nextToken"])
    assert len(first["events"]) + len(second["events"]) == 25
    assert [e["message"] for e in first["events"] + second["events"]] == [f"m{i:02d}" for i in range(25)]
    assert len({e["eventId"] for e in first["events"] + second["events"]}) == 25
    assert "nextToken" not in second


def test_filter_rejects_bad_tokens_and_limits(logs: Any, mixed_group: str) -> None:
    with raises("InvalidParameterException") as exc:
        logs.filter_log_events(logGroupName=mixed_group, nextToken="not-a-token")
    assert exc.value.response["Error"]["Message"] == "The specified nextToken is invalid."
    with raises("InvalidParameterException") as exc:
        logs.filter_log_events(logGroupName=mixed_group, limit=10001)
    assert exc.value.response["Error"]["Message"] == (
        "1 validation error detected: Value '10001' at 'limit' failed to satisfy constraint: "
        "Member must have value less than or equal to 10000"
    )


def test_filter_on_a_missing_group(logs: Any) -> None:
    with raises("ResourceNotFoundException"):
        logs.filter_log_events(logGroupName=uniq("/conf/missing"))


def test_filter_interleaves_streams_by_timestamp(logs: Any, make_group: Callable[..., str], make_stream: Callable[[str, str], str]) -> None:
    group = make_group()
    make_stream(group, "a")
    make_stream(group, "b")
    base = now_ms() - 10_000
    put(logs, group, "a", [(base, "a0"), (base + 20, "a2")])
    put(logs, group, "b", [(base + 10, "b1"), (base + 30, "b3")])
    out = logs.filter_log_events(logGroupName=group)["events"]
    assert [(e["message"], e["logStreamName"]) for e in out] == [("a0", "a"), ("b1", "b"), ("a2", "a"), ("b3", "b")]


def test_retention_policy_values(logs: Any, make_group: Callable[..., str]) -> None:
    group = make_group()
    logs.put_retention_policy(logGroupName=group, retentionInDays=7)
    assert logs.describe_log_groups(logGroupNamePrefix=group)["logGroups"][0]["retentionInDays"] == 7
    with raises("InvalidParameterException"):
        logs.put_retention_policy(logGroupName=group, retentionInDays=8)
    logs.delete_retention_policy(logGroupName=group)
    assert "retentionInDays" not in logs.describe_log_groups(logGroupNamePrefix=group)["logGroups"][0]


def test_events_expire_when_the_retention_period_passes_on_the_emulator_clock(
    logs: Any, make_group: Callable[..., str], make_stream: Callable[[str, str], str], clock: Admin
) -> None:
    group = make_group()
    make_stream(group, "s")
    logs.put_retention_policy(logGroupName=group, retentionInDays=1)
    t = now_ms()
    put(logs, group, "s", [(t, "short-lived")])
    assert filtered(logs, group) == ["short-lived"]

    clock.advance(DAY_MS / 1000 - 600)
    assert filtered(logs, group) == ["short-lived"]
    clock.advance(1200)
    assert filtered(logs, group) == []
    assert logs.get_log_events(logGroupName=group, logStreamName="s", startFromHead=True)["events"] == []


def test_events_older_than_the_retention_period_are_rejected_at_ingestion(
    logs: Any, make_group: Callable[..., str], make_stream: Callable[[str, str], str]
) -> None:
    group = make_group()
    make_stream(group, "s")
    logs.put_retention_policy(logGroupName=group, retentionInDays=1)
    t = now_ms()
    # A batch may span at most 24 hours, so the expired event is exactly one
    # retention period older than the kept one: past it by the time the
    # server reads the clock.
    out = put(logs, group, "s", [(t - DAY_MS, "expired"), (t, "kept")])
    assert out["rejectedLogEventsInfo"]["expiredLogEventEndIndex"] == 1
    assert filtered(logs, group) == ["kept"]


def test_log_group_tags_are_shared_between_both_tag_apis(logs: Any, make_group: Callable[..., str]) -> None:
    group = make_group(tags={"env": "test"})
    arn = logs.describe_log_groups(logGroupNamePrefix=group)["logGroups"][0]["logGroupArn"]
    assert logs.list_tags_log_group(logGroupName=group)["tags"] == {"env": "test"}

    logs.tag_log_group(logGroupName=group, tags={"team": "pipeline"})
    logs.tag_resource(resourceArn=arn, tags={"owner": "me"})
    expected = {"env": "test", "team": "pipeline", "owner": "me"}
    assert logs.list_tags_log_group(logGroupName=group)["tags"] == expected
    assert logs.list_tags_for_resource(resourceArn=arn)["tags"] == expected

    logs.untag_log_group(logGroupName=group, tags=["env"])
    logs.untag_resource(resourceArn=arn, tagKeys=["owner"])
    assert logs.list_tags_for_resource(resourceArn=arn)["tags"] == {"team": "pipeline"}


def test_messages_round_trip_byte_exact(logs: Any, make_group: Callable[..., str], make_stream: Callable[[str, str], str]) -> None:
    group = make_group()
    make_stream(group, "s")
    messages = ["multi\nline\tmessage", json.dumps({"k": ["v", 1, None]}), "unicode é中\U0001f600", " leading and trailing "]
    t = now_ms()
    put(logs, group, "s", [(t + i, m) for i, m in enumerate(messages)])
    got = [e["message"] for e in logs.get_log_events(logGroupName=group, logStreamName="s", startFromHead=True)["events"]]
    assert got == messages
