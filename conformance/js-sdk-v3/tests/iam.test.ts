// IAM over awsQuery, through @aws-sdk/client-iam.
// Expected behavior: docs/research/phase1/iam-sts-kms.md section 3.

import { describe, expect, test } from "bun:test";
import {
  AddRoleToInstanceProfileCommand,
  AttachRolePolicyCommand,
  CreateAccessKeyCommand,
  CreateInstanceProfileCommand,
  CreatePolicyCommand,
  CreatePolicyVersionCommand,
  CreateRoleCommand,
  CreateUserCommand,
  DeleteAccessKeyCommand,
  DeleteInstanceProfileCommand,
  DeletePolicyCommand,
  DeletePolicyVersionCommand,
  DeleteRoleCommand,
  DeleteRolePolicyCommand,
  DeleteUserCommand,
  DetachRolePolicyCommand,
  GetInstanceProfileCommand,
  GetPolicyCommand,
  GetPolicyVersionCommand,
  GetRoleCommand,
  GetRolePolicyCommand,
  GetUserCommand,
  IAMClient,
  ListAccessKeysCommand,
  ListAttachedRolePoliciesCommand,
  ListInstanceProfilesForRoleCommand,
  ListPoliciesCommand,
  ListPolicyVersionsCommand,
  ListRolePoliciesCommand,
  ListRolesCommand,
  ListRoleTagsCommand,
  PutRolePolicyCommand,
  RemoveRoleFromInstanceProfileCommand,
  SetDefaultPolicyVersionCommand,
  TagRoleCommand,
  UntagRoleCommand,
  UpdateAccessKeyCommand,
  UpdateAssumeRolePolicyCommand,
  UpdateRoleCommand,
} from "@aws-sdk/client-iam";
import { clientConfig, defaultAccount } from "../support/env";
import { awsFailure, expectFailure, uniq } from "../support/helpers";

const iam = new IAMClient(clientConfig());

const trust = (service = "lambda.amazonaws.com") =>
  JSON.stringify({ Version: "2012-10-17", Statement: [{ Effect: "Allow", Principal: { Service: service }, Action: "sts:AssumeRole" }] });

const policyDoc = (action = "s3:GetObject") =>
  JSON.stringify({ Version: "2012-10-17", Statement: [{ Effect: "Allow", Action: action, Resource: "*" }] });

const decoded = (document: string | undefined): unknown => JSON.parse(decodeURIComponent(document ?? "null"));

async function makeRole(extra: Partial<ConstructorParameters<typeof CreateRoleCommand>[0]> = {}) {
  const name = extra.RoleName ?? uniq("role");
  const out = await iam.send(new CreateRoleCommand({ RoleName: name, AssumeRolePolicyDocument: trust(), ...extra }));
  return { name, role: out.Role };
}

async function makePolicy(document = policyDoc()) {
  const name = uniq("policy");
  const out = await iam.send(new CreatePolicyCommand({ PolicyName: name, PolicyDocument: document }));
  return { name, arn: out.Policy?.Arn ?? "", policy: out.Policy };
}

