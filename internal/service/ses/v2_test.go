package ses_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

func (h *harness) createIdentity(name string, tags ...sesv2api.Tag) *sesv2api.CreateEmailIdentityOutput {
	h.Helper()
	return must(h, sesv2api.OpCreateEmailIdentity, &sesv2api.CreateEmailIdentityInput{EmailIdentity: name, Tags: tags})
}

func (h *harness) getIdentity(name string) *sesv2api.GetEmailIdentityOutput {
	h.Helper()
	return must(h, sesv2api.OpGetEmailIdentity, &sesv2api.GetEmailIdentityInput{EmailIdentity: name})
}

// What AWS returned right after CreateEmailIdentity, as Terraform's
// acceptance tests assert (provider, ses.md 5.4 table): an address has a
// non-null DKIM block with NOT_STARTED, no tokens, RSA_1024_BIT next and no
// current key; a domain PENDING with three tokens, RSA_2048_BIT both, and a
// key generation time. Both are PENDING and not verified for sending.
func TestGetEmailIdentityAttributeTable(t *testing.T) {
	run(t, func(h *harness) {
		for name, kind := range map[string]sesv2api.IdentityType{
			"person@example.com": sesv2api.IdentityTypeEmailAddress,
			"example.org":        sesv2api.IdentityTypeDomain,
		} {
			out := h.createIdentity(name)
			if out.IdentityType == nil || *out.IdentityType != kind || out.VerifiedForSendingStatus {
				h.Errorf("CreateEmailIdentity(%s) = %+v, want %s and not verified for sending (ses.md 5.4)", name, out, kind)
			}
		}

		a := h.getIdentity("person@example.com")
		if a.IdentityType == nil || *a.IdentityType != sesv2api.IdentityTypeEmailAddress ||
			a.VerificationStatus == nil || *a.VerificationStatus != sesv2api.VerificationStatusPending || a.VerifiedForSendingStatus {
			h.Errorf("address: type %v status %v verified %v, want EMAIL_ADDRESS PENDING false", a.IdentityType, a.VerificationStatus, a.VerifiedForSendingStatus)
		}
		switch d := a.DkimAttributes; {
		case d == nil:
			h.Error("address: DkimAttributes is null, want a block (ses.md 5.4)")
		case d.Status == nil || *d.Status != sesv2api.DkimStatusNotStarted || len(d.Tokens) != 0 ||
			d.NextSigningKeyLength == nil || *d.NextSigningKeyLength != sesv2api.DkimSigningKeyLengthRsa1024Bit ||
			d.CurrentSigningKeyLength != nil || d.LastKeyGenerationTimestamp != nil ||
			d.SigningAttributesOrigin == nil || *d.SigningAttributesOrigin != sesv2api.DkimSigningAttributesOriginAwsSes:
			h.Errorf("address DKIM = %+v, want the ses.md 5.4 email column", d)
		}

		dom := h.getIdentity("example.org")
		if dom.IdentityType == nil || *dom.IdentityType != sesv2api.IdentityTypeDomain ||
			dom.VerificationStatus == nil || *dom.VerificationStatus != sesv2api.VerificationStatusPending || dom.VerifiedForSendingStatus {
			h.Errorf("domain: type %v status %v verified %v, want DOMAIN PENDING false", dom.IdentityType, dom.VerificationStatus, dom.VerifiedForSendingStatus)
		}
		switch d := dom.DkimAttributes; {
		case d == nil:
			h.Error("domain: DkimAttributes is null")
		case d.Status == nil || *d.Status != sesv2api.DkimStatusPending || len(d.Tokens) != 3 ||
			d.NextSigningKeyLength == nil || *d.NextSigningKeyLength != sesv2api.DkimSigningKeyLengthRsa2048Bit ||
			d.CurrentSigningKeyLength == nil || *d.CurrentSigningKeyLength != sesv2api.DkimSigningKeyLengthRsa2048Bit ||
			d.LastKeyGenerationTimestamp == nil ||
			d.SigningAttributesOrigin == nil || *d.SigningAttributesOrigin != sesv2api.DkimSigningAttributesOriginAwsSes:
			h.Errorf("domain DKIM = %+v, want the ses.md 5.4 domain column", d)
		default:
			// Stable tokens for an empty second plan (provider, ses.md 9.3).
			if again := h.getIdentity("example.org").DkimAttributes; again == nil || !slices.Equal(again.Tokens, d.Tokens) {
				h.Errorf("DKIM tokens changed between reads: %q then %+v", d.Tokens, again)
			}
		}
	})
}

