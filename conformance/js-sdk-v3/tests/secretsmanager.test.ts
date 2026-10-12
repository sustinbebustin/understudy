// Secrets Manager over awsJson1_1, through @aws-sdk/client-secrets-manager.
// Expected behavior: docs/research/phase1/secrets-logs-events-alarms.md section 2.

import { describe, expect, test } from "bun:test";
import {
  CreateSecretCommand,
  DeleteResourcePolicyCommand,
  DeleteSecretCommand,
  DescribeSecretCommand,
  GetResourcePolicyCommand,
  GetSecretValueCommand,
  ListSecretsCommand,
  ListSecretVersionIdsCommand,
  PutResourcePolicyCommand,
  PutSecretValueCommand,
  RestoreSecretCommand,
  SecretsManagerClient,
  TagResourceCommand,
  UntagResourceCommand,
  UpdateSecretCommand,
  UpdateSecretVersionStageCommand,
} from "@aws-sdk/client-secrets-manager";
import { randomUUID } from "node:crypto";
import { CreateKeyCommand, KMSClient } from "@aws-sdk/client-kms";
import { clientConfig, defaultAccount, region } from "../support/env";
import { awsFailure, expectFailure, uniq } from "../support/helpers";

const sm = new SecretsManagerClient(clientConfig());
const kms = new KMSClient(clientConfig());

/** Creates a secret; pass null as value to create it empty. */
async function makeSecret(value: string | null = "initial", extra: Partial<ConstructorParameters<typeof CreateSecretCommand>[0]> = {}) {
  const name = extra.Name ?? uniq("secret");
  const out = await sm.send(new CreateSecretCommand({ Name: name, SecretString: value ?? undefined, ...extra }));
  return { name, arn: out.ARN ?? "", versionId: out.VersionId, out };
}

