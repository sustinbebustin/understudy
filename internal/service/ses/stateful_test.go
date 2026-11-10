package ses_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// Export, Reset, Import gives back the identities, the templates, the
// messages and the recipient counter; Resources lists
// identities then templates, each in creation order, with the v1 status
// or "template" as summary.
func TestSnapshotRoundTrip(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		h.verifyEmail("pending@example.com")
		token := h.verifyDomain("example.org")
		must(h, sesapi.OpCreateTemplate, &sesapi.CreateTemplateInput{Template: sesapi.Template{TemplateName: "welcome", SubjectPart: new("Hi"), TextPart: new("t")}})
		id := must(h, sesapi.OpSendEmail, email(sender, recipient)).MessageId

		sections, err := h.svc.Export()
		if err != nil || len(sections) == 0 {
			h.Fatalf("Export = %d sections, %v", len(sections), err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if got := h.attributes(sender, "pending@example.com"); len(got) != 0 {
			h.Fatalf("after Reset the identities remain: %v", got)
		}
		if got := h.quota().SentLast24Hours; got != 0 {
			h.Errorf("SentLast24Hours = %v after Reset, want 0", got)
		}
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		if got := h.status(sender); got != sesapi.VerificationStatusSuccess {
			h.Errorf("%s = %q after the load, want Success", sender, got)
		}
		if got := h.status("pending@example.com"); got != sesapi.VerificationStatusPending {
			h.Errorf("pending@example.com = %q after the load, want Pending", got)
		}
		if a := h.attributes("example.org")["example.org"]; a.VerificationToken == nil || *a.VerificationToken != token {
			h.Errorf("domain token after the load = %v, want %q", a.VerificationToken, token)
		}
		if got := messageIDs(h.sends()); !slices.Equal(got, []string{id}) {
			h.Errorf("sends after the load = %q, want [%s]", got, id)
		}
		if got := h.quota().SentLast24Hours; got != 1 {
			h.Errorf("SentLast24Hours = %v after the load, want 1", got)
		}
		tmpl := must(h, sesapi.OpGetTemplate, &sesapi.GetTemplateInput{TemplateName: "welcome"}).Template
		if tmpl == nil || tmpl.SubjectPart == nil || *tmpl.SubjectPart != "Hi" {
			h.Errorf("template after the load = %+v", tmpl)
		}

		resources := h.svc.Resources(h.Scope)
		got := make([]string, 0, len(resources))
		for _, r := range resources {
			got = append(got, r.ARN.String()+" "+r.Summary)
		}
		want := []string{
			h.identityARN(sender) + " Success",
			h.identityARN(verifiedDomain) + " Success",
			h.identityARN("pending@example.com") + " Pending",
			h.identityARN("example.org") + " Pending",
			"arn:aws:ses:us-east-1:000000000000:template/welcome template",
		}
		if !slices.Equal(got, want) {
			h.Errorf("Resources = %q\nwant %q", got, want)
		}
		if r := h.v2.Resources(h.Scope); r != nil {
			h.Errorf("the v2 view lists %v, want nothing", r)
		}
		if s, err := h.v2.Export(); s != nil || err != nil {
			h.Errorf("the v2 view exports %v, %v; want nothing", s, err)
		}
	})
}

// Import refuses a section of another schema version.
func TestImportRefusesAnotherVersion(t *testing.T) {
	run(t, func(h *harness) {
		h.verifyEmail(sender)
		sections, err := h.svc.Export()
		if err != nil || len(sections) == 0 {
			h.Fatalf("Export = %d sections, %v", len(sections), err)
		}
		bumped := slices.Clone(sections)
		for i := range bumped {
			bumped[i].Version = h.svc.SchemaVersion() + 1
		}
		if err := h.svc.Import(bumped); err == nil {
			h.Error("Import accepted a section of a newer version")
		}
	})
}

