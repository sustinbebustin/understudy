package ses_test

import (
	"context"
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
// operation, the condition keys it was asked to refuse.
type refusals struct {
	mu  sync.Mutex
	got map[string][]string
}

func (r *refusals) Authorize(_ context.Context, c *kernel.Call, q kernel.AuthzQuery) kernel.Verdict {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got[c.Op().ID.String()] = q.Refused
	return kernel.Allowed{}
}

// Every SES binding refuses the SES condition keys understudy does not
// supply, so iam.enforce enforce never takes one as absent (09g-ses spec,
// enforce matrix): a call of each kind, v1 and v2, carries them.
func TestEveryBindingRefusesTheSESConditionKeys(t *testing.T) {
	r := &refusals{got: map[string][]string{}}
	runVerified(t, ses.Options{}, func(h *harness) {
		must(h, sesapi.OpSendEmail, email(sender, recipient))
		must(h, sesapi.OpGetSendQuota, &sesapi.GetSendQuotaInput{})
		must(h, sesapi.OpListIdentities, &sesapi.ListIdentitiesInput{})
		must(h, sesapi.OpCreateTemplate, &sesapi.CreateTemplateInput{Template: sesapi.Template{TemplateName: "t"}})
		must(h, sesapi.OpListTemplates, &sesapi.ListTemplatesInput{})
		must(h, sesv2api.OpGetAccount, &sesv2api.GetAccountInput{})
		must(h, sesv2api.OpCreateEmailIdentity, &sesv2api.CreateEmailIdentityInput{EmailIdentity: "example.org"})
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, op := range []string{
			"ses.VerifyEmailIdentity", "ses.VerifyDomainIdentity", "ses.SendEmail", "ses.GetSendQuota", "ses.ListIdentities",
			"ses.CreateTemplate", "ses.ListTemplates", "sesv2.GetAccount", "sesv2.CreateEmailIdentity",
		} {
			if got, ok := r.got[op]; !ok || !slices.Equal(got, ses.ConditionKeys()) {
				h.Errorf("%s refused %q (asked %t), want %q", op, got, ok, ses.ConditionKeys())
			}
		}
	}, kerneltest.Authorizer(r))
}

// Switches lists ses.denial.identity at DenialIdentityValue, inferred,
// with the iam-enforcement-surfaces.md 12.3 anchor and SES-28, governing
// the authorized v1 operations (iam-enforcement-surfaces.md 12.8).
func TestSwitchesListTheDenialIdentity(t *testing.T) {
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
