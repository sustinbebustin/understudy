// Helpers for the testcontainers-understudy suite. The suite runs a real
// understudy image through scripts/sealed and passes the container's endpoint
// to every client explicitly (testcontainers.md 8.7). The module's design is
// testcontainers.md sections 8 and 9.

import { execFileSync } from "node:child_process";
import { statSync } from "node:fs";
import { crc32, deflateRawSync } from "node:zlib";
import { getContainerRuntimeClient, getReaper } from "testcontainers";
import { CreateRoleCommand, IAMClient } from "@aws-sdk/client-iam";
import {
  CreateFunctionCommand,
  InvokeCommand,
  LambdaClient,
  waitUntilFunctionActiveV2,
} from "@aws-sdk/client-lambda";
import assert from "node:assert/strict";
import type { StartedUnderstudyContainer } from "../src/index.ts";

/** The image under test: UNDERSTUDY_IMAGE, else the local build `just image` tags (testcontainers.md 8.7). */
export function image(): string {
  return process.env.UNDERSTUDY_IMAGE || "understudy:dev";
}

/** Client settings for the container: the test pair understudytest.Env uses, in us-east-1 (testcontainers.md 3.8). */
export function clientConfig(c: StartedUnderstudyContainer) {
  return {
    endpoint: c.getEndpoint(),
    region: "us-east-1",
    credentials: { accessKeyId: "test", secretAccessKey: "test" },
  };
}

/** The session ID tc-node's Ryuk filters on (testcontainers.md 6.4). */
export async function sessionId(): Promise<string> {
  return (await getReaper(await getContainerRuntimeClient())).sessionId;
}

function docker(...args: string[]): string {
  return execFileSync("docker", args, { encoding: "utf8" });
}

/** Docker's inspect document for a container. */
export interface Inspect {
  Config: { Env: string[] | null; Labels: Record<string, string> | null };
  HostConfig: {
    PortBindings: Record<string, { HostIp: string; HostPort: string }[]> | null;
    Mounts: { Type: string; Source: string; Target: string; ReadOnly?: boolean }[] | null;
    Binds: string[] | null;
    GroupAdd: string[] | null;
  };
  NetworkSettings: {
    Ports: Record<string, { HostIp: string; HostPort: string }[] | null> | null;
    Networks: Record<string, unknown>;
  };
}

export function inspect(c: StartedUnderstudyContainer): Inspect {
  const [doc] = JSON.parse(docker("inspect", c.getId())) as Inspect[];
  assert.ok(doc, `docker inspect ${c.getId()} returned nothing`);
  return doc;
}

/** The container's environment; Docker keeps the last value of a repeated name. */
export function containerEnv(c: StartedUnderstudyContainer): Record<string, string> {
  const env: Record<string, string> = {};
  for (const kv of inspect(c).Config.Env ?? []) {
    const i = kv.indexOf("=");
    env[kv.slice(0, i)] = kv.slice(i + 1);
  }
  return env;
}

function list(filters: string[], all = true): string[] {
  const args = ["ps", "--no-trunc", "--format", "{{.ID}}"];
  if (all) args.push("-a");
  for (const f of filters) args.push("--filter", f);
  return docker(...args)
    .split("\n")
    .filter((id) => id !== "");
}

/**
 * understudy's own containers carrying this session's label: understudy
 * labels what it creates understudy.dev/managed=true (testcontainers.md 3.6),
 * and tc-node's session label is org.testcontainers.session-id
 * (testcontainers.md 6.4).
 */
export async function managedSessionContainers(): Promise<string[]> {
  return list(["label=understudy.dev/managed=true", `label=org.testcontainers.session-id=${await sessionId()}`]);
}

/** This session's containers of the image under test, running only unless all is set. */
export async function imageContainers(all = true): Promise<string[]> {
  return list([`ancestor=${image()}`, `label=org.testcontainers.session-id=${await sessionId()}`], all);
}

export function removeContainers(ids: string[]): void {
  if (ids.length > 0) docker("rm", "-f", "-v", ...ids);
}

/** The group owning the local Docker socket the client uses (testcontainers.md 8.5). */
export function dockerSocketGid(): number {
  const host = process.env.DOCKER_HOST ?? "unix:///var/run/docker.sock";
  assert.ok(host.startsWith("unix://"), `the Docker daemon is at ${host}, not a local unix socket; this test needs one`);
  return statSync(host.slice("unix://".length)).gid;
}

/** One admin API answer: the status and the body as text. */
export interface Answer {
  status: number;
  text: string;
}

/** Call /_understudy/v1<path> on the container, with the bearer token when given (testcontainers.md 3.3, 3.7). */
export async function adminCall(
  c: StartedUnderstudyContainer,
  method: string,
  path: string,
  token = "",
): Promise<Answer> {
  const headers: Record<string, string> = {};
  if (token) headers.Authorization = `Bearer ${token}`;
  const resp = await fetch(`${c.getEndpoint()}/_understudy/v1${path}`, { method, headers });
  return { status: resp.status, text: await resp.text() };
}

