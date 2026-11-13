package ses_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// putDefaultSet sets (or, nil, clears) identity's default configuration
// set through v2.
func (h *harness) putDefaultSet(identity string, set *string) {
	h.Helper()
	must(h, sesv2api.OpPutEmailIdentityConfigurationSetAttributes,
		&sesv2api.PutEmailIdentityConfigurationSetAttributesInput{EmailIdentity: identity, ConfigurationSetName: set})
}

// sentSet sends in through v2 and returns the configuration set the
// message recorded.
func (h *harness) sentSet(in *sesv2api.SendEmailInput) string {
	h.Helper()
	must(h, sesv2api.OpSendEmail, in)
	sends := h.sends()
	return sends[len(sends)-1].ConfigurationSet
}

// A send naming no set records the default of its sender identity (ses.md
// 8: "will only be applied if the identity is verified"); clearing the
// default removes it.
func TestSendRecordsTheSenderIdentitysDefaultSet(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		h.createSet("alpha")
		h.putDefaultSet(sender, new("alpha"))
		if got := h.sentSet(simpleEmail(sender, recipient)); got != "alpha" {
			h.Errorf("recorded set = %q, want the sender's default alpha", got)
		}
		h.putDefaultSet(sender, nil)
		if got := h.sentSet(simpleEmail(sender, recipient)); got != "" {
			h.Errorf("recorded set after clearing = %q, want none", got)
		}
	})
}

// An unverified identity's default is not applied: the send falls back to
// the verified domain's default, or to none.
func TestUnverifiedIdentityDefaultIsNotApplied(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		const pending = "pending@" + verifiedDomain
		h.createSet("alpha")
		h.createSet("beta")
		must(h, sesv2api.OpCreateEmailIdentity, &sesv2api.CreateEmailIdentityInput{EmailIdentity: pending, ConfigurationSetName: new("alpha")})
		if got := h.sentSet(simpleEmail(pending, recipient)); got != "" {
			h.Errorf("recorded set = %q, want none while pending@ is unverified", got)
		}
		h.putDefaultSet(verifiedDomain, new("beta"))
		if got := h.sentSet(simpleEmail(pending, recipient)); got != "beta" {
			h.Errorf("recorded set = %q, want the verified domain's default beta", got)
		}
	})
}

// An address's default overrides its domain's; a set named in the request
// overrides both (ses.md 8).
func TestAddressDefaultOverridesDomainAndRequestOverridesBoth(t *testing.T) {
	const address = "person@" + verifiedDomain
	var o ses.Options
	o.VerifiedIdentities.Addresses = []string{address}
	runVerified(t, o, func(h *harness) {
		h.createSet("domain-set")
		h.createSet("address-set")
		h.createSet("request-set")
		h.putDefaultSet(verifiedDomain, new("domain-set"))
		if got := h.sentSet(simpleEmail(address, recipient)); got != "domain-set" {
			h.Errorf("recorded set = %q, want the domain's default", got)
		}
		must(h, sesv2api.OpCreateEmailIdentity, &sesv2api.CreateEmailIdentityInput{EmailIdentity: address})
		h.putDefaultSet(address, new("address-set"))
		if got := h.sentSet(simpleEmail(address, recipient)); got != "address-set" {
			h.Errorf("recorded set = %q, want the address's default", got)
		}
		in := simpleEmail(address, recipient)
		in.ConfigurationSetName = new("request-set")
		if got := h.sentSet(in); got != "request-set" {
			h.Errorf("recorded set = %q, want the request's set", got)
		}
	})
}

