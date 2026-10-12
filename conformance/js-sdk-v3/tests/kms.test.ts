// KMS over awsJson1_1, through @aws-sdk/client-kms.
// Expected behavior: docs/research/phase1/iam-sts-kms.md section 5.

import { describe, expect, test } from "bun:test";
import {
  CancelKeyDeletionCommand,
  CreateAliasCommand,
  CreateKeyCommand,
  DecryptCommand,
  DeleteAliasCommand,
  DescribeKeyCommand,
  DisableKeyCommand,
  DisableKeyRotationCommand,
  EnableKeyCommand,
  EnableKeyRotationCommand,
  EncryptCommand,
  GenerateDataKeyCommand,
  GenerateDataKeyWithoutPlaintextCommand,
  GenerateRandomCommand,
  GetKeyPolicyCommand,
  GetKeyRotationStatusCommand,
  KMSClient,
  ListAliasesCommand,
  ListKeyPoliciesCommand,
  ListKeysCommand,
  ListResourceTagsCommand,
  PutKeyPolicyCommand,
  ReEncryptCommand,
  ScheduleKeyDeletionCommand,
  TagResourceCommand,
  UntagResourceCommand,
  UpdateAliasCommand,
  UpdateKeyDescriptionCommand,
} from "@aws-sdk/client-kms";
import { clientConfig, defaultAccount, region } from "../support/env";
import { awsFailure, expectFailure, uniq } from "../support/helpers";

const kms = new KMSClient(clientConfig());
const utf8 = new TextEncoder();
const decode = (bytes: Uint8Array | undefined) => new TextDecoder().decode(bytes);

async function makeKey(extra: Partial<ConstructorParameters<typeof CreateKeyCommand>[0]> = {}) {
  const out = await kms.send(new CreateKeyCommand(extra));
  const meta = out.KeyMetadata;
  if (meta?.KeyId === undefined || meta.Arn === undefined) throw new Error("CreateKey returned no key metadata");
  return { id: meta.KeyId, arn: meta.Arn, meta };
}

const aliasName = () => `alias/${uniq("conf")}`;

