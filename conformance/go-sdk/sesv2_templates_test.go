package gosdk_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	sestypes "github.com/aws/aws-sdk-go-v2/service/ses/types"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// Research: docs/research/ses.md sections 2.3, 3.2, 5.3 and 7.

// A v2 email template reads back its content and tags (ses.md 7), is
// listed, takes tags through its ARN (3.2), is read by v1 GetTemplate from
// the one store (2.3), and after DeleteEmailTemplate is NotFoundException,
// which the model declares for GetEmailTemplate (7).
func TestSESv2EmailTemplateLifecycle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sesv2.NewFromConfig(awsCfg)
	tmpl := name(t, "sesv2-template")

	_, err := client.CreateEmailTemplate(ctx, &sesv2.CreateEmailTemplateInput{
		TemplateName:    aws.String(tmpl),
		TemplateContent: &types.EmailTemplateContent{Subject: aws.String("Hi {{name}}"), Text: aws.String("Dear {{name}}")},
		Tags:            []types.Tag{{Key: aws.String("env"), Value: aws.String("dev")}},
	})
	ok(t, err, "CreateEmailTemplate")
	t.Cleanup(func() {
		_, _ = client.DeleteEmailTemplate(context.Background(), &sesv2.DeleteEmailTemplateInput{TemplateName: aws.String(tmpl)})
	})

	got, err := client.GetEmailTemplate(ctx, &sesv2.GetEmailTemplateInput{TemplateName: aws.String(tmpl)})
	ok(t, err, "GetEmailTemplate")
	if c := got.TemplateContent; c == nil || aws.ToString(c.Subject) != "Hi {{name}}" || aws.ToString(c.Text) != "Dear {{name}}" || c.Html != nil ||
		len(got.Tags) != 1 || aws.ToString(got.Tags[0].Key) != "env" {
		t.Errorf("GetEmailTemplate = %+v", got)
	}

	_, err = client.UpdateEmailTemplate(ctx, &sesv2.UpdateEmailTemplateInput{
		TemplateName:    aws.String(tmpl),
		TemplateContent: &types.EmailTemplateContent{Subject: aws.String("Hello {{name}}")},
	})
	ok(t, err, "UpdateEmailTemplate")
	v1, err := ses.NewFromConfig(awsCfg).GetTemplate(ctx, &ses.GetTemplateInput{TemplateName: aws.String(tmpl)})
	ok(t, err, "GetTemplate")
	if v1.Template == nil || aws.ToString(v1.Template.SubjectPart) != "Hello {{name}}" || v1.Template.TextPart != nil {
		t.Errorf("v1 GetTemplate = %+v, want the updated subject alone", v1.Template)
	}

	listed, err := client.ListEmailTemplates(ctx, &sesv2.ListEmailTemplatesInput{PageSize: aws.Int32(100)})
	ok(t, err, "ListEmailTemplates")
	if !slices.ContainsFunc(listed.TemplatesMetadata, func(m types.EmailTemplateMetadata) bool {
		return aws.ToString(m.TemplateName) == tmpl && m.CreatedTimestamp != nil
	}) {
		t.Errorf("ListEmailTemplates omits %s: %+v", tmpl, listed.TemplatesMetadata)
	}

	arn := "arn:aws:ses:" + awsCfg.Region + ":000000000000:template/" + tmpl
	_, err = client.TagResource(ctx, &sesv2.TagResourceInput{ResourceArn: aws.String(arn), Tags: []types.Tag{{Key: aws.String("team"), Value: aws.String("mail")}}})
	ok(t, err, "TagResource")
	_, err = client.UntagResource(ctx, &sesv2.UntagResourceInput{ResourceArn: aws.String(arn), TagKeys: []string{"env"}})
	ok(t, err, "UntagResource")
	tags, err := client.ListTagsForResource(ctx, &sesv2.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
	ok(t, err, "ListTagsForResource")
	if len(tags.Tags) != 1 || aws.ToString(tags.Tags[0].Key) != "team" {
		t.Errorf("ListTagsForResource = %+v, want team alone", tags.Tags)
	}

	_, err = client.DeleteEmailTemplate(ctx, &sesv2.DeleteEmailTemplateInput{TemplateName: aws.String(tmpl)})
	ok(t, err, "DeleteEmailTemplate")
	_, err = client.GetEmailTemplate(ctx, &sesv2.GetEmailTemplateInput{TemplateName: aws.String(tmpl)})
	var missing *types.NotFoundException
	if !errors.As(err, &missing) {
		t.Errorf("GetEmailTemplate after delete = %v, want NotFoundException", err)
	}
}

// The deprecated VerifyEmailAddress starts verification as
// VerifyEmailIdentity does, so the address is Pending (ses.md 5.2, 5.3) and
// not among the verified addresses; DeleteVerifiedEmailAddress removes it.
func TestSESLegacyEmailAddressOperations(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := ses.NewFromConfig(awsCfg)
	addr := name(t, "ses-legacy") + "@example.com"

	_, err := client.VerifyEmailAddress(ctx, &ses.VerifyEmailAddressInput{EmailAddress: aws.String(addr)})
	ok(t, err, "VerifyEmailAddress")
	t.Cleanup(func() {
		_, _ = client.DeleteIdentity(context.Background(), &ses.DeleteIdentityInput{Identity: aws.String(addr)})
	})
	attrs, err := client.GetIdentityVerificationAttributes(ctx, &ses.GetIdentityVerificationAttributesInput{Identities: []string{addr}})
	ok(t, err, "GetIdentityVerificationAttributes")
	if a, found := attrs.VerificationAttributes[addr]; !found || a.VerificationStatus != sestypes.VerificationStatusPending {
		t.Errorf("after VerifyEmailAddress = %+v, want Pending", attrs.VerificationAttributes)
	}

	verified, err := client.ListVerifiedEmailAddresses(ctx, &ses.ListVerifiedEmailAddressesInput{})
	ok(t, err, "ListVerifiedEmailAddresses")
	if slices.Contains(verified.VerifiedEmailAddresses, addr) {
		t.Errorf("ListVerifiedEmailAddresses lists the Pending %s", addr)
	}

	_, err = client.DeleteVerifiedEmailAddress(ctx, &ses.DeleteVerifiedEmailAddressInput{EmailAddress: aws.String(addr)})
	ok(t, err, "DeleteVerifiedEmailAddress")
	attrs, err = client.GetIdentityVerificationAttributes(ctx, &ses.GetIdentityVerificationAttributesInput{Identities: []string{addr}})
	ok(t, err, "GetIdentityVerificationAttributes")
	if _, found := attrs.VerificationAttributes[addr]; found {
		t.Errorf("after DeleteVerifiedEmailAddress = %+v, want %s gone", attrs.VerificationAttributes, addr)
	}
}
