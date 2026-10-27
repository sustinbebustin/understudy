"""Lambda layers and asynchronous invocation destinations (docs/research/lambda-destinations-layers.md,
lambda-api.md 7.7): the nine layer operations, layers on functions, what reaches /opt, and the
invocation records Lambda sends to SQS, SNS and EventBridge destinations.

Bare REC line numbers are in LocalStack's AWS-validated test_lambda_api.snapshot.json at
localstack@8b9a79f, as the research note cites them. Tests marked docker run functions in AWS's
Python base image.
"""

from __future__ import annotations

import base64
import hashlib
import json
import re
from collections.abc import Callable
from typing import Any

import pytest

from support import REGION, Admin, eventually, http_get, lambda_zip, raises, status_of, uniq

RUNTIME = "python3.13"
HANDLER = "lambda_function.handler"

TRUST_LAMBDA = json.dumps(
    {
        "Version": "2012-10-17",
        "Statement": [{"Effect": "Allow", "Principal": {"Service": "lambda.amazonaws.com"}, "Action": "sts:AssumeRole"}],
    }
)

GREET = {"python/greet.py": "def hello():\n    return 'hello from the layer'\n"}

ECHO = "def handler(event, context):\n    return {'echo': event}\n"

FAILS = 'def handler(event, context):\n    raise Exception("Handler fails")\n'

RECORD_TIMESTAMP = re.compile(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z")


@pytest.fixture(scope="module")
def account(sts: Any) -> str:
    return sts.get_caller_identity()["Account"]


@pytest.fixture(scope="module")
def events(make_client: Callable[..., Any]) -> Any:
    return make_client("events")


@pytest.fixture
def publish(lambda_: Any, cleanup: Any) -> Callable[..., dict[str, Any]]:
    """Publish a layer version, deleted after the test; returns the PublishLayerVersion answer."""

    def make(name: str, files: dict[str, str] = GREET, *, client: Any = None, **args: Any) -> dict[str, Any]:
        c = lambda_ if client is None else client
        out = c.publish_layer_version(LayerName=name, Content={"ZipFile": lambda_zip(files)}, **args)
        cleanup.add(c.delete_layer_version, LayerName=name, VersionNumber=out["Version"])
        return out

    return make


@pytest.fixture
def role(iam: Any, cleanup: Any) -> tuple[str, str]:
    """An execution role Lambda may assume; returns (name, arn)."""
    name = uniq("conf-layers-role")
    arn = iam.create_role(RoleName=name, AssumeRolePolicyDocument=TRUST_LAMBDA)["Role"]["Arn"]
    cleanup.add(iam.delete_role, RoleName=name)
    return name, arn


@pytest.fixture
def allow(iam: Any, role: tuple[str, str], cleanup: Any) -> Callable[[str, str], None]:
    """Grant the execution role one action on one resource, as a destination needs (2.5)."""

    def grant(action: str, resource: str) -> None:
        policy = uniq("conf-dest")
        document = {"Version": "2012-10-17", "Statement": [{"Effect": "Allow", "Action": action, "Resource": resource}]}
        iam.put_role_policy(RoleName=role[0], PolicyName=policy, PolicyDocument=json.dumps(document))
        cleanup.add(iam.delete_role_policy, RoleName=role[0], PolicyName=policy)

    return grant


@pytest.fixture
def make_function(lambda_: Any, logs: Any, role: tuple[str, str], cleanup: Any) -> Callable[..., dict[str, Any]]:
    def make(code: str = ECHO, **args: Any) -> dict[str, Any]:
        name = uniq("conf-layers-fn")
        created = lambda_.create_function(
            FunctionName=name,
            Runtime=RUNTIME,
            Role=role[1],
            Handler=HANDLER,
            Code={"ZipFile": lambda_zip({"lambda_function.py": code})},
            **args,
        )
        cleanup.add(logs.delete_log_group, logGroupName=f"/aws/lambda/{name}")
        cleanup.add(lambda_.delete_function, FunctionName=name)
        lambda_.get_waiter("function_active_v2").wait(FunctionName=name, WaiterConfig={"Delay": 1, "MaxAttempts": 60})
        return created

    return make


def received(sqs: Any, admin: Admin, url: str) -> list[dict[str, Any]]:
    """Every message the queue holds once the emulator has settled, waiting for at least one."""

    def drain() -> list[dict[str, Any]]:
        out: list[dict[str, Any]] = []
        while True:
            batch = sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=10, MessageAttributeNames=["All"]).get("Messages", [])
            if not batch:
                return out
            for m in batch:
                sqs.delete_message(QueueUrl=url, ReceiptHandle=m["ReceiptHandle"])
            out.extend(batch)

    admin.settle()
    return eventually(drain, bool, timeout=60, what=f"a message in {url}")