describe("KMS keys", () => {
  test("CreateKey returns the documented metadata for a default key", async () => {
    const { id, arn, meta } = await makeKey();
    expect(id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
    expect(arn).toBe(`arn:aws:kms:${region}:${defaultAccount}:key/${id}`);
    expect(meta).toMatchObject({
      AWSAccountId: defaultAccount,
      Enabled: true,
      KeyState: "Enabled",
      KeyUsage: "ENCRYPT_DECRYPT",
      KeySpec: "SYMMETRIC_DEFAULT",
      CustomerMasterKeySpec: "SYMMETRIC_DEFAULT",
      KeyManager: "CUSTOMER",
      Origin: "AWS_KMS",
      MultiRegion: false,
      Description: "",
      EncryptionAlgorithms: ["SYMMETRIC_DEFAULT"],
    });
    expect(meta.CreationDate).toBeInstanceOf(Date);
    expect(meta.DeletionDate).toBeUndefined();
    expect(meta.CurrentKeyMaterialId).toMatch(/^[0-9a-f]{64}$/);
  });

  test("CreateKey with a description and tags is reflected by DescribeKey and ListResourceTags", async () => {
    const { id } = await makeKey({ Description: "my key", Tags: [{ TagKey: "env", TagValue: "test" }, { TagKey: "empty", TagValue: "" }] });
    expect((await kms.send(new DescribeKeyCommand({ KeyId: id }))).KeyMetadata?.Description).toBe("my key");
    const tags = await kms.send(new ListResourceTagsCommand({ KeyId: id }));
    expect(Object.fromEntries((tags.Tags ?? []).map((t) => [t.TagKey, t.TagValue]))).toEqual({ env: "test", empty: "" });
    expect(tags.Truncated).toBe(false);
  });

  test("DescribeKey accepts the key id, the key ARN and an alias", async () => {
    const { id, arn } = await makeKey();
    const alias = aliasName();
    await kms.send(new CreateAliasCommand({ AliasName: alias, TargetKeyId: id }));
    for (const keyId of [id, arn, alias, `arn:aws:kms:${region}:${defaultAccount}:${alias}`]) {
      expect((await kms.send(new DescribeKeyCommand({ KeyId: keyId }))).KeyMetadata?.Arn).toBe(arn);
    }
  });

  test("DescribeKey on a key id that does not exist fails NotFoundException with the key ARN in the message", async () => {
    const missing = "00000000-0000-4000-8000-000000000000";
    await expectFailure(kms.send(new DescribeKeyCommand({ KeyId: missing })), {
      name: "NotFoundException",
      status: 400,
      messageIncludes: `Key 'arn:aws:kms:${region}:${defaultAccount}:key/${missing}' does not exist`,
    });
  });

  test("DescribeKey on a missing alias fails NotFoundException", async () => {
    await expectFailure(kms.send(new DescribeKeyCommand({ KeyId: aliasName() })), { name: "NotFoundException", status: 400 });
  });

  test("UpdateKeyDescription changes the description", async () => {
    const { id } = await makeKey({ Description: "before" });
    await kms.send(new UpdateKeyDescriptionCommand({ KeyId: id, Description: "after" }));
    expect((await kms.send(new DescribeKeyCommand({ KeyId: id }))).KeyMetadata?.Description).toBe("after");
  });

  test("ListKeys includes created keys with their ARNs", async () => {
    const { id, arn } = await makeKey();
    let marker: string | undefined;
    let found = false;
    do {
      const out = await kms.send(new ListKeysCommand({ Marker: marker, Limit: 1000 }));
      if (out.Keys?.some((k) => k.KeyId === id && k.KeyArn === arn)) found = true;
      marker = out.Truncated ? out.NextMarker : undefined;
    } while (marker !== undefined && !found);
    expect(found).toBe(true);
  });

  test("an unknown KeySpec fails ValidationException with the enum set", async () => {
    const failure = await expectFailure(kms.send(new CreateKeyCommand({ KeySpec: "HMAC_random" as never })), {
      name: "ValidationException",
      status: 400,
      messageIncludes: "Value 'HMAC_random' at 'keySpec' failed to satisfy constraint: Member must satisfy enum value set: [",
    });
    expect(failure.message.startsWith("1 validation error detected")).toBe(true);
  });

  test("a KeyUsage that is not compatible with the KeySpec fails ValidationException", async () => {
    await expectFailure(kms.send(new CreateKeyCommand({ KeySpec: "RSA_2048", KeyUsage: "KEY_AGREEMENT" })), {
      name: "ValidationException",
      status: 400,
      messageIncludes: "KeyUsage KEY_AGREEMENT is not compatible with KeySpec RSA_2048",
    });
  });
});

describe("KMS key policies", () => {
  test("the default key policy has one statement granting the account root kms:*", async () => {
    const { id } = await makeKey();
    const out = await kms.send(new GetKeyPolicyCommand({ KeyId: id, PolicyName: "default" }));
    const policy = JSON.parse(out.Policy ?? "null");
    expect(policy.Version).toBe("2012-10-17");
    expect(policy.Id).toBe("key-default-1");
    expect(policy.Statement).toHaveLength(1);
    expect(policy.Statement[0]).toMatchObject({
      Sid: "Enable IAM User Permissions",
      Effect: "Allow",
      Action: "kms:*",
      Resource: "*",
    });
    const principal = policy.Statement[0].Principal.AWS;
    expect([principal].flat()).toEqual([`arn:aws:iam::${defaultAccount}:root`]);
  });

  test("a policy supplied at CreateKey is stored and PutKeyPolicy replaces it", async () => {
    const policy = (sid: string) =>
      JSON.stringify({
        Version: "2012-10-17",
        Id: "custom",
        Statement: [{ Sid: sid, Effect: "Allow", Principal: { AWS: `arn:aws:iam::${defaultAccount}:root` }, Action: "kms:*", Resource: "*" }],
      });
    const { id } = await makeKey({ Policy: policy("first") });
    expect(JSON.parse((await kms.send(new GetKeyPolicyCommand({ KeyId: id, PolicyName: "default" }))).Policy ?? "null")).toEqual(JSON.parse(policy("first")));
    await kms.send(new PutKeyPolicyCommand({ KeyId: id, PolicyName: "default", Policy: policy("second") }));
    expect(JSON.parse((await kms.send(new GetKeyPolicyCommand({ KeyId: id, PolicyName: "default" }))).Policy ?? "null")).toEqual(JSON.parse(policy("second")));
  });

  test("ListKeyPolicies returns only the default policy", async () => {
    const { id } = await makeKey();
    const out = await kms.send(new ListKeyPoliciesCommand({ KeyId: id }));
    expect(out.PolicyNames).toEqual(["default"]);
    expect(out.Truncated).toBe(false);
  });
});

describe("KMS aliases", () => {
  test("CreateAlias then ListAliases shows the alias with its target, ARN and dates", async () => {
    const { id } = await makeKey();
    const alias = aliasName();
    await kms.send(new CreateAliasCommand({ AliasName: alias, TargetKeyId: id }));
    const out = await kms.send(new ListAliasesCommand({ KeyId: id }));
    expect(out.Aliases).toHaveLength(1);
    expect(out.Aliases?.[0]).toMatchObject({
      AliasName: alias,
      AliasArn: `arn:aws:kms:${region}:${defaultAccount}:${alias}`,
      TargetKeyId: id,
    });
    expect(out.Aliases?.[0]?.CreationDate).toBeInstanceOf(Date);
    expect(out.Aliases?.[0]?.LastUpdatedDate).toBeInstanceOf(Date);
  });

  test("CreateAlias on an existing name fails AlreadyExistsException", async () => {
    const { id } = await makeKey();
    const alias = aliasName();
    await kms.send(new CreateAliasCommand({ AliasName: alias, TargetKeyId: id }));
    await expectFailure(kms.send(new CreateAliasCommand({ AliasName: alias, TargetKeyId: id })), {
      name: "AlreadyExistsException",
      status: 409,
      messageIncludes: `An alias with the name arn:aws:kms:${region}:${defaultAccount}:${alias} already exists`,
    });
  });

  test("CreateAlias without the alias/ prefix fails ValidationException", async () => {
    const { id } = await makeKey();
    await expectFailure(kms.send(new CreateAliasCommand({ AliasName: uniq("noprefix"), TargetKeyId: id })), {
      name: "ValidationException",
      status: 400,
      messageIncludes: 'Alias must start with the prefix "alias/". Please see https://docs.aws.amazon.com/kms/latest/developerguide/kms-alias.html',
    });
  });

  test("CreateAlias in the reserved alias/aws/ namespace is refused", async () => {
    const { id } = await makeKey();
    const failure = await awsFailure(kms.send(new CreateAliasCommand({ AliasName: `alias/aws/${uniq("x")}`, TargetKeyId: id })));
    expect(failure.status).toBe(400);
  });

  test("CreateAlias targeting a missing key fails NotFoundException", async () => {
    await expectFailure(kms.send(new CreateAliasCommand({ AliasName: aliasName(), TargetKeyId: "00000000-0000-4000-8000-000000000000" })), {
      name: "NotFoundException",
      status: 400,
    });
  });

  test("UpdateAlias retargets the alias and DeleteAlias removes it", async () => {
    const a = await makeKey();
    const b = await makeKey();
    const alias = aliasName();
    await kms.send(new CreateAliasCommand({ AliasName: alias, TargetKeyId: a.id }));
    await kms.send(new UpdateAliasCommand({ AliasName: alias, TargetKeyId: b.id }));
    expect((await kms.send(new DescribeKeyCommand({ KeyId: alias }))).KeyMetadata?.KeyId).toBe(b.id);
    await kms.send(new DeleteAliasCommand({ AliasName: alias }));
    await expectFailure(kms.send(new DescribeKeyCommand({ KeyId: alias })), { name: "NotFoundException", status: 400 });
  });

  test("DeleteAlias of a missing alias fails NotFoundException", async () => {
    await expectFailure(kms.send(new DeleteAliasCommand({ AliasName: aliasName() })), { name: "NotFoundException", status: 400 });
  });
});

describe("KMS key state and deletion", () => {
  test("DisableKey and EnableKey flip Enabled and KeyState", async () => {
    const { id } = await makeKey();
    await kms.send(new DisableKeyCommand({ KeyId: id }));
    expect((await kms.send(new DescribeKeyCommand({ KeyId: id }))).KeyMetadata).toMatchObject({ Enabled: false, KeyState: "Disabled" });
    await kms.send(new EnableKeyCommand({ KeyId: id }));
    expect((await kms.send(new DescribeKeyCommand({ KeyId: id }))).KeyMetadata).toMatchObject({ Enabled: true, KeyState: "Enabled" });
  });

  test("cryptographic operations on a disabled key fail DisabledException naming the key ARN", async () => {
    const { id, arn } = await makeKey();
    await kms.send(new DisableKeyCommand({ KeyId: id }));
    for (const call of [
      kms.send(new EncryptCommand({ KeyId: id, Plaintext: utf8.encode("x") })),
      kms.send(new GenerateDataKeyCommand({ KeyId: id, KeySpec: "AES_256" })),
    ]) {
      await expectFailure(call, { name: "DisabledException", status: 400, messageIncludes: `${arn} is disabled.` });
    }
  });

  test("ScheduleKeyDeletion moves the key to PendingDeletion with a DeletionDate window days ahead", async () => {
    const { id, arn } = await makeKey();
    const before = Date.now();
    const out = await kms.send(new ScheduleKeyDeletionCommand({ KeyId: id, PendingWindowInDays: 7 }));
    expect(out).toMatchObject({ KeyId: arn, KeyState: "PendingDeletion", PendingWindowInDays: 7 });
    const deletion = out.DeletionDate?.getTime() ?? 0;
    expect(deletion).toBeGreaterThanOrEqual(before + 7 * 86_400_000 - 5_000);
    expect(deletion).toBeLessThanOrEqual(Date.now() + 7 * 86_400_000 + 5_000);
    const meta = (await kms.send(new DescribeKeyCommand({ KeyId: id }))).KeyMetadata;
    expect(meta).toMatchObject({ KeyState: "PendingDeletion", Enabled: false });
    expect(meta?.DeletionDate?.getTime()).toBe(deletion);
  });

  test("the default deletion window is 30 days", async () => {
    const { id } = await makeKey();
    const out = await kms.send(new ScheduleKeyDeletionCommand({ KeyId: id }));
    expect(out.PendingWindowInDays).toBe(30);
  });

  test("a deletion window outside 7 to 30 days fails ValidationException", async () => {
    const { id } = await makeKey();
    for (const days of [6, 31]) {
      await expectFailure(kms.send(new ScheduleKeyDeletionCommand({ KeyId: id, PendingWindowInDays: days })), {
        name: "ValidationException",
        status: 400,
      });
    }
  });

  test("operations on a key pending deletion fail KMSInvalidStateException naming the key ARN", async () => {
    const { id, arn } = await makeKey();
    await kms.send(new ScheduleKeyDeletionCommand({ KeyId: id, PendingWindowInDays: 7 }));
    await expectFailure(kms.send(new EncryptCommand({ KeyId: id, Plaintext: utf8.encode("x") })), {
      name: "KMSInvalidStateException",
      status: 400,
      messageIncludes: `${arn} is pending deletion`,
    });
    await expectFailure(kms.send(new ScheduleKeyDeletionCommand({ KeyId: id, PendingWindowInDays: 7 })), {
      name: "KMSInvalidStateException",
      status: 400,
      messageIncludes: "is pending deletion",
    });
    await expectFailure(kms.send(new EnableKeyCommand({ KeyId: id })), { name: "KMSInvalidStateException", status: 400 });
  });

  test("CancelKeyDeletion returns the key ARN and leaves the key Disabled", async () => {
    const { id, arn } = await makeKey();
    await kms.send(new ScheduleKeyDeletionCommand({ KeyId: id, PendingWindowInDays: 7 }));
    const out = await kms.send(new CancelKeyDeletionCommand({ KeyId: id }));
    expect(out.KeyId).toBe(arn);
    expect((await kms.send(new DescribeKeyCommand({ KeyId: id }))).KeyMetadata).toMatchObject({ KeyState: "Disabled", Enabled: false });
  });

  test("CancelKeyDeletion on a key that is not pending deletion fails KMSInvalidStateException", async () => {
    const { id, arn } = await makeKey();
    await expectFailure(kms.send(new CancelKeyDeletionCommand({ KeyId: id })), {
      name: "KMSInvalidStateException",
      status: 400,
      messageIncludes: `${arn} is not pending deletion`,
    });
  });

  test("DescribeKey, GetKeyPolicy and ListResourceTags keep working on a key pending deletion", async () => {
    const { id } = await makeKey();
    await kms.send(new ScheduleKeyDeletionCommand({ KeyId: id, PendingWindowInDays: 7 }));
    await kms.send(new DescribeKeyCommand({ KeyId: id }));
    await kms.send(new GetKeyPolicyCommand({ KeyId: id, PolicyName: "default" }));
    await kms.send(new ListResourceTagsCommand({ KeyId: id }));
  });
});

describe("KMS rotation and tags", () => {
  test("GetKeyRotationStatus is false until rotation is enabled, then reports the period", async () => {
    const { id } = await makeKey();
    expect((await kms.send(new GetKeyRotationStatusCommand({ KeyId: id }))).KeyRotationEnabled).toBe(false);
    await kms.send(new EnableKeyRotationCommand({ KeyId: id }));
    const enabled = await kms.send(new GetKeyRotationStatusCommand({ KeyId: id }));
    expect(enabled.KeyRotationEnabled).toBe(true);
    expect(enabled.RotationPeriodInDays).toBe(365);
    await kms.send(new DisableKeyRotationCommand({ KeyId: id }));
    expect((await kms.send(new GetKeyRotationStatusCommand({ KeyId: id }))).KeyRotationEnabled).toBe(false);
  });

  test("EnableKeyRotation with a custom period stores it and rejects a period outside 90 to 2560", async () => {
    const { id } = await makeKey();
    await kms.send(new EnableKeyRotationCommand({ KeyId: id, RotationPeriodInDays: 90 }));
    expect((await kms.send(new GetKeyRotationStatusCommand({ KeyId: id }))).RotationPeriodInDays).toBe(90);
    await expectFailure(kms.send(new EnableKeyRotationCommand({ KeyId: id, RotationPeriodInDays: 89 })), { name: "ValidationException", status: 400 });
  });

  test("EnableKeyRotation on a disabled key fails DisabledException", async () => {
    const { id, arn } = await makeKey();
    await kms.send(new DisableKeyCommand({ KeyId: id }));
    await expectFailure(kms.send(new EnableKeyRotationCommand({ KeyId: id })), {
      name: "DisabledException",
      status: 400,
      messageIncludes: `${arn} is disabled.`,
    });
  });

  test("a pending-deletion key reports rotation false", async () => {
    const { id } = await makeKey();
    await kms.send(new EnableKeyRotationCommand({ KeyId: id }));
    await kms.send(new ScheduleKeyDeletionCommand({ KeyId: id, PendingWindowInDays: 7 }));
    expect((await kms.send(new GetKeyRotationStatusCommand({ KeyId: id }))).KeyRotationEnabled).toBe(false);
  });

  test("TagResource replaces by key, is case-sensitive, and UntagResource removes", async () => {
    const { id } = await makeKey({ Tags: [{ TagKey: "a", TagValue: "1" }] });
    await kms.send(new TagResourceCommand({ KeyId: id, Tags: [{ TagKey: "a", TagValue: "2" }, { TagKey: "A", TagValue: "3" }] }));
    const tags = (await kms.send(new ListResourceTagsCommand({ KeyId: id }))).Tags ?? [];
    expect(Object.fromEntries(tags.map((t) => [t.TagKey, t.TagValue]))).toEqual({ a: "2", A: "3" });
    await kms.send(new UntagResourceCommand({ KeyId: id, TagKeys: ["a"] }));
    expect((await kms.send(new ListResourceTagsCommand({ KeyId: id }))).Tags).toEqual([{ TagKey: "A", TagValue: "3" }]);
  });

  test("a tag key longer than 128 characters fails ValidationException", async () => {
    const { id } = await makeKey();
    await expectFailure(kms.send(new TagResourceCommand({ KeyId: id, Tags: [{ TagKey: "k".repeat(129), TagValue: "v" }] })), {
      name: "ValidationException",
      status: 400,
    });
  });
});

describe("KMS cryptography", () => {
  test("Encrypt then Decrypt returns the plaintext and the key ARN", async () => {
    const { id, arn } = await makeKey();
    const enc = await kms.send(new EncryptCommand({ KeyId: id, Plaintext: utf8.encode("attack at dawn") }));
    expect(enc.KeyId).toBe(arn);
    expect(enc.EncryptionAlgorithm).toBe("SYMMETRIC_DEFAULT");
    expect(enc.CiphertextBlob?.length ?? 0).toBeGreaterThan("attack at dawn".length);
    expect(decode(enc.CiphertextBlob)).not.toContain("attack at dawn");
    const dec = await kms.send(new DecryptCommand({ CiphertextBlob: enc.CiphertextBlob }));
    expect(decode(dec.Plaintext)).toBe("attack at dawn");
    expect(dec.KeyId).toBe(arn);
  });

  test("Encrypt works through an alias and the ciphertext decrypts without naming a key", async () => {
    const { id, arn } = await makeKey();
    const alias = aliasName();
    await kms.send(new CreateAliasCommand({ AliasName: alias, TargetKeyId: id }));
    const enc = await kms.send(new EncryptCommand({ KeyId: alias, Plaintext: utf8.encode("via alias") }));
    expect(enc.KeyId).toBe(arn);
    expect(decode((await kms.send(new DecryptCommand({ CiphertextBlob: enc.CiphertextBlob }))).Plaintext)).toBe("via alias");
  });

  test("encrypting the same plaintext twice gives different ciphertexts", async () => {
    const { id } = await makeKey();
    const one = await kms.send(new EncryptCommand({ KeyId: id, Plaintext: utf8.encode("same") }));
    const two = await kms.send(new EncryptCommand({ KeyId: id, Plaintext: utf8.encode("same") }));
    expect(Buffer.from(one.CiphertextBlob ?? []).equals(Buffer.from(two.CiphertextBlob ?? []))).toBe(false);
  });

  test("Decrypt needs the same encryption context, exactly", async () => {
    const { id } = await makeKey();
    const enc = await kms.send(new EncryptCommand({ KeyId: id, Plaintext: utf8.encode("ctx"), EncryptionContext: { purpose: "test" } }));
    const ok = await kms.send(new DecryptCommand({ CiphertextBlob: enc.CiphertextBlob, EncryptionContext: { purpose: "test" } }));
    expect(decode(ok.Plaintext)).toBe("ctx");
    await expectFailure(kms.send(new DecryptCommand({ CiphertextBlob: enc.CiphertextBlob, EncryptionContext: { purpose: "Test" } })), {
      name: "InvalidCiphertextException",
      status: 400,
    });
    await expectFailure(kms.send(new DecryptCommand({ CiphertextBlob: enc.CiphertextBlob })), { name: "InvalidCiphertextException", status: 400 });
  });

  test("Decrypt with a KeyId that is not the encrypting key fails IncorrectKeyException", async () => {
    const a = await makeKey();
    const b = await makeKey();
    const enc = await kms.send(new EncryptCommand({ KeyId: a.id, Plaintext: utf8.encode("x") }));
    await expectFailure(kms.send(new DecryptCommand({ CiphertextBlob: enc.CiphertextBlob, KeyId: b.id })), {
      name: "IncorrectKeyException",
      status: 400,
    });
  });

  test("Decrypt of garbage fails InvalidCiphertextException", async () => {
    await expectFailure(kms.send(new DecryptCommand({ CiphertextBlob: utf8.encode("this is not a ciphertext blob at all") })), {
      name: "InvalidCiphertextException",
      status: 400,
    });
  });

  test("Encrypt accepts 4096 bytes and rejects 4097 with the length constraint message", async () => {
    const { id } = await makeKey();
    await kms.send(new EncryptCommand({ KeyId: id, Plaintext: new Uint8Array(4096).fill(7) }));
    await expectFailure(kms.send(new EncryptCommand({ KeyId: id, Plaintext: new Uint8Array(4097).fill(7) })), {
      name: "ValidationException",
      status: 400,
      messageIncludes: "Value at 'plaintext' failed to satisfy constraint: Member must have length less than or equal to 4096",
    });
  });

  test("Encrypt with a key that does not exist fails NotFoundException", async () => {
    await expectFailure(kms.send(new EncryptCommand({ KeyId: "00000000-0000-4000-8000-000000000000", Plaintext: utf8.encode("x") })), {
      name: "NotFoundException",
      status: 400,
    });
  });

  test("GenerateDataKey returns a plaintext of the requested size and a ciphertext that decrypts to it", async () => {
    const { id, arn } = await makeKey();
    const aes256 = await kms.send(new GenerateDataKeyCommand({ KeyId: id, KeySpec: "AES_256" }));
    expect(aes256.Plaintext).toHaveLength(32);
    expect(aes256.KeyId).toBe(arn);
    expect(Buffer.from((await kms.send(new DecryptCommand({ CiphertextBlob: aes256.CiphertextBlob }))).Plaintext ?? []).equals(Buffer.from(aes256.Plaintext ?? []))).toBe(true);
    expect((await kms.send(new GenerateDataKeyCommand({ KeyId: id, KeySpec: "AES_128" }))).Plaintext).toHaveLength(16);
    expect((await kms.send(new GenerateDataKeyCommand({ KeyId: id, NumberOfBytes: 48 }))).Plaintext).toHaveLength(48);
  });

  test("GenerateDataKey with both KeySpec and NumberOfBytes, or neither, fails ValidationException", async () => {
    const { id } = await makeKey();
    await expectFailure(kms.send(new GenerateDataKeyCommand({ KeyId: id, KeySpec: "AES_256", NumberOfBytes: 32 })), { name: "ValidationException", status: 400 });
    await expectFailure(kms.send(new GenerateDataKeyCommand({ KeyId: id })), { name: "ValidationException", status: 400 });
  });

  test("GenerateDataKeyWithoutPlaintext returns only the ciphertext, which decrypts", async () => {
    const { id } = await makeKey();
    const out = await kms.send(new GenerateDataKeyWithoutPlaintextCommand({ KeyId: id, KeySpec: "AES_256" }));
    expect((out as { Plaintext?: unknown }).Plaintext).toBeUndefined();
    expect((await kms.send(new DecryptCommand({ CiphertextBlob: out.CiphertextBlob }))).Plaintext).toHaveLength(32);
  });

  test("data keys are random: two calls return different plaintexts", async () => {
    const { id } = await makeKey();
    const one = await kms.send(new GenerateDataKeyCommand({ KeyId: id, KeySpec: "AES_256" }));
    const two = await kms.send(new GenerateDataKeyCommand({ KeyId: id, KeySpec: "AES_256" }));
    expect(Buffer.from(one.Plaintext ?? []).equals(Buffer.from(two.Plaintext ?? []))).toBe(false);
  });

  test("ReEncrypt moves a ciphertext to another key", async () => {
    const a = await makeKey();
    const b = await makeKey();
    const enc = await kms.send(new EncryptCommand({ KeyId: a.id, Plaintext: utf8.encode("move me") }));
    const re = await kms.send(new ReEncryptCommand({ CiphertextBlob: enc.CiphertextBlob, DestinationKeyId: b.id }));
    expect(re.SourceKeyId).toBe(a.arn);
    expect(re.KeyId).toBe(b.arn);
    const dec = await kms.send(new DecryptCommand({ CiphertextBlob: re.CiphertextBlob, KeyId: b.id }));
    expect(decode(dec.Plaintext)).toBe("move me");
  });

  test("GenerateRandom returns the requested number of bytes", async () => {
    const out = await kms.send(new GenerateRandomCommand({ NumberOfBytes: 64 }));
    expect(out.Plaintext).toHaveLength(64);
  });

  test("GenerateRandom with 0 bytes fails ValidationException with the range message", async () => {
    await expectFailure(kms.send(new GenerateRandomCommand({ NumberOfBytes: 0 })), {
      name: "ValidationException",
      status: 400,
      messageIncludes: "Value '0' at 'numberOfBytes' failed to satisfy constraint: Member must have value greater than or equal to 1",
    });
  });

  test("a customer key refuses Encrypt after it is scheduled for deletion and works again once cancelled and enabled", async () => {
    const { id } = await makeKey();
    await kms.send(new ScheduleKeyDeletionCommand({ KeyId: id, PendingWindowInDays: 7 }));
    await kms.send(new CancelKeyDeletionCommand({ KeyId: id }));
    await kms.send(new EnableKeyCommand({ KeyId: id }));
    const enc = await kms.send(new EncryptCommand({ KeyId: id, Plaintext: utf8.encode("back") }));
    expect(decode((await kms.send(new DecryptCommand({ CiphertextBlob: enc.CiphertextBlob }))).Plaintext)).toBe("back");
  });
});
