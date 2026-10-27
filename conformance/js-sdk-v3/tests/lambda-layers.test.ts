// Lambda layers and asynchronous invocation destinations over restJson1,
// through @aws-sdk/client-lambda.
// Expected behavior: docs/research/lambda-destinations-layers.md (sections 2
// to 7 and the LocalStack snapshots they cite) and lambda-api.md 7.7.
//
// The tests that create functions wait for them to become Active and run them
// in AWS's Python base image, so they need Docker; CONFORMANCE_LAMBDA_EXECUTION=0
// skips them.

import { afterAll, describe, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import {
  AddLayerVersionPermissionCommand,
  CreateFunctionCommand,
  DeleteFunctionCommand,
  DeleteLayerVersionCommand,
  GetLayerVersionByArnCommand,
  GetLayerVersionCommand,
  GetLayerVersionPolicyCommand,
  InvokeCommand,
  LambdaClient,
  ListLayersCommand,
  ListLayerVersionsCommand,
  PublishLayerVersionCommand,
  PutFunctionEventInvokeConfigCommand,
  UpdateFunctionConfigurationCommand,
  waitUntilFunctionActiveV2,
} from "@aws-sdk/client-lambda";
import { CreateRoleCommand, DeleteRoleCommand, IAMClient } from "@aws-sdk/client-iam";
import { CreateQueueCommand, DeleteQueueCommand, GetQueueAttributesCommand, ReceiveMessageCommand, SQSClient } from "@aws-sdk/client-sqs";
import { clientConfig, defaultAccount, lambdaExecutionEnabled, region } from "../support/env";
import { eventually, expectFailure, uniq } from "../support/helpers";
import { zip } from "../support/zip";

const lambda = new LambdaClient(clientConfig());
const iam = new IAMClient(clientConfig());
const sqs = new SQSClient(clientConfig());

const TRUST_LAMBDA = JSON.stringify({
  Version: "2012-10-17",
  Statement: [{ Effect: "Allow", Principal: { Service: "lambda.amazonaws.com" }, Action: "sts:AssumeRole" }],
});

const GREET_LAYER = { "python/greet.py": "def hello():\n    return 'hello from the layer'\n" };

const cleanups: Array<() => Promise<unknown>> = [];

afterAll(async () => {
  for (const cleanup of cleanups.reverse()) await cleanup().catch(() => undefined);
});

/** Publishes files as a version of layerName and deletes it after the run. */
async function publish(layerName: string, files: Record<string, string> = GREET_LAYER) {
  const out = await lambda.send(new PublishLayerVersionCommand({ LayerName: layerName, Content: { ZipFile: zip(files) } }));
  const version = out.Version ?? 0;
  cleanups.push(() => lambda.send(new DeleteLayerVersionCommand({ LayerName: layerName, VersionNumber: version })));
  return out;
}

async function makeRole(): Promise<string> {
  const name = uniq("conf-layers-role");
  const out = await iam.send(new CreateRoleCommand({ RoleName: name, AssumeRolePolicyDocument: TRUST_LAMBDA }));
  cleanups.push(() => iam.send(new DeleteRoleCommand({ RoleName: name })));
  const arn = out.Role?.Arn;
  if (arn === undefined) throw new Error("CreateRole returned no Arn");
  return arn;
}

/** Creates a python3.13 function running code with the layers given and waits until it is Active. */
async function makeFunction(code: string, layers: string[] = []) {
  const name = uniq("conf-layers-fn");
  const created = await lambda.send(
    new CreateFunctionCommand({
      FunctionName: name,
      Runtime: "python3.13",
      Role: await makeRole(),
      Handler: "lambda_function.handler",
      Code: { ZipFile: zip({ "lambda_function.py": code }) },
      Layers: layers.length === 0 ? undefined : layers,
    }),
  );
  cleanups.push(() => lambda.send(new DeleteFunctionCommand({ FunctionName: name })));
  await waitUntilFunctionActiveV2({ client: lambda, maxWaitTime: 120, minDelay: 1, maxDelay: 2 }, { FunctionName: name });
  return { ...created, FunctionName: name, FunctionArn: created.FunctionArn ?? "" };
}

async function makeQueue(): Promise<{ url: string; arn: string }> {
  const out = await sqs.send(new CreateQueueCommand({ QueueName: uniq("conf-dest") }));
  const url = out.QueueUrl ?? "";
  cleanups.push(() => sqs.send(new DeleteQueueCommand({ QueueUrl: url })));
  const attrs = await sqs.send(new GetQueueAttributesCommand({ QueueUrl: url, AttributeNames: ["QueueArn"] }));
  return { url, arn: attrs.Attributes?.QueueArn ?? "" };
}

describe("Lambda layers", () => {
  // 6.3: version 1, the archive's base64 SHA-256 and size, Description "" and
  // no LicenseInfo when not sent (test_lambda_api.snapshot.json:L8271-L8292);
  // 6.5: newest first (L8761-L8794); 6.6: a delete is idempotent and a deleted
  // version is not found (L8356-L8413); finding 9: numbers are never reused.
  test("a layer's versions publish, list newest first, delete and are never reused", async () => {
    const layerName = uniq("conf-layer");
    const files = GREET_LAYER;
    const first = await publish(layerName, files);
    const layerArn = `arn:aws:lambda:${region}:${defaultAccount}:layer:${layerName}`;
    const archive = zip(files);
    expect(first).toMatchObject({ Version: 1, LayerArn: layerArn, LayerVersionArn: `${layerArn}:1`, Description: "" });
    expect(first.LicenseInfo).toBeUndefined();
    expect(first.Content?.CodeSha256).toBe(createHash("sha256").update(archive).digest("base64"));
    expect(first.Content?.CodeSize).toBe(archive.length);
    expect(first.CreatedDate).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}\+0000$/);

    const byName = await lambda.send(new GetLayerVersionCommand({ LayerName: layerName, VersionNumber: 1 }));
    const byArn = await lambda.send(new GetLayerVersionByArnCommand({ Arn: `${layerArn}:1` }));
    for (const got of [byName, byArn]) {
      expect(got).toMatchObject({ Version: 1, LayerVersionArn: `${layerArn}:1`, Content: { CodeSha256: first.Content?.CodeSha256 } });
    }

    await publish(layerName, files);
    const versions = await lambda.send(new ListLayerVersionsCommand({ LayerName: layerName }));
    expect((versions.LayerVersions ?? []).map((v) => v.Version)).toEqual([2, 1]);
    const layers = await lambda.send(new ListLayersCommand({}));
    expect((layers.Layers ?? []).filter((l) => l.LayerName === layerName).map((l) => l.LatestMatchingVersion?.Version)).toEqual([2]);

    for (let i = 0; i < 2; i++) await lambda.send(new DeleteLayerVersionCommand({ LayerName: layerName, VersionNumber: 1 }));
    await expectFailure(lambda.send(new GetLayerVersionCommand({ LayerName: layerName, VersionNumber: 1 })), {
      name: "ResourceNotFoundException",
      status: 404,
      messageIncludes: "The resource you requested does not exist.",
    });
    await lambda.send(new DeleteLayerVersionCommand({ LayerName: layerName, VersionNumber: 2 }));
    const third = await publish(layerName, files);
    expect(third.Version).toBe(3);
  });

  // 6.7, 9.4: an account principal renders as {"AWS":"arn:aws:iam::<account>:root"}
  // (the Smithy model's example); 6.8: no policy 404, duplicate statement 409,
  // stale RevisionId 412 (test_lambda_api.snapshot.json:L8915-L8974).
  test("layer version permissions render an account principal as its root ARN", async () => {
    const layerName = uniq("conf-layer-policy");
    const version = (await publish(layerName)).LayerVersionArn ?? "";
    await expectFailure(lambda.send(new GetLayerVersionPolicyCommand({ LayerName: layerName, VersionNumber: 1 })), {
      name: "ResourceNotFoundException",
      status: 404,
      messageIncludes: "No policy is associated with the given resource.",
    });
    const added = await lambda.send(
      new AddLayerVersionPermissionCommand({ LayerName: layerName, VersionNumber: 1, StatementId: "account", Action: "lambda:GetLayerVersion", Principal: defaultAccount }),
    );
    const statement = `{"Sid":"account","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::${defaultAccount}:root"},"Action":"lambda:GetLayerVersion","Resource":"${version}"}`;
    expect(added.Statement).toBe(statement);
    const policy = await lambda.send(new GetLayerVersionPolicyCommand({ LayerName: layerName, VersionNumber: 1 }));
    expect(policy.Policy).toBe(`{"Version":"2012-10-17","Id":"default","Statement":[${statement}]}`);
    await expectFailure(
      lambda.send(new AddLayerVersionPermissionCommand({ LayerName: layerName, VersionNumber: 1, StatementId: "account", Action: "lambda:GetLayerVersion", Principal: "*" })),
      { name: "ResourceConflictException", status: 409 },
    );
    await expectFailure(
      lambda.send(
        new AddLayerVersionPermissionCommand({ LayerName: layerName, VersionNumber: 1, StatementId: "other", Action: "lambda:GetLayerVersion", Principal: "*", RevisionId: "wrong" }),
      ),
      { name: "PreconditionFailedException", status: 412 },
    );
  });
});

