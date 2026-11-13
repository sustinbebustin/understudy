package ses_test

import (
	"context"
	"maps"
	"slices"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// refusals is an Authorizer that allows every call and keeps, per
// operation in call order, the condition keys it was supplied and asked to
// refuse.
type refusals struct {
	mu  sync.Mutex
	got map[string][]kernel.AuthzQuery
}

func (r *refusals) Authorize(_ context.Context, c *kernel.Call, q kernel.AuthzQuery) kernel.Verdict {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := c.Op().ID.String()
	r.got[id] = append(r.got[id], q)
	return kernel.Allowed{}
}

// Every SES binding supplies ses:ApiVersion, "1" on v1 and "2" on v2, and
// refuses each address key it does not supply, so iam.enforce enforce
// never takes one as absent (iam-enforcement-surfaces.md 12.5): v1
// SendEmail and SendTemplatedEmail supply ses:FromAddress and
// ses:Recipients and refuse ses:FromDisplayName and ses:FeedbackAddress;
// every other binding refuses all four, and v2 SendEmail supplies the two
// for Simple content and not for Raw. A call of each kind, v1 and v2,
// shows it.
func TestEveryBindingRefusesTheSESConditionKeys(t *testing.T) {
	r := &refusals{got: map[string][]kernel.AuthzQuery{}}
	raw := mime("b\r\n", "From: "+sender, "To: "+recipient, "Subject: s")
	runVerified(t, ses.Options{}, func(h *harness) {
		named := email("Sender <"+sender+">", recipient)
		named.Destination.CcAddresses = []string{simulator}
		named.ReplyToAddresses = []string{"reply@verified.test"}
		must(h, sesapi.OpSendEmail, named)
		must(h, sesapi.OpSendRawEmail, rawEmail(raw))
		must(h, sesapi.OpGetSendQuota, &sesapi.GetSendQuotaInput{})
		must(h, sesapi.OpListIdentities, &sesapi.ListIdentitiesInput{})
		must(h, sesapi.OpCreateTemplate, &sesapi.CreateTemplateInput{Template: sesapi.Template{TemplateName: "t", TextPart: new("t")}})
		must(h, sesapi.OpListTemplates, &sesapi.ListTemplatesInput{})
		must(h, sesapi.OpSendTemplatedEmail, &sesapi.SendTemplatedEmailInput{
			Source: sender, Destination: sesapi.Destination{BccAddresses: []string{recipient}}, Template: "t", TemplateData: "{}",
		})
		must(h, sesv2api.OpGetAccount, &sesv2api.GetAccountInput{})
		must(h, sesv2api.OpCreateEmailIdentity, &sesv2api.CreateEmailIdentityInput{EmailIdentity: "example.org"})
		must(h, sesv2api.OpPutEmailIdentityConfigurationSetAttributes,
			&sesv2api.PutEmailIdentityConfigurationSetAttributesInput{EmailIdentity: "example.org"})
		must(h, sesv2api.OpPutEmailIdentityDkimSigningAttributes, &sesv2api.PutEmailIdentityDkimSigningAttributesInput{
			EmailIdentity: "example.org", SigningAttributesOrigin: sesv2api.DkimSigningAttributesOriginAwsSes,
		})
		must(h, sesv2api.OpSendEmail, simpleEmail(sender, recipient))
		must(h, sesv2api.OpSendEmail, &sesv2api.SendEmailInput{Content: sesv2api.EmailContent{Raw: &sesv2api.RawMessage{Data: raw}}})

		all := []string{"ses:FromAddress", "ses:Recipients", "ses:FromDisplayName", "ses:FeedbackAddress"}
		unsettled := []string{"ses:FromDisplayName", "ses:FeedbackAddress"}
		v1 := kernel.ConditionKeys{"ses:ApiVersion": {"1"}}
		v2 := kernel.ConditionKeys{"ses:ApiVersion": {"2"}}
		type asked struct {
			refused []string
			keys    kernel.ConditionKeys
		}
		want := map[string][]asked{
			"ses.VerifyEmailIdentity":  {{all, v1}},
			"ses.VerifyDomainIdentity": {{all, v1}},
			"ses.SendEmail": {{unsettled, kernel.ConditionKeys{
				"ses:ApiVersion": {"1"}, "ses:FromAddress": {sender}, "ses:Recipients": {recipient, simulator},
			}}},
			"ses.SendRawEmail":   {{all, v1}},
			"ses.GetSendQuota":   {{all, v1}},
			"ses.ListIdentities": {{all, v1}},
			"ses.CreateTemplate": {{all, v1}},
			"ses.ListTemplates":  {{all, v1}},
			"ses.SendTemplatedEmail": {{unsettled, kernel.ConditionKeys{
				"ses:ApiVersion": {"1"}, "ses:FromAddress": {sender}, "ses:Recipients": {recipient},
			}}},
			"sesv2.GetAccount":                                 {{all, v2}},
			"sesv2.CreateEmailIdentity":                        {{all, v2}},
			"sesv2.PutEmailIdentityConfigurationSetAttributes": {{all, v2}},
			"sesv2.PutEmailIdentityDkimSigningAttributes":      {{all, v2}},
			"sesv2.SendEmail": {
				{all, kernel.ConditionKeys{"ses:ApiVersion": {"2"}, "ses:FromAddress": {sender}, "ses:Recipients": {recipient}}},
				{all, v2},
			},
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		for op, calls := range want {
			got := r.got[op]
			if len(got) != len(calls) {
				h.Errorf("%s was asked %d times, want %d", op, len(got), len(calls))
				continue
			}
			for i, w := range calls {
				if !slices.Equal(got[i].Refused, w.refused) || !maps.EqualFunc(got[i].Keys, w.keys, slices.Equal) {
					h.Errorf("%s call %d refused %q with keys %v, want %q with %v", op, i+1, got[i].Refused, got[i].Keys, w.refused, w.keys)
				}
			}
		}
	}, kerneltest.Authorizer(r))
}

// Switches lists the two condition-key derivations
// iam-enforcement-surfaces.md 12.5 marks inferred, at the values the
// supplied keys follow:
// ses.condition.api-version-v1 "1", governing every authorized v1
// operation, and ses.condition.address-form, governing the v1 Simple and
// templated sends. TestEveryBindingRefusesTheSESConditionKeys and the
// enforce tests in internal/server fail when either value flips.
func TestSwitchesListTheConditionKeyDerivations(t *testing.T) {
	const addressForm = "a Name <address> parameter carries the address between the angle brackets, case, +label and quotes as written; " +
		"ses:Recipients are To, Cc and Bcc, not ReplyTo"
	if ses.APIVersionV1 != "1" || ses.AddressFormValue != addressForm {
		t.Errorf("APIVersionV1 = %q, AddressFormValue = %q; want %q and %q", ses.APIVersionV1, ses.AddressFormValue, "1", addressForm)
	}
	run(t, func(h *harness) {
		entries := h.svc.Switches()
		for name, want := range map[string]struct {
			value string
			ops   []string
		}{
			"ses.condition.api-version-v1": {"1", nil},
			"ses.condition.address-form":   {addressForm, []string{"ses.SendEmail", "ses.SendTemplatedEmail"}},
		} {
			i := slices.IndexFunc(entries, func(e fidelity.Entry) bool { return e.Name == name })
			if i < 0 {
				h.Errorf("Switches omits %s", name)
				continue
			}
			e := entries[i]
			if e.Value != want.value || e.Label != evidence.Inferred || e.Record != "SES-31" || e.Ref != "docs/research/iam-enforcement-surfaces.md#125-context-keys" {
				h.Errorf("%s = %+v", name, e)
			}
			if want.ops != nil && !slices.Equal(e.Ops, want.ops) {
				h.Errorf("%s governs %q, want %q", name, e.Ops, want.ops)
			}
		}
		i := slices.IndexFunc(entries, func(e fidelity.Entry) bool { return e.Name == "ses.condition.api-version-v1" })
		if ops := entries[i].Ops; !slices.Contains(ops, "ses.ListIdentities") || !slices.Contains(ops, "ses.SendRawEmail") {
			h.Errorf("ses.condition.api-version-v1 governs %q, want every authorized v1 operation", ops)
		}
	})
}

// Switches lists ses.denial.identity at DenialIdentityValue, inferred,
// with the iam-enforcement-surfaces.md 12.3 anchor and SES-28, governing
// the authorized v1 operations (iam-enforcement-surfaces.md 12.8). The
// value is what SES-28 and IAM-ENF-38/39 recorded: a send's own text, the
// IAM grammar for every other action, and v2's body key "Message".
func TestSwitchesListTheDenialIdentity(t *testing.T) {
	const recorded = `v1 AccessDenied, Type Sender, 403; v2 AccessDeniedException in x-amzn-ErrorType, 403, body {"Message": "<text>"}; ` +
		`a send's text "User '<principal arn>' is not authorized to perform '<action>' on resource '<identity arn>'", every other action's the IAM grammar`
	if ses.DenialIdentityValue != recorded {
		t.Errorf("DenialIdentityValue =\n%s\nwant the recorded\n%s", ses.DenialIdentityValue, recorded)
	}
	run(t, func(h *harness) {
		entries := h.svc.Switches()
		i := slices.IndexFunc(entries, func(e fidelity.Entry) bool { return e.Name == "ses.denial.identity" })
		if i < 0 {
			h.Fatal("Switches omits ses.denial.identity")
		}
		e := entries[i]
		if e.Value != ses.DenialIdentityValue || e.Label != evidence.Inferred || e.Record != "SES-28" ||
			e.Ref != "docs/research/iam-enforcement-surfaces.md#123-error-identities-to-emit" {
			h.Errorf("ses.denial.identity = %+v", e)
		}
		if !slices.Contains(e.Ops, "ses.SendEmail") || !slices.Contains(e.Ops, "ses.CreateTemplate") || slices.Contains(e.Ops, "ses.VerificationLink") {
			h.Errorf("ses.denial.identity governs %q, want the authorized v1 operations and not the verification link", e.Ops)
		}
	})
}
