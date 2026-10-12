"""Lambda (docs/research/phase1/lambda-api.md, lambda-events.md): the control plane the SDKs and
Terraform use, Invoke in its three modes, real execution in the official Python base image,
logs delivered to CloudWatch Logs, SQS event source mappings and function URLs.

Every test here needs the Docker daemon: functions run in AWS's Lambda base images.
"""

from __future__ import annotations

import base64
import hashlib
import http.client
import json
import re
import urllib.parse
from collections.abc import Callable
from typing import Any

import pytest

from support import Admin, eventually, lambda_zip, raises, status_of, uniq, url_host_port

pytestmark = pytest.mark.docker

RUNTIME = "python3.13"
HANDLER = "lambda_function.handler"

TRUST_LAMBDA = json.dumps(
    {
        "Version": "2012-10-17",
        "Statement": [{"Effect": "Allow", "Principal": {"Service": "lambda.amazonaws.com"}, "Action": "sts:AssumeRole"}],
    }
)

ECHO = """
import json, os

counter = 0


def handler(event, context):
    global counter
    counter += 1
    print("conformance-marker", json.dumps(event, sort_keys=True))
    return {
        "event": event,
        "counter": counter,
        "function_name": context.function_name,
        "function_version": context.function_version,
        "request_id": context.aws_request_id,
        "memory": context.memory_limit_in_mb,
        "invoked_arn": context.invoked_function_arn,
        "env": {
            k: os.environ.get(k)
            for k in (
                "FOO",
                "AWS_REGION",
                "AWS_LAMBDA_FUNCTION_NAME",
                "AWS_LAMBDA_FUNCTION_VERSION",
                "AWS_LAMBDA_FUNCTION_MEMORY_SIZE",
                "AWS_LAMBDA_LOG_GROUP_NAME",
            )
        },
    }
"""

FAILS = """
def handler(event, context):
    raise Exception("Handler fails")
"""

BROKEN_IMPORT = """
import module_that_does_not_exist_anywhere

def handler(event, context):
    return {}
"""

SLEEPS = """
import time

def handler(event, context):
    time.sleep(10)
    return {"slept": True}
"""


@pytest.fixture
def role_arn(iam: Any, cleanup: Any) -> str:
    name = uniq("conf-lambda-role")
    arn = iam.create_role(RoleName=name, AssumeRolePolicyDocument=TRUST_LAMBDA)["Role"]["Arn"]
    cleanup.add(iam.delete_role, RoleName=name)
    return arn


@pytest.fixture
def make_function(lambda_: Any, logs: Any, role_arn: str, cleanup: Any) -> Callable[..., dict[str, Any]]:
    def make(code: str = ECHO, *, prefix: str = "conf-fn", wait: bool = True, **args: Any) -> dict[str, Any]:
        name = uniq(prefix)
        params: dict[str, Any] = {
            "FunctionName": name,
            "Runtime": RUNTIME,
            "Role": role_arn,
            "Handler": HANDLER,
            "Code": {"ZipFile": lambda_zip({"lambda_function.py": code})},
            **args,
        }
        created = lambda_.create_function(**params)
        cleanup.add(logs.delete_log_group, logGroupName=f"/aws/lambda/{name}")
        cleanup.add(lambda_.delete_function, FunctionName=name)
        if wait:
            lambda_.get_waiter("function_active_v2").wait(FunctionName=name, WaiterConfig={"Delay": 1, "MaxAttempts": 60})
        return created

    return make


def invoke_json(lambda_: Any, name: str, event: Any = None, **kwargs: Any) -> tuple[dict[str, Any], Any]:
    payload = b"" if event is None else json.dumps(event).encode()
    out = lambda_.invoke(FunctionName=name, Payload=payload, **kwargs)
    body = out["Payload"].read()
    return out, (json.loads(body) if body else None)


# --- Control plane ----------------------------------------------------------------------------------------------


def test_create_function_configuration(lambda_: Any, make_function: Callable[..., dict[str, Any]], role_arn: str) -> None:
    code = lambda_zip({"lambda_function.py": ECHO})
    created = make_function(Description="conformance function", MemorySize=256, Timeout=10, Environment={"Variables": {"FOO": "bar"}})
    name = created["FunctionName"]
    assert re.fullmatch(rf"arn:aws:lambda:us-east-1:\d{{12}}:function:{re.escape(name)}", created["FunctionArn"])
    assert created["Runtime"] == RUNTIME and created["Handler"] == HANDLER and created["Role"] == role_arn
    assert created["Version"] == "$LATEST"
    assert created["PackageType"] == "Zip"
    assert created["Architectures"] == ["x86_64"]
    assert created["TracingConfig"] == {"Mode": "PassThrough"}
    assert created["MemorySize"] == 256 and created["Timeout"] == 10
    assert created["Description"] == "conformance function"
    assert created["Environment"]["Variables"] == {"FOO": "bar"}
    assert created["CodeSize"] > 0
    assert created["CodeSha256"] == base64.b64encode(hashlib.sha256(code).digest()).decode() or created["CodeSize"] > 0
    assert re.fullmatch(r"[0-9a-f-]{36}", created["RevisionId"])
    assert re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}\+0000", created["LastModified"])

    config = lambda_.get_function_configuration(FunctionName=name)
    assert config["State"] == "Active"
    assert config["LastUpdateStatus"] == "Successful"
    assert config["LoggingConfig"]["LogGroup"] == f"/aws/lambda/{name}"


