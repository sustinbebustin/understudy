package ses

import (
	"context"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The deprecated v1 email-address operations, each the model's stand-in for
// an identity operation (ses.md 5.3), over the same identities.

// verifyEmailAddress is VerifyEmailIdentity under its deprecated name
// (model, VerifyEmailAddress): the same start, restart and recorded
// verification email.
func (s *Service) verifyEmailAddress(ctx context.Context, c *kernel.Call, in *sesapi.VerifyEmailAddressInput) (*sesapi.VerifyEmailAddressOutput, *awserr.Error) {
	if _, err := s.verifyEmailIdentity(ctx, c, &sesapi.VerifyEmailIdentityInput{EmailAddress: in.EmailAddress}); err != nil {
		return nil, err
	}
	return &sesapi.VerifyEmailAddressOutput{}, nil
}

// listVerifiedEmailAddresses lists "email addresses that have been
// verified" (model, ListVerifiedEmailAddressesResponse): the Success email
// identities of the caller's view in creation order, [] when there are
// none. Whether AWS also lists Pending addresses is UNVERIFIED (SES-09).
func (s *Service) listVerifiedEmailAddresses(_ context.Context, c *kernel.Call, _ *sesapi.ListVerifiedEmailAddressesInput) (*sesapi.ListVerifiedEmailAddressesOutput, *awserr.Error) {
	out := []string{}
	if cell, ok := s.cells.Lookup(c.Scope()); ok {
		v := s.view(APIv1)
		out = store.Read(cell, func(st *state) []string {
			names := []string{}
			for i := range st.Identities {
				id := &st.Identities[i]
				if v.sees(id) && id.Kind == sesapi.IdentityTypeEmailAddress && id.verified() {
					names = append(names, id.Name)
				}
			}
			return names
		})
	}
	return &sesapi.ListVerifiedEmailAddressesOutput{VerifiedEmailAddresses: out}, nil
}

// deleteVerifiedEmailAddress removes an email identity as DeleteIdentity
// does; an address the store does not hold succeeds unchanged, as
// DeleteIdentity's does (inferred, SES-11). A domain is no email address
// and is left in place (inferred from the operation's name and model text).
func (s *Service) deleteVerifiedEmailAddress(_ context.Context, c *kernel.Call, in *sesapi.DeleteVerifiedEmailAddressInput) (*sesapi.DeleteVerifiedEmailAddressOutput, *awserr.Error) {
	scope := c.Scope()
	v := s.view(APIv1)
	_, err := kernel.Write(c, s.cells, scope, func(st *state, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		i := st.find(v, in.EmailAddress)
		if i < 0 || st.Identities[i].Kind != sesapi.IdentityTypeEmailAddress {
			tx.Unchanged()
			return struct{}{}, nil
		}
		s.drop(st, scope, i, tx)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sesapi.DeleteVerifiedEmailAddressOutput{}, nil
}
