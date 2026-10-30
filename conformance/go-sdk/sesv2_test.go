package gosdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ses"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// Research: docs/research/ses.md sections 2.3, 4.4, 5.4, 6 and 9.3. This
// client (service/sesv2 v1.79.4, after v1.77.0) sends ListEmailIdentities
// as POST /v2/email/list-identities (ses.md 3.2).

// A domain made through v2 reads back PENDING with three stable Easy DKIM
// tokens and RSA_2048_BIT keys (ses.md 5.4 table), is listed, carries its
// tags through the tag operations (5.4, 9.3), and after
// DeleteEmailIdentity is NotFoundException, which Terraform reads as gone
// (9.3).
func TestSESv2DomainIdentityLifecycle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sesv2.NewFromConfig(awsCfg)
	domain := name(t, "sesv2-domain") + ".example.org"

	created, err := client.CreateEmailIdentity(ctx, &sesv2.CreateEmailIdentityInput{
		EmailIdentity: aws.String(domain),
		Tags:          []types.Tag{{Key: aws.String("env"), Value: aws.String("dev")}},
	})
	ok(t, err, "CreateEmailIdentity")
	t.Cleanup(func() {
		_, _ = client.DeleteEmailIdentity(context.Background(), &sesv2.DeleteEmailIdentityInput{EmailIdentity: aws.String(domain)})
	})
	if created.IdentityType != types.IdentityTypeDomain || created.VerifiedForSendingStatus {
		t.Errorf("CreateEmailIdentity = %+v, want DOMAIN not verified for sending", created)
	}

	got, err := client.GetEmailIdentity(ctx, &sesv2.GetEmailIdentityInput{EmailIdentity: aws.String(domain)})
	ok(t, err, "GetEmailIdentity")
	d := got.DkimAttributes
	if got.VerificationStatus != types.VerificationStatusPending || d == nil || d.Status != types.DkimStatusPending ||
		len(d.Tokens) != 3 || d.NextSigningKeyLength != types.DkimSigningKeyLengthRsa2048Bit ||
		d.CurrentSigningKeyLength != types.DkimSigningKeyLengthRsa2048Bit || d.LastKeyGenerationTimestamp == nil {
		t.Errorf("GetEmailIdentity = %+v, DKIM %+v; want the ses.md 5.4 domain column", got, d)
	}

	listed, err := client.ListEmailIdentities(ctx, &sesv2.ListEmailIdentitiesInput{PageSize: aws.Int32(1000)})
	ok(t, err, "ListEmailIdentities")
	found := false
	for _, info := range listed.EmailIdentities {
		found = found || aws.ToString(info.IdentityName) == domain
	}
	if !found {
		t.Errorf("ListEmailIdentities omits %s: %+v", domain, listed.EmailIdentities)
	}

	arn := "arn:aws:ses:" + awsCfg.Region + ":000000000000:identity/" + domain
	_, err = client.TagResource(ctx, &sesv2.TagResourceInput{ResourceArn: aws.String(arn), Tags: []types.Tag{{Key: aws.String("team"), Value: aws.String("mail")}}})
	ok(t, err, "TagResource")
	_, err = client.UntagResource(ctx, &sesv2.UntagResourceInput{ResourceArn: aws.String(arn), TagKeys: []string{"env"}})
	ok(t, err, "UntagResource")
	tags, err := client.ListTagsForResource(ctx, &sesv2.ListTagsForResourceInput{ResourceArn: aws.String(arn)})
	ok(t, err, "ListTagsForResource")
	if len(tags.Tags) != 1 || aws.ToString(tags.Tags[0].Key) != "team" {
		t.Errorf("ListTagsForResource = %+v, want team alone", tags.Tags)
	}

	_, err = client.DeleteEmailIdentity(ctx, &sesv2.DeleteEmailIdentityInput{EmailIdentity: aws.String(domain)})
	ok(t, err, "DeleteEmailIdentity")
	_, err = client.GetEmailIdentity(ctx, &sesv2.GetEmailIdentityInput{EmailIdentity: aws.String(domain)})
	var missing *types.NotFoundException
	if !errors.As(err, &missing) {
		t.Errorf("GetEmailIdentity after delete = %v, want NotFoundException", err)
	}
}

