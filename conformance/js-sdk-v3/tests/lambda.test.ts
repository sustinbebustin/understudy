// Lambda over restJson1, through @aws-sdk/client-lambda: the control plane the
// SDKs and Terraform use, Invoke in its three modes, real execution in the
// official Python base image, logs delivered to CloudWatch Logs, SQS event
// source mappings and function URLs.
// Expected behavior: docs/research/lambda-api.md, lambda-events.md,
// lambda-runtime-contract.md and error-matrix.md section 5.6.
//
// Execution needs a Docker daemon: functions run in AWS's Lambda base images,
// pulled on first use. CONFORMANCE_LAMBDA_EXECUTION=0 skips those tests and the
// control-plane tests that wait for a function to become Active.

import { afterAll, describe, expect, test } from "bun:test";
import { request } from "node:http";
import { createHash } from "node:crypto";
import {
  AddPermissionCommand,
  CreateAliasCommand,
  CreateEventSourceMappingCommand,
  CreateFunctionCommand,
  CreateFunctionUrlConfigCommand,
  DeleteEventSourceMappingCommand,
  DeleteFunctionCommand,
  DeleteFunctionConcurrencyCommand,
  DeleteFunctionUrlConfigCommand,
  GetAliasCommand,
  GetEventSourceMappingCommand,
  GetFunctionCommand,
  GetFunctionConcurrencyCommand,
  GetFunctionConfigurationCommand,
  GetFunctionUrlConfigCommand,
  GetPolicyCommand,
  InvokeCommand,
  LambdaClient,
  ListAliasesCommand,
  ListFunctionsCommand,
  ListFunctionUrlConfigsCommand,
  ListTagsCommand,
  ListVersionsByFunctionCommand,
  PublishVersionCommand,
  PutFunctionConcurrencyCommand,
  RemovePermissionCommand,
  TagResourceCommand,
  UntagResourceCommand,
  UpdateFunctionCodeCommand,
  UpdateFunctionConfigurationCommand,
  waitUntilFunctionActiveV2,
  waitUntilFunctionUpdatedV2,
  type CreateFunctionCommandInput,
  type FunctionConfiguration,
} from "@aws-sdk/client-lambda";
import { CreateRoleCommand, DeleteRoleCommand, IAMClient } from "@aws-sdk/client-iam";
import { CloudWatchLogsClient, DeleteLogGroupCommand, DescribeLogGroupsCommand, DescribeLogStreamsCommand, FilterLogEventsCommand } from "@aws-sdk/client-cloudwatch-logs";
import { CreateQueueCommand, DeleteQueueCommand, GetQueueAttributesCommand, ReceiveMessageCommand, SendMessageCommand, SQSClient } from "@aws-sdk/client-sqs";
import { clientConfig, defaultAccount, lambdaExecutionEnabled, port, region } from "../support/env";
import { awsFailure, eventually, expectFailure, sleep, uniq } from "../support/helpers";
import { zip } from "../support/zip";

const lambda = new LambdaClient(clientConfig());
const iam = new IAMClient(clientConfig());
const logs = new CloudWatchLogsClient(clientConfig());
const sqs = new SQSClient(clientConfig());

const RUNTIME = "python3.13";
const HANDLER = "lambda_function.handler";
const TRUST_LAMBDA = JSON.stringify({
  Version: "2012-10-17",
  Statement: [{ Effect: "Allow", Principal: { Service: "lambda.amazonaws.com" }, Action: "sts:AssumeRole" }],
});

const ECHO = `
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
`;

const FAILS = `
def handler(event, context):
    raise Exception("Handler fails")
`;

const BROKEN_IMPORT = `
import module_that_does_not_exist_anywhere

def handler(event, context):
    return {}
`;

const SLEEPS = `
import time

def handler(event, context):
    time.sleep(10)
    return {"slept": True}
`;

const BATCH_HANDLER = `
def handler(event, context):
    failures = []
    for record in event["Records"]:
        print("conformance-record", record["body"], record["attributes"]["ApproximateReceiveCount"])
        if "fail" in record["body"]:
            failures.append({"itemIdentifier": record["messageId"]})
    return {"batchItemFailures": failures}
`;

const RAISES = `
def handler(event, context):
    for record in event["Records"]:
        print("conformance-record", record["body"], record["attributes"]["ApproximateReceiveCount"])
    raise Exception("always fails")
`;

const URL_HANDLER = `
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
`;

const cleanups: Array<() => Promise<unknown>> = [];

afterAll(async () => {
  for (const cleanup of cleanups.reverse()) await cleanup().catch(() => undefined);
});

async function makeRole(): Promise<string> {
  const name = uniq("conf-lambda-role");
  const out = await iam.send(new CreateRoleCommand({ RoleName: name, AssumeRolePolicyDocument: TRUST_LAMBDA }));
  cleanups.push(() => iam.send(new DeleteRoleCommand({ RoleName: name })));
  const arn = out.Role?.Arn;
  if (arn === undefined) throw new Error("CreateRole returned no Arn");
  return arn;
}

let sharedRole: Promise<string> | undefined;
const roleArn = () => (sharedRole ??= makeRole());

interface MakeOptions extends Partial<Omit<CreateFunctionCommandInput, "Code" | "FunctionName">> {
  wait?: boolean;
}

/** Creates a python3.13 function running code and, by default, waits until it is Active. */
async function makeFunction(code: string = ECHO, options: MakeOptions = {}): Promise<FunctionConfiguration & { FunctionName: string; FunctionArn: string }> {
  const { wait = true, ...rest } = options;
  const name = uniq("conf-fn");
  const created = await lambda.send(
    new CreateFunctionCommand({
      FunctionName: name,
      Runtime: RUNTIME,
      Role: await roleArn(),
      Handler: HANDLER,
      Code: { ZipFile: zip({ "lambda_function.py": code }) },
      ...rest,
    }),
  );
  cleanups.push(() => logs.send(new DeleteLogGroupCommand({ logGroupName: `/aws/lambda/${name}` })));
  cleanups.push(() => lambda.send(new DeleteFunctionCommand({ FunctionName: name })));
  if (wait) await waitUntilFunctionActiveV2({ client: lambda, maxWaitTime: 120, minDelay: 1, maxDelay: 2 }, { FunctionName: name });
  if (created.FunctionName === undefined || created.FunctionArn === undefined) throw new Error("CreateFunction returned no name or ARN");
  return { ...created, FunctionName: created.FunctionName, FunctionArn: created.FunctionArn };
}