describe("IAM roles", () => {
  test("CreateRole returns a full ARN, an AROA role id, path / and the default session duration", async () => {
    const { name, role } = await makeRole();
    expect(role?.Arn).toBe(`arn:aws:iam::${defaultAccount}:role/${name}`);
    expect(role?.RoleId).toMatch(/^AROA[A-Z0-9]{17}$/);
    expect(role?.Path).toBe("/");
    expect(role?.RoleName).toBe(name);
    expect(role?.CreateDate).toBeInstanceOf(Date);
    const got = (await iam.send(new GetRoleCommand({ RoleName: name }))).Role;
    expect(got?.MaxSessionDuration).toBe(3600);
  });

  test("CreateRole with a path puts the path in the ARN", async () => {
    const { name, role } = await makeRole({ Path: "/service-role/" });
    expect(role?.Arn).toBe(`arn:aws:iam::${defaultAccount}:role/service-role/${name}`);
    expect(role?.Path).toBe("/service-role/");
  });

  test("GetRole returns the trust policy URL-encoded so that decoding round trips it", async () => {
    const { name } = await makeRole({ AssumeRolePolicyDocument: trust("ec2.amazonaws.com") });
    const document = (await iam.send(new GetRoleCommand({ RoleName: name }))).Role?.AssumeRolePolicyDocument;
    expect(document).toBeDefined();
    expect(document).not.toContain("{");
    expect(decoded(document)).toEqual(JSON.parse(trust("ec2.amazonaws.com")));
  });

  test("a trust policy value containing + survives URL-encoding", async () => {
    const document = JSON.stringify({
      Version: "2012-10-17",
      Statement: [{ Effect: "Allow", Principal: { Service: "lambda.amazonaws.com" }, Action: "sts:AssumeRole", Condition: { StringEquals: { "sts:ExternalId": "a+b c" } } }],
    });
    const { name } = await makeRole({ AssumeRolePolicyDocument: document });
    const got = (await iam.send(new GetRoleCommand({ RoleName: name }))).Role?.AssumeRolePolicyDocument ?? "";
    // Terraform decodes with QueryUnescape, which turns + into a space; a literal plus must be %2B.
    expect(new URLSearchParams(`d=${got}`).get("d")).toBe(JSON.stringify(JSON.parse(document)));
  });

  test("CreateRole with the same name fails EntityAlreadyExists with status 409", async () => {
    const { name } = await makeRole();
    await expectFailure(iam.send(new CreateRoleCommand({ RoleName: name, AssumeRolePolicyDocument: trust() })), {
      name: "EntityAlreadyExists",
      status: 409,
      messageIncludes: `Role with name ${name} already exists.`,
    });
  });

  test("role names are unique case-insensitively", async () => {
    const { name } = await makeRole({ RoleName: `Mixed-${uniq("case")}` });
    await expectFailure(iam.send(new CreateRoleCommand({ RoleName: name.toUpperCase(), AssumeRolePolicyDocument: trust() })), {
      name: "EntityAlreadyExists",
      status: 409,
    });
  });

  test("GetRole on a missing role fails NoSuchEntity with status 404", async () => {
    const name = uniq("missing");
    await expectFailure(iam.send(new GetRoleCommand({ RoleName: name })), {
      name: "NoSuchEntity",
      status: 404,
      messageIncludes: `The role with name ${name} cannot be found.`,
    });
  });

  test("CreateRole with invalid JSON in the trust policy fails MalformedPolicyDocument", async () => {
    await expectFailure(iam.send(new CreateRoleCommand({ RoleName: uniq("bad"), AssumeRolePolicyDocument: "not json" })), {
      name: "MalformedPolicyDocument",
      status: 400,
    });
  });

  test("CreateRole with a name containing a space fails with a validation error", async () => {
    const failure = await awsFailure(iam.send(new CreateRoleCommand({ RoleName: "has space", AssumeRolePolicyDocument: trust() })));
    expect(failure.status).toBe(400);
    expect(["ValidationError", "InvalidInput"]).toContain(failure.code ?? failure.name);
  });

  test("tags at creation are returned by ListRoleTags; TagRole and UntagRole change them", async () => {
    const { name } = await makeRole({ Tags: [{ Key: "env", Value: "test" }] });
    const tags = async () => Object.fromEntries(((await iam.send(new ListRoleTagsCommand({ RoleName: name }))).Tags ?? []).map((t) => [t.Key, t.Value]));
    expect(await tags()).toEqual({ env: "test" });
    await iam.send(new TagRoleCommand({ RoleName: name, Tags: [{ Key: "team", Value: "a" }] }));
    expect(await tags()).toEqual({ env: "test", team: "a" });
    await iam.send(new UntagRoleCommand({ RoleName: name, TagKeys: ["env"] }));
    expect(await tags()).toEqual({ team: "a" });
    expect((await iam.send(new GetRoleCommand({ RoleName: name }))).Role?.Tags).toEqual([{ Key: "team", Value: "a" }]);
  });

  test("UpdateRole changes MaxSessionDuration and the description", async () => {
    const { name } = await makeRole();
    await iam.send(new UpdateRoleCommand({ RoleName: name, MaxSessionDuration: 7200, Description: "updated" }));
    const role = (await iam.send(new GetRoleCommand({ RoleName: name }))).Role;
    expect(role).toMatchObject({ MaxSessionDuration: 7200, Description: "updated" });
  });

  test("UpdateAssumeRolePolicy replaces the trust policy", async () => {
    const { name } = await makeRole();
    await iam.send(new UpdateAssumeRolePolicyCommand({ RoleName: name, PolicyDocument: trust("events.amazonaws.com") }));
    const role = (await iam.send(new GetRoleCommand({ RoleName: name }))).Role;
    expect(decoded(role?.AssumeRolePolicyDocument)).toEqual(JSON.parse(trust("events.amazonaws.com")));
  });

  test("ListRoles with a path prefix lists matching roles without tags", async () => {
    const path = `/${uniq("path")}/`;
    const a = await makeRole({ Path: path, Tags: [{ Key: "k", Value: "v" }] });
    await makeRole();
    const out = await iam.send(new ListRolesCommand({ PathPrefix: path }));
    expect(out.Roles?.map((r) => r.RoleName)).toEqual([a.name]);
    expect(out.Roles?.[0]?.Tags).toBeUndefined();
    expect(out.IsTruncated).toBe(false);
  });

  test("ListRoles pages with MaxItems and Marker", async () => {
    const path = `/${uniq("page")}/`;
    const names = [];
    for (let i = 0; i < 3; i += 1) names.push((await makeRole({ Path: path })).name);
    const first = await iam.send(new ListRolesCommand({ PathPrefix: path, MaxItems: 2 }));
    expect(first.Roles).toHaveLength(2);
    expect(first.IsTruncated).toBe(true);
    expect(first.Marker).toBeDefined();
    const second = await iam.send(new ListRolesCommand({ PathPrefix: path, MaxItems: 2, Marker: first.Marker }));
    expect(second.Roles).toHaveLength(1);
    expect(second.IsTruncated).toBe(false);
    expect([...(first.Roles ?? []), ...(second.Roles ?? [])].map((r) => r.RoleName).sort()).toEqual([...names].sort());
  });

  test("DeleteRole removes the role and a second delete fails NoSuchEntity", async () => {
    const { name } = await makeRole();
    await iam.send(new DeleteRoleCommand({ RoleName: name }));
    await expectFailure(iam.send(new GetRoleCommand({ RoleName: name })), { name: "NoSuchEntity", status: 404 });
    await expectFailure(iam.send(new DeleteRoleCommand({ RoleName: name })), { name: "NoSuchEntity", status: 404 });
  });
});