describe("Secrets Manager: create and read", () => {
  test("CreateSecret returns an ARN with a six-character suffix, the name and a version id", async () => {
    const { name, arn, versionId } = await makeSecret();
    expect(arn).toMatch(new RegExp(`^arn:aws:secretsmanager:${region}:${defaultAccount}:secret:${name}-[A-Za-z0-9]{6}$`));
    expect(versionId).toMatch(/^[0-9a-f-]{36}$/);
  });

  test("CreateSecret without a value returns no VersionId", async () => {
    const { out } = await makeSecret(null);
    expect(out.VersionId).toBeUndefined();
    expect(out.ARN).toBeDefined();
  });

  test("GetSecretValue returns the value, name, ARN, version id and the AWSCURRENT stage", async () => {
    const { name, arn, versionId } = await makeSecret("s3cr3t");
    const out = await sm.send(new GetSecretValueCommand({ SecretId: name }));
    expect(out).toMatchObject({ SecretString: "s3cr3t", Name: name, ARN: arn, VersionId: versionId, VersionStages: ["AWSCURRENT"] });
    expect(out.SecretBinary).toBeUndefined();
    expect(out.CreatedDate).toBeInstanceOf(Date);
  });

  test("GetSecretValue works by full ARN", async () => {
    const { arn } = await makeSecret("by arn");
    expect((await sm.send(new GetSecretValueCommand({ SecretId: arn }))).SecretString).toBe("by arn");
  });

  test("binary secrets round trip as SecretBinary", async () => {
    const name = uniq("bin");
    await sm.send(new CreateSecretCommand({ Name: name, SecretBinary: new Uint8Array([0, 1, 2, 250, 255]) }));
    const out = await sm.send(new GetSecretValueCommand({ SecretId: name }));
    expect(Array.from(out.SecretBinary ?? [])).toEqual([0, 1, 2, 250, 255]);
    expect(out.SecretString).toBeUndefined();
  });

  test("a name with the permitted punctuation round trips", async () => {
    const name = `${uniq("punct")}/_+=.@-name`;
    const { arn } = await makeSecret("v", { Name: name });
    expect(arn).toContain(`secret:${name}-`);
    expect((await sm.send(new GetSecretValueCommand({ SecretId: name }))).SecretString).toBe("v");
  });

  test("CreateSecret with an invalid name fails ValidationException", async () => {
    await expectFailure(sm.send(new CreateSecretCommand({ Name: "has space", SecretString: "x" })), {
      name: "ValidationException",
      status: 400,
      messageIncludes: "Invalid name. Must be a valid name containing alphanumeric characters, or any of the following: -/_+=.@!",
    });
  });

  test("CreateSecret on an existing name fails ResourceExistsException", async () => {
    const { name } = await makeSecret();
    await expectFailure(sm.send(new CreateSecretCommand({ Name: name, SecretString: "again", ClientRequestToken: randomUUID() })), {
      name: "ResourceExistsException",
      status: 400,
      messageIncludes: `The operation failed because the secret ${name} already exists.`,
    });
  });

  test("CreateSecret replayed with the same token and value is idempotent", async () => {
    const name = uniq("idem");
    const token = randomUUID();
    const first = await sm.send(new CreateSecretCommand({ Name: name, SecretString: "v", ClientRequestToken: token }));
    const second = await sm.send(new CreateSecretCommand({ Name: name, SecretString: "v", ClientRequestToken: token }));
    expect(second.ARN).toBe(first.ARN);
    expect(second.VersionId).toBe(first.VersionId);
  });

  test("the ClientRequestToken becomes the version id", async () => {
    const token = randomUUID();
    const { versionId } = await makeSecret("v", { ClientRequestToken: token });
    expect(versionId).toBe(token);
  });

  test("GetSecretValue on an unknown secret fails ResourceNotFoundException with status 400", async () => {
    await expectFailure(sm.send(new GetSecretValueCommand({ SecretId: uniq("missing") })), {
      name: "ResourceNotFoundException",
      status: 400,
      messageIncludes: "Secrets Manager can't find the specified secret.",
    });
  });

  test("GetSecretValue on a secret created without a value fails with the staging label message", async () => {
    const { name } = await makeSecret(null);
    await expectFailure(sm.send(new GetSecretValueCommand({ SecretId: name })), {
      name: "ResourceNotFoundException",
      status: 400,
      messageIncludes: "Secrets Manager can't find the specified secret value for staging label: AWSCURRENT",
    });
  });

  test("GetSecretValue with an unknown VersionId fails with the VersionId message", async () => {
    const { name } = await makeSecret();
    const unknown = randomUUID();
    await expectFailure(sm.send(new GetSecretValueCommand({ SecretId: name, VersionId: unknown })), {
      name: "ResourceNotFoundException",
      status: 400,
      messageIncludes: `Secrets Manager can't find the specified secret value for VersionId: ${unknown}`,
    });
  });

  test("GetSecretValue with a VersionId and a VersionStage of different versions fails InvalidRequestException", async () => {
    const { name, versionId } = await makeSecret("one");
    await sm.send(new PutSecretValueCommand({ SecretId: name, SecretString: "two", ClientRequestToken: randomUUID() }));
    await expectFailure(sm.send(new GetSecretValueCommand({ SecretId: name, VersionId: versionId, VersionStage: "AWSCURRENT" })), {
      name: "InvalidRequestException",
      status: 400,
      messageIncludes: "You provided a VersionStage that is not associated to the provided VersionId.",
    });
  });

  test("the first PutSecretValue on an empty secret returns the AWSCURRENT stage", async () => {
    const { name } = await makeSecret(null);
    const out = await sm.send(new PutSecretValueCommand({ SecretId: name, SecretString: "first", ClientRequestToken: randomUUID() }));
    expect(out.VersionStages).toEqual(["AWSCURRENT"]);
  });
});

