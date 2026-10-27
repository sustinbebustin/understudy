// Step Functions, Standard workflows, over awsJson1_0 through
// @aws-sdk/client-sfn: create, validate, a Choice/Wait/Pass machine finished
// with an admin clock advance, describe, history, list, stop, update,
// delete, and the refusals. Expected behavior:
// docs/research/stepfunctions-api.md and stepfunctions-asl.md.

import { afterEach, describe, expect, test } from "bun:test";
import { CreateRoleCommand, DeleteRoleCommand, IAMClient } from "@aws-sdk/client-iam";
import {
  CreateStateMachineCommand,
  DeleteStateMachineCommand,
  DescribeExecutionCommand,
  DescribeStateMachineCommand,
  GetExecutionHistoryCommand,
  ListExecutionsCommand,
  ListStateMachinesCommand,
  SFNClient,
  StartExecutionCommand,
  StartSyncExecutionCommand,
  StopExecutionCommand,
  UpdateStateMachineCommand,
  ValidateStateMachineDefinitionCommand,
} from "@aws-sdk/client-sfn";
import { clientConfig, endpoint } from "../support/env";
import { expectFailure, uniq } from "../support/helpers";

const sfn = new SFNClient(clientConfig());
const iam = new IAMClient(clientConfig());

const statesTrust = JSON.stringify({
  Version: "2012-10-17",
  Statement: [{ Effect: "Allow", Principal: { Service: "states.amazonaws.com" }, Action: "sts:AssumeRole" }],
});

// A Choice on $.wait between a one-hour Wait and the end.
const routeMachine = JSON.stringify({
  StartAt: "Route",
  States: {
    Route: { Type: "Choice", Choices: [{ Variable: "$.wait", BooleanEquals: true, Next: "Hold" }], Default: "Done" },
    Hold: { Type: "Wait", Seconds: 3600, Next: "Done" },
    Done: { Type: "Pass", Result: { finished: true }, ResultPath: "$.result", End: true },
  },
});

const cleanups: Array<() => Promise<unknown>> = [];
afterEach(async () => {
  while (cleanups.length > 0) {
    await cleanups.pop()?.().catch(() => undefined);
  }
});

