// API Gateway REST APIs over restJson1, through @aws-sdk/client-api-gateway:
// the control plane the SDKs and Terraform use, MOCK methods and Lambda proxy
// invocations through the execute-api path fallback.
// Expected behavior: docs/research/apigateway-rest.md (sections 1 to 6). Error
// texts are asserted only where LocalStack's AWS-validated snapshots record
// them (apigateway-rest.md 2.4).
//
// Invocation of a function needs a Docker daemon: functions run in AWS's
// Lambda base images. CONFORMANCE_LAMBDA_EXECUTION=0 skips that test.

import { afterAll, describe, expect, test } from "bun:test";
import {
  APIGatewayClient,
  CreateDeploymentCommand,
  CreateResourceCommand,
  CreateRestApiCommand,
  DeleteDeploymentCommand,
  DeleteRestApiCommand,
  DeleteStageCommand,
  GetResourcesCommand,
  GetRestApiCommand,
  GetRestApisCommand,
  GetStagesCommand,
  GetTagsCommand,
  ImportRestApiCommand,
  PutIntegrationCommand,
  PutIntegrationResponseCommand,
  PutMethodCommand,
  PutMethodResponseCommand,
  TagResourceCommand,
  UntagResourceCommand,
  UpdateRestApiCommand,
  type CreateRestApiCommandInput,
} from "@aws-sdk/client-api-gateway";
import { AddPermissionCommand, CreateFunctionCommand, DeleteFunctionCommand, LambdaClient, waitUntilFunctionActiveV2 } from "@aws-sdk/client-lambda";
import { CreateRoleCommand, DeleteRoleCommand, IAMClient } from "@aws-sdk/client-iam";
import { clientConfig, defaultAccount, endpoint, lambdaExecutionEnabled, region } from "../support/env";
import { expectFailure, uniq } from "../support/helpers";
import { zip } from "../support/zip";

const apigw = new APIGatewayClient(clientConfig());
const lambda = new LambdaClient(clientConfig());
const iam = new IAMClient(clientConfig());

const cleanups: Array<() => Promise<unknown>> = [];

afterAll(async () => {
  for (const cleanup of cleanups.reverse()) await cleanup().catch(() => undefined);
});

/** Creates a REST API, deleted after the run, and returns its id and root resource id. */
async function makeApi(extra: Partial<CreateRestApiCommandInput> = {}) {
  const created = await apigw.send(new CreateRestApiCommand({ name: uniq("conf-rest"), ...extra }));
  const id = created.id;
  const root = created.rootResourceId;
  if (id === undefined || root === undefined) throw new Error("CreateRestApi returned no id or rootResourceId");
  cleanups.push(() => apigw.send(new DeleteRestApiCommand({ restApiId: id })));
  return { created, id, root };
}

/** Creates the path part under parent and returns its id. */
async function makeResource(restApiId: string, parentId: string, pathPart: string) {
  const out = await apigw.send(new CreateResourceCommand({ restApiId, parentId, pathPart }));
  if (out.id === undefined) throw new Error(`CreateResource ${pathPart} returned no id`);
  return out.id;
}

/** The path fallback /_understudy/execute-api/<api-id>/<stage>/<rest> (apigateway-rest.md 4.1). */
function restUrl(id: string, path: string) {
  return `${endpoint}/_understudy/execute-api/${id}${path}`;
}