def allow_service_to_send(sqs: Any, queue_url: str, queue_arn: str, service: str, source_arn: str) -> None:
    """The queue policy AWS needs before SNS or EventBridge may send to a queue."""
    policy = {
        "Version": "2012-10-17",
        "Statement": [
            {
                "Effect": "Allow",
                "Principal": {"Service": service},
                "Action": "sqs:SendMessage",
                "Resource": queue_arn,
                "Condition": {"ArnEquals": {"aws:SourceArn": source_arn}},
            }
        ],
    }
    sqs.set_queue_attributes(QueueUrl=queue_url, Attributes={"Policy": json.dumps(policy)})


# Layer versions.


def test_publish_from_s3_keeps_the_metadata_as_sent(
    lambda_: Any, s3: Any, make_bucket: Callable[..., str], publish: Callable[..., dict[str, Any]], cleanup: Any
) -> None:
    # 6.3: Content from S3, an S3Key with a leading slash accepted (REC L9201-L9221); the
    # compatible lists come back in the order sent (REC L13488-L13491); CodeSha256 is the
    # base64 SHA-256 of the archive and CodeSize its length; Content.Location serves it (6.3).
    archive = lambda_zip(GREET)
    bucket = make_bucket("conf-layer-src")
    s3.put_object(Bucket=bucket, Key="/layercontent.zip", Body=archive)
    name = uniq("conf-layer-s3")
    out = lambda_.publish_layer_version(
        LayerName=name,
        Description="from s3",
        LicenseInfo="MIT",
        CompatibleRuntimes=["python3.13", "python3.12"],
        CompatibleArchitectures=["arm64", "x86_64"],
        Content={"S3Bucket": bucket, "S3Key": "/layercontent.zip"},
    )
    cleanup.add(lambda_.delete_layer_version, LayerName=name, VersionNumber=out["Version"])
    assert out["Version"] == 1
    assert out["Description"] == "from s3" and out["LicenseInfo"] == "MIT"
    assert out["CompatibleRuntimes"] == ["python3.13", "python3.12"]
    assert out["CompatibleArchitectures"] == ["arm64", "x86_64"]
    assert out["Content"]["CodeSha256"] == base64.b64encode(hashlib.sha256(archive).digest()).decode()
    assert out["Content"]["CodeSize"] == len(archive)

    got = lambda_.get_layer_version(LayerName=name, VersionNumber=1)
    for key in ("LayerVersionArn", "Description", "LicenseInfo", "CompatibleRuntimes", "CompatibleArchitectures", "CreatedDate"):
        assert got[key] == out[key], key
    status, _, body = http_get(got["Content"]["Location"])
    assert status == 200 and body == archive


def test_identical_content_publishes_a_new_version_with_the_same_sha(publish: Callable[..., dict[str, Any]]) -> None:
    # 6.2: publishing the same archive again creates version 2 with the same CodeSha256
    # (REC test_lambda_api.py:L6576-L6594, snapshot L8565-L8610).
    name = uniq("conf-layer-same")
    first, second = publish(name), publish(name)
    assert (first["Version"], second["Version"]) == (1, 2)
    assert second["Content"]["CodeSha256"] == first["Content"]["CodeSha256"]
    assert second["LayerVersionArn"] == first["LayerArn"] + ":2"