describe("Secrets Manager: versions and stages", () => {
  test("PutSecretValue makes the new version AWSCURRENT and demotes the old one to AWSPREVIOUS", async () => {
    const { name, versionId: v1 } = await makeSecret("one");
    const v2 = (await sm.send(new PutSecretValueCommand({ SecretId: name, SecretString: "two", ClientRequestToken: randomUUID() }))).VersionId;
    expect((await sm.send(new GetSecretValueCommand({ SecretId: name }))).SecretString).toBe("two");
    const previous = await sm.send(new GetSecretValueCommand({ SecretId: name, VersionStage: "AWSPREVIOUS" }));
    expect(previous).toMatchObject({ SecretString: "one", VersionId: v1 });
    const described = await sm.send(new DescribeSecretCommand({ SecretId: name }));
    expect(described.VersionIdsToStages).toEqual({ [v1 ?? ""]: ["AWSPREVIOUS"], [v2 ?? ""]: ["AWSCURRENT"] });
  });

  test("a third version leaves the oldest unlabeled and absent from the default listing", async () => {
    const { name, versionId: v1 } = await makeSecret("one");
    await sm.send(new PutSecretValueCommand({ SecretId: name, SecretString: "two", ClientRequestToken: randomUUID() }));
    await sm.send(new PutSecretValueCommand({ SecretId: name, SecretString: "three", ClientRequestToken: randomUUID() }));
    const listed = await sm.send(new ListSecretVersionIdsCommand({ SecretId: name }));
    expect(listed.Versions).toHaveLength(2);
    expect(listed.Versions?.some((v) => v.VersionId === v1)).toBe(false);
    const all = await sm.send(new ListSecretVersionIdsCommand({ SecretId: name, IncludeDeprecated: true }));
    expect(all.Versions).toHaveLength(3);
    expect(all.Versions?.find((v) => v.VersionId === v1)?.VersionStages).toBeUndefined();
  });

  test("PutSecretValue with custom stages leaves AWSCURRENT in place and sorts the returned stages", async () => {
    const { name, versionId: v1 } = await makeSecret("one");
    const out = await sm.send(
      new PutSecretValueCommand({ SecretId: name, SecretString: "two", ClientRequestToken: randomUUID(), VersionStages: ["two", "one", "three"] }),
    );
    expect(out.VersionStages).toEqual(["one", "three", "two"]);
    expect((await sm.send(new GetSecretValueCommand({ SecretId: name }))).VersionId).toBe(v1);
  });

  test("PutSecretValue with AWSPENDING adds a pending version without moving AWSCURRENT", async () => {
    const { name, versionId: v1 } = await makeSecret("one");
    const pending = await sm.send(
      new PutSecretValueCommand({ SecretId: name, SecretString: "next", ClientRequestToken: randomUUID(), VersionStages: ["AWSPENDING"] }),
    );
    const described = await sm.send(new DescribeSecretCommand({ SecretId: name }));
    expect(described.VersionIdsToStages).toEqual({ [v1 ?? ""]: ["AWSCURRENT"], [pending.VersionId ?? ""]: ["AWSPENDING"] });
  });

  test("UpdateSecretVersionStage moves AWSCURRENT, keeps the pending label and demotes the old version", async () => {
    const { name, versionId: v1 } = await makeSecret("one");
    const pending = await sm.send(
      new PutSecretValueCommand({ SecretId: name, SecretString: "next", ClientRequestToken: randomUUID(), VersionStages: ["AWSPENDING"] }),
    );
    const out = await sm.send(
      new UpdateSecretVersionStageCommand({
        SecretId: name,
        VersionStage: "AWSCURRENT",
        RemoveFromVersionId: v1,
        MoveToVersionId: pending.VersionId,
      }),
    );
    expect(Object.keys(out).filter((k) => k !== "$metadata").sort()).toEqual(["ARN", "Name"]);
    const described = await sm.send(new DescribeSecretCommand({ SecretId: name }));
    expect(described.VersionIdsToStages).toEqual({
      [v1 ?? ""]: ["AWSPREVIOUS"],
      [pending.VersionId ?? ""]: ["AWSCURRENT", "AWSPENDING"],
    });
  });

  test("a custom stage label can be attached to and moved between versions", async () => {
    const { name, versionId: v1 } = await makeSecret("one");
    const v2 = (await sm.send(new PutSecretValueCommand({ SecretId: name, SecretString: "two", ClientRequestToken: randomUUID() }))).VersionId;
    await sm.send(new UpdateSecretVersionStageCommand({ SecretId: name, VersionStage: "mine", MoveToVersionId: v1 }));
    await sm.send(new UpdateSecretVersionStageCommand({ SecretId: name, VersionStage: "mine", RemoveFromVersionId: v1, MoveToVersionId: v2 }));
    const out = await sm.send(new GetSecretValueCommand({ SecretId: name, VersionStage: "mine" }));
    expect(out.SecretString).toBe("two");
  });

  test("moving a label that sits on another version without RemoveFromVersionId fails", async () => {
    const { name } = await makeSecret("one");
    const v2 = (await sm.send(new PutSecretValueCommand({ SecretId: name, SecretString: "two", ClientRequestToken: randomUUID(), VersionStages: ["label"] }))).VersionId;
    const v3 = (await sm.send(new PutSecretValueCommand({ SecretId: name, SecretString: "three", ClientRequestToken: randomUUID(), VersionStages: ["other"] }))).VersionId;
    expect(v2).not.toBe(v3);
    // The documented outcome is that the operation fails; the error code is not documented.
    const failure = await awsFailure(sm.send(new UpdateSecretVersionStageCommand({ SecretId: name, VersionStage: "label", MoveToVersionId: v3 })));
    expect(failure.status).toBe(400);
  });

  test("the same token with the same value replays PutSecretValue idempotently", async () => {
    const { name } = await makeSecret("one");
    const token = randomUUID();
    const first = await sm.send(new PutSecretValueCommand({ SecretId: name, SecretString: "two", ClientRequestToken: token }));
    const second = await sm.send(new PutSecretValueCommand({ SecretId: name, SecretString: "two", ClientRequestToken: token }));
    expect(second.VersionId).toBe(first.VersionId);
    expect((await sm.send(new ListSecretVersionIdsCommand({ SecretId: name }))).Versions).toHaveLength(2);
  });

  test("the same token with a different value fails ResourceExistsException", async () => {
    const { name } = await makeSecret("one");
    const token = randomUUID();
    await sm.send(new PutSecretValueCommand({ SecretId: name, SecretString: "two", ClientRequestToken: token }));
    await expectFailure(sm.send(new PutSecretValueCommand({ SecretId: name, SecretString: "different", ClientRequestToken: token })), {
      name: "ResourceExistsException",
      status: 400,
    });
  });

  test("the SDK supplies a ClientRequestToken when the caller omits it, and it becomes the version id", async () => {
    const { name } = await makeSecret("one");
    const out = await sm.send(new PutSecretValueCommand({ SecretId: name, SecretString: "two" }));
    expect(out.VersionId).toMatch(/^[0-9a-f-]{36}$/);
  });

  test("UpdateSecret with a value creates a new version; description-only updates return no VersionId", async () => {
    const { name } = await makeSecret("one");
    const withValue = await sm.send(new UpdateSecretCommand({ SecretId: name, SecretString: "two" }));
    expect(withValue.VersionId).toBeDefined();
    const described = await sm.send(new UpdateSecretCommand({ SecretId: name, Description: "described" }));
    expect(described.VersionId).toBeUndefined();
    expect((await sm.send(new DescribeSecretCommand({ SecretId: name }))).Description).toBe("described");
    expect((await sm.send(new GetSecretValueCommand({ SecretId: name }))).SecretString).toBe("two");
  });

  test("ListSecretVersionIds lists versions with their stages and the default key marker", async () => {
    const { name, versionId } = await makeSecret("one");
    const out = await sm.send(new ListSecretVersionIdsCommand({ SecretId: name }));
    expect(out.Name).toBe(name);
    expect(out.Versions).toHaveLength(1);
    expect(out.Versions?.[0]).toMatchObject({ VersionId: versionId, VersionStages: ["AWSCURRENT"], KmsKeyIds: ["DefaultEncryptionKey"] });
  });
});

