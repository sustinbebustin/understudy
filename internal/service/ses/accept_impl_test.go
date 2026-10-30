package ses_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// A Pending identity is not verified: only Success passes the sender check
// (ses.md 5.1, 5.2).
func TestPendingSenderIsRejected(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		const pending = "pending@example.com"
		h.verifyEmail(pending)
		_, err := call(h, sesapi.OpSendEmail, email(pending, simulator))
		wantErr(h, err, sesapi.IdentityMessageRejected(), unverifiedText(h.Scope.Region, pending))
	})
}

// Simulator addresses match their domain regardless of case; any other
// local part there is an ordinary recipient, refused in the sandbox
// (ses.simulator.addresses, SES-19).
func TestSimulatorMatching(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		upper := "success@SIMULATOR.amazonses.com"
		must(h, sesapi.OpSendEmail, email(sender, upper))
		if got := h.sends()[0].Simulator[upper]; got != ses.ScenarioSuccess {
			h.Errorf("Simulator[%s] = %q, want %q", upper, got, ses.ScenarioSuccess)
		}
		other := "nobody@" + ses.SimulatorDomain
		_, err := call(h, sesapi.OpSendEmail, email(sender, other))
		wantErr(h, err, sesapi.IdentityMessageRejected(), unverifiedText(h.Scope.Region, other))
	})
}

// The sender check runs before the sandbox check, and the sandbox check
// before the daily quota (SendChecks; ses.send.check-order, SES-17).
func TestIdentityChecksBeforeDailyQuota(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		for batch := range int(ses.SandboxMax24HourSend) / ses.MaxRecipients {
			to := make([]string, ses.MaxRecipients)
			for i := range to {
				to[i] = fmt.Sprintf("u%d-%d@%s", batch, i, verifiedDomain)
			}
			must(h, sesapi.OpSendEmail, email(sender, to...))
		}
		const stranger = "stranger@unverified.test"
		_, err := call(h, sesapi.OpSendEmail, email(stranger, recipient))
		wantErr(h, err, sesapi.IdentityMessageRejected(), unverifiedText(h.Scope.Region, stranger))
		_, err = call(h, sesapi.OpSendEmail, email(sender, stranger))
		wantErr(h, err, sesapi.IdentityMessageRejected(), unverifiedText(h.Scope.Region, stranger))
	})
}

// Production access lifts the daily quota with the recipient check: its
// Max24HourSend is unlimited (ProductionMax24HourSend, SES-26), and every
// recipient is still counted.
func TestProductionAccessHasNoDailyQuota(t *testing.T) {
	runVerified(t, ses.Options{ProductionAccess: true}, func(h *harness) {
		sent := 0
		for batch := range int(ses.SandboxMax24HourSend)/ses.MaxRecipients + 1 {
			to := make([]string, ses.MaxRecipients)
			for i := range to {
				to[i] = fmt.Sprintf("u%d-%d@elsewhere.test", batch, i)
			}
			must(h, sesapi.OpSendEmail, email(sender, to...))
			sent += len(to)
		}
		if got := h.quota().SentLast24Hours; got != float64(sent) {
			h.Errorf("SentLast24Hours = %v, want %d", got, sent)
		}
	})
}

// The counter keeps a recipient until 24 hours have passed (ses.md 6.3),
// counted per minute.
func TestCounterWindow(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		must(h, sesapi.OpSendEmail, email(sender, recipient))
		h.advance(24*time.Hour - time.Minute)
		if got := h.quota().SentLast24Hours; got != 1 {
			h.Errorf("SentLast24Hours = %v a minute before the day is out, want 1", got)
		}
		h.advance(time.Minute)
		if got := h.quota().SentLast24Hours; got != 0 {
			h.Errorf("SentLast24Hours = %v a day later, want 0", got)
		}
	})
}

// The recipient counter is snapshot state: it survives Export, Reset and
// Import even after the messages are cleared (contract 1.3).
func TestCounterRoundTrips(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		must(h, sesapi.OpSendEmail, email(sender, recipient, "other@"+verifiedDomain))
		if _, err := h.svc.ClearMessages(ses.MessageQuery{}); err != nil {
			h.Fatal(err)
		}
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		if got := h.quota().SentLast24Hours; got != 2 {
			h.Errorf("SentLast24Hours = %v after the load, want 2", got)
		}
	})
}

// A known identity reports no topics, forwarding on and every header
// setting off, as the 11.1 coverage note says.
func TestNotificationAttributeValues(t *testing.T) {
	run(t, func(h *harness) {
		h.verifyEmail(sender)
		out := must(h, sesapi.OpGetIdentityNotificationAttributes, &sesapi.GetIdentityNotificationAttributesInput{Identities: []string{sender}})
		want := sesapi.IdentityNotificationAttributes{ForwardingEnabled: true}
		if got := out.NotificationAttributes[sender]; got != want {
			h.Errorf("NotificationAttributes[%s] = %+v, want %+v", sender, got, want)
		}
	})
}