async function waitUpdated(name: string): Promise<void> {
  await waitUntilFunctionUpdatedV2({ client: lambda, maxWaitTime: 120, minDelay: 1, maxDelay: 2 }, { FunctionName: name });
}

/** Invokes name and parses the JSON payload. */
async function invokeJson(name: string, event?: unknown, extra: { Qualifier?: string; LogType?: "Tail" } = {}) {
  const out = await lambda.send(
    new InvokeCommand({
      FunctionName: name,
      Payload: event === undefined ? undefined : new TextEncoder().encode(JSON.stringify(event)),
      ...extra,
    }),
  );
  const text = out.Payload === undefined ? "" : out.Payload.transformToString();
  return { out, body: text === "" ? undefined : (JSON.parse(text) as Record<string, any>) };
}

async function logMessages(fn: string, marker: string): Promise<string[]> {
  try {
    const messages: string[] = [];
    let nextToken: string | undefined;
    do {
      const page = await logs.send(new FilterLogEventsCommand({ logGroupName: `/aws/lambda/${fn}`, filterPattern: `"${marker}"`, nextToken }));
      for (const event of page.events ?? []) messages.push(event.message ?? "");
      nextToken = page.nextToken;
    } while (nextToken !== undefined);
    return messages;
  } catch (error) {
    if (error instanceof Error && error.name === "ResourceNotFoundException") return [];
    throw error;
  }
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

describe.skipIf(!lambdaExecutionEnabled)("Lambda control plane", () => {
  test("CreateFunction returns the full configuration and the function becomes Active", async () => {
    const created = await makeFunction(ECHO, { Description: "conformance function", MemorySize: 256, Timeout: 10, Environment: { Variables: { FOO: "bar" } } });
    const name = created.FunctionName;
    expect(created.FunctionArn).toBe(`arn:aws:lambda:${region}:${defaultAccount}:function:${name}`);
    expect(created).toMatchObject({
      Runtime: RUNTIME,
      Handler: HANDLER,
      Version: "$LATEST",
      PackageType: "Zip",
      Architectures: ["x86_64"],
      TracingConfig: { Mode: "PassThrough" },
      MemorySize: 256,
      Timeout: 10,
      Description: "conformance function",
      Environment: { Variables: { FOO: "bar" } },
    });
    expect(created.CodeSize ?? 0).toBeGreaterThan(0);
    expect(created.RevisionId).toMatch(UUID);
    expect(created.LastModified).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}\+0000$/);
    expect(created.CodeSha256).toMatch(/^[A-Za-z0-9+/]{43}=$/);

    const config = await lambda.send(new GetFunctionConfigurationCommand({ FunctionName: name }));
    expect(config.State).toBe("Active");
    expect(config.LastUpdateStatus).toBe("Successful");
    expect(config.LoggingConfig?.LogGroup).toBe(`/aws/lambda/${name}`);
  });

  test("CreateFunction defaults are Timeout 3, MemorySize 128 and 512 MB ephemeral storage", async () => {
    const created = await makeFunction(ECHO, { wait: false });
    expect(created).toMatchObject({ Timeout: 3, MemorySize: 128, EphemeralStorage: { Size: 512 }, LoggingConfig: { LogFormat: "Text" } });
  });

  test("CodeSha256 is the base64 SHA-256 of the deployment package and CodeSize its size", async () => {
    const zipFile = zip({ "lambda_function.py": ECHO });
    const name = uniq("conf-sha");
    const created = await lambda.send(new CreateFunctionCommand({ FunctionName: name, Runtime: RUNTIME, Role: await roleArn(), Handler: HANDLER, Code: { ZipFile: zipFile } }));
    cleanups.push(() => lambda.send(new DeleteFunctionCommand({ FunctionName: name })));
    expect(created.CodeSha256).toBe(createHash("sha256").update(zipFile).digest("base64"));
    expect(created.CodeSize).toBe(zipFile.length);
  });

  test("GetFunction returns the configuration, the code location and the tags", async () => {
    const created = await makeFunction(ECHO, { Tags: { team: "pipeline" } });
    const got = await lambda.send(new GetFunctionCommand({ FunctionName: created.FunctionName }));
    expect(got.Configuration?.FunctionArn).toBe(created.FunctionArn);
    expect(got.Code?.RepositoryType).toBe("S3");
    expect(got.Code?.Location).toMatch(/^http/);
    expect(got.Tags).toEqual({ team: "pipeline" });
    expect(got.Concurrency).toBeUndefined();
  });

  test("ListFunctions paginates with Marker and MaxItems", async () => {
    const names = new Set<string>();
    for (let i = 0; i < 3; i += 1) names.add((await makeFunction(ECHO, { wait: false })).FunctionName);
    const listed = new Set<string>();
    let marker: string | undefined;
    do {
      const page = await lambda.send(new ListFunctionsCommand({ MaxItems: 2, Marker: marker }));
      expect((page.Functions ?? []).length).toBeLessThanOrEqual(2);
      for (const fn of page.Functions ?? []) listed.add(fn.FunctionName ?? "");
      marker = page.NextMarker;
    } while (marker !== undefined && marker !== "");
    for (const name of names) expect(listed.has(name)).toBe(true);
  });

  test("GetFunction on a missing function fails ResourceNotFoundException with the function ARN", async () => {
    const name = uniq("conf-missing");
    const failure = await expectFailure(lambda.send(new GetFunctionCommand({ FunctionName: name })), { name: "ResourceNotFoundException", status: 404 });
    expect(failure.message).toBe(`Function not found: arn:aws:lambda:${region}:${defaultAccount}:function:${name}`);
  });

  test("CreateFunction with an existing name fails ResourceConflictException", async () => {
    const created = await makeFunction(ECHO, { wait: false });
    const failure = await expectFailure(
      lambda.send(
        new CreateFunctionCommand({
          FunctionName: created.FunctionName,
          Runtime: RUNTIME,
          Role: await roleArn(),
          Handler: HANDLER,
          Code: { ZipFile: zip({ "lambda_function.py": ECHO }) },
        }),
      ),
      { name: "ResourceConflictException", status: 409 },
    );
    expect(failure.message).toBe(`Function already exist: ${created.FunctionName}`);
  });

  test("CreateFunction with a deprecated runtime is refused", async () => {
    const failure = await expectFailure(
      lambda.send(
        new CreateFunctionCommand({
          FunctionName: uniq("conf-old"),
          Runtime: "go1.x",
          Role: await roleArn(),
          Handler: "main",
          Code: { ZipFile: zip({ main: "x" }) },
        }),
      ),
      { name: "InvalidParameterValueException", status: 400 },
    );
    expect(failure.message).toBe(
      "The runtime parameter of go1.x is no longer supported for creating or updating AWS Lambda functions. We recommend you use a supported runtime while creating or updating functions.",
    );
  });

  test("CreateFunction with a corrupt zip is refused", async () => {
    const failure = await expectFailure(
      lambda.send(
        new CreateFunctionCommand({
          FunctionName: uniq("conf-bad-zip"),
          Runtime: RUNTIME,
          Role: await roleArn(),
          Handler: HANDLER,
          Code: { ZipFile: new TextEncoder().encode("not a zip file") },
        }),
      ),
      { name: "InvalidParameterValueException", status: 400 },
    );
    expect(failure.message).toBe("Could not unzip uploaded file. Please check your file, then try to upload again.");
  });

  test("UpdateFunctionConfiguration changes only the fields that were sent", async () => {
    const created = await makeFunction(ECHO, { Environment: { Variables: { FOO: "one" } } });
    const name = created.FunctionName;
    const out = await lambda.send(
      new UpdateFunctionConfigurationCommand({ FunctionName: name, MemorySize: 512, Timeout: 20, Environment: { Variables: { FOO: "two", BAR: "x" } } }),
    );
    expect(out.MemorySize).toBe(512);
    await waitUpdated(name);
    const config = await lambda.send(new GetFunctionConfigurationCommand({ FunctionName: name }));
    expect({ memory: config.MemorySize, timeout: config.Timeout }).toEqual({ memory: 512, timeout: 20 });
    expect(config.Environment?.Variables).toEqual({ FOO: "two", BAR: "x" });
    expect(config.RevisionId).not.toBe(created.RevisionId);
    expect(config.Handler).toBe(HANDLER);
  });

  test("TagResource, ListTags and UntagResource", async () => {
    const created = await makeFunction();
    const arn = created.FunctionArn;
    await lambda.send(new TagResourceCommand({ Resource: arn, Tags: { a: "1", b: "2" } }));
    expect((await lambda.send(new ListTagsCommand({ Resource: arn }))).Tags).toEqual({ a: "1", b: "2" });
    await lambda.send(new UntagResourceCommand({ Resource: arn, TagKeys: ["a", "never-set"] }));
    expect((await lambda.send(new ListTagsCommand({ Resource: arn }))).Tags).toEqual({ b: "2" });
  });

  test("reserved concurrency is stored and reported by GetFunction", async () => {
    const { FunctionName: name } = await makeFunction();
    expect((await lambda.send(new GetFunctionConcurrencyCommand({ FunctionName: name }))).ReservedConcurrentExecutions).toBeUndefined();
    const put = await lambda.send(new PutFunctionConcurrencyCommand({ FunctionName: name, ReservedConcurrentExecutions: 7 }));
    expect(put.ReservedConcurrentExecutions).toBe(7);
    expect((await lambda.send(new GetFunctionConcurrencyCommand({ FunctionName: name }))).ReservedConcurrentExecutions).toBe(7);
    expect((await lambda.send(new GetFunctionCommand({ FunctionName: name }))).Concurrency?.ReservedConcurrentExecutions).toBe(7);
    await lambda.send(new DeleteFunctionConcurrencyCommand({ FunctionName: name }));
    expect((await lambda.send(new GetFunctionConcurrencyCommand({ FunctionName: name }))).ReservedConcurrentExecutions).toBeUndefined();
  });

  test("AddPermission, GetPolicy and RemovePermission", async () => {
    const created = await makeFunction();
    const name = created.FunctionName;
    const topic = `arn:aws:sns:${region}:${defaultAccount}:conf-topic`;
    const out = await lambda.send(
      new AddPermissionCommand({
        FunctionName: name,
        StatementId: "sns",
        Action: "lambda:InvokeFunction",
        Principal: "sns.amazonaws.com",
        SourceArn: topic,
        SourceAccount: defaultAccount,
      }),
    );
    const statement = JSON.parse(out.Statement ?? "null");
    expect(statement).toEqual({
      Sid: "sns",
      Effect: "Allow",
      Principal: { Service: "sns.amazonaws.com" },
      Action: "lambda:InvokeFunction",
      Resource: created.FunctionArn,
      Condition: { ArnLike: { "AWS:SourceArn": topic }, StringEquals: { "AWS:SourceAccount": defaultAccount } },
    });
    const policy = JSON.parse((await lambda.send(new GetPolicyCommand({ FunctionName: name }))).Policy ?? "null");
    expect(policy.Version).toBe("2012-10-17");
    expect(policy.Statement).toEqual([statement]);

    const dup = await expectFailure(
      lambda.send(new AddPermissionCommand({ FunctionName: name, StatementId: "sns", Action: "lambda:InvokeFunction", Principal: "sns.amazonaws.com" })),
      { name: "ResourceConflictException", status: 409 },
    );
    expect(dup.message.startsWith("The statement id (sns) provided already exists.")).toBe(true);

    await lambda.send(new RemovePermissionCommand({ FunctionName: name, StatementId: "sns" }));
    await expectFailure(lambda.send(new GetPolicyCommand({ FunctionName: name })), { name: "ResourceNotFoundException", status: 404 });
  });

  test("DeleteFunction removes the function and a second delete fails ResourceNotFoundException", async () => {
    const created = await makeFunction(ECHO, { wait: false });
    await lambda.send(new DeleteFunctionCommand({ FunctionName: created.FunctionName }));
    await expectFailure(lambda.send(new GetFunctionCommand({ FunctionName: created.FunctionName })), { name: "ResourceNotFoundException", status: 404 });
    await expectFailure(lambda.send(new DeleteFunctionCommand({ FunctionName: created.FunctionName })), { name: "ResourceNotFoundException", status: 404 });
  });
});