// Tags given at creation round-trip through ListTagsForResource and the
// tag operations on the identity ARN (provider, ses.md 5.4, 9.3).
func TestEmailIdentityTags(t *testing.T) {
	run(t, func(h *harness) {
		h.createIdentity("example.org", sesv2api.Tag{Key: "env", Value: "dev"})
		arn := h.identityARN("example.org")
		list := func() []sesv2api.Tag {
			return must(h, sesv2api.OpListTagsForResource, &sesv2api.ListTagsForResourceInput{ResourceArn: arn}).Tags
		}
		if diff := cmp.Diff([]sesv2api.Tag{{Key: "env", Value: "dev"}}, list(), cmp.Comparer(tagEqual)); diff != "" {
			h.Errorf("tags after create (-want +got):\n%s", diff)
		}
		must(h, sesv2api.OpTagResource, &sesv2api.TagResourceInput{ResourceArn: arn, Tags: []sesv2api.Tag{{Key: "team", Value: "mail"}}})
		must(h, sesv2api.OpUntagResource, &sesv2api.UntagResourceInput{ResourceArn: arn, TagKeys: []string{"env"}})
		if diff := cmp.Diff([]sesv2api.Tag{{Key: "team", Value: "mail"}}, list(), cmp.Comparer(tagEqual)); diff != "" {
			h.Errorf("tags after tag and untag (-want +got):\n%s", diff)
		}
	})
}

func tagEqual(a, b sesv2api.Tag) bool { return a.Key == b.Key && a.Value == b.Value }

// GetEmailIdentity and DeleteEmailIdentity of a missing identity answer
// NotFoundException, which Terraform reads as gone (provider, ses.md 5.4,
// 9.3; 404 per the model, ses.md 3.3); the texts are recorded (SES-11) and
// read from the constants.
func TestMissingEmailIdentityIsNotFound(t *testing.T) {
	run(t, func(h *harness) {
		h.createIdentity("person@example.com")
		must(h, sesv2api.OpDeleteEmailIdentity, &sesv2api.DeleteEmailIdentityInput{EmailIdentity: "person@example.com"})
		_, err := call(h, sesv2api.OpGetEmailIdentity, &sesv2api.GetEmailIdentityInput{EmailIdentity: "person@example.com"})
		wantErr(h, err, sesv2api.IdentityNotFoundException(), fmt.Sprintf(ses.GetIdentityMissingText, "person@example.com"))
		_, err = call(h, sesv2api.OpDeleteEmailIdentity, &sesv2api.DeleteEmailIdentityInput{EmailIdentity: "person@example.com"})
		wantErr(h, err, sesv2api.IdentityNotFoundException(), fmt.Sprintf(ses.IdentityMissingText, "person@example.com"))
	})
}

// ListEmailIdentities lists verified and unverified identities with the
// IdentityInfo members, narrowed by Filter (model, ses.md 5.4). The order
// is UNVERIFIED (SES-14), so the names are compared sorted. PageSize is at
// most 1000 (model); the answer past it is inferred and read from the
// constants.
func TestListEmailIdentities(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		h.createIdentity("pending@example.com")
		names := func(f map[sesv2api.IdentityFilterKey]string) []string {
			out := must(h, sesv2api.OpListEmailIdentities, &sesv2api.ListEmailIdentitiesInput{Filter: f})
			got := make([]string, 0, len(out.EmailIdentities))
			for _, info := range out.EmailIdentities {
				if info.IdentityName == nil {
					h.Fatalf("IdentityInfo without IdentityName: %+v", info)
				}
				got = append(got, *info.IdentityName)
				if *info.IdentityName == sender && (!info.SendingEnabled || info.VerificationStatus == nil || *info.VerificationStatus != sesv2api.VerificationStatusSuccess) {
					h.Errorf("verified %s listed as %+v", sender, info)
				}
			}
			slices.Sort(got)
			return got
		}
		if got := names(nil); !slices.Equal(got, []string{"pending@example.com", sender, verifiedDomain}) {
			h.Errorf("ListEmailIdentities = %q", got)
		}
		if got := names(map[sesv2api.IdentityFilterKey]string{sesv2api.IdentityFilterKeyIdentityType: string(sesv2api.IdentityTypeDomain)}); !slices.Equal(got, []string{verifiedDomain}) {
			h.Errorf("IDENTITY_TYPE DOMAIN = %q", got)
		}
		if got := names(map[sesv2api.IdentityFilterKey]string{sesv2api.IdentityFilterKeyVerificationStatus: string(sesv2api.VerificationStatusPending)}); !slices.Equal(got, []string{"pending@example.com"}) {
			h.Errorf("VERIFICATION_STATUS PENDING = %q", got)
		}
		if got := names(map[sesv2api.IdentityFilterKey]string{sesv2api.IdentityFilterKeyIdentityNameContains: "pending"}); !slices.Equal(got, []string{"pending@example.com"}) {
			h.Errorf("IDENTITY_NAME_CONTAINS pending = %q", got)
		}
		over := int32(ses.ListEmailIdentitiesMaxPageSize + 1)
		_, err := call(h, sesv2api.OpListEmailIdentities, &sesv2api.ListEmailIdentitiesInput{PageSize: &over})
		wantErr(h, err, sesv2api.IdentityBadRequestException(), fmt.Sprintf(ses.PageSizeText, ses.ListEmailIdentitiesMaxPageSize))
	})
}

