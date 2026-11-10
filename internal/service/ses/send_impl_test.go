package ses_test

import (
	"fmt"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// passesValidation fails when err is InvalidParameterValue, the answer of
// every failed validation check.
func passesValidation(h *harness, what string, err *awserr.Error) {
	h.Helper()
	if err != nil && err.ID.QueryCode == sesapi.IdentityInvalidParameterValue().QueryCode {
		h.Errorf("%s: %s %q, want it past validation", what, err.ID.QueryCode, err.Message)
	}
}

// Every address of a send is validated, the sender, Reply-To and
// Return-Path included, and one invalid address rejects the message
// (ses.md 4.1); the texts are inferred and read from the constants
// (SES-04). Addresses must be 7-bit ASCII (model, ses.md 4.1).
func TestSendEmailValidatesEveryAddress(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		_, err := call(h, sesapi.OpSendEmail, email("no-at-sign", simulator))
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), ses.AddressMissingDomainText)

		in := email(sender, simulator)
		in.ReplyToAddresses = []string{"reply@example."}
		_, err = call(h, sesapi.OpSendEmail, in)
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), ses.AddressTrailingDotText)

		in = email(sender, simulator)
		in.ReturnPath = new("bounce\t@example.com")
		_, err = call(h, sesapi.OpSendEmail, in)
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), ses.AddressWhitespaceText)

		for _, bad := range []string{"a@b@example.com", "josé@example.com", "<unclosed@example.com"} {
			_, err = call(h, sesapi.OpSendEmail, email(sender, simulator, bad))
			wantErr(h, err, sesapi.IdentityInvalidParameterValue(), ses.AddressIllegalText)
		}
		if got := len(h.sends()); got != 0 {
			h.Errorf("%d messages recorded from rejected sends", got)
		}

		// A display name is a valid mailbox (ses.md 6.1 quotes one).
		_, err = call(h, sesapi.OpSendEmail, email("Sender <"+sender+">", simulator))
		passesValidation(h, "display-name Source", err)
	})
}

// A tag value admits "." and "@", which a name does not (recorded texts,
// ses.md 4.5).
func TestTagValueAdmitsDotAndAt(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		in := email(sender, simulator)
		in.Tags = []sesapi.MessageTag{{Name: "campaign_id-1", Value: "user.name@example.com"}}
		_, err := call(h, sesapi.OpSendEmail, in)
		passesValidation(h, "tag value with . and @", err)
	})
}

// NotImplemented members answer before tags, and tags before addresses
// (SendChecks; ses.send.check-order, SES-17).
func TestSendEmailChecksBeforeAddresses(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		in := email(sender, "no-at-sign")
		in.Tags = []sesapi.MessageTag{{Name: "bad:name", Value: "v"}}
		in.ConfigurationSetName = new("set")
		_, err := call(h, sesapi.OpSendEmail, in)
		wantNotImplemented(h, err, "ConfigurationSetName")

		in.ConfigurationSetName = nil
		_, err = call(h, sesapi.OpSendEmail, in)
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(),
			"Invalid tag name <bad:name>: only alphanumeric ASCII characters, '_',  '-' are allowed.")

		in.Tags = nil
		_, err = call(h, sesapi.OpSendEmail, in)
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), ses.AddressMissingDomainText)
	})
}

// VerifyEmailIdentity refuses an invalid address as a send does and
// creates nothing (the text is recorded, SES-04).
func TestVerifyEmailIdentityRefusesInvalidAddress(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, sesapi.OpVerifyEmailIdentity, &sesapi.VerifyEmailIdentityInput{EmailAddress: "no-at-sign"})
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), fmt.Sprintf(ses.VerifyAddressText, "no-at-sign"))
		if got := h.attributes("no-at-sign"); len(got) != 0 {
			h.Errorf("VerificationAttributes = %v after a refused verify, want none", got)
		}
		if got := len(h.messages(ses.MessageQuery{})); got != 0 {
			h.Errorf("%d messages recorded by a refused verify", got)
		}
	})
}