async function admin(method: string, path: string, body?: unknown): Promise<unknown> {
  const resp = await fetch(`${endpoint}/_understudy/v1${path}`, {
    method,
    headers: body === undefined ? {} : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await resp.text();
  if (!resp.ok) throw new Error(`${method} ${path} = ${resp.status}: ${text}`);
  return text === "" ? {} : JSON.parse(text);
}

async function settle(): Promise<void> {
  const out = (await admin("GET", "/settle?timeout=30s")) as { settled?: boolean };
  if (!out.settled) throw new Error("the emulator did not settle within 30s");
}

/** Moves emulator time forward and settles; the clock is put back after the test. */
async function advance(seconds: number): Promise<void> {
  cleanups.push(() => admin("POST", "/reset", { services: ["sts"], clock: true }));
  await admin("POST", "/clock", { op: "advance", by: `${seconds}s` });
  await settle();
}

async function makeRole(): Promise<string> {
  const name = uniq("sfn-role");
  const out = await iam.send(new CreateRoleCommand({ RoleName: name, AssumeRolePolicyDocument: statesTrust }));
  cleanups.push(() => iam.send(new DeleteRoleCommand({ RoleName: name })));
  return out.Role?.Arn ?? "";
}

async function makeMachine(role: string, definition = routeMachine): Promise<string> {
  const out = await sfn.send(new CreateStateMachineCommand({ name: uniq("sfn"), definition, roleArn: role }));
  const arn = out.stateMachineArn ?? "";
  cleanups.push(() => sfn.send(new DeleteStateMachineCommand({ stateMachineArn: arn })));
  return arn;
}

describe("Step Functions", () => {
  test("ValidateStateMachineDefinition answers the recorded diagnostics (stepfunctions-api.md 5.6)", async () => {
    const ok = await sfn.send(new ValidateStateMachineDefinitionCommand({ definition: routeMachine }));
    expect(ok.result).toBe("OK");
    expect(ok.diagnostics).toEqual([]);
    const bad = await sfn.send(new ValidateStateMachineDefinitionCommand({ definition: "{}" }));
    expect(bad.result).toBe("FAIL");
    expect(bad.diagnostics?.[0]).toMatchObject({
      severity: "ERROR",
      code: "SCHEMA_VALIDATION_FAILED",
      message: "These fields are required: [States, StartAt]",
      location: "/",
    });
  });

  test("a Choice/Wait/Pass machine runs, finishes on one clock advance, stops, updates and deletes", async () => {
    const role = await makeRole();
    const arn = await makeMachine(role);
    // Recorded defaults (stepfunctions-api.md 5.2).
    const d = await sfn.send(new DescribeStateMachineCommand({ stateMachineArn: arn }));
    expect(d).toMatchObject({
      definition: routeMachine,
      status: "ACTIVE",
      type: "STANDARD",
      loggingConfiguration: { level: "OFF", includeExecutionData: false },
      tracingConfiguration: { enabled: false },
      encryptionConfiguration: { type: "AWS_OWNED_KEY" },
    });
    expect(d.revisionId).toBeUndefined();

    const run = await sfn.send(new StartExecutionCommand({ stateMachineArn: arn, name: "waits", input: '{"wait":true}' }));
    await settle();
    expect((await sfn.send(new DescribeExecutionCommand({ executionArn: run.executionArn }))).status).toBe("RUNNING");
    await advance(3600);
    const done = await sfn.send(new DescribeExecutionCommand({ executionArn: run.executionArn }));
    expect(done.status).toBe("SUCCEEDED");
    expect(JSON.parse(done.output ?? "null")).toEqual({ wait: true, result: { finished: true } });
    // History linkage (stepfunctions-asl.md 11.2 rules 1 and 2).
    const events = (await sfn.send(new GetExecutionHistoryCommand({ executionArn: run.executionArn }))).events ?? [];
    expect(events[0]).toMatchObject({ type: "ExecutionStarted", id: 1, previousEventId: 0 });
    expect(events[1]).toMatchObject({ type: "ChoiceStateEntered", previousEventId: 0 });
    expect(events.at(-1)?.type).toBe("ExecutionSucceeded");

    // StopExecution (stepfunctions-api.md 6.4).
    const held = await sfn.send(new StartExecutionCommand({ stateMachineArn: arn, name: "stopped", input: '{"wait":true}' }));
    await settle();
    await sfn.send(new StopExecutionCommand({ executionArn: held.executionArn }));
    await settle();
    expect((await sfn.send(new DescribeExecutionCommand({ executionArn: held.executionArn }))).status).toBe("ABORTED");

    expect((await sfn.send(new ListExecutionsCommand({ stateMachineArn: arn }))).executions).toHaveLength(2);
    const machines = (await sfn.send(new ListStateMachinesCommand({}))).stateMachines ?? [];
    expect(machines.map((m) => m.stateMachineArn)).toContain(arn);

    // UpdateStateMachine mints a revisionId (stepfunctions-api.md 5.3).
    const updated = await sfn.send(
      new UpdateStateMachineCommand({ stateMachineArn: arn, definition: routeMachine.replace('"Seconds":3600', '"Seconds":60') }),
    );
    expect(updated.revisionId).toMatch(/^[0-9a-f-]{36}$/);

    await sfn.send(new DeleteStateMachineCommand({ stateMachineArn: arn }));
    await settle();
    await expectFailure(sfn.send(new DescribeStateMachineCommand({ stateMachineArn: arn })), {
      name: "StateMachineDoesNotExist",
      status: 400,
      messageIncludes: `State Machine Does Not Exist: '${arn}'`,
    });
  });

  test("JSONata, EXPRESS and StartSyncExecution are refused (stepfunctions-asl.md 10.4; stepfunctions-api.md 13.1)", async () => {
    const role = await makeRole();
    const jsonata = JSON.stringify({ QueryLanguage: "JSONata", StartAt: "P", States: { P: { Type: "Pass", End: true } } });
    await expectFailure(sfn.send(new CreateStateMachineCommand({ name: uniq("sfn"), definition: jsonata, roleArn: role })), {
      name: "InvalidDefinition",
      status: 400,
      messageIncludes: "Invalid State Machine Definition: 'UNSUPPORTED_BY_UNDERSTUDY: ",
    });
    await expectFailure(
      sfn.send(new CreateStateMachineCommand({ name: uniq("sfn"), definition: routeMachine, roleArn: role, type: "EXPRESS" })),
      { name: "NotImplemented", status: 501 },
    );
    // Without disableHostPrefix the SDK sends this to sync-<host> (stepfunctions-api.md 3.3).
    const arn = await makeMachine(role);
    const unprefixed = new SFNClient({ ...clientConfig(), disableHostPrefix: true });
    await expectFailure(unprefixed.send(new StartSyncExecutionCommand({ stateMachineArn: arn })), {
      name: "NotImplemented",
      status: 501,
    });
  });
});
