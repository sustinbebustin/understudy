"""SNS (docs/research/phase1/sns.md): topics, subscriptions, Publish and PublishBatch, filter
policies in both scopes, raw and enveloped delivery to SQS, FIFO topics and subscription DLQs."""

from __future__ import annotations

import hashlib
import json
import re
from collections.abc import Callable
from typing import Any

import pytest

from support import Admin, eventually, raises, status_of, uniq

AttributeMap = dict[str, dict[str, Any]]


def string(value: str) -> dict[str, Any]:
    return {"DataType": "String", "StringValue": value}


def number(value: str) -> dict[str, Any]:
    return {"DataType": "Number", "StringValue": value}


@pytest.fixture
def make_topic(sns: Any, cleanup: Any) -> Callable[..., str]:
    def make(prefix: str = "conf-topic", *, fifo: bool = False, attributes: dict[str, str] | None = None) -> str:
        name = uniq(prefix) + (".fifo" if fifo else "")
        attrs = dict(attributes or {})
        if fifo:
            attrs["FifoTopic"] = "true"
        args: dict[str, Any] = {"Name": name}
        if attrs:
            args["Attributes"] = attrs
        arn = sns.create_topic(**args)["TopicArn"]
        cleanup.add(sns.delete_topic, TopicArn=arn)
        return arn

    return make


@pytest.fixture
def subscribe_queue(
    sns: Any, make_queue: Callable[..., str], queue_arn: Callable[[str], str]
) -> Callable[..., tuple[str, str]]:
    """Subscribe a fresh queue to a topic; returns (queue_url, subscription_arn)."""

    def subscribe(topic_arn: str, *, fifo: bool = False, queue_attributes: dict[str, str] | None = None, **attributes: str) -> tuple[str, str]:
        queue = make_queue("conf-sub", fifo=fifo, attributes=queue_attributes)
        sub = sns.subscribe(TopicArn=topic_arn, Protocol="sqs", Endpoint=queue_arn(queue), Attributes=attributes, ReturnSubscriptionArn=True)
        return queue, sub["SubscriptionArn"]

    return subscribe


def drain(sqs: Any, url: str) -> list[dict[str, Any]]:
    """Receive and delete everything currently in the queue."""
    out: list[dict[str, Any]] = []
    while True:
        batch = sqs.receive_message(
            QueueUrl=url, MaxNumberOfMessages=10, MessageAttributeNames=["All"], AttributeNames=["All"]
        ).get("Messages", [])
        if not batch:
            return out
        for m in batch:
            sqs.delete_message(QueueUrl=url, ReceiptHandle=m["ReceiptHandle"])
        out.extend(batch)


def delivered(sqs: Any, admin: Admin, url: str, *, expect: bool = True) -> list[dict[str, Any]]:
    """What reached the queue once the emulator has no work left.

    With expect=True a bounded wait covers asynchronous delivery; with expect=False the queue is
    checked once after settling, so a message that should be filtered out has to be absent.
    """
    admin.settle()
    if expect:
        return eventually(lambda: drain(sqs, url), bool, what=f"a delivery to {url}")
    return drain(sqs, url)


# --- Topics --------------------------------------------------------------------------------------------------


def test_create_topic_is_idempotent_and_returns_an_arn(sns: Any, make_topic: Callable[..., str]) -> None:
    arn = make_topic("conf-t1")
    assert re.fullmatch(r"arn:aws:sns:us-east-1:\d{12}:conf-t1-[0-9a-f]{10}", arn)
    assert sns.create_topic(Name=arn.rsplit(":", 1)[1])["TopicArn"] == arn


def test_create_topic_with_different_attributes_fails(sns: Any, make_topic: Callable[..., str]) -> None:
    arn = make_topic("conf-t2", attributes={"DisplayName": "one"})
    with raises("InvalidParameter") as exc:
        sns.create_topic(Name=arn.rsplit(":", 1)[1], Attributes={"DisplayName": "two"})
    assert exc.value.response["Error"]["Message"] == "Invalid parameter: Attributes Reason: Topic already exists with different attributes"


@pytest.mark.parametrize("name", ["has space", "dot.name", "x" * 257, "bad.fifo"])
def test_topic_name_constraints(sns: Any, name: str) -> None:
    with raises("InvalidParameter") as exc:
        sns.create_topic(Name=name)
    assert status_of(exc.value) == 400
    assert exc.value.response["Error"]["Message"] == "Invalid parameter: Topic Name"


def test_new_topic_attributes(sns: Any, make_topic: Callable[..., str]) -> None:
    arn = make_topic()
    attrs = sns.get_topic_attributes(TopicArn=arn)["Attributes"]
    assert attrs["TopicArn"] == arn
    assert attrs["DisplayName"] == ""
    assert attrs["Owner"] == arn.split(":")[4]
    assert (attrs["SubscriptionsConfirmed"], attrs["SubscriptionsPending"], attrs["SubscriptionsDeleted"]) == ("0", "0", "0")
    assert "FifoTopic" not in attrs
    policy = json.loads(attrs["Policy"])
    assert policy["Statement"][0]["Sid"] == "__default_statement_ID"
    assert policy["Statement"][0]["Resource"] == arn
    effective = json.loads(attrs["EffectiveDeliveryPolicy"])
    assert effective["http"]["defaultHealthyRetryPolicy"]["numRetries"] == 3


def test_set_topic_attributes(sns: Any, make_topic: Callable[..., str]) -> None:
    arn = make_topic()
    sns.set_topic_attributes(TopicArn=arn, AttributeName="DisplayName", AttributeValue="Pipeline")
    assert sns.get_topic_attributes(TopicArn=arn)["Attributes"]["DisplayName"] == "Pipeline"
    with raises("InvalidParameter"):
        sns.set_topic_attributes(TopicArn=arn, AttributeName="FifoTopic", AttributeValue="true")


