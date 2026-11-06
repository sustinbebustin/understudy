"""EventBridge (docs/research/secrets-logs-events-alarms.md section 4 for the default bus,
docs/research/eventbridge-buses.md for custom buses): rules and PutEvents on the default
bus, the custom bus lifecycle, rules and PutEvents per bus, bus targets and bus policies."""

from __future__ import annotations

import json
from collections.abc import Callable
from typing import Any

import pytest

from support import REGION, Admin, eventually, raises, status_of, uniq

# EVENTS_TRUST lets EventBridge assume a target's role (iam-enforcement-surfaces.md 8.4).
EVENTS_TRUST = json.dumps(
    {
        "Version": "2012-10-17",
        "Statement": [{"Effect": "Allow", "Principal": {"Service": "events.amazonaws.com"}, "Action": "sts:AssumeRole"}],
    }
)


@pytest.fixture(scope="module")
def events(make_client: Callable[..., Any]) -> Any:
    return make_client("events")


@pytest.fixture(scope="module")
def account(sts: Any) -> str:
    return sts.get_caller_identity()["Account"]


def bus_arn(account: str, name: str) -> str:
    return f"arn:aws:events:{REGION}:{account}:event-bus/{name}"


@pytest.fixture
def make_bus(events: Any, cleanup: Any) -> Callable[..., str]:
    """Create a custom bus, deleted after the test; returns its name."""

    def make(prefix: str = "conf-bus", **create_args: Any) -> str:
        name = uniq(prefix)
        events.create_event_bus(Name=name, **create_args)
        cleanup.add(events.delete_event_bus, Name=name)
        return name

    return make


@pytest.fixture
def make_rule(events: Any, cleanup: Any) -> Callable[..., str]:
    """Put a pattern rule with targets on a bus, removed after the test (before its bus);
    returns the rule ARN."""

    def make(bus: str, name: str, pattern: dict[str, Any], targets: list[dict[str, Any]]) -> str:
        arn = events.put_rule(Name=name, EventBusName=bus, EventPattern=json.dumps(pattern))["RuleArn"]
        cleanup.add(events.delete_rule, Name=name, EventBusName=bus)
        if targets:
            out = events.put_targets(Rule=name, EventBusName=bus, Targets=targets)
            assert out["FailedEntryCount"] == 0, out["FailedEntries"]
            cleanup.add(events.remove_targets, Rule=name, EventBusName=bus, Ids=[t["Id"] for t in targets])
        return arn

    return make


def drain(sqs: Any, url: str) -> list[dict[str, Any]]:
    """Receive and delete everything currently in the queue."""
    out: list[dict[str, Any]] = []
    while True:
        batch = sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=10).get("Messages", [])
        if not batch:
            return out
        for m in batch:
            sqs.delete_message(QueueUrl=url, ReceiptHandle=m["ReceiptHandle"])
        out.extend(batch)


def delivered(sqs: Any, admin: Admin, url: str, *, expect: bool = True) -> list[dict[str, Any]]:
    """What reached the queue once the emulator has no work left; with expect=False the queue
    is checked once after settling, so a delivery that must not happen has to be absent."""
    admin.settle()
    if expect:
        return eventually(lambda: drain(sqs, url), bool, what=f"a delivery to {url}")
    return drain(sqs, url)


# The default bus (secrets-logs-events-alarms.md 4.1, 4.7).


def test_put_events_delivers_the_envelope_to_a_matching_rule(
    events: Any, sqs: Any, admin: Admin, account: str, make_queue: Callable[..., str], queue_arn: Callable[[str], str], make_rule: Callable[..., str]
) -> None:
    queue = make_queue("conf-ev")
    source = "conformance." + uniq("app")
    name = uniq("conf-rule")
    arn = make_rule("default", name, {"source": [source]}, [{"Id": "q", "Arn": queue_arn(queue)}])
    assert arn == f"arn:aws:events:{REGION}:{account}:rule/{name}"
    out = events.put_events(Entries=[{"Source": source, "DetailType": "Order Placed", "Detail": json.dumps({"n": 1})}])
    assert out["FailedEntryCount"] == 0
    [message] = delivered(sqs, admin, queue)
    body = json.loads(message["Body"])
    assert body["id"] == out["Entries"][0]["EventId"]
    assert {k: body[k] for k in ("version", "source", "detail-type", "account", "region", "resources", "detail")} == {
        "version": "0", "source": source, "detail-type": "Order Placed", "account": account, "region": REGION, "resources": [], "detail": {"n": 1},
    }


def test_put_events_to_a_missing_bus_answers_an_event_id(events: Any) -> None:
    # testdata/parity/events/R14/03-missing-bus.json (aws-recorded).
    out = events.put_events(Entries=[{"Source": "rec", "DetailType": "x", "Detail": "{}", "EventBusName": uniq("conf-missing")}])
    assert out["FailedEntryCount"] == 0
    assert out["Entries"][0]["EventId"]


# Custom buses (eventbridge-buses.md).


