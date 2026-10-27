"""Step Functions, Standard workflows (docs/research/stepfunctions-api.md,
docs/research/stepfunctions-asl.md): create, validate, a Choice/Wait/Pass
machine finished with an admin clock advance, describe, history, list, stop,
update, delete, and the refusals."""

from __future__ import annotations

import json
from collections.abc import Callable
from typing import Any

import pytest
from botocore.config import Config

from support import Admin, raises, status_of, uniq

STATES_TRUST = json.dumps(
    {
        "Version": "2012-10-17",
        "Statement": [{"Effect": "Allow", "Principal": {"Service": "states.amazonaws.com"}, "Action": "sts:AssumeRole"}],
    }
)

# A Choice on $.wait between a one-hour Wait and the end.
ROUTE_MACHINE = json.dumps(
    {
        "StartAt": "Route",
        "States": {
            "Route": {"Type": "Choice", "Choices": [{"Variable": "$.wait", "BooleanEquals": True, "Next": "Hold"}], "Default": "Done"},
            "Hold": {"Type": "Wait", "Seconds": 3600, "Next": "Done"},
            "Done": {"Type": "Pass", "Result": {"finished": True}, "ResultPath": "$.result", "End": True},
        },
    },
    separators=(",", ":"),
)


@pytest.fixture(scope="module")
def sfn(make_client: Callable[..., Any]) -> Any:
    return make_client("stepfunctions")


@pytest.fixture
def role(iam: Any, cleanup: Any) -> str:
    name = uniq("sfn-role")
    arn = iam.create_role(RoleName=name, AssumeRolePolicyDocument=STATES_TRUST)["Role"]["Arn"]
    cleanup.add(iam.delete_role, RoleName=name)
    return arn


@pytest.fixture
def make_machine(sfn: Any, role: str, cleanup: Any) -> Callable[[str], str]:
    def make(definition: str) -> str:
        arn = sfn.create_state_machine(name=uniq("sfn"), definition=definition, roleArn=role)["stateMachineArn"]
        cleanup.add(sfn.delete_state_machine, stateMachineArn=arn)
        return arn

    return make


def test_validate_state_machine_definition(sfn: Any) -> None:
    """The recorded answers (stepfunctions-api.md 5.6)."""
    ok = sfn.validate_state_machine_definition(definition=ROUTE_MACHINE)
    assert ok["result"] == "OK"
    assert ok["diagnostics"] == []
    bad = sfn.validate_state_machine_definition(definition="{}")
    assert bad["result"] == "FAIL"
    first = bad["diagnostics"][0]
    assert (first["severity"], first["code"], first["message"], first.get("location")) == (
        "ERROR",
        "SCHEMA_VALIDATION_FAILED",
        "These fields are required: [States, StartAt]",
        "/",
    )


def test_state_machine_lifecycle(sfn: Any, make_machine: Callable[[str], str], clock: Admin) -> None:
    """Describe defaults (stepfunctions-api.md 5.2), a run finished by one clock advance
    (stepfunctions-asl.md 7.1), its history (11.2 rules 1 and 2), the listings, a stop (6.4),
    an update with a revisionId (5.3) and a delete (5.4)."""
    arn = make_machine(ROUTE_MACHINE)
    d = sfn.describe_state_machine(stateMachineArn=arn)
    assert d["definition"] == ROUTE_MACHINE
    assert d["status"] == "ACTIVE"
    assert d["type"] == "STANDARD"
    assert d["loggingConfiguration"] == {"level": "OFF", "includeExecutionData": False}
    assert d["tracingConfiguration"] == {"enabled": False}
    assert d["encryptionConfiguration"] == {"type": "AWS_OWNED_KEY"}
    assert "revisionId" not in d

    run = sfn.start_execution(stateMachineArn=arn, name="waits", input='{"wait":true}')["executionArn"]
    clock.settle()
    assert sfn.describe_execution(executionArn=run)["status"] == "RUNNING"
    clock.advance(3600)
    done = sfn.describe_execution(executionArn=run)
    assert done["status"] == "SUCCEEDED"
    assert json.loads(done["output"]) == {"wait": True, "result": {"finished": True}}
    events = sfn.get_execution_history(executionArn=run)["events"]
    assert (events[0]["type"], events[0]["id"], events[0]["previousEventId"]) == ("ExecutionStarted", 1, 0)
    assert (events[1]["type"], events[1]["previousEventId"]) == ("ChoiceStateEntered", 0)
    assert events[-1]["type"] == "ExecutionSucceeded"

    held = sfn.start_execution(stateMachineArn=arn, name="stopped", input='{"wait":true}')["executionArn"]
    clock.settle()
    sfn.stop_execution(executionArn=held)
    clock.settle()
    assert sfn.describe_execution(executionArn=held)["status"] == "ABORTED"

    assert len(sfn.list_executions(stateMachineArn=arn)["executions"]) == 2
    assert arn in [m["stateMachineArn"] for m in sfn.list_state_machines()["stateMachines"]]

    updated = sfn.update_state_machine(stateMachineArn=arn, definition=ROUTE_MACHINE.replace('"Seconds":3600', '"Seconds":60'))
    assert updated["revisionId"]

    sfn.delete_state_machine(stateMachineArn=arn)
    clock.settle()
    with raises("StateMachineDoesNotExist") as info:
        sfn.describe_state_machine(stateMachineArn=arn)
    assert info.value.response["Error"]["Message"] == f"State Machine Does Not Exist: '{arn}'"


def test_refusals(sfn: Any, make_client: Callable[..., Any], make_machine: Callable[[str], str], role: str) -> None:
    """JSONata is InvalidDefinition with UNSUPPORTED_BY_UNDERSTUDY (stepfunctions-asl.md 10.4);
    EXPRESS and StartSyncExecution answer 501 (stepfunctions-api.md 13.1). StartSyncExecution
    goes out with host-prefix injection off, or boto3 sends it to sync-127.0.0.1 (3.3)."""
    jsonata = json.dumps({"QueryLanguage": "JSONata", "StartAt": "P", "States": {"P": {"Type": "Pass", "End": True}}})
    with raises("InvalidDefinition") as info:
        sfn.create_state_machine(name=uniq("sfn"), definition=jsonata, roleArn=role)
    assert status_of(info.value) == 400
    assert info.value.response["Error"]["Message"].startswith("Invalid State Machine Definition: 'UNSUPPORTED_BY_UNDERSTUDY: ")

    with raises("NotImplemented") as info:
        sfn.create_state_machine(name=uniq("sfn"), definition=ROUTE_MACHINE, roleArn=role, type="EXPRESS")
    assert status_of(info.value) == 501

    arn = make_machine(ROUTE_MACHINE)
    unprefixed = make_client("stepfunctions", config=Config(inject_host_prefix=False))
    with raises("NotImplemented") as info:
        unprefixed.start_sync_execution(stateMachineArn=arn)
    assert status_of(info.value) == 501
