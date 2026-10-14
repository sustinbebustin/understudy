// S3 over restXml, through @aws-sdk/client-s3, @aws-sdk/lib-storage and
// @aws-sdk/s3-request-presigner. Expected behavior: docs/research/s3.md
// and sigv4-checksums-endpoints.md.

import { describe, expect, test } from "bun:test";
import {
  AbortMultipartUploadCommand,
  CompleteMultipartUploadCommand,
  CopyObjectCommand,
  CreateBucketCommand,
  CreateMultipartUploadCommand,
  DeleteBucketCommand,
  DeleteBucketCorsCommand,
  DeleteBucketEncryptionCommand,
  DeleteBucketLifecycleCommand,
  DeleteBucketOwnershipControlsCommand,
  DeleteBucketPolicyCommand,
  DeleteBucketTaggingCommand,
  DeleteBucketWebsiteCommand,
  DeleteObjectCommand,
  DeleteObjectsCommand,
  DeleteObjectTaggingCommand,
  DeletePublicAccessBlockCommand,
  GetBucketAccelerateConfigurationCommand,
  GetBucketAclCommand,
  GetBucketCorsCommand,
  GetBucketEncryptionCommand,
  GetBucketLifecycleConfigurationCommand,
  GetBucketLocationCommand,
  GetBucketLoggingCommand,
  GetBucketMetricsConfigurationCommand,
  GetBucketNotificationConfigurationCommand,
  GetBucketOwnershipControlsCommand,
  GetBucketPolicyCommand,
  GetBucketReplicationCommand,
  GetBucketRequestPaymentCommand,
  GetBucketTaggingCommand,
  GetBucketVersioningCommand,
  GetBucketWebsiteCommand,
  GetObjectAttributesCommand,
  GetObjectCommand,
  GetObjectLockConfigurationCommand,
  GetObjectTaggingCommand,
  GetPublicAccessBlockCommand,
  HeadBucketCommand,
  HeadObjectCommand,
  ListBucketsCommand,
  ListMultipartUploadsCommand,
  ListObjectsCommand,
  ListObjectsV2Command,
  ListObjectVersionsCommand,
  ListPartsCommand,
  PutBucketAccelerateConfigurationCommand,
  PutBucketCorsCommand,
  PutBucketEncryptionCommand,
  PutBucketLifecycleConfigurationCommand,
  PutBucketNotificationConfigurationCommand,
  PutBucketOwnershipControlsCommand,
  PutBucketPolicyCommand,
  PutBucketRequestPaymentCommand,
  PutBucketTaggingCommand,
  PutBucketVersioningCommand,
  PutBucketWebsiteCommand,
  PutObjectCommand,
  PutObjectTaggingCommand,
  PutPublicAccessBlockCommand,
  S3Client,
  UploadPartCommand,
  UploadPartCopyCommand,
  type BucketLocationConstraint,
  type ChecksumAlgorithm as SdkChecksumAlgorithm,
  type CompletedPart,
} from "@aws-sdk/client-s3";
import { Upload } from "@aws-sdk/lib-storage";
import { getSignedUrl } from "@aws-sdk/s3-request-presigner";
import { CreateKeyCommand, DisableKeyCommand, KMSClient } from "@aws-sdk/client-kms";
import { CreateQueueCommand, DeleteMessageBatchCommand, ReceiveMessageCommand, SQSClient } from "@aws-sdk/client-sqs";
import { Readable } from "node:stream";
import { checksum, compositeChecksum, md5Hex, multipartEtag, patternBytes } from "../support/checksums";
import { clientConfig, defaultAccount, localhostEndpoint, region } from "../support/env";
import { awsFailure, bodyBytes, bodyText, eventually, expectFailure, sleep, uniq } from "../support/helpers";

const s3 = new S3Client({ ...clientConfig(), forcePathStyle: true });
/** Same client, but with the SDK's default checksum behavior turned down to what S3 requires. */
const s3WhenRequired = new S3Client({
  ...clientConfig(),
  forcePathStyle: true,
  requestChecksumCalculation: "WHEN_REQUIRED",
  responseChecksumValidation: "WHEN_REQUIRED",
});
const kms = new KMSClient(clientConfig());
const sqs = new SQSClient(clientConfig());

const MIB = 1024 * 1024;
const text = (value: string) => new TextEncoder().encode(value);

async function makeBucket(options: { objectLock?: boolean } = {}): Promise<string> {
  const bucket = uniq("b");
  await s3.send(new CreateBucketCommand({ Bucket: bucket, ObjectLockEnabledForBucket: options.objectLock }));
  return bucket;
}

async function put(bucket: string, key: string, body: string | Uint8Array, extra: Partial<ConstructorParameters<typeof PutObjectCommand>[0]> = {}) {
  return s3.send(new PutObjectCommand({ Bucket: bucket, Key: key, Body: body, ...extra }));
}

async function read(bucket: string, key: string): Promise<Uint8Array> {
  const out = await s3.send(new GetObjectCommand({ Bucket: bucket, Key: key }));
  return bodyBytes(out.Body);
}

async function keysOf(bucket: string, options: { prefix?: string } = {}): Promise<string[]> {
  const out = await s3.send(new ListObjectsV2Command({ Bucket: bucket, Prefix: options.prefix }));
  return (out.Contents ?? []).map((o) => o.Key ?? "");
}

describe("S3 buckets", () => {
  test("CreateBucket then HeadBucket succeeds and reports the bucket region", async () => {
    const bucket = await makeBucket();
    const head = await s3.send(new HeadBucketCommand({ Bucket: bucket }));
    expect(head.$metadata.httpStatusCode).toBe(200);
    expect(head.BucketRegion).toBe(region);
  });

  test("HeadBucket on a missing bucket fails NotFound with status 404", async () => {
    await expectFailure(s3.send(new HeadBucketCommand({ Bucket: uniq("missing") })), { name: "NotFound", status: 404 });
  });

  test("CreateBucket in us-east-1 with no location constraint reports an empty GetBucketLocation", async () => {
    const bucket = await makeBucket();
    const out = await s3.send(new GetBucketLocationCommand({ Bucket: bucket }));
    expect(out.LocationConstraint ?? "").toBe("");
  });

  test("CreateBucket with LocationConstraint us-east-1 fails InvalidLocationConstraint", async () => {
    const failure = await expectFailure(
      s3.send(
        new CreateBucketCommand({
          Bucket: uniq("b"),
          // The SDK's enum omits us-east-1 because AWS refuses it; send it anyway.
          CreateBucketConfiguration: { LocationConstraint: "us-east-1" as unknown as BucketLocationConstraint },
        }),
      ),
      { name: "InvalidLocationConstraint", status: 400, messageIncludes: "The specified location-constraint is not valid" },
    );
    expect(failure.error.LocationConstraint).toBe("us-east-1");
  });

  test("CreateBucket on an existing bucket you own in us-east-1 succeeds without changing it", async () => {
    const bucket = await makeBucket();
    await put(bucket, "keep", "data");
    const again = await s3.send(new CreateBucketCommand({ Bucket: bucket }));
    expect(again.$metadata.httpStatusCode).toBe(200);
    expect(await keysOf(bucket)).toEqual(["keep"]);
  });

  test("CreateBucket with tags on an existing owned us-east-1 bucket fails BucketAlreadyOwnedByYou", async () => {
    const bucket = await makeBucket();
    await expectFailure(
      s3.send(new CreateBucketCommand({ Bucket: bucket, CreateBucketConfiguration: { Tags: [{ Key: "a", Value: "b" }] } })),
      { name: "BucketAlreadyOwnedByYou", status: 409 },
    );
  });

  const badNames = ["ab", "UPPERCASE-bucket", "has_underscore", "-leading-dash", "trailing-dash-", "double..dot", "192.168.5.4", "xn--punycode", "a".repeat(64)];
  for (const name of badNames) {
    test(`CreateBucket rejects the invalid name ${name.length > 20 ? `${name.slice(0, 10)}... (${name.length} chars)` : name}`, async () => {
      const failure = await awsFailure(s3.send(new CreateBucketCommand({ Bucket: name })));
      expect(failure.status).toBe(400);
      expect(["InvalidBucketName", "InvalidRequest"]).toContain(failure.name);
    });
  }

  test("ListBuckets includes created buckets with name, creation date and owner", async () => {
    const bucket = await makeBucket();
    const out = await s3.send(new ListBucketsCommand({}));
    const found = out.Buckets?.find((b) => b.Name === bucket);
    expect(found).toBeDefined();
    expect(found?.CreationDate).toBeInstanceOf(Date);
    expect(out.Owner?.ID?.length ?? 0).toBeGreaterThan(0);
  });

  test("ListBuckets filters by prefix and pages with MaxBuckets and ContinuationToken", async () => {
    const prefix = uniq("lb");
    const names = [`${prefix}-a`, `${prefix}-b`, `${prefix}-c`];
    for (const name of names) await s3.send(new CreateBucketCommand({ Bucket: name }));
    const first = await s3.send(new ListBucketsCommand({ Prefix: prefix, MaxBuckets: 2 }));
    expect(first.Buckets?.map((b) => b.Name)).toEqual(names.slice(0, 2));
    expect(first.ContinuationToken).toBeDefined();
    const second = await s3.send(new ListBucketsCommand({ Prefix: prefix, MaxBuckets: 2, ContinuationToken: first.ContinuationToken }));
    expect(second.Buckets?.map((b) => b.Name)).toEqual(names.slice(2));
    expect(second.ContinuationToken).toBeUndefined();
  });

  test("DeleteBucket removes an empty bucket and the bucket then fails NoSuchBucket", async () => {
    const bucket = await makeBucket();
    const out = await s3.send(new DeleteBucketCommand({ Bucket: bucket }));
    expect(out.$metadata.httpStatusCode).toBe(204);
    await expectFailure(s3.send(new ListObjectsV2Command({ Bucket: bucket })), { name: "NoSuchBucket", status: 404 });
  });

  test("DeleteBucket on a non-empty bucket fails BucketNotEmpty with status 409", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "v");
    const failure = await expectFailure(s3.send(new DeleteBucketCommand({ Bucket: bucket })), {
      name: "BucketNotEmpty",
      status: 409,
      messageIncludes: "The bucket you tried to delete is not empty",
    });
    expect(failure.error.BucketName).toBe(bucket);
  });

  test("DeleteBucket on a missing bucket fails NoSuchBucket", async () => {
    await expectFailure(s3.send(new DeleteBucketCommand({ Bucket: uniq("missing") })), { name: "NoSuchBucket", status: 404 });
  });

  test("a missing bucket error carries the BucketName", async () => {
    const bucket = uniq("missing");
    const failure = await expectFailure(s3.send(new ListObjectsV2Command({ Bucket: bucket })), {
      name: "NoSuchBucket",
      status: 404,
      messageIncludes: "The specified bucket does not exist",
    });
    expect(failure.error.BucketName).toBe(bucket);
  });
});

describe("S3 bucket subresources: defaults of a new bucket", () => {
  test("GetBucketAcl returns the owner with a single FULL_CONTROL grant", async () => {
    const bucket = await makeBucket();
    const out = await s3.send(new GetBucketAclCommand({ Bucket: bucket }));
    expect(out.Owner?.ID?.length ?? 0).toBeGreaterThan(0);
    expect(out.Grants).toHaveLength(1);
    expect(out.Grants?.[0]).toMatchObject({ Permission: "FULL_CONTROL", Grantee: { Type: "CanonicalUser", ID: out.Owner?.ID } });
  });

  test("GetBucketOwnershipControls returns BucketOwnerEnforced", async () => {
    const bucket = await makeBucket();
    const out = await s3.send(new GetBucketOwnershipControlsCommand({ Bucket: bucket }));
    expect(out.OwnershipControls?.Rules).toEqual([{ ObjectOwnership: "BucketOwnerEnforced" }]);
  });

  test("GetPublicAccessBlock returns all four settings true", async () => {
    const bucket = await makeBucket();
    const out = await s3.send(new GetPublicAccessBlockCommand({ Bucket: bucket }));
    expect(out.PublicAccessBlockConfiguration).toEqual({
      BlockPublicAcls: true,
      BlockPublicPolicy: true,
      IgnorePublicAcls: true,
      RestrictPublicBuckets: true,
    });
  });

  test("GetBucketEncryption returns AES256 with BucketKeyEnabled false", async () => {
    const bucket = await makeBucket();
    const out = await s3.send(new GetBucketEncryptionCommand({ Bucket: bucket }));
    const rule = out.ServerSideEncryptionConfiguration?.Rules?.[0];
    expect(out.ServerSideEncryptionConfiguration?.Rules).toHaveLength(1);
    expect(rule?.ApplyServerSideEncryptionByDefault?.SSEAlgorithm).toBe("AES256");
    expect(rule?.BucketKeyEnabled ?? false).toBe(false);
  });

  test("GetBucketVersioning on a new bucket has no Status", async () => {
    const bucket = await makeBucket();
    const out = await s3.send(new GetBucketVersioningCommand({ Bucket: bucket }));
    expect(out.Status).toBeUndefined();
  });

  test("GetBucketAccelerateConfiguration on a new bucket has no Status", async () => {
    const bucket = await makeBucket();
    const out = await s3.send(new GetBucketAccelerateConfigurationCommand({ Bucket: bucket }));
    expect(out.Status).toBeUndefined();
  });

  test("GetBucketRequestPayment reports BucketOwner", async () => {
    const bucket = await makeBucket();
    const out = await s3.send(new GetBucketRequestPaymentCommand({ Bucket: bucket }));
    expect(out.Payer).toBe("BucketOwner");
  });

  test("GetBucketLogging on a new bucket has no LoggingEnabled", async () => {
    const bucket = await makeBucket();
    const out = await s3.send(new GetBucketLoggingCommand({ Bucket: bucket }));
    expect(out.LoggingEnabled).toBeUndefined();
  });

  test("GetBucketNotificationConfiguration on a new bucket is empty", async () => {
    const bucket = await makeBucket();
    const out = await s3.send(new GetBucketNotificationConfigurationCommand({ Bucket: bucket }));
    expect(out.QueueConfigurations ?? []).toEqual([]);
    expect(out.TopicConfigurations ?? []).toEqual([]);
    expect(out.LambdaFunctionConfigurations ?? []).toEqual([]);
  });
});

