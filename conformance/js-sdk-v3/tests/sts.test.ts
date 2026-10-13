// STS over awsQuery, through @aws-sdk/client-sts.
// Expected behavior: docs/research/phase1/iam-sts-kms.md section 4 and
// docs/adr/0012-identity-and-defaults.md (account derived from the access key).

import { describe, expect, test } from "bun:test";
import { AssumeRoleCommand, GetCallerIdentityCommand, GetSessionTokenCommand, STSClient } from "@aws-sdk/client-sts";
import { CreateRoleCommand, IAMClient } from "@aws-sdk/client-iam";
import { clientConfig, defaultAccount, sigv4Verify } from "../support/env";
import { awsFailure, expectFailure, uniq } from "../support/helpers";

const sts = new STSClient(clientConfig());
const iam = new IAMClient(clientConfig());

const trust = JSON.stringify({
  Version: "2012-10-17",
  Statement: [{ Effect: "Allow", Principal: { AWS: `arn:aws:iam::${defaultAccount}:root` }, Action: "sts:AssumeRole" }],
});

async function makeRole(extra: Partial<ConstructorParameters<typeof CreateRoleCommand>[0]> = {}) {
  const name = uniq("role");
  const out = await iam.send(new CreateRoleCommand({ RoleName: name, AssumeRolePolicyDocument: trust, ...extra }));
  return { name, arn: out.Role?.Arn ?? "", id: out.Role?.RoleId ?? "" };
}

describe("STS GetCallerIdentity", () => {
  test("the default access key is the root of the default account", async () => {
    const out = await sts.send(new GetCallerIdentityCommand({}));
    expect(out).toMatchObject({ Account: defaultAccount, Arn: `arn:aws:iam::${defaultAccount}:root`, UserId: defaultAccount });
  });

  test("a 12-digit numeric access key id is that account's root", async () => {
    const client = new STSClient(clientConfig({ accessKeyId: "123456789012" }));
    const out = await client.send(new GetCallerIdentityCommand({}));
    expect(out).toMatchObject({ Account: "123456789012", Arn: "arn:aws:iam::123456789012:root", UserId: "123456789012" });
  });

  // Under --sigv4 verify a key understudy holds no secret for is refused as AWS
  // refuses an unknown key (ADR 0012 pre-tag review amendment).
  test.skipIf(sigv4Verify)("an arbitrary access key maps to the default account", async () => {
    const client = new STSClient(clientConfig({ accessKeyId: "foobar123" }));
    expect((await client.send(new GetCallerIdentityCommand({}))).Account).toBe(defaultAccount);
  });

  test.skipIf(sigv4Verify)("the documentation example key AKIAIOSFODNN7EXAMPLE is not decoded into a foreign account", async () => {
    const client = new STSClient(clientConfig({ accessKeyId: "AKIAIOSFODNN7EXAMPLE" }));
    expect((await client.send(new GetCallerIdentityCommand({}))).Account).toBe(defaultAccount);
  });

  test.skipIf(!sigv4Verify)("under sigv4 verify a key understudy did not mint is refused as unknown", async () => {
    for (const accessKeyId of ["foobar123", "AKIAIOSFODNN7EXAMPLE"]) {
      const client = new STSClient(clientConfig({ accessKeyId }));
      await expectFailure(client.send(new GetCallerIdentityCommand({})), { name: "InvalidClientTokenId", status: 403 });
    }
  });

  test("the region in the credential scope does not change the answer", async () => {
    const client = new STSClient(clientConfig({ region: "eu-west-1" }));
    expect((await client.send(new GetCallerIdentityCommand({}))).Account).toBe(defaultAccount);
  });
});