def test_bus_lifecycle(events: Any, account: str, cleanup: Any) -> None:
    # eventbridge-buses.md 3.1-3.5, third-party-recorded; the missing-bus text is
    # testdata/parity/events/R07/04-describe-event-bus-missing.json.
    name = uniq("conf-bus")
    created = events.create_event_bus(Name=name, Description="test bus", Tags=[{"Key": "tag1", "Value": "value1"}])
    cleanup.add(events.delete_event_bus, Name=name)
    assert created["EventBusArn"] == bus_arn(account, name)
    assert created["Description"] == "test bus"
    described = events.describe_event_bus(Name=name)
    assert described["Name"] == name and described["Arn"] == bus_arn(account, name) and described["Description"] == "test bus"
    assert "CreationTime" in described and "LastModifiedTime" in described
    assert not {"Policy", "KmsKeyIdentifier", "DeadLetterConfig", "LogConfig"} & described.keys()
    assert [b["Name"] for b in events.list_event_buses(NamePrefix=name)["EventBuses"]] == [name]
    assert events.list_tags_for_resource(ResourceARN=bus_arn(account, name))["Tags"] == [{"Key": "tag1", "Value": "value1"}]
    with raises("ResourceAlreadyExistsException") as info:
        events.create_event_bus(Name=name)
    assert info.value.response["Error"]["Message"] == f"Event bus {name} already exists."
    assert status_of(info.value) == 400
    events.delete_event_bus(Name=name)
    with raises("ResourceNotFoundException") as info:
        events.describe_event_bus(Name=name)
    assert info.value.response["Error"]["Message"] == f"Event bus {name} does not exist."


def test_delete_event_bus_refuses_default_and_ignores_a_missing_bus(events: Any) -> None:
    # eventbridge-buses.md 3.5, third-party-recorded.
    with raises("ValidationException") as info:
        events.delete_event_bus(Name="default")
    assert info.value.response["Error"]["Message"] == "Cannot delete event bus default."
    events.delete_event_bus(Name=uniq("conf-never"))


def test_update_event_bus_unsets_the_description_and_echoes_log_config(events: Any, make_bus: Callable[..., str]) -> None:
    # Terraform unsets a description with "" and reads NONE/OFF back (eventbridge-buses.md 3.4, 8.1).
    name = make_bus(Description="d1")
    events.update_event_bus(Name=name, Description="", LogConfig={"IncludeDetail": "NONE", "Level": "OFF"})
    described = events.describe_event_bus(Name=name)
    assert described.get("Description", "") == ""
    assert described["LogConfig"] == {"IncludeDetail": "NONE", "Level": "OFF"}


def test_default_bus_takes_tags(events: Any, account: str) -> None:
    # eventbridge-buses.md 5.5, third-party-recorded.
    key = uniq("tag")
    arn = bus_arn(account, "default")
    events.tag_resource(ResourceARN=arn, Tags=[{"Key": key, "Value": "v"}])
    try:
        assert {"Key": key, "Value": "v"} in events.list_tags_for_resource(ResourceARN=arn)["Tags"]
    finally:
        events.untag_resource(ResourceARN=arn, TagKeys=[key])


def test_rules_on_a_bus_match_only_its_events(
    events: Any, sqs: Any, admin: Admin, account: str, make_bus: Callable[..., str], make_queue: Callable[..., str],
    queue_arn: Callable[[str], str], make_rule: Callable[..., str],
) -> None:
    # eventbridge-buses.md 5.2 (rule ARN names the bus) and 6.1 (only the named bus's rules match).
    bus = make_bus()
    on_bus, on_default = make_queue("conf-on-bus"), make_queue("conf-on-default")
    source = "conformance." + bus
    name = uniq("conf-rule")
    arn = make_rule(bus_arn(account, bus), name, {"source": [source]}, [{"Id": "q", "Arn": queue_arn(on_bus)}])
    assert arn == f"arn:aws:events:{REGION}:{account}:rule/{bus}/{name}"
    described = events.describe_rule(Name=name, EventBusName=bus)
    assert described["EventBusName"] == bus and described["CreatedBy"] == account and described["Arn"] == arn
    make_rule("default", name, {"source": [source]}, [{"Id": "q", "Arn": queue_arn(on_default)}])
    out = events.put_events(Entries=[{"Source": source, "DetailType": "Order Placed", "Detail": "{}", "EventBusName": bus}])
    assert out["FailedEntryCount"] == 0
    [message] = delivered(sqs, admin, on_bus)
    assert json.loads(message["Body"])["id"] == out["Entries"][0]["EventId"]
    assert delivered(sqs, admin, on_default, expect=False) == []


def test_schedule_is_refused_on_a_custom_bus(events: Any, make_bus: Callable[..., str]) -> None:
    # testdata/parity/events/R14/04-custom-bus-schedule.json (aws-recorded). The bus exists: AWS
    # looks it up first, so a missing bus is ResourceNotFoundException (events/EBB/03-put-rule-schedule.json).
    bus = make_bus()
    with raises("ValidationException") as info:
        events.put_rule(Name=uniq("conf-busrule"), ScheduleExpression="rate(5 minutes)", EventBusName=bus)
    assert info.value.response["Error"]["Message"] == "ScheduleExpression is supported only on the default event bus."
    assert status_of(info.value) == 400