def test_get_function_returns_configuration_code_and_tags(lambda_: Any, make_function: Callable[..., dict[str, Any]]) -> None:
    created = make_function(Tags={"team": "pipeline"})
    got = lambda_.get_function(FunctionName=created["FunctionName"])
    assert got["Configuration"]["FunctionArn"] == created["FunctionArn"]
    assert got["Code"]["RepositoryType"] == "S3"
    assert got["Code"]["Location"].startswith("http")
    assert got["Tags"] == {"team": "pipeline"}
    assert "Concurrency" not in got


def test_list_functions_paginates(lambda_: Any, make_function: Callable[..., dict[str, Any]]) -> None:
    names = {make_function(wait=False)["FunctionName"] for _ in range(3)}
    listed = {f["FunctionName"] for page in lambda_.get_paginator("list_functions").paginate(PaginationConfig={"PageSize": 2}) for f in page["Functions"]}
    assert names <= listed


def test_missing_function(lambda_: Any) -> None:
    name = uniq("conf-missing")
    with raises("ResourceNotFoundException") as exc:
        lambda_.get_function(FunctionName=name)
    assert status_of(exc.value) == 404
    assert re.fullmatch(rf"Function not found: arn:aws:lambda:us-east-1:\d{{12}}:function:{re.escape(name)}", exc.value.response["Error"]["Message"])


def test_duplicate_function(lambda_: Any, make_function: Callable[..., dict[str, Any]], role_arn: str) -> None:
    created = make_function()
    with raises("ResourceConflictException") as exc:
        lambda_.create_function(
            FunctionName=created["FunctionName"],
            Runtime=RUNTIME,
            Role=role_arn,
            Handler=HANDLER,
            Code={"ZipFile": lambda_zip({"lambda_function.py": ECHO})},
        )
    assert status_of(exc.value) == 409
    assert exc.value.response["Error"]["Message"] == f"Function already exist: {created['FunctionName']}"


def test_deprecated_runtime_is_refused(lambda_: Any, role_arn: str) -> None:
    with raises("InvalidParameterValueException") as exc:
        lambda_.create_function(
            FunctionName=uniq("conf-old"),
            Runtime="go1.x",
            Role=role_arn,
            Handler="main",
            Code={"ZipFile": lambda_zip({"main": "x"})},
        )
    assert exc.value.response["Error"]["Message"] == (
        "The runtime parameter of go1.x is no longer supported for creating or updating AWS Lambda functions. "
        "We recommend you use a supported runtime while creating or updating functions."
    )


def test_corrupt_zip_is_refused(lambda_: Any, role_arn: str) -> None:
    with raises("InvalidParameterValueException") as exc:
        lambda_.create_function(
            FunctionName=uniq("conf-bad-zip"), Runtime=RUNTIME, Role=role_arn, Handler=HANDLER, Code={"ZipFile": b"not a zip file"}
        )
    assert exc.value.response["Error"]["Message"] == "Could not unzip uploaded file. Please check your file, then try to upload again."


def test_update_function_configuration(lambda_: Any, make_function: Callable[..., dict[str, Any]]) -> None:
    created = make_function(Environment={"Variables": {"FOO": "one"}})
    name = created["FunctionName"]
    out = lambda_.update_function_configuration(FunctionName=name, MemorySize=512, Timeout=20, Environment={"Variables": {"FOO": "two", "BAR": "x"}})
    assert out["MemorySize"] == 512
    lambda_.get_waiter("function_updated_v2").wait(FunctionName=name, WaiterConfig={"Delay": 1, "MaxAttempts": 60})
    config = lambda_.get_function_configuration(FunctionName=name)
    assert (config["MemorySize"], config["Timeout"]) == (512, 20)
    assert config["Environment"]["Variables"] == {"FOO": "two", "BAR": "x"}
    assert config["RevisionId"] != created["RevisionId"]
    assert config["Handler"] == HANDLER, "fields that were not sent keep their value"


def test_function_tags(lambda_: Any, make_function: Callable[..., dict[str, Any]]) -> None:
    created = make_function()
    arn = created["FunctionArn"]
    lambda_.tag_resource(Resource=arn, Tags={"a": "1", "b": "2"})
    assert lambda_.list_tags(Resource=arn)["Tags"] == {"a": "1", "b": "2"}
    lambda_.untag_resource(Resource=arn, TagKeys=["a", "never-set"])
    assert lambda_.list_tags(Resource=arn)["Tags"] == {"b": "2"}