describe("S3 bucket subresources: not configured", () => {
  const unset: Array<{
    title: string;
    call: (bucket: string) => Promise<unknown>;
    code: string;
    message: string;
  }> = [
    { title: "GetBucketPolicy", call: (b) => s3.send(new GetBucketPolicyCommand({ Bucket: b })), code: "NoSuchBucketPolicy", message: "The bucket policy does not exist" },
    { title: "GetBucketTagging", call: (b) => s3.send(new GetBucketTaggingCommand({ Bucket: b })), code: "NoSuchTagSet", message: "The TagSet does not exist" },
    { title: "GetBucketCors", call: (b) => s3.send(new GetBucketCorsCommand({ Bucket: b })), code: "NoSuchCORSConfiguration", message: "The CORS configuration does not exist" },
    {
      title: "GetBucketWebsite",
      call: (b) => s3.send(new GetBucketWebsiteCommand({ Bucket: b })),
      code: "NoSuchWebsiteConfiguration",
      message: "The specified bucket does not have a website configuration",
    },
    {
      title: "GetBucketLifecycleConfiguration",
      call: (b) => s3.send(new GetBucketLifecycleConfigurationCommand({ Bucket: b })),
      code: "NoSuchLifecycleConfiguration",
      message: "The lifecycle configuration does not exist",
    },
    {
      title: "GetBucketReplication",
      call: (b) => s3.send(new GetBucketReplicationCommand({ Bucket: b })),
      code: "ReplicationConfigurationNotFoundError",
      message: "The replication configuration was not found",
    },
    {
      title: "GetObjectLockConfiguration",
      call: (b) => s3.send(new GetObjectLockConfigurationCommand({ Bucket: b })),
      code: "ObjectLockConfigurationNotFoundError",
      message: "Object Lock configuration does not exist for this bucket",
    },
    {
      title: "GetBucketMetricsConfiguration",
      call: (b) => s3.send(new GetBucketMetricsConfigurationCommand({ Bucket: b, Id: "missing" })),
      code: "NoSuchConfiguration",
      message: "The specified configuration does not exist.",
    },
  ];

  for (const c of unset) {
    test(`${c.title} on an unconfigured bucket fails ${c.code} with status 404`, async () => {
      const bucket = await makeBucket();
      const failure = await expectFailure(c.call(bucket), { name: c.code, status: 404, messageIncludes: c.message });
      if (c.code !== "NoSuchConfiguration") expect(failure.error.BucketName).toBe(bucket);
    });

    test(`${c.title} on a missing bucket fails NoSuchBucket`, async () => {
      await expectFailure(c.call(uniq("missing")), { name: "NoSuchBucket", status: 404 });
    });
  }

  test("after DeleteBucketOwnershipControls the Get fails OwnershipControlsNotFoundError", async () => {
    const bucket = await makeBucket();
    await s3.send(new DeleteBucketOwnershipControlsCommand({ Bucket: bucket }));
    await expectFailure(s3.send(new GetBucketOwnershipControlsCommand({ Bucket: bucket })), {
      name: "OwnershipControlsNotFoundError",
      status: 404,
      messageIncludes: "The bucket ownership controls were not found",
    });
  });

  test("after DeletePublicAccessBlock the Get fails NoSuchPublicAccessBlockConfiguration", async () => {
    const bucket = await makeBucket();
    await s3.send(new DeletePublicAccessBlockCommand({ Bucket: bucket }));
    await expectFailure(s3.send(new GetPublicAccessBlockCommand({ Bucket: bucket })), {
      name: "NoSuchPublicAccessBlockConfiguration",
      status: 404,
      messageIncludes: "The public access block configuration was not found",
    });
  });

  test("after DeleteBucketEncryption the bucket is back at the SSE-S3 default, which every bucket always has", async () => {
    const bucket = await makeBucket();
    await s3.send(
      new PutBucketEncryptionCommand({
        Bucket: bucket,
        ServerSideEncryptionConfiguration: { Rules: [{ ApplyServerSideEncryptionByDefault: { SSEAlgorithm: "aws:kms", KMSMasterKeyID: "some-key-id" } }] },
      }),
    );
    const del = await s3.send(new DeleteBucketEncryptionCommand({ Bucket: bucket }));
    expect(del.$metadata.httpStatusCode).toBe(204);
    const rule = (await s3.send(new GetBucketEncryptionCommand({ Bucket: bucket }))).ServerSideEncryptionConfiguration?.Rules?.[0];
    expect(rule?.ApplyServerSideEncryptionByDefault?.SSEAlgorithm).toBe("AES256");
  });

  test("deleting unset subresources succeeds with status 204", async () => {
    const bucket = await makeBucket();
    for (const command of [
      new DeleteBucketPolicyCommand({ Bucket: bucket }),
      new DeleteBucketTaggingCommand({ Bucket: bucket }),
      new DeleteBucketCorsCommand({ Bucket: bucket }),
      new DeleteBucketWebsiteCommand({ Bucket: bucket }),
      new DeleteBucketLifecycleCommand({ Bucket: bucket }),
    ]) {
      const out = await s3.send(command);
      expect(out.$metadata.httpStatusCode).toBe(204);
    }
  });
});

describe("S3 bucket subresources: round trips", () => {
  test("PutBucketPolicy returns 204 and GetBucketPolicy returns the JSON as sent", async () => {
    const bucket = await makeBucket();
    const policy = JSON.stringify({
      Version: "2012-10-17",
      Statement: [
        { Sid: "s", Effect: "Allow", Principal: { AWS: `arn:aws:iam::${defaultAccount}:root` }, Action: "s3:GetObject", Resource: `arn:aws:s3:::${bucket}/*` },
      ],
    });
    const putOut = await s3.send(new PutBucketPolicyCommand({ Bucket: bucket, Policy: policy }));
    expect(putOut.$metadata.httpStatusCode).toBe(204);
    const got = await s3.send(new GetBucketPolicyCommand({ Bucket: bucket }));
    expect(JSON.parse(got.Policy ?? "null")).toEqual(JSON.parse(policy));
    await s3.send(new DeleteBucketPolicyCommand({ Bucket: bucket }));
    await expectFailure(s3.send(new GetBucketPolicyCommand({ Bucket: bucket })), { name: "NoSuchBucketPolicy", status: 404 });
  });

  test("PutBucketPolicy with invalid JSON fails MalformedPolicy", async () => {
    const bucket = await makeBucket();
    await expectFailure(s3.send(new PutBucketPolicyCommand({ Bucket: bucket, Policy: "not json" })), {
      name: "MalformedPolicy",
      status: 400,
      messageIncludes: "Policies must be valid JSON and the first byte must be '{'",
    });
  });

  test("PutBucketPolicy without a Statement fails MalformedPolicy", async () => {
    const bucket = await makeBucket();
    await expectFailure(s3.send(new PutBucketPolicyCommand({ Bucket: bucket, Policy: "{}" })), {
      name: "MalformedPolicy",
      status: 400,
      messageIncludes: "Missing required field Statement",
    });
  });

  test("bucket tagging round trips with status 204 on put and delete", async () => {
    const bucket = await makeBucket();
    const putOut = await s3.send(
      new PutBucketTaggingCommand({
        Bucket: bucket,
        Tagging: {
          TagSet: [
            { Key: "env", Value: "test" },
            { Key: "team", Value: "a" },
          ],
        },
      }),
    );
    expect(putOut.$metadata.httpStatusCode).toBe(204);
    const got = await s3.send(new GetBucketTaggingCommand({ Bucket: bucket }));
    expect(Object.fromEntries((got.TagSet ?? []).map((t) => [t.Key, t.Value]))).toEqual({ env: "test", team: "a" });
    const delOut = await s3.send(new DeleteBucketTaggingCommand({ Bucket: bucket }));
    expect(delOut.$metadata.httpStatusCode).toBe(204);
    await expectFailure(s3.send(new GetBucketTaggingCommand({ Bucket: bucket })), { name: "NoSuchTagSet", status: 404 });
  });

  test("bucket tags set at CreateBucket are returned", async () => {
    const bucket = uniq("b");
    await s3.send(new CreateBucketCommand({ Bucket: bucket, CreateBucketConfiguration: { Tags: [{ Key: "k", Value: "v" }] } }));
    const got = await s3.send(new GetBucketTaggingCommand({ Bucket: bucket }));
    expect(got.TagSet).toEqual([{ Key: "k", Value: "v" }]);
  });

  test("PutBucketTagging rejects duplicate keys with InvalidTag", async () => {
    const bucket = await makeBucket();
    await expectFailure(
      s3.send(
        new PutBucketTaggingCommand({
          Bucket: bucket,
          Tagging: {
            TagSet: [
              { Key: "dup", Value: "1" },
              { Key: "dup", Value: "2" },
            ],
          },
        }),
      ),
      { name: "InvalidTag", status: 400, messageIncludes: "Cannot provide multiple Tags with the same key" },
    );
  });

  test("PutBucketTagging rejects the reserved aws: key prefix with InvalidTag", async () => {
    const bucket = await makeBucket();
    await expectFailure(s3.send(new PutBucketTaggingCommand({ Bucket: bucket, Tagging: { TagSet: [{ Key: "aws:x", Value: "1" }] } })), {
      name: "InvalidTag",
      status: 400,
    });
  });

  test("CORS rules round trip", async () => {
    const bucket = await makeBucket();
    const rules = [
      {
        ID: "r1",
        AllowedMethods: ["GET", "PUT"],
        AllowedOrigins: ["https://example.com"],
        AllowedHeaders: ["*"],
        ExposeHeaders: ["ETag"],
        MaxAgeSeconds: 3000,
      },
    ];
    await s3.send(new PutBucketCorsCommand({ Bucket: bucket, CORSConfiguration: { CORSRules: rules } }));
    const got = await s3.send(new GetBucketCorsCommand({ Bucket: bucket }));
    expect(got.CORSRules).toEqual(rules);
    await s3.send(new DeleteBucketCorsCommand({ Bucket: bucket }));
    await expectFailure(s3.send(new GetBucketCorsCommand({ Bucket: bucket })), { name: "NoSuchCORSConfiguration", status: 404 });
  });

  test("website configuration round trips and delete is idempotent", async () => {
    const bucket = await makeBucket();
    await s3.send(
      new PutBucketWebsiteCommand({
        Bucket: bucket,
        WebsiteConfiguration: { IndexDocument: { Suffix: "index.html" }, ErrorDocument: { Key: "error.html" } },
      }),
    );
    const got = await s3.send(new GetBucketWebsiteCommand({ Bucket: bucket }));
    expect(got.IndexDocument?.Suffix).toBe("index.html");
    expect(got.ErrorDocument?.Key).toBe("error.html");
    await s3.send(new DeleteBucketWebsiteCommand({ Bucket: bucket }));
    await s3.send(new DeleteBucketWebsiteCommand({ Bucket: bucket }));
    await expectFailure(s3.send(new GetBucketWebsiteCommand({ Bucket: bucket })), { name: "NoSuchWebsiteConfiguration", status: 404 });
  });

  test("lifecycle configuration reads back exactly the rule that was put", async () => {
    const bucket = await makeBucket();
    const rules = [
      {
        ID: "expire-logs",
        Status: "Enabled" as const,
        Filter: { Prefix: "logs/" },
        Expiration: { Days: 30 },
        AbortIncompleteMultipartUpload: { DaysAfterInitiation: 7 },
      },
    ];
    await s3.send(new PutBucketLifecycleConfigurationCommand({ Bucket: bucket, LifecycleConfiguration: { Rules: rules } }));
    const got = await s3.send(new GetBucketLifecycleConfigurationCommand({ Bucket: bucket }));
    expect(got.Rules).toEqual(rules);
  });

  test("lifecycle rule without an ID or Filter fails MalformedXML", async () => {
    const bucket = await makeBucket();
    await expectFailure(
      s3.send(
        new PutBucketLifecycleConfigurationCommand({
          Bucket: bucket,
          LifecycleConfiguration: { Rules: [{ Status: "Enabled", Expiration: { Days: 1 } } as never] },
        }),
      ),
      { name: "MalformedXML", status: 400 },
    );
  });

  test("versioning moves from unset to Enabled to Suspended and rejects other values", async () => {
    const bucket = await makeBucket();
    await s3.send(new PutBucketVersioningCommand({ Bucket: bucket, VersioningConfiguration: { Status: "Enabled" } }));
    expect((await s3.send(new GetBucketVersioningCommand({ Bucket: bucket }))).Status).toBe("Enabled");
    await s3.send(new PutBucketVersioningCommand({ Bucket: bucket, VersioningConfiguration: { Status: "Suspended" } }));
    expect((await s3.send(new GetBucketVersioningCommand({ Bucket: bucket }))).Status).toBe("Suspended");
    await expectFailure(
      s3.send(new PutBucketVersioningCommand({ Bucket: bucket, VersioningConfiguration: { Status: "enabled" as never } })),
      { name: "MalformedXML", status: 400 },
    );
  });

  test("PutBucketVersioning with an empty configuration fails IllegalVersioningConfigurationException", async () => {
    const bucket = await makeBucket();
    await expectFailure(s3.send(new PutBucketVersioningCommand({ Bucket: bucket, VersioningConfiguration: {} })), {
      name: "IllegalVersioningConfigurationException",
      status: 400,
      messageIncludes: "The Versioning element must be specified",
    });
  });

  test("default encryption with aws:kms round trips and rejects KMSMasterKeyID with AES256", async () => {
    const bucket = await makeBucket();
    await s3.send(
      new PutBucketEncryptionCommand({
        Bucket: bucket,
        ServerSideEncryptionConfiguration: {
          Rules: [{ ApplyServerSideEncryptionByDefault: { SSEAlgorithm: "aws:kms", KMSMasterKeyID: "some-key-id" }, BucketKeyEnabled: true }],
        },
      }),
    );
    const rule = (await s3.send(new GetBucketEncryptionCommand({ Bucket: bucket }))).ServerSideEncryptionConfiguration?.Rules?.[0];
    expect(rule?.ApplyServerSideEncryptionByDefault).toMatchObject({ SSEAlgorithm: "aws:kms", KMSMasterKeyID: "some-key-id" });
    expect(rule?.BucketKeyEnabled).toBe(true);
    await expectFailure(
      s3.send(
        new PutBucketEncryptionCommand({
          Bucket: bucket,
          ServerSideEncryptionConfiguration: { Rules: [{ ApplyServerSideEncryptionByDefault: { SSEAlgorithm: "AES256", KMSMasterKeyID: "k" } }] },
        }),
      ),
      { name: "InvalidArgument", status: 400 },
    );
  });

  test("ownership controls and public access block round trip", async () => {
    const bucket = await makeBucket();
    await s3.send(
      new PutBucketOwnershipControlsCommand({ Bucket: bucket, OwnershipControls: { Rules: [{ ObjectOwnership: "BucketOwnerPreferred" }] } }),
    );
    expect((await s3.send(new GetBucketOwnershipControlsCommand({ Bucket: bucket }))).OwnershipControls?.Rules).toEqual([
      { ObjectOwnership: "BucketOwnerPreferred" },
    ]);
    await s3.send(
      new PutPublicAccessBlockCommand({
        Bucket: bucket,
        PublicAccessBlockConfiguration: { BlockPublicAcls: false, BlockPublicPolicy: false, IgnorePublicAcls: false, RestrictPublicBuckets: false },
      }),
    );
    expect((await s3.send(new GetPublicAccessBlockCommand({ Bucket: bucket }))).PublicAccessBlockConfiguration).toEqual({
      BlockPublicAcls: false,
      BlockPublicPolicy: false,
      IgnorePublicAcls: false,
      RestrictPublicBuckets: false,
    });
  });

  test("accelerate and request payment configurations round trip", async () => {
    const bucket = await makeBucket();
    await s3.send(new PutBucketAccelerateConfigurationCommand({ Bucket: bucket, AccelerateConfiguration: { Status: "Enabled" } }));
    expect((await s3.send(new GetBucketAccelerateConfigurationCommand({ Bucket: bucket }))).Status).toBe("Enabled");
    await s3.send(new PutBucketRequestPaymentCommand({ Bucket: bucket, RequestPaymentConfiguration: { Payer: "Requester" } }));
    expect((await s3.send(new GetBucketRequestPaymentCommand({ Bucket: bucket }))).Payer).toBe("Requester");
  });

  test("object lock enabled at CreateBucket enables versioning and returns an Enabled configuration", async () => {
    const bucket = await makeBucket({ objectLock: true });
    expect((await s3.send(new GetObjectLockConfigurationCommand({ Bucket: bucket }))).ObjectLockConfiguration?.ObjectLockEnabled).toBe("Enabled");
    expect((await s3.send(new GetBucketVersioningCommand({ Bucket: bucket }))).Status).toBe("Enabled");
  });
});

