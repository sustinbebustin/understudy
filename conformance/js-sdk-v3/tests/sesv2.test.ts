// SES v2 over restJson1, through @aws-sdk/client-sesv2.
// Expected behavior: docs/research/ses.md sections 2.3, 4.4, 5.4, 6 and 9.3.
// This client (3.1160.0) sends ListEmailIdentities as POST
// /v2/email/list-identities (ses.md 3.2).

import { describe, expect, test } from "bun:test";
import { GetIdentityVerificationAttributesCommand, SESClient } from "@aws-sdk/client-ses";
import {
  CreateEmailIdentityCommand,
  DeleteEmailIdentityCommand,
  GetAccountCommand,
  GetEmailIdentityCommand,
  ListEmailIdentitiesCommand,
  ListTagsForResourceCommand,
  SendEmailCommand,
  SESv2Client,
  TagResourceCommand,
  UntagResourceCommand,
} from "@aws-sdk/client-sesv2";
import { clientConfig, defaultAccount, endpoint, region } from "../support/env";
import { expectFailure, uniq } from "../support/helpers";

const sesv2 = new SESv2Client(clientConfig());
const ses = new SESClient(clientConfig());

describe("SES v2 email identities", () => {
  test("a domain reads back PENDING with three stable DKIM tokens, lists and tags, and is gone after delete", async () => {
    const domain = `${uniq("sesv2-domain")}.example.org`;
    const created = await sesv2.send(
      new CreateEmailIdentityCommand({ EmailIdentity: domain, Tags: [{ Key: "env", Value: "dev" }] }),
    );
    expect(created.IdentityType).toBe("DOMAIN");
    expect(created.VerifiedForSendingStatus).toBe(false);
    // The ses.md 5.4 domain column.
    const got = await sesv2.send(new GetEmailIdentityCommand({ EmailIdentity: domain }));
    expect(got.VerificationStatus).toBe("PENDING");
    expect(got.DkimAttributes?.Status).toBe("PENDING");
    expect(got.DkimAttributes?.Tokens).toHaveLength(3);
    expect(got.DkimAttributes?.NextSigningKeyLength).toBe("RSA_2048_BIT");
    expect(got.DkimAttributes?.CurrentSigningKeyLength).toBe("RSA_2048_BIT");
    expect(got.DkimAttributes?.LastKeyGenerationTimestamp).toBeInstanceOf(Date);
    const again = await sesv2.send(new GetEmailIdentityCommand({ EmailIdentity: domain }));
    expect(again.DkimAttributes?.Tokens).toEqual(got.DkimAttributes?.Tokens ?? []);
    // Listed regardless of status (ses.md 5.4).
    const listed = await sesv2.send(new ListEmailIdentitiesCommand({ PageSize: 1000 }));
    expect((listed.EmailIdentities ?? []).map((i) => i.IdentityName)).toContain(domain);
    // Tags through the tag operations on the identity ARN (ses.md 5.4, 9.3).
    const arn = `arn:aws:ses:${region}:${defaultAccount}:identity/${domain}`;
    await sesv2.send(new TagResourceCommand({ ResourceArn: arn, Tags: [{ Key: "team", Value: "mail" }] }));
    await sesv2.send(new UntagResourceCommand({ ResourceArn: arn, TagKeys: ["env"] }));
    const tags = await sesv2.send(new ListTagsForResourceCommand({ ResourceArn: arn }));
    expect(tags.Tags).toEqual([{ Key: "team", Value: "mail" }]);
    await sesv2.send(new DeleteEmailIdentityCommand({ EmailIdentity: domain }));
    // NotFoundException, 404 per the model (ses.md 3.3), read by Terraform as gone (9.3).
    await expectFailure(sesv2.send(new GetEmailIdentityCommand({ EmailIdentity: domain })), {
      name: "NotFoundException",
      status: 404,
    });
  });

  test("an address has a NOT_STARTED DKIM block and v1 reads it Pending", async () => {
    const addr = `${uniq("sesv2-address")}@example.com`;
    await sesv2.send(new CreateEmailIdentityCommand({ EmailIdentity: addr }));
    try {
      // The ses.md 5.4 email column.
      const got = await sesv2.send(new GetEmailIdentityCommand({ EmailIdentity: addr }));
      expect(got.DkimAttributes?.Status).toBe("NOT_STARTED");
      expect(got.DkimAttributes?.Tokens ?? []).toEqual([]);
      expect(got.DkimAttributes?.NextSigningKeyLength).toBe("RSA_1024_BIT");
      // One store behind both APIs (ses.md 2.3, switch ses.store.shared-v1-v2).
      const attrs = await ses.send(new GetIdentityVerificationAttributesCommand({ Identities: [addr] }));
      expect(attrs.VerificationAttributes?.[addr]?.VerificationStatus).toBe("Pending");
    } finally {
      await sesv2.send(new DeleteEmailIdentityCommand({ EmailIdentity: addr }));
    }
  });
});