def test_list_layer_versions_without_versions_is_empty(lambda_: Any, publish: Callable[..., dict[str, Any]]) -> None:
    # 6.5: a layer name that never existed and a layer whose versions are all deleted both
    # answer an empty list (REC L8313-L8319, L8858-L8864).
    assert lambda_.list_layer_versions(LayerName=uniq("conf-layer-never"))["LayerVersions"] == []
    name = uniq("conf-layer-gone")
    publish(name)
    lambda_.delete_layer_version(LayerName=name, VersionNumber=1)
    assert lambda_.list_layer_versions(LayerName=name)["LayerVersions"] == []


def test_list_filters_by_runtime_and_architecture(lambda_: Any, publish: Callable[..., dict[str, Any]]) -> None:
    # 6.5: a runtime filter lists only layers "that indicate that they're compatible with that
    # runtime", with the latest matching version (SMITHY ListLayers); list items carry no
    # Content (REC L8762-L8789).
    name = uniq("conf-layer-filter")
    publish(name, CompatibleRuntimes=["python3.12"], CompatibleArchitectures=["x86_64"])
    publish(name, CompatibleRuntimes=["python3.13"], CompatibleArchitectures=["arm64"])

    def versions(**filters: str) -> list[int]:
        return [v["Version"] for v in lambda_.list_layer_versions(LayerName=name, **filters)["LayerVersions"]]

    assert versions() == [2, 1]
    assert versions(CompatibleRuntime="python3.12") == [1]
    assert versions(CompatibleArchitecture="arm64") == [2]

    def listed(**filters: str) -> list[dict[str, Any]]:
        pages = lambda_.get_paginator("list_layers").paginate(**filters)
        return [layer for page in pages for layer in page["Layers"] if layer["LayerName"] == name]

    latest = listed(CompatibleRuntime="python3.12")
    assert [layer["LatestMatchingVersion"]["Version"] for layer in latest] == [1]
    assert "Content" not in latest[0]["LatestMatchingVersion"]
    assert [layer["LatestMatchingVersion"]["Version"] for layer in listed()] == [2]
    assert listed(CompatibleRuntime="nodejs22.x") == []


def test_list_filters_refuse_unknown_values(lambda_: Any) -> None:
    # 6.5: the runtime filter is checked against AWS's 56-value internal list, printed verbatim
    # (REC L8293-L8312).
    runtimes = (
        "[ruby3.5, ruby2.6, dotnetcore1.0, python3.7, nodejs8.10, nasa, ruby2.7, python2.7-greengrass, dotnetcore2.0, "
        "python3.8, java21, dotnet6, dotnetcore2.1, python3.9, java11, nodejs6.10, provided, dotnetcore3.1, dotnet8, "
        "java25, java17, nodejs, nodejs4.3, java8.al2, go1.x, dotnet10, nodejs20.x, go1.9, byol, nodejs10.x, "
        "provided.al2023, nodejs22.x, python3.10, java8, nodejs12.x, python3.11, nodejs24.x, nodejs8.x, python3.12, "
        "nodejs14.x, nodejs8.9, nodejs26.x, python3.13, python3.14, nodejs16.x, python3.15, provided.al2, "
        "nodejs4.3-edge, nodejs18.x, ruby3.2, python3.4, ruby3.3, ruby3.4, ruby2.5, python3.6, python2.7]"
    )
    with raises("ValidationException") as exc:
        lambda_.list_layers(CompatibleRuntime="runtimedoesnotexist")
    assert exc.value.response["Error"]["Message"] == (
        "1 validation error detected: Value 'runtimedoesnotexist' at 'compatibleRuntime' failed to satisfy constraint: "
        "Member must satisfy enum value set: " + runtimes
    )
    with raises("ValidationException") as exc:
        lambda_.list_layer_versions(LayerName=uniq("conf-layer"), CompatibleArchitecture="invalidarch")
    assert exc.value.response["Error"]["Message"] == (
        "1 validation error detected: Value 'invalidarch' at 'compatibleArchitecture' failed to satisfy constraint: "
        "Member must satisfy enum value set: [x86_64, arm64]"
    )