describe("S3 objects: put, get, head, delete", () => {
  test("PutObject returns a quoted MD5 ETag and GetObject returns the body", async () => {
    const bucket = await makeBucket();
    const out = await put(bucket, "hello.txt", "hello");
    expect(out.ETag).toBe('"5d41402abc4b2a76b9719d911017c592"');
    expect(await bodyText((await s3.send(new GetObjectCommand({ Bucket: bucket, Key: "hello.txt" }))).Body)).toBe("hello");
  });

  test("PutObject always reports ServerSideEncryption AES256 when no encryption was asked for", async () => {
    const bucket = await makeBucket();
    const out = await put(bucket, "k", "v");
    expect(out.ServerSideEncryption).toBe("AES256");
    const head = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "k" }));
    expect(head.ServerSideEncryption).toBe("AES256");
  });

  test("HeadObject returns size, ETag, Last-Modified at second precision and Accept-Ranges", async () => {
    const bucket = await makeBucket();
    const body = text("0123456789");
    await put(bucket, "k", body);
    const head = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "k" }));
    expect(head.ContentLength).toBe(10);
    expect(head.ETag).toBe(`"${md5Hex(body)}"`);
    expect(head.AcceptRanges).toBe("bytes");
    expect(head.LastModified?.getMilliseconds()).toBe(0);
  });

  test("the default Content-Type is binary/octet-stream", async () => {
    const bucket = await makeBucket();
    // The JS SDK sends Content-Type: application/octet-stream for a string
    // body (observed on the wire), which S3 stores as sent; drop it before
    // signing so the request carries none and S3's default applies
    // (s3.md 5.4).
    const command = new PutObjectCommand({ Bucket: bucket, Key: "k", Body: "v" });
    command.middlewareStack.add(
      (next) => async (args) => {
        const request = args.request as { headers?: Record<string, string> };
        if (request.headers) delete request.headers["content-type"];
        return next(args);
      },
      { step: "build", name: "dropContentType" },
    );
    await s3.send(command);
    const head = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "k" }));
    expect(head.ContentType).toBe("binary/octet-stream");
  });

  test("system metadata headers are stored and returned", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "v", {
      ContentType: "text/plain",
      CacheControl: "max-age=60",
      ContentDisposition: 'attachment; filename="a.txt"',
      ContentEncoding: "gzip",
      ContentLanguage: "en",
      Expires: new Date("2030-01-01T00:00:00Z"),
    });
    const head = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "k" }));
    expect(head).toMatchObject({
      ContentType: "text/plain",
      CacheControl: "max-age=60",
      ContentDisposition: 'attachment; filename="a.txt"',
      ContentEncoding: "gzip",
      ContentLanguage: "en",
    });
    expect(head.ExpiresString).toBe("Tue, 01 Jan 2030 00:00:00 GMT");
  });

  test("Content-Encoding gzip is stored and the body is not decoded", async () => {
    const bucket = await makeBucket();
    const raw = text("not actually gzip");
    await put(bucket, "k", raw, { ContentEncoding: "gzip" });
    expect(Array.from(await read(bucket, "k"))).toEqual(Array.from(raw));
  });

  test("user metadata is lower-cased, returned and not merged on overwrite", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "v", { Metadata: { Owner: "alice", "Mixed-Case": "x", under_score: "1" } });
    expect((await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "k" }))).Metadata).toEqual({
      owner: "alice",
      "mixed-case": "x",
      under_score: "1",
    });
    await put(bucket, "k", "v2", { Metadata: { other: "2" } });
    expect((await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "k" }))).Metadata).toEqual({ other: "2" });
  });

  test("response-header overrides apply to a GetObject", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "v", { ContentType: "text/plain" });
    const out = await s3.send(
      new GetObjectCommand({
        Bucket: bucket,
        Key: "k",
        ResponseContentType: "application/json",
        ResponseContentDisposition: "inline; filename=x.json",
        ResponseCacheControl: "no-store",
      }),
    );
    expect(out).toMatchObject({ ContentType: "application/json", ContentDisposition: "inline; filename=x.json", CacheControl: "no-store" });
  });

  test("GetObject of a missing key fails NoSuchKey with the Key element", async () => {
    const bucket = await makeBucket();
    const failure = await expectFailure(s3.send(new GetObjectCommand({ Bucket: bucket, Key: "nope" })), {
      name: "NoSuchKey",
      status: 404,
      messageIncludes: "The specified key does not exist.",
    });
    expect(failure.error.Key).toBe("nope");
  });

  test("HeadObject of a missing key fails NotFound with status 404", async () => {
    const bucket = await makeBucket();
    await expectFailure(s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "nope" })), { name: "NotFound", status: 404 });
  });

  test("keys are case-sensitive", async () => {
    const bucket = await makeBucket();
    await put(bucket, "Key", "v");
    await expectFailure(s3.send(new GetObjectCommand({ Bucket: bucket, Key: "key" })), { name: "NoSuchKey", status: 404 });
  });

  const specialKeys = ["file%2Fname", "test@key/", "test%123", "test key//", "a/%F0%9F%98%80/", "emoji-\u{1F600}", "plus+sign", "a//b", "/leading", "dots/../x", "tilde~and!bang*star'quote(paren)", "中文/é"];
  for (const key of specialKeys) {
    test(`an object with the key ${JSON.stringify(key)} round trips`, async () => {
      const bucket = await makeBucket();
      await put(bucket, key, `body of ${key}`);
      expect(new TextDecoder().decode(await read(bucket, key))).toBe(`body of ${key}`);
      expect(await keysOf(bucket)).toEqual([key]);
    });
  }

  test("a key of 1025 bytes fails KeyTooLongError", async () => {
    const bucket = await makeBucket();
    await expectFailure(put(bucket, "k".repeat(1025), "v"), { name: "KeyTooLongError", status: 400 });
    await put(bucket, "k".repeat(1024), "v");
  });

  test("overwriting an object replaces its body and clears its tags", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "one", { Tagging: "a=1" });
    await put(bucket, "k", "two");
    expect(new TextDecoder().decode(await read(bucket, "k"))).toBe("two");
    expect((await s3.send(new GetObjectTaggingCommand({ Bucket: bucket, Key: "k" }))).TagSet).toEqual([]);
  });

  test("DeleteObject returns 204 for existing and missing keys alike", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "v");
    expect((await s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: "k" }))).$metadata.httpStatusCode).toBe(204);
    expect((await s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: "k" }))).$metadata.httpStatusCode).toBe(204);
    await expectFailure(s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k" })), { name: "NoSuchKey", status: 404 });
  });

  test("DeleteObject with a version id other than null on an unversioned bucket fails InvalidArgument", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "v");
    await expectFailure(s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: "k", VersionId: "abc" })), {
      name: "InvalidArgument",
      status: 400,
      messageIncludes: "Invalid version id specified",
    });
  });

  test("GetObject with VersionId null on an unversioned bucket returns the object", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "v");
    const out = await s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k", VersionId: "null" }));
    expect(await bodyText(out.Body)).toBe("v");
  });

  test("DeleteObjects deletes many keys, reports missing keys as Deleted, and Quiet hides them", async () => {
    const bucket = await makeBucket();
    await put(bucket, "a", "1");
    await put(bucket, "b", "2");
    const out = await s3.send(
      new DeleteObjectsCommand({ Bucket: bucket, Delete: { Objects: [{ Key: "a" }, { Key: "b" }, { Key: "never" }] } }),
    );
    expect(out.Deleted?.map((d) => d.Key).sort()).toEqual(["a", "b", "never"]);
    expect(out.Errors ?? []).toEqual([]);
    expect(await keysOf(bucket)).toEqual([]);
    const quiet = await s3.send(new DeleteObjectsCommand({ Bucket: bucket, Delete: { Quiet: true, Objects: [{ Key: "zzz" }] } }));
    expect(quiet.Deleted ?? []).toEqual([]);
    expect(quiet.Errors ?? []).toEqual([]);
  });

  test("DeleteObjects on a missing bucket fails NoSuchBucket", async () => {
    await expectFailure(s3.send(new DeleteObjectsCommand({ Bucket: uniq("missing"), Delete: { Objects: [{ Key: "a" }] } })), {
      name: "NoSuchBucket",
      status: 404,
    });
  });

  test("a body of several megabytes round trips byte for byte", async () => {
    const bucket = await makeBucket();
    const body = patternBytes(3 * MIB + 17);
    await put(bucket, "big", body);
    expect(Buffer.from(await read(bucket, "big")).equals(Buffer.from(body))).toBe(true);
  });

  test("an empty object round trips and a ranged read of it fails InvalidRange", async () => {
    const bucket = await makeBucket();
    await put(bucket, "empty", "");
    const head = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "empty" }));
    expect(head.ContentLength).toBe(0);
    expect(head.ETag).toBe('"d41d8cd98f00b204e9800998ecf8427e"');
    await expectFailure(s3.send(new GetObjectCommand({ Bucket: bucket, Key: "empty", Range: "bytes=0-0" })), { name: "InvalidRange", status: 416 });
  });

  test("a folder-style key ending in / is an ordinary object", async () => {
    const bucket = await makeBucket();
    await put(bucket, "folder/", "");
    expect(await keysOf(bucket)).toEqual(["folder/"]);
  });

  test("Content-MD5 matching the body is accepted", async () => {
    const bucket = await makeBucket();
    const body = "md5 body";
    const md5 = Buffer.from(md5Hex(body), "hex").toString("base64");
    const out = await put(bucket, "k", body, { ContentMD5: md5 });
    expect(out.ETag).toBe(`"${md5Hex(body)}"`);
  });

  test("Content-MD5 that is not a valid digest fails InvalidDigest", async () => {
    const bucket = await makeBucket();
    await expectFailure(put(bucket, "k", "v", { ContentMD5: "__invalid__" }), { name: "InvalidDigest", status: 400 });
  });

  test("Content-MD5 of different content fails BadDigest", async () => {
    const bucket = await makeBucket();
    const wrong = Buffer.from(md5Hex("other"), "hex").toString("base64");
    await expectFailure(put(bucket, "k", "v", { ContentMD5: wrong }), { name: "BadDigest", status: 400 });
  });

  test("an invalid storage class fails InvalidStorageClass", async () => {
    const bucket = await makeBucket();
    await expectFailure(put(bucket, "k", "v", { StorageClass: "OUTPOSTS" }), { name: "InvalidStorageClass", status: 400 });
  });

  test("HeadObject omits the storage class for STANDARD and reports others", async () => {
    const bucket = await makeBucket();
    await put(bucket, "std", "v");
    await put(bucket, "ia", "v", { StorageClass: "STANDARD_IA" });
    expect((await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "std" }))).StorageClass).toBeUndefined();
    expect((await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "ia" }))).StorageClass).toBe("STANDARD_IA");
  });

  test("GetObject of a GLACIER object fails InvalidObjectState but HeadObject works", async () => {
    const bucket = await makeBucket();
    await put(bucket, "cold", "v", { StorageClass: "GLACIER" });
    expect((await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "cold" }))).StorageClass).toBe("GLACIER");
    await expectFailure(s3.send(new GetObjectCommand({ Bucket: bucket, Key: "cold" })), { name: "InvalidObjectState", status: 403 });
  });

  test("PutObject in a missing bucket fails NoSuchBucket", async () => {
    await expectFailure(put(uniq("missing"), "k", "v"), { name: "NoSuchBucket", status: 404 });
  });
});

describe("S3 range requests", () => {
  const body = "0123456789abcdefghij";

  async function ranged(range: string) {
    const bucket = await makeBucket();
    await put(bucket, "k", body);
    return s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k", Range: range }));
  }

  test("bytes=a-b returns 206 with Content-Range and the slice", async () => {
    const out = await ranged("bytes=2-5");
    expect(out.$metadata.httpStatusCode).toBe(206);
    expect(out.ContentRange).toBe("bytes 2-5/20");
    expect(out.ContentLength).toBe(4);
    expect(await bodyText(out.Body)).toBe("2345");
  });

  test("bytes=a- returns the tail", async () => {
    const out = await ranged("bytes=15-");
    expect(out.ContentRange).toBe("bytes 15-19/20");
    expect(await bodyText(out.Body)).toBe("fghij");
  });

  test("bytes=-n returns the last n bytes", async () => {
    const out = await ranged("bytes=-3");
    expect(out.ContentRange).toBe("bytes 17-19/20");
    expect(await bodyText(out.Body)).toBe("hij");
  });

  test("an end past the object is clipped", async () => {
    const out = await ranged("bytes=18-100");
    expect(out.ContentRange).toBe("bytes 18-19/20");
    expect(await bodyText(out.Body)).toBe("ij");
  });

  test("a start at or past the size fails InvalidRange with status 416 and the sizes", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "12345678");
    const failure = await expectFailure(s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k", Range: "bytes=1024-4096" })), {
      name: "InvalidRange",
      status: 416,
      messageIncludes: "The requested range is not satisfiable",
    });
    expect(String(failure.error.ActualObjectSize)).toBe("8");
    expect(failure.error.RangeRequested).toBe("bytes=1024-4096");
  });

  test("a range read of a missing key fails NoSuchKey", async () => {
    const bucket = await makeBucket();
    await expectFailure(s3.send(new GetObjectCommand({ Bucket: bucket, Key: "nope", Range: "bytes=0-1" })), { name: "NoSuchKey", status: 404 });
  });

  test("HeadObject with a Range returns the range headers", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", body);
    const head = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "k", Range: "bytes=0-4" }));
    expect(head.ContentLength).toBe(5);
    expect(head.ContentRange).toBe("bytes 0-4/20");
  });
});

