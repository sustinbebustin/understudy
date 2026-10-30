package gosdk_test

import (
	"context"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/aws-sdk-go-v2/service/ses/types"
)

// Research: docs/research/ses.md sections 4, 5, 6 and 9.1; 4.2 for
// SendRawEmail.

// An email identity is Pending until its owner opens the link (ses.md
// 5.2), keyed by the request string (9.1); after DeleteIdentity the key is
// absent, which Terraform reads as gone (9.1).
func TestSESEmailIdentityLifecycle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := ses.NewFromConfig(awsCfg)
	addr := name(t, "ses-identity") + "@example.com"

	_, err := client.VerifyEmailIdentity(ctx, &ses.VerifyEmailIdentityInput{EmailAddress: aws.String(addr)})
	ok(t, err, "VerifyEmailIdentity")
	got, err := client.GetIdentityVerificationAttributes(ctx, &ses.GetIdentityVerificationAttributesInput{Identities: []string{addr}})
	ok(t, err, "GetIdentityVerificationAttributes")
	if a, found := got.VerificationAttributes[addr]; !found || a.VerificationStatus != types.VerificationStatusPending {
		t.Errorf("VerificationAttributes = %+v, want %s Pending", got.VerificationAttributes, addr)
	}

	_, err = client.DeleteIdentity(ctx, &ses.DeleteIdentityInput{Identity: aws.String(addr)})
	ok(t, err, "DeleteIdentity")
	got, err = client.GetIdentityVerificationAttributes(ctx, &ses.GetIdentityVerificationAttributesInput{Identities: []string{addr}})
	ok(t, err, "GetIdentityVerificationAttributes after delete")
	if len(got.VerificationAttributes) != 0 {
		t.Errorf("VerificationAttributes after DeleteIdentity = %+v, want none", got.VerificationAttributes)
	}
}

// A domain identity answers a 44-character token (ses.md 5.3), the same on
// a repeat and on every read (9.2), and ListIdentities lists it under
// IdentityType Domain, not EmailAddress (5.3).
func TestSESDomainIdentity(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := ses.NewFromConfig(awsCfg)
	domain := name(t, "ses-domain") + ".example.org"

	first, err := client.VerifyDomainIdentity(ctx, &ses.VerifyDomainIdentityInput{Domain: aws.String(domain)})
	ok(t, err, "VerifyDomainIdentity")
	t.Cleanup(func() {
		_, _ = client.DeleteIdentity(context.Background(), &ses.DeleteIdentityInput{Identity: aws.String(domain)})
	})
	token := aws.ToString(first.VerificationToken)
	if len(token) != 44 {
		t.Errorf("VerificationToken %q has %d characters, want 44", token, len(token))
	}
	again, err := client.VerifyDomainIdentity(ctx, &ses.VerifyDomainIdentityInput{Domain: aws.String(domain)})
	ok(t, err, "VerifyDomainIdentity again")
	if got := aws.ToString(again.VerificationToken); got != token {
		t.Errorf("a repeated VerifyDomainIdentity answers %q, want %q", got, token)
	}
	attrs, err := client.GetIdentityVerificationAttributes(ctx, &ses.GetIdentityVerificationAttributesInput{Identities: []string{domain}})
	ok(t, err, "GetIdentityVerificationAttributes")
	if a := attrs.VerificationAttributes[domain]; a.VerificationStatus != types.VerificationStatusPending || aws.ToString(a.VerificationToken) != token {
		t.Errorf("VerificationAttributes[%s] = %+v, want Pending with %q", domain, a, token)
	}

	listed := func(kind types.IdentityType) bool {
		p := ses.NewListIdentitiesPaginator(client, &ses.ListIdentitiesInput{IdentityType: kind, MaxItems: aws.Int32(1000)})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			ok(t, err, "ListIdentities")
			if slices.Contains(page.Identities, domain) {
				return true
			}
		}
		return false
	}
	asDomain, asAddress := listed(types.IdentityTypeDomain), listed(types.IdentityTypeEmailAddress)
	if !asDomain || asAddress {
		t.Errorf("ListIdentities lists %s as Domain %v, as EmailAddress %v; want Domain only", domain, asDomain, asAddress)
	}
}