// GetAccount reports the sandbox from ProductionAccessEnabled ("If the
// value is false, then your account is in the sandbox", model, ses.md 6.3)
// and the same SendQuota as v1 GetSendQuota (both models' three members,
// ses.md 6.3).
func TestGetAccountReportsSandboxAndQuota(t *testing.T) {
	for _, production := range []bool{false, true} {
		runVerified(t, ses.Options{ProductionAccess: ses.ProductionAccess{All: production}}, func(h *harness) {
			must(h, sesapi.OpSendEmail, email(sender, recipient))
			acct := must(h, sesv2api.OpGetAccount, &sesv2api.GetAccountInput{})
			q := h.quota()
			if acct.ProductionAccessEnabled != production || acct.SendQuota == nil ||
				acct.SendQuota.Max24HourSend != q.Max24HourSend || acct.SendQuota.MaxSendRate != q.MaxSendRate ||
				acct.SendQuota.SentLast24Hours != q.SentLast24Hours || q.SentLast24Hours != 1 {
				h.Errorf("production %v: GetAccount = %+v quota %+v, GetSendQuota = %+v", production, acct, acct.SendQuota, q)
			}
		})
	}
}

// Switch ses.store.shared-v1-v2 (inferred, SES-12): by default an identity
// made through one API is read through the other (ses.md 2.3); separate
// keeps them apart.
func TestStoreSharingFollowsItsSwitch(t *testing.T) {
	run(t, func(h *harness) {
		h.verifyEmail("v1@example.com")
		if got := h.getIdentity("v1@example.com"); got.VerificationStatus == nil || *got.VerificationStatus != sesv2api.VerificationStatusPending {
			h.Errorf("switch ses.store.shared-v1-v2=%s: v2 reads the v1 identity as %+v", ses.StoreShared, got)
		}
		h.createIdentity("v2@example.com")
		if got := h.status("v2@example.com"); got != sesapi.VerificationStatusPending {
			h.Errorf("switch ses.store.shared-v1-v2=%s: v1 reads the v2 identity as %q", ses.StoreShared, got)
		}
	})
	runWith(t, ses.Options{Store: ses.StoreSeparate}, func(h *harness) {
		h.verifyEmail("v1@example.com")
		if _, err := call(h, sesv2api.OpGetEmailIdentity, &sesv2api.GetEmailIdentityInput{EmailIdentity: "v1@example.com"}); err == nil {
			h.Errorf("switch ses.store.shared-v1-v2=%s: v2 reads the v1 identity", ses.StoreSeparate)
		}
		h.createIdentity("v2@example.com")
		if _, ok := h.attributes("v2@example.com")["v2@example.com"]; ok {
			h.Errorf("switch ses.store.shared-v1-v2=%s: v1 reads the v2 identity", ses.StoreSeparate)
		}
	})
}

// CreateEmailIdentity of an address starts its verification as
// VerifyEmailIdentity does: a verification email recorded under the v2 API.
func TestCreateEmailIdentityRecordsVerificationEmail(t *testing.T) {
	run(t, func(h *harness) {
		h.createIdentity("person@example.com")
		all := h.messages(ses.MessageQuery{})
		if len(all) != 1 || all[0].Kind != ses.KindVerification || all[0].API != ses.APIv2 || all[0].Operation != "CreateEmailIdentity" ||
			!slices.Equal(all[0].To, []string{"person@example.com"}) {
			h.Errorf("messages after CreateEmailIdentity = %+v, want one v2 verification email", all)
		}
	})
}