describe("S3 conditional requests", () => {
  async function seeded() {
    const bucket = await makeBucket();
    const out = await put(bucket, "k", "conditional");
    const head = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "k" }));
    return { bucket, etag: out.ETag ?? "", lastModified: head.LastModified ?? new Date() };
  }

  test("If-Match with the ETag returns 200 and with another value fails PreconditionFailed", async () => {
    const { bucket, etag } = await seeded();
    expect((await s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k", IfMatch: etag }))).$metadata.httpStatusCode).toBe(200);
    const failure = await expectFailure(s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k", IfMatch: '"deadbeef"' })), {
      name: "PreconditionFailed",
      status: 412,
      messageIncludes: "At least one of the pre-conditions you specified did not hold",
    });
    expect(failure.error.Condition).toBe("If-Match");
  });

  test("If-None-Match with the ETag returns 304, with or without quotes", async () => {
    const { bucket, etag } = await seeded();
    for (const value of [etag, etag.replaceAll('"', "")]) {
      const failure = await awsFailure(s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k", IfNoneMatch: value })));
      expect(failure.status).toBe(304);
    }
    expect((await s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k", IfNoneMatch: '"other"' }))).$metadata.httpStatusCode).toBe(200);
  });

  test("If-Modified-Since at or after Last-Modified returns 304 and a future date is ignored", async () => {
    const { bucket, lastModified } = await seeded();
    const failure = await awsFailure(s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k", IfModifiedSince: lastModified })));
    expect(failure.status).toBe(304);
    const earlier = new Date(lastModified.getTime() - 60_000);
    expect((await s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k", IfModifiedSince: earlier }))).$metadata.httpStatusCode).toBe(200);
    const future = new Date(Date.now() + 3_600_000);
    expect((await s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k", IfModifiedSince: future }))).$metadata.httpStatusCode).toBe(200);
  });

  test("If-Unmodified-Since earlier than Last-Modified fails PreconditionFailed and an equal date passes", async () => {
    const { bucket, lastModified } = await seeded();
    expect((await s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k", IfUnmodifiedSince: lastModified }))).$metadata.httpStatusCode).toBe(200);
    const earlier = new Date(lastModified.getTime() - 60_000);
    const failure = await expectFailure(s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k", IfUnmodifiedSince: earlier })), {
      name: "PreconditionFailed",
      status: 412,
    });
    expect(failure.error.Condition).toBe("If-Unmodified-Since");
  });

  test("HeadObject answers a failed precondition with a bare 412", async () => {
    const { bucket } = await seeded();
    const failure = await awsFailure(s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "k", IfMatch: '"deadbeef"' })));
    expect(failure.status).toBe(412);
  });

  test("If-Match true with If-Unmodified-Since false still returns the object", async () => {
    const { bucket, etag, lastModified } = await seeded();
    const out = await s3.send(
      new GetObjectCommand({ Bucket: bucket, Key: "k", IfMatch: etag, IfUnmodifiedSince: new Date(lastModified.getTime() - 60_000) }),
    );
    expect(out.$metadata.httpStatusCode).toBe(200);
  });

  test("PutObject with If-None-Match * succeeds for a new key and fails PreconditionFailed for an existing one", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "first", { IfNoneMatch: "*" });
    const failure = await expectFailure(put(bucket, "k", "second", { IfNoneMatch: "*" }), { name: "PreconditionFailed", status: 412 });
    expect(failure.error.Condition).toBe("If-None-Match");
    expect(new TextDecoder().decode(await read(bucket, "k"))).toBe("first");
  });

  test("PutObject with If-Match checks the current ETag and a missing key fails NoSuchKey", async () => {
    const bucket = await makeBucket();
    await expectFailure(put(bucket, "absent", "v", { IfMatch: '"abc"' }), { name: "NoSuchKey", status: 404 });
    const first = await put(bucket, "k", "one");
    await put(bucket, "k", "two", { IfMatch: first.ETag });
    const failure = await expectFailure(put(bucket, "k", "three", { IfMatch: first.ETag }), { name: "PreconditionFailed", status: 412 });
    expect(failure.error.Condition).toBe("If-Match");
  });

  test("PutObject with an If-None-Match value other than * fails NotImplemented", async () => {
    const bucket = await makeBucket();
    await expectFailure(put(bucket, "k", "v", { IfNoneMatch: '"abc"' }), { name: "NotImplemented", status: 501 });
  });

  test("CopyObject with a failing copy-source condition fails PreconditionFailed", async () => {
    const { bucket } = await seeded();
    const failure = await expectFailure(
      s3.send(new CopyObjectCommand({ Bucket: bucket, Key: "dst", CopySource: `${bucket}/k`, CopySourceIfMatch: '"deadbeef"' })),
      { name: "PreconditionFailed", status: 412 },
    );
    expect(failure.error.Condition).toBe("x-amz-copy-source-If-Match");
  });
});

describe("S3 object tagging", () => {
  test("tags sent in the x-amz-tagging header are returned by GetObjectTagging and counted on HeadObject", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "v", { Tagging: "a=1&b=two" });
    const tags = await s3.send(new GetObjectTaggingCommand({ Bucket: bucket, Key: "k" }));
    expect(Object.fromEntries((tags.TagSet ?? []).map((t) => [t.Key, t.Value]))).toEqual({ a: "1", b: "two" });
    const get = await s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k" }));
    expect(get.TagCount).toBe(2);
  });

  test("PutObjectTagging replaces the set, DeleteObjectTagging returns 204 and clears it", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "v");
    await s3.send(new PutObjectTaggingCommand({ Bucket: bucket, Key: "k", Tagging: { TagSet: [{ Key: "x", Value: "1" }] } }));
    expect((await s3.send(new GetObjectTaggingCommand({ Bucket: bucket, Key: "k" }))).TagSet).toEqual([{ Key: "x", Value: "1" }]);
    const del = await s3.send(new DeleteObjectTaggingCommand({ Bucket: bucket, Key: "k" }));
    expect(del.$metadata.httpStatusCode).toBe(204);
    expect((await s3.send(new GetObjectTaggingCommand({ Bucket: bucket, Key: "k" }))).TagSet).toEqual([]);
  });

  test("tagging changes keep the ETag and Last-Modified", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "v");
    const before = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "k" }));
    await sleep(1100);
    await s3.send(new PutObjectTaggingCommand({ Bucket: bucket, Key: "k", Tagging: { TagSet: [{ Key: "x", Value: "1" }] } }));
    const after = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "k" }));
    expect(after.ETag).toBe(before.ETag);
    expect(after.LastModified?.getTime()).toBe(before.LastModified?.getTime());
  });

  test("a malformed x-amz-tagging header fails InvalidArgument", async () => {
    const bucket = await makeBucket();
    await expectFailure(put(bucket, "k", "v", { Tagging: "a=1&a=2" }), { name: "InvalidArgument", status: 400 });
  });

  test("GetObjectTagging on a missing key fails NoSuchKey", async () => {
    const bucket = await makeBucket();
    await expectFailure(s3.send(new GetObjectTaggingCommand({ Bucket: bucket, Key: "nope" })), { name: "NoSuchKey", status: 404 });
  });
});

describe("S3 CopyObject", () => {
  test("copies the body and metadata, with a new ETag equal to the content MD5", async () => {
    const bucket = await makeBucket();
    await put(bucket, "src", "copy me", { ContentType: "text/plain", Metadata: { a: "1" } });
    const out = await s3.send(new CopyObjectCommand({ Bucket: bucket, Key: "dst", CopySource: `${bucket}/src` }));
    expect(out.CopyObjectResult?.ETag).toBe(`"${md5Hex("copy me")}"`);
    expect(out.CopyObjectResult?.LastModified).toBeInstanceOf(Date);
    const head = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "dst" }));
    expect(head).toMatchObject({ ContentType: "text/plain", Metadata: { a: "1" } });
    expect(new TextDecoder().decode(await read(bucket, "dst"))).toBe("copy me");
  });

  test("works across buckets", async () => {
    const a = await makeBucket();
    const b = await makeBucket();
    await put(a, "src", "across");
    await s3.send(new CopyObjectCommand({ Bucket: b, Key: "dst", CopySource: `${a}/src` }));
    expect(new TextDecoder().decode(await read(b, "dst"))).toBe("across");
  });

  test("REPLACE takes metadata only from the request", async () => {
    const bucket = await makeBucket();
    await put(bucket, "src", "v", { ContentType: "text/plain", Metadata: { a: "1" }, ContentLanguage: "en" });
    await s3.send(
      new CopyObjectCommand({
        Bucket: bucket,
        Key: "dst",
        CopySource: `${bucket}/src`,
        MetadataDirective: "REPLACE",
        Metadata: { b: "2" },
      }),
    );
    const head = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "dst" }));
    expect(head.Metadata).toEqual({ b: "2" });
    expect(head.ContentType).toBe("binary/octet-stream");
    expect(head.ContentLanguage).toBeUndefined();
  });

  test("COPY ignores metadata headers sent with the request", async () => {
    const bucket = await makeBucket();
    await put(bucket, "src", "v", { Metadata: { a: "1" } });
    await s3.send(new CopyObjectCommand({ Bucket: bucket, Key: "dst", CopySource: `${bucket}/src`, Metadata: { b: "2" } }));
    expect((await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "dst" }))).Metadata).toEqual({ a: "1" });
  });

  test("tagging directive COPY carries the source tags and REPLACE takes the request tags", async () => {
    const bucket = await makeBucket();
    await put(bucket, "src", "v", { Tagging: "key1=value1" });
    await s3.send(new CopyObjectCommand({ Bucket: bucket, Key: "copied", CopySource: `${bucket}/src`, Tagging: "ignored=1" }));
    expect((await s3.send(new GetObjectTaggingCommand({ Bucket: bucket, Key: "copied" }))).TagSet).toEqual([{ Key: "key1", Value: "value1" }]);
    await s3.send(
      new CopyObjectCommand({ Bucket: bucket, Key: "replaced", CopySource: `${bucket}/src`, TaggingDirective: "REPLACE", Tagging: "key2=value2" }),
    );
    expect((await s3.send(new GetObjectTaggingCommand({ Bucket: bucket, Key: "replaced" }))).TagSet).toEqual([{ Key: "key2", Value: "value2" }]);
  });

  test("copying an object onto itself with nothing changed fails InvalidRequest", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "v");
    await expectFailure(s3.send(new CopyObjectCommand({ Bucket: bucket, Key: "k", CopySource: `${bucket}/k` })), {
      name: "InvalidRequest",
      status: 400,
      messageIncludes: "This copy request is illegal because it is trying to copy an object to itself",
    });
  });

  test("copying onto itself with MetadataDirective REPLACE succeeds", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "v");
    await s3.send(
      new CopyObjectCommand({ Bucket: bucket, Key: "k", CopySource: `${bucket}/k`, MetadataDirective: "REPLACE", Metadata: { n: "1" } }),
    );
    expect((await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "k" }))).Metadata).toEqual({ n: "1" });
  });

  test("a missing source key fails NoSuchKey and a missing source bucket fails NoSuchBucket", async () => {
    const bucket = await makeBucket();
    await expectFailure(s3.send(new CopyObjectCommand({ Bucket: bucket, Key: "dst", CopySource: `${bucket}/nope` })), { name: "NoSuchKey", status: 404 });
    await expectFailure(s3.send(new CopyObjectCommand({ Bucket: bucket, Key: "dst", CopySource: `${uniq("missing")}/k` })), {
      name: "NoSuchBucket",
      status: 404,
    });
  });

  test("a copy source that is not bucket/key fails InvalidArgument", async () => {
    const bucket = await makeBucket();
    await expectFailure(s3.send(new CopyObjectCommand({ Bucket: bucket, Key: "dst", CopySource: "nobucketslash" })), {
      name: "InvalidArgument",
      status: 400,
      messageIncludes: "Invalid copy source object key",
    });
  });

  test("source keys with special characters copy correctly", async () => {
    const bucket = await makeBucket();
    const key = "dir/with space+plus@at/é";
    await put(bucket, key, "special");
    await s3.send(
      new CopyObjectCommand({
        Bucket: bucket,
        Key: "dst",
        CopySource: `${bucket}/${key.split("/").map(encodeURIComponent).join("/")}`,
      }),
    );
    expect(new TextDecoder().decode(await read(bucket, "dst"))).toBe("special");
  });
});

describe("S3 listing", () => {
  async function filled(keys: string[]): Promise<string> {
    const bucket = await makeBucket();
    for (const key of keys) await put(bucket, key, key);
    return bucket;
  }

  test("ListObjectsV2 returns keys in UTF-8 byte order with size, ETag, StorageClass and KeyCount", async () => {
    const bucket = await filled(["test_2", "test_10", "test_1", "test_11", "test_0"]);
    const out = await s3.send(new ListObjectsV2Command({ Bucket: bucket }));
    expect(out.Contents?.map((o) => o.Key)).toEqual(["test_0", "test_1", "test_10", "test_11", "test_2"]);
    expect(out.KeyCount).toBe(5);
    expect(out.IsTruncated).toBe(false);
    expect(out.MaxKeys).toBe(1000);
    expect(out.Contents?.[0]).toMatchObject({ Size: "test_0".length, StorageClass: "STANDARD", ETag: `"${md5Hex("test_0")}"` });
    expect(out.Name).toBe(bucket);
  });

  test("ListObjectsV2 on an empty bucket has no Contents and KeyCount 0", async () => {
    const bucket = await makeBucket();
    const out = await s3.send(new ListObjectsV2Command({ Bucket: bucket }));
    expect(out.Contents).toBeUndefined();
    expect(out.KeyCount).toBe(0);
    expect(out.IsTruncated).toBe(false);
  });

  test("a prefix with a delimiter collapses keys into CommonPrefixes", async () => {
    const bucket = await filled(["a/1", "a/2", "b/1", "c", "a/sub/3"]);
    const out = await s3.send(new ListObjectsV2Command({ Bucket: bucket, Delimiter: "/" }));
    expect(out.Contents?.map((o) => o.Key)).toEqual(["c"]);
    expect(out.CommonPrefixes?.map((p) => p.Prefix)).toEqual(["a/", "b/"]);
    const inner = await s3.send(new ListObjectsV2Command({ Bucket: bucket, Prefix: "a/", Delimiter: "/" }));
    expect(inner.Contents?.map((o) => o.Key)).toEqual(["a/1", "a/2"]);
    expect(inner.CommonPrefixes?.map((p) => p.Prefix)).toEqual(["a/sub/"]);
  });

  test("a prefix without a trailing slash still groups at the next delimiter", async () => {
    const bucket = await filled(["test/foo/1", "test/foo/2", "test/bar"]);
    const out = await s3.send(new ListObjectsV2Command({ Bucket: bucket, Prefix: "test/foo", Delimiter: "/" }));
    expect(out.CommonPrefixes?.map((p) => p.Prefix)).toEqual(["test/foo/"]);
  });

  test("max-keys truncates and the continuation token resumes without repeats", async () => {
    const keys = ["k1", "k2", "k3", "k4", "k5"];
    const bucket = await filled(keys);
    const seen: string[] = [];
    let token: string | undefined;
    let pages = 0;
    do {
      const out = await s3.send(new ListObjectsV2Command({ Bucket: bucket, MaxKeys: 2, ContinuationToken: token }));
      expect(out.Contents?.length ?? 0).toBeLessThanOrEqual(2);
      seen.push(...(out.Contents ?? []).map((o) => o.Key ?? ""));
      if (out.IsTruncated) expect(out.NextContinuationToken).toBeDefined();
      token = out.NextContinuationToken;
      pages += 1;
    } while (token !== undefined && pages < 10);
    expect(seen).toEqual(keys);
    expect(pages).toBe(3);
  });

  test("each common prefix counts as one key toward max-keys", async () => {
    const bucket = await filled(["a/1", "a/2", "b/1", "c/1"]);
    const out = await s3.send(new ListObjectsV2Command({ Bucket: bucket, Delimiter: "/", MaxKeys: 2 }));
    expect(out.CommonPrefixes?.map((p) => p.Prefix)).toEqual(["a/", "b/"]);
    expect(out.IsTruncated).toBe(true);
    expect(out.KeyCount).toBe(2);
  });

  test("start-after lists only keys after it", async () => {
    const bucket = await filled(["a", "b", "c", "d"]);
    const out = await s3.send(new ListObjectsV2Command({ Bucket: bucket, StartAfter: "b" }));
    expect(out.Contents?.map((o) => o.Key)).toEqual(["c", "d"]);
    expect(out.StartAfter).toBe("b");
  });

  test("max-keys 0 returns an empty page", async () => {
    const bucket = await filled(["a"]);
    const out = await s3.send(new ListObjectsV2Command({ Bucket: bucket, MaxKeys: 0 }));
    expect(out.Contents).toBeUndefined();
    expect(out.IsTruncated).toBe(false);
  });

  test("Owner appears on v2 entries only with fetch-owner", async () => {
    const bucket = await filled(["a"]);
    expect((await s3.send(new ListObjectsV2Command({ Bucket: bucket }))).Contents?.[0]?.Owner).toBeUndefined();
    expect((await s3.send(new ListObjectsV2Command({ Bucket: bucket, FetchOwner: true }))).Contents?.[0]?.Owner?.ID).toBeDefined();
  });

  test("encoding-type url encodes keys and prefixes in the response", async () => {
    const bucket = await filled(["a b+c", "foo/bar baz"]);
    const out = await s3.send(new ListObjectsV2Command({ Bucket: bucket, EncodingType: "url", Delimiter: "/" }));
    expect(out.EncodingType).toBe("url");
    // A space may be + or %20; a literal + must be %2B because clients decode with unquote_plus.
    expect(out.Contents?.map((o) => o.Key)).toEqual([expect.stringMatching(/^a(\+|%20)b%2Bc$/)]);
    expect(out.CommonPrefixes?.map((p) => p.Prefix)).toEqual(["foo/"]);
    expect(out.Delimiter).toBe("/");
  });

  test("an encoding-type other than url fails InvalidArgument", async () => {
    const bucket = await makeBucket();
    await expectFailure(s3.send(new ListObjectsV2Command({ Bucket: bucket, EncodingType: "bogus" as never })), {
      name: "InvalidArgument",
      status: 400,
      messageIncludes: "Invalid Encoding Method specified in Request",
    });
  });

  test("a made-up continuation token fails InvalidArgument", async () => {
    const bucket = await filled(["a"]);
    await expectFailure(s3.send(new ListObjectsV2Command({ Bucket: bucket, ContinuationToken: "not-a-real-token" })), {
      name: "InvalidArgument",
      status: 400,
    });
  });

  test("ListObjects v1 returns Marker, Owner and NextMarker only when a delimiter is sent", async () => {
    const bucket = await filled(["a/1", "b/1", "c/1"]);
    const plain = await s3.send(new ListObjectsCommand({ Bucket: bucket, MaxKeys: 2 }));
    expect(plain.IsTruncated).toBe(true);
    expect(plain.Contents?.map((o) => o.Key)).toEqual(["a/1", "b/1"]);
    expect(plain.Contents?.[0]?.Owner?.ID).toBeDefined();
    expect(plain.NextMarker).toBeUndefined();
    const next = await s3.send(new ListObjectsCommand({ Bucket: bucket, Marker: "b/1" }));
    expect(next.Contents?.map((o) => o.Key)).toEqual(["c/1"]);
    const delimited = await s3.send(new ListObjectsCommand({ Bucket: bucket, MaxKeys: 1, Delimiter: "/" }));
    expect(delimited.IsTruncated).toBe(true);
    expect(delimited.NextMarker).toBe("a/");
  });

  test("an in-progress multipart upload is not listed as an object", async () => {
    const bucket = await makeBucket();
    await s3.send(new CreateMultipartUploadCommand({ Bucket: bucket, Key: "pending" }));
    expect(await keysOf(bucket)).toEqual([]);
  });
});

