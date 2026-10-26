// S3 Object Lock and SSE-C through @aws-sdk/client-s3. Expected behavior:
// docs/research/s3-ssec-object-lock.md. Recorded texts cite LocalStack's
// AWS-validated snapshots (localstack@8b9a79f, validated 2026-02-21) by file
// and line; behavior AWS has not been recorded doing follows the fidelity
// switch understudy declares, read from its coverage report.

import { describe, expect, test } from "bun:test";
import {
  CreateBucketCommand,
  DeleteBucketCommand,
  DeleteObjectCommand,
  DeleteObjectsCommand,
  GetObjectLegalHoldCommand,
  HeadObjectCommand,
  ListObjectVersionsCommand,
  PutBucketEncryptionCommand,
  PutObjectCommand,
  PutObjectLegalHoldCommand,
  S3Client,
} from "@aws-sdk/client-s3";
import { clientConfig, endpoint } from "../support/env";
import { expectFailure, parseJson, uniq } from "../support/helpers";

const s3 = new S3Client({ ...clientConfig(), forcePathStyle: true });

/** The refusal of a delete or change Object Lock forbids (test_s3.snapshot.json L9478, L9639, L10044). */
const LOCKED = "Access Denied because object protected by object lock.";

/** The value the running understudy declares for the fidelity switch name. */
async function switchValue(name: string): Promise<string> {
  const resp = await fetch(`${endpoint}/_understudy/v1/coverage`);
  expect(resp.status).toBe(200);
  const report = parseJson(await resp.text()) as { switches: { name: string; value: string }[] };
  const found = report.switches.find((s) => s.name === name);
  if (found === undefined) throw new Error(`understudy declares no switch ${name} (s3-ssec-object-lock.md 11.4)`);
  return found.value;
}

/**
 * Runs fn with a new bucket with Object Lock, then lifts every hold, deletes every version with the
 * GOVERNANCE bypass and deletes the bucket. The tests use GOVERNANCE only, so cleanup always works.
 */
async function withLockBucket(fn: (bucket: string) => Promise<void>): Promise<void> {
  const bucket = uniq("lock");
  await s3.send(new CreateBucketCommand({ Bucket: bucket, ObjectLockEnabledForBucket: true }));
  try {
    await fn(bucket);
  } finally {
    const versions = await s3.send(new ListObjectVersionsCommand({ Bucket: bucket })).catch(() => undefined);
    for (const v of versions?.Versions ?? []) {
      await s3
        .send(new PutObjectLegalHoldCommand({ Bucket: bucket, Key: v.Key, VersionId: v.VersionId, LegalHold: { Status: "OFF" } }))
        .catch(() => undefined);
      await s3
        .send(new DeleteObjectCommand({ Bucket: bucket, Key: v.Key, VersionId: v.VersionId, BypassGovernanceRetention: true }))
        .catch(() => undefined);
    }
    for (const m of versions?.DeleteMarkers ?? []) {
      await s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: m.Key, VersionId: m.VersionId })).catch(() => undefined);
    }
    await s3.send(new DeleteBucketCommand({ Bucket: bucket })).catch(() => undefined);
  }
}

