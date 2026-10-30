package gosdk_test

import (
	"context"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/aws-sdk-go-v2/service/ses/types"
)

// Research: docs/research/ses.md sections 5 and 9.1.

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