describe.skipIf(!lambdaExecutionEnabled)("Lambda versions and aliases", () => {
  test("a published version pins its code while $LATEST moves on", async () => {
    const created = await makeFunction('def handler(event, context):\n    return {"which": "one"}\n');
    const name = created.FunctionName;
    const published = await lambda.send(new PublishVersionCommand({ FunctionName: name }));
    expect(published.Version).toBe("1");
    expect(published.FunctionArn).toBe(`${created.FunctionArn}:1`);
    await lambda.send(new CreateAliasCommand({ FunctionName: name, Name: "live", FunctionVersion: "1" }));

    await lambda.send(
      new UpdateFunctionCodeCommand({ FunctionName: name, ZipFile: zip({ "lambda_function.py": 'def handler(event, context):\n    return {"which": "two"}\n' }) }),
    );
    await waitUpdated(name);

    let result = await invokeJson(name);
    expect(result.body).toEqual({ which: "two" });
    expect(result.out.ExecutedVersion).toBe("$LATEST");
    result = await invokeJson(name, undefined, { Qualifier: "1" });
    expect(result.body).toEqual({ which: "one" });
    expect(result.out.ExecutedVersion).toBe("1");
    result = await invokeJson(name, undefined, { Qualifier: "live" });
    expect(result.body).toEqual({ which: "one" });
    expect(result.out.ExecutedVersion).toBe("1");

    const alias = await lambda.send(new GetAliasCommand({ FunctionName: name, Name: "live" }));
    expect(alias.FunctionVersion).toBe("1");
    expect(alias.AliasArn).toBe(`${created.FunctionArn}:live`);
    const versions = await lambda.send(new ListVersionsByFunctionCommand({ FunctionName: name }));
    expect(versions.Versions?.map((v) => v.Version)).toEqual(["$LATEST", "1"]);
    const aliases = await lambda.send(new ListAliasesCommand({ FunctionName: name }));
    expect(aliases.Aliases?.map((a) => a.Name)).toEqual(["live"]);
  });

  test("Invoke of a version that does not exist fails ResourceNotFoundException naming the qualified ARN", async () => {
    const created = await makeFunction();
    const failure = await expectFailure(lambda.send(new InvokeCommand({ FunctionName: created.FunctionName, Qualifier: "10", Payload: new TextEncoder().encode("{}") })), {
      name: "ResourceNotFoundException",
      status: 404,
    });
    expect(failure.message).toBe(`Function not found: ${created.FunctionArn}:10`);
  });
});