// CreateEmailIdentity stores ConfigurationSetName, which GetEmailIdentity
// returns; the set must exist.
func TestCreateEmailIdentityStoresItsConfigurationSet(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, sesv2api.OpCreateEmailIdentity, &sesv2api.CreateEmailIdentityInput{EmailIdentity: "example.org", ConfigurationSetName: new("missing")})
		wantErr(h, err, sesv2api.IdentityNotFoundException(), setMissingText("missing"))
		_, err = call(h, sesv2api.OpGetEmailIdentity, &sesv2api.GetEmailIdentityInput{EmailIdentity: "example.org"})
		wantCode(h, err, sesv2api.IdentityNotFoundException())
		h.createSet("alpha")
		must(h, sesv2api.OpCreateEmailIdentity, &sesv2api.CreateEmailIdentityInput{EmailIdentity: "example.org", ConfigurationSetName: new("alpha")})
		if got := h.getIdentity("example.org").ConfigurationSetName; got == nil || *got != "alpha" {
			h.Errorf("GetEmailIdentity ConfigurationSetName = %v, want alpha", got)
		}
		h.putDefaultSet("example.org", nil)
		if got := h.getIdentity("example.org").ConfigurationSetName; got != nil {
			h.Errorf("GetEmailIdentity ConfigurationSetName after clearing = %v, want absent", *got)
		}
	})
}

// PutEmailIdentityConfigurationSetAttributes refuses a missing identity or
// set and changes nothing.
func TestPutDefaultSetRefusesMissingIdentityOrSet(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, sesv2api.OpPutEmailIdentityConfigurationSetAttributes,
			&sesv2api.PutEmailIdentityConfigurationSetAttributesInput{EmailIdentity: "example.org"})
		wantCode(h, err, sesv2api.IdentityNotFoundException())
		must(h, sesv2api.OpCreateEmailIdentity, &sesv2api.CreateEmailIdentityInput{EmailIdentity: "example.org"})
		_, err = call(h, sesv2api.OpPutEmailIdentityConfigurationSetAttributes,
			&sesv2api.PutEmailIdentityConfigurationSetAttributesInput{EmailIdentity: "example.org", ConfigurationSetName: new("missing")})
		wantErr(h, err, sesv2api.IdentityNotFoundException(), setMissingText("missing"))
		if got := h.getIdentity("example.org").ConfigurationSetName; got != nil {
			h.Errorf("ConfigurationSetName after a refused put = %v, want absent", *got)
		}
	})
}

// PutEmailIdentityDkimSigningAttributes stores a BYODKIM selector (EXTERNAL
// origin) and leaves the DKIM status and tokens as they were; AWS_SES
// origin drops the selector.
func TestPutDkimSigningAttributesChangesNoStatus(t *testing.T) {
	run(t, func(h *harness) {
		must(h, sesv2api.OpCreateEmailIdentity, &sesv2api.CreateEmailIdentityInput{EmailIdentity: "example.org"})
		before := h.getIdentity("example.org").DkimAttributes
		external := sesv2api.DkimSigningAttributesOriginExternal
		out := must(h, sesv2api.OpPutEmailIdentityDkimSigningAttributes, &sesv2api.PutEmailIdentityDkimSigningAttributesInput{
			EmailIdentity:           "example.org",
			SigningAttributesOrigin: external,
			SigningAttributes:       &sesv2api.DkimSigningAttributes{DomainSigningSelector: new("sel"), DomainSigningPrivateKey: new("a2V5")},
		})
		after := h.getIdentity("example.org").DkimAttributes
		if after.Status == nil || before.Status == nil || *after.Status != *before.Status || len(after.Tokens) != 3 {
			h.Errorf("DKIM status/tokens changed: before %+v, after %+v", before, after)
		}
		if after.SigningAttributesOrigin == nil || *after.SigningAttributesOrigin != external {
			h.Errorf("origin = %v, want EXTERNAL", after.SigningAttributesOrigin)
		}
		if out.DkimStatus == nil || *out.DkimStatus != *before.Status || len(out.DkimTokens) != 3 {
			h.Errorf("output = %+v, want the unchanged status and tokens", out)
		}
		must(h, sesv2api.OpPutEmailIdentityDkimSigningAttributes, &sesv2api.PutEmailIdentityDkimSigningAttributesInput{
			EmailIdentity: "example.org", SigningAttributesOrigin: sesv2api.DkimSigningAttributesOriginAwsSes,
		})
		if o := h.getIdentity("example.org").DkimAttributes.SigningAttributesOrigin; o == nil || *o != sesv2api.DkimSigningAttributesOriginAwsSes {
			h.Errorf("origin after AWS_SES = %v, want AWS_SES", o)
		}
	})
}