def test_publish_refuses_unknown_runtimes_and_architectures(lambda_: Any) -> None:
    # 6.3: one ValidationException naming both members, the 25-value creation list for the
    # runtimes (REC L8444-L8463).
    with raises("ValidationException") as exc:
        lambda_.publish_layer_version(
            LayerName=uniq("conf-layer-bad"),
            Content={"ZipFile": lambda_zip(GREET)},
            CompatibleRuntimes=["invalidruntime"],
            CompatibleArchitectures=["invalidarch"],
        )
    assert status_of(exc.value) == 400
    assert exc.value.response["Error"]["Message"] == (
        "2 validation errors detected: Value '[invalidruntime]' at 'compatibleRuntimes' failed to satisfy constraint: "
        "Member must satisfy enum value set: [nodejs20.x, python3.14, provided.al2023, python3.12, python3.13, "
        "nodejs24.x, nodejs22.x, java25, python3.10, python3.11, java21, ruby3.3, ruby3.4, ruby3.2, python3.8, "
        "python3.9, java17, nodejs16.x, dotnet10, dotnet8, java11, dotnet6, nodejs18.x, provided.al2, java8.al2]; "
        "Value '[invalidarch]' at 'compatibleArchitectures' failed to satisfy constraint: Member must satisfy "
        "constraint: [Member must satisfy enum value set: [x86_64, arm64], Member must not be null]"
    )


def test_version_numbers_below_one_are_refused(lambda_: Any, publish: Callable[..., dict[str, Any]]) -> None:
    # 6.2, 6.8: Get and Delete of version 0 (REC L8332-L8355, L8414-L8425).
    name = uniq("conf-layer-zero")
    publish(name)
    for call in (lambda_.get_layer_version, lambda_.delete_layer_version):
        with raises("InvalidParameterValueException") as exc:
            call(LayerName=name, VersionNumber=0)
        assert status_of(exc.value) == 400
        assert exc.value.response["Error"]["Message"] == "Layer Version Cannot be less than 1"


def test_get_layer_version_by_arn_needs_a_version(lambda_: Any, publish: Callable[..., dict[str, Any]]) -> None:
    # 6.4: an unversioned ARN fails the pattern, printed without the model's anchors and outer
    # group (REC L8368-L8377).
    layer_arn = publish(uniq("conf-layer-arn"))["LayerArn"]
    with raises("ValidationException") as exc:
        lambda_.get_layer_version_by_arn(Arn=layer_arn)
    assert exc.value.response["Error"]["Message"] == (
        f"1 validation error detected: Value '{layer_arn}' at 'arn' failed to satisfy constraint: Member must satisfy "
        r"regular expression pattern: (arn:(aws[a-zA-Z-]*)?:lambda:(eusc-)?[a-z]{2}((-gov)|(-iso([a-z]?)))?-[a-z]+-\d{1}"
        r":\d{12}:layer:[a-zA-Z0-9-_]+:[0-9]+)|(arn:[a-zA-Z0-9-]+:lambda:::awslayer:[a-zA-Z0-9-_]+)"
    )