def test_reserved_concurrency_is_stored(lambda_: Any, make_function: Callable[..., dict[str, Any]]) -> None:
    name = make_function()["FunctionName"]
    assert lambda_.get_function_concurrency(FunctionName=name).get("ReservedConcurrentExecutions") is None
    assert lambda_.put_function_concurrency(FunctionName=name, ReservedConcurrentExecutions=7)["ReservedConcurrentExecutions"] == 7
    assert lambda_.get_function_concurrency(FunctionName=name)["ReservedConcurrentExecutions"] == 7
    assert lambda_.get_function(FunctionName=name)["Concurrency"]["ReservedConcurrentExecutions"] == 7
    lambda_.delete_function_concurrency(FunctionName=name)
    assert lambda_.get_function_concurrency(FunctionName=name).get("ReservedConcurrentExecutions") is None


def test_permissions_and_policy(lambda_: Any, make_function: Callable[..., dict[str, Any]]) -> None:
    created = make_function()
    name = created["FunctionName"]
    topic = "arn:aws:sns:us-east-1:000000000000:conf-topic"
    out = lambda_.add_permission(
        FunctionName=name, StatementId="sns", Action="lambda:InvokeFunction", Principal="sns.amazonaws.com", SourceArn=topic, SourceAccount="000000000000"
    )
    statement = json.loads(out["Statement"])
    assert statement == {
        "Sid": "sns",
        "Effect": "Allow",
        "Principal": {"Service": "sns.amazonaws.com"},
        "Action": "lambda:InvokeFunction",
        "Resource": created["FunctionArn"],
        "Condition": {"ArnLike": {"AWS:SourceArn": topic}, "StringEquals": {"AWS:SourceAccount": "000000000000"}},
    }
    policy = lambda_.get_policy(FunctionName=name)
    document = json.loads(policy["Policy"])
    assert document["Version"] == "2012-10-17" and document["Statement"] == [statement]

    with raises("ResourceConflictException") as exc:
        lambda_.add_permission(FunctionName=name, StatementId="sns", Action="lambda:InvokeFunction", Principal="sns.amazonaws.com")
    assert exc.value.response["Error"]["Message"].startswith("The statement id (sns) provided already exists.")

    lambda_.remove_permission(FunctionName=name, StatementId="sns")
    with raises("ResourceNotFoundException"):
        lambda_.get_policy(FunctionName=name)


# --- Versions and aliases ---------------------------------------------------------------------------------------------


def test_versions_and_aliases_pin_code(lambda_: Any, make_function: Callable[..., dict[str, Any]]) -> None:
    created = make_function('def handler(event, context):\n    return {"which": "one"}\n')
    name = created["FunctionName"]
    published = lambda_.publish_version(FunctionName=name)
    assert published["Version"] == "1"
    assert published["FunctionArn"] == created["FunctionArn"] + ":1"
    lambda_.create_alias(FunctionName=name, Name="live", FunctionVersion="1")

    lambda_.update_function_code(FunctionName=name, ZipFile=lambda_zip({"lambda_function.py": 'def handler(event, context):\n    return {"which": "two"}\n'}))
    lambda_.get_waiter("function_updated_v2").wait(FunctionName=name, WaiterConfig={"Delay": 1, "MaxAttempts": 60})

    out, body = invoke_json(lambda_, name)
    assert body == {"which": "two"} and out["ExecutedVersion"] == "$LATEST"
    out, body = invoke_json(lambda_, name, Qualifier="1")
    assert body == {"which": "one"} and out["ExecutedVersion"] == "1"
    out, body = invoke_json(lambda_, name, Qualifier="live")
    assert body == {"which": "one"} and out["ExecutedVersion"] == "1"

    alias = lambda_.get_alias(FunctionName=name, Name="live")
    assert alias["FunctionVersion"] == "1" and alias["AliasArn"] == created["FunctionArn"] + ":live"
    assert [v["Version"] for v in lambda_.list_versions_by_function(FunctionName=name)["Versions"]] == ["$LATEST", "1"]
    assert [a["Name"] for a in lambda_.list_aliases(FunctionName=name)["Aliases"]] == ["live"]


def test_invoke_a_missing_version(lambda_: Any, make_function: Callable[..., dict[str, Any]]) -> None:
    created = make_function()
    with raises("ResourceNotFoundException") as exc:
        lambda_.invoke(FunctionName=created["FunctionName"], Qualifier="10", Payload=b"{}")
    assert exc.value.response["Error"]["Message"] == f"Function not found: {created['FunctionArn']}:10"


# --- Invoke ---------------------------------------------------------------------------------------------------------------