def test_unknown_topic_is_not_found(sns: Any) -> None:
    arn = f"arn:aws:sns:us-east-1:000000000000:{uniq('conf-missing')}"
    with raises("NotFound") as exc:
        sns.get_topic_attributes(TopicArn=arn)
    assert status_of(exc.value) == 404
    assert exc.value.response["Error"]["Message"] == "Topic does not exist"
    with raises("NotFound"):
        sns.publish(TopicArn=arn, Message="x")


def test_delete_topic_is_idempotent(sns: Any, make_topic: Callable[..., str]) -> None:
    arn = make_topic()
    sns.delete_topic(TopicArn=arn)
    sns.delete_topic(TopicArn=arn)


def test_list_topics_includes_new_topics(sns: Any, make_topic: Callable[..., str]) -> None:
    arns = {make_topic() for _ in range(3)}
    listed = {t["TopicArn"] for page in sns.get_paginator("list_topics").paginate() for t in page["Topics"]}
    assert arns <= listed


def test_topic_tags(sns: Any, make_topic: Callable[..., str]) -> None:
    arn = make_topic()
    sns.tag_resource(ResourceArn=arn, Tags=[{"Key": "env", "Value": "test"}, {"Key": "team", "Value": "a"}])
    sns.tag_resource(ResourceArn=arn, Tags=[{"Key": "env", "Value": "prod"}])
    tags = {t["Key"]: t["Value"] for t in sns.list_tags_for_resource(ResourceArn=arn)["Tags"]}
    assert tags == {"env": "prod", "team": "a"}
    sns.untag_resource(ResourceArn=arn, TagKeys=["team"])
    assert [t["Key"] for t in sns.list_tags_for_resource(ResourceArn=arn)["Tags"]] == ["env"]
    with raises("InvalidParameter") as exc:
        sns.tag_resource(ResourceArn=arn, Tags=[{"Key": "k", "Value": "1"}, {"Key": "k", "Value": "2"}])
    assert exc.value.response["Error"]["Message"] == "Invalid parameter: Duplicated keys are not allowed."


def test_fifo_topic_attributes(sns: Any, make_topic: Callable[..., str]) -> None:
    arn = make_topic(fifo=True)
    assert arn.endswith(".fifo")
    attrs = sns.get_topic_attributes(TopicArn=arn)["Attributes"]
    assert attrs["FifoTopic"] == "true"
    assert attrs["ContentBasedDeduplication"] == "false"


# --- Subscriptions -----------------------------------------------------------------------------------------------


def test_sqs_subscription_is_confirmed_on_return(
    sns: Any, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]], queue_arn: Callable[[str], str]
) -> None:
    topic = make_topic()
    queue, sub_arn = subscribe_queue(topic)
    assert re.fullmatch(re.escape(topic) + r":[0-9a-f-]{36}", sub_arn)

    attrs = sns.get_subscription_attributes(SubscriptionArn=sub_arn)["Attributes"]
    assert attrs["Protocol"] == "sqs"
    assert attrs["Endpoint"] == queue_arn(queue)
    assert attrs["TopicArn"] == topic
    assert attrs["PendingConfirmation"] == "false"
    assert attrs["ConfirmationWasAuthenticated"] == "true"
    assert attrs["RawMessageDelivery"] == "false"
    assert "FilterPolicy" not in attrs and "FilterPolicyScope" not in attrs

    topic_attrs = sns.get_topic_attributes(TopicArn=topic)["Attributes"]
    assert topic_attrs["SubscriptionsConfirmed"] == "1"


def test_subscribe_is_idempotent_per_endpoint(
    sns: Any, make_topic: Callable[..., str], make_queue: Callable[..., str], queue_arn: Callable[[str], str]
) -> None:
    topic = make_topic()
    arn = queue_arn(make_queue())
    first = sns.subscribe(TopicArn=topic, Protocol="sqs", Endpoint=arn)["SubscriptionArn"]
    assert sns.subscribe(TopicArn=topic, Protocol="sqs", Endpoint=arn)["SubscriptionArn"] == first
    assert len(sns.list_subscriptions_by_topic(TopicArn=topic)["Subscriptions"]) == 1


def test_subscribe_validation(sns: Any, make_topic: Callable[..., str], make_queue: Callable[..., str]) -> None:
    topic = make_topic()
    with raises("InvalidParameter") as exc:
        sns.subscribe(TopicArn=topic, Protocol="test-protocol", Endpoint="x")
    assert exc.value.response["Error"]["Message"] == "Invalid parameter: Amazon SNS does not support this protocol string: test-protocol"
    with raises("InvalidParameter") as exc:
        sns.subscribe(TopicArn=topic, Protocol="sqs", Endpoint=make_queue())
    assert exc.value.response["Error"]["Message"] == "Invalid parameter: SQS endpoint ARN"


def test_list_subscriptions_by_topic(
    sns: Any, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]], queue_arn: Callable[[str], str]
) -> None:
    topic = make_topic()
    queues = [subscribe_queue(topic) for _ in range(3)]
    listed = sns.list_subscriptions_by_topic(TopicArn=topic)["Subscriptions"]
    assert sorted(s["SubscriptionArn"] for s in listed) == sorted(sub for _, sub in queues)
    assert {(s["Protocol"], s["TopicArn"]) for s in listed} == {("sqs", topic)}
    assert sorted(s["Endpoint"] for s in listed) == sorted(queue_arn(q) for q, _ in queues)
    everything = [s["SubscriptionArn"] for page in sns.get_paginator("list_subscriptions").paginate() for s in page["Subscriptions"]]
    assert {sub for _, sub in queues} <= set(everything)