def test_layer_version_policy_statements(lambda_: Any, publish: Callable[..., dict[str, Any]]) -> None:
    # 6.7: "*" renders as the string "*", an organization adds a PrincipalOrgID condition, the
    # policy keeps insertion order and RevisionId changes on every add and remove (REC
    # L8927-L8940, L9112-L9198); 6.8: a bad action (L8941-L8950) and a missing version
    # (L8975-L9046).
    name = uniq("conf-layer-policy")
    version_arn = publish(name)["LayerVersionArn"]
    everyone = lambda_.add_layer_version_permission(
        LayerName=name, VersionNumber=1, StatementId="everyone", Action="lambda:GetLayerVersion", Principal="*"
    )
    assert everyone["Statement"] == (
        f'{{"Sid":"everyone","Effect":"Allow","Principal":"*","Action":"lambda:GetLayerVersion","Resource":"{version_arn}"}}'
    )
    org = lambda_.add_layer_version_permission(
        LayerName=name,
        VersionNumber=1,
        StatementId="org",
        Action="lambda:GetLayerVersion",
        Principal="*",
        OrganizationId="o-0123456789",
    )
    assert org["Statement"] == (
        f'{{"Sid":"org","Effect":"Allow","Principal":"*","Action":"lambda:GetLayerVersion","Resource":"{version_arn}",'
        '"Condition":{"StringEquals":{"aws:PrincipalOrgID":"o-0123456789"}}}'
    )
    assert org["RevisionId"] != everyone["RevisionId"]
    policy = lambda_.get_layer_version_policy(LayerName=name, VersionNumber=1)
    assert policy["RevisionId"] == org["RevisionId"]
    document = json.loads(policy["Policy"])
    assert (document["Version"], document["Id"]) == ("2012-10-17", "default")
    assert [s["Sid"] for s in document["Statement"]] == ["everyone", "org"]

    lambda_.remove_layer_version_permission(LayerName=name, VersionNumber=1, StatementId="everyone", RevisionId=org["RevisionId"])
    after = lambda_.get_layer_version_policy(LayerName=name, VersionNumber=1)
    assert after["RevisionId"] != org["RevisionId"]
    assert [s["Sid"] for s in json.loads(after["Policy"])["Statement"]] == ["org"]

    with raises("ValidationException") as exc:
        lambda_.add_layer_version_permission(LayerName=name, VersionNumber=1, StatementId="star", Action="*", Principal="*")
    assert exc.value.response["Error"]["Message"] == (
        "1 validation error detected: Value '*' at 'action' failed to satisfy constraint: "
        "Member must satisfy regular expression pattern: lambda:GetLayerVersion"
    )

    missing = version_arn.rsplit(":", 1)[0] + ":9"
    for call, args in (
        (lambda_.add_layer_version_permission, {"StatementId": "s", "Action": "lambda:GetLayerVersion", "Principal": "*"}),
        (lambda_.get_layer_version_policy, {}),
        (lambda_.remove_layer_version_permission, {"StatementId": "org"}),
    ):
        with raises("ResourceNotFoundException") as exc:
            call(LayerName=name, VersionNumber=9, **args)
        assert status_of(exc.value) == 404
        assert exc.value.response["Error"]["Message"] == f"Layer version {missing} does not exist."


# Layers on functions.


def test_create_function_refuses_bad_layer_lists(
    lambda_: Any, make_client: Callable[..., Any], role: tuple[str, str], publish: Callable[..., dict[str, Any]], cleanup: Any
) -> None:
    # 7.1, every layer named here exists, so each case meets only its own refusal: six layers
    # (REC L12313-L12329), two versions of one layer (L9336-L9371), a layer in another Region
    # (L9406-L9417) and an unversioned ARN (L9396-L9405, the pattern as printed in 6.4).
    six = [publish(uniq("conf-layer-six"))["LayerVersionArn"] for _ in range(6)]
    twice = uniq("conf-layer-twice")
    v1, v2 = publish(twice)["LayerVersionArn"], publish(twice)["LayerVersionArn"]
    abroad = publish(uniq("conf-layer-abroad"), client=make_client("lambda", region="eu-west-1"))["LayerVersionArn"]
    unversioned = v1.rsplit(":", 1)[0]
    pattern = (
        r"(arn:(aws[a-zA-Z-]*)?:lambda:(eusc-)?[a-z]{2}((-gov)|(-iso([a-z]?)))?-[a-z]+-\d{1}:\d{12}:layer:[a-zA-Z0-9-_]+:[0-9]+)"
        r"|(arn:[a-zA-Z0-9-]+:lambda:::awslayer:[a-zA-Z0-9-_]+)"
    )
    cases = [
        (six, "InvalidParameterValueException", "Cannot reference more than 5 layers."),
        (
            [v1, v2],
            "InvalidParameterValueException",
            "Two different versions of the same layer are not allowed to be referenced in the same function. "
            f"{v1} and {v2} are versions of the same layer.",
        ),
        (
            [abroad],
            "InvalidParameterValueException",
            f"Layers are not in the same region as the function. Layers are expected to be in region {REGION}.",
        ),
        (
            [unversioned],
            "ValidationException",
            f"1 validation error detected: Value '[{unversioned}]' at 'layers' failed to satisfy constraint: Member must "
            "satisfy constraint: [Member must have length less than or equal to 2048, Member must have length greater "
            f"than or equal to 1, Member must satisfy regular expression pattern: {pattern}, Member must not be null]",
        ),
    ]
    for layers, code, message in cases:
        name = uniq("conf-layers-refused")
        cleanup.add(lambda_.delete_function, FunctionName=name)
        with raises(code) as exc:
            lambda_.create_function(
                FunctionName=name,
                Runtime=RUNTIME,
                Role=role[1],
                Handler=HANDLER,
                Code={"ZipFile": lambda_zip({"lambda_function.py": ECHO})},
                Layers=layers,
            )
        assert status_of(exc.value) == 400, layers
        assert exc.value.response["Error"]["Message"] == message