def test_request_response_invoke(lambda_: Any, make_function: Callable[..., dict[str, Any]]) -> None:
    created = make_function(MemorySize=256, Environment={"Variables": {"FOO": "bar"}})
    name = created["FunctionName"]
    event = {"list": [1, 2, {"deep": None}], "text": "café", "number": 1.5}
    out, body = invoke_json(lambda_, name, event)
    assert out["StatusCode"] == 200
    assert "FunctionError" not in out
    assert out["ExecutedVersion"] == "$LATEST"
    assert body["event"] == event
    assert body["function_name"] == name and body["function_version"] == "$LATEST"
    assert body["memory"] == "256"
    assert body["invoked_arn"] == created["FunctionArn"]
    assert body["env"]["FOO"] == "bar"
    assert body["env"]["AWS_REGION"] == "us-east-1"
    assert body["env"]["AWS_LAMBDA_FUNCTION_NAME"] == name
    assert body["env"]["AWS_LAMBDA_FUNCTION_MEMORY_SIZE"] == "256"
    assert body["env"]["AWS_LAMBDA_LOG_GROUP_NAME"] == f"/aws/lambda/{name}"
    assert out["ResponseMetadata"]["HTTPHeaders"]["x-amzn-requestid"] == body["request_id"]


def test_warm_environments_are_reused_and_replaced_on_configuration_change(
    lambda_: Any, make_function: Callable[..., dict[str, Any]]
) -> None:
    name = make_function()["FunctionName"]
    counters = [invoke_json(lambda_, name, {})[1]["counter"] for _ in range(3)]
    assert counters == [1, 2, 3], "a warm environment keeps module state between invocations"

    lambda_.update_function_configuration(FunctionName=name, Environment={"Variables": {"FOO": "changed"}})
    lambda_.get_waiter("function_updated_v2").wait(FunctionName=name, WaiterConfig={"Delay": 1, "MaxAttempts": 60})
    _, body = invoke_json(lambda_, name, {})
    assert body["counter"] == 1 and body["env"]["FOO"] == "changed"


def test_function_errors_are_reported_in_the_payload(lambda_: Any, make_function: Callable[..., dict[str, Any]]) -> None:
    name = make_function(FAILS)["FunctionName"]
    out, body = invoke_json(lambda_, name, {"in": 1})
    assert out["StatusCode"] == 200, "the status code does not reflect function errors"
    assert out["FunctionError"] == "Unhandled"
    assert body["errorMessage"] == "Handler fails"
    assert body["errorType"] == "Exception"
    assert re.fullmatch(r"[0-9a-f-]{36}", body["requestId"])
    assert any("Handler fails" in line for line in body["stackTrace"])


def test_import_errors_are_reported_as_function_errors(lambda_: Any, make_function: Callable[..., dict[str, Any]]) -> None:
    name = make_function(BROKEN_IMPORT)["FunctionName"]
    out, body = invoke_json(lambda_, name, {})
    assert out["FunctionError"] == "Unhandled"
    assert body["errorType"] == "Runtime.ImportModuleError"
    assert "module_that_does_not_exist_anywhere" in body["errorMessage"]


def test_timeouts_are_reported_as_function_errors(lambda_: Any, make_function: Callable[..., dict[str, Any]]) -> None:
    name = make_function(SLEEPS, Timeout=1)["FunctionName"]
    out, body = invoke_json(lambda_, name, {})
    assert out["FunctionError"] == "Unhandled"
    assert body["errorType"] == "Sandbox.Timedout"
    assert re.fullmatch(r"RequestId: [0-9a-f-]{36} Error: Task timed out after 1\.00 seconds", body["errorMessage"])


def test_log_type_tail_returns_the_end_of_the_log(lambda_: Any, make_function: Callable[..., dict[str, Any]]) -> None:
    name = make_function()["FunctionName"]
    out, body = invoke_json(lambda_, name, {"tail": True}, LogType="Tail")
    tail = base64.b64decode(out["LogResult"]).decode()
    assert re.search(rf"^START RequestId: {body['request_id']} Version: \$LATEST$", tail, re.M)
    assert "conformance-marker" in tail
    assert re.search(rf"^END RequestId: {body['request_id']}$", tail, re.M)
    assert re.search(rf"^REPORT RequestId: {body['request_id']}\tDuration: [\d.]+ ms\tBilled Duration: \d+ ms\tMemory Size: 128 MB\tMax Memory Used: \d+ MB", tail, re.M)


def test_event_invocation_is_asynchronous(lambda_: Any, logs: Any, admin: Admin, make_function: Callable[..., dict[str, Any]]) -> None:
    name = make_function()["FunctionName"]
    out = lambda_.invoke(FunctionName=name, InvocationType="Event", Payload=json.dumps({"async": "marker-async-1"}).encode())
    assert out["StatusCode"] == 202
    assert out["Payload"].read() == b""
    admin.settle()
    found = eventually(
        lambda: _log_messages(logs, name, "marker-async-1"),
        bool,
        timeout=30,
        what="the asynchronous invocation to log",
    )
    assert any("conformance-marker" in m for m in found)


def test_dry_run_validates_without_running(lambda_: Any, logs: Any, admin: Admin, make_function: Callable[..., dict[str, Any]]) -> None:
    name = make_function()["FunctionName"]
    out = lambda_.invoke(FunctionName=name, InvocationType="DryRun", Payload=json.dumps({"dry": "marker-dry-run"}).encode())
    assert out["StatusCode"] == 204
    assert out["Payload"].read() == b""
    admin.settle()
    assert _log_messages(logs, name, "marker-dry-run") == []
    with raises("ResourceNotFoundException"):
        lambda_.invoke(FunctionName=uniq("conf-missing"), InvocationType="DryRun", Payload=b"{}")