describe("Secrets Manager: describe, list and tags", () => {
  test("DescribeSecret on a fresh secret returns only the fields that have a value", async () => {
    const { name, arn } = await makeSecret("v");
    const out = await sm.send(new DescribeSecretCommand({ SecretId: name }));
    expect(Object.keys(out).filter((k) => k !== "$metadata").sort()).toEqual(["ARN", "CreatedDate", "LastChangedDate", "Name", "VersionIdsToStages"]);
    expect(out.ARN).toBe(arn);
  });

  test("LastAccessedDate appears after a GetSecretValue", async () => {
    const { name } = await makeSecret("v");
    expect((await sm.send(new DescribeSecretCommand({ SecretId: name }))).LastAccessedDate).toBeUndefined();
    await sm.send(new GetSecretValueCommand({ SecretId: name }));
    expect((await sm.send(new DescribeSecretCommand({ SecretId: name }))).LastAccessedDate).toBeInstanceOf(Date);
  });

  test("DescribeSecret on an unknown secret fails ResourceNotFoundException", async () => {
    await expectFailure(sm.send(new DescribeSecretCommand({ SecretId: uniq("missing") })), { name: "ResourceNotFoundException", status: 400 });
  });

  test("tags at creation are described; TagResource adds, UntagResource removes, and the last removal leaves an empty Tags list", async () => {
    const { name } = await makeSecret("v", { Tags: [{ Key: "env", Value: "test" }] });
    expect((await sm.send(new DescribeSecretCommand({ SecretId: name }))).Tags).toEqual([{ Key: "env", Value: "test" }]);
    await sm.send(new TagResourceCommand({ SecretId: name, Tags: [{ Key: "Env", Value: "case-sensitive" }] }));
    const tags = (await sm.send(new DescribeSecretCommand({ SecretId: name }))).Tags ?? [];
    expect(tags.map((t) => t.Key).sort()).toEqual(["Env", "env"]);
    await sm.send(new UntagResourceCommand({ SecretId: name, TagKeys: ["Env", "env"] }));
    expect((await sm.send(new DescribeSecretCommand({ SecretId: name }))).Tags).toEqual([]);
  });

  test("ListSecrets filters by name prefix and returns SecretVersionsToStages", async () => {
    const prefix = uniq("list");
    const a = await makeSecret("a", { Name: `${prefix}-a` });
    await makeSecret("b", { Name: `${prefix}-b` });
    await makeSecret("c", { Name: uniq("unrelated") });
    const out = await sm.send(new ListSecretsCommand({ Filters: [{ Key: "name", Values: [prefix] }], SortOrder: "asc" }));
    expect(out.SecretList?.map((s) => s.Name)).toEqual([`${prefix}-a`, `${prefix}-b`]);
    expect(out.SecretList?.[0]?.ARN).toBe(a.arn);
    expect(out.SecretList?.[0]?.SecretVersionsToStages).toBeDefined();
  });

  test("ListSecrets pages with MaxResults and NextToken", async () => {
    const prefix = uniq("page");
    for (const suffix of ["1", "2", "3"]) await makeSecret("v", { Name: `${prefix}-${suffix}` });
    const first = await sm.send(new ListSecretsCommand({ Filters: [{ Key: "name", Values: [prefix] }], MaxResults: 2 }));
    expect(first.SecretList).toHaveLength(2);
    expect(first.NextToken).toBeDefined();
    const second = await sm.send(new ListSecretsCommand({ Filters: [{ Key: "name", Values: [prefix] }], MaxResults: 2, NextToken: first.NextToken }));
    expect(second.SecretList).toHaveLength(1);
    expect(second.NextToken).toBeUndefined();
  });

  test("ListSecrets filters by tag key", async () => {
    const tagKey = uniq("tk");
    const tagged = await makeSecret("v", { Tags: [{ Key: tagKey, Value: "x" }] });
    await makeSecret("v");
    const out = await sm.send(new ListSecretsCommand({ Filters: [{ Key: "tag-key", Values: [tagKey] }] }));
    expect(out.SecretList?.map((s) => s.Name)).toEqual([tagged.name]);
  });
});

