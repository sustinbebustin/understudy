package ses_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

func setAccountSending(h *harness, enabled bool) {
	h.Helper()
	must(h, sesapi.OpUpdateAccountSendingEnabled, &sesapi.UpdateAccountSendingEnabledInput{Enabled: enabled})
}

func accountSending(h *harness) (v1, v2 bool) {
	h.Helper()
	return must(h, sesapi.OpGetAccountSendingEnabled, &sesapi.GetAccountSendingEnabledInput{}).Enabled,
		must(h, sesv2api.OpGetAccount, &sesv2api.GetAccountInput{}).SendingEnabled
}

// Account sending starts enabled; UpdateAccountSendingEnabled false fails
// the next v1 and v2 send, v1 with AccountSendingPausedException (model
// code and status; the text is inferred) and v2 with SendingPausedException,
// the only pause error its model declares; true restores sending. Both
// getters follow the setting.
func TestPausedAccountSendingFailsSendsInBothAPIs(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		if v1, v2 := accountSending(h); !v1 || !v2 {
			h.Fatalf("a new account reads sending enabled v1 %v v2 %v, want both true", v1, v2)
		}
		setAccountSending(h, false)
		if v1, v2 := accountSending(h); v1 || v2 {
			h.Errorf("a paused account reads sending enabled v1 %v v2 %v, want both false", v1, v2)
		}
		_, err := call(h, sesapi.OpSendEmail, email(sender, recipient))
		wantErr(h, err, sesapi.IdentityAccountSendingPausedException(), ses.AccountSendingPausedText)
		_, err = call(h, sesv2api.OpSendEmail, simpleEmail(sender, recipient))
		wantErr(h, err, sesv2api.IdentitySendingPausedException(), ses.AccountSendingPausedText)
		if got := len(h.sends()); got != 0 {
			h.Errorf("%d messages recorded from paused sends", got)
		}

		setAccountSending(h, true)
		must(h, sesapi.OpSendEmail, email(sender, recipient))
		must(h, sesv2api.OpSendEmail, simpleEmail(sender, recipient))
	})
}

// A paused account's send is checked for its configuration set first and
// for its sender and recipients after (SendChecks; the step's position is
// inferred, ses.md 6.4).
func TestAccountSendingPauseSitsBetweenTheConfigurationSetAndSenderChecks(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		setAccountSending(h, false)

		in := email("stranger@unverified.test", "someone@unverified.test")
		_, err := call(h, sesapi.OpSendEmail, in)
		wantErr(h, err, sesapi.IdentityAccountSendingPausedException(), ses.AccountSendingPausedText)

		in.ConfigurationSetName = new("missing")
		_, err = call(h, sesapi.OpSendEmail, in)
		wantErr(h, err, sesapi.IdentityConfigurationSetDoesNotExistException(), setMissingText("missing"))
	})
}

// The setting is per account and Region, and a reset of the scope drops it.
func TestAccountSendingIsPerScopeAndResets(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		paused := h.Scope
		setAccountSending(h, false)

		elsewhere := paused
		elsewhere.Region = westRegion(h)
		h.Scope = elsewhere
		if v1, v2 := accountSending(h); !v1 || !v2 {
			h.Errorf("another Region reads sending enabled v1 %v v2 %v, want both true", v1, v2)
		}

		h.Scope = paused
		h.svc.Reset(kernel.ResetScope{})
		if v1, v2 := accountSending(h); !v1 || !v2 {
			h.Errorf("after a reset sending reads enabled v1 %v v2 %v, want both true", v1, v2)
		}
	})
}
