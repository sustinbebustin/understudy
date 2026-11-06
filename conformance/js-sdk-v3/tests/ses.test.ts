// SES v1 over awsQuery, through @aws-sdk/client-ses.
// Expected behavior: docs/research/ses.md sections 4 (4.2 for SendRawEmail, 4.3 for
// SendTemplatedEmail), 5, 6, 7, 9.1 and 9.2.

import { describe, expect, test } from "bun:test";
import {
  CreateTemplateCommand,
  DeleteIdentityCommand,
  DeleteTemplateCommand,
  GetIdentityNotificationAttributesCommand,
  GetIdentityVerificationAttributesCommand,
  GetSendQuotaCommand,
  GetTemplateCommand,
  ListTemplatesCommand,
  paginateListIdentities,
  SendEmailCommand,
  SendRawEmailCommand,
  SendTemplatedEmailCommand,
  SESClient,
  UpdateTemplateCommand,
  VerifyDomainIdentityCommand,
  VerifyEmailIdentityCommand,
} from "@aws-sdk/client-ses";
import { clientConfig, endpoint } from "../support/env";
import { expectFailure, uniq } from "../support/helpers";

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

describe("SES SendEmail validation", () => {
  test("an invalid tag name answers InvalidParameterValue before the sender check", async () => {
    // Recorded text (third-party-recorded 2024-07-30, ses.md 4.5;
    // localstack@8b9a79f tests/aws/services/ses/test_ses.snapshot.json:L592-L735,
    // validated 2024-07-30), from an unverified Source.
    const failure = await expectFailure(
      ses.send(
        new SendEmailCommand({
          Source: `${uniq("ses-unverified")}@example.com`,
          Destination: { ToAddresses: ["success@example.com"] },
          Message: { Subject: { Data: "subject" }, Body: { Text: { Data: "text" } } },
          Tags: [{ Name: "test_invalid_name:123", Value: "test" }],
        }),
      ),
      { name: "InvalidParameterValue", status: 400 },
    );
    expect(failure.message).toBe(
      "Invalid tag name <test_invalid_name:123>: only alphanumeric ASCII characters, '_',  '-' are allowed.",
    );
  });

  test("an unverified Source answers MessageRejected naming it", async () => {
    // The documented text (aws-doc and capture, ses.md 6.1).
    const source = `${uniq("ses-unverified")}@example.com`;
    const failure = await expectFailure(
      ses.send(
        new SendEmailCommand({
          Source: source,
          Destination: { ToAddresses: ["success@simulator.amazonses.com"] },
          Message: { Subject: { Data: "subject" }, Body: { Text: { Data: "text" } } },
        }),
      ),
      { name: "MessageRejected", status: 400 },
    );
    expect(failure.message).toStartWith(
      "Email address is not verified. The following identities failed the check in region ",
    );
    expect(failure.message).toContain(source);
  });
});

describe("SES quota and notification attributes", () => {
  test("a sandbox account's quota is 200 a day at 1 a second", async () => {
    // Golden get-quota-0 (third-party-recorded 2024-11-27, ses.md 6.3;
    // localstack@8b9a79f tests/aws/services/ses/test_ses.snapshot.json:L905-L918,
    // validated 2024-11-27).
    const quota = await ses.send(new GetSendQuotaCommand({}));
    expect(quota.Max24HourSend).toBe(200);
    expect(quota.MaxSendRate).toBe(1);
  });

  test("GetIdentityNotificationAttributes keys a known identity", async () => {
    const addr = `${uniq("ses-notif")}@example.com`;
    await ses.send(new VerifyEmailIdentityCommand({ EmailAddress: addr }));
    try {
      // The aws_ses_email_identity data source needs the key (ses.md 9.1).
      const out = await ses.send(new GetIdentityNotificationAttributesCommand({ Identities: [addr] }));
      expect(Object.keys(out.NotificationAttributes ?? {})).toEqual([addr]);
    } finally {
      await ses.send(new DeleteIdentityCommand({ Identity: addr }));
    }
  });
});

describe("SES SendRawEmail", () => {
  test("an unverified raw From and destination answer MessageRejected", async () => {
    // Golden test_send_email_raises_message_rejected (third-party-recorded
    // 2025-09-11, ses.md 4.2; localstack@8b9a79f
    // tests/aws/services/ses/test_ses.py:L1162-L1170, validated 2025-09-11).
    const raw = "From: origin@example.com\r\nTo: invalid@example.com\r\nSubject: test\r\n\r\nThis is the message body.\r\n";
    await expectFailure(
      ses.send(
        new SendRawEmailCommand({
          Destinations: ["invalid@example.com"],
          RawMessage: { Data: new TextEncoder().encode(raw) },
        }),
      ),
      { name: "MessageRejected", status: 400 },
    );
  });
});