// An address made through v2 has a DKIM block with NOT_STARTED and no
// tokens (ses.md 5.4 table), and v1 reads it Pending: one store behind
// both APIs (2.3, switch ses.store.shared-v1-v2).
func TestSESv2EmailIdentitySharedWithV1(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sesv2.NewFromConfig(awsCfg)
	addr := name(t, "sesv2-address") + "@example.com"

	_, err := client.CreateEmailIdentity(ctx, &sesv2.CreateEmailIdentityInput{EmailIdentity: aws.String(addr)})
	ok(t, err, "CreateEmailIdentity")
	t.Cleanup(func() {
		_, _ = client.DeleteEmailIdentity(context.Background(), &sesv2.DeleteEmailIdentityInput{EmailIdentity: aws.String(addr)})
	})
	got, err := client.GetEmailIdentity(ctx, &sesv2.GetEmailIdentityInput{EmailIdentity: aws.String(addr)})
	ok(t, err, "GetEmailIdentity")
	if d := got.DkimAttributes; got.IdentityType != types.IdentityTypeEmailAddress || d == nil || d.Status != types.DkimStatusNotStarted ||
		len(d.Tokens) != 0 || d.NextSigningKeyLength != types.DkimSigningKeyLengthRsa1024Bit {
		t.Errorf("GetEmailIdentity = %+v, DKIM %+v; want the ses.md 5.4 email column", got, d)
	}
	attrs, err := ses.NewFromConfig(awsCfg).GetIdentityVerificationAttributes(ctx, &ses.GetIdentityVerificationAttributesInput{Identities: []string{addr}})
	ok(t, err, "GetIdentityVerificationAttributes")
	if a, found := attrs.VerificationAttributes[addr]; !found || a.VerificationStatus != "Pending" {
		t.Errorf("v1 reads the v2 address as %+v, want Pending", attrs.VerificationAttributes)
	}
}

// An unverified FromEmailAddress "has to be verified" (model, ses.md 4.4):
// MessageRejected with the documented text naming it (ses.md 6.1).
func TestSESv2SendEmailRejectsAnUnverifiedFrom(t *testing.T) {
	t.Parallel()
	from := name(t, "sesv2-unverified") + "@example.com"
	_, err := sesv2.NewFromConfig(awsCfg).SendEmail(t.Context(), simpleV2(from))
	apiErr(t, err, "SendEmail", "MessageRejected")
	wantMessageContains(t, err, "SendEmail", "Email address is not verified. The following identities failed the check in region ")
	wantMessageContains(t, err, "SendEmail", from)
}