def test_unsubscribe_stops_delivery_and_is_idempotent(
    sns: Any, sqs: Any, admin: Admin, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]
) -> None:
    topic = make_topic()
    queue, sub = subscribe_queue(topic)
    sns.unsubscribe(SubscriptionArn=sub)
    sns.unsubscribe(SubscriptionArn=sub)
    with raises("NotFound") as exc:
        sns.get_subscription_attributes(SubscriptionArn=sub)
    assert exc.value.response["Error"]["Message"] == "Subscription does not exist"
    sns.publish(TopicArn=topic, Message="after")
    assert delivered(sqs, admin, queue, expect=False) == []


def test_set_subscription_attributes(sns: Any, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]) -> None:
    topic = make_topic()
    _, sub = subscribe_queue(topic)
    sns.set_subscription_attributes(SubscriptionArn=sub, AttributeName="RawMessageDelivery", AttributeValue="true")
    assert sns.get_subscription_attributes(SubscriptionArn=sub)["Attributes"]["RawMessageDelivery"] == "true"
    with raises("InvalidParameter") as exc:
        sns.set_subscription_attributes(SubscriptionArn=sub, AttributeName="RawMessageDelivery", AttributeValue="wrongvalue")
    assert exc.value.response["Error"]["Message"] == "Invalid parameter: RawMessageDelivery: Invalid value [wrongvalue]. Must be true or false."
    with raises("InvalidParameter") as exc:
        sns.set_subscription_attributes(SubscriptionArn=sub, AttributeName="Endpoint", AttributeValue="x")
    assert exc.value.response["Error"]["Message"] == "Invalid parameter: AttributeName"


# --- Delivery: envelope and raw ----------------------------------------------------------------------------------------


def test_enveloped_delivery_to_sqs(
    sns: Any, sqs: Any, admin: Admin, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]
) -> None:
    topic = make_topic()
    queue, sub = subscribe_queue(topic)
    sent = sns.publish(
        TopicArn=topic,
        Message="hello subscribers",
        Subject="Greeting",
        MessageAttributes={"attr1": number("99.12"), "attr2": string("text")},
    )
    [msg] = delivered(sqs, admin, queue)
    body = json.loads(msg["Body"])
    assert body["Type"] == "Notification"
    assert body["MessageId"] == sent["MessageId"]
    assert body["TopicArn"] == topic
    assert body["Subject"] == "Greeting"
    assert body["Message"] == "hello subscribers"
    assert re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z", body["Timestamp"])
    assert body["SignatureVersion"] == "1"
    assert body["Signature"] and body["SigningCertURL"].endswith(".pem")
    assert body["UnsubscribeURL"].endswith(f"Action=Unsubscribe&SubscriptionArn={sub}")
    assert body["MessageAttributes"] == {"attr1": {"Type": "Number", "Value": "99.12"}, "attr2": {"Type": "String", "Value": "text"}}
    assert "MessageAttributes" not in msg, "attributes live inside the envelope, not on the SQS message"
    # The envelope lists its fields in this order.
    assert list(body)[:5] == ["Type", "MessageId", "TopicArn", "Subject", "Message"]


def test_envelope_omits_subject_and_attributes_when_not_published(
    sns: Any, sqs: Any, admin: Admin, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]
) -> None:
    topic = make_topic()
    queue, _ = subscribe_queue(topic)
    sns.publish(TopicArn=topic, Message="plain")
    [msg] = delivered(sqs, admin, queue)
    body = json.loads(msg["Body"])
    assert "Subject" not in body and "MessageAttributes" not in body


def test_raw_delivery_sends_the_message_and_attributes_unwrapped(
    sns: Any, sqs: Any, admin: Admin, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]
) -> None:
    topic = make_topic()
    queue, _ = subscribe_queue(topic, RawMessageDelivery="true")
    sns.publish(
        TopicArn=topic,
        Message='{"raw": true}',
        Subject="dropped",
        MessageAttributes={
            "n": number("111"),
            "s": string("v"),
            "b": {"DataType": "Binary", "BinaryValue": b"\x02\x03\x04"},
        },
    )
    [msg] = delivered(sqs, admin, queue)
    assert msg["Body"] == '{"raw": true}'
    assert msg["MessageAttributes"]["n"] == {"DataType": "Number", "StringValue": "111"}
    assert msg["MessageAttributes"]["s"] == {"DataType": "String", "StringValue": "v"}
    assert msg["MessageAttributes"]["b"] == {"DataType": "Binary", "BinaryValue": b"\x02\x03\x04"}


def test_every_subscriber_gets_its_own_copy(
    sns: Any, sqs: Any, admin: Admin, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]
) -> None:
    topic = make_topic()
    queues = [subscribe_queue(topic, RawMessageDelivery="true")[0] for _ in range(3)]
    sent = sns.publish(TopicArn=topic, Message="fan out")
    assert sent["MessageId"]
    admin.settle()
    for q in queues:
        assert [m["Body"] for m in delivered(sqs, admin, q)] == ["fan out"]


def test_message_structure_json_selects_the_protocol_message(
    sns: Any, sqs: Any, admin: Admin, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]
) -> None:
    topic = make_topic()
    queue, _ = subscribe_queue(topic, RawMessageDelivery="true")
    sns.publish(TopicArn=topic, MessageStructure="json", Message=json.dumps({"default": "for everyone", "sqs": "for queues"}))
    [msg] = delivered(sqs, admin, queue)
    assert msg["Body"] == "for queues"


def test_message_structure_json_errors(sns: Any, make_topic: Callable[..., str]) -> None:
    topic = make_topic()
    with raises("InvalidParameter") as exc:
        sns.publish(TopicArn=topic, MessageStructure="json", Message="{not json")
    assert exc.value.response["Error"]["Message"] == "Invalid parameter: Message Structure - JSON message body failed to parse"
    with raises("InvalidParameter") as exc:
        sns.publish(TopicArn=topic, MessageStructure="json", Message=json.dumps({"sqs": "no default"}))
    assert exc.value.response["Error"]["Message"] == "Invalid parameter: Message Structure - No default entry in JSON message body"


