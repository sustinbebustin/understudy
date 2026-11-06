"""API Gateway HTTP APIs (docs/research/apigateway-http.md): the control plane boto3 and
Terraform use, and Lambda proxy invocations through the execute-api path fallback.

API Gateway v2's error texts are UNVERIFIED (apigateway-http.md 2.3), so each error check is
its code and status. The invocation tests need the Docker daemon: functions run in AWS's
Lambda base images.
"""

from __future__ import annotations

import json
import re
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

# Answers an API Gateway Lambda proxy request with what it saw, in payload format 1.0 or 2.0
# (apigateway-http.md 5.1, 5.2), as a structured response both formats read (5.3, 5.4).
APIGW_HANDLER = """
import json


def handler(event, context):
    ctx = event.get("requestContext", {})
    method = event.get("httpMethod") or ctx.get("http", {}).get("method")
    return {
        "statusCode": 200,
        "headers": {"content-type": "application/json", "x-conformance": "yes"},
        "body": json.dumps({"version": event.get("version"), "method": method, "stage": ctx.get("stage")}),
    }
"""


@pytest.fixture(scope="session")
def apigw(make_client: Callable[..., Any]) -> Any:
    return make_client("apigatewayv2")


@pytest.fixture
def make_api(apigw: Any, cleanup: Any) -> Callable[..., dict[str, Any]]:
    def make(**args: Any) -> dict[str, Any]:
        created = apigw.create_api(Name=uniq("conf-api"), ProtocolType="HTTP", **args)
        cleanup.add(apigw.delete_api, ApiId=created["ApiId"])
        return created

    return make


def test_api_lifecycle(apigw: Any, make_api: Callable[..., dict[str, Any]]) -> None:
    # Create defaults (apigateway-http.md 3.1, create-api.rst L13-L19); ApiEndpoint names the
    # emulator, never amazonaws.com (11.3).
    created = make_api()
    api_id = created["ApiId"]
    assert created["ApiKeySelectionExpression"] == "$request.header.x-api-key"
    assert created["RouteSelectionExpression"] == "$request.method $request.path"
    assert created["ProtocolType"] == "HTTP"
    assert re.fullmatch(rf"https?://{api_id}\.execute-api\.{REGION}\.[^/]+", created["ApiEndpoint"]), created["ApiEndpoint"]
    assert "amazonaws.com" not in created["ApiEndpoint"]

    got = apigw.get_api(ApiId=api_id)
    assert got["Name"] == created["Name"] and got["ApiEndpoint"] == created["ApiEndpoint"]
    apigw.update_api(ApiId=api_id, Description="updated")
    assert apigw.get_api(ApiId=api_id)["Description"] == "updated"
    listed: list[str] = []
    args: dict[str, Any] = {"MaxResults": "50"}
    while True:
        page = apigw.get_apis(**args)
        listed += [a["ApiId"] for a in page["Items"]]
        if "NextToken" not in page:
            break
        args["NextToken"] = page["NextToken"]
    assert api_id in listed

    apigw.delete_api(ApiId=api_id)
    with raises("NotFoundException") as missing:
        apigw.get_api(ApiId=api_id)
    assert status_of(missing.value) == 404


def test_tags_take_the_escaped_arn(apigw: Any, make_api: Callable[..., dict[str, Any]]) -> None:
    # The ARN is a path label botocore percent-encodes (apigateway-http.md 2.2); create tags are
    # GetApi's (3.6).
    created = make_api(Tags={"team": "a"})
    arn = f"arn:aws:apigateway:{REGION}::/apis/{created['ApiId']}"
    apigw.tag_resource(ResourceArn=arn, Tags={"env": "dev"})
    assert apigw.get_tags(ResourceArn=arn)["Tags"] == {"team": "a", "env": "dev"}
    apigw.untag_resource(ResourceArn=arn, TagKeys=["team"])
    assert apigw.get_api(ApiId=created["ApiId"])["Tags"] == {"env": "dev"}


