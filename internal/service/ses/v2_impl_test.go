package ses_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// CreateEmailIdentity of an identity the store holds is
// AlreadyExistsException; the text is inferred (SES-11) and read from the
// constant (contract 2.5).
func TestCreateEmailIdentityRefusesAnExistingIdentity(t *testing.T) {
	run(t, func(h *harness) {
		h.verifyEmail("person@example.com")
		_, err := call(h, sesv2api.OpCreateEmailIdentity, &sesv2api.CreateEmailIdentityInput{EmailIdentity: "person@example.com"})
		wantErr(h, err, sesv2api.IdentityAlreadyExistsException(), fmt.Sprintf(ses.IdentityExistsText, "person@example.com"))
	})
}

// ConfigurationSetName answers NotImplemented naming it, and creates
// nothing (spec conflict row 4; invariant 3).
func TestCreateEmailIdentityConfigurationSetIsNotImplemented(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, sesv2api.OpCreateEmailIdentity, &sesv2api.CreateEmailIdentityInput{EmailIdentity: "example.org", ConfigurationSetName: new("set")})
		wantNotImplemented(h, err, "ConfigurationSetName")
		_, err = call(h, sesv2api.OpGetEmailIdentity, &sesv2api.GetEmailIdentityInput{EmailIdentity: "example.org"})
		wantCode(h, err, sesv2api.IdentityNotFoundException())
	})
}

// DkimSigningAttributes are for domains only (model, ses.md 5.4); on a
// domain a BYODKIM selector is stored and reported as an EXTERNAL origin
// (contract 2.5), the tokens unchanged.
func TestDkimSigningAttributes(t *testing.T) {
	run(t, func(h *harness) {
		attrs := &sesv2api.DkimSigningAttributes{DomainSigningSelector: new("sel"), DomainSigningPrivateKey: new("a2V5")}
		_, err := call(h, sesv2api.OpCreateEmailIdentity, &sesv2api.CreateEmailIdentityInput{EmailIdentity: "person@example.com", DkimSigningAttributes: attrs})
		wantErr(h, err, sesv2api.IdentityBadRequestException(), ses.DkimOnAddressText)
		must(h, sesv2api.OpCreateEmailIdentity, &sesv2api.CreateEmailIdentityInput{EmailIdentity: "example.org", DkimSigningAttributes: attrs})
		d := h.getIdentity("example.org").DkimAttributes
		if d == nil || d.SigningAttributesOrigin == nil || *d.SigningAttributesOrigin != sesv2api.DkimSigningAttributesOriginExternal || len(d.Tokens) != 3 {
			h.Errorf("DKIM with a BYODKIM selector = %+v, want EXTERNAL and three tokens", d)
		}
	})
}

// An invalid address is refused as VerifyEmailIdentity refuses it
// (ses.md 4.6, SES-04), in v2's identity (contract 1.6).
func TestCreateEmailIdentityValidatesTheAddress(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, sesv2api.OpCreateEmailIdentity, &sesv2api.CreateEmailIdentityInput{EmailIdentity: "a b@example.com"})
		wantErr(h, err, sesv2api.IdentityBadRequestException(), ses.AddressWhitespaceText)
	})
}

// A v2 domain answers a stable v1 verification token, as v1 domains do
// (ses.md 9.2), and a v1 domain reads in v2 without Easy DKIM tokens.
func TestDomainsReadAcrossAPIs(t *testing.T) {
	run(t, func(h *harness) {
		h.createIdentity("v2.example")
		a := h.attributes("v2.example")["v2.example"]
		if a.VerificationToken == nil || len(*a.VerificationToken) != 44 {
			h.Errorf("v1 reads the v2 domain as %+v, want a 44-character token (ses.md 5.3)", a)
		}
		if again := h.verifyDomain("v2.example"); a.VerificationToken == nil || again != *a.VerificationToken {
			h.Errorf("VerifyDomainIdentity of the v2 domain = %q, want its token", again)
		}
		h.verifyDomain("v1.example")
		if d := h.getIdentity("v1.example").DkimAttributes; d == nil || len(d.Tokens) != 0 || d.Status == nil || *d.Status != sesv2api.DkimStatusNotStarted {
			h.Errorf("v2 reads the v1 domain's DKIM as %+v, want NOT_STARTED without tokens", d)
		}
	})
}