def test_a_function_keeps_a_deleted_layer_version(
    lambda_: Any, role: tuple[str, str], make_function: Callable[..., dict[str, Any]], publish: Callable[..., dict[str, Any]], cleanup: Any
) -> None:
    # 7.2: Layers [{Arn, CodeSize}] (REC L8631-L8636); a deleted version stays on the functions
    # that use it (finding 9, REC L8801-L8851) but no new function can use it ("you cannot
    # create a new function that uses a deleted layer version", adding-layers); a published
    # version keeps its layers (lambda-api.md 7.1); an empty list clears $LATEST's.
    layer = publish(uniq("conf-layer-kept"))
    arn = layer["LayerVersionArn"]
    created = make_function(Layers=[arn])
    name = created["FunctionName"]
    want = [{"Arn": arn, "CodeSize": layer["Content"]["CodeSize"]}]
    assert created["Layers"] == want

    lambda_.delete_layer_version(LayerName=layer["LayerArn"], VersionNumber=1)
    assert lambda_.get_function_configuration(FunctionName=name)["Layers"] == want
    version = lambda_.publish_version(FunctionName=name)["Version"]
    assert lambda_.get_function_configuration(FunctionName=name, Qualifier=version)["Layers"] == want

    other = uniq("conf-layers-fn")
    cleanup.add(lambda_.delete_function, FunctionName=other)
    with raises("InvalidParameterValueException") as exc:
        lambda_.create_function(
            FunctionName=other,
            Runtime=RUNTIME,
            Role=role[1],
            Handler=HANDLER,
            Code={"ZipFile": lambda_zip({"lambda_function.py": ECHO})},
            Layers=[arn],
        )
    assert exc.value.response["Error"]["Message"] == f"Layer version {arn} does not exist."

    lambda_.get_waiter("function_updated_v2").wait(FunctionName=name, WaiterConfig={"Delay": 1, "MaxAttempts": 60})
    cleared = lambda_.update_function_configuration(FunctionName=name, Layers=[])
    # Whether the cleared list is absent or [] is unrecorded (LDL-16); both mean no layers.
    assert not cleared.get("Layers")
    assert lambda_.get_function_configuration(FunctionName=name, Qualifier=version)["Layers"] == want


@pytest.mark.docker
def test_python_finds_a_layer_module_in_site_packages(lambda_: Any, make_function: Callable[..., dict[str, Any]], publish: Callable[..., dict[str, Any]]) -> None:
    # 8.1, 8.2: Lambda extracts the layer under /opt, and the Python runtime searches
    # /opt/python/lib/python3.x/site-packages (python-package, "Dependency search path").
    layer = publish(uniq("conf-layer-sp"), {"python/lib/python3.13/site-packages/sp_greet.py": "WHERE = __file__\n"})
    fn = make_function("import sp_greet\n\n\ndef handler(event, context):\n    return {'where': sp_greet.WHERE}\n", Layers=[layer["LayerVersionArn"]])
    out = lambda_.invoke(FunctionName=fn["FunctionName"], Payload=b"{}")
    assert "FunctionError" not in out
    assert json.loads(out["Payload"].read()) == {"where": "/opt/python/lib/python3.13/site-packages/sp_greet.py"}


# Asynchronous invocation destinations.