describe("S3 Object Lock", () => {
  test("GOVERNANCE retention refuses a versioned delete unless the bypass header is true (L9623, L9639, L9649)", async () => {
    await withLockBucket(async (bucket) => {
      const until = new Date(Math.floor(Date.now() / 1000) * 1000 + 3600_000);
      const put = await s3.send(
        new PutObjectCommand({ Bucket: bucket, Key: "gov", Body: "v", ObjectLockMode: "GOVERNANCE", ObjectLockRetainUntilDate: until }),
      );
      const head = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "gov", VersionId: put.VersionId }));
      expect(head.ObjectLockMode).toBe("GOVERNANCE");
      expect(head.ObjectLockRetainUntilDate?.getTime()).toBe(until.getTime());
      const failure = await expectFailure(s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: "gov", VersionId: put.VersionId })), {
        name: "AccessDenied",
        status: 403,
      });
      expect(failure.message).toBe(LOCKED);
      await s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: "gov", VersionId: put.VersionId, BypassGovernanceRetention: true }));
    });
  });

  test("a legal hold reads back ON, refuses deletes even with bypass, and reads OFF once lifted (L9950, L10044, L10054)", async () => {
    await withLockBucket(async (bucket) => {
      const put = await s3.send(new PutObjectCommand({ Bucket: bucket, Key: "test-delete-locked", Body: "test" }));
      await s3.send(
        new PutObjectLegalHoldCommand({ Bucket: bucket, Key: "test-delete-locked", VersionId: put.VersionId, LegalHold: { Status: "ON" } }),
      );
      const on = await s3.send(new GetObjectLegalHoldCommand({ Bucket: bucket, Key: "test-delete-locked", VersionId: put.VersionId }));
      expect(on.LegalHold?.Status).toBe("ON");
      const failure = await expectFailure(
        s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: "test-delete-locked", VersionId: put.VersionId, BypassGovernanceRetention: true })),
        { name: "AccessDenied", status: 403 },
      );
      expect(failure.message).toBe(LOCKED);
      const batch = await s3.send(
        new DeleteObjectsCommand({ Bucket: bucket, Delete: { Objects: [{ Key: "test-delete-locked", VersionId: put.VersionId }] } }),
      );
      expect(batch.$metadata.httpStatusCode).toBe(200);
      expect(batch.Errors).toEqual([{ Key: "test-delete-locked", VersionId: put.VersionId, Code: "AccessDenied", Message: LOCKED }]);
      await s3.send(
        new PutObjectLegalHoldCommand({ Bucket: bucket, Key: "test-delete-locked", VersionId: put.VersionId, LegalHold: { Status: "OFF" } }),
      );
      expect(await switchValue("s3.object-lock.cleared-reads")).toBe("404-and-OFF");
      const off = await s3.send(new GetObjectLegalHoldCommand({ Bucket: bucket, Key: "test-delete-locked", VersionId: put.VersionId }));
      expect(off.LegalHold?.Status).toBe("OFF");
      await s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: "test-delete-locked", VersionId: put.VersionId }));
    });
  });

  test("x-amz-bypass-governance-retention on a bucket without Object Lock fails InvalidArgument (test_s3_api.snapshot.json L3535)", async () => {
    const bucket = uniq("nolock");
    await s3.send(new CreateBucketCommand({ Bucket: bucket }));
    try {
      await s3.send(new PutObjectCommand({ Bucket: bucket, Key: "k", Body: "v" }));
      const failure = await expectFailure(s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: "k", BypassGovernanceRetention: true })), {
        name: "InvalidArgument",
        status: 400,
      });
      expect(failure.message).toBe("x-amz-bypass-governance-retention is only applicable to Object Lock enabled buckets.");
      expect(failure.error.ArgumentName).toBe("x-amz-bypass-governance-retention");
    } finally {
      await s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: "k" })).catch(() => undefined);
      await s3.send(new DeleteBucketCommand({ Bucket: bucket })).catch(() => undefined);
    }
  });
});

describe("S3 SSE-C", () => {
  test("SSE-C over plain HTTP follows switch s3.sse-c.plain-http", async () => {
    const bucket = uniq("ssec");
    await s3.send(new CreateBucketCommand({ Bucket: bucket }));
    try {
      await s3.send(
        new PutBucketEncryptionCommand({
          Bucket: bucket,
          ServerSideEncryptionConfiguration: { Rules: [{ BlockedEncryptionTypes: { EncryptionType: ["NONE"] } }] },
        }),
      );
      expect(await switchValue("s3.sse-c.plain-http")).toBe("refused");
      const failure = await expectFailure(
        s3.send(
          new PutObjectCommand({
            Bucket: bucket,
            Key: "k",
            Body: "v",
            SSECustomerAlgorithm: "AES256",
            // The SDK's SSE-C middleware base64-encodes the key and adds its MD5.
            SSECustomerKey: "1234567890abcdef1234567890abcdef",
          }),
        ),
        { name: "InvalidRequest", status: 400 },
      );
      expect(failure.message).toBe(
        "Requests specifying Server Side Encryption with Customer provided keys must be made over a secure connection.",
      );
    } finally {
      await s3.send(new DeleteBucketCommand({ Bucket: bucket })).catch(() => undefined);
    }
  });
});