describe("SES v2 account", () => {
  test("GetAccount reports the sandbox and its quota", async () => {
    // ProductionAccessEnabled false is the sandbox (model, ses.md 6.3), with
    // the recorded sandbox quota of 200 and 1 (ses.md 6.3).
    const account = await sesv2.send(new GetAccountCommand({}));
    expect(account.ProductionAccessEnabled).toBe(false);
    expect(account.SendQuota?.Max24HourSend).toBe(200);
    expect(account.SendQuota?.MaxSendRate).toBe(1);
  });
});

const simulator = "success@simulator.amazonses.com";
const simple = { Simple: { Subject: { Data: "hello" }, Body: { Text: { Data: "text" } } } };

/** The part of a recorded message these tests read. */
interface SesMessage {
  api?: string;
  subject?: { data: string };
  text?: { data: string };
  attachments?: Array<{ fileName: string; content: string }>;
}

/** The recorded SES messages the filters select (GET /_understudy/v1/ses/messages). */
async function sesMessages(query: Record<string, string>): Promise<SesMessage[]> {
  const res = await fetch(`${endpoint}/_understudy/v1/ses/messages?${new URLSearchParams(query)}`);
  expect(res.status).toBe(200);
  // SAFETY: the admin API answers SESMessagePage (admin/ses.go); the test reads only these members.
  return ((await res.json()) as { messages: SesMessage[] }).messages;
}

describe("SES v2 SendEmail", () => {
  test("an unverified FromEmailAddress is MessageRejected", async () => {
    const from = `${uniq("sesv2-unverified")}@example.com`;
    // FromEmailAddress "has to be verified" (model, ses.md 4.4); the text is
    // the documented one (ses.md 6.1).
    const failure = await expectFailure(
      sesv2.send(new SendEmailCommand({ FromEmailAddress: from, Destination: { ToAddresses: [simulator] }, Content: simple })),
      {
        name: "MessageRejected",
        status: 400,
        messageIncludes: "Email address is not verified. The following identities failed the check in region ",
      },
    );
    expect(failure.message).toContain(from);
  });

  test("a verified sender's Simple send is recorded with its attachment", async () => {
    const from = `${uniq("sesv2-sender")}@example.com`;
    await sesv2.send(new CreateEmailIdentityCommand({ EmailIdentity: from }));
    try {
      const [verification] = await sesMessages({ to: from });
      const link = /http\S*\/_understudy\/ses\/VerificationLink\?token=[A-Za-z0-9_-]+/.exec(verification?.text?.data ?? "");
      expect(link).not.toBeNull();
      expect((await fetch(link?.[0] ?? "")).status).toBe(200);
      // A verified sender may mail the mailbox simulator from the sandbox
      // (ses.md 6.1, 6.2); understudy records instead of sending (ses.md 11.2).
      const sent = await sesv2.send(
        new SendEmailCommand({
          FromEmailAddress: from,
          Destination: { ToAddresses: [simulator] },
          Content: {
            Simple: {
              ...simple.Simple,
              Attachments: [{ FileName: "report.txt", RawContent: new TextEncoder().encode("attached") }],
            },
          },
        }),
      );
      const [record] = await sesMessages({ messageId: sent.MessageId ?? "" });
      expect(record?.api).toBe("sesv2");
      expect(record?.subject?.data).toBe("hello");
      expect(record?.attachments?.map((a) => [a.fileName, atob(a.content)])).toEqual([["report.txt", "attached"]]);
    } finally {
      await sesv2.send(new DeleteEmailIdentityCommand({ EmailIdentity: from }));
    }
  });
});