def test_event_invoke_config_refusals(lambda_: Any, make_function: Callable[..., dict[str, Any]], account: str) -> None:
    # lambda-api.md 7.7: the recorded refusals for no setting, the function itself, a missing
    # destination function and an ARN of another service (lambda-destinations-layers.md 2.2,
    # test_lambda_api.snapshot.json:L4342-L4353).
    fn = make_function()
    name, arn = fn["FunctionName"], fn["FunctionArn"]
    missing = f"arn:aws:lambda:{REGION}:{account}:function:{uniq('conf-missing')}"
    table = f"arn:aws:dynamodb:{REGION}:{account}:table/{uniq('conf-table')}"
    cases: list[tuple[dict[str, Any], str]] = [
        ({}, "You must specify at least one of error handling or destination setting."),
        ({"DestinationConfig": {"OnSuccess": {"Destination": arn}}}, "You can't specify the function as a destination for itself."),
        ({"DestinationConfig": {"OnFailure": {"Destination": missing}}}, f"The destination ARN {missing} is invalid."),
        (
            {"DestinationConfig": {"OnSuccess": {"Destination": table}}},
            f"The provided destination config DestinationConfig(onSuccess=OnSuccess(destination={table}), onFailure=null) is invalid.",
        ),
    ]
    for args, message in cases:
        with raises("InvalidParameterValueException") as exc:
            lambda_.put_function_event_invoke_config(FunctionName=name, **args)
        assert status_of(exc.value) == 400
        assert exc.value.response["Error"]["Message"] == message


def test_zero_reserved_concurrency_sends_a_record_without_a_response(
    lambda_: Any,
    sqs: Any,
    admin: Admin,
    make_function: Callable[..., dict[str, Any]],
    make_queue: Callable[..., str],
    queue_arn: Callable[[str], str],
    allow: Callable[[str, str], None],
) -> None:
    # 3.3 item 2, finding 5: with reserved concurrency 0 an event never runs, and the
    # on-failure record is ZeroReservedConcurrency with count 0 and no response half, in the
    # key order of the recorded bodies (3.1; test_lambda.snapshot.json:L2968-L2978).
    fn = make_function()
    queue = make_queue("conf-zero")
    allow("sqs:SendMessage", queue_arn(queue))
    lambda_.put_function_concurrency(FunctionName=fn["FunctionName"], ReservedConcurrentExecutions=0)
    lambda_.put_function_event_invoke_config(FunctionName=fn["FunctionName"], DestinationConfig={"OnFailure": {"Destination": queue_arn(queue)}})
    assert lambda_.invoke(FunctionName=fn["FunctionName"], InvocationType="Event", Payload=b'{"n": 2}')["StatusCode"] == 202

    [message] = received(sqs, admin, queue)
    record = json.loads(message["Body"])
    assert list(record) == ["version", "timestamp", "requestContext", "requestPayload"]
    assert list(record["requestContext"]) == ["requestId", "functionArn", "condition", "approximateInvokeCount"]
    assert record["version"] == "1.0" and RECORD_TIMESTAMP.fullmatch(record["timestamp"])
    ctx = record["requestContext"]
    assert ctx["functionArn"] == fn["FunctionArn"] + ":$LATEST"
    assert (ctx["condition"], ctx["approximateInvokeCount"]) == ("ZeroReservedConcurrency", 0)
    assert record["requestPayload"] == {"n": 2}


@pytest.mark.docker
def test_async_success_publishes_the_record_to_an_sns_destination(
    lambda_: Any,
    sns: Any,
    sqs: Any,
    admin: Admin,
    make_function: Callable[..., dict[str, Any]],
    make_queue: Callable[..., str],
    queue_arn: Callable[[str], str],
    allow: Callable[[str, str], None],
    cleanup: Any,
) -> None:
    # 4.2: the record is the published Message; 3.4: a success is condition Success, count 1,
    # statusCode 200 and the executed version with no functionError, and the function's
    # response as responsePayload (test_lambda_destinations.snapshot.json:L87-L114).
    topic = sns.create_topic(Name=uniq("conf-dest-topic"))["TopicArn"]
    cleanup.add(sns.delete_topic, TopicArn=topic)
    queue = make_queue("conf-dest-sub")
    allow_service_to_send(sqs, queue, queue_arn(queue), "sns.amazonaws.com", topic)
    sns.subscribe(TopicArn=topic, Protocol="sqs", Endpoint=queue_arn(queue), Attributes={"RawMessageDelivery": "true"})
    allow("sns:Publish", topic)
    fn = make_function()
    lambda_.put_function_event_invoke_config(FunctionName=fn["FunctionName"], DestinationConfig={"OnSuccess": {"Destination": topic}})
    assert lambda_.invoke(FunctionName=fn["FunctionName"], InvocationType="Event", Payload=b'{"n": 1}')["StatusCode"] == 202

    [message] = received(sqs, admin, queue)
    record = json.loads(message["Body"])
    assert record["version"] == "1.0" and RECORD_TIMESTAMP.fullmatch(record["timestamp"])
    ctx = record["requestContext"]
    assert ctx["functionArn"] == fn["FunctionArn"] + ":$LATEST"
    assert (ctx["condition"], ctx["approximateInvokeCount"]) == ("Success", 1)
    assert record["requestPayload"] == {"n": 1}
    assert record["responseContext"] == {"statusCode": 200, "executedVersion": "$LATEST"}
    assert record["responsePayload"] == {"echo": {"n": 1}}