describe.skipIf(!lambdaExecutionEnabled)("Lambda Invoke", () => {
  test("RequestResponse runs the function and returns the result with the request id header", async () => {
    const created = await makeFunction(ECHO, { MemorySize: 256, Environment: { Variables: { FOO: "bar" } } });
    const name = created.FunctionName;
    const event = { list: [1, 2, { deep: null }], text: "café", number: 1.5 };
    const { out, body } = await invokeJson(name, event);
    expect(out.StatusCode).toBe(200);
    expect(out.FunctionError).toBeUndefined();
    expect(out.ExecutedVersion).toBe("$LATEST");
    expect(body?.event).toEqual(event);
    expect(body).toMatchObject({ function_name: name, function_version: "$LATEST", memory: "256", invoked_arn: created.FunctionArn });
    expect(body?.env).toMatchObject({
      FOO: "bar",
      AWS_REGION: region,
      AWS_LAMBDA_FUNCTION_NAME: name,
      AWS_LAMBDA_FUNCTION_MEMORY_SIZE: "256",
      AWS_LAMBDA_LOG_GROUP_NAME: `/aws/lambda/${name}`,
    });
    expect(out.$metadata.requestId).toBe(body?.request_id);
  });

  test("a warm environment keeps module state between invocations and is replaced when the configuration changes", async () => {
    const { FunctionName: name } = await makeFunction();
    const counters: number[] = [];
    for (let i = 0; i < 3; i += 1) counters.push((await invokeJson(name, {})).body?.counter);
    expect(counters).toEqual([1, 2, 3]);

    await lambda.send(new UpdateFunctionConfigurationCommand({ FunctionName: name, Environment: { Variables: { FOO: "changed" } } }));
    await waitUpdated(name);
    const { body } = await invokeJson(name, {});
    expect(body?.counter).toBe(1);
    expect(body?.env.FOO).toBe("changed");
  });

  test("a handler exception is reported as FunctionError Unhandled with status 200", async () => {
    const { FunctionName: name } = await makeFunction(FAILS);
    const { out, body } = await invokeJson(name, { in: 1 });
    expect(out.StatusCode).toBe(200);
    expect(out.FunctionError).toBe("Unhandled");
    expect(body?.errorMessage).toBe("Handler fails");
    expect(body?.errorType).toBe("Exception");
    expect(body?.requestId).toMatch(UUID);
    expect((body?.stackTrace as string[]).some((line) => line.includes("Handler fails"))).toBe(true);
  });

  test("an import error is reported as a Runtime.ImportModuleError function error", async () => {
    const { FunctionName: name } = await makeFunction(BROKEN_IMPORT);
    const { out, body } = await invokeJson(name, {});
    expect(out.FunctionError).toBe("Unhandled");
    expect(body?.errorType).toBe("Runtime.ImportModuleError");
    expect(body?.errorMessage).toContain("module_that_does_not_exist_anywhere");
  });

  test("a timeout is reported as a Sandbox.Timedout function error", async () => {
    const { FunctionName: name } = await makeFunction(SLEEPS, { Timeout: 1 });
    const { out, body } = await invokeJson(name, {});
    expect(out.FunctionError).toBe("Unhandled");
    expect(body?.errorType).toBe("Sandbox.Timedout");
    expect(body?.errorMessage).toMatch(/^RequestId: [0-9a-f-]{36} Error: Task timed out after 1\.00 seconds$/);
  });

  test("LogType Tail returns the base64 end of the invocation log", async () => {
    const { FunctionName: name } = await makeFunction();
    const { out, body } = await invokeJson(name, { tail: true }, { LogType: "Tail" });
    const tail = Buffer.from(out.LogResult ?? "", "base64").toString();
    const id = body?.request_id as string;
    expect(tail).toContain(`START RequestId: ${id} Version: $LATEST`);
    expect(tail).toContain("conformance-marker");
    expect(tail).toContain(`END RequestId: ${id}`);
    expect(tail).toMatch(new RegExp(`REPORT RequestId: ${id}\\tDuration: [\\d.]+ ms\\tBilled Duration: \\d+ ms\\tMemory Size: 128 MB\\tMax Memory Used: \\d+ MB`));
  });

  test("InvocationType Event returns 202 with an empty body and the function still runs", async () => {
    const { FunctionName: name } = await makeFunction();
    const out = await lambda.send(
      new InvokeCommand({ FunctionName: name, InvocationType: "Event", Payload: new TextEncoder().encode(JSON.stringify({ async: "marker-async-1" })) }),
    );
    expect(out.StatusCode).toBe(202);
    expect(out.Payload?.length ?? 0).toBe(0);
    const found = await eventually("the asynchronous invocation to log", async () => {
      const messages = await logMessages(name, "marker-async-1");
      return messages.length > 0 ? messages : undefined;
    }, { timeoutMs: 60_000, intervalMs: 500 });
    expect(found.some((m) => m.includes("conformance-marker"))).toBe(true);
  });

  test("InvocationType DryRun returns 204 and does not run the function", async () => {
    const { FunctionName: name } = await makeFunction();
    const out = await lambda.send(
      new InvokeCommand({ FunctionName: name, InvocationType: "DryRun", Payload: new TextEncoder().encode(JSON.stringify({ dry: "marker-dry-run" })) }),
    );
    expect(out.StatusCode).toBe(204);
    expect(out.Payload?.length ?? 0).toBe(0);
    await sleep(2000);
    expect(await logMessages(name, "marker-dry-run")).toEqual([]);
    await expectFailure(
      lambda.send(new InvokeCommand({ FunctionName: uniq("conf-missing"), InvocationType: "DryRun", Payload: new TextEncoder().encode("{}") })),
      { name: "ResourceNotFoundException", status: 404 },
    );
  });

  test("Invoke of a missing function fails ResourceNotFoundException", async () => {
    const failure = await expectFailure(lambda.send(new InvokeCommand({ FunctionName: uniq("conf-missing"), Payload: new TextEncoder().encode("{}") })), {
      name: "ResourceNotFoundException",
      status: 404,
    });
    expect(failure.message.startsWith(`Function not found: arn:aws:lambda:${region}:`)).toBe(true);
  });

  test("an invalid JSON payload fails InvalidRequestContentException", async () => {
    const { FunctionName: name } = await makeFunction();
    const failure = await expectFailure(lambda.send(new InvokeCommand({ FunctionName: name, Payload: new TextEncoder().encode("{not json") })), {
      name: "InvalidRequestContentException",
      status: 400,
    });
    expect(failure.message.startsWith("Could not parse request body into json: Could not parse payload into json:")).toBe(true);
  });

  test("a request payload over 6 MB fails RequestEntityTooLargeException", async () => {
    const { FunctionName: name } = await makeFunction();
    const big = new TextEncoder().encode(JSON.stringify({ blob: "x".repeat(6 * 1024 * 1024 + 100) }));
    const failure = await expectFailure(lambda.send(new InvokeCommand({ FunctionName: name, Payload: big })), {
      name: "RequestEntityTooLargeException",
      status: 413,
    });
    expect(failure.message).toBe("Request must be smaller than 6291456 bytes for the InvokeFunction operation");
  });

  test("an empty payload invokes the function with a JSON document", async () => {
    const { FunctionName: name } = await makeFunction();
    const out = await lambda.send(new InvokeCommand({ FunctionName: name }));
    expect(out.StatusCode).toBe(200);
    expect(out.FunctionError).toBeUndefined();
  });

  test("reserved concurrency 0 throttles invocations with TooManyRequestsException", async () => {
    const { FunctionName: name } = await makeFunction();
    await lambda.send(new PutFunctionConcurrencyCommand({ FunctionName: name, ReservedConcurrentExecutions: 0 }));
    const failure = await awsFailure(lambda.send(new InvokeCommand({ FunctionName: name, Payload: new TextEncoder().encode("{}") })));
    expect({ name: failure.name, status: failure.status }).toEqual({ name: "TooManyRequestsException", status: 429 });
  });
});

