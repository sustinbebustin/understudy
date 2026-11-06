// SES v2 email templates and the deprecated v1 email-address operations.
// Expected behavior: docs/research/ses.md sections 2.3, 3.2, 5.3 and 7.

import { describe, expect, test } from "bun:test";
import {
  DeleteIdentityCommand,
  DeleteTemplateCommand,
  DeleteVerifiedEmailAddressCommand,
  GetIdentityVerificationAttributesCommand,
  GetTemplateCommand,
  ListVerifiedEmailAddressesCommand,
  SESClient,
  VerifyEmailAddressCommand,
} from "@aws-sdk/client-ses";
import {
  CreateEmailTemplateCommand,
  DeleteEmailTemplateCommand,
  GetEmailTemplateCommand,
  ListEmailTemplatesCommand,
  ListTagsForResourceCommand,
  SESv2Client,
  TagResourceCommand,
  UntagResourceCommand,
  UpdateEmailTemplateCommand,
} from "@aws-sdk/client-sesv2";
import { clientConfig, defaultAccount, region } from "../support/env";
import { expectFailure, uniq } from "../support/helpers";

const sesv2 = new SESv2Client(clientConfig());
const ses = new SESClient(clientConfig());

describe("SES v2 email templates", () => {
  test("a template reads back its content and tags, is shared with v1, lists, tags by ARN and is gone after delete", async () => {
    const name = uniq("sesv2-template");
    await sesv2.send(
      new CreateEmailTemplateCommand({
        TemplateName: name,
        TemplateContent: { Subject: "Hi {{name}}", Text: "Dear {{name}}" },
        Tags: [{ Key: "env", Value: "dev" }],
      }),
    );
    try {
      const got = await sesv2.send(new GetEmailTemplateCommand({ TemplateName: name }));
      expect(got.TemplateContent).toEqual({ Subject: "Hi {{name}}", Text: "Dear {{name}}" });
      expect(got.Tags).toEqual([{ Key: "env", Value: "dev" }]);
      // One store for v1 and v2 templates (ses.md 2.3).
      await sesv2.send(new UpdateEmailTemplateCommand({ TemplateName: name, TemplateContent: { Subject: "Hello {{name}}" } }));
      const v1 = await ses.send(new GetTemplateCommand({ TemplateName: name }));
      expect(v1.Template?.SubjectPart).toBe("Hello {{name}}");
      expect(v1.Template?.TextPart).toBeUndefined();
      const listed = await sesv2.send(new ListEmailTemplatesCommand({ PageSize: 100 }));
      expect((listed.TemplatesMetadata ?? []).map((m) => m.TemplateName)).toContain(name);
      // Tags through the tag operations on the template ARN (ses.md 3.2).
      const arn = `arn:aws:ses:${region}:${defaultAccount}:template/${name}`;
      await sesv2.send(new TagResourceCommand({ ResourceArn: arn, Tags: [{ Key: "team", Value: "mail" }] }));
      await sesv2.send(new UntagResourceCommand({ ResourceArn: arn, TagKeys: ["env"] }));
      const tags = await sesv2.send(new ListTagsForResourceCommand({ ResourceArn: arn }));
      expect(tags.Tags).toEqual([{ Key: "team", Value: "mail" }]);
      await sesv2.send(new DeleteEmailTemplateCommand({ TemplateName: name }));
      // NotFoundException, 404 per the model (ses.md 3.3, 7).
      await expectFailure(sesv2.send(new GetEmailTemplateCommand({ TemplateName: name })), {
        name: "NotFoundException",
        status: 404,
      });
    } finally {
      await ses.send(new DeleteTemplateCommand({ TemplateName: name }));
    }
  });
});

describe("SES deprecated email-address operations", () => {
  test("VerifyEmailAddress starts Pending, is not listed as verified, and DeleteVerifiedEmailAddress removes it", async () => {
    const addr = `${uniq("ses-legacy")}@example.com`;
    // VerifyEmailAddress is the deprecated VerifyEmailIdentity (ses.md 5.3).
    await ses.send(new VerifyEmailAddressCommand({ EmailAddress: addr }));
    try {
      const attrs = await ses.send(new GetIdentityVerificationAttributesCommand({ Identities: [addr] }));
      expect(attrs.VerificationAttributes?.[addr]?.VerificationStatus).toBe("Pending");
      const verified = await ses.send(new ListVerifiedEmailAddressesCommand({}));
      expect(verified.VerifiedEmailAddresses ?? []).not.toContain(addr);
      await ses.send(new DeleteVerifiedEmailAddressCommand({ EmailAddress: addr }));
      const after = await ses.send(new GetIdentityVerificationAttributesCommand({ Identities: [addr] }));
      expect(after.VerificationAttributes?.[addr]).toBeUndefined();
    } finally {
      await ses.send(new DeleteIdentityCommand({ Identity: addr }));
    }
  });
});
