"""API Gateway REST APIs (docs/research/apigateway-rest.md): the control plane boto3 and
Terraform use, MOCK methods and Lambda proxy invocations through the execute-api path fallback.

Error texts are asserted only where LocalStack's AWS-validated snapshots record them
(apigateway-rest.md 2.4). The AWS_PROXY test needs the Docker daemon: functions run in AWS's
Lambda base images.
"""

from __future__ import annotations

import json
from collections.abc import Callable
from typing import Any

import pytest

from support import REGION, http_request, lambda_zip, raises, status_of, uniq

TRUST_LAMBDA = json.dumps(
    {
        "Version": "2012-10-17",
        "Statement": [{"Effect": "Allow", "Principal": {"Service": "lambda.amazonaws.com"}, "Action": "sts:AssumeRole"}],
    }
)

# Answers an API Gateway Lambda proxy request with what it saw in the REST 1.0 event
# (apigateway-rest.md 5.1), as a proxy response (5.2).
APIGW_HANDLER = """
import json


def handler(event, context):
    ctx = event["requestContext"]
    return {
        "statusCode": 201,
        "headers": {"content-type": "application/json", "x-conformance": "yes"},
        "body": json.dumps({
            "resource": event["resource"],
            "path": event["path"],
            "method": event["httpMethod"],
            "stage": ctx["stage"],
            "id": event["pathParameters"]["id"],
        }),
    }
"""


@pytest.fixture(scope="session")
def apigw(make_client: Callable[..., Any]) -> Any:
    return make_client("apigateway")


@pytest.fixture
def make_api(apigw: Any, cleanup: Any) -> Callable[..., dict[str, Any]]:
    def make(**args: Any) -> dict[str, Any]:
        created = apigw.create_rest_api(name=uniq("conf-rest"), **args)
        cleanup.add(apigw.delete_rest_api, restApiId=created["id"])
        return created

    return make


def rest_url(endpoint: str, api_id: str, path: str) -> str:
    # The path fallback /_understudy/execute-api/<api-id>/<stage>/<rest> (apigateway-rest.md 4.1).
    return f"{endpoint}/_understudy/execute-api/{api_id}{path}"


def test_rest_api_lifecycle(apigw: Any, make_api: Callable[..., dict[str, Any]], sts: Any) -> None:
    # Create defaults (apigateway-rest.md 2.3) and the root resource "/" with no parentId (3.1).
    created = make_api(description="first")
    api_id = created["id"]
    assert created["apiKeySource"] == "HEADER"
    assert created["disableExecuteApiEndpoint"] is False
    assert created["endpointConfiguration"]["types"] == ["EDGE"]
    resources = apigw.get_resources(restApiId=api_id)["items"]
    assert [(r["id"], r["path"], r.get("parentId")) for r in resources] == [(created["rootResourceId"], "/", None)]

    updated = apigw.update_rest_api(restApiId=api_id, patchOperations=[{"op": "replace", "path": "/description", "value": "second"}])
    assert updated["description"] == "second"
    assert any(a["id"] == api_id and a["description"] == "second" for a in apigw.get_rest_apis(limit=500)["items"])

    # Ids are case-sensitive; an unknown one answers the recorded text (apigateway-rest.md 2.4).
    account = sts.get_caller_identity()["Account"]
    if api_id.upper() != api_id:
        with raises("NotFoundException") as upper:
            apigw.get_rest_api(restApiId=api_id.upper())
        assert status_of(upper.value) == 404
        assert upper.value.response["Error"]["Message"] == f"Invalid API identifier specified {account}:{api_id.upper()}"
    apigw.delete_rest_api(restApiId=api_id)
    with raises("NotFoundException") as missing:
        apigw.get_rest_api(restApiId=api_id)
    assert missing.value.response["Error"]["Message"] == f"Invalid API identifier specified {account}:{api_id}"