describe("API Gateway REST API control plane", () => {
  test("a REST API is created with AWS's defaults, read, listed, updated and deleted", async () => {
    // Create defaults (apigateway-rest.md 2.3) and the root resource "/" with
    // no parentId (3.1).
    const { created, id, root } = await makeApi({ description: "first" });
    expect(created).toMatchObject({ apiKeySource: "HEADER", disableExecuteApiEndpoint: false });
    expect(created.endpointConfiguration?.types).toEqual(["EDGE"]);
    const resources = await apigw.send(new GetResourcesCommand({ restApiId: id }));
    expect(resources.items?.map((r) => [r.id, r.path, r.parentId])).toEqual([[root, "/", undefined]]);
    const updated = await apigw.send(
      new UpdateRestApiCommand({ restApiId: id, patchOperations: [{ op: "replace", path: "/description", value: "second" }] }),
    );
    expect(updated.description).toBe("second");
    const listed = await apigw.send(new GetRestApisCommand({ limit: 500 }));
    expect(listed.items?.filter((a) => a.id === id).map((a) => a.description)).toEqual(["second"]);

    // Ids are case-sensitive; an unknown one answers the recorded text
    // (apigateway-rest.md 2.4).
    if (id.toUpperCase() !== id) {
      await expectFailure(apigw.send(new GetRestApiCommand({ restApiId: id.toUpperCase() })), {
        name: "NotFoundException",
        status: 404,
        messageIncludes: `Invalid API identifier specified ${defaultAccount}:${id.toUpperCase()}`,
      });
    }
    await apigw.send(new DeleteRestApiCommand({ restApiId: id }));
    await expectFailure(apigw.send(new GetRestApiCommand({ restApiId: id })), {
      name: "NotFoundException",
      status: 404,
      messageIncludes: `Invalid API identifier specified ${defaultAccount}:${id}`,
    });
  });

  test("the control plane answers AWS's recorded refusals", async () => {
    // apigateway-rest.md 2.4.
    await expectFailure(apigw.send(new CreateRestApiCommand({ name: uniq("conf-rest"), description: "" })), {
      name: "BadRequestException",
      status: 400,
      messageIncludes: "Description cannot be an empty string",
    });
    const { id, root } = await makeApi();
    await makeResource(id, root, "pets");
    await expectFailure(apigw.send(new CreateResourceCommand({ restApiId: id, parentId: root, pathPart: "pets" })), {
      name: "ConflictException",
      status: 409,
      messageIncludes: "Another resource with the same parent already has this name: pets",
    });
    const greedy = await makeResource(id, root, "{proxy+}");
    await expectFailure(apigw.send(new CreateResourceCommand({ restApiId: id, parentId: greedy, pathPart: "child" })), {
      name: "BadRequestException",
      status: 400,
      messageIncludes: "Cannot create a child of a resource with a greedy path variable: {proxy+}",
    });
  });

  test("a MOCK method answers its literal template, and a live deployment cannot be deleted", async () => {
    const { id, root } = await makeApi();
    const items = await makeResource(id, root, "items");
    await apigw.send(new PutMethodCommand({ restApiId: id, resourceId: items, httpMethod: "GET", authorizationType: "NONE" }));
    await apigw.send(new PutMethodResponseCommand({ restApiId: id, resourceId: items, httpMethod: "GET", statusCode: "200" }));
    // MOCK defaults (apigateway-rest.md 2.3): no httpMethod, WHEN_NO_MATCH, 29000 ms.
    const integ = await apigw.send(
      new PutIntegrationCommand({
        restApiId: id,
        resourceId: items,
        httpMethod: "GET",
        type: "MOCK",
        requestTemplates: { "application/json": '{"statusCode": 200}' },
      }),
    );
    expect({ httpMethod: integ.httpMethod, passthrough: integ.passthroughBehavior, timeout: integ.timeoutInMillis }).toEqual({
      httpMethod: undefined,
      passthrough: "WHEN_NO_MATCH",
      timeout: 29000,
    });
    await apigw.send(
      new PutIntegrationResponseCommand({
        restApiId: id,
        resourceId: items,
        httpMethod: "GET",
        statusCode: "200",
        responseTemplates: { "application/json": '{"mock": true}' },
      }),
    );
    const deployment = (await apigw.send(new CreateDeploymentCommand({ restApiId: id, stageName: "s" }))).id;
    // Stage defaults (apigateway-rest.md 2.3).
    const stages = await apigw.send(new GetStagesCommand({ restApiId: id }));
    expect(stages.item?.map((s) => [s.stageName, s.deploymentId, s.cacheClusterStatus, s.tracingEnabled, s.methodSettings])).toEqual([
      ["s", deployment, "NOT_AVAILABLE", false, {}],
    ]);

    // A literal MOCK template selects the status and is the body (6.3, spec
    // 6b VTL decision).
    const ok = await fetch(restUrl(id, "/s/items"));
    expect({ status: ok.status, body: await ok.json() }).toEqual({ status: 200, body: { mock: true } });
    // An unknown path or method is 403 Missing Authentication Token (finding 3).
    for (const [method, path] of [["GET", "/s/nothing"], ["POST", "/s/items"]]) {
      const res = await fetch(restUrl(id, path), { method });
      expect({ status: res.status, errorType: res.headers.get("x-amzn-errortype"), body: await res.json() }).toEqual({
        status: 403,
        errorType: "MissingAuthenticationTokenException",
        body: { message: "Missing Authentication Token" },
      });
    }

    // A deployment a stage points at cannot be deleted (finding 12).
    await expectFailure(apigw.send(new DeleteDeploymentCommand({ restApiId: id, deploymentId: deployment })), {
      name: "BadRequestException",
      status: 400,
      messageIncludes: "Active stages pointing to this deployment must be moved or deleted",
    });
    await apigw.send(new DeleteStageCommand({ restApiId: id, stageName: "s" }));
    await apigw.send(new DeleteDeploymentCommand({ restApiId: id, deploymentId: deployment }));
  });

  test("tags take the REST API's ARN as an escaped path label", async () => {
    // apigateway-rest.md 3.9, 3.10.
    const { id } = await makeApi({ tags: { team: "a" } });
    const arn = `arn:aws:apigateway:${region}::/restapis/${id}`;
    await apigw.send(new TagResourceCommand({ resourceArn: arn, tags: { env: "dev" } }));
    expect((await apigw.send(new GetTagsCommand({ resourceArn: arn }))).tags).toEqual({ team: "a", env: "dev" });
    await apigw.send(new UntagResourceCommand({ resourceArn: arn, tagKeys: ["team"] }));
    expect((await apigw.send(new GetRestApiCommand({ restApiId: id }))).tags).toEqual({ env: "dev" });
  });

  test("ImportRestApi answers NotImplemented", async () => {
    // A valid request understudy does not emulate (the unit's conflict row 4).
    const body = new TextEncoder().encode('{"openapi":"3.0.1","info":{"title":"t","version":"1"},"paths":{}}');
    await expectFailure(apigw.send(new ImportRestApiCommand({ body })), { name: "NotImplemented", status: 501 });
  });
});