describe("IAM inline and managed policies on roles", () => {
  test("PutRolePolicy, GetRolePolicy and ListRolePolicies round trip an inline policy", async () => {
    const { name } = await makeRole();
    await iam.send(new PutRolePolicyCommand({ RoleName: name, PolicyName: "inline", PolicyDocument: policyDoc("logs:*") }));
    const got = await iam.send(new GetRolePolicyCommand({ RoleName: name, PolicyName: "inline" }));
    expect(got).toMatchObject({ RoleName: name, PolicyName: "inline" });
    expect(decoded(got.PolicyDocument)).toEqual(JSON.parse(policyDoc("logs:*")));
    expect((await iam.send(new ListRolePoliciesCommand({ RoleName: name }))).PolicyNames).toEqual(["inline"]);
    await iam.send(new DeleteRolePolicyCommand({ RoleName: name, PolicyName: "inline" }));
    expect((await iam.send(new ListRolePoliciesCommand({ RoleName: name }))).PolicyNames).toEqual([]);
  });

  test("GetRolePolicy for a missing inline policy fails NoSuchEntity", async () => {
    const { name } = await makeRole();
    await expectFailure(iam.send(new GetRolePolicyCommand({ RoleName: name, PolicyName: "nope" })), { name: "NoSuchEntity", status: 404 });
  });

  test("PutRolePolicy with malformed JSON fails MalformedPolicyDocument", async () => {
    const { name } = await makeRole();
    await expectFailure(iam.send(new PutRolePolicyCommand({ RoleName: name, PolicyName: "bad", PolicyDocument: "nope" })), {
      name: "MalformedPolicyDocument",
      status: 400,
    });
  });

  test("an identity policy that names a Principal fails MalformedPolicyDocument", async () => {
    const { name } = await makeRole();
    const withPrincipal = JSON.stringify({
      Version: "2012-10-17",
      Statement: [{ Effect: "Allow", Principal: { AWS: "*" }, Action: "s3:GetObject", Resource: "*" }],
    });
    await expectFailure(iam.send(new PutRolePolicyCommand({ RoleName: name, PolicyName: "bad", PolicyDocument: withPrincipal })), {
      name: "MalformedPolicyDocument",
      status: 400,
      messageIncludes: "Policy document should not specify a principal.",
    });
  });

  test("AttachRolePolicy and ListAttachedRolePolicies work for a customer managed policy and an AWS managed ARN", async () => {
    const { name } = await makeRole();
    const { arn, name: policyName } = await makePolicy();
    const managed = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole";
    await iam.send(new AttachRolePolicyCommand({ RoleName: name, PolicyArn: arn }));
    await iam.send(new AttachRolePolicyCommand({ RoleName: name, PolicyArn: managed }));
    const attached = await iam.send(new ListAttachedRolePoliciesCommand({ RoleName: name }));
    expect([...(attached.AttachedPolicies ?? [])].sort((a, b) => (a.PolicyName ?? "").localeCompare(b.PolicyName ?? ""))).toEqual(
      [
        { PolicyName: policyName, PolicyArn: arn },
        { PolicyName: "AWSLambdaBasicExecutionRole", PolicyArn: managed },
      ].sort((a, b) => a.PolicyName.localeCompare(b.PolicyName)),
    );
    await iam.send(new DetachRolePolicyCommand({ RoleName: name, PolicyArn: arn }));
    expect((await iam.send(new ListAttachedRolePoliciesCommand({ RoleName: name }))).AttachedPolicies?.map((p) => p.PolicyArn)).toEqual([managed]);
  });

  test("AttachRolePolicy to a policy that does not exist fails NoSuchEntity", async () => {
    const { name } = await makeRole();
    await expectFailure(iam.send(new AttachRolePolicyCommand({ RoleName: name, PolicyArn: `arn:aws:iam::${defaultAccount}:policy/${uniq("ghost")}` })), {
      name: "NoSuchEntity",
      status: 404,
    });
  });

  test("DeleteRole with an attached managed policy fails DeleteConflict", async () => {
    const { name } = await makeRole();
    const { arn } = await makePolicy();
    await iam.send(new AttachRolePolicyCommand({ RoleName: name, PolicyArn: arn }));
    await expectFailure(iam.send(new DeleteRoleCommand({ RoleName: name })), {
      name: "DeleteConflict",
      status: 409,
      messageIncludes: "Cannot delete entity, must detach all policies first.",
    });
  });

  test("DeleteRole with an inline policy fails DeleteConflict", async () => {
    const { name } = await makeRole();
    await iam.send(new PutRolePolicyCommand({ RoleName: name, PolicyName: "inline", PolicyDocument: policyDoc() }));
    await expectFailure(iam.send(new DeleteRoleCommand({ RoleName: name })), {
      name: "DeleteConflict",
      status: 409,
      messageIncludes: "Cannot delete entity, must delete policies first.",
    });
  });
});

