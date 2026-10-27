// SSM Parameter Store over awsJson1_1, through @aws-sdk/client-ssm.
// Expected behavior: docs/research/ssm-parameters.md; recorded shapes from
// localstack@8b9a79f tests/aws/services/ssm/test_ssm.snapshot.json.

import { afterAll, describe, expect, test } from "bun:test";
import { DecryptCommand, KMSClient } from "@aws-sdk/client-kms";
import {
  AddTagsToResourceCommand,
  DeleteParameterCommand,
  DeleteParametersCommand,
  DescribeParametersCommand,
  GetParameterCommand,
  GetParameterHistoryCommand,
  GetParametersCommand,
  LabelParameterVersionCommand,
  ListTagsForResourceCommand,
  PutParameterCommand,
  type PutParameterCommandInput,
  RemoveTagsFromResourceCommand,
  SSMClient,
  UnlabelParameterVersionCommand,
  paginateGetParametersByPath,
} from "@aws-sdk/client-ssm";
import { clientConfig, defaultAccount, region } from "../support/env";
import { awsFailure, uniq } from "../support/helpers";

const ssm = new SSMClient(clientConfig());
const kms = new KMSClient(clientConfig());
const created: string[] = [];

async function put(input: PutParameterCommandInput) {
  const out = await ssm.send(new PutParameterCommand(input));
  if (input.Name !== undefined) created.push(input.Name);
  return out;
}

const arnOf = (name: string) => `arn:aws:ssm:${region}:${defaultAccount}:parameter${name}`;

afterAll(async () => {
  for (const name of created) {
    await ssm.send(new DeleteParameterCommand({ Name: name })).catch(() => undefined);
  }
});