// Answers an API Gateway Lambda proxy request with what it saw in the REST 1.0
// event (apigateway-rest.md 5.1), as a proxy response (5.2).
const APIGW_HANDLER = `
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
`;

const TRUST_LAMBDA = JSON.stringify({
  Version: "2012-10-17",
  Statement: [{ Effect: "Allow", Principal: { Service: "lambda.amazonaws.com" }, Action: "sts:AssumeRole" }],
});

describe.skipIf(!lambdaExecutionEnabled)("API Gateway REST API invocations", () => {
  // An AWS_PROXY method on /items/{id} reaches a real function through the
  // path fallback, with the permission "${execution_arn}/*/*" for
  // apigateway.amazonaws.com (apigateway-rest.md 5.4); its proxy response is
  // the answer (5.2).
  test("a Lambda proxy method answers with the function's response", async () => {
    const role = uniq("conf-apigw1-role");
    const roleOut = await iam.send(new CreateRoleCommand({ RoleName: role, AssumeRolePolicyDocument: TRUST_LAMBDA }));
    cleanups.push(() => iam.send(new DeleteRoleCommand({ RoleName: role })));
    const fn = uniq("conf-apigw1");
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

    const { id, root } = await makeApi();
    const item = await makeResource(id, await makeResource(id, root, "items"), "{id}");
    await apigw.send(new PutMethodCommand({ restApiId: id, resourceId: item, httpMethod: "GET", authorizationType: "NONE" }));
    await apigw.send(
      new PutIntegrationCommand({
        restApiId: id,
        resourceId: item,
        httpMethod: "GET",
        type: "AWS_PROXY",
        integrationHttpMethod: "POST",
        uri: `arn:aws:apigateway:${region}:lambda:path/2015-03-31/functions/${created.FunctionArn}/invocations`,
      }),
    );
    await lambda.send(
      new AddPermissionCommand({
        FunctionName: fn,
        StatementId: "apigateway",
        Action: "lambda:InvokeFunction",
        Principal: "apigateway.amazonaws.com",
        SourceArn: `arn:aws:execute-api:${region}:${defaultAccount}:${id}/*/*`,
      }),
    );
    await apigw.send(new CreateDeploymentCommand({ restApiId: id, stageName: "live" }));

    const res = await fetch(restUrl(id, "/live/items/42"));
    const text = await res.text();
    expect({ status: res.status, conformance: res.headers.get("x-conformance") }).toEqual({ status: 201, conformance: "yes" });
    expect(res.headers.get("x-amzn-requestid")).toBeTruthy();
    expect(JSON.parse(text)).toEqual({ resource: "/items/{id}", path: "/items/42", method: "GET", stage: "live", id: "42" });
  });
});
