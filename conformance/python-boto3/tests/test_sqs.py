"""SQS data plane and control plane (docs/research/phase1/sqs.md).

This module also runs under the pinned botocore that still speaks the Query
protocol to SQS (see README.md). Keep every assertion protocol-neutral.
"""

from __future__ import annotations

import hashlib
import json
import re
import threading
import time
from collections.abc import Callable
from typing import Any

import pytest
from botocore.exceptions import ClientError

from support import Admin, error_code, eventually, md5_hex, sqs_attribute_md5, status_of, uniq

def system_attributes_param(sqs: Any) -> str:
    """The ReceiveMessage member that selects system attributes in the installed botocore.

    The pinned Query-protocol botocore predates MessageSystemAttributeNames and only
    has the older AttributeNames, which AWS still honors (sqs.md 7.1).
    """
    members = sqs.meta.service_model.operation_model("ReceiveMessage").input_shape.members
    return "MessageSystemAttributeNames" if "MessageSystemAttributeNames" in members else "AttributeNames"


def receive(sqs: Any, url: str, n: int = 1, **kwargs: Any) -> list[dict[str, Any]]:
    kwargs.setdefault(system_attributes_param(sqs), ["All"])
    return sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=n, **kwargs).get("Messages", [])


def receive_until(sqs: Any, url: str, count: int, *, timeout: float = 15.0, **kwargs: Any) -> list[dict[str, Any]]:
    """Receive and delete until `count` messages were seen, in arrival order."""
    seen: list[dict[str, Any]] = []

    def step() -> list[dict[str, Any]]:
        for m in receive(sqs, url, 10, WaitTimeSeconds=1, **kwargs):
            seen.append(m)
            sqs.delete_message(QueueUrl=url, ReceiptHandle=m["ReceiptHandle"])
        return seen

    return eventually(step, lambda s: len(s) >= count, timeout=timeout, interval=0.0, what=f"{count} messages on {url}")


# --- Queues -----------------------------------------------------------------------------------