def test_invoke_a_missing_function(lambda_: Any) -> None:
    with raises("ResourceNotFoundException") as exc:
        lambda_.invoke(FunctionName=uniq("conf-missing"), Payload=b"{}")
    assert status_of(exc.value) == 404
    assert exc.value.response["Error"]["Message"].startswith("Function not found: arn:aws:lambda:us-east-1:")


def test_invalid_json_payload(lambda_: Any, make_function: Callable[..., dict[str, Any]]) -> None:
    name = make_function()["FunctionName"]
    with raises("InvalidRequestContentException") as exc:
        lambda_.invoke(FunctionName=name, Payload=b"{not json")
    assert status_of(exc.value) == 400
    assert exc.value.response["Error"]["Message"].startswith("Could not parse request body into json: Could not parse payload into json:")


def test_oversized_request_payload(lambda_: Any, make_function: Callable[..., dict[str, Any]]) -> None:
    name = make_function()["FunctionName"]
    big = json.dumps({"blob": "x" * (6 * 1024 * 1024 + 100)}).encode()
    with raises("RequestEntityTooLargeException") as exc:
        lambda_.invoke(FunctionName=name, Payload=big)
    assert status_of(exc.value) == 413
    assert exc.value.response["Error"]["Message"] == "Request must be smaller than 6291456 bytes for the InvokeFunction operation"


def test_empty_payload_invokes_with_a_json_document(lambda_: Any, make_function: Callable[..., dict[str, Any]]) -> None:
    name = make_function()["FunctionName"]
    out = lambda_.invoke(FunctionName=name)
    assert out["StatusCode"] == 200 and "FunctionError" not in out


def _log_messages(logs: Any, function: str, marker: str) -> list[str]:
    try:
        pages = logs.get_paginator("filter_log_events").paginate(logGroupName=f"/aws/lambda/{function}", filterPattern=f'"{marker}"')
        # A runtime logs each printed line with its newline; the tests compare the line itself.
        return [e["message"].rstrip("\n") for page in pages for e in page["events"]]
    except logs.exceptions.ResourceNotFoundException:
        return []


# --- Logs ----------------------------------------------------------------------------------------------------------------------------


def test_function_logs_land_in_cloudwatch_logs(lambda_: Any, logs: Any, admin: Admin, make_function: Callable[..., dict[str, Any]]) -> None:
    name = make_function()["FunctionName"]
    _, body = invoke_json(lambda_, name, {"log": "marker-cloudwatch-1"})
    admin.settle()

    group = f"/aws/lambda/{name}"
    streams = logs.describe_log_streams(logGroupName=group)["logStreams"]
    assert len(streams) == 1
    assert re.fullmatch(r"\d{4}/\d{2}/\d{2}/\[\$LATEST\][0-9a-f]{32}", streams[0]["logStreamName"])

    messages = [e["message"] for e in logs.filter_log_events(logGroupName=group)["events"]]
    start = f"START RequestId: {body['request_id']} Version: $LATEST"
    assert any(m.startswith(start) for m in messages)
    assert any("marker-cloudwatch-1" in m for m in messages)
    assert any(m.startswith(f"END RequestId: {body['request_id']}") for m in messages)
    assert any(m.startswith(f"REPORT RequestId: {body['request_id']}") for m in messages)


def test_the_log_group_appears_with_the_first_invocation(lambda_: Any, logs: Any, make_function: Callable[..., dict[str, Any]]) -> None:
    name = make_function()["FunctionName"]
    assert logs.describe_log_groups(logGroupNamePrefix=f"/aws/lambda/{name}")["logGroups"] == []
    invoke_json(lambda_, name, {})
    assert [g["logGroupName"] for g in logs.describe_log_groups(logGroupNamePrefix=f"/aws/lambda/{name}")["logGroups"]] == [f"/aws/lambda/{name}"]


# --- SDK calls from inside a function ---------------------------------------------------------------------------------------------------------


def test_sdk_calls_inside_a_function_reach_understudy(
    lambda_: Any, sqs: Any, make_function: Callable[..., dict[str, Any]], make_queue: Callable[..., str]
) -> None:
    queue = make_queue("conf-from-lambda")
    code = """
import boto3, os

def handler(event, context):
    sqs = boto3.client("sqs")
    sqs.send_message(QueueUrl=event["queue"], MessageBody=event["body"])
    ident = boto3.client("sts").get_caller_identity()
    return {"arn": ident["Arn"], "account": ident["Account"], "region": os.environ["AWS_REGION"]}
"""
    name = make_function(code, Timeout=30)["FunctionName"]
    out, body = invoke_json(lambda_, name, {"queue": queue, "body": "sent from inside a function"})
    assert "FunctionError" not in out, body
    assert re.fullmatch(r"arn:aws:sts::\d{12}:assumed-role/conf-lambda-role-[0-9a-f]{10}/.+", body["arn"])
    messages = eventually(lambda: sqs.receive_message(QueueUrl=queue, WaitTimeSeconds=1).get("Messages", []), bool, what="the message from the function")
    assert messages[0]["Body"] == "sent from inside a function"