// A loaded snapshot re-arms expiry: an identity Pending at save time turns
// Failed at its original deadline (ses.md 5.2, the deadline
// read from EmailVerificationExpiry).
func TestRearmRestoresExpiry(t *testing.T) {
	run(t, func(h *harness) {
		const addr = "pending@example.com"
		h.verifyEmail(addr)
		half := ses.EmailVerificationExpiry / 2
		h.advance(half)
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		h.svc.Rearm(h.Clock.Now())
		h.advance(ses.EmailVerificationExpiry - half - 1)
		if got := h.status(addr); got != sesapi.VerificationStatusPending {
			h.Fatalf("before the original deadline %s = %q, want Pending", addr, got)
		}
		h.advance(1)
		if got := h.status(addr); got != sesapi.VerificationStatusFailed {
			h.Errorf("at the original deadline %s = %q, want Failed", addr, got)
		}
	})
}

// A reset scoped to one Region leaves the others (kernel.ResetScope), and
// Scopes lists the scopes that hold state through both views.
func TestResetIsScoped(t *testing.T) {
	run(t, func(h *harness) {
		h.verifyEmail(sender)
		east := h.Scope
		h.Scope.Region = westRegion(h)
		h.verifyEmail(sender)
		if got := h.v2.Scopes(); len(got) != 2 || !slices.Equal(got, h.svc.Scopes()) {
			h.Errorf("Scopes: v1 %v, v2 %v; want both Regions in each", h.svc.Scopes(), got)
		}
		h.svc.Reset(kernel.ResetScope{Regions: []aws.Region{h.Scope.Region}})
		if got := h.attributes(sender); len(got) != 0 {
			h.Errorf("us-west-2 still holds %v after its reset", got)
		}
		h.Scope = east
		if got := h.status(sender); got != sesapi.VerificationStatusPending {
			h.Errorf("us-east-1 %s = %q after the us-west-2 reset, want Pending", sender, got)
		}
	})
}

// Switches lists the five Options switches with their Ref and Record,
// each Value the String of the value in effect, so flipping one
// through Options changes the entry. The two whose default SES-12 and
// SES-08 recorded are labelled aws-recorded, the rest inferred.
func TestSwitchesListTheOptionsSwitches(t *testing.T) {
	type row struct {
		ref, record, def, flipped string
		label                     evidence.Label
	}
	want := map[string]row{
		"ses.store.shared-v1-v2":            {"docs/research/ses.md#23-decision", "SES-12", ses.StoreShared.String(), ses.StoreSeparate.String(), evidence.AWSRecorded},
		"ses.message-id.format":             {"docs/research/ses.md#47-messageid", "SES-07", ses.MessageIDTimestamped.String(), ses.MessageIDRandom.String(), evidence.Inferred},
		"ses.error.unverified.text":         {"docs/research/ses.md#61-sandbox-rules-per-send", "SES-01, SES-03", ses.UnverifiedHashOrder.String(), ses.UnverifiedRecipientsFirst.String(), evidence.Inferred},
		"ses.verification.unknown-identity": {"docs/research/ses.md#53-v1-identity-operations", "SES-08", ses.UnknownIdentityOmitted.String(), ses.UnknownIdentityNotStarted.String(), evidence.AWSRecorded},
		"ses.raw.envelope":                  {"docs/research/ses.md#42-v1-sendrawemail", "SES-20", ses.RawEnvelopeDestinations.String(), ses.RawEnvelopeUnion.String(), evidence.Inferred},
	}
	check := func(h *harness, flipped bool) {
		entries := h.svc.Switches()
		for name, w := range want {
			i := slices.IndexFunc(entries, func(e fidelity.Entry) bool { return e.Name == name })
			if i < 0 {
				h.Errorf("Switches omits %s", name)
				continue
			}
			e := entries[i]
			value := w.def
			if flipped {
				value = w.flipped
			}
			if e.Value != value || e.Label != w.label || e.Ref != w.ref || e.Record != w.record {
				h.Errorf("%s = %+v, want value %q, %s, Ref %s, Record %s", name, e, value, w.label, w.ref, w.record)
			}
		}
	}
	run(t, func(h *harness) { check(h, false) })
	runWith(t, ses.Options{
		Store: ses.StoreSeparate, MessageID: ses.MessageIDRandom, UnverifiedText: ses.UnverifiedRecipientsFirst,
		UnknownIdentity: ses.UnknownIdentityNotStarted, RawEnvelope: ses.RawEnvelopeUnion,
	}, func(h *harness) { check(h, true) })
}