def test_routes_integrations_stages_and_deployments(apigw: Any, make_api: Callable[..., dict[str, Any]], sts: Any) -> None:
    api_id = make_api()["ApiId"]
    account = sts.get_caller_identity()["Account"]
    fn_arn = f"arn:aws:lambda:{REGION}:{account}:function:{uniq('conf-target')}"
    # Lambda proxy defaults (apigateway-http.md 3.3).
    integ = apigw.create_integration(ApiId=api_id, IntegrationType="AWS_PROXY", IntegrationUri=fn_arn, PayloadFormatVersion="2.0")
    assert integ["IntegrationMethod"] == "POST"
    assert integ["ConnectionType"] == "INTERNET"
    assert integ["TimeoutInMillis"] == 30000
    assert integ["IntegrationUri"] == fn_arn
    # Route defaults (apigateway-http.md 3.2).
    target = f"integrations/{integ['IntegrationId']}"
    route = apigw.create_route(ApiId=api_id, RouteKey="GET /pets/{id}", Target=target)
    assert route["ApiKeyRequired"] is False and route["AuthorizationType"] == "NONE" and route["Target"] == target

    with raises("ConflictException") as dup:
        apigw.create_route(ApiId=api_id, RouteKey="GET /pets/{id}")
    assert status_of(dup.value) == 409
    with raises("NotFoundException"):
        apigw.get_route(ApiId=api_id, RouteId="zzzzzzz")
    # TimeoutInMillis is 50..30000 (the model's @range, apigateway-http.md 2.4).
    with raises("BadRequestException") as bad:
        apigw.update_integration(ApiId=api_id, IntegrationId=integ["IntegrationId"], TimeoutInMillis=30001)
    assert status_of(bad.value) == 400

    # An automatic deployment (apigateway-http.md 3.4, 3.5).
    stage = apigw.create_stage(ApiId=api_id, StageName="$default", AutoDeploy=True)
    deployment = stage["DeploymentId"]
    assert stage["LastDeploymentStatusMessage"] == f"Successfully deployed stage with deployment ID '{deployment}'"
    items = apigw.get_deployments(ApiId=api_id)["Items"]
    assert [(d["DeploymentId"], d["AutoDeployed"], d["DeploymentStatus"]) for d in items] == [(deployment, True, "DEPLOYED")]

    apigw.create_stage(ApiId=api_id, StageName="prod")
    manual = apigw.create_deployment(ApiId=api_id, StageName="prod", Description="v1")
    assert manual["AutoDeployed"] is False and manual["DeploymentStatus"] == "DEPLOYED"
    assert apigw.get_stage(ApiId=api_id, StageName="prod")["DeploymentId"] == manual["DeploymentId"]


def test_websocket_apis_are_not_implemented(apigw: Any) -> None:
    # A valid request understudy does not emulate (the unit's conflict row 4).
    with raises("NotImplemented") as err:
        apigw.create_api(Name=uniq("conf-ws"), ProtocolType="WEBSOCKET", RouteSelectionExpression="$request.body.action")
    assert status_of(err.value) == 501


@pytest.mark.docker
@pytest.mark.parametrize("version", ["1.0", "2.0"])
def test_invokes_a_lambda_proxy_integration(
    version: str, endpoint: str, apigw: Any, lambda_: Any, iam: Any, sts: Any, make_api: Callable[..., dict[str, Any]], cleanup: Any
) -> None:
    # A Lambda proxy route of the $default stage, reached through the path fallback
    # /_understudy/execute-api/<api-id>/ (apigateway-http.md 11.3), with the permission
    # "${execution_arn}/*/*" for apigateway.amazonaws.com (5.5, 9.6).
    role = uniq("conf-apigw-role")
    role_arn = iam.create_role(RoleName=role, AssumeRolePolicyDocument=TRUST_LAMBDA)["Role"]["Arn"]
    cleanup.add(iam.delete_role, RoleName=role)
    fn = uniq("conf-apigw")
    fn_arn = lambda_.create_function(
        FunctionName=fn, Runtime="python3.13", Role=role_arn, Handler="lambda_function.handler",
        Code={"ZipFile": lambda_zip({"lambda_function.py": APIGW_HANDLER})},
    )["FunctionArn"]
    cleanup.add(lambda_.delete_function, FunctionName=fn)
    lambda_.get_waiter("function_active_v2").wait(FunctionName=fn, WaiterConfig={"Delay": 1, "MaxAttempts": 60})

    api_id = make_api()["ApiId"]
    integ = apigw.create_integration(ApiId=api_id, IntegrationType="AWS_PROXY", IntegrationUri=fn_arn, PayloadFormatVersion=version)
    apigw.create_route(ApiId=api_id, RouteKey="GET /hello", Target=f"integrations/{integ['IntegrationId']}")
    apigw.create_stage(ApiId=api_id, StageName="$default", AutoDeploy=True)
    account = sts.get_caller_identity()["Account"]
    lambda_.add_permission(
        FunctionName=fn, StatementId="apigateway", Action="lambda:InvokeFunction", Principal="apigateway.amazonaws.com",
        SourceArn=f"arn:aws:execute-api:{REGION}:{account}:{api_id}/*/*",
    )

    status, headers, body = http_request("GET", f"{endpoint}/_understudy/execute-api/{api_id}/hello")
    assert status == 200, body
    assert headers["x-conformance"] == "yes"
    assert headers["apigw-requestid"]
    assert json.loads(body) == {"version": version, "method": "GET", "stage": "$default"}