def test_publish_validation(sns: Any, make_topic: Callable[..., str]) -> None:
    topic = make_topic()
    with raises("InvalidParameter") as exc:
        sns.publish(TopicArn=topic, Message="")
    assert exc.value.response["Error"]["Message"] == "Invalid parameter: Empty message"
    with raises("InvalidParameter") as exc:
        sns.publish(TopicArn=topic, Message="a" * 262145)
    assert exc.value.response["Error"]["Message"] == "Invalid parameter: Message too long"
    with raises("InvalidParameter") as exc:
        sns.publish(TopicArn=topic, Message="x", Subject="")
    assert exc.value.response["Error"]["Message"] == "Invalid parameter: Subject"


def test_message_attribute_validation(sns: Any, make_topic: Callable[..., str]) -> None:
    topic = make_topic()
    with raises("ParameterValueInvalid") as exc:
        sns.publish(TopicArn=topic, Message="x", MessageAttributes={"attr1": {"DataType": "String", "StringValue": ""}})
    assert exc.value.response["Error"]["Message"] == (
        "The message attribute 'attr1' must contain non-empty message attribute value for message attribute type 'String'."
    )
    with raises("ParameterValueInvalid") as exc:
        sns.publish(TopicArn=topic, Message="x", MessageAttributes={"attr1": {"DataType": "Strange", "StringValue": "v"}})
    assert exc.value.response["Error"]["Message"] == (
        "The message attribute 'attr1' has an invalid message attribute type, the set of supported type prefixes is "
        "Binary, Number, and String."
    )
    with raises("ParameterValueInvalid"):
        sns.publish(TopicArn=topic, Message="x", MessageAttributes={".bad": string("v")})


# --- Filter policies, attribute scope ----------------------------------------------------------------------------------------


def delivered_with(
    sns: Any,
    sqs: Any,
    admin: Admin,
    topic: str,
    queue: str,
    attributes: AttributeMap,
) -> bool:
    sns.publish(TopicArn=topic, Message="probe", MessageAttributes=attributes)
    return bool(delivered(sqs, admin, queue, expect=False))


ATTRIBUTE_CASES: list[Any] = [
    pytest.param({"color": ["blue"]}, {"color": string("blue")}, True, id="exact-match"),
    pytest.param({"color": ["blue"]}, {"color": string("red")}, False, id="exact-mismatch"),
    pytest.param({"color": ["blue"]}, {"other": string("blue")}, False, id="missing-key"),
    pytest.param({"color": ["blue"]}, {"color": string("blue"), "extra": string("x")}, True, id="extra-attributes-ignored"),
    pytest.param({"color": ["blue", "red"]}, {"color": string("red")}, True, id="or-within-key"),
    pytest.param({"color": ["blue"]}, {"color": string("Blue")}, False, id="case-sensitive"),
    pytest.param({"a": ["1"], "b": ["2"]}, {"a": string("1"), "b": string("2")}, True, id="and-across-keys"),
    pytest.param({"a": ["1"], "b": ["2"]}, {"a": string("1")}, False, id="and-across-keys-missing"),
    pytest.param({"store": ["value1"]}, {"store": {"DataType": "String.Array", "StringValue": '["value1","value2"]'}}, True, id="string-array-hit"),
    pytest.param({"store": ["value1"]}, {"store": {"DataType": "String.Array", "StringValue": '["value2","value3"]'}}, False, id="string-array-miss"),
    pytest.param({"bin": ["x"]}, {"bin": {"DataType": "Binary", "BinaryValue": b"x"}}, False, id="binary-ignored"),
    pytest.param({"price": [{"numeric": [">", 10, "<=", 20]}]}, {"price": number("15")}, True, id="numeric-range-inside"),
    pytest.param({"price": [{"numeric": [">", 10, "<=", 20]}]}, {"price": number("10")}, False, id="numeric-range-lower-exclusive"),
    pytest.param({"price": [{"numeric": [">", 10, "<=", 20]}]}, {"price": number("20")}, True, id="numeric-range-upper-inclusive"),
    pytest.param({"price": [{"numeric": [">", 10, "<=", 20]}]}, {"price": number("25")}, False, id="numeric-range-outside"),
    pytest.param({"price": [{"numeric": ["=", 301.5]}]}, {"price": number("301.5")}, True, id="numeric-equals"),
    pytest.param({"price": [{"numeric": ["=", 301.5]}]}, {"price": number("3.015e2")}, True, id="numeric-equals-exponent"),
    pytest.param({"name": [{"prefix": "auto-"}]}, {"name": string("auto-123")}, True, id="prefix-hit"),
    pytest.param({"name": [{"prefix": "auto-"}]}, {"name": string("manual")}, False, id="prefix-miss"),
    pytest.param({"name": [{"suffix": ".png"}]}, {"name": string("pic.png")}, True, id="suffix-hit"),
    pytest.param({"name": [{"suffix": ".png"}]}, {"name": string("pic.jpg")}, False, id="suffix-miss"),
    pytest.param({"name": [{"equals-ignore-case": "ABC"}]}, {"name": string("abc")}, True, id="equals-ignore-case"),
    pytest.param({"name": [{"anything-but": "x"}]}, {"name": string("y")}, True, id="anything-but-hit"),
    pytest.param({"name": [{"anything-but": "x"}]}, {"name": string("x")}, False, id="anything-but-miss"),
    pytest.param({"name": [{"anything-but": "x"}]}, {"other": string("y")}, False, id="anything-but-needs-the-key"),
    pytest.param({"name": [{"anything-but": ["x", "y"]}]}, {"name": string("z")}, True, id="anything-but-list-hit"),
    pytest.param({"name": [{"anything-but": ["x", "y"]}]}, {"name": string("y")}, False, id="anything-but-list-miss"),
    pytest.param({"name": [{"anything-but": {"prefix": "bad"}}]}, {"name": string("good")}, True, id="anything-but-prefix-hit"),
    pytest.param({"name": [{"anything-but": {"prefix": "bad"}}]}, {"name": string("badass")}, False, id="anything-but-prefix-miss"),
    pytest.param({"flag": [{"exists": True}]}, {"flag": string("v")}, True, id="exists-true-hit"),
    pytest.param({"flag": [{"exists": True}]}, {"flag": number("5")}, True, id="exists-true-number"),
    pytest.param({"flag": [{"exists": True}]}, {"other": string("v")}, False, id="exists-true-miss"),
    pytest.param({"flag": [{"exists": False}]}, {"other": string("v")}, True, id="exists-false-hit"),
    pytest.param({"flag": [{"exists": False}]}, {"flag": string("v")}, False, id="exists-false-miss"),
    pytest.param({"ip": [{"cidr": "10.0.0.0/24"}]}, {"ip": string("10.0.0.5")}, True, id="cidr-hit"),
    pytest.param({"ip": [{"cidr": "10.0.0.0/24"}]}, {"ip": string("10.0.1.5")}, False, id="cidr-miss"),
    pytest.param({"name": [{"wildcard": "*ball"}]}, {"name": string("baseball")}, True, id="wildcard-hit"),
    pytest.param({"name": [{"wildcard": "*ball"}]}, {"name": string("ballpark")}, False, id="wildcard-miss"),
    pytest.param({"$or": [{"a": ["1"]}, {"b": ["2"]}]}, {"a": string("1")}, True, id="or-first-branch"),
    pytest.param({"$or": [{"a": ["1"]}, {"b": ["2"]}]}, {"b": string("2")}, True, id="or-second-branch"),
    pytest.param({"$or": [{"a": ["1"]}, {"b": ["2"]}]}, {"c": string("3")}, False, id="or-no-branch"),
]