// The tag operations accept only an identity ARN of the caller's scope;
// a template ARN or another Region's identity is NotFoundException naming
// the resource (contract 2.5).
func TestTagOperationsRefuseOtherARNs(t *testing.T) {
	run(t, func(h *harness) {
		h.createIdentity("example.org")
		template := "arn:aws:ses:" + h.Scope.Region.String() + ":" + h.Scope.Account.String() + ":template/welcome"
		_, err := call(h, sesv2api.OpListTagsForResource, &sesv2api.ListTagsForResourceInput{ResourceArn: template})
		wantErr(h, err, sesv2api.IdentityNotFoundException(), fmt.Sprintf(ses.IdentityMissingText, "welcome"))
		west := "arn:aws:ses:us-west-2:" + h.Scope.Account.String() + ":identity/example.org"
		_, err = call(h, sesv2api.OpTagResource, &sesv2api.TagResourceInput{ResourceArn: west, Tags: []sesv2api.Tag{{Key: "k", Value: "v"}}})
		wantErr(h, err, sesv2api.IdentityNotFoundException(), fmt.Sprintf(ses.IdentityMissingText, "example.org"))
		_, err = call(h, sesv2api.OpUntagResource, &sesv2api.UntagResourceInput{ResourceArn: h.identityARN("gone.example"), TagKeys: []string{"k"}})
		wantErr(h, err, sesv2api.IdentityNotFoundException(), fmt.Sprintf(ses.IdentityMissingText, "gone.example"))
	})
}

// TagResource of a key already set replaces its value.
func TestTagResourceReplacesAValue(t *testing.T) {
	run(t, func(h *harness) {
		h.createIdentity("example.org", sesv2api.Tag{Key: "env", Value: "dev"})
		arn := h.identityARN("example.org")
		must(h, sesv2api.OpTagResource, &sesv2api.TagResourceInput{ResourceArn: arn, Tags: []sesv2api.Tag{{Key: "env", Value: "prod"}}})
		got := must(h, sesv2api.OpListTagsForResource, &sesv2api.ListTagsForResourceInput{ResourceArn: arn}).Tags
		if len(got) != 1 || got[0].Key != "env" || got[0].Value != "prod" {
			h.Errorf("tags = %+v, want env=prod alone", got)
		}
		if tags := h.getIdentity("example.org").Tags; len(tags) != 1 || tags[0].Value != "prod" {
			h.Errorf("GetEmailIdentity Tags = %+v", tags)
		}
	})
}

// ListEmailIdentities pages by PageSize with its own NextToken, and
// refuses a token it never returned.
func TestListEmailIdentitiesPages(t *testing.T) {
	run(t, func(h *harness) {
		for _, n := range []string{"a.example", "b.example", "c.example"} {
			h.createIdentity(n)
		}
		size := int32(2)
		first := must(h, sesv2api.OpListEmailIdentities, &sesv2api.ListEmailIdentitiesInput{PageSize: &size})
		if len(first.EmailIdentities) != 2 || first.NextToken == nil {
			h.Fatalf("first page = %+v", first)
		}
		second := must(h, sesv2api.OpListEmailIdentities, &sesv2api.ListEmailIdentitiesInput{PageSize: &size, NextToken: first.NextToken})
		if len(second.EmailIdentities) != 1 || second.NextToken != nil {
			h.Errorf("second page = %+v", second)
		}
		_, err := call(h, sesv2api.OpListEmailIdentities, &sesv2api.ListEmailIdentitiesInput{NextToken: new("forged")})
		wantCode(h, err, sesv2api.IdentityBadRequestException())
	})
}

// The 10,001st identity of a scope is refused through v2 as through v1
// (ses.md 5.1; the text is inferred and read from the constant).
func TestCreateEmailIdentityLimit(t *testing.T) {
	run(t, func(h *harness) {
		for i := range ses.MaxIdentities {
			h.verifyDomain(fmt.Sprintf("d%d.example", i))
		}
		_, err := call(h, sesv2api.OpCreateEmailIdentity, &sesv2api.CreateEmailIdentityInput{EmailIdentity: "over.example"})
		wantErr(h, err, sesv2api.IdentityLimitExceededException(), fmt.Sprintf(ses.IdentityLimitText, ses.MaxIdentities))
	})
}