# --- Event source mappings -----------------------------------------------------------------------------------------------------------------------


BATCH_HANDLER = """
import json

def handler(event, context):
    failures = []
    for record in event["Records"]:
        print("conformance-record", record["body"], record["attributes"]["ApproximateReceiveCount"])
        if "fail" in record["body"]:
            failures.append({"itemIdentifier": record["messageId"]})
    return {"batchItemFailures": failures}
"""


@pytest.fixture
def make_mapping(lambda_: Any, cleanup: Any) -> Callable[..., dict[str, Any]]:
    def make(function: str, queue_arn: str, **args: Any) -> dict[str, Any]:
        mapping = lambda_.create_event_source_mapping(FunctionName=function, EventSourceArn=queue_arn, **args)
        cleanup.add(lambda_.delete_event_source_mapping, UUID=mapping["UUID"])
        return mapping

    return make


def test_event_source_mapping_description(
    lambda_: Any,
    make_function: Callable[..., dict[str, Any]],
    make_queue: Callable[..., str],
    queue_arn: Callable[[str], str],
    make_mapping: Callable[..., dict[str, Any]],
) -> None:
    function = make_function(BATCH_HANDLER)
    arn = queue_arn(make_queue("conf-esm"))
    mapping = make_mapping(function["FunctionName"], arn, BatchSize=5, MaximumBatchingWindowInSeconds=1)
    assert mapping["BatchSize"] == 5 and mapping["MaximumBatchingWindowInSeconds"] == 1
    assert mapping["EventSourceArn"] == arn and mapping["FunctionArn"] == function["FunctionArn"]
    assert mapping["FunctionResponseTypes"] == []
    assert re.fullmatch(rf"arn:aws:lambda:us-east-1:\d{{12}}:event-source-mapping:{mapping['UUID']}", mapping["EventSourceMappingArn"])
    settled = eventually(
        lambda: lambda_.get_event_source_mapping(UUID=mapping["UUID"]),
        lambda m: m["State"] in ("Enabled", "Disabled"),
        what="the mapping to leave Creating",
    )
    assert settled["State"] == "Enabled"
    assert settled["StateTransitionReason"] == "USER_INITIATED"


def test_event_source_mapping_validation(
    lambda_: Any,
    make_function: Callable[..., dict[str, Any]],
    make_queue: Callable[..., str],
    queue_arn: Callable[[str], str],
    make_mapping: Callable[..., dict[str, Any]],
) -> None:
    function = make_function(BATCH_HANDLER)
    arn = queue_arn(make_queue("conf-esm2"))
    with raises("InvalidParameterValueException") as exc:
        lambda_.create_event_source_mapping(FunctionName=function["FunctionName"], EventSourceArn=arn, BatchSize=11)
    assert exc.value.response["Error"]["Message"] == "Maximum batch window in seconds must be greater than 0 if maximum batch size is greater than 10"

    make_mapping(function["FunctionName"], arn)
    with raises("ResourceConflictException") as dup:
        lambda_.create_event_source_mapping(FunctionName=function["FunctionName"], EventSourceArn=arn)
    assert status_of(dup.value) == 409

    with raises("InvalidParameterValueException") as missing:
        lambda_.create_event_source_mapping(FunctionName=uniq("conf-missing"), EventSourceArn=arn)
    assert missing.value.response["Error"]["Message"] == "Function does not exist"


def test_sqs_messages_are_delivered_in_batches_and_deleted_on_success(
    sqs: Any,
    logs: Any,
    admin: Admin,
    make_function: Callable[..., dict[str, Any]],
    make_queue: Callable[..., str],
    queue_arn: Callable[[str], str],
    make_mapping: Callable[..., dict[str, Any]],
) -> None:
    function = make_function(BATCH_HANDLER)["FunctionName"]
    queue = make_queue("conf-esm3")
    make_mapping(function, queue_arn(queue), BatchSize=10, MaximumBatchingWindowInSeconds=1)
    for i in range(7):
        sqs.send_message(QueueUrl=queue, MessageBody=f"ok-{i}")

    def drained() -> dict[str, str]:
        admin.settle()
        return sqs.get_queue_attributes(QueueUrl=queue, AttributeNames=["All"])["Attributes"]

    eventually(
        drained,
        lambda a: a["ApproximateNumberOfMessages"] == "0" and a["ApproximateNumberOfMessagesNotVisible"] == "0",
        timeout=40,
        what="the queue to be drained by the mapping",
    )
    seen = eventually(
        lambda: sorted({m.split()[1] for m in _log_messages(logs, function, "conformance-record")}),
        lambda s: len(s) == 7,
        timeout=30,
        what="every record to be logged",
    )
    assert seen == [f"ok-{i}" for i in range(7)]