@pytest.mark.parametrize(("policy", "attributes", "expected"), ATTRIBUTE_CASES)
def test_attribute_filter_policy(
    sns: Any,
    sqs: Any,
    admin: Admin,
    make_topic: Callable[..., str],
    subscribe_queue: Callable[..., tuple[str, str]],
    policy: dict[str, Any],
    attributes: AttributeMap,
    expected: bool,
) -> None:
    topic = make_topic()
    queue, _ = subscribe_queue(topic, RawMessageDelivery="true", FilterPolicy=json.dumps(policy))
    assert delivered_with(sns, sqs, admin, topic, queue, attributes) is expected


def test_a_subscription_without_a_policy_receives_everything(
    sns: Any, sqs: Any, admin: Admin, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]
) -> None:
    topic = make_topic()
    queue, _ = subscribe_queue(topic, RawMessageDelivery="true")
    assert delivered_with(sns, sqs, admin, topic, queue, {}) is True


def test_filter_policy_changes_apply_immediately_and_can_be_cleared(
    sns: Any, sqs: Any, admin: Admin, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]
) -> None:
    topic = make_topic()
    queue, sub = subscribe_queue(topic, RawMessageDelivery="true")
    sns.set_subscription_attributes(SubscriptionArn=sub, AttributeName="FilterPolicy", AttributeValue='{"k":["v"]}')
    attrs = sns.get_subscription_attributes(SubscriptionArn=sub)["Attributes"]
    assert json.loads(attrs["FilterPolicy"]) == {"k": ["v"]} and attrs["FilterPolicyScope"] == "MessageAttributes"
    assert delivered_with(sns, sqs, admin, topic, queue, {"k": string("other")}) is False

    sns.set_subscription_attributes(SubscriptionArn=sub, AttributeName="FilterPolicy", AttributeValue="")
    assert "FilterPolicy" not in sns.get_subscription_attributes(SubscriptionArn=sub)["Attributes"]
    assert delivered_with(sns, sqs, admin, topic, queue, {"k": string("other")}) is True


# --- Filter policies, body scope -------------------------------------------------------------------------------------------------


BODY_CASES: list[Any] = [
    pytest.param(
        {"order": {"status": ["paid"], "total": [{"numeric": [">", 100]}]}},
        {"order": {"status": "paid", "total": 150}},
        True,
        id="nested-keys-and-numeric",
    ),
    pytest.param(
        {"order": {"status": ["paid"], "total": [{"numeric": [">", 100]}]}},
        {"order": {"status": "paid", "total": 50}},
        False,
        id="nested-numeric-miss",
    ),
    pytest.param({"order": {"status": ["paid"]}}, {"order": {"status": "new"}}, False, id="nested-string-miss"),
    pytest.param({"headers": {"route-to": ["queue1"]}}, {"headers": {"route-to": ["queue1", "queue2"]}}, True, id="array-values-are-transparent"),
    pytest.param({"headers": {"route-to": ["queue3"]}}, {"headers": {"route-to": ["queue1", "queue2"]}}, False, id="array-values-miss"),
    pytest.param({"Records": {"s3": {"object": {"key": [{"prefix": "in/"}]}}}}, {"Records": [{"s3": {"object": {"key": "in/a.txt"}}}]}, True, id="path-through-arrays-of-objects"),
    pytest.param({"object.nested": ["string.value"]}, {"object": {"nested": "string.value"}}, True, id="dotted-key-matches-nested-path"),
    pytest.param({"flag": [True]}, {"flag": True}, True, id="boolean-literal"),
    pytest.param({"flag": [True]}, {"flag": "true"}, False, id="boolean-is-not-the-string"),
    pytest.param({"gone": [{"exists": False}]}, {"present": 1}, True, id="exists-false-on-absent-leaf"),
    pytest.param({"$or": [{"a": ["1"]}, {"b": {"c": ["2"]}}]}, {"b": {"c": "2"}}, True, id="or-with-nested-branch"),
    pytest.param({"detail": {"eventVersion": [""]}}, {"detail": {"eventVersion": "", "resources": []}}, True, id="empty-arrays-do-not-break-matching"),
]


