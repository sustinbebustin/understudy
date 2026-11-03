package ses_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
)

// The answers the 2026-11-03 recording session settled (testdata/parity/ses,
// ses.md 12), asserted as recorded text rather than read from the
// package's constants.

// SES-04: an invalid address to VerifyEmailIdentity is "Invalid email
// address<...>." with no space; an empty SendEmail Source is "Invalid email
// address ." (both InvalidParameterValue, ses.md 4.6).
func TestRecordedInvalidAddressTexts(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, sesapi.OpVerifyEmailIdentity, &sesapi.VerifyEmailIdentityInput{EmailAddress: "not-an-email"})
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), "Invalid email address<not-an-email>.")
		_, err = call(h, sesapi.OpSendEmail, email("", simulator))
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), "Invalid email address .")
	})
}

// SES-08 step 02: 101 identities (ses.md 5.3).
func TestRecordedVerificationAttributesLimitText(t *testing.T) {
	run(t, func(h *harness) {
		ids := make([]string, 101)
		for i := range ids {
			ids[i] = fmt.Sprintf("us1103-x%d@example.com", i+1)
		}
		_, err := call(h, sesapi.OpGetIdentityVerificationAttributes, &sesapi.GetIdentityVerificationAttributesInput{Identities: ids})
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), "Can only get verification attributes for up to 100 identities at a time.")
	})
}

// SES-11: the v2 texts for a missing and a duplicate identity; Get puts the
// name in angle brackets, Delete does not, and "exist" is AWS's (ses.md
// 5.4).
func TestRecordedV2IdentityTexts(t *testing.T) {
	run(t, func(h *harness) {
		const never = "us1103-never@example.com"
		_, err := call(h, sesv2api.OpGetEmailIdentity, &sesv2api.GetEmailIdentityInput{EmailIdentity: never})
		wantErr(h, err, sesv2api.IdentityNotFoundException(), "Email identity <us1103-never@example.com> does not exist.")
		_, err = call(h, sesv2api.OpDeleteEmailIdentity, &sesv2api.DeleteEmailIdentityInput{EmailIdentity: never})
		wantErr(h, err, sesv2api.IdentityNotFoundException(), "Email identity us1103-never@example.com does not exist.")
		h.createIdentity("us1103-dup@example.com")
		_, err = call(h, sesv2api.OpCreateEmailIdentity, &sesv2api.CreateEmailIdentityInput{EmailIdentity: "us1103-dup@example.com"})
		wantErr(h, err, sesv2api.IdentityAlreadyExistsException(), "Email identity us1103-dup@example.com already exist.")
	})
}

// SES-14: the first page of an unfiltered listing, v1 and v2, held
// us1103.example.org, then us1103.example.com, of the six identities the
// session had made in the order below: descending by name, not creation
// order. The IDENTITY_NAME_CONTAINS listing came back ascending by name
// (ses.md 5.3; both orders inferred from one page each).
func TestRecordedListingOrder(t *testing.T) {
	created := []string{
		"us1103-me@example.com", "us1103.example.com", "us1103-dup@example.com",
		"us1103-v1@example.com", "us1103-v2@example.com", "us1103.example.org",
	}
	run(t, func(h *harness) {
		for _, name := range created {
			h.createIdentity(name)
		}
		two := int32(2)
		if got := must(h, sesapi.OpListIdentities, &sesapi.ListIdentitiesInput{MaxItems: &two}).Identities; !slices.Equal(got, []string{"us1103.example.org", "us1103.example.com"}) {
			h.Errorf("ListIdentities MaxItems 2 = %q, want [us1103.example.org us1103.example.com]", got)
		}
		names := func(in *sesv2api.ListEmailIdentitiesInput) []string {
			infos := must(h, sesv2api.OpListEmailIdentities, in).EmailIdentities
			out := make([]string, 0, len(infos))
			for _, i := range infos {
				out = append(out, *i.IdentityName)
			}
			return out
		}
		if got := names(&sesv2api.ListEmailIdentitiesInput{PageSize: &two}); !slices.Equal(got, []string{"us1103.example.org", "us1103.example.com"}) {
			h.Errorf("ListEmailIdentities PageSize 2 = %q, want [us1103.example.org us1103.example.com]", got)
		}
		want := slices.Sorted(slices.Values(created))
		filter := map[sesv2api.IdentityFilterKey]string{sesv2api.IdentityFilterKeyIdentityNameContains: "us1103"}
		if got := names(&sesv2api.ListEmailIdentitiesInput{Filter: filter}); !slices.Equal(got, want) {
			h.Errorf("ListEmailIdentities IDENTITY_NAME_CONTAINS = %q, want ascending %q", got, want)
		}
	})
}