// Tag validation runs before the sender check: an invalid tag name from an
// unverified Source answers InvalidParameterValue with the recorded text
// (third-party-recorded 2024-07-30, ses.md 4.5; localstack@8b9a79f
// tests/aws/services/ses/test_ses.snapshot.json:L592-L735, validated
// 2024-07-30).
func TestSESSendEmailRejectsAnInvalidTagName(t *testing.T) {
	t.Parallel()
	_, err := ses.NewFromConfig(awsCfg).SendEmail(t.Context(), &ses.SendEmailInput{
		Source:      aws.String(name(t, "ses-unverified") + "@example.com"),
		Destination: &types.Destination{ToAddresses: []string{"success@example.com"}},
		Message: &types.Message{
			Subject: &types.Content{Data: aws.String("subject")},
			Body:    &types.Body{Text: &types.Content{Data: aws.String("text")}},
		},
		Tags: []types.MessageTag{{Name: aws.String("test_invalid_name:123"), Value: aws.String("test")}},
	})
	apiErr(t, err, "SendEmail", "InvalidParameterValue")
	wantMessage(t, err, "SendEmail", "Invalid tag name <test_invalid_name:123>: only alphanumeric ASCII characters, '_',  '-' are allowed.")
}

// An unverified Source is refused with MessageRejected, the documented
// text naming it (aws-doc and capture, ses.md 6.1).
func TestSESSendEmailRejectsAnUnverifiedSource(t *testing.T) {
	t.Parallel()
	source := name(t, "ses-unverified") + "@example.com"
	_, err := ses.NewFromConfig(awsCfg).SendEmail(t.Context(), &ses.SendEmailInput{
		Source:      aws.String(source),
		Destination: &types.Destination{ToAddresses: []string{"success@simulator.amazonses.com"}},
		Message: &types.Message{
			Subject: &types.Content{Data: aws.String("subject")},
			Body:    &types.Body{Text: &types.Content{Data: aws.String("text")}},
		},
	})
	apiErr(t, err, "SendEmail", "MessageRejected")
	wantMessageContains(t, err, "SendEmail", "Email address is not verified. The following identities failed the check in region ")
	wantMessageContains(t, err, "SendEmail", source)
}

// A sandbox account's quota is 200 messages a day at 1 a second (golden
// get-quota-0, third-party-recorded 2024-11-27, ses.md 6.3;
// localstack@8b9a79f tests/aws/services/ses/test_ses.snapshot.json:L905-L918,
// validated 2024-11-27).
func TestSESGetSendQuota(t *testing.T) {
	t.Parallel()
	q, err := ses.NewFromConfig(awsCfg).GetSendQuota(t.Context(), &ses.GetSendQuotaInput{})
	ok(t, err, "GetSendQuota")
	if q.Max24HourSend != 200 || q.MaxSendRate != 1 {
		t.Errorf("GetSendQuota = %v a day at %v a second, want 200 and 1", q.Max24HourSend, q.MaxSendRate)
	}
}

// GetIdentityNotificationAttributes keys a known identity by the request
// string, which the aws_ses_email_identity data source requires (ses.md
// 9.1).
func TestSESNotificationAttributesKeyAKnownIdentity(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := ses.NewFromConfig(awsCfg)
	addr := name(t, "ses-notif") + "@example.com"
	_, err := client.VerifyEmailIdentity(ctx, &ses.VerifyEmailIdentityInput{EmailAddress: aws.String(addr)})
	ok(t, err, "VerifyEmailIdentity")
	t.Cleanup(func() {
		_, _ = client.DeleteIdentity(context.Background(), &ses.DeleteIdentityInput{Identity: aws.String(addr)})
	})
	out, err := client.GetIdentityNotificationAttributes(ctx, &ses.GetIdentityNotificationAttributesInput{Identities: []string{addr}})
	ok(t, err, "GetIdentityNotificationAttributes")
	if _, found := out.NotificationAttributes[addr]; !found || len(out.NotificationAttributes) != 1 {
		t.Errorf("NotificationAttributes = %+v, want the one key %s", out.NotificationAttributes, addr)
	}
}