describe("STS AssumeRole", () => {
  test("AssumeRole returns an ASIA key, a secret, a token and an expiration an hour ahead", async () => {
    const role = await makeRole();
    const before = Date.now();
    const out = await sts.send(new AssumeRoleCommand({ RoleArn: role.arn, RoleSessionName: "session-one" }));
    expect(out.Credentials?.AccessKeyId).toMatch(/^ASIA[A-Z2-7]{16}$/);
    expect((out.Credentials?.SecretAccessKey ?? "").length).toBeGreaterThanOrEqual(40);
    expect((out.Credentials?.SessionToken ?? "").length).toBeGreaterThan(0);
    const expiresIn = (out.Credentials?.Expiration?.getTime() ?? 0) - before;
    expect(expiresIn).toBeGreaterThan(3_590_000);
    expect(expiresIn).toBeLessThan(3_620_000);
    expect(out.AssumedRoleUser).toEqual({
      Arn: `arn:aws:sts::${defaultAccount}:assumed-role/${role.name}/session-one`,
      AssumedRoleId: `${role.id}:session-one`,
    });
  });

  test("DurationSeconds sets the expiration", async () => {
    const role = await makeRole({ MaxSessionDuration: 7200 });
    const before = Date.now();
    const out = await sts.send(new AssumeRoleCommand({ RoleArn: role.arn, RoleSessionName: "long", DurationSeconds: 7200 }));
    const expiresIn = (out.Credentials?.Expiration?.getTime() ?? 0) - before;
    expect(expiresIn).toBeGreaterThan(7_190_000);
    expect(expiresIn).toBeLessThan(7_220_000);
  });

  test("the minted credentials act as the assumed role in GetCallerIdentity", async () => {
    const role = await makeRole();
    const out = await sts.send(new AssumeRoleCommand({ RoleArn: role.arn, RoleSessionName: "who-am-i" }));
    const assumed = new STSClient({
      ...clientConfig(),
      credentials: {
        accessKeyId: out.Credentials?.AccessKeyId ?? "",
        secretAccessKey: out.Credentials?.SecretAccessKey ?? "",
        sessionToken: out.Credentials?.SessionToken,
      },
    });
    const identity = await assumed.send(new GetCallerIdentityCommand({}));
    expect(identity).toEqual(
      expect.objectContaining({
        Account: defaultAccount,
        Arn: `arn:aws:sts::${defaultAccount}:assumed-role/${role.name}/who-am-i`,
        UserId: `${role.id}:who-am-i`,
      }),
    );
  });

  test("the assumed-role ARN has the role name only, never its path", async () => {
    const role = await makeRole({ Path: "/deep/path/" });
    const out = await sts.send(new AssumeRoleCommand({ RoleArn: role.arn, RoleSessionName: "pathless" }));
    expect(out.AssumedRoleUser?.Arn).toBe(`arn:aws:sts::${defaultAccount}:assumed-role/${role.name}/pathless`);
  });

  test("SourceIdentity is echoed when supplied", async () => {
    const role = await makeRole();
    const out = await sts.send(new AssumeRoleCommand({ RoleArn: role.arn, RoleSessionName: "src", SourceIdentity: "Alice" }));
    expect(out.SourceIdentity).toBe("Alice");
  });

  test("a RoleSessionName with a colon fails ValidationError with the regex message", async () => {
    const role = await makeRole();
    await expectFailure(sts.send(new AssumeRoleCommand({ RoleArn: role.arn, RoleSessionName: "Session1:2" })), {
      name: "ValidationError",
      status: 400,
      messageIncludes: "1 validation error detected: Value 'Session1:2' at 'roleSessionName' failed to satisfy constraint: Member must satisfy regular expression pattern: [\\w+=,.@-]*",
    });
  });

  test("a DurationSeconds above the role's MaxSessionDuration fails ValidationError", async () => {
    const role = await makeRole();
    await expectFailure(sts.send(new AssumeRoleCommand({ RoleArn: role.arn, RoleSessionName: "toolong", DurationSeconds: 7200 })), {
      name: "ValidationError",
      status: 400,
      messageIncludes: "The requested DurationSeconds exceeds the MaxSessionDuration set for this role.",
    });
  });

  test("a DurationSeconds above 43200 fails ValidationError with the range message", async () => {
    const role = await makeRole();
    await expectFailure(sts.send(new AssumeRoleCommand({ RoleArn: role.arn, RoleSessionName: "huge", DurationSeconds: 43_201 })), {
      name: "ValidationError",
      status: 400,
      messageIncludes: "Value '43201' at 'durationSeconds' failed to satisfy constraint",
    });
  });

  test("AssumeRole for a role that does not exist is refused", async () => {
    const failure = await awsFailure(
      sts.send(new AssumeRoleCommand({ RoleArn: `arn:aws:iam::${defaultAccount}:role/${uniq("ghost")}`, RoleSessionName: "ghost" })),
    );
    expect([403, 404]).toContain(failure.status ?? 0);
  });

  test("AssumeRole with a malformed role ARN fails ValidationError", async () => {
    await expectFailure(sts.send(new AssumeRoleCommand({ RoleArn: "arn::b:::something/test-role", RoleSessionName: "bad" })), {
      name: "ValidationError",
      status: 400,
    });
  });

  test("two sessions of one role get different credentials", async () => {
    const role = await makeRole();
    const one = await sts.send(new AssumeRoleCommand({ RoleArn: role.arn, RoleSessionName: "s1" }));
    const two = await sts.send(new AssumeRoleCommand({ RoleArn: role.arn, RoleSessionName: "s2" }));
    expect(one.Credentials?.AccessKeyId).not.toBe(two.Credentials?.AccessKeyId);
    expect(one.Credentials?.SessionToken).not.toBe(two.Credentials?.SessionToken);
  });
});

describe("STS GetSessionToken", () => {
  test("GetSessionToken returns temporary credentials for the caller", async () => {
    const out = await sts.send(new GetSessionTokenCommand({ DurationSeconds: 900 }));
    expect(out.Credentials?.AccessKeyId).toMatch(/^ASIA/);
    expect((out.Credentials?.SessionToken ?? "").length).toBeGreaterThan(0);
  });
});