describe.skipIf(!lambdaExecutionEnabled)("Layers on functions", () => {
  // 7.2: Layers [{Arn, CodeSize}] (test_lambda_api.snapshot.json:L8631-L8636);
  // 8.1, 8.2: Lambda extracts the layer under /opt and the Python runtime
  // searches /opt/python; 7.1: a version twice is refused (L9360-L9371).
  test("a function lists its layer, imports the layer's module and refuses a layer twice", async () => {
    const layer = await publish(uniq("conf-greet"));
    const arn = layer.LayerVersionArn ?? "";
    const fn = await makeFunction("import greet\n\n\ndef handler(event, context):\n    return {'greeting': greet.hello()}\n", [arn]);
    expect(fn.Layers).toEqual([{ Arn: arn, CodeSize: layer.Content?.CodeSize }]);
    const out = await lambda.send(new InvokeCommand({ FunctionName: fn.FunctionName, Payload: new TextEncoder().encode("{}") }));
    expect(out.FunctionError).toBeUndefined();
    expect(JSON.parse(out.Payload?.transformToString() ?? "")).toEqual({ greeting: "hello from the layer" });
    await expectFailure(lambda.send(new UpdateFunctionConfigurationCommand({ FunctionName: fn.FunctionName, Layers: [arn, arn] })), {
      name: "InvalidParameterValueException",
      status: 400,
      messageIncludes: `Two different versions of the same layer are not allowed to be referenced in the same function. ${arn} and ${arn} are versions of the same layer.`,
    });
  }, 180_000);
});

