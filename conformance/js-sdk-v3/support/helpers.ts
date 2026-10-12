// Small helpers shared by the per-service test files.

import { expect } from "bun:test";

let counter = 0;
const runId = Date.now().toString(36);

/** A name unique to this run and call, safe for queues, buckets, secrets and functions. */
export function uniq(prefix: string): string {
  counter += 1;
  return `${prefix}-${runId}-${counter}`.toLowerCase();
}

export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/** Polls check until it returns a value, failing with what after timeoutMs. */
export async function eventually<T>(
  what: string,
  check: () => Promise<T | undefined>,
  options: { timeoutMs?: number; intervalMs?: number } = {},
): Promise<T> {
  const { timeoutMs = 10_000, intervalMs = 100 } = options;
  const deadline = Date.now() + timeoutMs;
  let lastError: unknown;
  for (;;) {
    try {
      const value = await check();
      if (value !== undefined) return value;
    } catch (error) {
      lastError = error;
    }
    if (Date.now() >= deadline) {
      throw new Error(
        `timed out after ${timeoutMs}ms waiting for ${what}` +
          (lastError === undefined ? "" : `; last error: ${String(lastError)}`),
      );
    }
    await sleep(intervalMs);
  }
}

/** What a failed SDK call carries, flattened for assertions. */
export interface AwsFailure {
  /** The exception name the SDK resolved, e.g. "QueueDoesNotExist". */
  name: string;
  /** The error code on the wire when the SDK exposes one separately (Query-compatible errors). */
  code: string | undefined;
  status: number | undefined;
  message: string;
  fault: string | undefined;
  /** The error object itself for extra members (for example S3's ArgumentName). */
  error: Record<string, unknown>;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

/** Awaits promise, which must reject, and returns the error the SDK produced. */
export async function awsFailure(promise: Promise<unknown>): Promise<AwsFailure> {
  let outcome: { ok: true; value: unknown } | { ok: false; error: unknown };
  try {
    outcome = { ok: true, value: await promise };
  } catch (error) {
    outcome = { ok: false, error };
  }
  if (outcome.ok) {
    throw new Error(
      `expected the call to fail, but it succeeded with ${JSON.stringify(outcome.value)?.slice(0, 500)}`,
    );
  }
  const error = outcome.error;
  if (!isRecord(error)) throw new Error(`call failed with a non-object: ${String(error)}`);
  const metadata = isRecord(error.$metadata) ? error.$metadata : {};
  return {
    name: String(error.name),
    code: typeof error.Code === "string" ? error.Code : typeof error.code === "string" ? error.code : undefined,
    status: typeof metadata.httpStatusCode === "number" ? metadata.httpStatusCode : undefined,
    message: String(error.message ?? ""),
    fault: typeof error.$fault === "string" ? error.$fault : undefined,
    error,
  };
}

/**
 * Asserts promise fails with an error identified as name and, when given, HTTP
 * status and message text. An error is identified by the exception name the
 * SDK resolved or, for awsQueryCompatible services, the query code it exposes
 * as Code.
 */
export async function expectFailure(
  promise: Promise<unknown>,
  expected: { name: string; status?: number; messageIncludes?: string },
): Promise<AwsFailure> {
  const failure = await awsFailure(promise);
  const identity = failure.name === expected.name || failure.code === expected.name ? expected.name : failure.name;
  expect({ identity, status: failure.status, message: failure.message, code: failure.code }).toMatchObject({
    identity: expected.name,
    ...(expected.status === undefined ? {} : { status: expected.status }),
    ...(expected.messageIncludes === undefined
      ? {}
      : { message: expect.stringContaining(expected.messageIncludes) }),
  });
  return failure;
}

/** Reads a response body stream (SDK streaming blob) fully as bytes. */
export async function bodyBytes(body: {
  transformToByteArray(): Promise<Uint8Array>;
} | undefined): Promise<Uint8Array> {
  if (body === undefined) throw new Error("the response had no body");
  return body.transformToByteArray();
}

export async function bodyText(body: {
  transformToString(encoding?: string): Promise<string>;
} | undefined): Promise<string> {
  if (body === undefined) throw new Error("the response had no body");
  return body.transformToString();
}

/** Parses JSON, failing the test with the text when it is not JSON. */
export function parseJson(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    throw new Error(`expected JSON, got: ${text.slice(0, 300)}`);
  }
}