describe("S3 versioning", () => {
  async function versionedBucket(): Promise<string> {
    const bucket = await makeBucket();
    await s3.send(new PutBucketVersioningCommand({ Bucket: bucket, VersioningConfiguration: { Status: "Enabled" } }));
    return bucket;
  }

  test("each write on a versioned bucket returns a new 32-character version id", async () => {
    const bucket = await versionedBucket();
    const one = await put(bucket, "k", "one");
    const two = await put(bucket, "k", "two");
    expect(one.VersionId).toMatch(/^[A-Za-z0-9._]{32}$/);
    expect(two.VersionId).toMatch(/^[A-Za-z0-9._]{32}$/);
    expect(one.VersionId).not.toBe(two.VersionId);
  });

  test("GetObject with a VersionId returns that version while the default returns the latest", async () => {
    const bucket = await versionedBucket();
    const one = await put(bucket, "k", "one");
    await put(bucket, "k", "two");
    expect(new TextDecoder().decode(await read(bucket, "k"))).toBe("two");
    const old = await s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k", VersionId: one.VersionId }));
    expect(await bodyText(old.Body)).toBe("one");
    expect(old.VersionId).toBe(one.VersionId);
  });

  test("a delete without a version id adds a delete marker and the key then reads as NoSuchKey", async () => {
    const bucket = await versionedBucket();
    const written = await put(bucket, "k", "v");
    const del = await s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: "k" }));
    expect(del.DeleteMarker).toBe(true);
    expect(del.VersionId).toBeDefined();
    await expectFailure(s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k" })), { name: "NoSuchKey", status: 404 });
    const old = await s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k", VersionId: written.VersionId }));
    expect(await bodyText(old.Body)).toBe("v");
    expect(await keysOf(bucket)).toEqual([]);
  });

  test("deleting the delete marker by id restores the object", async () => {
    const bucket = await versionedBucket();
    await put(bucket, "k", "v");
    const marker = await s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: "k" }));
    await s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: "k", VersionId: marker.VersionId }));
    expect(new TextDecoder().decode(await read(bucket, "k"))).toBe("v");
  });

  test("GetObject naming a delete marker version fails MethodNotAllowed", async () => {
    const bucket = await versionedBucket();
    await put(bucket, "k", "v");
    const marker = await s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: "k" }));
    await expectFailure(s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k", VersionId: marker.VersionId })), {
      name: "MethodNotAllowed",
      status: 405,
    });
  });

  test("an unknown version id on a versioned bucket fails NoSuchVersion", async () => {
    const bucket = await versionedBucket();
    await put(bucket, "k", "v");
    await expectFailure(s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k", VersionId: "x".repeat(32) })), {
      name: "NoSuchVersion",
      status: 404,
    });
  });

  test("ListObjectVersions lists versions newest first and delete markers with IsLatest", async () => {
    const bucket = await versionedBucket();
    const one = await put(bucket, "k", "one");
    const two = await put(bucket, "k", "two");
    const marker = await s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: "k" }));
    const out = await s3.send(new ListObjectVersionsCommand({ Bucket: bucket }));
    expect(out.Versions?.map((v) => v.VersionId)).toEqual([two.VersionId, one.VersionId]);
    expect(out.Versions?.every((v) => v.IsLatest === false)).toBe(true);
    expect(out.DeleteMarkers).toHaveLength(1);
    expect(out.DeleteMarkers?.[0]).toMatchObject({ Key: "k", VersionId: marker.VersionId, IsLatest: true });
  });

  test("a bucket with versions or delete markers cannot be deleted", async () => {
    const bucket = await versionedBucket();
    await put(bucket, "k", "v");
    await s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: "k" }));
    await expectFailure(s3.send(new DeleteBucketCommand({ Bucket: bucket })), {
      name: "BucketNotEmpty",
      status: 409,
      messageIncludes: "You must delete all versions in the bucket",
    });
  });

  test("ListObjectVersions on an unversioned bucket shows objects with VersionId null", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "v");
    const out = await s3.send(new ListObjectVersionsCommand({ Bucket: bucket }));
    expect(out.Versions).toHaveLength(1);
    expect(out.Versions?.[0]).toMatchObject({ Key: "k", VersionId: "null", IsLatest: true });
  });

  test("CopyObject reports the source version id and the new version id", async () => {
    const bucket = await versionedBucket();
    const src = await put(bucket, "src", "v");
    const out = await s3.send(new CopyObjectCommand({ Bucket: bucket, Key: "dst", CopySource: `${bucket}/src` }));
    expect(out.CopySourceVersionId).toBe(src.VersionId);
    expect(out.VersionId).toMatch(/^[A-Za-z0-9._]{32}$/);
  });
});

describe("S3 addressing", () => {
  test("virtual-hosted style requests through bucket.localhost reach the same objects as path style", async () => {
    const bucket = await makeBucket();
    const virtual = new S3Client(clientConfig({ endpoint: localhostEndpoint }));
    await virtual.send(new PutObjectCommand({ Bucket: bucket, Key: "vh/key.txt", Body: "virtual" }));
    expect(new TextDecoder().decode(await read(bucket, "vh/key.txt"))).toBe("virtual");
    const listed = await virtual.send(new ListObjectsV2Command({ Bucket: bucket }));
    expect(listed.Contents?.map((o) => o.Key)).toEqual(["vh/key.txt"]);
    const got = await virtual.send(new GetObjectCommand({ Bucket: bucket, Key: "vh/key.txt" }));
    expect(await bodyText(got.Body)).toBe("virtual");
  });

  test("CreateBucket and DeleteBucket work in virtual-hosted style", async () => {
    const virtual = new S3Client(clientConfig({ endpoint: localhostEndpoint }));
    const bucket = uniq("vhb");
    await virtual.send(new CreateBucketCommand({ Bucket: bucket }));
    expect((await s3.send(new HeadBucketCommand({ Bucket: bucket }))).$metadata.httpStatusCode).toBe(200);
    await virtual.send(new DeleteBucketCommand({ Bucket: bucket }));
    await expectFailure(s3.send(new HeadBucketCommand({ Bucket: bucket })), { name: "NotFound", status: 404 });
  });

  test("a missing bucket in virtual-hosted style fails NoSuchBucket", async () => {
    const virtual = new S3Client(clientConfig({ endpoint: localhostEndpoint }));
    await expectFailure(virtual.send(new ListObjectsV2Command({ Bucket: uniq("vhmissing") })), { name: "NoSuchBucket", status: 404 });
  });
});

describe("S3 presigned URLs", () => {
  test("a presigned GET URL returns the object without credentials", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "presigned body", { ContentType: "text/plain" });
    const url = await getSignedUrl(s3, new GetObjectCommand({ Bucket: bucket, Key: "k" }), { expiresIn: 300 });
    const response = await fetch(url);
    expect(response.status).toBe(200);
    expect(await response.text()).toBe("presigned body");
    expect(response.headers.get("content-type")).toBe("text/plain");
  });

  test("a presigned PUT URL stores the uploaded body", async () => {
    const bucket = await makeBucket();
    const url = await getSignedUrl(s3, new PutObjectCommand({ Bucket: bucket, Key: "up" }), { expiresIn: 300 });
    const response = await fetch(url, { method: "PUT", body: "uploaded through a presigned url" });
    expect(response.status).toBe(200);
    expect(new TextDecoder().decode(await read(bucket, "up"))).toBe("uploaded through a presigned url");
  });

  test("a presigned PUT without a checksum stores CRC64NVME like any other write", async () => {
    const bucket = await makeBucket();
    const url = await getSignedUrl(s3, new PutObjectCommand({ Bucket: bucket, Key: "up" }), { expiresIn: 300 });
    await fetch(url, { method: "PUT", body: "nvme" });
    const head = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "up", ChecksumMode: "ENABLED" }));
    expect(head.ChecksumCRC64NVME).toBe(checksum("CRC64NVME", "nvme"));
  });

  test("a presigned URL for a virtual-hosted client uses the bucket subdomain and works", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "vh presigned");
    const virtual = new S3Client(clientConfig({ endpoint: localhostEndpoint }));
    const url = await getSignedUrl(virtual, new GetObjectCommand({ Bucket: bucket, Key: "k" }), { expiresIn: 300 });
    expect(new URL(url).hostname).toBe(`${bucket}.localhost`);
    const response = await fetch(url);
    expect(await response.text()).toBe("vh presigned");
  });

  test("a presigned GET with response-content-disposition applies the override", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "x");
    const url = await getSignedUrl(
      s3,
      new GetObjectCommand({ Bucket: bucket, Key: "k", ResponseContentDisposition: "attachment; filename=dl.txt" }),
      { expiresIn: 300 },
    );
    expect((await fetch(url)).headers.get("content-disposition")).toBe("attachment; filename=dl.txt");
  });
});