describe("IAM managed policies", () => {
  test("CreatePolicy returns the ARN, a v1 default version and zero attachments", async () => {
    const { name, policy } = await makePolicy();
    expect(policy).toMatchObject({
      PolicyName: name,
      Arn: `arn:aws:iam::${defaultAccount}:policy/${name}`,
      DefaultVersionId: "v1",
      AttachmentCount: 0,
      IsAttachable: true,
      Path: "/",
    });
    expect(policy?.PolicyId).toMatch(/^ANPA[A-Z0-9]{17}$/);
  });

  test("GetPolicy and GetPolicyVersion agree on the default version, with an encoded document", async () => {
    const { arn } = await makePolicy(policyDoc("sqs:*"));
    const got = (await iam.send(new GetPolicyCommand({ PolicyArn: arn }))).Policy;
    expect(got?.DefaultVersionId).toBe("v1");
    const version = (await iam.send(new GetPolicyVersionCommand({ PolicyArn: arn, VersionId: "v1" }))).PolicyVersion;
    expect(version).toMatchObject({ VersionId: "v1", IsDefaultVersion: true });
    expect(decoded(version?.Document)).toEqual(JSON.parse(policyDoc("sqs:*")));
  });

  test("CreatePolicy with an existing name fails EntityAlreadyExists", async () => {
    const { name } = await makePolicy();
    await expectFailure(iam.send(new CreatePolicyCommand({ PolicyName: name, PolicyDocument: policyDoc() })), {
      name: "EntityAlreadyExists",
      status: 409,
      messageIncludes: `A policy called ${name} already exists. Duplicate names are not allowed.`,
    });
  });

  test("GetPolicy on a missing policy fails NoSuchEntity", async () => {
    const arn = `arn:aws:iam::${defaultAccount}:policy/${uniq("missing")}`;
    await expectFailure(iam.send(new GetPolicyCommand({ PolicyArn: arn })), { name: "NoSuchEntity", status: 404, messageIncludes: `Policy ${arn} was not found.` });
  });

  test("CreatePolicyVersion adds a version and SetAsDefault moves the default", async () => {
    const { arn } = await makePolicy(policyDoc("s3:GetObject"));
    const created = await iam.send(new CreatePolicyVersionCommand({ PolicyArn: arn, PolicyDocument: policyDoc("s3:PutObject"), SetAsDefault: true }));
    expect(created.PolicyVersion).toMatchObject({ VersionId: "v2", IsDefaultVersion: true });
    expect((await iam.send(new GetPolicyCommand({ PolicyArn: arn }))).Policy?.DefaultVersionId).toBe("v2");
    const listed = await iam.send(new ListPolicyVersionsCommand({ PolicyArn: arn }));
    expect(listed.Versions?.map((v) => [v.VersionId, v.IsDefaultVersion]).sort()).toEqual([["v1", false], ["v2", true]]);
    expect(listed.Versions?.every((v) => v.CreateDate instanceof Date)).toBe(true);
  });

  test("SetDefaultPolicyVersion switches back and the default version cannot be deleted", async () => {
    const { arn } = await makePolicy();
    await iam.send(new CreatePolicyVersionCommand({ PolicyArn: arn, PolicyDocument: policyDoc("s3:PutObject"), SetAsDefault: true }));
    await iam.send(new SetDefaultPolicyVersionCommand({ PolicyArn: arn, VersionId: "v1" }));
    expect((await iam.send(new GetPolicyCommand({ PolicyArn: arn }))).Policy?.DefaultVersionId).toBe("v1");
    await expectFailure(iam.send(new DeletePolicyVersionCommand({ PolicyArn: arn, VersionId: "v1" })), {
      name: "DeleteConflict",
      status: 409,
      messageIncludes: "Cannot delete the default version of a policy.",
    });
    await iam.send(new DeletePolicyVersionCommand({ PolicyArn: arn, VersionId: "v2" }));
  });

  test("a sixth policy version fails LimitExceeded", async () => {
    const { arn } = await makePolicy();
    for (let i = 0; i < 4; i += 1) await iam.send(new CreatePolicyVersionCommand({ PolicyArn: arn, PolicyDocument: policyDoc(`s3:Action${i}`) }));
    await expectFailure(iam.send(new CreatePolicyVersionCommand({ PolicyArn: arn, PolicyDocument: policyDoc("s3:Sixth") })), {
      name: "LimitExceeded",
      status: 409,
      messageIncludes: "A managed policy can have up to 5 versions.",
    });
  });

  test("ListPolicies with Scope Local and OnlyAttached lists attached customer policies", async () => {
    const path = `/${uniq("lp")}/`;
    const name = uniq("policy");
    const arn = (await iam.send(new CreatePolicyCommand({ PolicyName: name, Path: path, PolicyDocument: policyDoc() }))).Policy?.Arn ?? "";
    expect(arn).toBe(`arn:aws:iam::${defaultAccount}:policy${path}${name}`);
    expect((await iam.send(new ListPoliciesCommand({ Scope: "Local", PathPrefix: path }))).Policies?.map((p) => p.Arn)).toEqual([arn]);
    expect((await iam.send(new ListPoliciesCommand({ Scope: "Local", PathPrefix: path, OnlyAttached: true }))).Policies ?? []).toEqual([]);
    const { name: roleName } = await makeRole();
    await iam.send(new AttachRolePolicyCommand({ RoleName: roleName, PolicyArn: arn }));
    expect((await iam.send(new ListPoliciesCommand({ Scope: "Local", PathPrefix: path, OnlyAttached: true }))).Policies?.map((p) => p.Arn)).toEqual([arn]);
    expect((await iam.send(new GetPolicyCommand({ PolicyArn: arn }))).Policy?.AttachmentCount).toBe(1);
  });

  test("DeletePolicy while attached fails DeleteConflict and succeeds after detaching", async () => {
    const { arn } = await makePolicy();
    const { name } = await makeRole();
    await iam.send(new AttachRolePolicyCommand({ RoleName: name, PolicyArn: arn }));
    await expectFailure(iam.send(new DeletePolicyCommand({ PolicyArn: arn })), { name: "DeleteConflict", status: 409 });
    await iam.send(new DetachRolePolicyCommand({ RoleName: name, PolicyArn: arn }));
    await iam.send(new DeletePolicyCommand({ PolicyArn: arn }));
    await expectFailure(iam.send(new GetPolicyCommand({ PolicyArn: arn })), { name: "NoSuchEntity", status: 404 });
  });

  test("DeletePolicy removes the non-default versions together with the policy once they are deleted", async () => {
    const { arn } = await makePolicy();
    await iam.send(new CreatePolicyVersionCommand({ PolicyArn: arn, PolicyDocument: policyDoc("s3:PutObject") }));
    const failure = await awsFailure(iam.send(new DeletePolicyCommand({ PolicyArn: arn })));
    expect(failure.status).toBe(409);
    await iam.send(new DeletePolicyVersionCommand({ PolicyArn: arn, VersionId: "v2" }));
    await iam.send(new DeletePolicyCommand({ PolicyArn: arn }));
  });
});