def test_control_plane_refusals(apigw: Any, make_api: Callable[..., dict[str, Any]]) -> None:
    # The recorded refusals (apigateway-rest.md 2.4).
    with raises("BadRequestException") as empty:
        apigw.create_rest_api(name=uniq("conf-rest"), description="")
    assert empty.value.response["Error"]["Message"] == "Description cannot be an empty string"
    created = make_api()
    api_id, root = created["id"], created["rootResourceId"]
    apigw.create_resource(restApiId=api_id, parentId=root, pathPart="pets")
    with raises("ConflictException") as dup:
        apigw.create_resource(restApiId=api_id, parentId=root, pathPart="pets")
    assert status_of(dup.value) == 409
    assert dup.value.response["Error"]["Message"] == "Another resource with the same parent already has this name: pets"
    greedy = apigw.create_resource(restApiId=api_id, parentId=root, pathPart="{proxy+}")["id"]
    with raises("BadRequestException") as child:
        apigw.create_resource(restApiId=api_id, parentId=greedy, pathPart="child")
    assert child.value.response["Error"]["Message"] == "Cannot create a child of a resource with a greedy path variable: {proxy+}"
    apigw.put_method(restApiId=api_id, resourceId=root, httpMethod="GET", authorizationType="NONE")
    with raises("ValidationException") as wrong:
        apigw.put_integration(restApiId=api_id, resourceId=root, httpMethod="GET", type="HTTPS_PROXY")
    assert status_of(wrong.value) == 400


def test_mock_method_and_deployments(endpoint: str, apigw: Any, make_api: Callable[..., dict[str, Any]]) -> None:
    created = make_api()
    api_id = created["id"]
    items = apigw.create_resource(restApiId=api_id, parentId=created["rootResourceId"], pathPart="items")["id"]
    apigw.put_method(restApiId=api_id, resourceId=items, httpMethod="GET", authorizationType="NONE")
    apigw.put_method_response(restApiId=api_id, resourceId=items, httpMethod="GET", statusCode="200")
    # MOCK defaults (apigateway-rest.md 2.3): no httpMethod, WHEN_NO_MATCH, 29000 ms.
    integ = apigw.put_integration(
        restApiId=api_id, resourceId=items, httpMethod="GET", type="MOCK",
        requestTemplates={"application/json": '{"statusCode": 200}'},
    )
    assert "httpMethod" not in integ
    assert integ["passthroughBehavior"] == "WHEN_NO_MATCH" and integ["timeoutInMillis"] == 29000
    apigw.put_integration_response(
        restApiId=api_id, resourceId=items, httpMethod="GET", statusCode="200",
        responseTemplates={"application/json": '{"mock": true}'},
    )
    deployment = apigw.create_deployment(restApiId=api_id, stageName="s")["id"]
    # Stage defaults (apigateway-rest.md 2.3).
    stages = apigw.get_stages(restApiId=api_id)["item"]
    assert [(s["stageName"], s["deploymentId"], s["cacheClusterStatus"], s["tracingEnabled"], s["methodSettings"]) for s in stages] == [
        ("s", deployment, "NOT_AVAILABLE", False, {})
    ]

    # A literal MOCK template selects the status and is the body (6.3).
    status, _, body = http_request("GET", rest_url(endpoint, api_id, "/s/items"))
    assert status == 200, body
    assert json.loads(body) == {"mock": True}
    # An unknown path or method is 403 Missing Authentication Token (finding 3).
    for method, path in [("GET", "/s/nothing"), ("POST", "/s/items")]:
        status, headers, body = http_request(method, rest_url(endpoint, api_id, path))
        assert status == 403, (method, path, body)
        assert json.loads(body) == {"message": "Missing Authentication Token"}
        assert headers["x-amzn-errortype"] == "MissingAuthenticationTokenException"

    # A deployment a stage points at cannot be deleted (finding 12).
    with raises("BadRequestException") as live:
        apigw.delete_deployment(restApiId=api_id, deploymentId=deployment)
    assert live.value.response["Error"]["Message"] == "Active stages pointing to this deployment must be moved or deleted"
    apigw.delete_stage(restApiId=api_id, stageName="s")
    apigw.delete_deployment(restApiId=api_id, deploymentId=deployment)