describe.skipIf(!lambdaExecutionEnabled)("Lambda logs", () => {
  test("invocation logs land in the function's CloudWatch Logs group", async () => {
    const { FunctionName: name } = await makeFunction();
    const { body } = await invokeJson(name, { log: "marker-cloudwatch-1" });
    const group = `/aws/lambda/${name}`;
    const streams = await eventually("the log stream", async () => {
      const found = await logs.send(new DescribeLogStreamsCommand({ logGroupName: group }));
      return (found.logStreams ?? []).length > 0 ? found.logStreams : undefined;
    });
    expect(streams).toHaveLength(1);
    expect(streams[0]?.logStreamName).toMatch(/^\d{4}\/\d{2}\/\d{2}\/\[\$LATEST\][0-9a-f]{32}$/);

    const id = body?.request_id as string;
    const messages = await eventually("the START, END and REPORT lines", async () => {
      const all = (await logs.send(new FilterLogEventsCommand({ logGroupName: group }))).events?.map((e) => e.message ?? "") ?? [];
      return all.some((m) => m.startsWith(`REPORT RequestId: ${id}`)) ? all : undefined;
    });
    expect(messages.some((m) => m.startsWith(`START RequestId: ${id} Version: $LATEST`))).toBe(true);
    expect(messages.some((m) => m.includes("marker-cloudwatch-1"))).toBe(true);
    expect(messages.some((m) => m.startsWith(`END RequestId: ${id}`))).toBe(true);
  });

  test("the log group appears with the first invocation", async () => {
    const { FunctionName: name } = await makeFunction();
    const prefix = `/aws/lambda/${name}`;
    expect((await logs.send(new DescribeLogGroupsCommand({ logGroupNamePrefix: prefix }))).logGroups).toEqual([]);
    await invokeJson(name, {});
    const groups = await eventually("the log group", async () => {
      const found = (await logs.send(new DescribeLogGroupsCommand({ logGroupNamePrefix: prefix }))).logGroups ?? [];
      return found.length > 0 ? found : undefined;
    });
    expect(groups.map((g) => g.logGroupName)).toEqual([prefix]);
  });
});

