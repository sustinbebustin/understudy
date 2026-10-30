// SES v1 over awsQuery, through @aws-sdk/client-ses.
// Expected behavior: docs/research/ses.md sections 5, 9.1 and 9.2.

import { describe, expect, test } from "bun:test";
import {
  DeleteIdentityCommand,
  GetIdentityVerificationAttributesCommand,
  paginateListIdentities,
  SESClient,
  VerifyDomainIdentityCommand,
  VerifyEmailIdentityCommand,
} from "@aws-sdk/client-ses";
import { clientConfig } from "../support/env";
import { uniq } from "../support/helpers";

const ses = new SESClient(clientConfig());

describe("SES email identities", () => {
  test("a new email identity is Pending, keyed by the request string", async () => {
    const addr = `${uniq("ses-pending")}@example.com`;
    await ses.send(new VerifyEmailIdentityCommand({ EmailAddress: addr }));
    // Pending until the owner opens the link (ses.md 5.2); keyed byte for byte (9.1).
    const out = await ses.send(new GetIdentityVerificationAttributesCommand({ Identities: [addr] }));
    expect(Object.keys(out.VerificationAttributes ?? {})).toEqual([addr]);
    expect(out.VerificationAttributes?.[addr]?.VerificationStatus).toBe("Pending");
  });

  test("DeleteIdentity removes the key", async () => {
    const addr = `${uniq("ses-delete")}@example.com`;
    await ses.send(new VerifyEmailIdentityCommand({ EmailAddress: addr }));
    await ses.send(new DeleteIdentityCommand({ Identity: addr }));
    // Terraform reads a missing key as gone (ses.md 9.1).
    const out = await ses.send(new GetIdentityVerificationAttributesCommand({ Identities: [addr] }));
    expect(out.VerificationAttributes ?? {}).toEqual({});
  });
});

describe("SES domain identities", () => {
  test("the domain token is 44 characters and stable, and ListIdentities lists the domain", async () => {
    const domain = `${uniq("ses-domain")}.example.org`;
    const first = await ses.send(new VerifyDomainIdentityCommand({ Domain: domain }));
    try {
      // 44 base64 characters, the encoding of 32 bytes (ses.md 5.3), the
      // same on a repeat and on every read (ses.md 9.2).
      const token = first.VerificationToken ?? "";
      expect(token).toHaveLength(44);
      const again = await ses.send(new VerifyDomainIdentityCommand({ Domain: domain }));
      expect(again.VerificationToken).toBe(token);
      const out = await ses.send(new GetIdentityVerificationAttributesCommand({ Identities: [domain] }));
      expect(out.VerificationAttributes?.[domain]).toEqual({ VerificationStatus: "Pending", VerificationToken: token });
      // Listed regardless of status, under its type only (ses.md 5.3).
      const listed = async (kind: "Domain" | "EmailAddress") => {
        const names: string[] = [];
        for await (const page of paginateListIdentities({ client: ses }, { IdentityType: kind })) {
          names.push(...(page.Identities ?? []));
        }
        return names.includes(domain);
      };
      expect(await listed("Domain")).toBe(true);
      expect(await listed("EmailAddress")).toBe(false);
    } finally {
      await ses.send(new DeleteIdentityCommand({ Identity: domain }));
    }
  });
});