@pytest.mark.parametrize(("policy", "body", "expected"), BODY_CASES)
def test_body_filter_policy(
    sns: Any,
    sqs: Any,
    admin: Admin,
    make_topic: Callable[..., str],
    subscribe_queue: Callable[..., tuple[str, str]],
    policy: dict[str, Any],
    body: dict[str, Any],
    expected: bool,
) -> None:
    topic = make_topic()
    queue, _ = subscribe_queue(topic, RawMessageDelivery="true", FilterPolicy=json.dumps(policy), FilterPolicyScope="MessageBody")
    sns.publish(TopicArn=topic, Message=json.dumps(body))
    assert bool(delivered(sqs, admin, queue, expect=False)) is expected


@pytest.mark.parametrize("message", ["plain text", '"json string"', "[1, 2]"])
def test_body_scope_filters_out_messages_that_are_not_json_objects(
    sns: Any, sqs: Any, admin: Admin, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]], message: str
) -> None:
    topic = make_topic()
    queue, _ = subscribe_queue(topic, RawMessageDelivery="true", FilterPolicy='{"a":["b"]}', FilterPolicyScope="MessageBody")
    sns.publish(TopicArn=topic, Message=message)
    assert delivered(sqs, admin, queue, expect=False) == []


def test_body_scope_ignores_message_attributes(
    sns: Any, sqs: Any, admin: Admin, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]
) -> None:
    topic = make_topic()
    queue, _ = subscribe_queue(topic, RawMessageDelivery="true", FilterPolicy='{"a":["b"]}', FilterPolicyScope="MessageBody")
    sns.publish(TopicArn=topic, Message='{"a":"c"}', MessageAttributes={"a": string("b")})
    assert delivered(sqs, admin, queue, expect=False) == []


# --- Filter policy validation ------------------------------------------------------------------------------------------------------


INVALID_POLICIES: list[Any] = [
    pytest.param('{"key":[]}', "Empty arrays are not allowed", id="empty-array"),
    pytest.param('{"key":[{"wrong-operator":true}]}', "Unrecognized match type wrong-operator", id="unknown-operator"),
    pytest.param('{"key":[{"suffix":"v","prefix":"w"}]}', "Only one key allowed in match expression", id="two-operators"),
    pytest.param('{"key":[{"suffix":100}]}', "suffix match pattern must be a string", id="suffix-not-string"),
    pytest.param('{"key":[{"exists":"no"}]}', "exists match pattern must be either true or false.", id="exists-not-boolean"),
    pytest.param('{"key":[["value"]]}', "Match value must be String, number, true, false, or null", id="nested-array"),
    pytest.param('{"key":[{"anything-but":{"wrong-operator":null}}]}', "Unsupported anything-but pattern: wrong-operator", id="anything-but-unknown"),
    pytest.param('{"key":[{"numeric":[">",3,"<",1]}]}', "Bottom must be less than top", id="numeric-bottom-above-top"),
    pytest.param('{"key":[{"numeric":["=","000"]}]}', "Value of equals must be numeric", id="numeric-equals-string"),
    pytest.param('{"key":[{"cidr":"bad-filter"}]}', "Malformed CIDR, one '/' required", id="cidr-without-slash"),
    pytest.param(json.dumps({f"k{i}": ["v"] for i in range(6)}), "Filter policy can not have more than 5 keys", id="too-many-keys"),
    pytest.param(json.dumps({"key": [f"v{i}" for i in range(151)]}), "Filter policy is too complex", id="too-complex"),
]


@pytest.mark.parametrize(("policy", "detail"), INVALID_POLICIES)
def test_invalid_filter_policy_on_set_subscription_attributes(
    sns: Any, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]], policy: str, detail: str
) -> None:
    topic = make_topic()
    _, sub = subscribe_queue(topic)
    with raises("InvalidParameter") as exc:
        sns.set_subscription_attributes(SubscriptionArn=sub, AttributeName="FilterPolicy", AttributeValue=policy)
    assert status_of(exc.value) == 400
    # Structural errors may append a parser position line; the first line is the contract.
    assert exc.value.response["Error"]["Message"].split("\n")[0] == f"Invalid parameter: FilterPolicy: {detail}"


def test_invalid_filter_policy_on_subscribe_names_the_attributes_parameter(
    sns: Any, make_topic: Callable[..., str], make_queue: Callable[..., str], queue_arn: Callable[[str], str]
) -> None:
    topic = make_topic()
    with raises("InvalidParameter") as exc:
        sns.subscribe(TopicArn=topic, Protocol="sqs", Endpoint=queue_arn(make_queue()), Attributes={"FilterPolicy": '{"key":[]}'})
    assert exc.value.response["Error"]["Message"] == "Invalid parameter: Attributes Reason: FilterPolicy: Empty arrays are not allowed"


def test_invalid_filter_policy_scope(sns: Any, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]) -> None:
    topic = make_topic()
    _, sub = subscribe_queue(topic)
    with raises("InvalidParameter") as exc:
        sns.set_subscription_attributes(SubscriptionArn=sub, AttributeName="FilterPolicyScope", AttributeValue="RandomValue")
    assert exc.value.response["Error"]["Message"] == (
        "Invalid parameter: FilterPolicyScope: Invalid value [RandomValue]. Please use either MessageBody or MessageAttributes"
    )


def test_nested_policy_is_invalid_under_attribute_scope(
    sns: Any, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]
) -> None:
    topic = make_topic()
    _, sub = subscribe_queue(topic)
    with raises("InvalidParameter") as exc:
        sns.set_subscription_attributes(SubscriptionArn=sub, AttributeName="FilterPolicy", AttributeValue='{"object":{"key":[{"prefix":"auto-"}]}}')
    assert exc.value.response["Error"]["Message"] == "Invalid parameter: Filter policy scope MessageAttributes does not support nested filter policy"


# --- PublishBatch -----------------------------------------------------------------------------------------------------------------------


