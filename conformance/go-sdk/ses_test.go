package gosdk_test

import (
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