describe("S3 checksums: SDK default behavior", () => {
  test("a default PutObject of a string sends CRC32 and S3 stores and returns it", async () => {
    const bucket = await makeBucket();
    const out = await put(bucket, "k", "hello");
    expect(out.ChecksumCRC32).toBe(checksum("CRC32", "hello"));
    expect(out.ChecksumType).toBe("FULL_OBJECT");
    const head = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "k", ChecksumMode: "ENABLED" }));
    expect(head.ChecksumCRC32).toBe(checksum("CRC32", "hello"));
    expect(head.ChecksumType).toBe("FULL_OBJECT");
  });

  test("a default GetObject returns the stored CRC32 and the SDK validates the body against it", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "validated body");
    const out = await s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k" }));
    expect(out.ChecksumCRC32).toBe(checksum("CRC32", "validated body"));
    expect(await bodyText(out.Body)).toBe("validated body");
  });

  test("a streamed body uploads with an aws-chunked unsigned trailer and stores the trailing CRC32", async () => {
    const bucket = await makeBucket();
    const content = "streamed-body";
    const out = await put(bucket, "stream", Readable.from([Buffer.from(content)]) as never, { ContentLength: content.length });
    expect(out.ChecksumCRC32).toBe(checksum("CRC32", content));
    const stored = await s3.send(new GetObjectCommand({ Bucket: bucket, Key: "stream" }));
    expect(stored.ContentLength).toBe(content.length);
    expect(await bodyText(stored.Body)).toBe(content);
    expect(stored.ContentEncoding).toBeUndefined();
  });

  test("a large streamed body decodes across many aws-chunked chunks", async () => {
    const bucket = await makeBucket();
    const content = patternBytes(1 * MIB + 123, 7);
    const chunks: Buffer[] = [];
    for (let offset = 0; offset < content.length; offset += 40_000) chunks.push(Buffer.from(content.subarray(offset, offset + 40_000)));
    const out = await put(bucket, "stream-large", Readable.from(chunks) as never, { ContentLength: content.length });
    expect(out.ChecksumCRC32).toBe(checksum("CRC32", content));
    expect(Buffer.from(await read(bucket, "stream-large")).equals(Buffer.from(content))).toBe(true);
  });

  test("an object written with the SDK checksum calculation turned down gets CRC64NVME from S3", async () => {
    const bucket = await makeBucket();
    const out = await s3WhenRequired.send(new PutObjectCommand({ Bucket: bucket, Key: "k", Body: "no client checksum" }));
    expect(out.ChecksumCRC64NVME).toBe(checksum("CRC64NVME", "no client checksum"));
    expect(out.ChecksumType).toBe("FULL_OBJECT");
    const head = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "k", ChecksumMode: "ENABLED" }));
    expect(head.ChecksumCRC64NVME).toBe(checksum("CRC64NVME", "no client checksum"));
  });

  test("HeadObject without ChecksumMode returns no checksum header", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "hello");
    const head = await s3WhenRequired.send(new HeadObjectCommand({ Bucket: bucket, Key: "k" }));
    expect(head.ChecksumCRC32).toBeUndefined();
    expect(head.ChecksumCRC64NVME).toBeUndefined();
  });

  const algorithms: Array<"CRC32" | "CRC32C" | "SHA1" | "SHA256"> = ["CRC32", "CRC32C", "SHA1", "SHA256"];
  for (const algorithm of algorithms) {
    test(`PutObject with ChecksumAlgorithm ${algorithm} stores that algorithm's checksum`, async () => {
      const bucket = await makeBucket();
      const body = "algorithm test body";
      const out = await put(bucket, "k", body, { ChecksumAlgorithm: algorithm });
      expect(out[`Checksum${algorithm}` as const]).toBe(checksum(algorithm, body));
      const head = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "k", ChecksumMode: "ENABLED" }));
      expect(head[`Checksum${algorithm}` as const]).toBe(checksum(algorithm, body));
    });

    test(`PutObject with a precomputed ${algorithm} value accepts a match and rejects a mismatch with BadDigest`, async () => {
      const bucket = await makeBucket();
      const body = "precomputed";
      const key = `Checksum${algorithm}` as const;
      await put(bucket, "ok", body, { [key]: checksum(algorithm, body) });
      const failure = await expectFailure(put(bucket, "bad", body, { [key]: checksum(algorithm, "something else") }), {
        name: "BadDigest",
        status: 400,
      });
      expect(failure.message).toContain(`The ${algorithm} you specified did not match the calculated checksum.`);
    });
  }

  test("PutObject with ChecksumAlgorithm CRC64NVME stores the full-object NVME checksum", async () => {
    const bucket = await makeBucket();
    const body = "nvme body";
    const out = await put(bucket, "k", body, { ChecksumAlgorithm: "CRC64NVME" });
    expect(out.ChecksumCRC64NVME).toBe(checksum("CRC64NVME", body));
    expect(out.ChecksumType).toBe("FULL_OBJECT");
  });

  test("a checksum value that is not valid base64 of the right length fails InvalidRequest", async () => {
    const bucket = await makeBucket();
    await expectFailure(put(bucket, "k", "v", { ChecksumSHA256: "not-base64-of-the-right-length" }), {
      name: "InvalidRequest",
      status: 400,
      messageIncludes: "Value for x-amz-checksum-sha256 header is invalid.",
    });
  });

  test("a streamed body whose trailing checksum is wrong fails BadDigest", async () => {
    const bucket = await makeBucket();
    const content = "trailing mismatch";
    await expectFailure(
      put(bucket, "k", Readable.from([Buffer.from(content)]) as never, { ContentLength: content.length, ChecksumCRC32: checksum("CRC32", "different") }),
      { name: "BadDigest", status: 400 },
    );
  });

  test("GetObjectAttributes returns an unquoted ETag, the checksum, size and storage class", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "attrs body");
    const out = await s3.send(
      new GetObjectAttributesCommand({ Bucket: bucket, Key: "k", ObjectAttributes: ["ETag", "Checksum", "ObjectSize", "StorageClass"] }),
    );
    expect(out.ETag).toBe(md5Hex("attrs body"));
    expect(out.Checksum?.ChecksumCRC32).toBe(checksum("CRC32", "attrs body"));
    expect(out.ObjectSize).toBe("attrs body".length);
    expect(out.StorageClass).toBe("STANDARD");
  });

  test("listings report ChecksumAlgorithm and ChecksumType for objects with a checksum", async () => {
    const bucket = await makeBucket();
    await put(bucket, "k", "listed");
    const out = await s3.send(new ListObjectsV2Command({ Bucket: bucket }));
    expect(out.Contents?.[0]?.ChecksumAlgorithm).toEqual(["CRC32"]);
    expect(out.Contents?.[0]?.ChecksumType).toBe("FULL_OBJECT");
  });

  test("CopyObject keeps the source's stored checksum algorithm", async () => {
    const bucket = await makeBucket();
    await put(bucket, "src", "copy checksum", { ChecksumAlgorithm: "SHA256" });
    const out = await s3.send(new CopyObjectCommand({ Bucket: bucket, Key: "dst", CopySource: `${bucket}/src` }));
    expect(out.CopyObjectResult?.ChecksumSHA256).toBe(checksum("SHA256", "copy checksum"));
  });

  test("DeleteObjects and PutBucketTagging are accepted with the SDK's default request checksum", async () => {
    const bucket = await makeBucket();
    await put(bucket, "a", "1");
    const out = await s3.send(new DeleteObjectsCommand({ Bucket: bucket, Delete: { Objects: [{ Key: "a" }] } }));
    expect(out.Deleted).toHaveLength(1);
    await s3.send(new PutBucketTaggingCommand({ Bucket: bucket, Tagging: { TagSet: [{ Key: "k", Value: "v" }] } }));
  });
});

describe("S3 multipart uploads: commands", () => {
  const partOne = patternBytes(5 * MIB, 11);
  const partTwo = patternBytes(1 * MIB + 5, 12);

  async function createUpload(bucket: string, key: string, extra: Partial<ConstructorParameters<typeof CreateMultipartUploadCommand>[0]> = {}) {
    const out = await s3.send(new CreateMultipartUploadCommand({ Bucket: bucket, Key: key, ...extra }));
    if (out.UploadId === undefined) throw new Error("CreateMultipartUpload returned no UploadId");
    return out.UploadId;
  }

  async function uploadPart(bucket: string, key: string, uploadId: string, partNumber: number, body: Uint8Array, extra: Partial<ConstructorParameters<typeof UploadPartCommand>[0]> = {}) {
    return s3.send(new UploadPartCommand({ Bucket: bucket, Key: key, UploadId: uploadId, PartNumber: partNumber, Body: body, ...extra }));
  }

  test("CreateMultipartUpload returns Bucket, Key and a URL-safe UploadId", async () => {
    const bucket = await makeBucket();
    const out = await s3.send(new CreateMultipartUploadCommand({ Bucket: bucket, Key: "obj" }));
    expect(out.Bucket).toBe(bucket);
    expect(out.Key).toBe("obj");
    expect(out.UploadId).toMatch(/^[A-Za-z0-9._~-]{16,}$/);
  });

  test("two parts complete into an object whose ETag is the MD5 of the part MD5s with -2", async () => {
    const bucket = await makeBucket();
    const uploadId = await createUpload(bucket, "obj");
    const one = await uploadPart(bucket, "obj", uploadId, 1, partOne, { ChecksumAlgorithm: undefined });
    const two = await uploadPart(bucket, "obj", uploadId, 2, partTwo);
    expect(one.ETag).toBe(`"${md5Hex(partOne)}"`);
    const done = await s3.send(
      new CompleteMultipartUploadCommand({
        Bucket: bucket,
        Key: "obj",
        UploadId: uploadId,
        MultipartUpload: {
          Parts: [
            { PartNumber: 1, ETag: one.ETag },
            { PartNumber: 2, ETag: two.ETag },
          ],
        },
      }),
    );
    expect(done.ETag).toBe(`"${multipartEtag([partOne, partTwo])}"`);
    expect(done.Bucket).toBe(bucket);
    expect(done.Key).toBe("obj");
    const body = await read(bucket, "obj");
    expect(Buffer.from(body).equals(Buffer.concat([partOne, partTwo]))).toBe(true);
    const head = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "obj" }));
    expect(head.ETag).toBe(`"${multipartEtag([partOne, partTwo])}"`);
    expect(head.ContentLength).toBe(partOne.length + partTwo.length);
  });

  test("the completed upload id is gone from ListMultipartUploads and ListParts", async () => {
    const bucket = await makeBucket();
    const uploadId = await createUpload(bucket, "obj");
    const part = await uploadPart(bucket, "obj", uploadId, 1, partTwo);
    await s3.send(
      new CompleteMultipartUploadCommand({ Bucket: bucket, Key: "obj", UploadId: uploadId, MultipartUpload: { Parts: [{ PartNumber: 1, ETag: part.ETag }] } }),
    );
    expect((await s3.send(new ListMultipartUploadsCommand({ Bucket: bucket }))).Uploads ?? []).toEqual([]);
    await expectFailure(s3.send(new ListPartsCommand({ Bucket: bucket, Key: "obj", UploadId: uploadId })), { name: "NoSuchUpload", status: 404 });
  });

  test("a single-part upload under 5 MiB is allowed and has the -1 ETag suffix", async () => {
    const bucket = await makeBucket();
    const uploadId = await createUpload(bucket, "obj");
    const part = await uploadPart(bucket, "obj", uploadId, 1, text("tiny"));
    const done = await s3.send(
      new CompleteMultipartUploadCommand({ Bucket: bucket, Key: "obj", UploadId: uploadId, MultipartUpload: { Parts: [{ PartNumber: 1, ETag: part.ETag }] } }),
    );
    expect(done.ETag).toBe(`"${multipartEtag([text("tiny")])}"`);
  });

  test("metadata, content type and tags given at CreateMultipartUpload apply to the final object", async () => {
    const bucket = await makeBucket();
    const uploadId = await createUpload(bucket, "obj", { ContentType: "application/x-test", Metadata: { origin: "mpu" }, Tagging: "t=1" });
    const part = await uploadPart(bucket, "obj", uploadId, 1, text("x"));
    await s3.send(
      new CompleteMultipartUploadCommand({ Bucket: bucket, Key: "obj", UploadId: uploadId, MultipartUpload: { Parts: [{ PartNumber: 1, ETag: part.ETag }] } }),
    );
    const head = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "obj" }));
    expect(head).toMatchObject({ ContentType: "application/x-test", Metadata: { origin: "mpu" } });
    expect((await s3.send(new GetObjectTaggingCommand({ Bucket: bucket, Key: "obj" }))).TagSet).toEqual([{ Key: "t", Value: "1" }]);
  });

  test("a ranged read and a partNumber read of a multipart object return the right bytes", async () => {
    const bucket = await makeBucket();
    const uploadId = await createUpload(bucket, "obj");
    const one = await uploadPart(bucket, "obj", uploadId, 1, partOne);
    const two = await uploadPart(bucket, "obj", uploadId, 2, partTwo);
    await s3.send(
      new CompleteMultipartUploadCommand({
        Bucket: bucket,
        Key: "obj",
        UploadId: uploadId,
        MultipartUpload: {
          Parts: [
            { PartNumber: 1, ETag: one.ETag },
            { PartNumber: 2, ETag: two.ETag },
          ],
        },
      }),
    );
    const ranged = await s3.send(new GetObjectCommand({ Bucket: bucket, Key: "obj", Range: `bytes=${partOne.length - 4}-${partOne.length + 3}` }));
    expect(Buffer.from(await bodyBytes(ranged.Body)).equals(Buffer.concat([partOne.subarray(partOne.length - 4), partTwo.subarray(0, 4)]))).toBe(true);
    const part2 = await s3.send(new GetObjectCommand({ Bucket: bucket, Key: "obj", PartNumber: 2 }));
    expect(part2.$metadata.httpStatusCode).toBe(206);
    expect(part2.PartsCount).toBe(2);
    expect(Buffer.from(await bodyBytes(part2.Body)).equals(Buffer.from(partTwo))).toBe(true);
  });

  test("ListParts returns part numbers, sizes, ETags and pages with max-parts", async () => {
    const bucket = await makeBucket();
    const uploadId = await createUpload(bucket, "obj");
    await uploadPart(bucket, "obj", uploadId, 1, partOne);
    await uploadPart(bucket, "obj", uploadId, 2, partOne);
    await uploadPart(bucket, "obj", uploadId, 3, partTwo);
    const first = await s3.send(new ListPartsCommand({ Bucket: bucket, Key: "obj", UploadId: uploadId, MaxParts: 2 }));
    expect(first.Parts?.map((p) => p.PartNumber)).toEqual([1, 2]);
    expect(first.Parts?.[0]).toMatchObject({ Size: partOne.length, ETag: `"${md5Hex(partOne)}"` });
    expect(first.IsTruncated).toBe(true);
    expect(first.NextPartNumberMarker).toBe("2");
    const second = await s3.send(new ListPartsCommand({ Bucket: bucket, Key: "obj", UploadId: uploadId, PartNumberMarker: "2" }));
    expect(second.Parts?.map((p) => p.PartNumber)).toEqual([3]);
    expect(second.IsTruncated).toBe(false);
  });

  test("re-uploading a part number overwrites that part", async () => {
    const bucket = await makeBucket();
    const uploadId = await createUpload(bucket, "obj");
    await uploadPart(bucket, "obj", uploadId, 1, text("first attempt"));
    const second = await uploadPart(bucket, "obj", uploadId, 1, text("second attempt"));
    const listed = await s3.send(new ListPartsCommand({ Bucket: bucket, Key: "obj", UploadId: uploadId }));
    expect(listed.Parts).toHaveLength(1);
    expect(listed.Parts?.[0]?.ETag).toBe(second.ETag);
  });

  test("ListMultipartUploads lists open uploads with key and id, and AbortMultipartUpload removes one", async () => {
    const bucket = await makeBucket();
    const a = await createUpload(bucket, "a");
    await createUpload(bucket, "b");
    const listed = await s3.send(new ListMultipartUploadsCommand({ Bucket: bucket }));
    expect(listed.Uploads?.map((u) => u.Key)).toEqual(["a", "b"]);
    expect(listed.Uploads?.[0]?.UploadId).toBe(a);
    const aborted = await s3.send(new AbortMultipartUploadCommand({ Bucket: bucket, Key: "a", UploadId: a }));
    expect(aborted.$metadata.httpStatusCode).toBe(204);
    expect((await s3.send(new ListMultipartUploadsCommand({ Bucket: bucket }))).Uploads?.map((u) => u.Key)).toEqual(["b"]);
    await expectFailure(s3.send(new AbortMultipartUploadCommand({ Bucket: bucket, Key: "a", UploadId: a })), { name: "NoSuchUpload", status: 404 });
  });

  test("an upload does not appear in the bucket listing and aborting leaves no object", async () => {
    const bucket = await makeBucket();
    const uploadId = await createUpload(bucket, "obj");
    await uploadPart(bucket, "obj", uploadId, 1, text("part"));
    await s3.send(new AbortMultipartUploadCommand({ Bucket: bucket, Key: "obj", UploadId: uploadId }));
    expect(await keysOf(bucket)).toEqual([]);
    await expectFailure(s3.send(new GetObjectCommand({ Bucket: bucket, Key: "obj" })), { name: "NoSuchKey", status: 404 });
  });

  test("UploadPart with an unknown upload id fails NoSuchUpload", async () => {
    const bucket = await makeBucket();
    await expectFailure(uploadPart(bucket, "obj", "no-such-upload-id", 1, text("x")), { name: "NoSuchUpload", status: 404 });
  });

  test("UploadPart with a part number outside 1 to 10000 fails InvalidArgument", async () => {
    const bucket = await makeBucket();
    const uploadId = await createUpload(bucket, "obj");
    for (const partNumber of [0, 10_001]) {
      await expectFailure(uploadPart(bucket, "obj", uploadId, partNumber, text("x")), {
        name: "InvalidArgument",
        status: 400,
        messageIncludes: "Part number must be an integer between 1 and 10000, inclusive",
      });
    }
  });

  test("Complete with a wrong part ETag fails InvalidPart", async () => {
    const bucket = await makeBucket();
    const uploadId = await createUpload(bucket, "obj");
    await uploadPart(bucket, "obj", uploadId, 1, text("x"));
    await expectFailure(
      s3.send(
        new CompleteMultipartUploadCommand({
          Bucket: bucket,
          Key: "obj",
          UploadId: uploadId,
          MultipartUpload: { Parts: [{ PartNumber: 1, ETag: '"00000000000000000000000000000000"' }] },
        }),
      ),
      { name: "InvalidPart", status: 400 },
    );
  });

  test("Complete naming a part that was never uploaded fails InvalidPart", async () => {
    const bucket = await makeBucket();
    const uploadId = await createUpload(bucket, "obj");
    const one = await uploadPart(bucket, "obj", uploadId, 1, text("x"));
    await expectFailure(
      s3.send(
        new CompleteMultipartUploadCommand({
          Bucket: bucket,
          Key: "obj",
          UploadId: uploadId,
          MultipartUpload: {
            Parts: [
              { PartNumber: 1, ETag: one.ETag },
              { PartNumber: 2, ETag: one.ETag },
            ],
          },
        }),
      ),
      { name: "InvalidPart", status: 400 },
    );
  });

  test("Complete with parts out of order fails InvalidPartOrder", async () => {
    const bucket = await makeBucket();
    const uploadId = await createUpload(bucket, "obj");
    const one = await uploadPart(bucket, "obj", uploadId, 1, partOne);
    const two = await uploadPart(bucket, "obj", uploadId, 2, partTwo);
    await expectFailure(
      s3.send(
        new CompleteMultipartUploadCommand({
          Bucket: bucket,
          Key: "obj",
          UploadId: uploadId,
          MultipartUpload: {
            Parts: [
              { PartNumber: 2, ETag: two.ETag },
              { PartNumber: 1, ETag: one.ETag },
            ],
          },
        }),
      ),
      { name: "InvalidPartOrder", status: 400 },
    );
  });

  test("Complete with a non-final part under 5 MiB fails EntityTooSmall", async () => {
    const bucket = await makeBucket();
    const uploadId = await createUpload(bucket, "obj");
    const one = await uploadPart(bucket, "obj", uploadId, 1, text("small"));
    const two = await uploadPart(bucket, "obj", uploadId, 2, text("also small"));
    await expectFailure(
      s3.send(
        new CompleteMultipartUploadCommand({
          Bucket: bucket,
          Key: "obj",
          UploadId: uploadId,
          MultipartUpload: {
            Parts: [
              { PartNumber: 1, ETag: one.ETag },
              { PartNumber: 2, ETag: two.ETag },
            ],
          },
        }),
      ),
      { name: "EntityTooSmall", status: 400 },
    );
  });

  test("Complete with no parts fails", async () => {
    const bucket = await makeBucket();
    const uploadId = await createUpload(bucket, "obj");
    const failure = await awsFailure(
      s3.send(new CompleteMultipartUploadCommand({ Bucket: bucket, Key: "obj", UploadId: uploadId, MultipartUpload: { Parts: [] } })),
    );
    expect(failure.status).toBe(400);
  });

  test("an invalid storage class fails at CreateMultipartUpload", async () => {
    const bucket = await makeBucket();
    await expectFailure(s3.send(new CreateMultipartUploadCommand({ Bucket: bucket, Key: "obj", StorageClass: "OUTPOSTS" })), {
      name: "InvalidStorageClass",
      status: 400,
    });
  });

  test("UploadPartCopy copies a byte range of an existing object into a part", async () => {
    const bucket = await makeBucket();
    const source = patternBytes(6 * MIB, 21);
    await put(bucket, "src", source);
    const uploadId = await createUpload(bucket, "dst");
    const one = await s3.send(
      new UploadPartCopyCommand({ Bucket: bucket, Key: "dst", UploadId: uploadId, PartNumber: 1, CopySource: `${bucket}/src`, CopySourceRange: `bytes=0-${5 * MIB - 1}` }),
    );
    const two = await s3.send(
      new UploadPartCopyCommand({ Bucket: bucket, Key: "dst", UploadId: uploadId, PartNumber: 2, CopySource: `${bucket}/src`, CopySourceRange: `bytes=${5 * MIB}-${6 * MIB - 1}` }),
    );
    expect(one.CopyPartResult?.ETag).toBe(`"${md5Hex(source.subarray(0, 5 * MIB))}"`);
    await s3.send(
      new CompleteMultipartUploadCommand({
        Bucket: bucket,
        Key: "dst",
        UploadId: uploadId,
        MultipartUpload: {
          Parts: [
            { PartNumber: 1, ETag: one.CopyPartResult?.ETag },
            { PartNumber: 2, ETag: two.CopyPartResult?.ETag },
          ],
        },
      }),
    );
    expect(Buffer.from(await read(bucket, "dst")).equals(Buffer.from(source))).toBe(true);
  });

  test("UploadPartCopy with a malformed copy-source-range fails InvalidArgument", async () => {
    const bucket = await makeBucket();
    await put(bucket, "src", "0123456789");
    const uploadId = await createUpload(bucket, "dst");
    await expectFailure(
      s3.send(new UploadPartCopyCommand({ Bucket: bucket, Key: "dst", UploadId: uploadId, PartNumber: 1, CopySource: `${bucket}/src`, CopySourceRange: "bytes=1-0" })),
      { name: "InvalidArgument", status: 400 },
    );
  });
});