describe("SSM Parameter Store", () => {
  test("String put, get, overwrite and version", async () => {
    const name = `/${uniq("conf-ssm")}/string`;
    // test_ssm.snapshot.json:L19-L26
    const out = await put({ Name: name, Value: "one", Type: "String" });
    expect([out.Tier, out.Version]).toEqual(["Standard", 1]);
    // test_ssm.snapshot.json:L27-L56: by name and by ARN, no Selector.
    for (const ask of [name, arnOf(name)]) {
      const got = (await ssm.send(new GetParameterCommand({ Name: ask }))).Parameter;
      expect([got?.Name, got?.Value, got?.Version, got?.ARN, got?.DataType]).toEqual([name, "one", 1, arnOf(name), "text"]);
      expect(got?.Selector).toBeUndefined();
    }
    // ssm-parameters.md 7.1
    const exists = await awsFailure(ssm.send(new PutParameterCommand({ Name: name, Value: "two", Type: "String" })));
    expect([exists.name, exists.status]).toEqual(["ParameterAlreadyExists", 400]);
    // ssm-parameters.md 6.1, 6.3
    expect((await put({ Name: name, Value: "two", Overwrite: true })).Version).toBe(2);
    const old = (await ssm.send(new GetParameterCommand({ Name: `${name}:1` }))).Parameter;
    expect([old?.Value, old?.Selector]).toEqual(["one", ":1"]);
  });

  test("GetParameter after delete is ParameterNotFound with an empty message", async () => {
    // localstack@8b9a79f tests/aws/services/cloudformation/resources/test_ssm.snapshot.json:L105-L114
    const name = `/${uniq("conf-ssm")}/gone`;
    await put({ Name: name, Value: "v", Type: "String" });
    await ssm.send(new DeleteParameterCommand({ Name: name }));
    const gone = await awsFailure(ssm.send(new GetParameterCommand({ Name: name })));
    // AWS's message is empty; the SDK surfaces an empty message as
    // "UnknownError" (@smithy/core 3.35.2
    // dist-es/submodules/client/smithy-client/exceptions.js
    // decorateServiceException: `exception.message || exception.Message || "UnknownError"`).
    expect([gone.name, gone.status, gone.message]).toEqual(["ParameterNotFound", 400, "UnknownError"]);
  });

  test("SecureString with and without decryption; KMS Decrypt opens the ciphertext", async () => {
    const name = `/${uniq("conf-ssm")}/secure`;
    await put({ Name: name, Value: "hunter2", Type: "SecureString" });
    const plain = (await ssm.send(new GetParameterCommand({ Name: name, WithDecryption: true }))).Parameter;
    expect(plain?.Value).toBe("hunter2");
    const sealed = (await ssm.send(new GetParameterCommand({ Name: name }))).Parameter?.Value ?? "";
    expect(sealed).not.toBe("hunter2");
    // ssm-parameters.md 5.2: PARAMETER_ARN is the encryption context.
    const opened = await kms.send(
      new DecryptCommand({ CiphertextBlob: Buffer.from(sealed, "base64"), EncryptionContext: { PARAMETER_ARN: arnOf(name) } }),
    );
    expect(new TextDecoder().decode(opened.Plaintext)).toBe("hunter2");
    // ssm-parameters.md 5.4: the default key reads back alias/aws/ssm.
    const meta = await ssm.send(
      new DescribeParametersCommand({ ParameterFilters: [{ Key: "Name", Option: "Equals", Values: [name] }] }),
    );
    expect(meta.Parameters?.map((p) => p.KeyId)).toEqual(["alias/aws/ssm"]);
    expect(meta.NextToken).toBeUndefined();
  });

  test("GetParameters lists a missing name in InvalidParameters", async () => {
    // ssm-parameters.md 7.3
    const base = `/${uniq("conf-ssm")}`;
    await put({ Name: `${base}/a`, Value: "v", Type: "String" });
    const out = await ssm.send(new GetParametersCommand({ Names: [`${base}/a`, `${base}/missing`] }));
    expect(out.Parameters?.map((p) => p.Name)).toEqual([`${base}/a`]);
    expect(out.InvalidParameters).toEqual([`${base}/missing`]);
  });

  test("GetParametersByPath recursive across pages", async () => {
    // ssm-parameters.md 7.4
    const base = `/${uniq("conf-ssm")}`;
    const want: string[] = [];
    for (let i = 0; i < 5; i++) {
      const name = `${base}/l${i % 2}/p${i}`;
      await put({ Name: name, Value: "v", Type: "String" });
      want.push(name);
    }
    const got: string[] = [];
    for await (const page of paginateGetParametersByPath({ client: ssm, pageSize: 2 }, { Path: base, Recursive: true })) {
      for (const p of page.Parameters ?? []) got.push(p.Name ?? "");
    }
    expect(got.sort()).toEqual(want.sort());
  });

  test("history and labels", async () => {
    // ssm-parameters.md 6.2, 7.6, 7.8
    const name = `/${uniq("conf-ssm")}/labeled`;
    await put({ Name: name, Value: "v1", Type: "String" });
    await put({ Name: name, Value: "v2", Overwrite: true });
    const label = await ssm.send(new LabelParameterVersionCommand({ Name: name, ParameterVersion: 1, Labels: ["stable", "1bad"] }));
    expect([label.ParameterVersion, label.InvalidLabels]).toEqual([1, ["1bad"]]);
    const got = (await ssm.send(new GetParameterCommand({ Name: `${name}:stable` }))).Parameter;
    expect([got?.Value, got?.Selector]).toEqual(["v1", ":stable"]);
    const hist = (await ssm.send(new GetParameterHistoryCommand({ Name: name }))).Parameters ?? [];
    expect(hist.map((h) => h.Version)).toEqual([1, 2]);
    expect(hist[0]?.Labels).toEqual(["stable"]);
    const unlabel = await ssm.send(new UnlabelParameterVersionCommand({ Name: name, ParameterVersion: 1, Labels: ["stable", "absent"] }));
    expect([unlabel.RemovedLabels, unlabel.InvalidLabels]).toEqual([["stable"], ["absent"]]);
  });

  test("tags", async () => {
    // ssm-parameters.md 7.9
    const name = `/${uniq("conf-ssm")}/tagged`;
    await put({ Name: name, Value: "v", Type: "String", Tags: [{ Key: "env", Value: "dev" }] });
    await ssm.send(
      new AddTagsToResourceCommand({ ResourceType: "Parameter", ResourceId: name, Tags: [{ Key: "env", Value: "prod" }, { Key: "team", Value: "a" }] }),
    );
    await ssm.send(new RemoveTagsFromResourceCommand({ ResourceType: "Parameter", ResourceId: name, TagKeys: ["team"] }));
    const tags = await ssm.send(new ListTagsForResourceCommand({ ResourceType: "Parameter", ResourceId: name }));
    expect(tags.TagList).toEqual([{ Key: "env", Value: "prod" }]);
  });

  test("DeleteParameters", async () => {
    // ssm-parameters.md 7.7: deleted names alphabetical, missing ones listed.
    const base = `/${uniq("conf-ssm")}`;
    await put({ Name: `${base}/b`, Value: "v", Type: "String" });
    await put({ Name: `${base}/a`, Value: "v", Type: "String" });
    const out = await ssm.send(new DeleteParametersCommand({ Names: [`${base}/b`, `${base}/missing`, `${base}/a`] }));
    expect(out.DeletedParameters).toEqual([`${base}/a`, `${base}/b`]);
    expect(out.InvalidParameters).toEqual([`${base}/missing`]);
  });
});