export async function adminBytes(c: StartedUnderstudyContainer, method: string, path: string): Promise<Uint8Array> {
  const resp = await fetch(`${c.getEndpoint()}/_understudy/v1${path}`, { method });
  assert.equal(resp.status, 200, `${method} ${path}`);
  return new Uint8Array(await resp.arrayBuffer());
}

/** The emulator clock: GET /_understudy/v1/clock (testcontainers.md 3.7). */
export async function clock(c: StartedUnderstudyContainer): Promise<{ mode: string; now: Date }> {
  const answer = await adminCall(c, "GET", "/clock");
  assert.equal(answer.status, 200, answer.text);
  const body = JSON.parse(answer.text) as { mode: string; now: string };
  return { mode: body.mode, now: new Date(body.now) };
}

/** A zip archive of text files for CreateFunction: deflated entries, no extra fields. */
export function zip(files: Record<string, string>): Uint8Array {
  const encoder = new TextEncoder();
  const chunks: Uint8Array[] = [];
  const directory: Uint8Array[] = [];
  let offset = 0;
  for (const [name, text] of Object.entries(files)) {
    const nameBytes = encoder.encode(name);
    const raw = encoder.encode(text);
    const packed = deflateRawSync(raw);
    const crc = crc32(raw);
    const local = new Uint8Array(30 + nameBytes.length);
    const lv = new DataView(local.buffer);
    lv.setUint32(0, 0x04034b50, true);
    lv.setUint16(4, 20, true);
    lv.setUint16(8, 8, true);
    lv.setUint32(14, crc, true);
    lv.setUint32(18, packed.length, true);
    lv.setUint32(22, raw.length, true);
    lv.setUint16(26, nameBytes.length, true);
    local.set(nameBytes, 30);
    const entry = new Uint8Array(46 + nameBytes.length);
    const ev = new DataView(entry.buffer);
    ev.setUint32(0, 0x02014b50, true);
    ev.setUint16(4, 20, true);
    ev.setUint16(6, 20, true);
    ev.setUint16(10, 8, true);
    ev.setUint32(16, crc, true);
    ev.setUint32(20, packed.length, true);
    ev.setUint32(24, raw.length, true);
    ev.setUint16(28, nameBytes.length, true);
    ev.setUint32(42, offset, true);
    entry.set(nameBytes, 46);
    chunks.push(local, packed);
    directory.push(entry);
    offset += local.length + packed.length;
  }
  const directorySize = directory.reduce((sum, e) => sum + e.length, 0);
  const end = new Uint8Array(22);
  const dv = new DataView(end.buffer);
  dv.setUint32(0, 0x06054b50, true);
  dv.setUint16(8, directory.length, true);
  dv.setUint16(10, directory.length, true);
  dv.setUint32(12, directorySize, true);
  dv.setUint32(16, offset, true);
  return Buffer.concat([...chunks, ...directory, end]);
}

// The trust policy Lambda's execution role needs
// (https://docs.aws.amazon.com/lambda/latest/dg/lambda-intro-execution-role.html).
const lambdaTrustPolicy = JSON.stringify({
  Version: "2012-10-17",
  Statement: [{ Effect: "Allow", Principal: { Service: "lambda.amazonaws.com" }, Action: "sts:AssumeRole" }],
});

/**
 * Create a python3.13 function echoing its event and invoke it once. The
 * runtime image is in the local cache and understudy pulls only a missing
 * image, so nothing is pulled from public.ecr.aws.
 */
export async function invokeEcho(c: StartedUnderstudyContainer): Promise<void> {
  const cfg = clientConfig(c);
  const role = await new IAMClient(cfg).send(
    new CreateRoleCommand({ RoleName: "echo-role", AssumeRolePolicyDocument: lambdaTrustPolicy }),
  );
  const lambda = new LambdaClient(cfg);
  await lambda.send(
    new CreateFunctionCommand({
      FunctionName: "echo",
      Role: role.Role?.Arn,
      Runtime: "python3.13",
      Handler: "handler.echo",
      Code: { ZipFile: zip({ "handler.py": "def echo(event, context):\n    return event\n" }) },
    }),
  );
  await waitUntilFunctionActiveV2({ client: lambda, maxWaitTime: 120 }, { FunctionName: "echo" });
  const out = await lambda.send(
    new InvokeCommand({ FunctionName: "echo", Payload: new TextEncoder().encode('{"ok": 1}') }),
  );
  // A synchronous invoke answers 200, and a handler error would set
  // FunctionError (https://docs.aws.amazon.com/lambda/latest/api/API_Invoke.html).
  const payload = new TextDecoder().decode(out.Payload);
  assert.equal(out.StatusCode, 200, payload);
  assert.equal(out.FunctionError, undefined, payload);
  assert.deepEqual(JSON.parse(payload), { ok: 1 });
}
