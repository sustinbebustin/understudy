package ses_test

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// link runs the VerificationLink raw operation on a request of method for
// target, as the edge dispatches an unsigned link.
func (h *harness) link(method, target string) *httptest.ResponseRecorder {
	h.Helper()
	op, ok := sesapi.Service().Operation("VerificationLink")
	if !ok {
		h.Fatal("sesapi has no VerificationLink")
	}
	c := h.Kernel.NewCall(kernel.ExternalCall{
		RequestID: h.Kernel.IDs().RequestID(),
		Operation: op.ID,
		Scope:     h.Scope,
		Principal: identity.Anonymous{},
		Origin:    ref.External{Protocol: ref.Raw, Listener: ref.ListenerMain},
		Admitted:  h.Clock.Now(),
		Endpoint:  h.Endpoint,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(h.Context(), method, target, http.NoBody)
	if r, failed := h.Kernel.Dispatch(h.Context(), c, kernel.Ok{In: kernel.RawExchange{Request: req, Writer: rec}}).(kernel.Failure); failed {
		h.Fatalf("VerificationLink %s %s = %s %q", method, target, r.Err.ID.QueryCode, r.Err.Message)
	}
	return rec
}

// senderLink is the path and query of the link in the latest verification
// email to sender.
func (h *harness) senderLink() string {
	h.Helper()
	msgs := h.messages(ses.MessageQuery{To: sender})
	for i := range slices.Backward(msgs) {
		msg := &msgs[i]
		if msg.Kind != ses.KindVerification || msg.Text == nil {
			continue
		}
		_, rest, ok := strings.Cut(msg.Text.Data, h.Endpoint.String())
		if link, _, _ := strings.Cut(rest, "\n"); ok && strings.HasPrefix(link, ses.VerificationLinkPath+"?token=") {
			return link
		}
	}
	h.Fatalf("no verification email to %s carries a link", sender)
	return ""
}

// wantLinkAnswer fails unless rec is status with the link's headers.
func wantLinkAnswer(h *harness, rec *httptest.ResponseRecorder, status int) {
	h.Helper()
	if rec.Code != status || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" ||
		rec.Header().Get("Content-Security-Policy") != "sandbox" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		h.Errorf("link answer = %d %v %q, want %d text/plain, sandboxed, nosniff", rec.Code, rec.Header(), rec.Body, status)
	}
}

// Opening the link of a Pending identity verifies it with a StateChange
// and cancels its expiry; opening it again changes nothing.
func TestVerificationLinkVerifies(t *testing.T) {
	run(t, func(h *harness) {
		h.verifyEmail(sender)
		link := h.senderLink()
		rec := h.link(http.MethodGet, link)
		wantLinkAnswer(h, rec, http.StatusOK)
		if !strings.Contains(rec.Body.String(), sender) {
			h.Errorf("the answer %q does not name %s", rec.Body, sender)
		}
		h.advance(ses.EmailVerificationExpiry)
		if got := h.status(sender); got != sesapi.VerificationStatusSuccess {
			h.Errorf("%s = %q after its link and the expiry, want Success", sender, got)
		}
		wantLinkAnswer(h, h.link(http.MethodGet, link), http.StatusOK)
		if got := h.stateChanges(h.identityARN(sender)); !slices.Equal(got, []string{"Pending->Success"}) {
			h.Errorf("StateChanges = %q, want one Pending->Success", got)
		}
		// Repeating the verify of a verified identity sends no new email.
		h.verifyEmail(sender)
		if got := len(h.messages(ses.MessageQuery{To: sender})); got != 1 {
			h.Errorf("%d verification emails after verifying a verified identity, want 1", got)
		}
	})
}

// An expired link is 410; verifying again restarts Pending with the same
// link, which then verifies (inferred, SES-09).
func TestVerificationLinkAfterExpiry(t *testing.T) {
	run(t, func(h *harness) {
		h.verifyEmail(sender)
		link := h.senderLink()
		h.advance(ses.EmailVerificationExpiry)
		wantLinkAnswer(h, h.link(http.MethodGet, link), http.StatusGone)
		if got := h.status(sender); got != sesapi.VerificationStatusFailed {
			h.Fatalf("%s = %q after an expired link, want Failed", sender, got)
		}
		h.verifyEmail(sender)
		if again := h.senderLink(); again != link {
			h.Errorf("the restarted email links %s, want the same %s", again, link)
		}
		wantLinkAnswer(h, h.link(http.MethodGet, link), http.StatusOK)
		want := []string{"Pending->Failed", "Failed->Pending", "Pending->Success"}
		if got := h.stateChanges(h.identityARN(sender)); !slices.Equal(got, want) {
			h.Errorf("StateChanges = %q, want %q", got, want)
		}
	})
}

// A link naming no identity, one with no token, and one whose identity
// was deleted are 404; another method is 405 naming GET.
func TestVerificationLinkRefusals(t *testing.T) {
	run(t, func(h *harness) {
		h.verifyEmail(sender)
		link := h.senderLink()
		rec := h.link(http.MethodPost, link)
		wantLinkAnswer(h, rec, http.StatusMethodNotAllowed)
		if got := rec.Header().Get("Allow"); got != http.MethodGet {
			h.Errorf("Allow = %q, want GET", got)
		}
		wantLinkAnswer(h, h.link(http.MethodGet, ses.VerificationLinkPath+"?token=nope"), http.StatusNotFound)
		wantLinkAnswer(h, h.link(http.MethodGet, ses.VerificationLinkPath), http.StatusNotFound)
		must(h, sesapi.OpDeleteIdentity, &sesapi.DeleteIdentityInput{Identity: sender})
		wantLinkAnswer(h, h.link(http.MethodGet, link), http.StatusNotFound)
	})
}

// A repeated verify of a Pending address restarts its 24 hours from the
// new email (ses.md 5.2: the link expires 24 hours after sending).
func TestRepeatVerifyRestartsExpiry(t *testing.T) {
	run(t, func(h *harness) {
		half := ses.EmailVerificationExpiry / 2
		h.verifyEmail(sender)
		h.advance(half)
		h.verifyEmail(sender)
		h.advance(ses.EmailVerificationExpiry - half)
		if got := h.status(sender); got != sesapi.VerificationStatusPending {
			h.Fatalf("%s = %q at the first deadline, want Pending until the second", sender, got)
		}
		h.advance(half)
		if got := h.status(sender); got != sesapi.VerificationStatusFailed {
			h.Errorf("%s = %q at the second deadline, want Failed", sender, got)
		}
	})
}

// Deleting a Pending identity disarms its expiry, and a configured one
// arms none and is sent no verification email.
func TestExpiryTimersFollowTheIdentities(t *testing.T) {
	o := ses.Options{VerifiedIdentities: ses.VerifiedIdentities{Addresses: []string{"listed@example.com"}}}
	runWith(t, o, func(h *harness) {
		h.verifyEmail(sender)
		h.verifyEmail("listed@example.com")
		if got := expiryTimers(h); got != 1 {
			h.Errorf("%d expiry timers armed, want 1 for the Pending identity", got)
		}
		if got := h.messages(ses.MessageQuery{To: "listed@example.com"}); len(got) != 0 {
			h.Errorf("a configured identity was sent %d verification emails", len(got))
		}
		must(h, sesapi.OpDeleteIdentity, &sesapi.DeleteIdentityInput{Identity: sender})
		if got := expiryTimers(h); got != 0 {
			h.Errorf("%d expiry timers armed after the delete, want 0", got)
		}
	})
}

func expiryTimers(h *harness) int {
	n := 0
	for _, a := range h.Kernel.Timers().Scheduler().Snapshot() {
		if a.Key.Kind == ses.TimerExpiry {
			n++
		}
	}
	return n
}

// A NextToken ListIdentities never returned is refused.
func TestListIdentitiesRefusesAForeignNextToken(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, sesapi.OpListIdentities, &sesapi.ListIdentitiesInput{NextToken: new("forged")})
		wantCode(h, err, sesapi.IdentityInvalidParameterValue())
	})
}