describe.skipIf(!lambdaExecutionEnabled)("Lambda SDK calls from inside a function", () => {
  test("a function reaches SQS and STS through understudy with the role's assumed identity", async () => {
    const queueName = uniq("conf-from-lambda");
    const queueUrl = (await sqs.send(new CreateQueueCommand({ QueueName: queueName }))).QueueUrl ?? "";
    cleanups.push(() => sqs.send(new DeleteQueueCommand({ QueueUrl: queueUrl })));
    const code = `
import boto3, os

def handler(event, context):
    sqs = boto3.client("sqs")
    sqs.send_message(QueueUrl=event["queue"], MessageBody=event["body"])
    ident = boto3.client("sts").get_caller_identity()
    return {"arn": ident["Arn"], "account": ident["Account"], "region": os.environ["AWS_REGION"]}
`;
    const { FunctionName: name } = await makeFunction(code, { Timeout: 30 });
    const { out, body } = await invokeJson(name, { queue: queueUrl, body: "sent from inside a function" });
    expect(out.FunctionError, JSON.stringify(body)).toBeUndefined();
    expect(body?.arn).toMatch(/^arn:aws:sts::\d{12}:assumed-role\/conf-lambda-role-[a-z0-9-]+\/.+$/);
    const message = await eventually("the message from the function", async () => {
      const received = await sqs.send(new ReceiveMessageCommand({ QueueUrl: queueUrl, WaitTimeSeconds: 1 }));
      return received.Messages?.[0];
    });
    expect(message.Body).toBe("sent from inside a function");
  });
});