@pytest.mark.docker
def test_async_failure_puts_the_record_on_an_event_bus(
    lambda_: Any,
    events: Any,
    sqs: Any,
    admin: Admin,
    account: str,
    make_function: Callable[..., dict[str, Any]],
    make_queue: Callable[..., str],
    queue_arn: Callable[[str], str],
    allow: Callable[[str, str], None],
    cleanup: Any,
) -> None:
    # 4.4: the record is the event's detail, source "lambda", detail-type "Lambda Function
    # Invocation Result - Failure", resources the bus and the qualified function (their order
    # is lambda.destination.eventbridge-resources-order, so it is not asserted here); 3.4: a
    # failure with MaximumRetryAttempts 0 is RetriesExhausted with count 1 and functionError
    # Unhandled (test_lambda_destinations.snapshot.json:L150-L175, L551-L586).
    bus = uniq("conf-dest-bus")
    bus_arn = events.create_event_bus(Name=bus)["EventBusArn"]
    cleanup.add(events.delete_event_bus, Name=bus)
    queue = make_queue("conf-dest-events")
    rule = uniq("conf-dest-rule")
    rule_arn = events.put_rule(Name=rule, EventBusName=bus, EventPattern=json.dumps({"source": ["lambda"]}))["RuleArn"]
    cleanup.add(events.delete_rule, Name=rule, EventBusName=bus)
    allow_service_to_send(sqs, queue, queue_arn(queue), "events.amazonaws.com", rule_arn)
    assert events.put_targets(Rule=rule, EventBusName=bus, Targets=[{"Id": "queue", "Arn": queue_arn(queue)}])["FailedEntryCount"] == 0
    cleanup.add(events.remove_targets, Rule=rule, EventBusName=bus, Ids=["queue"])
    allow("events:PutEvents", bus_arn)
    fn = make_function(FAILS)
    lambda_.put_function_event_invoke_config(
        FunctionName=fn["FunctionName"], MaximumRetryAttempts=0, DestinationConfig={"OnFailure": {"Destination": bus_arn}}
    )
    assert lambda_.invoke(FunctionName=fn["FunctionName"], InvocationType="Event", Payload=b'{"n": 3}')["StatusCode"] == 202

    [message] = received(sqs, admin, queue)
    event = json.loads(message["Body"])
    qualified = fn["FunctionArn"] + ":$LATEST"
    assert event["source"] == "lambda"
    assert event["detail-type"] == "Lambda Function Invocation Result - Failure"
    assert (event["account"], event["region"]) == (account, REGION)
    assert sorted(event["resources"]) == sorted([bus_arn, qualified])
    detail = event["detail"]
    ctx = detail["requestContext"]
    assert ctx["functionArn"] == qualified
    assert (ctx["condition"], ctx["approximateInvokeCount"]) == ("RetriesExhausted", 1)
    assert detail["requestPayload"] == {"n": 3}
    assert detail["responseContext"] == {"statusCode": 200, "executedVersion": "$LATEST", "functionError": "Unhandled"}
    assert detail["responsePayload"]["errorMessage"] == "Handler fails"
    assert detail["responsePayload"]["errorType"] == "Exception"