def test_tags_take_the_escaped_arn(apigw: Any, make_api: Callable[..., dict[str, Any]]) -> None:
    # The ARN is a path label botocore percent-encodes (apigateway-rest.md 3.9, 3.10).
    created = make_api(tags={"team": "a"})
    arn = f"arn:aws:apigateway:{REGION}::/restapis/{created['id']}"
    apigw.tag_resource(resourceArn=arn, tags={"env": "dev"})
    assert apigw.get_tags(resourceArn=arn)["tags"] == {"team": "a", "env": "dev"}
    apigw.untag_resource(resourceArn=arn, tagKeys=["team"])
    assert apigw.get_rest_api(restApiId=created["id"])["tags"] == {"env": "dev"}


def test_import_rest_api_is_not_implemented(apigw: Any) -> None:
    # A valid request understudy does not emulate.
    with raises("NotImplemented") as err:
        apigw.import_rest_api(body=b'{"openapi":"3.0.1","info":{"title":"t","version":"1"},"paths":{}}')
    assert status_of(err.value) == 501


@pytest.mark.docker
def test_invokes_a_lambda_proxy_method(
    endpoint: str, apigw: Any, lambda_: Any, iam: Any, sts: Any, make_api: Callable[..., dict[str, Any]], cleanup: Any
) -> None:
    # An AWS_PROXY method on /items/{id} reaches a real function through the path fallback, with
    # the permission "${execution_arn}/*/*" for apigateway.amazonaws.com (apigateway-rest.md 5.4);
    # its proxy response is the answer (5.2).
    role = uniq("conf-apigw1-role")
    role_arn = iam.create_role(RoleName=role, AssumeRolePolicyDocument=TRUST_LAMBDA)["Role"]["Arn"]
    cleanup.add(iam.delete_role, RoleName=role)
    fn = uniq("conf-apigw1")
    fn_arn = lambda_.create_function(
        FunctionName=fn, Runtime="python3.13", Role=role_arn, Handler="lambda_function.handler",
        Code={"ZipFile": lambda_zip({"lambda_function.py": APIGW_HANDLER})},
    )["FunctionArn"]
    cleanup.add(lambda_.delete_function, FunctionName=fn)
    lambda_.get_waiter("function_active_v2").wait(FunctionName=fn, WaiterConfig={"Delay": 1, "MaxAttempts": 60})

    created = make_api()
    api_id = created["id"]
    items = apigw.create_resource(restApiId=api_id, parentId=created["rootResourceId"], pathPart="items")["id"]
    item = apigw.create_resource(restApiId=api_id, parentId=items, pathPart="{id}")["id"]
    apigw.put_method(restApiId=api_id, resourceId=item, httpMethod="GET", authorizationType="NONE")
    apigw.put_integration(
        restApiId=api_id, resourceId=item, httpMethod="GET", type="AWS_PROXY", integrationHttpMethod="POST",
        uri=f"arn:aws:apigateway:{REGION}:lambda:path/2015-03-31/functions/{fn_arn}/invocations",
    )
    account = sts.get_caller_identity()["Account"]
    lambda_.add_permission(
        FunctionName=fn, StatementId="apigateway", Action="lambda:InvokeFunction", Principal="apigateway.amazonaws.com",
        SourceArn=f"arn:aws:execute-api:{REGION}:{account}:{api_id}/*/*",
    )
    apigw.create_deployment(restApiId=api_id, stageName="live")

    status, headers, body = http_request("GET", rest_url(endpoint, api_id, "/live/items/42"))
    assert status == 201, body
    assert headers["x-conformance"] == "yes"
    assert headers["x-amzn-requestid"]
    assert json.loads(body) == {"resource": "/items/{id}", "path": "/items/42", "method": "GET", "stage": "live", "id": "42"}
