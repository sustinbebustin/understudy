package ses_test

import (
	"encoding/base64"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// A new email identity is Pending until its owner verifies it (model,
// GetIdentityVerificationAttributes; ses.md 5.2), keyed by the request
// string byte for byte (provider, ses.md 9.1).
func TestVerifyEmailIdentityStartsPending(t *testing.T) {
	run(t, func(h *harness) {
		const addr = "Owner+tag@Example.COM"
		h.verifyEmail(addr)
		attrs := h.attributes(addr)
		if got := slices.Sorted(maps.Keys(attrs)); !slices.Equal(got, []string{addr}) {
			h.Fatalf("VerificationAttributes keys = %q, want exactly %q", got, addr)
		}
		if got := attrs[addr].VerificationStatus; got != sesapi.VerificationStatusPending {
			h.Errorf("VerificationStatus = %q, want Pending (ses.md 5.2)", got)
		}
	})
}

// A domain identity answers a 44-character base64 token, the encoding of
// 32 bytes (model example, ses.md 5.3), and GetIdentityVerificationAttributes
// returns the same token on every read (provider, ses.md 9.2: the token must
// not change between reads).
func TestVerifyDomainIdentityTokenIsStable(t *testing.T) {
	run(t, func(h *harness) {
		const domain = "example.org"
		token := h.verifyDomain(domain)
		if len(token) != 44 {
			h.Errorf("VerificationToken %q has %d characters, want 44 (ses.md 5.3)", token, len(token))
		}
		if raw, err := base64.StdEncoding.DecodeString(token); err != nil || len(raw) != 32 {
			h.Errorf("VerificationToken %q decodes to %d bytes (%v), want 32 (ses.md 5.3)", token, len(raw), err)
		}
		for range 2 {
			a, ok := h.attributes(domain)[domain]
			if !ok {
				h.Fatalf("GetIdentityVerificationAttributes omits %s", domain)
			}
			if a.VerificationStatus != sesapi.VerificationStatusPending {
				h.Errorf("domain status = %q, want Pending (ses.md 5.2)", a.VerificationStatus)
			}
			if a.VerificationToken == nil || *a.VerificationToken != token {
				h.Errorf("VerificationToken = %v, want %q on every read (ses.md 9.2)", a.VerificationToken, token)
			}
		}
	})
}

// Switch ses.verification.unknown-identity (inferred, SES-08): an identity
// the store does not hold is omitted by default, which Terraform reads as
// gone (provider, ses.md 9.1), and present with NotStarted when flipped.
func TestUnknownIdentityFollowsItsSwitch(t *testing.T) {
	const ghost = "ghost@example.com"
	run(t, func(h *harness) {
		if _, ok := h.attributes(ghost)[ghost]; ok {
			h.Errorf("switch ses.verification.unknown-identity=%s: %s is present, want omitted", ses.UnknownIdentityOmitted, ghost)
		}
	})
	runWith(t, ses.Options{UnknownIdentity: ses.UnknownIdentityNotStarted}, func(h *harness) {
		a, ok := h.attributes(ghost)[ghost]
		if !ok || a.VerificationStatus != sesapi.VerificationStatusNotStarted || a.VerificationToken != nil {
			h.Errorf("switch ses.verification.unknown-identity=%s: %s = %+v (present %v), want NotStarted without a token",
				ses.UnknownIdentityNotStarted, ghost, a, ok)
		}
	})
}

// DeleteIdentity removes the identity: its key is absent after delete
// (provider, ses.md 9.1).
func TestDeleteIdentityRemovesIt(t *testing.T) {
	run(t, func(h *harness) {
		h.verifyEmail(sender)
		must(h, sesapi.OpDeleteIdentity, &sesapi.DeleteIdentityInput{Identity: sender})
		if _, ok := h.attributes(sender)[sender]; ok {
			h.Errorf("%s is still in VerificationAttributes after DeleteIdentity (ses.md 9.1)", sender)
		}
		if ids := must(h, sesapi.OpListIdentities, &sesapi.ListIdentitiesInput{}).Identities; slices.Contains(ids, sender) {
			h.Errorf("ListIdentities = %q after DeleteIdentity", ids)
		}
	})
}

// ListIdentities lists identities "regardless of verification status",
// narrowed by IdentityType (model, ses.md 5.3), and pages with NextToken.
// The order is UNVERIFIED (SES-14), so the pages are compared as a set.
func TestListIdentitiesFiltersAndPages(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		h.verifyEmail("pending@example.com")
		h.verifyDomain("example.org")
		all := listAll(h, nil, 1)
		slices.Sort(all)
		want := []string{"example.org", "pending@example.com", sender, verifiedDomain}
		slices.Sort(want)
		if !slices.Equal(all, want) {
			h.Errorf("ListIdentities pages of 1 = %q, want %q", all, want)
		}
		emails := listAll(h, new(sesapi.IdentityTypeEmailAddress), 1000)
		slices.Sort(emails)
		if !slices.Equal(emails, []string{"pending@example.com", sender}) {
			h.Errorf("ListIdentities EmailAddress = %q", emails)
		}
		domains := listAll(h, new(sesapi.IdentityTypeDomain), 1000)
		slices.Sort(domains)
		if !slices.Equal(domains, []string{"example.org", verifiedDomain}) {
			h.Errorf("ListIdentities Domain = %q", domains)
		}
	})
}