// Under ses.store.shared-v1-v2=separate one name can be an identity of
// each API; each expires on its own timer, and deleting one leaves the
// other (contract 1.4).
func TestSeparateStoresKeepTheirOwnTimers(t *testing.T) {
	runWith(t, ses.Options{Store: ses.StoreSeparate}, func(h *harness) {
		h.verifyEmail("both@example.com")
		h.createIdentity("both@example.com")
		h.advance(ses.EmailVerificationExpiry + time.Second)
		if got := h.status("both@example.com"); got != sesapi.VerificationStatusFailed {
			h.Errorf("v1 identity = %q after expiry, want Failed", got)
		}
		if got := h.getIdentity("both@example.com").VerificationStatus; got == nil || *got != sesv2api.VerificationStatusFailed {
			h.Errorf("v2 identity = %v after expiry, want FAILED", got)
		}
		must(h, sesv2api.OpDeleteEmailIdentity, &sesv2api.DeleteEmailIdentityInput{EmailIdentity: "both@example.com"})
		if got := h.status("both@example.com"); got != sesapi.VerificationStatusFailed {
			h.Errorf("v1 identity = %q after the v2 delete, want Failed", got)
		}
	})
}

// Under separate stores a v1 send passes only identities v1 created.
func TestSeparateStoresCheckSendersInTheirOwnAPI(t *testing.T) {
	runWith(t, ses.Options{Store: ses.StoreSeparate, VerifiedIdentities: ses.VerifiedIdentities{Addresses: []string{sender}}}, func(h *harness) {
		h.createIdentity(sender)
		_, err := call(h, sesapi.OpSendEmail, email(sender, simulator))
		wantErr(h, err, sesapi.IdentityMessageRejected(), unverifiedText(h.Scope.Region, sender))
		h.verifyEmail(sender)
		must(h, sesapi.OpSendEmail, email(sender, simulator))
	})
}

// A v2 identity's tags, DKIM tokens and status survive a snapshot
// (contract 1.3).
func TestV2IdentitySnapshotRoundTrip(t *testing.T) {
	run(t, func(h *harness) {
		h.createIdentity("example.org", sesv2api.Tag{Key: "env", Value: "dev"})
		before := h.getIdentity("example.org")
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.v2.Reset(kernel.ResetScope{})
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		after := h.getIdentity("example.org")
		if after.DkimAttributes == nil || before.DkimAttributes == nil || !slices.Equal(after.DkimAttributes.Tokens, before.DkimAttributes.Tokens) ||
			!after.DkimAttributes.LastKeyGenerationTimestamp.Equal(*before.DkimAttributes.LastKeyGenerationTimestamp) ||
			len(after.Tags) != 1 || after.Tags[0].Key != "env" || *after.VerificationStatus != *before.VerificationStatus {
			h.Errorf("after the load %+v, before %+v", after, before)
		}
	})
}

// A tag operation journals the identity its ResourceArn names (contract
// 2.6).
func TestTagOperationsTouchTheIdentity(t *testing.T) {
	run(t, func(h *harness) {
		h.createIdentity("example.org")
		arn := h.identityARN("example.org")
		must(h, sesv2api.OpTagResource, &sesv2api.TagResourceInput{ResourceArn: arn, Tags: []sesv2api.Tag{{Key: "env", Value: "dev"}}})
		calls := h.apiCalls("sesv2", "TagResource")
		if len(calls) != 1 {
			h.Fatalf("%d TagResource calls journaled", len(calls))
		}
		wantTouches(h, calls[0].Resources, arn)
	})
}

// GetAccount reports a HEALTHY enforcement status and sending enabled,
// since sending cannot be paused yet (ses.md 11.1 GetAccount note).
func TestGetAccountStatus(t *testing.T) {
	run(t, func(h *harness) {
		acct := must(h, sesv2api.OpGetAccount, &sesv2api.GetAccountInput{})
		if !acct.SendingEnabled || acct.EnforcementStatus == nil || *acct.EnforcementStatus != "HEALTHY" || acct.ProductionAccessEnabled {
			h.Errorf("GetAccount = %+v", acct)
		}
	})
}