def test_publish_batch_delivers_each_entry(
    sns: Any, sqs: Any, admin: Admin, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]
) -> None:
    topic = make_topic()
    queue, _ = subscribe_queue(topic, RawMessageDelivery="true")
    out = sns.publish_batch(
        TopicArn=topic,
        PublishBatchRequestEntries=[{"Id": f"e{i}", "Message": f"batch-{i}"} for i in range(10)],
    )
    assert out["Failed"] == []
    assert sorted(s["Id"] for s in out["Successful"]) == sorted(f"e{i}" for i in range(10))
    assert all(s["MessageId"] for s in out["Successful"])
    admin.settle()
    got: list[dict[str, Any]] = []
    eventually(lambda: got.extend(drain(sqs, queue)) or got, lambda g: len(g) >= 10, what="10 batch deliveries")
    assert sorted(m["Body"] for m in got) == sorted(f"batch-{i}" for i in range(10))


def test_publish_batch_applies_filter_policies_per_entry(
    sns: Any, sqs: Any, admin: Admin, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]
) -> None:
    topic = make_topic()
    filtered_queue, _ = subscribe_queue(topic, RawMessageDelivery="true", FilterPolicy=json.dumps({"price": [{"numeric": ["<=", 100]}]}))
    open_queue, _ = subscribe_queue(topic, RawMessageDelivery="true")
    sns.publish_batch(
        TopicArn=topic,
        PublishBatchRequestEntries=[
            {"Id": "cheap", "Message": "cheap", "MessageAttributes": {"price": number("99")}},
            {"Id": "dear", "Message": "dear", "MessageAttributes": {"price": number("111")}},
        ],
    )
    assert [m["Body"] for m in delivered(sqs, admin, filtered_queue)] == ["cheap"]
    assert sorted(m["Body"] for m in delivered(sqs, admin, open_queue)) == ["cheap", "dear"]


def test_publish_batch_request_errors(sns: Any, make_topic: Callable[..., str]) -> None:
    topic = make_topic()
    with raises("TooManyEntriesInBatchRequest") as exc:
        sns.publish_batch(TopicArn=topic, PublishBatchRequestEntries=[{"Id": f"e{i}", "Message": "m"} for i in range(11)])
    assert exc.value.response["Error"]["Message"] == "The batch request contains more entries than permissible."
    with raises("BatchEntryIdsNotDistinct") as exc:
        sns.publish_batch(TopicArn=topic, PublishBatchRequestEntries=[{"Id": "same", "Message": "a"}, {"Id": "same", "Message": "b"}])
    assert exc.value.response["Error"]["Message"] == "Two or more batch entries in the request have the same Id."
    with raises("InvalidBatchEntryId"):
        sns.publish_batch(TopicArn=topic, PublishBatchRequestEntries=[{"Id": "message.id", "Message": "a"}])
    with raises("EmptyBatchRequest"):
        sns.publish_batch(TopicArn=topic, PublishBatchRequestEntries=[])


# --- FIFO topics ----------------------------------------------------------------------------------------------------------------------------


def test_fifo_topic_to_fifo_queue_keeps_order_group_and_dedup_ids(
    sns: Any, sqs: Any, admin: Admin, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]
) -> None:
    topic = make_topic(fifo=True)
    queue, _ = subscribe_queue(topic, fifo=True, RawMessageDelivery="true")
    for i in range(3):
        out = sns.publish(TopicArn=topic, Message=f"m{i}", MessageGroupId="g", MessageDeduplicationId=f"d{i}")
        assert out["SequenceNumber"]
    admin.settle()
    got = eventually(lambda: sqs.receive_message(QueueUrl=queue, MaxNumberOfMessages=10, AttributeNames=["All"]).get("Messages", []), bool, what="FIFO deliveries")
    assert [m["Body"] for m in got] == ["m0", "m1", "m2"]
    assert [m["Attributes"]["MessageGroupId"] for m in got] == ["g"] * 3
    assert [m["Attributes"]["MessageDeduplicationId"] for m in got] == ["d0", "d1", "d2"]


def test_fifo_envelope_has_a_sequence_number_and_no_signature(
    sns: Any, sqs: Any, admin: Admin, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]
) -> None:
    topic = make_topic(fifo=True)
    queue, _ = subscribe_queue(topic, fifo=True)
    out = sns.publish(TopicArn=topic, Message="enveloped", MessageGroupId="g", MessageDeduplicationId="d")
    [msg] = delivered(sqs, admin, queue)
    body = json.loads(msg["Body"])
    assert body["Type"] == "Notification" and body["Message"] == "enveloped"
    assert body["SequenceNumber"] == out["SequenceNumber"]
    assert not {"Signature", "SignatureVersion", "SigningCertURL"} & set(body)


def test_fifo_topic_content_based_deduplication(
    sns: Any, sqs: Any, admin: Admin, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]
) -> None:
    topic = make_topic(fifo=True, attributes={"ContentBasedDeduplication": "true"})
    queue, _ = subscribe_queue(topic, fifo=True, RawMessageDelivery="true")
    sns.publish(TopicArn=topic, Message="Test", MessageGroupId="g")
    sns.publish(TopicArn=topic, Message="Test", MessageGroupId="g")
    assert len(delivered(sqs, admin, queue)) == 1
    admin.settle()
    assert drain(sqs, queue) == []


def test_fifo_topic_deduplicates_equal_dedup_ids(
    sns: Any, sqs: Any, admin: Admin, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]
) -> None:
    topic = make_topic(fifo=True)
    queue, _ = subscribe_queue(topic, fifo=True, RawMessageDelivery="true")
    first = sns.publish(TopicArn=topic, Message="one", MessageGroupId="g", MessageDeduplicationId="same")
    second = sns.publish(TopicArn=topic, Message="two", MessageGroupId="g", MessageDeduplicationId="same")
    assert first["MessageId"] and second["MessageId"], "a duplicate is accepted with a MessageId"
    assert [m["Body"] for m in delivered(sqs, admin, queue)] == ["one"]