func listAll(h *harness, kind *sesapi.IdentityType, maxItems int32) []string {
	h.Helper()
	var out []string
	var next *string
	for range 100 {
		page := must(h, sesapi.OpListIdentities, &sesapi.ListIdentitiesInput{IdentityType: kind, MaxItems: &maxItems, NextToken: next})
		if len(page.Identities) > int(maxItems) {
			h.Fatalf("a page of MaxItems %d holds %d identities", maxItems, len(page.Identities))
		}
		out = append(out, page.Identities...)
		if page.NextToken == nil {
			return out
		}
		next = page.NextToken
	}
	h.Fatal("ListIdentities never ended")
	return nil
}

// MaxItems is 1 to 1000 (aws-doc API_ListIdentities, ses.md 5.3); outside
// that range the answer is InvalidParameterValue with MaxItemsText, both
// inferred (SES-29) and read from the overlay and the constant.
func TestListIdentitiesMaxItemsRange(t *testing.T) {
	run(t, func(h *harness) {
		for _, n := range []int32{0, ses.ListIdentitiesMaxItems + 1} {
			_, err := call(h, sesapi.OpListIdentities, &sesapi.ListIdentitiesInput{MaxItems: &n})
			wantErr(h, err, sesapi.IdentityInvalidParameterValue(), fmt.Sprintf(ses.MaxItemsText, ses.ListIdentitiesMaxItems))
		}
		for _, n := range []int32{1, ses.ListIdentitiesMaxItems} {
			must(h, sesapi.OpListIdentities, &sesapi.ListIdentitiesInput{MaxItems: &n})
		}
	})
}

// GetIdentityVerificationAttributes takes at most 100 identities (model,
// ses.md 5.3); the answer past that is inferred (SES-08) and read from the
// constant.
func TestVerificationAttributesIdentityLimit(t *testing.T) {
	run(t, func(h *harness) {
		ids := make([]string, ses.MaxVerificationAttributesIdentities+1)
		for i := range ids {
			ids[i] = fmt.Sprintf("u%d@example.com", i)
		}
		_, err := call(h, sesapi.OpGetIdentityVerificationAttributes, &sesapi.GetIdentityVerificationAttributesInput{Identities: ids})
		wantErr(h, err, sesapi.IdentityInvalidParameterValue(), fmt.Sprintf(ses.TooManyIdentitiesText, ses.MaxVerificationAttributesIdentities))
		h.attributes(ids[:ses.MaxVerificationAttributesIdentities]...)
	})
}

// At most 10,000 identities per Region, domains and addresses combined
// (aws-doc creating-identities, ses.md 5.1); the answer past it is
// inferred and read from the constant. Another Region has its own count
// (ses.md 5.1: identities are per Region).
func TestIdentityLimitPerRegion(t *testing.T) {
	run(t, func(h *harness) {
		for i := range ses.MaxIdentities - 1 {
			h.verifyEmail(fmt.Sprintf("u%d@example.com", i))
		}
		h.verifyDomain("last.example")
		_, err := call(h, sesapi.OpVerifyEmailIdentity, &sesapi.VerifyEmailIdentityInput{EmailAddress: "over@example.com"})
		wantErr(h, err, sesapi.IdentityLimitExceededException(), fmt.Sprintf(ses.IdentityLimitText, ses.MaxIdentities))
		_, err = call(h, sesapi.OpVerifyDomainIdentity, &sesapi.VerifyDomainIdentityInput{Domain: "over.example"})
		wantErr(h, err, sesapi.IdentityLimitExceededException(), fmt.Sprintf(ses.IdentityLimitText, ses.MaxIdentities))
		h.Scope.Region = westRegion(h)
		h.verifyEmail("over@example.com")
	})
}