describe("S3 multipart uploads: checksums", () => {
  const partOne = patternBytes(5 * MIB, 31);
  const partTwo = patternBytes(2 * MIB + 3, 32);

  async function runUpload(
    bucket: string,
    algorithm: SdkChecksumAlgorithm,
    options: { type?: "COMPOSITE" | "FULL_OBJECT"; withPartChecksums?: boolean } = {},
  ) {
    const created = await s3.send(
      new CreateMultipartUploadCommand({ Bucket: bucket, Key: "obj", ChecksumAlgorithm: algorithm, ChecksumType: options.type }),
    );
    const uploadId = created.UploadId;
    const field = `Checksum${algorithm}` as const;
    const parts: CompletedPart[] = [];
    for (const [index, body] of [partOne, partTwo].entries()) {
      const out = await s3.send(
        new UploadPartCommand({ Bucket: bucket, Key: "obj", UploadId: uploadId, PartNumber: index + 1, Body: body, ChecksumAlgorithm: algorithm }),
      );
      expect(out[field]).toBe(checksum(algorithm as never, body));
      parts.push({ PartNumber: index + 1, ETag: out.ETag, ...(options.withPartChecksums === false ? {} : { [field]: out[field] }) });
    }
    return {
      created,
      done: await s3.send(new CompleteMultipartUploadCommand({ Bucket: bucket, Key: "obj", UploadId: uploadId, MultipartUpload: { Parts: parts } })),
    };
  }

  test("CRC32 defaults to the COMPOSITE type and Complete returns checksum-of-checksums with -2", async () => {
    const bucket = await makeBucket();
    const { created, done } = await runUpload(bucket, "CRC32");
    expect(created.ChecksumAlgorithm).toBe("CRC32");
    expect(created.ChecksumType).toBe("COMPOSITE");
    expect(done.ChecksumCRC32).toBe(compositeChecksum("CRC32", [partOne, partTwo]));
    expect(done.ChecksumType).toBe("COMPOSITE");
    const head = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "obj", ChecksumMode: "ENABLED" }));
    expect(head.ChecksumCRC32).toBe(compositeChecksum("CRC32", [partOne, partTwo]));
  });

  test("SHA256 uploads are composite", async () => {
    const bucket = await makeBucket();
    const { done } = await runUpload(bucket, "SHA256");
    expect(done.ChecksumSHA256).toBe(compositeChecksum("SHA256", [partOne, partTwo]));
    expect(done.ChecksumType).toBe("COMPOSITE");
  });

  test("CRC32C and SHA1 uploads are composite", async () => {
    for (const algorithm of ["CRC32C", "SHA1"] as const) {
      const bucket = await makeBucket();
      const { done } = await runUpload(bucket, algorithm);
      expect(done[`Checksum${algorithm}` as const]).toBe(compositeChecksum(algorithm, [partOne, partTwo]));
    }
  });

  test("CRC64NVME defaults to the FULL_OBJECT type with the checksum of the whole object", async () => {
    const bucket = await makeBucket();
    const { created, done } = await runUpload(bucket, "CRC64NVME");
    expect(created.ChecksumType).toBe("FULL_OBJECT");
    expect(done.ChecksumCRC64NVME).toBe(checksum("CRC64NVME", Buffer.concat([partOne, partTwo])));
    expect(done.ChecksumType).toBe("FULL_OBJECT");
  });

  test("CRC32 with the FULL_OBJECT type returns the CRC32 of the whole object", async () => {
    const bucket = await makeBucket();
    const { created, done } = await runUpload(bucket, "CRC32", { type: "FULL_OBJECT" });
    expect(created.ChecksumType).toBe("FULL_OBJECT");
    expect(done.ChecksumCRC32).toBe(checksum("CRC32", Buffer.concat([partOne, partTwo])));
  });

  test("the COMPOSITE type cannot be used with CRC64NVME", async () => {
    const bucket = await makeBucket();
    await expectFailure(
      s3.send(new CreateMultipartUploadCommand({ Bucket: bucket, Key: "obj", ChecksumAlgorithm: "CRC64NVME", ChecksumType: "COMPOSITE" })),
      { name: "InvalidRequest", status: 400, messageIncludes: "The COMPOSITE checksum type cannot be used with the crc64nvme checksum algorithm." },
    );
  });

  test("the FULL_OBJECT type cannot be used with SHA256", async () => {
    const bucket = await makeBucket();
    await expectFailure(
      s3.send(new CreateMultipartUploadCommand({ Bucket: bucket, Key: "obj", ChecksumAlgorithm: "SHA256", ChecksumType: "FULL_OBJECT" })),
      { name: "InvalidRequest", status: 400, messageIncludes: "The FULL_OBJECT checksum type cannot be used with the sha256 checksum algorithm." },
    );
  });

  test("a checksum type without an algorithm fails InvalidRequest", async () => {
    const bucket = await makeBucket();
    await expectFailure(s3.send(new CreateMultipartUploadCommand({ Bucket: bucket, Key: "obj", ChecksumType: "COMPOSITE" })), {
      name: "InvalidRequest",
      status: 400,
      messageIncludes: "The x-amz-checksum-type header can only be used with the x-amz-checksum-algorithm header.",
    });
  });

  test("a part with a different algorithm than the upload fails InvalidRequest", async () => {
    const bucket = await makeBucket();
    const created = await s3.send(new CreateMultipartUploadCommand({ Bucket: bucket, Key: "obj", ChecksumAlgorithm: "SHA256" }));
    await expectFailure(
      s3.send(new UploadPartCommand({ Bucket: bucket, Key: "obj", UploadId: created.UploadId, PartNumber: 1, Body: partTwo, ChecksumAlgorithm: "CRC32" })),
      { name: "InvalidRequest", status: 400, messageIncludes: "Checksum Type mismatch occurred, expected checksum Type: sha256, actual checksum Type: crc32" },
    );
  });

  test("a part whose checksum does not match fails BadDigest", async () => {
    const bucket = await makeBucket();
    const created = await s3.send(new CreateMultipartUploadCommand({ Bucket: bucket, Key: "obj", ChecksumAlgorithm: "CRC32" }));
    await expectFailure(
      s3.send(
        new UploadPartCommand({
          Bucket: bucket,
          Key: "obj",
          UploadId: created.UploadId,
          PartNumber: 1,
          Body: partTwo,
          ChecksumCRC32: checksum("CRC32", "different content"),
        }),
      ),
      { name: "BadDigest", status: 400 },
    );
  });

  test("an upload without a declared algorithm completes with CRC64NVME FULL_OBJECT", async () => {
    const bucket = await makeBucket();
    const created = await s3WhenRequired.send(new CreateMultipartUploadCommand({ Bucket: bucket, Key: "obj" }));
    const one = await s3WhenRequired.send(new UploadPartCommand({ Bucket: bucket, Key: "obj", UploadId: created.UploadId, PartNumber: 1, Body: partOne }));
    const two = await s3WhenRequired.send(new UploadPartCommand({ Bucket: bucket, Key: "obj", UploadId: created.UploadId, PartNumber: 2, Body: partTwo }));
    const done = await s3WhenRequired.send(
      new CompleteMultipartUploadCommand({
        Bucket: bucket,
        Key: "obj",
        UploadId: created.UploadId,
        MultipartUpload: {
          Parts: [
            { PartNumber: 1, ETag: one.ETag },
            { PartNumber: 2, ETag: two.ETag },
          ],
        },
      }),
    );
    expect(done.ChecksumCRC64NVME).toBe(checksum("CRC64NVME", Buffer.concat([partOne, partTwo])));
    expect(done.ChecksumType).toBe("FULL_OBJECT");
  });
});

describe("S3 managed uploads (lib-storage)", () => {
  async function managed(bucket: string, key: string, body: Uint8Array, options: { partSize?: number; queueSize?: number; params?: Record<string, unknown> } = {}) {
    const upload = new Upload({
      client: s3,
      params: { Bucket: bucket, Key: key, Body: Buffer.from(body), ...options.params },
      partSize: options.partSize ?? 5 * MIB,
      queueSize: options.queueSize ?? 3,
    });
    return upload.done();
  }

  test("a body under the part size uploads with a single PutObject", async () => {
    const bucket = await makeBucket();
    const body = patternBytes(1 * MIB, 41);
    const out = await managed(bucket, "small", body);
    expect(out.ETag).toBe(`"${md5Hex(body)}"`);
    expect(Buffer.from(await read(bucket, "small")).equals(Buffer.from(body))).toBe(true);
  });

  test("12 MiB in 5 MiB parts completes as three parts with a -3 ETag and a composite CRC32", async () => {
    const bucket = await makeBucket();
    const body = patternBytes(12 * MIB, 42);
    const parts = [body.subarray(0, 5 * MIB), body.subarray(5 * MIB, 10 * MIB), body.subarray(10 * MIB)];
    const out = await managed(bucket, "big", body);
    expect(out.ETag).toBe(`"${multipartEtag(parts)}"`);
    expect(out.ChecksumCRC32).toBe(compositeChecksum("CRC32", parts));
    expect(out.ChecksumType).toBe("COMPOSITE");
    const got = await s3.send(new GetObjectCommand({ Bucket: bucket, Key: "big" }));
    expect(got.ContentLength).toBe(12 * MIB);
    expect(got.ETag).toBe(`"${multipartEtag(parts)}"`);
    expect(Buffer.from(await bodyBytes(got.Body)).equals(Buffer.from(body))).toBe(true);
  });

  test("a managed upload with a requested SHA256 algorithm completes with a composite SHA256", async () => {
    const bucket = await makeBucket();
    const body = patternBytes(11 * MIB, 43);
    const parts = [body.subarray(0, 5 * MIB), body.subarray(5 * MIB, 10 * MIB), body.subarray(10 * MIB)];
    const out = await managed(bucket, "sha", body, { params: { ChecksumAlgorithm: "SHA256" } });
    expect(out.ChecksumSHA256).toBe(compositeChecksum("SHA256", parts));
    const head = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "sha", ChecksumMode: "ENABLED" }));
    expect(head.ChecksumSHA256).toBe(compositeChecksum("SHA256", parts));
  });

  test("a managed upload from a stream with unknown length completes", async () => {
    const bucket = await makeBucket();
    const body = patternBytes(11 * MIB, 44);
    const chunks: Buffer[] = [];
    for (let offset = 0; offset < body.length; offset += MIB) chunks.push(Buffer.from(body.subarray(offset, offset + MIB)));
    const upload = new Upload({ client: s3, params: { Bucket: bucket, Key: "streamed", Body: Readable.from(chunks) }, partSize: 5 * MIB });
    await upload.done();
    expect(Buffer.from(await read(bucket, "streamed")).equals(Buffer.from(body))).toBe(true);
  });

  test("metadata and content type given to a managed upload land on the final object", async () => {
    const bucket = await makeBucket();
    await managed(bucket, "meta", patternBytes(6 * MIB, 45), { params: { ContentType: "application/x-mpu", Metadata: { source: "lib-storage" } } });
    const head = await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "meta" }));
    expect(head).toMatchObject({ ContentType: "application/x-mpu", Metadata: { source: "lib-storage" } });
  });

  test("aborting a managed upload leaves no object and no open multipart upload", async () => {
    const bucket = await makeBucket();
    const upload = new Upload({ client: s3, params: { Bucket: bucket, Key: "aborted", Body: Buffer.from(patternBytes(12 * MIB, 46)) }, partSize: 5 * MIB, queueSize: 1 });
    const pending = upload.done();
    pending.catch(() => undefined);
    await sleep(50);
    await upload.abort();
    await awsFailure(pending).catch(() => undefined);
    await eventually("no open uploads", async () => {
      const listed = await s3.send(new ListMultipartUploadsCommand({ Bucket: bucket }));
      return (listed.Uploads ?? []).length === 0 ? true : undefined;
    });
    expect(await keysOf(bucket)).toEqual([]);
  });

  test("a managed upload into a missing bucket fails NoSuchBucket", async () => {
    await expectFailure(managed(uniq("missing"), "k", patternBytes(6 * MIB, 47)), { name: "NoSuchBucket", status: 404 });
  });
});

