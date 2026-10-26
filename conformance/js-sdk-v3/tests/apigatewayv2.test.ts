// API Gateway HTTP APIs over restJson1, through @aws-sdk/client-apigatewayv2:
// the control plane the SDKs and Terraform use, and Lambda proxy invocations
// through the execute-api path fallback.
// Expected behavior: docs/research/apigateway-http.md (sections 2 to 5, 9 and
// 11). API Gateway v2's error texts are UNVERIFIED (apigateway-http.md 2.3), so
// each error check is its name and status.
//
// Invocation needs a Docker daemon: functions run in AWS's Lambda base images.
// CONFORMANCE_LAMBDA_EXECUTION=0 skips those tests.

import { afterAll, describe, expect, test } from "bun:test";
import {
  ApiGatewayV2Client,
  CreateApiCommand,
  CreateDeploymentCommand,
  CreateIntegrationCommand,
  CreateRouteCommand,
  CreateStageCommand,
  DeleteApiCommand,
  GetApiCommand,
  GetApisCommand,
  GetDeploymentsCommand,
  GetRouteCommand,
  GetStageCommand,
  GetTagsCommand,
  TagResourceCommand,
  UntagResourceCommand,
  UpdateApiCommand,
  UpdateIntegrationCommand,
  type CreateApiCommandInput,
} from "@aws-sdk/client-apigatewayv2";
import { AddPermissionCommand, CreateFunctionCommand, DeleteFunctionCommand, LambdaClient, waitUntilFunctionActiveV2 } from "@aws-sdk/client-lambda";
import { CreateRoleCommand, DeleteRoleCommand, IAMClient } from "@aws-sdk/client-iam";
import { clientConfig, defaultAccount, endpoint, lambdaExecutionEnabled, region } from "../support/env";
import { expectFailure, uniq } from "../support/helpers";
import { zip } from "../support/zip";

const apigw = new ApiGatewayV2Client(clientConfig());
const lambda = new LambdaClient(clientConfig());
const iam = new IAMClient(clientConfig());

const cleanups: Array<() => Promise<unknown>> = [];

afterAll(async () => {
  for (const cleanup of cleanups.reverse()) await cleanup().catch(() => undefined);
});

/** Creates an HTTP API, deleted after the run, and returns its id. */
async function makeApi(extra: Partial<CreateApiCommandInput> = {}) {
  const created = await apigw.send(new CreateApiCommand({ Name: uniq("conf-api"), ProtocolType: "HTTP", ...extra }));
  const id = created.ApiId;
  if (id === undefined) throw new Error("CreateApi returned no ApiId");
  cleanups.push(() => apigw.send(new DeleteApiCommand({ ApiId: id })));
  return { created, id };
}