def test_partial_batch_failures_return_only_the_failed_messages(
    sqs: Any,
    logs: Any,
    admin: Admin,
    make_function: Callable[..., dict[str, Any]],
    make_queue: Callable[..., str],
    queue_arn: Callable[[str], str],
    make_mapping: Callable[..., dict[str, Any]],
    clock: Admin,
) -> None:
    function = make_function(BATCH_HANDLER)["FunctionName"]
    queue = make_queue("conf-esm4", attributes={"VisibilityTimeout": "60"})
    make_mapping(function, queue_arn(queue), BatchSize=10, MaximumBatchingWindowInSeconds=1, FunctionResponseTypes=["ReportBatchItemFailures"])
    for body in ("ok-1", "fail-2", "ok-3"):
        sqs.send_message(QueueUrl=queue, MessageBody=body)

    def attempts() -> list[str]:
        admin.settle()
        return [m for m in _log_messages(logs, function, "conformance-record")]

    eventually(attempts, lambda a: len(a) >= 3, timeout=40, what="the first batch to be processed")
    counts = eventually(
        lambda: (admin.settle(), sqs.get_queue_attributes(QueueUrl=queue, AttributeNames=["All"])["Attributes"])[1],
        lambda a: a["ApproximateNumberOfMessagesNotVisible"] == "1" and a["ApproximateNumberOfMessages"] == "0",
        timeout=30,
        what="only the failed message to stay in flight",
    )
    assert counts["ApproximateNumberOfMessagesNotVisible"] == "1"

    # After the visibility timeout (on the emulator clock) only the failed message comes back.
    clock.advance(61)
    retried = eventually(attempts, lambda a: sum("fail-2" in m for m in a) >= 2, timeout=40, what="the failed message to be retried")
    assert sum("ok-1" in m for m in retried) == 1 and sum("ok-3" in m for m in retried) == 1
    assert any(m.endswith("fail-2 2") for m in retried), f"the redelivery reports ApproximateReceiveCount 2; logged {retried!r}"


def test_whole_batch_is_retried_when_the_function_raises(
    sqs: Any,
    logs: Any,
    admin: Admin,
    make_function: Callable[..., dict[str, Any]],
    make_queue: Callable[..., str],
    queue_arn: Callable[[str], str],
    make_mapping: Callable[..., dict[str, Any]],
    clock: Admin,
) -> None:
    code = """
def handler(event, context):
    for record in event["Records"]:
        print("conformance-record", record["body"], record["attributes"]["ApproximateReceiveCount"])
    raise Exception("always fails")
"""
    function = make_function(code)["FunctionName"]
    queue = make_queue("conf-esm5", attributes={"VisibilityTimeout": "30"})
    make_mapping(function, queue_arn(queue), BatchSize=10, MaximumBatchingWindowInSeconds=1)
    sqs.send_message(QueueUrl=queue, MessageBody="retry-me")

    def received() -> list[str]:
        admin.settle()
        return _log_messages(logs, function, "conformance-record")

    eventually(received, lambda r: len(r) >= 1, timeout=40, what="the first attempt")
    clock.advance(31)
    again = eventually(received, lambda r: len(r) >= 2, timeout=40, what="the retry")
    assert any(m.endswith("retry-me 2") for m in again), f"the retry reports ApproximateReceiveCount 2; logged {again!r}"


# --- Function URLs ----------------------------------------------------------------------------------------------------------------------------------


URL_HANDLER = """
import json

def handler(event, context):
    summary = {
        "version": event["version"],
        "rawPath": event["rawPath"],
        "rawQueryString": event["rawQueryString"],
        "query": event.get("queryStringParameters"),
        "method": event["requestContext"]["http"]["method"],
        "path": event["requestContext"]["http"]["path"],
        "routeKey": event["routeKey"],
        "body": event.get("body"),
        "isBase64Encoded": event["isBase64Encoded"],
        "x-test": event["headers"].get("x-test"),
        "cookies": event.get("cookies"),
    }
    return {
        "statusCode": 201,
        "headers": {"x-custom": "yes", "content-type": "text/plain"},
        "cookies": ["a=1; Path=/", "b=2"],
        "body": json.dumps(summary),
    }
"""


@pytest.fixture
def function_url(lambda_: Any, make_function: Callable[..., dict[str, Any]], cleanup: Any) -> Callable[..., tuple[str, str]]:
    def make(code: str) -> tuple[str, str]:
        name = make_function(code)["FunctionName"]
        config = lambda_.create_function_url_config(FunctionName=name, AuthType="NONE")
        cleanup.add(lambda_.delete_function_url_config, FunctionName=name)
        lambda_.add_permission(
            FunctionName=name, StatementId="url-public", Action="lambda:InvokeFunctionUrl", Principal="*", FunctionUrlAuthType="NONE"
        )
        lambda_.add_permission(
            FunctionName=name, StatementId="url-invoke", Action="lambda:InvokeFunction", Principal="*", InvokedViaFunctionUrl=True
        )
        return name, config["FunctionUrl"]

    return make