describe("IAM instance profiles", () => {
  test("CreateInstanceProfile returns an AIPA id, an ARN and an empty role list", async () => {
    const name = uniq("profile");
    const out = await iam.send(new CreateInstanceProfileCommand({ InstanceProfileName: name }));
    expect(out.InstanceProfile).toMatchObject({ InstanceProfileName: name, Arn: `arn:aws:iam::${defaultAccount}:instance-profile/${name}`, Path: "/", Roles: [] });
    expect(out.InstanceProfile?.InstanceProfileId).toMatch(/^AIPA[A-Z0-9]{17}$/);
    expect(out.InstanceProfile?.CreateDate).toBeInstanceOf(Date);
  });

  test("a role can be added, listed through the profile and the role, and removed", async () => {
    const profile = uniq("profile");
    await iam.send(new CreateInstanceProfileCommand({ InstanceProfileName: profile }));
    const { name } = await makeRole();
    await iam.send(new AddRoleToInstanceProfileCommand({ InstanceProfileName: profile, RoleName: name }));
    const got = (await iam.send(new GetInstanceProfileCommand({ InstanceProfileName: profile }))).InstanceProfile;
    expect(got?.Roles?.map((r) => r.RoleName)).toEqual([name]);
    expect((await iam.send(new ListInstanceProfilesForRoleCommand({ RoleName: name }))).InstanceProfiles?.map((p) => p.InstanceProfileName)).toEqual([profile]);
    await iam.send(new RemoveRoleFromInstanceProfileCommand({ InstanceProfileName: profile, RoleName: name }));
    expect((await iam.send(new GetInstanceProfileCommand({ InstanceProfileName: profile }))).InstanceProfile?.Roles).toEqual([]);
  });

  test("an instance profile holds at most one role", async () => {
    const profile = uniq("profile");
    await iam.send(new CreateInstanceProfileCommand({ InstanceProfileName: profile }));
    const a = await makeRole();
    const b = await makeRole();
    await iam.send(new AddRoleToInstanceProfileCommand({ InstanceProfileName: profile, RoleName: a.name }));
    await expectFailure(iam.send(new AddRoleToInstanceProfileCommand({ InstanceProfileName: profile, RoleName: b.name })), {
      name: "LimitExceeded",
      status: 409,
    });
  });

  test("DeleteInstanceProfile with a role fails DeleteConflict", async () => {
    const profile = uniq("profile");
    await iam.send(new CreateInstanceProfileCommand({ InstanceProfileName: profile }));
    const { name } = await makeRole();
    await iam.send(new AddRoleToInstanceProfileCommand({ InstanceProfileName: profile, RoleName: name }));
    await expectFailure(iam.send(new DeleteInstanceProfileCommand({ InstanceProfileName: profile })), {
      name: "DeleteConflict",
      status: 409,
      messageIncludes: "Cannot delete entity, must remove roles from instance profile first.",
    });
  });

  test("DeleteRole while it is in an instance profile fails DeleteConflict", async () => {
    const profile = uniq("profile");
    await iam.send(new CreateInstanceProfileCommand({ InstanceProfileName: profile }));
    const { name } = await makeRole();
    await iam.send(new AddRoleToInstanceProfileCommand({ InstanceProfileName: profile, RoleName: name }));
    await expectFailure(iam.send(new DeleteRoleCommand({ RoleName: name })), { name: "DeleteConflict", status: 409 });
  });

  test("CreateInstanceProfile with an existing name fails EntityAlreadyExists", async () => {
    const profile = uniq("profile");
    await iam.send(new CreateInstanceProfileCommand({ InstanceProfileName: profile }));
    await expectFailure(iam.send(new CreateInstanceProfileCommand({ InstanceProfileName: profile })), {
      name: "EntityAlreadyExists",
      status: 409,
      messageIncludes: `Instance Profile ${profile} already exists.`,
    });
  });

  test("GetInstanceProfile on a missing profile fails NoSuchEntity", async () => {
    await expectFailure(iam.send(new GetInstanceProfileCommand({ InstanceProfileName: uniq("missing") })), { name: "NoSuchEntity", status: 404 });
  });
});