describe("API Gateway HTTP API control plane", () => {
  test("an API is created with AWS's defaults, read, listed, updated and deleted", async () => {
    // Create defaults (apigateway-http.md 3.1, create-api.rst L13-L19); the
    // endpoint names the emulator, never amazonaws.com (11.3).
    const { created, id } = await makeApi();
    expect(created).toMatchObject({
      ApiKeySelectionExpression: "$request.header.x-api-key",
      RouteSelectionExpression: "$request.method $request.path",
      ProtocolType: "HTTP",
    });
    expect(created.ApiEndpoint).toMatch(new RegExp(`^https?://${id}\\.execute-api\\.${region}\\.[^/]+$`));
    expect(created.ApiEndpoint).not.toContain("amazonaws.com");
    const read = await apigw.send(new GetApiCommand({ ApiId: id }));
    expect(read).toMatchObject({ ApiId: id, Name: created.Name, ApiEndpoint: created.ApiEndpoint });
    await apigw.send(new UpdateApiCommand({ ApiId: id, Description: "updated" }));
    const ids: string[] = [];
    let token: string | undefined;
    do {
      const page = await apigw.send(new GetApisCommand({ MaxResults: "50", NextToken: token }));
      for (const api of page.Items ?? []) if (api.ApiId !== undefined) ids.push(api.ApiId);
      token = page.NextToken;
    } while (token !== undefined);
    expect(ids).toContain(id);
    await apigw.send(new DeleteApiCommand({ ApiId: id }));
    await expectFailure(apigw.send(new GetApiCommand({ ApiId: id })), { name: "NotFoundException", status: 404 });
  });

  test("tags take the API's ARN as an escaped path label", async () => {
    // apigateway-http.md 2.2 (the label), 3.6 (create tags are GetApi's).
    const { id } = await makeApi({ Tags: { team: "a" } });
    const arn = `arn:aws:apigateway:${region}::/apis/${id}`;
    await apigw.send(new TagResourceCommand({ ResourceArn: arn, Tags: { env: "dev" } }));
    expect((await apigw.send(new GetTagsCommand({ ResourceArn: arn }))).Tags).toEqual({ team: "a", env: "dev" });
    await apigw.send(new UntagResourceCommand({ ResourceArn: arn, TagKeys: ["team"] }));
    expect((await apigw.send(new GetApiCommand({ ApiId: id }))).Tags).toEqual({ env: "dev" });
  });

  test("routes, integrations, stages and deployments answer AWS's defaults and errors", async () => {
    const { id } = await makeApi();
    const fnArn = `arn:aws:lambda:${region}:${defaultAccount}:function:${uniq("conf-target")}`;
    // Lambda proxy defaults (apigateway-http.md 3.3).
    const integ = await apigw.send(
      new CreateIntegrationCommand({ ApiId: id, IntegrationType: "AWS_PROXY", IntegrationUri: fnArn, PayloadFormatVersion: "2.0" }),
    );
    expect(integ).toMatchObject({ IntegrationMethod: "POST", ConnectionType: "INTERNET", TimeoutInMillis: 30000, IntegrationUri: fnArn });
    // Route defaults (apigateway-http.md 3.2).
    const target = `integrations/${integ.IntegrationId}`;
    const route = await apigw.send(new CreateRouteCommand({ ApiId: id, RouteKey: "GET /pets/{id}", Target: target }));
    expect(route).toMatchObject({ ApiKeyRequired: false, AuthorizationType: "NONE", Target: target });

    await expectFailure(apigw.send(new CreateRouteCommand({ ApiId: id, RouteKey: "GET /pets/{id}" })), { name: "ConflictException", status: 409 });
    await expectFailure(apigw.send(new GetRouteCommand({ ApiId: id, RouteId: "zzzzzzz" })), { name: "NotFoundException", status: 404 });
    // TimeoutInMillis is 50..30000 (the model's @range, apigateway-http.md 2.4).
    await expectFailure(apigw.send(new UpdateIntegrationCommand({ ApiId: id, IntegrationId: integ.IntegrationId, TimeoutInMillis: 30001 })), {
      name: "BadRequestException",
      status: 400,
    });

    // An automatic deployment (apigateway-http.md 3.4, 3.5).
    const stage = await apigw.send(new CreateStageCommand({ ApiId: id, StageName: "$default", AutoDeploy: true }));
    expect(stage.DeploymentId).toBeDefined();
    expect(stage.LastDeploymentStatusMessage).toBe(`Successfully deployed stage with deployment ID '${stage.DeploymentId}'`);
    const deployments = await apigw.send(new GetDeploymentsCommand({ ApiId: id }));
    expect(deployments.Items?.map((d) => [d.DeploymentId, d.AutoDeployed, d.DeploymentStatus])).toEqual([[stage.DeploymentId, true, "DEPLOYED"]]);

    await apigw.send(new CreateStageCommand({ ApiId: id, StageName: "prod" }));
    const manual = await apigw.send(new CreateDeploymentCommand({ ApiId: id, StageName: "prod", Description: "v1" }));
    expect(manual).toMatchObject({ AutoDeployed: false, DeploymentStatus: "DEPLOYED" });
    expect((await apigw.send(new GetStageCommand({ ApiId: id, StageName: "prod" }))).DeploymentId).toBe(manual.DeploymentId);
  });

  test("a WebSocket API answers NotImplemented", async () => {
    // A valid request understudy does not emulate (the unit's conflict row 4).
    await expectFailure(
      apigw.send(new CreateApiCommand({ Name: uniq("conf-ws"), ProtocolType: "WEBSOCKET", RouteSelectionExpression: "$request.body.action" })),
      { name: "NotImplemented", status: 501 },
    );
  });
});