describe("S3 server-side encryption with KMS", () => {
  async function makeKey() {
    const out = await kms.send(new CreateKeyCommand({}));
    const arn = out.KeyMetadata?.Arn;
    const id = out.KeyMetadata?.KeyId;
    if (arn === undefined || id === undefined) throw new Error("CreateKey returned no key");
    return { arn, id };
  }

  test("PutObject with aws:kms and a key id reports the key ARN, and GetObject reads it back", async () => {
    const bucket = await makeBucket();
    const key = await makeKey();
    const out = await put(bucket, "k", "secret", { ServerSideEncryption: "aws:kms", SSEKMSKeyId: key.id });
    expect(out.ServerSideEncryption).toBe("aws:kms");
    expect(out.SSEKMSKeyId).toBe(key.arn);
    expect(out.ETag).not.toBe(`"${md5Hex("secret")}"`);
    const got = await s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k" }));
    expect(got.ServerSideEncryption).toBe("aws:kms");
    expect(got.SSEKMSKeyId).toBe(key.arn);
    expect(await bodyText(got.Body)).toBe("secret");
  });

  test("a bucket default of aws:kms encrypts objects that name no encryption", async () => {
    const bucket = await makeBucket();
    const key = await makeKey();
    await s3.send(
      new PutBucketEncryptionCommand({
        Bucket: bucket,
        ServerSideEncryptionConfiguration: { Rules: [{ ApplyServerSideEncryptionByDefault: { SSEAlgorithm: "aws:kms", KMSMasterKeyID: key.arn }, BucketKeyEnabled: true }] },
      }),
    );
    const out = await put(bucket, "k", "defaulted");
    expect(out.ServerSideEncryption).toBe("aws:kms");
    expect(out.SSEKMSKeyId).toBe(key.arn);
    expect(out.BucketKeyEnabled).toBe(true);
  });

  test("objects written before a new bucket default keep AES256", async () => {
    const bucket = await makeBucket();
    await put(bucket, "before", "v");
    const key = await makeKey();
    await s3.send(
      new PutBucketEncryptionCommand({
        Bucket: bucket,
        ServerSideEncryptionConfiguration: { Rules: [{ ApplyServerSideEncryptionByDefault: { SSEAlgorithm: "aws:kms", KMSMasterKeyID: key.arn } }] },
      }),
    );
    expect((await s3.send(new HeadObjectCommand({ Bucket: bucket, Key: "before" }))).ServerSideEncryption).toBe("AES256");
  });

  test("a key id that does not exist fails KMS.NotFoundException", async () => {
    const bucket = await makeBucket();
    await expectFailure(put(bucket, "k", "v", { ServerSideEncryption: "aws:kms", SSEKMSKeyId: "fake-key-id" }), {
      name: "KMS.NotFoundException",
      status: 400,
      messageIncludes: "Invalid keyId 'fake-key-id'",
    });
  });

  test("a well-formed ARN of a missing key fails KMS.NotFoundException", async () => {
    const bucket = await makeBucket();
    const arn = `arn:aws:kms:${region}:${defaultAccount}:key/00000000-0000-4000-8000-000000000000`;
    await expectFailure(put(bucket, "k", "v", { ServerSideEncryption: "aws:kms", SSEKMSKeyId: arn }), {
      name: "KMS.NotFoundException",
      status: 400,
      messageIncludes: `Key '${arn}' does not exist`,
    });
  });

  test("a disabled key fails writes and reads of existing objects with KMS.DisabledException", async () => {
    const bucket = await makeBucket();
    const key = await makeKey();
    await put(bucket, "k", "v", { ServerSideEncryption: "aws:kms", SSEKMSKeyId: key.id });
    await kms.send(new DisableKeyCommand({ KeyId: key.id }));
    await expectFailure(put(bucket, "k2", "v", { ServerSideEncryption: "aws:kms", SSEKMSKeyId: key.id }), {
      name: "KMS.DisabledException",
      status: 400,
      messageIncludes: `${key.arn} is disabled.`,
    });
    await expectFailure(s3.send(new GetObjectCommand({ Bucket: bucket, Key: "k" })), { name: "KMS.DisabledException", status: 400 });
  });

  test("multipart uploads under SSE-KMS validate the key at CreateMultipartUpload", async () => {
    const bucket = await makeBucket();
    await expectFailure(s3.send(new CreateMultipartUploadCommand({ Bucket: bucket, Key: "k", ServerSideEncryption: "aws:kms", SSEKMSKeyId: "fake-key-id" })), {
      name: "KMS.NotFoundException",
      status: 400,
    });
  });

  test("a managed upload under SSE-KMS completes and reads back", async () => {
    const bucket = await makeBucket();
    const key = await makeKey();
    const body = patternBytes(6 * MIB, 51);
    const upload = new Upload({
      client: s3,
      params: { Bucket: bucket, Key: "kms-big", Body: Buffer.from(body), ServerSideEncryption: "aws:kms", SSEKMSKeyId: key.id },
      partSize: 5 * MIB,
    });
    const out = await upload.done();
    expect(out.ServerSideEncryption).toBe("aws:kms");
    expect(Buffer.from(await read(bucket, "kms-big")).equals(Buffer.from(body))).toBe(true);
  });
});

describe("S3 event notifications to SQS", () => {
  async function queueFor(): Promise<{ url: string; arn: string }> {
    const name = uniq("s3evt");
    const out = await sqs.send(new CreateQueueCommand({ QueueName: name }));
    return { url: out.QueueUrl ?? "", arn: `arn:aws:sqs:${region}:${defaultAccount}:${name}` };
  }

  async function receiveEvents(queueUrl: string, want: number): Promise<Array<Record<string, unknown>>> {
    const got: Array<Record<string, unknown>> = [];
    const deadline = Date.now() + 10_000;
    while (got.length < want && Date.now() < deadline) {
      const out = await sqs.send(new ReceiveMessageCommand({ QueueUrl: queueUrl, MaxNumberOfMessages: 10, WaitTimeSeconds: 1 }));
      const messages = out.Messages ?? [];
      if (messages.length > 0) {
        await sqs.send(new DeleteMessageBatchCommand({ QueueUrl: queueUrl, Entries: messages.map((m, i) => ({ Id: `m${i}`, ReceiptHandle: m.ReceiptHandle })) }));
      }
      for (const m of messages) {
        const body: unknown = JSON.parse(m.Body ?? "null");
        if (typeof body === "object" && body !== null && "Records" in body) got.push(body as Record<string, unknown>);
      }
    }
    return got;
  }

  test("PutBucketNotificationConfiguration stores the queue configuration and returns it capitalised", async () => {
    const bucket = await makeBucket();
    const queue = await queueFor();
    await s3.send(
      new PutBucketNotificationConfigurationCommand({
        Bucket: bucket,
        NotificationConfiguration: {
          QueueConfigurations: [
            {
              Id: "all-created",
              QueueArn: queue.arn,
              Events: ["s3:ObjectCreated:*"],
              Filter: { Key: { FilterRules: [{ Name: "prefix", Value: "in/" }, { Name: "suffix", Value: ".json" }] } },
            },
          ],
        },
      }),
    );
    const got = await s3.send(new GetBucketNotificationConfigurationCommand({ Bucket: bucket }));
    // AWS reports the rule names capitalized, which the SDK's request enum does not allow.
    const configured = got.QueueConfigurations ?? [];
    expect(configured).toHaveLength(1);
    expect(configured[0]).toMatchObject({ Id: "all-created", QueueArn: queue.arn, Events: ["s3:ObjectCreated:*"] });
    expect(configured[0]?.Filter?.Key?.FilterRules?.map((rule) => [String(rule.Name), rule.Value])).toEqual([
      ["Prefix", "in/"],
      ["Suffix", ".json"],
    ]);
  });

  test("an object created under a matching prefix and suffix produces one ObjectCreated:Put event", async () => {
    const bucket = await makeBucket();
    const queue = await queueFor();
    await s3.send(
      new PutBucketNotificationConfigurationCommand({
        Bucket: bucket,
        NotificationConfiguration: {
          QueueConfigurations: [
            {
              Id: "cfg",
              QueueArn: queue.arn,
              Events: ["s3:ObjectCreated:*"],
              Filter: { Key: { FilterRules: [{ Name: "prefix", Value: "in/" }, { Name: "suffix", Value: ".json" }] } },
            },
          ],
        },
      }),
    );
    await put(bucket, "in/skipped.txt", "no match");
    await put(bucket, "out/skipped.json", "no match");
    const body = "event body";
    await put(bucket, "in/a b@c.json", body);
    const events = await receiveEvents(queue.url, 1);
    expect(events).toHaveLength(1);
    const record = (events[0]?.Records as Array<Record<string, any>>)[0];
    expect(record).toMatchObject({ eventSource: "aws:s3", awsRegion: region, eventName: "ObjectCreated:Put" });
    expect(record?.s3.configurationId).toBe("cfg");
    expect(record?.s3.bucket).toMatchObject({ name: bucket, arn: `arn:aws:s3:::${bucket}` });
    expect(record?.s3.object.key).toBe("in/a+b%40c.json");
    expect(record?.s3.object.size).toBe(body.length);
    expect(record?.s3.object.eTag).toBe(md5Hex(body));
    expect(record?.eventTime).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/);
  });

  test("DeleteObject produces ObjectRemoved:Delete without size or eTag", async () => {
    const bucket = await makeBucket();
    const queue = await queueFor();
    await put(bucket, "k", "v");
    await s3.send(
      new PutBucketNotificationConfigurationCommand({
        Bucket: bucket,
        NotificationConfiguration: { QueueConfigurations: [{ QueueArn: queue.arn, Events: ["s3:ObjectRemoved:*"] }] },
      }),
    );
    await s3.send(new DeleteObjectCommand({ Bucket: bucket, Key: "k" }));
    const events = await receiveEvents(queue.url, 1);
    const record = (events[0]?.Records as Array<Record<string, any>>)[0];
    expect(record?.eventName).toBe("ObjectRemoved:Delete");
    expect(record?.s3.object.key).toBe("k");
    expect(record?.s3.object.size).toBeUndefined();
    expect(record?.s3.object.eTag).toBeUndefined();
  });

  test("CopyObject and CompleteMultipartUpload produce their own event names", async () => {
    const bucket = await makeBucket();
    const queue = await queueFor();
    await put(bucket, "src", "v");
    await s3.send(
      new PutBucketNotificationConfigurationCommand({
        Bucket: bucket,
        NotificationConfiguration: { QueueConfigurations: [{ QueueArn: queue.arn, Events: ["s3:ObjectCreated:*"] }] },
      }),
    );
    await s3.send(new CopyObjectCommand({ Bucket: bucket, Key: "dst", CopySource: `${bucket}/src` }));
    await managedUpload(bucket, "mpu");
    const events = await receiveEvents(queue.url, 2);
    const names = events.flatMap((e) => (e.Records as Array<Record<string, any>>).map((r) => r.eventName as string)).sort();
    expect(names).toEqual(["ObjectCreated:Copy", "ObjectCreated:CompleteMultipartUpload"].sort());
  });

  async function managedUpload(bucket: string, key: string) {
    await new Upload({ client: s3, params: { Bucket: bucket, Key: key, Body: Buffer.from(patternBytes(6 * MIB, 61)) }, partSize: 5 * MIB }).done();
  }

  test("a notification configuration naming an ARN that cannot be parsed fails InvalidArgument", async () => {
    const bucket = await makeBucket();
    await expectFailure(
      s3.send(
        new PutBucketNotificationConfigurationCommand({
          Bucket: bucket,
          NotificationConfiguration: { QueueConfigurations: [{ QueueArn: "not-an-arn", Events: ["s3:ObjectCreated:*"] }] },
        }),
      ),
      { name: "InvalidArgument", status: 400, messageIncludes: "The ARN could not be parsed" },
    );
  });

  test("a notification configuration naming a queue that does not exist fails InvalidArgument", async () => {
    const bucket = await makeBucket();
    await expectFailure(
      s3.send(
        new PutBucketNotificationConfigurationCommand({
          Bucket: bucket,
          NotificationConfiguration: {
            QueueConfigurations: [{ QueueArn: `arn:aws:sqs:${region}:${defaultAccount}:${uniq("ghost")}`, Events: ["s3:ObjectCreated:*"] }],
          },
        }),
      ),
      { name: "InvalidArgument", status: 400, messageIncludes: "Unable to validate the following destination configurations" },
    );
  });

  test("an empty notification configuration clears the previous one", async () => {
    const bucket = await makeBucket();
    const queue = await queueFor();
    await s3.send(
      new PutBucketNotificationConfigurationCommand({
        Bucket: bucket,
        NotificationConfiguration: { QueueConfigurations: [{ QueueArn: queue.arn, Events: ["s3:ObjectCreated:*"] }] },
      }),
    );
    await s3.send(new PutBucketNotificationConfigurationCommand({ Bucket: bucket, NotificationConfiguration: {} }));
    const got = await s3.send(new GetBucketNotificationConfigurationCommand({ Bucket: bucket }));
    expect(got.QueueConfigurations ?? []).toEqual([]);
  });
});