def test_bus_target_forwards_the_event_one_hop(
    events: Any, sqs: Any, iam: Any, admin: Admin, account: str, make_bus: Callable[..., str],
    make_queue: Callable[..., str], queue_arn: Callable[[str], str], make_rule: Callable[..., str], cleanup: Any,
) -> None:
    # eventbridge-buses.md 6.2 (third-party-recorded: the forwarded event keeps source, detail-type,
    # account, region and detail) and 6.5 (aws-doc eb-cross-account.html: no second hop). The
    # targets' role trusts events.amazonaws.com and its inline policy allows events:PutEvents on
    # the target buses (iam-enforcement-surfaces.md 8.4; aws-doc eb-event-bus-perms.html).
    first, second, third = make_bus(), make_bus(), make_bus()
    reached, beyond = make_queue("conf-reached"), make_queue("conf-beyond")
    source = "conformance." + first
    role_name = uniq("events-role")
    role = iam.create_role(RoleName=role_name, AssumeRolePolicyDocument=EVENTS_TRUST)["Role"]["Arn"]
    cleanup.add(iam.delete_role, RoleName=role_name)
    policy = {
        "Version": "2012-10-17",
        "Statement": [{"Effect": "Allow", "Action": "events:PutEvents", "Resource": [bus_arn(account, second), bus_arn(account, third)]}],
    }
    iam.put_role_policy(RoleName=role_name, PolicyName="put-events", PolicyDocument=json.dumps(policy))
    cleanup.add(iam.delete_role_policy, RoleName=role_name, PolicyName="put-events")
    make_rule(first, "fwd", {"source": [source]}, [{"Id": "bus", "Arn": bus_arn(account, second), "RoleArn": role}])
    make_rule(second, "in", {"source": [source]}, [
        {"Id": "q", "Arn": queue_arn(reached)},
        {"Id": "onward", "Arn": bus_arn(account, third), "RoleArn": role},
    ])
    make_rule(third, "last", {"source": [source]}, [{"Id": "q", "Arn": queue_arn(beyond)}])
    out = events.put_events(Entries=[{"Source": source, "DetailType": "Order Placed", "Detail": json.dumps({"n": 1}), "EventBusName": first}])
    assert out["FailedEntryCount"] == 0
    [message] = delivered(sqs, admin, reached)
    body = json.loads(message["Body"])
    assert {k: body[k] for k in ("source", "detail-type", "account", "region", "detail")} == {
        "source": source, "detail-type": "Order Placed", "account": account, "region": REGION, "detail": {"n": 1},
    }
    assert delivered(sqs, admin, beyond, expect=False) == []


def test_bus_policy_statements(events: Any, account: str, make_bus: Callable[..., str]) -> None:
    # eventbridge-buses.md 4.1-4.3, third-party-recorded.
    bus = make_bus()
    events.put_permission(EventBusName=bus, StatementId="s1", Action="events:PutEvents", Principal="111111111111")
    events.put_permission(EventBusName=bus, StatementId="s2", Action="events:PutEvents", Principal="*")
    policy = json.loads(events.describe_event_bus(Name=bus)["Policy"])
    assert policy["Version"] == "2012-10-17" and "Id" not in policy
    by_sid = {s["Sid"]: s for s in policy["Statement"]}
    assert by_sid == {
        "s1": {"Sid": "s1", "Effect": "Allow", "Principal": {"AWS": "arn:aws:iam::111111111111:root"}, "Action": "events:PutEvents", "Resource": bus_arn(account, bus)},
        "s2": {"Sid": "s2", "Effect": "Allow", "Principal": "*", "Action": "events:PutEvents", "Resource": bus_arn(account, bus)},
    }
    with raises("ResourceNotFoundException") as info:
        events.remove_permission(EventBusName=bus, StatementId="nope")
    assert info.value.response["Error"]["Message"] == "Statement with the provided id does not exist."
    events.remove_permission(EventBusName=bus, StatementId="s1")
    assert [s["Sid"] for s in json.loads(events.describe_event_bus(Name=bus)["Policy"])["Statement"]] == ["s2"]
    events.remove_permission(EventBusName=bus, RemoveAllPermissions=True)
    assert "Policy" not in events.describe_event_bus(Name=bus)


def test_put_permission_with_a_policy_replaces_it(events: Any, make_bus: Callable[..., str]) -> None:
    # eventbridge-buses.md 4.1, third-party-recorded: the document replaces every statement.
    bus = make_bus()
    events.put_permission(EventBusName=bus, StatementId="s1", Action="events:PutEvents", Principal="111111111111")
    document = {"Version": "2012-10-17", "Statement": [{"Sid": "listing", "Effect": "Allow", "Principal": {"Service": "events.amazonaws.com"}, "Action": "events:ListRules", "Resource": "*"}]}
    events.put_permission(EventBusName=bus, Policy=json.dumps(document))
    assert json.loads(events.describe_event_bus(Name=bus)["Policy"])["Statement"] == document["Statement"]