// Reset of the v2 view resets the shared store, so it clears what both
// APIs made.
func TestV2ResetClearsBothAPIs(t *testing.T) {
	run(t, func(h *harness) {
		h.verifyEmail("v1@example.com")
		h.createIdentity("v2@example.com")
		h.v2.Reset(kernel.ResetScope{})
		if got := h.attributes("v1@example.com", "v2@example.com"); len(got) != 0 {
			h.Errorf("after the v2 reset v1 still reads %v", got)
		}
		if got := h.messages(ses.MessageQuery{}); len(got) != 0 {
			h.Errorf("after the v2 reset %d messages remain", len(got))
		}
	})
}

// v2 SendEmail with Simple content.
func simpleEmail(from string, to ...string) *sesv2api.SendEmailInput {
	return &sesv2api.SendEmailInput{
		FromEmailAddress: &from,
		Destination:      &sesv2api.Destination{ToAddresses: to},
		Content: sesv2api.EmailContent{Simple: &sesv2api.Message{
			Subject: sesv2api.Content{Data: "subject"},
			Body:    sesv2api.Body{Text: &sesv2api.Content{Data: "text"}},
		}},
	}
}

// ConfigurationOverrides and EndpointId answer NotImplemented naming the
// member, as do the other unimplemented members (invariant 3).
func TestV2SendEmailNotImplementedMembers(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		arn := h.identityARN(sender)
		for member, set := range map[string]func(*sesv2api.SendEmailInput){
			"ConfigurationOverrides": func(in *sesv2api.SendEmailInput) { in.ConfigurationOverrides = &sesv2api.ConfigurationOverrides{} },
			"EndpointId":             func(in *sesv2api.SendEmailInput) { in.EndpointId = new("abc123.xyz") },
			"TenantName":             func(in *sesv2api.SendEmailInput) { in.TenantName = new("tenant") },
			"ListManagementOptions": func(in *sesv2api.SendEmailInput) {
				in.ListManagementOptions = &sesv2api.ListManagementOptions{ContactListName: "list"}
			},
			"FromEmailAddressIdentityArn":               func(in *sesv2api.SendEmailInput) { in.FromEmailAddressIdentityArn = &arn },
			"FeedbackForwardingEmailAddressIdentityArn": func(in *sesv2api.SendEmailInput) { in.FeedbackForwardingEmailAddressIdentityArn = &arn },
		} {
			in := simpleEmail(sender, simulator)
			set(in)
			_, err := call(h, sesv2api.OpSendEmail, in)
			wantNotImplemented(h, err, member)
		}
	})
}

// FromEmailAddress "has to be verified" (model, ses.md 4.4); the text is
// ses.error.unverified.text's.
func TestV2FromAddressMustBeVerified(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		_, err := call(h, sesv2api.OpSendEmail, simpleEmail("stranger@unverified.test", simulator))
		wantErr(h, err, sesv2api.IdentityMessageRejected(), unverifiedText(h.Scope.Region, "stranger@unverified.test"))
	})
}

// FeedbackForwardingEmailAddress is a sender, checked as ReturnPath
// (inferred) with the text of ses.error.unverified.text, and recorded as
// returnPath.
func TestV2FeedbackForwardingAddressIsSenderChecked(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		in := simpleEmail(sender, simulator)
		in.FeedbackForwardingEmailAddress = new("feedback@unverified.test")
		_, err := call(h, sesv2api.OpSendEmail, in)
		wantErr(h, err, sesv2api.IdentityMessageRejected(), unverifiedText(h.Scope.Region, "feedback@unverified.test"))
		in.FeedbackForwardingEmailAddress = new("feedback@" + verifiedDomain)
		must(h, sesv2api.OpSendEmail, in)
		if sent := h.sends(); len(sent) != 1 || sent[0].ReturnPath != "feedback@"+verifiedDomain {
			h.Errorf("recorded %+v, want returnPath feedback@%s", sent, verifiedDomain)
		}
	})
}

// Content must hold exactly one of Simple, Raw and Template, and Simple
// needs FromEmailAddress; both answers are UNVERIFIED (SES-23, ses.md 4.4)
// and read from the overlay's validation identity and the constants.
func TestV2ContentShape(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		in := simpleEmail(sender, simulator)
		in.Content = sesv2api.EmailContent{}
		_, err := call(h, sesv2api.OpSendEmail, in)
		wantErr(h, err, sesv2api.IdentityBadRequestException(), ses.ContentShapeText)
		in = simpleEmail(sender, simulator)
		in.Content.Raw = &sesv2api.RawMessage{Data: mime("body\r\n", "From: "+sender, "To: "+simulator)}
		_, err = call(h, sesv2api.OpSendEmail, in)
		wantErr(h, err, sesv2api.IdentityBadRequestException(), ses.ContentShapeText)
		in = simpleEmail(sender, simulator)
		in.FromEmailAddress = nil
		_, err = call(h, sesv2api.OpSendEmail, in)
		wantErr(h, err, sesv2api.IdentityBadRequestException(), ses.MissingFromText)
	})
}