describe.skipIf(!lambdaExecutionEnabled)("Lambda SQS event source mappings", () => {
  async function makeQueue(attributes: Record<string, string> = {}): Promise<{ url: string; arn: string }> {
    const url = (await sqs.send(new CreateQueueCommand({ QueueName: uniq("conf-esm"), Attributes: attributes }))).QueueUrl ?? "";
    cleanups.push(() => sqs.send(new DeleteQueueCommand({ QueueUrl: url })));
    const arn = (await sqs.send(new GetQueueAttributesCommand({ QueueUrl: url, AttributeNames: ["QueueArn"] }))).Attributes?.QueueArn ?? "";
    return { url, arn };
  }

  async function makeMapping(functionName: string, arn: string, extra: Partial<ConstructorParameters<typeof CreateEventSourceMappingCommand>[0]> = {}) {
    const mapping = await lambda.send(new CreateEventSourceMappingCommand({ FunctionName: functionName, EventSourceArn: arn, ...extra }));
    cleanups.push(() => lambda.send(new DeleteEventSourceMappingCommand({ UUID: mapping.UUID })));
    return mapping;
  }

  async function queueCounts(url: string) {
    const attrs = (await sqs.send(new GetQueueAttributesCommand({ QueueUrl: url, AttributeNames: ["All"] }))).Attributes ?? {};
    return { visible: attrs.ApproximateNumberOfMessages, inFlight: attrs.ApproximateNumberOfMessagesNotVisible };
  }

  test("CreateEventSourceMapping describes the mapping and it settles to Enabled", async () => {
    const fn = await makeFunction(BATCH_HANDLER);
    const queue = await makeQueue();
    const mapping = await makeMapping(fn.FunctionName, queue.arn, { BatchSize: 5, MaximumBatchingWindowInSeconds: 1 });
    expect(mapping).toMatchObject({ BatchSize: 5, MaximumBatchingWindowInSeconds: 1, EventSourceArn: queue.arn, FunctionArn: fn.FunctionArn });
    expect(mapping.FunctionResponseTypes ?? []).toEqual([]);
    expect(mapping.EventSourceMappingArn).toBe(`arn:aws:lambda:${region}:${defaultAccount}:event-source-mapping:${mapping.UUID}`);
    const settled = await eventually("the mapping to leave Creating", async () => {
      const got = await lambda.send(new GetEventSourceMappingCommand({ UUID: mapping.UUID }));
      return got.State === "Enabled" || got.State === "Disabled" ? got : undefined;
    });
    expect(settled.State).toBe("Enabled");
    expect(settled.StateTransitionReason).toBe("USER_INITIATED");
  });

  test("CreateEventSourceMapping validates the batch size, duplicates and a missing function", async () => {
    const fn = await makeFunction(BATCH_HANDLER);
    const queue = await makeQueue();
    const tooBig = await expectFailure(
      lambda.send(new CreateEventSourceMappingCommand({ FunctionName: fn.FunctionName, EventSourceArn: queue.arn, BatchSize: 11 })),
      { name: "InvalidParameterValueException", status: 400 },
    );
    expect(tooBig.message).toBe("Maximum batch window in seconds must be greater than 0 if maximum batch size is greater than 10");

    await makeMapping(fn.FunctionName, queue.arn);
    await expectFailure(lambda.send(new CreateEventSourceMappingCommand({ FunctionName: fn.FunctionName, EventSourceArn: queue.arn })), {
      name: "ResourceConflictException",
      status: 409,
    });

    const missing = await expectFailure(
      lambda.send(new CreateEventSourceMappingCommand({ FunctionName: uniq("conf-missing"), EventSourceArn: queue.arn })),
      { name: "InvalidParameterValueException", status: 400 },
    );
    expect(missing.message).toBe("Function does not exist");
  });

  test("messages are delivered in batches and deleted once the function succeeds", async () => {
    const fn = await makeFunction(BATCH_HANDLER);
    const queue = await makeQueue();
    await makeMapping(fn.FunctionName, queue.arn, { BatchSize: 10, MaximumBatchingWindowInSeconds: 1 });
    for (let i = 0; i < 7; i += 1) await sqs.send(new SendMessageCommand({ QueueUrl: queue.url, MessageBody: `ok-${i}` }));

    await eventually("the queue to be drained by the mapping", async () => {
      const counts = await queueCounts(queue.url);
      return counts.visible === "0" && counts.inFlight === "0" ? counts : undefined;
    }, { timeoutMs: 60_000, intervalMs: 500 });
    const seen = await eventually("every record to be logged", async () => {
      const bodies = new Set((await logMessages(fn.FunctionName, "conformance-record")).map((m) => m.split(" ")[1] ?? ""));
      return bodies.size === 7 ? [...bodies].sort() : undefined;
    }, { timeoutMs: 40_000, intervalMs: 500 });
    expect(seen).toEqual(Array.from({ length: 7 }, (_, i) => `ok-${i}`));
  });

  test("ReportBatchItemFailures returns only the failed messages to the queue", async () => {
    const fn = await makeFunction(BATCH_HANDLER);
    const queue = await makeQueue({ VisibilityTimeout: "3" });
    await makeMapping(fn.FunctionName, queue.arn, { BatchSize: 10, MaximumBatchingWindowInSeconds: 1, FunctionResponseTypes: ["ReportBatchItemFailures"] });
    for (const body of ["ok-1", "fail-2", "ok-3"]) await sqs.send(new SendMessageCommand({ QueueUrl: queue.url, MessageBody: body }));

    const retried = await eventually("the failed message to be retried", async () => {
      const all = await logMessages(fn.FunctionName, "conformance-record");
      return all.filter((m) => m.includes("fail-2")).length >= 2 ? all : undefined;
    }, { timeoutMs: 60_000, intervalMs: 500 });
    expect(retried.filter((m) => m.includes("ok-1"))).toHaveLength(1);
    expect(retried.filter((m) => m.includes("ok-3"))).toHaveLength(1);
    expect(retried.some((m) => m.trimEnd().endsWith("fail-2 2"))).toBe(true);
  });

  test("the whole batch is retried when the function raises", async () => {
    const fn = await makeFunction(RAISES);
    const queue = await makeQueue({ VisibilityTimeout: "3" });
    await makeMapping(fn.FunctionName, queue.arn, { BatchSize: 10, MaximumBatchingWindowInSeconds: 1 });
    await sqs.send(new SendMessageCommand({ QueueUrl: queue.url, MessageBody: "retry-me" }));
    const again = await eventually("the retry", async () => {
      const all = await logMessages(fn.FunctionName, "conformance-record");
      return all.length >= 2 ? all : undefined;
    }, { timeoutMs: 60_000, intervalMs: 500 });
    expect(again.some((m) => m.trimEnd().endsWith("retry-me 2"))).toBe(true);
  });

  test("DeleteEventSourceMapping removes the mapping", async () => {
    const fn = await makeFunction(BATCH_HANDLER, { wait: false });
    const queue = await makeQueue();
    const mapping = await lambda.send(new CreateEventSourceMappingCommand({ FunctionName: fn.FunctionName, EventSourceArn: queue.arn }));
    // AWS refuses to change a mapping still Creating (ResourceInUseException,
    // lambda-events.md 1.1), and a deleted one reads as Deleting for several
    // seconds before Get answers 404 (lambda-events.md 1.5), so the test
    // waits on both as Terraform's waiters do.
    await eventually("the mapping to leave Creating", async () => {
      const got = await lambda.send(new GetEventSourceMappingCommand({ UUID: mapping.UUID }));
      return got.State === "Enabled" || got.State === "Disabled" ? got : undefined;
    });
    const deleted = await lambda.send(new DeleteEventSourceMappingCommand({ UUID: mapping.UUID }));
    expect(deleted.State).toBe("Deleting");
    // awsFailure throws while Get still succeeds, which eventually retries.
    const failure = await eventually("the deleted mapping to be gone", () => awsFailure(lambda.send(new GetEventSourceMappingCommand({ UUID: mapping.UUID }))));
    expect(failure.name).toBe("ResourceNotFoundException");
    expect(failure.status).toBe(404);
  });
});

/** Sends a request to a function URL through the emulator listener, addressed by Host header so no DNS is involved. */
function callUrl(
  functionUrl: string,
  method: string,
  path: string,
  options: { body?: Buffer; headers?: Record<string, string> } = {},
): Promise<{ status: number; headers: Record<string, string | string[] | undefined>; body: Buffer }> {
  return new Promise((resolve, reject) => {
    const req = request(
      { host: "127.0.0.1", port, method, path, headers: { host: new URL(functionUrl).host, ...(options.headers ?? {}), ...(options.body === undefined ? {} : { "content-length": String(options.body.length) }) } },
      (res) => {
        const chunks: Buffer[] = [];
        res.on("data", (chunk: Buffer) => chunks.push(chunk));
        res.on("end", () => resolve({ status: res.statusCode ?? 0, headers: res.headers, body: Buffer.concat(chunks) }));
        res.on("error", reject);
      },
    );
    req.on("error", reject);
    req.end(options.body);
  });
}