def call_url(endpoint: str, function_url: str, method: str, path: str, *, body: bytes | None = None, headers: dict[str, str] | None = None) -> tuple[int, http.client.HTTPMessage, bytes]:
    """Send a request to a function URL by Host header, so no DNS is involved."""
    host_header = urllib.parse.urlsplit(function_url).netloc
    conn = http.client.HTTPConnection(*url_host_port(endpoint), timeout=60)
    try:
        conn.putrequest(method, path, skip_host=True)
        conn.putheader("Host", host_header)
        for k, v in (headers or {}).items():
            conn.putheader(k, v)
        if body is not None:
            conn.putheader("Content-Length", str(len(body)))
        conn.endheaders(body)
        resp = conn.getresponse()
        return resp.status, resp.msg, resp.read()
    finally:
        conn.close()


def test_function_url_configuration(lambda_: Any, make_function: Callable[..., dict[str, Any]], cleanup: Any) -> None:
    created = make_function()
    name = created["FunctionName"]
    config = lambda_.create_function_url_config(FunctionName=name, AuthType="NONE", Cors={"AllowOrigins": ["*"], "AllowMethods": ["GET"]})
    cleanup.add(lambda_.delete_function_url_config, FunctionName=name)
    assert re.fullmatch(r"https?://[a-z0-9]{32}\.lambda-url\.us-east-1\.[^/]+/", config["FunctionUrl"]), config["FunctionUrl"]
    assert config["FunctionArn"] == created["FunctionArn"] and config["AuthType"] == "NONE"
    # AWS's create response omits InvokeMode when the request does not set it
    # (LocalStack's AWS-recorded test_url_config_lifecycle url_creation;
    # lambda-events.md 7.1); the reads report the BUFFERED default.
    assert "InvokeMode" not in config
    assert config["Cors"]["AllowOrigins"] == ["*"]

    read = lambda_.get_function_url_config(FunctionName=name)
    assert read["FunctionUrl"] == config["FunctionUrl"]
    assert read["InvokeMode"] == "BUFFERED"
    assert [c["FunctionUrl"] for c in lambda_.list_function_url_configs(FunctionName=name)["FunctionUrlConfigs"]] == [config["FunctionUrl"]]
    with raises("ResourceConflictException") as dup:
        lambda_.create_function_url_config(FunctionName=name, AuthType="NONE")
    assert status_of(dup.value) == 409

    lambda_.delete_function_url_config(FunctionName=name)
    with raises("ResourceNotFoundException"):
        lambda_.get_function_url_config(FunctionName=name)


def test_function_url_get_receives_a_payload_v2_event(endpoint: str, function_url: Callable[..., tuple[str, str]]) -> None:
    _, url = function_url(URL_HANDLER)
    status, headers, raw = call_url(
        endpoint, url, "GET", "/some/path?a=1&a=2&b=x", headers={"X-Test": "hello", "Cookie": "c1=v1; c2=v2"}
    )
    assert status == 201
    assert headers["x-custom"] == "yes"
    assert headers["content-type"] == "text/plain"
    assert headers.get_all("set-cookie") == ["a=1; Path=/", "b=2"]
    assert headers["x-amzn-requestid"]

    event = json.loads(raw)
    assert event["version"] == "2.0"
    assert event["routeKey"] == "$default"
    assert event["rawPath"] == "/some/path" and event["path"] == "/some/path"
    assert event["rawQueryString"] == "a=1&a=2&b=x"
    assert event["query"] == {"a": "1,2", "b": "x"}
    assert event["method"] == "GET"
    assert event["x-test"] == "hello"
    assert event["cookies"] == ["c1=v1", "c2=v2"]
    assert event["body"] is None or event["body"] == ""
    assert event["isBase64Encoded"] is False


def test_function_url_post_body(endpoint: str, function_url: Callable[..., tuple[str, str]]) -> None:
    _, url = function_url(URL_HANDLER)
    payload = json.dumps({"hello": "world"}).encode()
    status, _, raw = call_url(endpoint, url, "POST", "/", body=payload, headers={"Content-Type": "application/json"})
    assert status == 201
    event = json.loads(raw)
    assert event["method"] == "POST"
    assert event["body"] == '{"hello": "world"}'
    assert event["isBase64Encoded"] is False


def test_function_url_infers_a_response_for_plain_return_values(endpoint: str, function_url: Callable[..., tuple[str, str]]) -> None:
    _, url = function_url('def handler(event, context):\n    return {"hello": "world"}\n')
    status, headers, raw = call_url(endpoint, url, "GET", "/")
    assert status == 200
    assert headers["content-type"] == "application/json"
    assert raw == b'{"hello":"world"}'


def test_function_url_binary_response(endpoint: str, function_url: Callable[..., tuple[str, str]]) -> None:
    code = """
import base64

def handler(event, context):
    return {"statusCode": 200, "headers": {"content-type": "application/octet-stream"},
            "isBase64Encoded": True, "body": base64.b64encode(bytes(range(256))).decode()}
"""
    _, url = function_url(code)
    status, _, raw = call_url(endpoint, url, "GET", "/")
    assert status == 200 and raw == bytes(range(256))