describe("SES templates", () => {
  const templateNames = async () => {
    const names: string[] = [];
    let NextToken: string | undefined;
    do {
      const page = await ses.send(new ListTemplatesCommand({ NextToken }));
      for (const m of page.TemplatesMetadata ?? []) {
        expect(m.CreatedTimestamp).toBeInstanceOf(Date);
        names.push(m.Name ?? "");
      }
      NextToken = page.NextToken;
    } while (NextToken);
    return names;
  };

  test("create, read, update, list and delete a template", async () => {
    // Goldens test_list_templates and test_delete_template: TemplatesMetadata
    // of {CreatedTimestamp, Name}, and the template gone after DeleteTemplate
    // (third-party-recorded 2023-08-25, ses.md 7; localstack@8b9a79f
    // tests/aws/services/ses/test_ses.snapshot.json:L736-L807, validated
    // 2023-08-25).
    const name = uniq("ses-template");
    await ses.send(
      new CreateTemplateCommand({ Template: { TemplateName: name, SubjectPart: "Hi {{name}}", TextPart: "Dear {{name}}" } }),
    );
    expect(await templateNames()).toContain(name);
    // GetTemplate echoes the parts given and omits the rest; UpdateTemplate
    // replaces them (ses.md 9.4).
    const got = await ses.send(new GetTemplateCommand({ TemplateName: name }));
    expect(got.Template).toEqual({ TemplateName: name, SubjectPart: "Hi {{name}}", TextPart: "Dear {{name}}" });
    // The duplicate carries a text part: AWS refuses a part-less template
    // with InvalidParameterValue before it checks the name (SES-18 02-dup).
    await expectFailure(ses.send(new CreateTemplateCommand({ Template: { TemplateName: name, TextPart: "again" } })), {
      name: "AlreadyExistsException",
      status: 400,
    });
    await ses.send(new UpdateTemplateCommand({ Template: { TemplateName: name, HtmlPart: "<p>new</p>" } }));
    const updated = await ses.send(new GetTemplateCommand({ TemplateName: name }));
    expect(updated.Template).toEqual({ TemplateName: name, HtmlPart: "<p>new</p>" });
    await ses.send(new DeleteTemplateCommand({ TemplateName: name }));
    expect(await templateNames()).not.toContain(name);
    // A missing template is TemplateDoesNotExist (ses.md 7).
    await expectFailure(ses.send(new GetTemplateCommand({ TemplateName: name })), {
      name: "TemplateDoesNotExistException",
      status: 400,
    });
  });

  test("SendTemplatedEmail renders {{name}} from a stored template and is recorded", async () => {
    const from = `${uniq("ses-tmpl-sender")}@example.com`;
    const name = uniq("ses-tmpl");
    await ses.send(new VerifyEmailIdentityCommand({ EmailAddress: from }));
    try {
      const [verification] = await sesMessages({ to: from });
      const link = /http\S*\/_understudy\/ses\/VerificationLink\?token=[A-Za-z0-9_-]+/.exec(verification?.text?.data ?? "");
      expect(link).not.toBeNull();
      expect((await fetch(link?.[0] ?? "")).status).toBe(200);
      const send = new SendTemplatedEmailCommand({
        Source: from,
        Destination: { ToAddresses: ["success@simulator.amazonses.com"] },
        Template: name,
        TemplateData: JSON.stringify({ name: "x" }),
      });
      // A missing template is TemplateDoesNotExist (model, ses.md 3.3).
      await expectFailure(ses.send(send), { name: "TemplateDoesNotExistException", status: 400 });
      // SES-18/07's send: accepted with a MessageId (ses.md 4.3); understudy
      // records the {{name}} substitution (ses.md 7, 11.6 item 6).
      await ses.send(
        new CreateTemplateCommand({ Template: { TemplateName: name, SubjectPart: "Hi {{name}}", TextPart: "Dear {{name}}" } }),
      );
      const sent = await ses.send(send);
      const [record] = await sesMessages({ messageId: sent.MessageId ?? "" });
      expect(record?.operation).toBe("SendTemplatedEmail");
      expect(record?.subject?.data).toBe("Hi x");
      expect(record?.text?.data).toBe("Dear x");
      expect(record?.template?.name).toBe(name);
    } finally {
      await ses.send(new DeleteTemplateCommand({ TemplateName: name }));
      await ses.send(new DeleteIdentityCommand({ Identity: from }));
    }
  });
});

/** The part of a recorded message these tests read. */
interface SesMessage {
  operation?: string;
  subject?: { data: string };
  text?: { data: string };
  template?: { name?: string; data: string };
}

/** The recorded SES messages the filters select (GET /_understudy/v1/ses/messages). */
async function sesMessages(query: Record<string, string>): Promise<SesMessage[]> {
  const res = await fetch(`${endpoint}/_understudy/v1/ses/messages?${new URLSearchParams(query)}`);
  expect(res.status).toBe(200);
  // SAFETY: the admin API answers SESMessagePage (admin/ses.go); the test reads only these members.
  return ((await res.json()) as { messages: SesMessage[] }).messages;
}