// A verified sender's Simple send to the mailbox simulator is accepted
// from the sandbox (ses.md 6.1, 6.2) and understudy records it, with its
// attachment, instead of sending it (ses.md 11.2).
func TestSESv2SendEmailFromAVerifiedAddressIsRecorded(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := sesv2.NewFromConfig(awsCfg)
	addr := name(t, "sesv2-sender") + "@example.com"
	_, err := client.CreateEmailIdentity(ctx, &sesv2.CreateEmailIdentityInput{EmailIdentity: aws.String(addr)})
	ok(t, err, "CreateEmailIdentity")
	t.Cleanup(func() {
		_, _ = client.DeleteEmailIdentity(context.Background(), &sesv2.DeleteEmailIdentityInput{EmailIdentity: aws.String(addr)})
	})
	verification := sesMessages(t, url.Values{"to": {addr}})
	if len(verification) != 1 {
		t.Fatalf("messages to %s = %+v, want the verification email", addr, verification)
	}
	link := verificationLink.FindString(verification[0].Text.Data)
	if link == "" {
		t.Fatalf("verification email has no link: %q", verification[0].Text.Data)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, http.NoBody)
	ok(t, err, "GET verification link")
	resp, err := http.DefaultClient.Do(req)
	ok(t, err, "GET verification link")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", link, resp.StatusCode)
	}

	in := simpleV2(addr)
	in.Content.Simple.Attachments = []types.Attachment{{FileName: aws.String("report.txt"), RawContent: []byte("attached")}}
	out, err := client.SendEmail(ctx, in)
	ok(t, err, "SendEmail")
	id := aws.ToString(out.MessageId)
	got := sesMessages(t, url.Values{"messageId": {id}})
	if len(got) != 1 || got[0].Kind != "send" || got[0].API != "sesv2" || got[0].Subject == nil || got[0].Subject.Data != "subject" ||
		len(got[0].Attachments) != 1 || got[0].Attachments[0].FileName != "report.txt" || string(got[0].Attachments[0].Content) != "attached" {
		t.Errorf("recorded %+v, want the v2 send with its attachment", got)
	}
}

// simpleV2 is a v2 Simple send from the given address to the mailbox
// simulator.
func simpleV2(from string) *sesv2.SendEmailInput {
	return &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(from),
		Destination:      &types.Destination{ToAddresses: []string{"success@simulator.amazonses.com"}},
		Content: &types.EmailContent{Simple: &types.Message{
			Subject: &types.Content{Data: aws.String("subject")},
			Body:    &types.Body{Text: &types.Content{Data: aws.String("text")}},
		}},
	}
}

// verificationLink finds the link in a verification email.
var verificationLink = regexp.MustCompile(`http\S*/_understudy/ses/VerificationLink\?token=[A-Za-z0-9_-]+`)

// sesMessage is the part of a recorded message these tests read, as GET
// /_understudy/v1/ses/messages answers it.
type sesMessage struct {
	MessageID string `json:"messageId"`
	API       string `json:"api"`
	Kind      string `json:"kind"`
	Subject   *struct {
		Data string `json:"data"`
	} `json:"subject"`
	Text struct {
		Data string `json:"data"`
	} `json:"text"`
	Attachments []struct {
		FileName string `json:"fileName"`
		Content  []byte `json:"content"`
	} `json:"attachments"`
}

// sesMessages is the recorded messages q selects.
func sesMessages(t *testing.T, q url.Values) []sesMessage {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint.String()+"/_understudy/v1/ses/messages?"+q.Encode(), http.NoBody)
	ok(t, err, "GET /_understudy/v1/ses/messages")
	resp, err := http.DefaultClient.Do(req)
	ok(t, err, "GET /_understudy/v1/ses/messages")
	defer func() { _ = resp.Body.Close() }()
	var page struct {
		Messages []sesMessage `json:"messages"`
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /_understudy/v1/ses/messages = %d", resp.StatusCode)
	}
	ok(t, json.NewDecoder(resp.Body).Decode(&page), "decoding the SES messages page")
	return page.Messages
}

// GetAccount reports the sandbox (ProductionAccessEnabled false, ses.md
// 6.3) with the recorded sandbox quota of 200 and 1 (6.3, LocalStack
// get-quota-0).
func TestSESv2GetAccount(t *testing.T) {
	t.Parallel()
	got, err := sesv2.NewFromConfig(awsCfg).GetAccount(t.Context(), &sesv2.GetAccountInput{})
	ok(t, err, "GetAccount")
	if got.ProductionAccessEnabled || got.SendQuota == nil || got.SendQuota.Max24HourSend != 200 || got.SendQuota.MaxSendRate != 1 {
		t.Errorf("GetAccount = %+v, quota %+v; want the sandbox with 200 and 1", got, got.SendQuota)
	}
}