def test_queue_url_is_account_slash_name(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-url")
    assert re.fullmatch(r"https?://[^/]+/\d{12}/conf-url-[0-9a-f]{10}", url), url
    assert sqs.get_queue_url(QueueName=url.rsplit("/", 1)[1])["QueueUrl"] == url


def test_list_queues_filters_by_prefix_and_paginates(sqs: Any, make_queue: Callable[..., str]) -> None:
    prefix = uniq("conf-list")
    urls = {make_queue(f"{prefix}-{i}") for i in range(5)}
    make_queue("conf-other")

    listed: list[str] = []
    for page in sqs.get_paginator("list_queues").paginate(QueueNamePrefix=prefix, PaginationConfig={"PageSize": 2}):
        assert len(page.get("QueueUrls", [])) <= 2
        listed.extend(page.get("QueueUrls", []))
    assert sorted(listed) == sorted(urls)


def test_list_queues_with_no_match_has_no_urls(sqs: Any) -> None:
    out = sqs.list_queues(QueueNamePrefix=uniq("conf-nothing-here"))
    assert "QueueUrls" not in out


def test_create_queue_is_idempotent_for_equal_attributes(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-idem", attributes={"DelaySeconds": "5"})
    name = url.rsplit("/", 1)[1]
    assert sqs.create_queue(QueueName=name)["QueueUrl"] == url
    assert sqs.create_queue(QueueName=name, Attributes={"DelaySeconds": "5"})["QueueUrl"] == url


def test_create_queue_with_a_different_attribute_value_fails(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-idem2", attributes={"DelaySeconds": "5"})
    with pytest.raises(ClientError) as exc:
        sqs.create_queue(QueueName=url.rsplit("/", 1)[1], Attributes={"DelaySeconds": "6"})
    assert error_code(exc.value) == "QueueAlreadyExists"
    assert status_of(exc.value) == 400


def test_missing_queue_is_non_existent_queue(sqs: Any) -> None:
    with pytest.raises(ClientError) as exc:
        sqs.get_queue_url(QueueName=uniq("conf-missing"))
    assert error_code(exc.value) == "AWS.SimpleQueueService.NonExistentQueue"
    assert status_of(exc.value) == 400
    assert isinstance(exc.value, sqs.exceptions.QueueDoesNotExist)


def test_operations_on_a_deleted_queue_fail(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-gone")
    sqs.delete_queue(QueueUrl=url)
    with pytest.raises(ClientError) as exc:
        sqs.send_message(QueueUrl=url, MessageBody="x")
    assert error_code(exc.value) == "AWS.SimpleQueueService.NonExistentQueue"


def test_fresh_standard_queue_attributes(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-attrs")
    attrs = sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["All"])["Attributes"]
    assert set(attrs) == {
        "ApproximateNumberOfMessages",
        "ApproximateNumberOfMessagesDelayed",
        "ApproximateNumberOfMessagesNotVisible",
        "CreatedTimestamp",
        "DelaySeconds",
        "LastModifiedTimestamp",
        "MaximumMessageSize",
        "MessageRetentionPeriod",
        "QueueArn",
        "ReceiveMessageWaitTimeSeconds",
        "SqsManagedSseEnabled",
        "VisibilityTimeout",
    }
    assert attrs["VisibilityTimeout"] == "30"
    assert attrs["MaximumMessageSize"] == "1048576"
    assert attrs["MessageRetentionPeriod"] == "345600"
    assert attrs["DelaySeconds"] == "0"
    assert attrs["ReceiveMessageWaitTimeSeconds"] == "0"
    assert attrs["SqsManagedSseEnabled"] == "true"
    assert re.fullmatch(r"arn:aws:sqs:us-east-1:\d{12}:" + re.escape(url.rsplit("/", 1)[1]), attrs["QueueArn"])


def test_get_queue_attributes_without_names_returns_nothing(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-noattr")
    assert not sqs.get_queue_attributes(QueueUrl=url).get("Attributes")


def test_unknown_attribute_name_is_rejected(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-badattr")
    with pytest.raises(ClientError) as exc:
        sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["Foobar"])
    assert error_code(exc.value) == "InvalidAttributeName"
    assert exc.value.response["Error"]["Message"] == "Unknown Attribute Foobar."


def test_set_queue_attributes_updates_values(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-setattr")
    before = sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["LastModifiedTimestamp"])["Attributes"]
    sqs.set_queue_attributes(QueueUrl=url, Attributes={"VisibilityTimeout": "77", "MessageRetentionPeriod": "120"})
    attrs = sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["All"])["Attributes"]
    assert attrs["VisibilityTimeout"] == "77"
    assert attrs["MessageRetentionPeriod"] == "120"
    assert int(attrs["LastModifiedTimestamp"]) >= int(before["LastModifiedTimestamp"])


def test_set_queue_attributes_rejects_an_out_of_range_value(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-badval")
    with pytest.raises(ClientError) as exc:
        sqs.set_queue_attributes(QueueUrl=url, Attributes={"VisibilityTimeout": "43201"})
    assert error_code(exc.value) == "InvalidAttributeValue"


def test_queue_tags(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-tags", tags={"team": "pipeline"})
    assert sqs.list_queue_tags(QueueUrl=url)["Tags"] == {"team": "pipeline"}
    sqs.tag_queue(QueueUrl=url, Tags={"env": "test", "team": "core"})
    assert sqs.list_queue_tags(QueueUrl=url)["Tags"] == {"team": "core", "env": "test"}
    sqs.untag_queue(QueueUrl=url, TagKeys=["env"])
    assert sqs.list_queue_tags(QueueUrl=url)["Tags"] == {"team": "core"}


def test_standard_queue_name_cannot_end_in_fifo(sqs: Any) -> None:
    with pytest.raises(ClientError) as exc:
        sqs.create_queue(QueueName=uniq("conf-bad") + ".fifo")
    assert error_code(exc.value) == "InvalidParameterValue"


def test_fifo_queue_name_must_end_in_fifo(sqs: Any) -> None:
    with pytest.raises(ClientError) as exc:
        sqs.create_queue(QueueName=uniq("conf-bad"), Attributes={"FifoQueue": "true"})
    assert error_code(exc.value) == "InvalidParameterValue"


# --- Send, receive, delete ----------------------------------------------------------------------


def test_send_receive_delete_round_trip(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-rt")
    sent = sqs.send_message(QueueUrl=url, MessageBody="hello world")
    assert sent["MD5OfMessageBody"] == md5_hex(b"hello world")
    assert "MD5OfMessageAttributes" not in sent
    assert "SequenceNumber" not in sent
    assert re.fullmatch(r"[0-9a-f-]{36}", sent["MessageId"])

    [msg] = receive(sqs, url)
    assert msg["Body"] == "hello world"
    assert msg["MessageId"] == sent["MessageId"]
    assert msg["MD5OfBody"] == sent["MD5OfMessageBody"]
    assert msg["Attributes"]["ApproximateReceiveCount"] == "1"
    assert msg["Attributes"]["SenderId"]
    assert abs(int(msg["Attributes"]["SentTimestamp"]) / 1000 - time.time()) < 120
    assert abs(int(msg["Attributes"]["ApproximateFirstReceiveTimestamp"]) / 1000 - time.time()) < 120

    sqs.delete_message(QueueUrl=url, ReceiptHandle=msg["ReceiptHandle"])
    counts = sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["All"])["Attributes"]
    assert counts["ApproximateNumberOfMessages"] == "0"
    assert counts["ApproximateNumberOfMessagesNotVisible"] == "0"


def test_empty_receive_has_no_messages_key(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-empty")
    assert "Messages" not in sqs.receive_message(QueueUrl=url)


def test_message_body_comes_back_byte_exact(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-body")
    body = 'line1\r\nline2 &amp; <b>"quoted"</b> é中\U0001f600\ttab'
    sqs.send_message(QueueUrl=url, MessageBody=body)
    [msg] = receive(sqs, url)
    assert msg["Body"] == body
    assert msg["MD5OfBody"] == md5_hex(body.encode())


def test_receive_returns_at_most_max_number_of_messages(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-max")
    for i in range(6):
        sqs.send_message(QueueUrl=url, MessageBody=f"m{i}")
    got = eventually(lambda: receive(sqs, url, 4), lambda m: len(m) >= 1, what="a first receive")
    assert 1 <= len(got) <= 4
    rest = receive_until(sqs, url, 6 - len(got))
    assert sorted(m["Body"] for m in [*got, *rest]) == [f"m{i}" for i in range(6)]


@pytest.mark.parametrize("n", [0, 11])
def test_max_number_of_messages_out_of_range(sqs: Any, make_queue: Callable[..., str], n: int) -> None:
    url = make_queue("conf-maxbad")
    with pytest.raises(ClientError) as exc:
        sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=n)
    assert error_code(exc.value) == "InvalidParameterValue"
    assert exc.value.response["Error"]["Message"] == (
        f"Value {n} for parameter MaxNumberOfMessages is invalid. Reason: Must be between 1 and 10, if provided."
    )


def test_wait_time_seconds_out_of_range(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-waitbad")
    with pytest.raises(ClientError) as exc:
        sqs.receive_message(QueueUrl=url, WaitTimeSeconds=21)
    assert error_code(exc.value) == "InvalidParameterValue"
    assert exc.value.response["Error"]["Message"] == (
        "Value 21 for parameter WaitTimeSeconds is invalid. Reason: Must be >= 0 and <= 20, if provided."
    )


def test_message_larger_than_the_queue_maximum_is_rejected(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-big", attributes={"MaximumMessageSize": "1024"})
    sqs.send_message(QueueUrl=url, MessageBody="a" * 1024)
    with pytest.raises(ClientError) as exc:
        sqs.send_message(QueueUrl=url, MessageBody="a" * 1025)
    assert error_code(exc.value) == "InvalidParameterValue"
    assert exc.value.response["Error"]["Message"] == (
        "One or more parameters are invalid. Reason: Message must be shorter than 1024 bytes."
    )


def test_message_attributes_round_trip_with_exact_md5(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-attr")
    attributes = {
        "Str": {"DataType": "String", "StringValue": "value"},
        "Num": {"DataType": "Number", "StringValue": "42.5"},
        "Bin": {"DataType": "Binary", "BinaryValue": b"\x00\x01\xfe\xff"},
        "Custom": {"DataType": "String.custom", "StringValue": "typed"},
    }
    sent = sqs.send_message(QueueUrl=url, MessageBody="b", MessageAttributes=attributes)
    assert sent["MD5OfMessageAttributes"] == sqs_attribute_md5(attributes)

    [msg] = receive(sqs, url, MessageAttributeNames=["All"])
    assert msg["MD5OfMessageAttributes"] == sent["MD5OfMessageAttributes"]
    assert msg["MessageAttributes"]["Str"] == {"DataType": "String", "StringValue": "value"}
    assert msg["MessageAttributes"]["Num"] == {"DataType": "Number", "StringValue": "42.5"}
    assert msg["MessageAttributes"]["Bin"] == {"DataType": "Binary", "BinaryValue": b"\x00\x01\xfe\xff"}
    assert msg["MessageAttributes"]["Custom"] == {"DataType": "String.custom", "StringValue": "typed"}


def test_receive_filters_message_attributes_by_name_and_prefix(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-attrnames")
    attributes = {
        "Hello": {"DataType": "String", "StringValue": "1"},
        "Help.Me": {"DataType": "String", "StringValue": "2"},
        "Other": {"DataType": "String", "StringValue": "3"},
    }
    for _ in range(3):
        sqs.send_message(QueueUrl=url, MessageBody="b", MessageAttributes=attributes)

    [by_name] = receive(sqs, url, MessageAttributeNames=["Other"], VisibilityTimeout=0)
    assert set(by_name["MessageAttributes"]) == {"Other"}
    assert by_name["MD5OfMessageAttributes"] == sqs_attribute_md5({"Other": attributes["Other"]})

    [by_prefix] = receive(sqs, url, MessageAttributeNames=["Hel.*"], VisibilityTimeout=0)
    assert set(by_prefix["MessageAttributes"]) == {"Hello", "Help.Me"}

    [none_asked] = receive(sqs, url, VisibilityTimeout=0)
    assert "MessageAttributes" not in none_asked


def test_system_attribute_names_select_what_is_returned(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-sysattr")
    sqs.send_message(QueueUrl=url, MessageBody="b")
    [bare] = sqs.receive_message(QueueUrl=url, VisibilityTimeout=0)["Messages"]
    assert "Attributes" not in bare
    [only_sender] = sqs.receive_message(
        QueueUrl=url, VisibilityTimeout=0, **{system_attributes_param(sqs): ["SenderId", "SequenceNumber"]}
    )["Messages"]
    # SequenceNumber does not apply to a standard queue and is silently omitted.
    assert set(only_sender["Attributes"]) == {"SenderId"}


def test_aws_trace_header_system_attribute(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-trace")
    trace = "Root=1-5759e988-bd862e3fe1be46a994272793;Parent=53995c3f42cd8ad8;Sampled=1"
    sent = sqs.send_message(
        QueueUrl=url,
        MessageBody="b",
        MessageSystemAttributes={"AWSTraceHeader": {"DataType": "String", "StringValue": trace}},
    )
    assert "MD5OfMessageSystemAttributes" in sent
    assert "MD5OfMessageAttributes" not in sent
    [msg] = receive(sqs, url)
    assert msg["Attributes"]["AWSTraceHeader"] == trace


def test_receive_count_increases_and_handles_differ(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-count")
    sqs.send_message(QueueUrl=url, MessageBody="b")
    [first] = receive(sqs, url, VisibilityTimeout=0)
    [second] = receive(sqs, url, VisibilityTimeout=0)
    assert first["MessageId"] == second["MessageId"]
    assert (first["Attributes"]["ApproximateReceiveCount"], second["Attributes"]["ApproximateReceiveCount"]) == ("1", "2")
    assert first["ReceiptHandle"] != second["ReceiptHandle"]


def test_delete_with_a_garbage_receipt_handle(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-garbage")
    with pytest.raises(ClientError) as exc:
        sqs.delete_message(QueueUrl=url, ReceiptHandle="garbage")
    assert error_code(exc.value) == "ReceiptHandleIsInvalid"
    assert exc.value.response["Error"]["Message"] == 'The input receipt handle "garbage" is not a valid receipt handle.'


def test_deleting_twice_succeeds(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-deltwice")
    sqs.send_message(QueueUrl=url, MessageBody="b")
    [msg] = receive(sqs, url)
    sqs.delete_message(QueueUrl=url, ReceiptHandle=msg["ReceiptHandle"])
    sqs.delete_message(QueueUrl=url, ReceiptHandle=msg["ReceiptHandle"])


# --- Batches ----------------------------------------------------------------------------------


def test_send_message_batch_delivers_every_entry(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-batch")
    entries = [{"Id": f"e{i}", "MessageBody": f"body-{i}"} for i in range(10)]
    out = sqs.send_message_batch(QueueUrl=url, Entries=entries)
    assert not out.get("Failed")
    assert sorted(s["Id"] for s in out["Successful"]) == sorted(e["Id"] for e in entries)
    for s in out["Successful"]:
        assert s["MD5OfMessageBody"] == md5_hex(f"body-{s['Id'][1:]}".encode())
    got = receive_until(sqs, url, 10)
    assert sorted(m["Body"] for m in got) == sorted(e["MessageBody"] for e in entries)


def test_send_message_batch_reports_a_too_large_entry_per_entry(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-batchbig", attributes={"MaximumMessageSize": "1024"})
    out = sqs.send_message_batch(
        QueueUrl=url,
        Entries=[{"Id": "small", "MessageBody": "ok"}, {"Id": "large", "MessageBody": "a" * 1025}],
    )
    assert [s["Id"] for s in out["Successful"]] == ["small"]
    [failed] = out["Failed"]
    assert failed["Id"] == "large"
    assert failed["SenderFault"] is True
    assert failed["Code"] == "InvalidParameterValue"
    assert failed["Message"] == "One or more parameters cannot be validated. Reason: Message must be shorter than 1024 bytes."


def test_send_message_batch_entry_attributes_are_kept(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-batchattr")
    attrs = {"k": {"DataType": "String", "StringValue": "v"}}
    out = sqs.send_message_batch(QueueUrl=url, Entries=[{"Id": "a", "MessageBody": "b", "MessageAttributes": attrs}])
    assert out["Successful"][0]["MD5OfMessageAttributes"] == sqs_attribute_md5(attrs)
    [msg] = receive(sqs, url, MessageAttributeNames=["All"])
    assert msg["MessageAttributes"]["k"]["StringValue"] == "v"


def test_batch_with_no_entries(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-batch0")
    with pytest.raises(ClientError) as exc:
        sqs.send_message_batch(QueueUrl=url, Entries=[])
    if sqs.meta.service_model.protocol == "query":
        # The Query serializer sends nothing for an empty list, so the server sees a missing
        # parameter; sqs.md does not record which code AWS answers there.
        assert status_of(exc.value) == 400
    else:
        assert error_code(exc.value) == "AWS.SimpleQueueService.EmptyBatchRequest"


def test_batch_with_more_than_ten_entries(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-batch11")
    with pytest.raises(ClientError) as exc:
        sqs.send_message_batch(QueueUrl=url, Entries=[{"Id": f"e{i}", "MessageBody": "b"} for i in range(11)])
    assert error_code(exc.value) == "AWS.SimpleQueueService.TooManyEntriesInBatchRequest"
    assert exc.value.response["Error"]["Message"] == "Maximum number of entries per request are 10. You have sent 11."


def test_batch_with_repeated_ids(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-batchdup")
    with pytest.raises(ClientError) as exc:
        sqs.send_message_batch(QueueUrl=url, Entries=[{"Id": "x", "MessageBody": "1"}, {"Id": "x", "MessageBody": "2"}])
    assert error_code(exc.value) == "AWS.SimpleQueueService.BatchEntryIdsNotDistinct"


@pytest.mark.parametrize("bad_id", ["invalid:id", "", "x" * 81])
def test_batch_with_an_invalid_entry_id(sqs: Any, make_queue: Callable[..., str], bad_id: str) -> None:
    url = make_queue("conf-batchid")
    with pytest.raises(ClientError) as exc:
        sqs.send_message_batch(QueueUrl=url, Entries=[{"Id": bad_id, "MessageBody": "b"}])
    assert error_code(exc.value) == "AWS.SimpleQueueService.InvalidBatchEntryId"
    assert exc.value.response["Error"]["Message"] == (
        "A batch entry id can only contain alphanumeric characters, hyphens and underscores. "
        "It can be at most 80 letters long."
    )


def test_delete_message_batch_reports_each_entry(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-delbatch")
    sqs.send_message_batch(QueueUrl=url, Entries=[{"Id": f"e{i}", "MessageBody": f"b{i}"} for i in range(3)])
    msgs = eventually(lambda: receive(sqs, url, 10), lambda m: len(m) == 3, what="3 messages")

    entries = [{"Id": f"d{i}", "ReceiptHandle": m["ReceiptHandle"]} for i, m in enumerate(msgs)]
    out = sqs.delete_message_batch(QueueUrl=url, Entries=entries)
    assert sorted(s["Id"] for s in out["Successful"]) == ["d0", "d1", "d2"]
    assert not out.get("Failed")
    assert sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["ApproximateNumberOfMessagesNotVisible"])[
        "Attributes"
    ]["ApproximateNumberOfMessagesNotVisible"] == "0"


def test_delete_message_batch_rejects_bad_ids(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-delbatchbad")
    with pytest.raises(ClientError) as exc:
        sqs.delete_message_batch(QueueUrl=url, Entries=[{"Id": "bad:id", "ReceiptHandle": "h"}])
    assert error_code(exc.value) == "AWS.SimpleQueueService.InvalidBatchEntryId"


def test_change_message_visibility_batch(sqs: Any, make_queue: Callable[..., str], clock: Admin) -> None:
    url = make_queue("conf-cmvbatch")
    sqs.send_message_batch(QueueUrl=url, Entries=[{"Id": f"e{i}", "MessageBody": f"b{i}"} for i in range(2)])
    msgs = eventually(lambda: receive(sqs, url, 10, VisibilityTimeout=60), lambda m: len(m) == 2, what="2 messages")

    out = sqs.change_message_visibility_batch(
        QueueUrl=url,
        Entries=[
            {"Id": "zero", "ReceiptHandle": msgs[0]["ReceiptHandle"], "VisibilityTimeout": 0},
            {"Id": "long", "ReceiptHandle": msgs[1]["ReceiptHandle"], "VisibilityTimeout": 600},
        ],
    )
    assert sorted(s["Id"] for s in out["Successful"]) == ["long", "zero"]

    # The first message is receivable right away; the second stays hidden past its old timeout.
    [again] = receive(sqs, url, 10, VisibilityTimeout=60)
    assert again["MessageId"] == msgs[0]["MessageId"]
    clock.advance(120)
    visible = receive(sqs, url, 10, VisibilityTimeout=60)
    assert [m["MessageId"] for m in visible] == [msgs[0]["MessageId"]], "the extended message became visible too early"


# --- Visibility, delay, retention on the emulator clock -----------------------------------------


def test_visibility_timeout_expires_on_the_emulator_clock(sqs: Any, make_queue: Callable[..., str], clock: Admin) -> None:
    url = make_queue("conf-vis")
    sqs.send_message(QueueUrl=url, MessageBody="b")
    [first] = receive(sqs, url, VisibilityTimeout=30)
    assert receive(sqs, url) == [], "a message in flight must not be delivered again"

    clock.advance(29)
    assert receive(sqs, url) == [], "message became visible before its timeout elapsed"
    clock.advance(2)
    [second] = receive(sqs, url, VisibilityTimeout=30)
    assert second["MessageId"] == first["MessageId"]
    assert second["Attributes"]["ApproximateReceiveCount"] == "2"
    assert second["ReceiptHandle"] != first["ReceiptHandle"]


def test_in_flight_messages_are_counted_not_visible(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-inflight")
    for i in range(3):
        sqs.send_message(QueueUrl=url, MessageBody=f"m{i}")
    got = eventually(lambda: receive(sqs, url, 2, VisibilityTimeout=300), lambda m: len(m) >= 1, what="a receive")
    counts = sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["All"])["Attributes"]
    assert int(counts["ApproximateNumberOfMessagesNotVisible"]) == len(got)
    assert int(counts["ApproximateNumberOfMessages"]) == 3 - len(got)


def test_visibility_timeout_on_receive_does_not_change_the_queue_default(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-visdefault")
    sqs.send_message(QueueUrl=url, MessageBody="b")
    receive(sqs, url, VisibilityTimeout=5)
    attrs = sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["VisibilityTimeout"])["Attributes"]
    assert attrs["VisibilityTimeout"] == "30"


def test_queue_default_visibility_timeout_applies(sqs: Any, make_queue: Callable[..., str], clock: Admin) -> None:
    url = make_queue("conf-visq", attributes={"VisibilityTimeout": "100"})
    sqs.send_message(QueueUrl=url, MessageBody="b")
    receive(sqs, url)
    clock.advance(99)
    assert receive(sqs, url) == []
    clock.advance(2)
    assert len(receive(sqs, url)) == 1


def test_change_message_visibility_extends_and_resets(sqs: Any, make_queue: Callable[..., str], clock: Admin) -> None:
    url = make_queue("conf-cmv")
    sqs.send_message(QueueUrl=url, MessageBody="b")
    [msg] = receive(sqs, url, VisibilityTimeout=30)

    sqs.change_message_visibility(QueueUrl=url, ReceiptHandle=msg["ReceiptHandle"], VisibilityTimeout=300)
    clock.advance(200)
    assert receive(sqs, url) == []
    clock.advance(101)
    [again] = receive(sqs, url, VisibilityTimeout=300)

    sqs.change_message_visibility(QueueUrl=url, ReceiptHandle=again["ReceiptHandle"], VisibilityTimeout=0)
    [third] = receive(sqs, url)
    assert third["MessageId"] == msg["MessageId"]


def test_delayed_message_is_hidden_until_the_delay_passes(sqs: Any, make_queue: Callable[..., str], clock: Admin) -> None:
    url = make_queue("conf-delay")
    sqs.send_message(QueueUrl=url, MessageBody="later", DelaySeconds=60)
    counts = sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["All"])["Attributes"]
    assert counts["ApproximateNumberOfMessagesDelayed"] == "1"
    assert counts["ApproximateNumberOfMessages"] == "0"
    assert receive(sqs, url) == []

    clock.advance(59)
    assert receive(sqs, url) == []
    clock.advance(2)
    [msg] = receive(sqs, url)
    assert msg["Body"] == "later"


def test_queue_delay_applies_to_every_message(sqs: Any, make_queue: Callable[..., str], clock: Admin) -> None:
    url = make_queue("conf-qdelay", attributes={"DelaySeconds": "120"})
    sqs.send_message(QueueUrl=url, MessageBody="b")
    assert receive(sqs, url) == []
    clock.advance(121)
    assert len(receive(sqs, url)) == 1


def test_messages_expire_after_the_retention_period(sqs: Any, make_queue: Callable[..., str], clock: Admin) -> None:
    url = make_queue("conf-retention", attributes={"MessageRetentionPeriod": "60"})
    sqs.send_message(QueueUrl=url, MessageBody="b")
    clock.advance(59)
    assert len(receive(sqs, url, VisibilityTimeout=0)) == 1
    clock.advance(2)
    assert receive(sqs, url) == []
    counts = sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["ApproximateNumberOfMessages"])["Attributes"]
    assert counts["ApproximateNumberOfMessages"] == "0"


def test_purge_queue_removes_messages_and_limits_repeat_purges(
    sqs: Any, make_queue: Callable[..., str], clock: Admin
) -> None:
    url = make_queue("conf-purge")
    for i in range(3):
        sqs.send_message(QueueUrl=url, MessageBody=f"m{i}")
    sqs.purge_queue(QueueUrl=url)
    assert receive(sqs, url) == []

    with pytest.raises(ClientError) as exc:
        sqs.purge_queue(QueueUrl=url)
    assert error_code(exc.value) == "AWS.SimpleQueueService.PurgeQueueInProgress"
    assert status_of(exc.value) == 403

    clock.advance(61)
    sqs.send_message(QueueUrl=url, MessageBody="after")
    sqs.purge_queue(QueueUrl=url)
    assert receive(sqs, url) == []


# --- Long polling -----------------------------------------------------------------------------


def test_long_poll_returns_as_soon_as_a_message_arrives(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-longpoll")
    threading.Timer(0.7, lambda: sqs.send_message(QueueUrl=url, MessageBody="arrived")).start()
    started = time.monotonic()
    msgs = receive(sqs, url, WaitTimeSeconds=10)
    elapsed = time.monotonic() - started
    assert [m["Body"] for m in msgs] == ["arrived"]
    assert 0.5 <= elapsed < 6, elapsed


def test_long_poll_on_an_empty_queue_waits_then_returns_nothing(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-longempty")
    started = time.monotonic()
    out = sqs.receive_message(QueueUrl=url, WaitTimeSeconds=2)
    elapsed = time.monotonic() - started
    assert "Messages" not in out
    assert 1.7 <= elapsed < 6, elapsed


def test_queue_wait_time_is_the_default_and_zero_overrides_it(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-qwait", attributes={"ReceiveMessageWaitTimeSeconds": "2"})
    started = time.monotonic()
    sqs.receive_message(QueueUrl=url)
    assert time.monotonic() - started >= 1.7
    started = time.monotonic()
    sqs.receive_message(QueueUrl=url, WaitTimeSeconds=0)
    assert time.monotonic() - started < 1.0


def test_delayed_message_wakes_a_long_poll(sqs: Any, make_queue: Callable[..., str], clock: Admin) -> None:
    url = make_queue("conf-delaywake")
    sqs.send_message(QueueUrl=url, MessageBody="b", DelaySeconds=30)
    threading.Timer(0.5, lambda: clock.advance(31)).start()
    msgs = receive(sqs, url, WaitTimeSeconds=10)
    assert [m["Body"] for m in msgs] == ["b"]


# --- Dead-letter queues and redrive ------------------------------------------------------------


def redrive_policy(dlq_arn: str, max_receive_count: int) -> str:
    return json.dumps({"deadLetterTargetArn": dlq_arn, "maxReceiveCount": max_receive_count})


def test_message_moves_to_the_dlq_after_max_receive_count(
    sqs: Any, make_queue: Callable[..., str], queue_arn: Callable[[str], str]
) -> None:
    dlq = make_queue("conf-dlq")
    src = make_queue("conf-src", attributes={"RedrivePolicy": redrive_policy(queue_arn(dlq), 2)})
    sent = sqs.send_message(QueueUrl=src, MessageBody="poison")

    assert [m["Attributes"]["ApproximateReceiveCount"] for m in receive(sqs, src, VisibilityTimeout=0)] == ["1"]
    assert [m["Attributes"]["ApproximateReceiveCount"] for m in receive(sqs, src, VisibilityTimeout=0)] == ["2"]
    # The attempt that would be the third receive does not return the message.
    assert receive(sqs, src, VisibilityTimeout=0) == []

    [moved] = eventually(lambda: receive(sqs, dlq), lambda m: len(m) == 1, what="the message in the DLQ")
    assert moved["Body"] == "poison"
    assert moved["MessageId"] == sent["MessageId"]
    assert moved["Attributes"]["DeadLetterQueueSourceArn"] == queue_arn(src)
    assert moved["Attributes"]["ApproximateReceiveCount"] == "3"


def test_redrive_policy_is_reported_with_a_numeric_max_receive_count(
    sqs: Any, make_queue: Callable[..., str], queue_arn: Callable[[str], str]
) -> None:
    dlq = make_queue("conf-dlq2")
    src = make_queue("conf-src2", attributes={"RedrivePolicy": redrive_policy(queue_arn(dlq), 5)})
    policy = json.loads(sqs.get_queue_attributes(QueueUrl=src, AttributeNames=["RedrivePolicy"])["Attributes"]["RedrivePolicy"])
    assert policy == {"deadLetterTargetArn": queue_arn(dlq), "maxReceiveCount": 5}


def test_redrive_policy_requires_both_fields(sqs: Any, make_queue: Callable[..., str], queue_arn: Callable[[str], str]) -> None:
    dlq = make_queue("conf-dlq3")
    with pytest.raises(ClientError) as exc:
        make_queue("conf-src3", attributes={"RedrivePolicy": json.dumps({"deadLetterTargetArn": queue_arn(dlq)})})
    assert error_code(exc.value) == "InvalidParameterValue"


def test_list_dead_letter_source_queues(sqs: Any, make_queue: Callable[..., str], queue_arn: Callable[[str], str]) -> None:
    dlq = make_queue("conf-dlq4")
    src_a = make_queue("conf-srcA", attributes={"RedrivePolicy": redrive_policy(queue_arn(dlq), 3)})
    src_b = make_queue("conf-srcB", attributes={"RedrivePolicy": redrive_policy(queue_arn(dlq), 3)})
    make_queue("conf-unrelated")
    out = sqs.list_dead_letter_source_queues(QueueUrl=dlq)
    assert sorted(out["queueUrls"]) == sorted([src_a, src_b])


def test_dlq_redrive_move_task_returns_messages_to_the_source(
    sqs: Any, make_queue: Callable[..., str], queue_arn: Callable[[str], str]
) -> None:
    dlq = make_queue("conf-dlq5")
    src = make_queue("conf-src5", attributes={"RedrivePolicy": redrive_policy(queue_arn(dlq), 1)})
    for i in range(3):
        sqs.send_message(QueueUrl=src, MessageBody=f"poison-{i}")
    for _ in range(3):
        receive(sqs, src, 1, VisibilityTimeout=0)
    receive(sqs, src, 10, VisibilityTimeout=0)
    eventually(
        lambda: sqs.get_queue_attributes(QueueUrl=dlq, AttributeNames=["ApproximateNumberOfMessages"])["Attributes"],
        lambda a: a["ApproximateNumberOfMessages"] == "3",
        what="3 messages in the DLQ",
    )

    handle = sqs.start_message_move_task(SourceArn=queue_arn(dlq))["TaskHandle"]
    assert handle

    task = eventually(
        lambda: sqs.list_message_move_tasks(SourceArn=queue_arn(dlq))["Results"][0],
        lambda t: t["Status"] == "COMPLETED",
        what="the move task to complete",
    )
    assert task["ApproximateNumberOfMessagesMoved"] == 3
    assert task["ApproximateNumberOfMessagesToMove"] == 3
    assert task["SourceArn"] == queue_arn(dlq)

    back = receive_until(sqs, src, 3)
    assert sorted(m["Body"] for m in back) == [f"poison-{i}" for i in range(3)]
    assert all(m["Attributes"]["ApproximateReceiveCount"] == "1" for m in back)


def test_move_task_source_must_be_a_dlq(sqs: Any, make_queue: Callable[..., str], queue_arn: Callable[[str], str]) -> None:
    plain = make_queue("conf-notdlq")
    with pytest.raises(ClientError) as exc:
        sqs.start_message_move_task(SourceArn=queue_arn(plain))
    assert error_code(exc.value) == "InvalidParameterValue"
    assert exc.value.response["Error"]["Message"] == "Source queue must be configured as a Dead Letter Queue."


# --- FIFO ---------------------------------------------------------------------------------------


def test_fifo_queue_attributes(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-fifoattr", fifo=True)
    attrs = sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["All"])["Attributes"]
    assert attrs["FifoQueue"] == "true"
    assert attrs["DeduplicationScope"] == "queue"
    assert attrs["FifoThroughputLimit"] == "perQueue"
    assert attrs.get("ContentBasedDeduplication", "false") == "false"


def test_fifo_send_requires_a_group_id(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-fifogrp", fifo=True)
    with pytest.raises(ClientError) as exc:
        sqs.send_message(QueueUrl=url, MessageBody="b", MessageDeduplicationId="d")
    assert error_code(exc.value) == "MissingParameter"
    assert exc.value.response["Error"]["Message"] == "The request must contain the parameter MessageGroupId."


def test_fifo_send_requires_a_dedup_id_without_content_based_dedup(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-fifodedup", fifo=True)
    with pytest.raises(ClientError) as exc:
        sqs.send_message(QueueUrl=url, MessageBody="b", MessageGroupId="g")
    assert error_code(exc.value) == "InvalidParameterValue"
    assert exc.value.response["Error"]["Message"] == (
        "The queue should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly"
    )


def test_fifo_batch_entry_without_group_id_fails_the_whole_call(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-fifobatchgrp", fifo=True)
    with pytest.raises(ClientError) as exc:
        sqs.send_message_batch(
            QueueUrl=url,
            Entries=[
                {"Id": "ok", "MessageBody": "a", "MessageGroupId": "g", "MessageDeduplicationId": "1"},
                {"Id": "bad", "MessageBody": "b", "MessageDeduplicationId": "2"},
            ],
        )
    assert error_code(exc.value) == "InvalidParameterValue"
    assert exc.value.response["Error"]["Message"] == "The request must contain the parameter MessageGroupId."


def test_fifo_per_message_delay_is_not_allowed(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-fifodelay", fifo=True)
    with pytest.raises(ClientError) as exc:
        sqs.send_message(QueueUrl=url, MessageBody="b", MessageGroupId="g", MessageDeduplicationId="d", DelaySeconds=2)
    assert error_code(exc.value) == "InvalidParameterValue"


def test_fifo_returns_sequence_numbers_and_keeps_group_order(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-fifoorder", fifo=True)
    sequence: list[int] = []
    for i in range(5):
        sent = sqs.send_message(QueueUrl=url, MessageBody=f"m{i}", MessageGroupId="g", MessageDeduplicationId=f"d{i}")
        sequence.append(int(sent["SequenceNumber"]))
    assert sequence == sorted(set(sequence))

    msgs = receive(sqs, url, 10)
    assert [m["Body"] for m in msgs] == [f"m{i}" for i in range(5)]
    assert all(m["Attributes"]["MessageGroupId"] == "g" for m in msgs)
    assert [m["Attributes"]["MessageDeduplicationId"] for m in msgs] == [f"d{i}" for i in range(5)]
    assert [int(m["Attributes"]["SequenceNumber"]) for m in msgs] == sequence


def test_fifo_group_with_an_in_flight_message_is_closed(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-fifoblock", fifo=True)
    for i in range(2):
        sqs.send_message(QueueUrl=url, MessageBody=f"m{i}", MessageGroupId="g", MessageDeduplicationId=f"d{i}")
    [first] = receive(sqs, url, 1, VisibilityTimeout=300)
    assert first["Body"] == "m0"
    assert receive(sqs, url, 10) == [], "a group with an in-flight message must not deliver its next message"

    sqs.delete_message(QueueUrl=url, ReceiptHandle=first["ReceiptHandle"])
    [second] = receive(sqs, url, 10)
    assert second["Body"] == "m1"


def test_fifo_other_groups_stay_available_while_one_is_closed(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-fifogroups", fifo=True)
    sqs.send_message(QueueUrl=url, MessageBody="a1", MessageGroupId="A", MessageDeduplicationId="a1")
    sqs.send_message(QueueUrl=url, MessageBody="a2", MessageGroupId="A", MessageDeduplicationId="a2")
    sqs.send_message(QueueUrl=url, MessageBody="b1", MessageGroupId="B", MessageDeduplicationId="b1")
    first = receive(sqs, url, 1, VisibilityTimeout=300)
    assert len(first) == 1
    # Which group answers first is unspecified; the other group must answer next, whole.
    in_flight_group = first[0]["Attributes"]["MessageGroupId"]
    rest = receive(sqs, url, 10, VisibilityTimeout=300)
    assert [m["Body"] for m in rest] == (["b1"] if in_flight_group == "A" else ["a1", "a2"])


def test_fifo_deduplicates_by_dedup_id_within_five_minutes(
    sqs: Any, make_queue: Callable[..., str], clock: Admin
) -> None:
    url = make_queue("conf-fifowindow", fifo=True)
    sqs.send_message(QueueUrl=url, MessageBody="once", MessageGroupId="g", MessageDeduplicationId="same")
    sqs.send_message(QueueUrl=url, MessageBody="twice", MessageGroupId="g", MessageDeduplicationId="same")
    [only] = receive(sqs, url, 10)
    assert only["Body"] == "once"
    sqs.delete_message(QueueUrl=url, ReceiptHandle=only["ReceiptHandle"])

    # Tracking continues after the original was deleted.
    sqs.send_message(QueueUrl=url, MessageBody="third", MessageGroupId="g", MessageDeduplicationId="same")
    assert receive(sqs, url, 10) == []

    clock.advance(301)
    sqs.send_message(QueueUrl=url, MessageBody="fourth", MessageGroupId="g", MessageDeduplicationId="same")
    [late] = receive(sqs, url, 10)
    assert late["Body"] == "fourth"


def test_fifo_content_based_deduplication_hashes_the_body_only(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-fifocbd", fifo=True, attributes={"ContentBasedDeduplication": "true"})
    sqs.send_message(QueueUrl=url, MessageBody="same body", MessageGroupId="g")
    sqs.send_message(
        QueueUrl=url,
        MessageBody="same body",
        MessageGroupId="g",
        MessageAttributes={"a": {"DataType": "String", "StringValue": "ignored"}},
    )
    sqs.send_message(QueueUrl=url, MessageBody="other body", MessageGroupId="g")
    bodies = [m["Body"] for m in receive(sqs, url, 10)]
    assert bodies == ["same body", "other body"]


def test_fifo_dedup_scope_queue_spans_groups(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-fifoscopeq", fifo=True)
    sqs.send_message(QueueUrl=url, MessageBody="a", MessageGroupId="g1", MessageDeduplicationId="x")
    sqs.send_message(QueueUrl=url, MessageBody="b", MessageGroupId="g2", MessageDeduplicationId="x")
    assert [m["Body"] for m in receive(sqs, url, 10)] == ["a"]


def test_fifo_dedup_scope_message_group_is_per_group(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-fifoscopeg", fifo=True, attributes={"DeduplicationScope": "messageGroup"})
    sqs.send_message(QueueUrl=url, MessageBody="a", MessageGroupId="g1", MessageDeduplicationId="x")
    sqs.send_message(QueueUrl=url, MessageBody="b", MessageGroupId="g2", MessageDeduplicationId="x")
    sqs.send_message(QueueUrl=url, MessageBody="c", MessageGroupId="g1", MessageDeduplicationId="x")
    assert sorted(m["Body"] for m in receive(sqs, url, 10)) == ["a", "b"]


def test_fifo_dedup_hash_is_sha256_of_the_body(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-fifohash", fifo=True, attributes={"ContentBasedDeduplication": "true"})
    sqs.send_message(QueueUrl=url, MessageBody="Test", MessageGroupId="g")
    [msg] = receive(sqs, url)
    assert msg["Attributes"]["MessageDeduplicationId"] == hashlib.sha256(b"Test").hexdigest()


# --- Message group ids on standard queues ----------------------------------------------------------


def test_standard_queue_accepts_and_returns_a_message_group_id(sqs: Any, make_queue: Callable[..., str]) -> None:
    url = make_queue("conf-fair")
    sqs.send_message(QueueUrl=url, MessageBody="b", MessageGroupId="tenant-1")
    [msg] = receive(sqs, url)
    assert msg["Attributes"]["MessageGroupId"] == "tenant-1"