describe.skipIf(!lambdaExecutionEnabled)("Lambda function URLs", () => {
  async function publicUrl(code: string): Promise<string> {
    const { FunctionName: name } = await makeFunction(code);
    const config = await lambda.send(new CreateFunctionUrlConfigCommand({ FunctionName: name, AuthType: "NONE" }));
    cleanups.push(() => lambda.send(new DeleteFunctionUrlConfigCommand({ FunctionName: name })));
    await lambda.send(
      new AddPermissionCommand({ FunctionName: name, StatementId: "url-public", Action: "lambda:InvokeFunctionUrl", Principal: "*", FunctionUrlAuthType: "NONE" }),
    );
    await lambda.send(
      new AddPermissionCommand({ FunctionName: name, StatementId: "url-invoke", Action: "lambda:InvokeFunction", Principal: "*", InvokedViaFunctionUrl: true }),
    );
    if (config.FunctionUrl === undefined) throw new Error("CreateFunctionUrlConfig returned no FunctionUrl");
    return config.FunctionUrl;
  }

  test("function URL configuration is created, read, listed and deleted", async () => {
    const created = await makeFunction();
    const name = created.FunctionName;
    const config = await lambda.send(
      new CreateFunctionUrlConfigCommand({ FunctionName: name, AuthType: "NONE", Cors: { AllowOrigins: ["*"], AllowMethods: ["GET"] } }),
    );
    cleanups.push(() => lambda.send(new DeleteFunctionUrlConfigCommand({ FunctionName: name })));
    expect(config.FunctionUrl).toMatch(/^https?:\/\/[a-z0-9]{32}\.lambda-url\.[a-z0-9-]+\.[^/]+\/$/);
    expect(config).toMatchObject({ FunctionArn: created.FunctionArn, AuthType: "NONE", Cors: { AllowOrigins: ["*"] } });
    // AWS's Create response omits InvokeMode when the request does not set it
    // (LocalStack's AWS-recorded test_url_config_lifecycle url_creation has no
    // InvokeMode; lambda-events.md 7.1); the reads report BUFFERED.
    expect(config.InvokeMode).toBeUndefined();

    const read = await lambda.send(new GetFunctionUrlConfigCommand({ FunctionName: name }));
    expect(read.FunctionUrl).toBe(config.FunctionUrl);
    expect(read.InvokeMode).toBe("BUFFERED");
    const listed = await lambda.send(new ListFunctionUrlConfigsCommand({ FunctionName: name }));
    expect(listed.FunctionUrlConfigs?.map((c) => c.FunctionUrl)).toEqual([config.FunctionUrl]);
    await expectFailure(lambda.send(new CreateFunctionUrlConfigCommand({ FunctionName: name, AuthType: "NONE" })), { name: "ResourceConflictException", status: 409 });

    await lambda.send(new DeleteFunctionUrlConfigCommand({ FunctionName: name }));
    await expectFailure(lambda.send(new GetFunctionUrlConfigCommand({ FunctionName: name })), { name: "ResourceNotFoundException", status: 404 });
  });

  test("a GET delivers a payload v2.0 event and the response status, headers and cookies come back", async () => {
    const url = await publicUrl(URL_HANDLER);
    const res = await callUrl(url, "GET", "/some/path?a=1&a=2&b=x", { headers: { "x-test": "hello", cookie: "c1=v1; c2=v2" } });
    expect(res.status).toBe(201);
    expect(res.headers["x-custom"]).toBe("yes");
    expect(res.headers["content-type"]).toBe("text/plain");
    expect(res.headers["set-cookie"]).toEqual(["a=1; Path=/", "b=2"]);
    expect(res.headers["x-amzn-requestid"]).toBeTruthy();

    const event = JSON.parse(res.body.toString());
    expect(event).toMatchObject({
      version: "2.0",
      routeKey: "$default",
      rawPath: "/some/path",
      path: "/some/path",
      rawQueryString: "a=1&a=2&b=x",
      query: { a: "1,2", b: "x" },
      method: "GET",
      "x-test": "hello",
      cookies: ["c1=v1", "c2=v2"],
      isBase64Encoded: false,
    });
    expect(event.body === null || event.body === "").toBe(true);
  });

  test("a POST delivers the request body", async () => {
    const url = await publicUrl(URL_HANDLER);
    const res = await callUrl(url, "POST", "/", { body: Buffer.from(JSON.stringify({ hello: "world" })), headers: { "content-type": "application/json" } });
    expect(res.status).toBe(201);
    const event = JSON.parse(res.body.toString());
    expect(event).toMatchObject({ method: "POST", body: '{"hello":"world"}', isBase64Encoded: false });
  });

  test("a plain return value becomes a 200 JSON response", async () => {
    const url = await publicUrl('def handler(event, context):\n    return {"hello": "world"}\n');
    const res = await callUrl(url, "GET", "/");
    expect(res.status).toBe(200);
    expect(res.headers["content-type"]).toBe("application/json");
    expect(res.body.toString()).toBe('{"hello":"world"}');
  });

  test("a base64 response body is decoded to bytes", async () => {
    const code = `
import base64

def handler(event, context):
    return {"statusCode": 200, "headers": {"content-type": "application/octet-stream"},
            "isBase64Encoded": True, "body": base64.b64encode(bytes(range(256))).decode()}
`;
    const url = await publicUrl(code);
    const res = await callUrl(url, "GET", "/");
    expect(res.status).toBe(200);
    expect([...res.body]).toEqual(Array.from({ length: 256 }, (_, i) => i));
  });

  test("a function URL without the public permission statements is forbidden", async () => {
    const { FunctionName: name } = await makeFunction();
    const config = await lambda.send(new CreateFunctionUrlConfigCommand({ FunctionName: name, AuthType: "NONE" }));
    cleanups.push(() => lambda.send(new DeleteFunctionUrlConfigCommand({ FunctionName: name })));
    const res = await callUrl(config.FunctionUrl ?? "", "GET", "/");
    expect(res.status).toBe(403);
  });
});