// An accepted v2 Simple send records its Headers and Attachments (model,
// ses.md 4.4), and its journaled input keeps no subject,
// body or attachment content while touching the sender's identity ARN.
func TestV2SimpleSendIsRecordedAndRedacted(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		in := simpleEmail(sender, simulator)
		in.Content.Simple.Subject.Data = journalSecret
		in.Content.Simple.Body.Html = &sesv2api.Content{Data: journalSecret}
		in.Content.Simple.Headers = []sesv2api.MessageHeader{{Name: "X-Custom", Value: "one"}}
		in.Content.Simple.Attachments = []sesv2api.Attachment{{FileName: "report.txt", ContentType: new("text/plain"), RawContent: []byte(journalSecret)}}
		in.EmailTags = []sesv2api.MessageTag{{Name: "campaign", Value: "spring"}}
		id := must(h, sesv2api.OpSendEmail, in).MessageId
		sent := h.sends()
		if len(sent) != 1 {
			h.Fatalf("%d sends recorded, want 1", len(sent))
		}
		m := sent[0]
		if id == nil || m.MessageID != *id || m.API != ses.APIv2 || m.Operation != "SendEmail" || !messageIDPattern.MatchString(m.MessageID) {
			h.Errorf("record %s %s %s, want SendEmail %v under sesv2", m.MessageID, m.API, m.Operation, id)
		}
		if !slices.Equal(m.Headers, []ses.Header{{Name: "X-Custom", Value: "one"}}) {
			h.Errorf("headers = %+v", m.Headers)
		}
		if len(m.Attachments) != 1 || m.Attachments[0].FileName != "report.txt" || m.Attachments[0].ContentType != "text/plain" || string(m.Attachments[0].Content) != journalSecret {
			h.Errorf("attachments = %+v", m.Attachments)
		}
		if !slices.Equal(m.Tags, []ses.Tag{{Name: "campaign", Value: "spring"}}) {
			h.Errorf("tags = %+v", m.Tags)
		}
		calls := h.apiCalls("sesv2", "SendEmail")
		if len(calls) != 1 {
			h.Fatalf("%d v2 SendEmail calls journaled, want 1", len(calls))
		}
		wantTouches(h, calls[0].Resources, h.identityARN(sender))
		wantNoSecret(h, calls[0].Input.JSON())
	})
}

// v2 Raw content follows ses.raw.envelope (inferred, SES-20): Destination
// when given, else the headers; union both.
func TestV2RawEnvelopeFollowsItsSwitch(t *testing.T) {
	const stranger = "header@unverified.test"
	raw := func(dest *sesv2api.Destination) *sesv2api.SendEmailInput {
		return &sesv2api.SendEmailInput{
			Destination: dest,
			Content:     sesv2api.EmailContent{Raw: &sesv2api.RawMessage{Data: mime("body\r\n", "From: "+sender, "To: "+stranger, "Subject: s")}},
		}
	}
	runVerified(t, ses.Options{}, func(h *harness) {
		must(h, sesv2api.OpSendEmail, raw(&sesv2api.Destination{ToAddresses: []string{simulator}}))
		_, err := call(h, sesv2api.OpSendEmail, raw(nil))
		wantErr(h, err, sesv2api.IdentityMessageRejected(), unverifiedText(h.Scope.Region, stranger))
	})
	runVerified(t, ses.Options{RawEnvelope: ses.RawEnvelopeUnion}, func(h *harness) {
		_, err := call(h, sesv2api.OpSendEmail, raw(&sesv2api.Destination{ToAddresses: []string{simulator}}))
		wantErr(h, err, sesv2api.IdentityMessageRejected(), unverifiedText(h.Scope.Region, stranger))
	})
}

// The daily quota on v2 answers TooManyRequestsException (the overlay's
// v2 throttle identity, inferred) with DailyQuotaText.
func TestV2DailyQuota(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		for batch := range int(ses.SandboxMax24HourSend) / ses.MaxRecipients {
			to := make([]string, ses.MaxRecipients)
			for i := range to {
				to[i] = fmt.Sprintf("u%d-%d@%s", batch, i, verifiedDomain)
			}
			must(h, sesv2api.OpSendEmail, simpleEmail(sender, to...))
		}
		_, err := call(h, sesv2api.OpSendEmail, simpleEmail(sender, recipient))
		wantErr(h, err, sesv2api.IdentityTooManyRequestsException(), ses.DailyQuotaText)
	})
}