describe("IAM users and access keys", () => {
  test("CreateUser returns an AIDA id and ARN and GetUser reads it back", async () => {
    const name = uniq("user");
    const created = (await iam.send(new CreateUserCommand({ UserName: name }))).User;
    expect(created).toMatchObject({ UserName: name, Arn: `arn:aws:iam::${defaultAccount}:user/${name}`, Path: "/" });
    expect(created?.UserId).toMatch(/^AIDA[A-Z0-9]{17}$/);
    expect((await iam.send(new GetUserCommand({ UserName: name }))).User?.UserId).toBe(created?.UserId);
  });

  test("GetUser on a missing user fails NoSuchEntity with the documented message", async () => {
    const name = uniq("missing");
    await expectFailure(iam.send(new GetUserCommand({ UserName: name })), {
      name: "NoSuchEntity",
      status: 404,
      messageIncludes: `The user with name ${name} cannot be found.`,
    });
  });

  test("CreateAccessKey returns a 20-character AKIA key and a 40-character secret, listed as Active", async () => {
    const name = uniq("user");
    await iam.send(new CreateUserCommand({ UserName: name }));
    const key = (await iam.send(new CreateAccessKeyCommand({ UserName: name }))).AccessKey;
    expect(key?.AccessKeyId).toMatch(/^AKIA[A-Z2-7]{16}$/);
    expect(key?.SecretAccessKey).toHaveLength(40);
    expect(key).toMatchObject({ UserName: name, Status: "Active" });
    const listed = await iam.send(new ListAccessKeysCommand({ UserName: name }));
    expect(listed.AccessKeyMetadata?.map((k) => [k.AccessKeyId, k.Status])).toEqual([[key?.AccessKeyId, "Active"]]);
    expect(listed.AccessKeyMetadata?.[0]).not.toHaveProperty("SecretAccessKey");
  });

  test("UpdateAccessKey sets the status and DeleteAccessKey removes the key", async () => {
    const name = uniq("user");
    await iam.send(new CreateUserCommand({ UserName: name }));
    const key = (await iam.send(new CreateAccessKeyCommand({ UserName: name }))).AccessKey;
    await iam.send(new UpdateAccessKeyCommand({ UserName: name, AccessKeyId: key?.AccessKeyId, Status: "Inactive" }));
    expect((await iam.send(new ListAccessKeysCommand({ UserName: name }))).AccessKeyMetadata?.[0]?.Status).toBe("Inactive");
    await iam.send(new DeleteAccessKeyCommand({ UserName: name, AccessKeyId: key?.AccessKeyId }));
    expect((await iam.send(new ListAccessKeysCommand({ UserName: name }))).AccessKeyMetadata).toEqual([]);
  });

  test("DeleteUser removes the user", async () => {
    const name = uniq("user");
    await iam.send(new CreateUserCommand({ UserName: name }));
    await iam.send(new DeleteUserCommand({ UserName: name }));
    await expectFailure(iam.send(new GetUserCommand({ UserName: name })), { name: "NoSuchEntity", status: 404 });
  });

  test("CreateUser with an existing name fails EntityAlreadyExists", async () => {
    const name = uniq("user");
    await iam.send(new CreateUserCommand({ UserName: name }));
    await expectFailure(iam.send(new CreateUserCommand({ UserName: name })), { name: "EntityAlreadyExists", status: 409 });
  });
});