describe("Secrets Manager: deletion and restore", () => {
  test("DeleteSecret schedules deletion RecoveryWindowInDays from now and blocks reads", async () => {
    const { name, arn } = await makeSecret("v");
    const before = Date.now();
    const out = await sm.send(new DeleteSecretCommand({ SecretId: name, RecoveryWindowInDays: 7 }));
    expect(out.ARN).toBe(arn);
    expect(out.Name).toBe(name);
    const deletionMs = out.DeletionDate?.getTime() ?? 0;
    expect(deletionMs).toBeGreaterThanOrEqual(before + 7 * 86_400_000 - 5_000);
    expect(deletionMs).toBeLessThanOrEqual(Date.now() + 7 * 86_400_000 + 5_000);
    await expectFailure(sm.send(new GetSecretValueCommand({ SecretId: name })), {
      name: "InvalidRequestException",
      status: 400,
      messageIncludes: "marked for deletion",
    });
  });

  test("the default recovery window is 30 days", async () => {
    const { name } = await makeSecret("v");
    const out = await sm.send(new DeleteSecretCommand({ SecretId: name }));
    const days = ((out.DeletionDate?.getTime() ?? 0) - Date.now()) / 86_400_000;
    expect(days).toBeGreaterThan(29.9);
    expect(days).toBeLessThan(30.1);
  });

  test("DescribeSecret works on a secret scheduled for deletion and reports DeletedDate", async () => {
    const { name } = await makeSecret("v");
    await sm.send(new DeleteSecretCommand({ SecretId: name, RecoveryWindowInDays: 7 }));
    expect((await sm.send(new DescribeSecretCommand({ SecretId: name }))).DeletedDate).toBeInstanceOf(Date);
  });

  test("a secret scheduled for deletion is hidden from ListSecrets unless IncludePlannedDeletion", async () => {
    const { name } = await makeSecret("v");
    await sm.send(new DeleteSecretCommand({ SecretId: name, RecoveryWindowInDays: 7 }));
    const hidden = await sm.send(new ListSecretsCommand({ Filters: [{ Key: "name", Values: [name] }] }));
    expect(hidden.SecretList ?? []).toEqual([]);
    const shown = await sm.send(new ListSecretsCommand({ Filters: [{ Key: "name", Values: [name] }], IncludePlannedDeletion: true }));
    expect(shown.SecretList?.map((s) => s.Name)).toEqual([name]);
  });

  test("writes and tag changes on a secret scheduled for deletion fail InvalidRequestException", async () => {
    const { name } = await makeSecret("v");
    await sm.send(new DeleteSecretCommand({ SecretId: name, RecoveryWindowInDays: 7 }));
    await expectFailure(sm.send(new PutSecretValueCommand({ SecretId: name, SecretString: "x", ClientRequestToken: randomUUID() })), {
      name: "InvalidRequestException",
      status: 400,
    });
    await expectFailure(sm.send(new TagResourceCommand({ SecretId: name, Tags: [{ Key: "a", Value: "b" }] })), {
      name: "InvalidRequestException",
      status: 400,
      messageIncludes: "You can't perform this operation on the secret because it was marked for deletion.",
    });
  });

  test("creating a secret whose name is in its deletion window fails InvalidRequestException", async () => {
    const { name } = await makeSecret("v");
    await sm.send(new DeleteSecretCommand({ SecretId: name, RecoveryWindowInDays: 7 }));
    await expectFailure(sm.send(new CreateSecretCommand({ Name: name, SecretString: "again", ClientRequestToken: randomUUID() })), {
      name: "InvalidRequestException",
      status: 400,
      messageIncludes: "You can't create this secret because a secret with this name is already scheduled for deletion.",
    });
  });

  test("RestoreSecret clears the deletion and the secret reads again", async () => {
    const { name, arn } = await makeSecret("restored");
    await sm.send(new DeleteSecretCommand({ SecretId: name, RecoveryWindowInDays: 7 }));
    const out = await sm.send(new RestoreSecretCommand({ SecretId: name }));
    expect(out).toMatchObject({ ARN: arn, Name: name });
    expect((await sm.send(new DescribeSecretCommand({ SecretId: name }))).DeletedDate).toBeUndefined();
    expect((await sm.send(new GetSecretValueCommand({ SecretId: name }))).SecretString).toBe("restored");
  });

  test("ForceDeleteWithoutRecovery removes the secret at once and frees the name", async () => {
    const { name, arn } = await makeSecret("v");
    const out = await sm.send(new DeleteSecretCommand({ SecretId: name, ForceDeleteWithoutRecovery: true }));
    expect(out.ARN).toBe(arn);
    await expectFailure(sm.send(new DescribeSecretCommand({ SecretId: name })), { name: "ResourceNotFoundException", status: 400 });
    const recreated = await sm.send(new CreateSecretCommand({ Name: name, SecretString: "new", ClientRequestToken: randomUUID() }));
    expect(recreated.ARN).not.toBe(arn);
  });

  test("force-deleting a secret that is already scheduled for deletion succeeds", async () => {
    const { name } = await makeSecret("v");
    await sm.send(new DeleteSecretCommand({ SecretId: name, RecoveryWindowInDays: 7 }));
    const out = await sm.send(new DeleteSecretCommand({ SecretId: name, ForceDeleteWithoutRecovery: true }));
    expect(out.Name).toBe(name);
  });

  test("RecoveryWindowInDays together with ForceDeleteWithoutRecovery fails InvalidParameterException", async () => {
    const { name } = await makeSecret("v");
    await expectFailure(sm.send(new DeleteSecretCommand({ SecretId: name, RecoveryWindowInDays: 7, ForceDeleteWithoutRecovery: true })), {
      name: "InvalidParameterException",
      status: 400,
    });
  });

  test("a RecoveryWindowInDays outside 7 to 30 fails InvalidParameterException", async () => {
    const { name } = await makeSecret("v");
    for (const days of [6, 31]) {
      await expectFailure(sm.send(new DeleteSecretCommand({ SecretId: name, RecoveryWindowInDays: days })), {
        name: "InvalidParameterException",
        status: 400,
      });
    }
  });
});

