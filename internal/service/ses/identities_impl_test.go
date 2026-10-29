package ses_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// The tracer bullet, as aws_ses_email_identity drives it (ses.md 9.1):
// VerifyEmailIdentity, a read keyed by the request string, a repeat that
// leaves a Pending identity Pending without a state change (inferred,
// SES-09), DeleteIdentity, then a read that omits the key; a second delete
// of the now unknown identity succeeds (inferred, SES-11).
func TestEmailIdentityLifecycle(t *testing.T) {
	run(t, func(h *harness) {
		h.verifyEmail(sender)
		h.verifyEmail(sender)
		if got := h.status(sender); got != sesapi.VerificationStatusPending {
			h.Fatalf("%s = %q after a repeated verify, want Pending (ses.md 5.2)", sender, got)
		}
		if got := h.stateChanges(h.identityARN(sender)); len(got) != 0 {
			h.Errorf("a repeated verify of a Pending identity journaled state changes %q, want none", got)
		}
		for range 2 {
			must(h, sesapi.OpDeleteIdentity, &sesapi.DeleteIdentityInput{Identity: sender})
			if got := h.attributes(sender); len(got) != 0 {
				h.Errorf("VerificationAttributes after DeleteIdentity = %v, want the key absent (ses.md 9.1)", got)
			}
		}
		for _, op := range []string{"VerifyEmailIdentity", "DeleteIdentity"} {
			for _, c := range h.apiCalls("ses", op) {
				wantTouches(h, c.Resources, h.identityARN(sender))
			}
		}
	})
}

// Identities are per Region (ses.md 5.1): one verified in us-west-2 is
// unknown in us-east-1.
func TestIdentitiesArePerRegion(t *testing.T) {
	run(t, func(h *harness) {
		east := h.Scope
		h.Scope.Region = westRegion(h)
		h.verifyEmail(sender)
		h.Scope = east
		if got := h.attributes(sender); len(got) != 0 {
			h.Errorf("us-east-1 answers %v for an identity made in us-west-2, want nothing", got)
		}
	})
}

// Export, Reset, Import gives back the identities with their status, and
// Resources lists them in creation order with the v1 status (contract 1.2,
// 1.3).
func TestIdentitySnapshotRoundTrip(t *testing.T) {
	run(t, func(h *harness) {
		h.verifyEmail("b@example.com")
		h.verifyEmail("a@example.com")
		sections, err := h.svc.Export()
		if err != nil || len(sections) != 1 {
			h.Fatalf("Export = %d sections, %v; want one for the scope", len(sections), err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if got := h.svc.Scopes(); len(got) != 0 {
			h.Fatalf("Scopes after Reset = %v", got)
		}
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		if got := h.status("a@example.com"); got != sesapi.VerificationStatusPending {
			h.Errorf("a@example.com = %q after the load, want Pending", got)
		}
		resources := h.svc.Resources(h.Scope)
		got := make([]string, 0, len(resources))
		for _, r := range resources {
			got = append(got, r.ARN.String()+" "+r.Summary)
		}
		want := []string{h.identityARN("b@example.com") + " Pending", h.identityARN("a@example.com") + " Pending"}
		if !slices.Equal(got, want) {
			h.Errorf("Resources = %q\nwant %q", got, want)
		}
	})
}