describe.skipIf(!lambdaExecutionEnabled)("Asynchronous invocation destinations", () => {
  // lambda-api.md 7.7: a destination function that does not exist is refused.
  // 3.4: a function error with MaximumRetryAttempts 0 sends RetriesExhausted
  // with count 1 and functionError Unhandled to the on-failure destination
  // (test_lambda_destinations.snapshot.json:L150-L175), as the SQS body (4.1).
  test("a missing destination is refused and an async failure reaches the on-failure queue", async () => {
    const fn = await makeFunction('def handler(event, context):\n    raise Exception("Handler fails")\n');
    const missing = `arn:aws:lambda:${region}:${defaultAccount}:function:${uniq("conf-missing")}`;
    await expectFailure(
      lambda.send(new PutFunctionEventInvokeConfigCommand({ FunctionName: fn.FunctionName, DestinationConfig: { OnSuccess: { Destination: missing } } })),
      { name: "InvalidParameterValueException", status: 400, messageIncludes: `The destination ARN ${missing} is invalid.` },
    );
    const queue = await makeQueue();
    await lambda.send(
      new PutFunctionEventInvokeConfigCommand({ FunctionName: fn.FunctionName, MaximumRetryAttempts: 0, DestinationConfig: { OnFailure: { Destination: queue.arn } } }),
    );
    const invoked = await lambda.send(new InvokeCommand({ FunctionName: fn.FunctionName, InvocationType: "Event", Payload: new TextEncoder().encode('{"n":1}') }));
    expect(invoked.StatusCode).toBe(202);
    const body = await eventually(
      "a record in the on-failure queue",
      async () => {
        const out = await sqs.send(new ReceiveMessageCommand({ QueueUrl: queue.url, WaitTimeSeconds: 1 }));
        return out.Messages?.[0]?.Body;
      },
      { timeoutMs: 120_000, intervalMs: 500 },
    );
    const record = JSON.parse(body);
    expect(record).toMatchObject({
      version: "1.0",
      requestContext: { functionArn: `${fn.FunctionArn}:$LATEST`, condition: "RetriesExhausted", approximateInvokeCount: 1 },
      requestPayload: { n: 1 },
      responseContext: { statusCode: 200, executedVersion: "$LATEST", functionError: "Unhandled" },
      responsePayload: { errorMessage: "Handler fails" },
    });
  }, 240_000);
});