// Golden test_send_email_raises_message_rejected: a raw message from an
// unverified From to an unverified destination answers MessageRejected
// (third-party-recorded 2025-09-11, ses.md 4.2; localstack@8b9a79f
// tests/aws/services/ses/test_ses.py:L1162-L1170, validated 2025-09-11).
func TestSESSendRawEmailRaisesMessageRejected(t *testing.T) {
	t.Parallel()
	raw := "From: origin@example.com\r\nTo: invalid@example.com\r\nSubject: test\r\n\r\nThis is the message body.\r\n"
	_, err := ses.NewFromConfig(awsCfg).SendRawEmail(t.Context(), &ses.SendRawEmailInput{
		Destinations: []string{"invalid@example.com"},
		RawMessage:   &types.RawMessage{Data: []byte(raw)},
	})
	apiErr(t, err, "SendRawEmail", "MessageRejected")
}

// Goldens test_list_templates and test_delete_template: ListTemplates
// lists a template by Name with its CreatedTimestamp and not after
// DeleteTemplate (third-party-recorded 2023-08-25, ses.md 7;
// localstack@8b9a79f tests/aws/services/ses/test_ses.snapshot.json:L736-L807,
// validated 2023-08-25). GetTemplate echoes the parts given and omits the
// rest; UpdateTemplate replaces them; a missing template is
// TemplateDoesNotExist and a duplicate AlreadyExists (ses.md 7, 9.4).
func TestSESTemplateLifecycle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := ses.NewFromConfig(awsCfg)
	tmpl := name(t, "ses-template")
	listed := func() bool {
		t.Helper()
		out, err := client.ListTemplates(ctx, &ses.ListTemplatesInput{MaxItems: aws.Int32(100)})
		ok(t, err, "ListTemplates")
		return slices.ContainsFunc(out.TemplatesMetadata, func(m types.TemplateMetadata) bool {
			return aws.ToString(m.Name) == tmpl && m.CreatedTimestamp != nil
		})
	}

	_, err := client.CreateTemplate(ctx, &ses.CreateTemplateInput{Template: &types.Template{
		TemplateName: aws.String(tmpl), SubjectPart: aws.String("Hi {{name}}"), TextPart: aws.String("Dear {{name}}"),
	}})
	ok(t, err, "CreateTemplate")
	if !listed() {
		t.Errorf("ListTemplates omits %s", tmpl)
	}
	got, err := client.GetTemplate(ctx, &ses.GetTemplateInput{TemplateName: aws.String(tmpl)})
	ok(t, err, "GetTemplate")
	if aws.ToString(got.Template.SubjectPart) != "Hi {{name}}" || aws.ToString(got.Template.TextPart) != "Dear {{name}}" || got.Template.HtmlPart != nil {
		t.Errorf("GetTemplate = %+v, want the parts as given and no HtmlPart", got.Template)
	}
	_, err = client.CreateTemplate(ctx, &ses.CreateTemplateInput{Template: &types.Template{TemplateName: aws.String(tmpl)}})
	apiErr(t, err, "CreateTemplate", "AlreadyExists")

	_, err = client.UpdateTemplate(ctx, &ses.UpdateTemplateInput{Template: &types.Template{
		TemplateName: aws.String(tmpl), HtmlPart: aws.String("<p>new</p>"),
	}})
	ok(t, err, "UpdateTemplate")
	got, err = client.GetTemplate(ctx, &ses.GetTemplateInput{TemplateName: aws.String(tmpl)})
	ok(t, err, "GetTemplate after UpdateTemplate")
	if aws.ToString(got.Template.HtmlPart) != "<p>new</p>" || got.Template.SubjectPart != nil {
		t.Errorf("GetTemplate after UpdateTemplate = %+v, want only HtmlPart", got.Template)
	}

	_, err = client.DeleteTemplate(ctx, &ses.DeleteTemplateInput{TemplateName: aws.String(tmpl)})
	ok(t, err, "DeleteTemplate")
	if listed() {
		t.Errorf("ListTemplates still lists %s after DeleteTemplate", tmpl)
	}
	_, err = client.GetTemplate(ctx, &ses.GetTemplateInput{TemplateName: aws.String(tmpl)})
	apiErr(t, err, "GetTemplate", "TemplateDoesNotExist")
}