// Every bound operation whose generated list holds more than one ses
// action decides which of them its calls use (iam-enforcement-surfaces.md
// 10.2).
func TestEveryMultiActionOperationDeclaresItsActions(t *testing.T) {
	run(t, func(h *harness) {
		for _, svc := range []*registry.Service{sesapi.Service(), sesv2api.Service()} {
			if got := kerneltest.UndeclaredActions(h.Kernel, svc); len(got) != 0 {
				h.Errorf("%s operations without an actions decision: %v", svc.ID, got)
			}
		}
	})
}

// Identity operations touch the identity ARN, so the
// journal names the identity a call acted on.
func TestIdentityOperationsTouchTheIdentityARN(t *testing.T) {
	run(t, func(h *harness) {
		h.verifyEmail(sender)
		h.createIdentity("example.org")
		must(h, sesv2api.OpGetEmailIdentity, &sesv2api.GetEmailIdentityInput{EmailIdentity: "example.org"})
		must(h, sesapi.OpDeleteIdentity, &sesapi.DeleteIdentityInput{Identity: sender})
		for _, c := range []struct{ svc, op, identity string }{
			{"ses", "VerifyEmailIdentity", sender},
			{"sesv2", "CreateEmailIdentity", "example.org"},
			{"sesv2", "GetEmailIdentity", "example.org"},
			{"ses", "DeleteIdentity", sender},
		} {
			calls := h.apiCalls(c.svc, c.op)
			if len(calls) != 1 {
				h.Errorf("%s.%s journaled %d times, want 1", c.svc, c.op, len(calls))
				continue
			}
			wantTouches(h, calls[0].Resources, h.identityARN(c.identity))
		}
	})
}

// Operations outside the slice stay unbound and answer 501 (invariant 3),
// including the first calls the LocalStack refusal checks
// make: DeleteConfigurationSet (test_ses.py L836),
// CreateConfigurationSetEventDestination (L817), CreateConfigurationSet
// (L545) and CreateReceiptRuleSet (L350), localstack@8b9a79f
// tests/aws/services/ses/test_ses.py.
func TestOutOfScopeOperationsAnswerNotImplemented(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, sesapi.OpDeleteConfigurationSet, &sesapi.DeleteConfigurationSetInput{ConfigurationSetName: "missing"})
		wantCode(h, err, awserr.NotImplemented())
		_, err = call(h, sesapi.OpCreateConfigurationSetEventDestination, &sesapi.CreateConfigurationSetEventDestinationInput{
			ConfigurationSetName: "set",
			EventDestination:     sesapi.EventDestination{Name: "dest", MatchingEventTypes: []sesapi.EventType{sesapi.EventTypeSend}},
		})
		wantCode(h, err, awserr.NotImplemented())
		_, err = call(h, sesapi.OpCreateConfigurationSet, &sesapi.CreateConfigurationSetInput{ConfigurationSet: sesapi.ConfigurationSet{Name: "set"}})
		wantCode(h, err, awserr.NotImplemented())
		_, err = call(h, sesapi.OpCreateReceiptRuleSet, &sesapi.CreateReceiptRuleSetInput{RuleSetName: "rules"})
		wantCode(h, err, awserr.NotImplemented())
		_, err = call(h, sesv2api.OpListConfigurationSets, &sesv2api.ListConfigurationSetsInput{})
		wantCode(h, err, awserr.NotImplemented())
	})
}