// SES-15 step 01: a sandbox account's GetAccount has automatic dedicated-IP
// warm-up on and the account suppression list on for bounces and
// complaints (ses.md 6.3).
func TestRecordedGetAccountDefaults(t *testing.T) {
	run(t, func(h *harness) {
		out := must(h, sesv2api.OpGetAccount, &sesv2api.GetAccountInput{})
		if !out.DedicatedIpAutoWarmupEnabled {
			h.Errorf("GetAccount DedicatedIpAutoWarmupEnabled = false, want true")
		}
		want := []sesv2api.SuppressionListReason{sesv2api.SuppressionListReasonBounce, sesv2api.SuppressionListReasonComplaint}
		if out.SuppressionAttributes == nil || !slices.Equal(out.SuppressionAttributes.SuppressedReasons, want) {
			h.Errorf("GetAccount SuppressionAttributes = %+v, want SuppressedReasons %q", out.SuppressionAttributes, want)
		}
	})
}

// SES-16: a tag name or value of 256 characters passes the tag check, v1
// and v2, so the unverified sender answers; 2,100 characters still answer
// the third-party-recorded "cannot exceed 255" text (ses.md 4.5).
func TestRecordedTagLengths(t *testing.T) {
	run(t, func(h *harness) {
		const nobody = "us1103-nobody@example.com"
		n256 := strings.Repeat("a", 256)
		for _, tag := range []sesapi.MessageTag{{Name: n256, Value: "v"}, {Name: "k", Value: n256}} {
			in := email(nobody, simulator)
			in.Tags = []sesapi.MessageTag{tag}
			_, err := call(h, sesapi.OpSendEmail, in)
			wantErr(h, err, sesapi.IdentityMessageRejected(), unverifiedText(h.Scope.Region, nobody))
		}
		v2 := simpleEmail(nobody, simulator)
		v2.EmailTags = []sesv2api.MessageTag{{Name: n256, Value: "v"}}
		_, err := call(h, sesv2api.OpSendEmail, v2)
		wantErr(h, err, sesv2api.IdentityMessageRejected(), unverifiedText(h.Scope.Region, nobody))
		in := email(nobody, simulator)
		in.Tags = []sesapi.MessageTag{{Name: strings.Repeat("a", 2100), Value: "v"}}
		_, err = call(h, sesapi.OpSendEmail, in)
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), "Tag name cannot exceed 255 characters.")
	})
}

// SES-18: a template with only a subject is refused before the name is
// found taken, v1 and v2; a missing v1 template answers without a
// TemplateName member (ses.md 7).
func TestRecordedTemplateTexts(t *testing.T) {
	run(t, func(h *harness) {
		h.createTemplate(sesapi.Template{TemplateName: "us1103-t3", SubjectPart: new("Hi {{name}}"), TextPart: new("Dear {{name}}")})
		_, err := call(h, sesapi.OpCreateTemplate, &sesapi.CreateTemplateInput{Template: sesapi.Template{TemplateName: "us1103-t3", SubjectPart: new("s")}})
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), "Template must specify at least one of the parts.")
		_, err = call(h, sesapi.OpGetTemplate, &sesapi.GetTemplateInput{TemplateName: "us1103-none"})
		wantErr(h, err, sesapi.IdentityTemplateDoesNotExistException(), "Template us1103-none does not exist.")
		if m, ok := err.WireMembers(); ok {
			h.Errorf("GetTemplate of a missing template carries members %+v, want none", m)
		}
		_, err = call(h, sesv2api.OpGetEmailTemplate, &sesv2api.GetEmailTemplateInput{TemplateName: "us1103-none"})
		wantErr(h, err, sesv2api.IdentityNotFoundException(), "Template us1103-none does not exist.")
		_, err = call(h, sesv2api.OpCreateEmailTemplate, &sesv2api.CreateEmailTemplateInput{
			TemplateName: "us1103-t3", TemplateContent: sesv2api.EmailTemplateContent{Subject: new("s")},
		})
		wantErr(h, err, sesv2api.IdentityBadRequestException(), "Template must specify at least one of the parts.")
	})
}

// SES-20 step 03: a raw message with neither Source nor a From header
// (ses.md 4.2).
func TestRecordedRawWithoutFrom(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, sesapi.OpSendRawEmail, rawEmail(mime("b\n", "To: success@simulator.amazonses.com", "Subject: r3")))
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), "Missing required header 'From'.")
	})
}

// SES-29: MaxItems outside 1 to 1000, and a template of 512,001 bytes
// (ses.md 11.5).
func TestRecordedLimitTexts(t *testing.T) {
	run(t, func(h *harness) {
		for _, n := range []int32{0, 1001} {
			_, err := call(h, sesapi.OpListIdentities, &sesapi.ListIdentitiesInput{MaxItems: &n})
			wantErr(h, err, sesapi.IdentityInvalidParameterValue(),
				fmt.Sprintf("Value %d for parameter MaxItems is invalid. MaxItems must be between 1 and 1000.", n))
		}
		_, err := call(h, sesapi.OpCreateTemplate, &sesapi.CreateTemplateInput{Template: sesapi.Template{
			TemplateName: "us1103-big", SubjectPart: new("s"), TextPart: new(strings.Repeat("a", 500*1024+1)),
		}})
		wantErr(h, err, sesapi.IdentityInvalidTemplateException(), "Template us1103-big exceeds maximum allowed size of 500 KB.")
	})
}