// GetIdentityNotificationAttributes lists a known identity, which the
// aws_ses_email_identity data source requires (provider, ses.md 9.1), and
// omits an unknown one under either value of
// ses.verification.unknown-identity (contract 1.4).
func TestNotificationAttributesListKnownIdentities(t *testing.T) {
	for _, u := range []ses.UnknownIdentity{ses.UnknownIdentityOmitted, ses.UnknownIdentityNotStarted} {
		runWith(t, ses.Options{UnknownIdentity: u}, func(h *harness) {
			h.verifyEmail(sender)
			out := must(h, sesapi.OpGetIdentityNotificationAttributes, &sesapi.GetIdentityNotificationAttributesInput{Identities: []string{sender, "ghost@example.com"}})
			if got := slices.Sorted(maps.Keys(out.NotificationAttributes)); !slices.Equal(got, []string{sender}) {
				h.Errorf("switch ses.verification.unknown-identity=%s: NotificationAttributes keys = %q, want [%s]", u, got, sender)
			}
		})
	}
}

// Expiry (ses.md 5.2, aws-doc: 24 hours for an address, 72 for a domain;
// that the change is exact is UNVERIFIED, SES-27, so the deadline is read
// from the constants): a Pending identity turns Failed at its deadline,
// never Success on its own, and each transition is a StateChange on the
// identity ARN (contract 2.3).
func TestPendingIdentitiesFailAtTheirDeadline(t *testing.T) {
	run(t, func(h *harness) {
		const addr, domain = "late@example.com", "late.example"
		h.verifyEmail(addr)
		h.verifyDomain(domain)
		h.advance(ses.EmailVerificationExpiry - 1)
		if got := h.status(addr); got != sesapi.VerificationStatusPending {
			h.Fatalf("before its deadline the address is %q, want Pending", got)
		}
		h.advance(1)
		if got := h.status(addr); got != sesapi.VerificationStatusFailed {
			h.Errorf("at EmailVerificationExpiry the address is %q, want Failed", got)
		}
		if got := h.status(domain); got != sesapi.VerificationStatusPending {
			h.Errorf("at EmailVerificationExpiry the domain is %q, want Pending until DomainVerificationExpiry", got)
		}
		h.advance(ses.DomainVerificationExpiry - ses.EmailVerificationExpiry)
		if got := h.status(domain); got != sesapi.VerificationStatusFailed {
			h.Errorf("at DomainVerificationExpiry the domain is %q, want Failed", got)
		}
		for _, id := range []string{addr, domain} {
			if got := h.stateChanges(h.identityARN(id)); !slices.Contains(got, "Pending->Failed") {
				h.Errorf("StateChanges of %s = %q, want Pending->Failed", id, got)
			}
		}
	})
}

// An identity named in ses.verifiedIdentities starts Success: exact
// addresses case-sensitively, domains case-insensitively (ses.md 5.1), and
// "*@domain" for addresses whose domain matches exactly (contract 6). It
// does not expire.
func TestConfiguredIdentitiesStartVerified(t *testing.T) {
	o := ses.Options{VerifiedIdentities: ses.VerifiedIdentities{
		Addresses:      []string{"exact@example.com"},
		Domains:        []string{"example.org"},
		AddressDomains: []string{"example.net"},
	}}
	runWith(t, o, func(h *harness) {
		h.verifyEmail("exact@example.com")
		h.verifyEmail("Exact@example.com")
		h.verifyDomain("EXAMPLE.org")
		h.verifyEmail("anyone@example.net")
		h.verifyEmail("someone@sub.example.net")
		h.verifyEmail("user@example.org")
		want := map[string]sesapi.VerificationStatus{
			"exact@example.com":       sesapi.VerificationStatusSuccess,
			"Exact@example.com":       sesapi.VerificationStatusPending,
			"EXAMPLE.org":             sesapi.VerificationStatusSuccess,
			"anyone@example.net":      sesapi.VerificationStatusSuccess,
			"someone@sub.example.net": sesapi.VerificationStatusPending,
			"user@example.org":        sesapi.VerificationStatusPending,
		}
		h.advance(ses.DomainVerificationExpiry)
		for id, st := range want {
			if st == sesapi.VerificationStatusPending {
				st = sesapi.VerificationStatusFailed
			}
			if got := h.status(id); got != st {
				h.Errorf("%s = %q after DomainVerificationExpiry, want %q", id, got, st)
			}
		}
		if got := h.stateChanges(h.identityARN("exact@example.com")); !containsSuffix(got, "->Success") {
			h.Errorf("StateChanges of a configured identity = %q, want one to Success (contract 6)", got)
		}
	})
}

func containsSuffix(ss []string, suffix string) bool {
	return slices.ContainsFunc(ss, func(s string) bool { return strings.HasSuffix(s, suffix) })
}