def test_fifo_topic_content_based_dedup_id_reaches_the_queue(
    sns: Any, sqs: Any, admin: Admin, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]
) -> None:
    topic = make_topic(fifo=True, attributes={"ContentBasedDeduplication": "true"})
    queue, _ = subscribe_queue(topic, fifo=True, RawMessageDelivery="true")
    sns.publish(TopicArn=topic, Message="Test", MessageGroupId="g")
    admin.settle()
    [msg] = eventually(lambda: sqs.receive_message(QueueUrl=queue, AttributeNames=["All"]).get("Messages", []), bool, what="delivery")
    assert msg["Attributes"]["MessageDeduplicationId"] == hashlib.sha256(b"Test").hexdigest()


def test_fifo_topic_to_standard_queue_forwards_no_group_id(
    sns: Any, sqs: Any, admin: Admin, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]
) -> None:
    topic = make_topic(fifo=True)
    queue, _ = subscribe_queue(topic, RawMessageDelivery="true")
    sns.publish(TopicArn=topic, Message="to standard", MessageGroupId="g", MessageDeduplicationId="d")
    [msg] = delivered(sqs, admin, queue)
    assert msg["Body"] == "to standard"
    assert "MessageGroupId" not in msg.get("Attributes", {})


def test_fifo_publish_validation(sns: Any, make_topic: Callable[..., str]) -> None:
    fifo = make_topic(fifo=True)
    with raises("InvalidParameter") as exc:
        sns.publish(TopicArn=fifo, Message="x")
    assert exc.value.response["Error"]["Message"] == "Invalid parameter: The MessageGroupId parameter is required for FIFO topics"
    with raises("InvalidParameter") as exc:
        sns.publish(TopicArn=fifo, Message="x", MessageGroupId="g")
    assert exc.value.response["Error"]["Message"] == (
        "Invalid parameter: The topic should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly"
    )
    standard = make_topic()
    with raises("InvalidParameter") as exc:
        sns.publish(TopicArn=standard, Message="x", MessageDeduplicationId="d")
    assert exc.value.response["Error"]["Message"] == (
        "Invalid parameter: MessageDeduplicationId Reason: The request includes MessageDeduplicationId parameter that is not valid for this topic type"
    )


def test_fifo_queue_cannot_subscribe_to_a_standard_topic(
    sns: Any, make_topic: Callable[..., str], make_queue: Callable[..., str], queue_arn: Callable[[str], str]
) -> None:
    topic = make_topic()
    fifo_queue = make_queue(fifo=True)
    with raises("InvalidParameter") as exc:
        sns.subscribe(TopicArn=topic, Protocol="sqs", Endpoint=queue_arn(fifo_queue))
    assert exc.value.response["Error"]["Message"] == (
        "Invalid parameter: Invalid parameter: Endpoint Reason: FIFO SQS Queues can not be subscribed to standard SNS topics"
    )


def test_publish_batch_to_a_fifo_topic(
    sns: Any, sqs: Any, admin: Admin, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]
) -> None:
    topic = make_topic(fifo=True)
    queue, _ = subscribe_queue(topic, fifo=True, RawMessageDelivery="true")
    out = sns.publish_batch(
        TopicArn=topic,
        PublishBatchRequestEntries=[
            {"Id": f"e{i}", "Message": f"m{i}", "MessageGroupId": "g", "MessageDeduplicationId": f"d{i}"} for i in range(3)
        ],
    )
    assert out["Failed"] == []
    assert all(s["SequenceNumber"] for s in out["Successful"])
    admin.settle()
    got = eventually(lambda: sqs.receive_message(QueueUrl=queue, MaxNumberOfMessages=10).get("Messages", []), bool, what="batch")
    assert [m["Body"] for m in got] == ["m0", "m1", "m2"]


# --- Subscription redrive ---------------------------------------------------------------------------------------------------------------------------


def test_undeliverable_messages_go_to_the_subscription_dlq(
    sns: Any,
    sqs: Any,
    admin: Admin,
    make_topic: Callable[..., str],
    make_queue: Callable[..., str],
    queue_arn: Callable[[str], str],
) -> None:
    topic = make_topic()
    dlq = make_queue("conf-snsdlq")
    endpoint = make_queue("conf-gone")
    sub = sns.subscribe(
        TopicArn=topic,
        Protocol="sqs",
        Endpoint=queue_arn(endpoint),
        Attributes={"RedrivePolicy": json.dumps({"deadLetterTargetArn": queue_arn(dlq)})},
        ReturnSubscriptionArn=True,
    )["SubscriptionArn"]
    assert json.loads(sns.get_subscription_attributes(SubscriptionArn=sub)["Attributes"]["RedrivePolicy"]) == {
        "deadLetterTargetArn": queue_arn(dlq)
    }
    sqs.delete_queue(QueueUrl=endpoint)

    sns.publish(TopicArn=topic, Message="lost", Subject="s")
    [msg] = delivered(sqs, admin, dlq)
    body = json.loads(msg["Body"])
    assert body["Type"] == "Notification" and body["Message"] == "lost" and body["TopicArn"] == topic


def test_subscription_redrive_policy_validation(
    sns: Any, make_topic: Callable[..., str], subscribe_queue: Callable[..., tuple[str, str]]
) -> None:
    topic = make_topic()
    _, sub = subscribe_queue(topic)
    with raises("InvalidParameter") as exc:
        sns.set_subscription_attributes(
            SubscriptionArn=sub, AttributeName="RedrivePolicy", AttributeValue=json.dumps({"deadLetterTargetArn": "not-an-arn"})
        )
    assert exc.value.response["Error"]["Message"] == "Invalid parameter: RedrivePolicy: deadLetterTargetArn is an invalid arn"