describe("Secrets Manager: resource policies and KMS", () => {
  const policyFor = (arn: string) =>
    JSON.stringify({
      Version: "2012-10-17",
      Statement: [{ Effect: "Allow", Principal: { AWS: `arn:aws:iam::${defaultAccount}:root` }, Action: "secretsmanager:GetSecretValue", Resource: "*" }],
    });

  test("a resource policy round trips and DeleteResourcePolicy removes it", async () => {
    const { name, arn } = await makeSecret("v");
    expect((await sm.send(new GetResourcePolicyCommand({ SecretId: name }))).ResourcePolicy).toBeUndefined();
    const put = await sm.send(new PutResourcePolicyCommand({ SecretId: name, ResourcePolicy: policyFor(arn) }));
    expect(put).toMatchObject({ ARN: arn, Name: name });
    const got = await sm.send(new GetResourcePolicyCommand({ SecretId: name }));
    expect(JSON.parse(got.ResourcePolicy ?? "null")).toEqual(JSON.parse(policyFor(arn)));
    const del = await sm.send(new DeleteResourcePolicyCommand({ SecretId: name }));
    expect(del).toMatchObject({ ARN: arn, Name: name });
    expect((await sm.send(new GetResourcePolicyCommand({ SecretId: name }))).ResourcePolicy).toBeUndefined();
  });

  test("a malformed resource policy fails MalformedPolicyDocumentException", async () => {
    const { name } = await makeSecret("v");
    await expectFailure(sm.send(new PutResourcePolicyCommand({ SecretId: name, ResourcePolicy: "not json" })), {
      name: "MalformedPolicyDocumentException",
      status: 400,
    });
  });

  test("a secret encrypted with a customer KMS key reads back and reports the key", async () => {
    const key = await kms.send(new CreateKeyCommand({}));
    const keyArn = key.KeyMetadata?.Arn ?? "";
    const { name } = await makeSecret("kms-protected", { KmsKeyId: keyArn });
    expect((await sm.send(new GetSecretValueCommand({ SecretId: name }))).SecretString).toBe("kms-protected");
    expect((await sm.send(new DescribeSecretCommand({ SecretId: name }))).KmsKeyId).toBe(keyArn);
  });

  test("a secret created with a KMS key that does not exist is refused with a client error", async () => {
    // The documented outcome is a refusal; the DOC names no single error code for a missing key.
    const failure = await awsFailure(
      sm.send(new CreateSecretCommand({ Name: uniq("badkms"), SecretString: "v", KmsKeyId: `arn:aws:kms:${region}:${defaultAccount}:key/00000000-0000-4000-8000-000000000000` })),
    );
    expect(failure.status).toBe(400);
  });
});
