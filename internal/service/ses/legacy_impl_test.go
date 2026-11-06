package ses_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

func (h *harness) verifiedEmailAddresses() []string {
	h.Helper()
	return must(h, sesapi.OpListVerifiedEmailAddresses, &sesapi.ListVerifiedEmailAddressesInput{}).VerifiedEmailAddresses
}

// VerifyEmailAddress is the deprecated VerifyEmailIdentity (model): the
// address starts Pending and its verification email is recorded under the
// legacy operation (ses.md 5.2, 5.3).
func TestVerifyEmailAddressStartsVerification(t *testing.T) {
	run(t, func(h *harness) {
		must(h, sesapi.OpVerifyEmailAddress, &sesapi.VerifyEmailAddressInput{EmailAddress: "old@example.com"})
		if got := h.status("old@example.com"); got != sesapi.VerificationStatusPending {
			h.Errorf("status after VerifyEmailAddress = %s, want Pending", got)
		}
		msgs := h.messages(ses.MessageQuery{To: "old@example.com"})
		if len(msgs) != 1 || msgs[0].Kind != ses.KindVerification || msgs[0].Operation != "VerifyEmailAddress" {
			h.Errorf("recorded = %+v, want one verification email from VerifyEmailAddress", msgs)
		}
		_, err := call(h, sesapi.OpVerifyEmailAddress, &sesapi.VerifyEmailAddressInput{EmailAddress: "a b@example.com"})
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), fmt.Sprintf(ses.VerifyAddressText, "a b@example.com"))
	})
}

// ListVerifiedEmailAddresses lists "email addresses that have been
// verified" (model, ListVerifiedEmailAddressesResponse): Success email
// identities in creation order, never a domain or a Pending address
// (inferred, SES-09).
func TestListVerifiedEmailAddresses(t *testing.T) {
	runWith(t, ses.Options{VerifiedIdentities: ses.VerifiedIdentities{Addresses: []string{"b@example.com", "a@example.com"}, Domains: []string{"example.net"}}}, func(h *harness) {
		if got := h.verifiedEmailAddresses(); got == nil || len(got) != 0 {
			h.Errorf("with no identities = %#v, want []", got)
		}
		h.verifyEmail("b@example.com")
		h.verifyEmail("pending@example.com")
		h.verifyDomain("example.net")
		h.verifyEmail("a@example.com")
		if got, want := h.verifiedEmailAddresses(), []string{"b@example.com", "a@example.com"}; !slices.Equal(got, want) {
			h.Errorf("ListVerifiedEmailAddresses = %q, want %q", got, want)
		}
	})
}

// DeleteVerifiedEmailAddress removes an email identity as DeleteIdentity
// does; an address the store does not hold succeeds unchanged, as
// DeleteIdentity's does (inferred, SES-11), and a domain is not an email
// address, so it stays.
func TestDeleteVerifiedEmailAddress(t *testing.T) {
	run(t, func(h *harness) {
		h.verifyEmail("old@example.com")
		h.verifyDomain("example.org")
		must(h, sesapi.OpDeleteVerifiedEmailAddress, &sesapi.DeleteVerifiedEmailAddressInput{EmailAddress: "old@example.com"})
		must(h, sesapi.OpDeleteVerifiedEmailAddress, &sesapi.DeleteVerifiedEmailAddressInput{EmailAddress: "never@example.com"})
		must(h, sesapi.OpDeleteVerifiedEmailAddress, &sesapi.DeleteVerifiedEmailAddressInput{EmailAddress: "example.org"})
		got := h.attributes("old@example.com", "example.org")
		if _, ok := got["old@example.com"]; ok {
			h.Errorf("old@example.com still listed: %+v", got)
		}
		if _, ok := got["example.org"]; !ok {
			h.Errorf("the domain was deleted: %+v", got)
		}
		calls := h.apiCalls("ses", "DeleteVerifiedEmailAddress")
		if len(calls) != 3 {
			h.Fatalf("%d DeleteVerifiedEmailAddress calls journaled", len(calls))
		}
		wantTouches(h, calls[0].Resources, h.identityARN("old@example.com"))
	})
}