// Answers an API Gateway Lambda proxy request with what it saw, in payload
// format 1.0 or 2.0 (apigateway-http.md 5.1, 5.2), as a structured response
// both formats read (5.3, 5.4).
const APIGW_HANDLER = `
import json


def handler(event, context):
    ctx = event.get("requestContext", {})
    method = event.get("httpMethod") or ctx.get("http", {}).get("method")
    return {
        "statusCode": 200,
        "headers": {"content-type": "application/json", "x-conformance": "yes"},
        "body": json.dumps({"version": event.get("version"), "method": method, "stage": ctx.get("stage")}),
    }
`;

const TRUST_LAMBDA = JSON.stringify({
  Version: "2012-10-17",
  Statement: [{ Effect: "Allow", Principal: { Service: "lambda.amazonaws.com" }, Action: "sts:AssumeRole" }],
});

describe.skipIf(!lambdaExecutionEnabled)("API Gateway HTTP API invocations", () => {
  // A Lambda proxy route of the $default stage, reached through the path
  // fallback /_understudy/execute-api/<api-id>/ (apigateway-http.md 11.3),
  // with the permission "${execution_arn}/*/*" for apigateway.amazonaws.com
  // (5.5, 9.6).
  for (const version of ["1.0", "2.0"]) {
    test(`a payload ${version} Lambda proxy integration answers with the function's response`, async () => {
      const role = uniq("conf-apigw-role");
      const roleOut = await iam.send(new CreateRoleCommand({ RoleName: role, AssumeRolePolicyDocument: TRUST_LAMBDA }));
      cleanups.push(() => iam.send(new DeleteRoleCommand({ RoleName: role })));
      const fn = uniq("conf-apigw");
      const created = await lambda.send(
        new CreateFunctionCommand({
          FunctionName: fn,
          Runtime: "python3.13",
          Role: roleOut.Role?.Arn,
          Handler: "lambda_function.handler",
          Code: { ZipFile: zip({ "lambda_function.py": APIGW_HANDLER }) },
        }),
      );
      cleanups.push(() => lambda.send(new DeleteFunctionCommand({ FunctionName: fn })));
      await waitUntilFunctionActiveV2({ client: lambda, maxWaitTime: 120, minDelay: 1, maxDelay: 2 }, { FunctionName: fn });

      const { id } = await makeApi();
      const integ = await apigw.send(
        new CreateIntegrationCommand({ ApiId: id, IntegrationType: "AWS_PROXY", IntegrationUri: created.FunctionArn, PayloadFormatVersion: version }),
      );
      await apigw.send(new CreateRouteCommand({ ApiId: id, RouteKey: "GET /hello", Target: `integrations/${integ.IntegrationId}` }));
      await apigw.send(new CreateStageCommand({ ApiId: id, StageName: "$default", AutoDeploy: true }));
      await lambda.send(
        new AddPermissionCommand({
          FunctionName: fn,
          StatementId: "apigateway",
          Action: "lambda:InvokeFunction",
          Principal: "apigateway.amazonaws.com",
          SourceArn: `arn:aws:execute-api:${region}:${defaultAccount}:${id}/*/*`,
        }),
      );

      const res = await fetch(`${endpoint}/_understudy/execute-api/${id}/hello`);
      const text = await res.text();
      expect({ status: res.status, conformance: res.headers.get("x-conformance") }).toEqual({ status: 200, conformance: "yes" });
      expect(res.headers.get("apigw-requestid")).toBeTruthy();
      expect(JSON.parse(text)).toEqual({ version, method: "GET", stage: "$default" });
    });
  }
});
