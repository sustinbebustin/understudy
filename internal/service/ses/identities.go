package ses

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// bindIdentities binds the v1 identity operations. Each uses its primary
// action alone: the generated lists hold no other ses action a call of
// these operations needs (contract 2.6).
func (s *Service) bindIdentities(b sesapi.Binder) {
	b.VerifyEmailIdentity(s.verifyEmailIdentity,
		registry.Partial{Note: "the verification email is recorded, never sent, for GET /_understudy/v1/ses/messages; the identity starts and stays Pending: " +
			"the link the email carries is not served yet, and ses.verifiedIdentities, the 24-hour expiry, address validation " +
			"and the 10,000-identity limit are not implemented yet"},
		kernel.WithoutSecondaryActions(),
		identitiesNamed(func(in *sesapi.VerifyEmailIdentityInput) []string { return []string{in.EmailAddress} }))
	b.GetIdentityVerificationAttributes(s.getIdentityVerificationAttributes,
		registry.Partial{Note: "the 100-identity limit per call is not enforced yet"},
		kernel.WithoutSecondaryActions(),
		identitiesNamed(func(in *sesapi.GetIdentityVerificationAttributesInput) []string { return in.Identities }))
	b.DeleteIdentity(s.deleteIdentity, registry.Full{},
		kernel.WithoutSecondaryActions(),
		identitiesNamed(func(in *sesapi.DeleteIdentityInput) []string { return []string{in.Identity} }))
}

// identitiesNamed names the identities an input addresses as the call's
// resources (contract 2.6), so the journal names what a call acted on.
func identitiesNamed[In any](namesOf func(*In) []string) kernel.BindOption {
	return kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		names := namesOf(in)
		out := make([]aws.ARN, len(names))
		for i, n := range names {
			out[i] = identityARN(c.Scope(), n)
		}
		return out
	})
}

// verifyEmailIdentity starts verifying an email address: a new identity is
// Pending (ses.md 5.2); a Pending, Failed or TemporaryFailure one restarts
// as Pending, and a Success one is left as it is (inferred, SES-09). Each
// start and restart records a verification email, always with the
// identity's one link token (contract 2.3).
func (s *Service) verifyEmailIdentity(_ context.Context, c *kernel.Call, in *sesapi.VerifyEmailIdentityInput) (*sesapi.VerifyEmailIdentityOutput, *awserr.Error) {
	scope := c.Scope()
	_, err := kernel.Write(c, s.cells, scope, func(st *state, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		i := st.find(in.EmailAddress)
		if i < 0 {
			st.Identities = append(st.Identities, identity{
				Name: in.EmailAddress, Kind: sesapi.IdentityTypeEmailAddress, Status: statusPending{Since: now},
				LinkToken: s.linkToken(), Created: now,
			})
			st.record(s.verificationEmail(c, APIv1, in.EmailAddress, st.Identities[len(st.Identities)-1].LinkToken, now))
			return struct{}{}, nil
		}
		id := &st.Identities[i]
		old := id.Status
		switch old.(type) {
		case statusSuccess:
			tx.Unchanged()
			return struct{}{}, nil
		case statusPending:
		default:
			tx.Record(scope, journal.StateChange{
				Resource: identityARN(scope, in.EmailAddress),
				From:     journal.ResourceState(old.v1()), To: journal.ResourceState(sesapi.VerificationStatusPending),
			})
		}
		id.Status = statusPending{Since: now}
		if id.LinkToken == "" {
			id.LinkToken = s.linkToken()
		}
		st.record(s.verificationEmail(c, APIv1, id.Name, id.LinkToken, now))
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sesapi.VerifyEmailIdentityOutput{}, nil
}

// linkToken draws a verification link token: 32 bytes as base64url without
// padding (contract 2.3).
func (s *Service) linkToken() string {
	return base64.RawURLEncoding.EncodeToString(s.ids.Draw(ids.KindSESLinkToken, 32))
}

// verificationEmail is the record of the verification email call c sends
// to address (ses.md 5.2, 11.3 item 1): from VerificationSender, the
// subject naming the Region, and a text body carrying the link, on its own
// line, built from the host the call was sent to. AWS's body text is
// unrecorded; this one is understudy's.
func (s *Service) verificationEmail(c *kernel.Call, api API, address, token string, now clock.Instant) record {
	region := c.Scope().Region.String()
	link := c.Endpoint().String() + VerificationLinkPath + "?token=" + token
	r := s.newRecord(c, api, KindVerification, now)
	r.Source = VerificationSender
	r.From = []string{VerificationSender}
	r.To = []string{address}
	r.Envelope = []string{address}
	r.Subject = &Content{Data: fmt.Sprintf(VerificationSubjectText, region)}
	r.Text = &Content{Data: "understudy recorded this verification email for " + address + " in " + region + " instead of sending it.\n" +
		"Open this link to verify the address:\n\n" + link + "\n"}
	return r
}

// getIdentityVerificationAttributes answers each named identity's status,
// keyed by the request string byte for byte (ses.md 9.1); an identity the
// store does not hold follows switch ses.verification.unknown-identity.
// An email identity carries no token (inferred, SES-08).
func (s *Service) getIdentityVerificationAttributes(_ context.Context, c *kernel.Call, in *sesapi.GetIdentityVerificationAttributesInput) (*sesapi.GetIdentityVerificationAttributesOutput, *awserr.Error) {
	known := map[string]sesapi.IdentityVerificationAttributes{}
	if cell, ok := s.cells.Lookup(c.Scope()); ok {
		known = store.Read(cell, func(st *state) map[string]sesapi.IdentityVerificationAttributes {
			out := make(map[string]sesapi.IdentityVerificationAttributes, len(in.Identities))
			for _, name := range in.Identities {
				if i := st.find(name); i >= 0 {
					out[name] = sesapi.IdentityVerificationAttributes{VerificationStatus: st.Identities[i].Status.v1()}
				}
			}
			return out
		})
	}
	if s.opts.UnknownIdentity == UnknownIdentityNotStarted {
		for _, name := range in.Identities {
			if _, ok := known[name]; !ok {
				known[name] = sesapi.IdentityVerificationAttributes{VerificationStatus: sesapi.VerificationStatusNotStarted}
			}
		}
	}
	return &sesapi.GetIdentityVerificationAttributesOutput{VerificationAttributes: known}, nil
}

// deleteIdentity removes an identity; one the store does not hold
// succeeds unchanged (inferred, SES-11). Sent messages stay.
func (s *Service) deleteIdentity(_ context.Context, c *kernel.Call, in *sesapi.DeleteIdentityInput) (*sesapi.DeleteIdentityOutput, *awserr.Error) {
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *state, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		i := st.find(in.Identity)
		if i < 0 {
			tx.Unchanged()
			return struct{}{}, nil
		}
		st.Identities = append(st.Identities[:i:i], st.Identities[i+1:]...)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sesapi.DeleteIdentityOutput{}, nil
}
