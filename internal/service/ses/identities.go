package ses

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// bindIdentities binds the v1 identity operations. Each uses its primary
// action alone: the generated lists hold no other ses action a call of
// these operations needs.
func (s *Service) bindIdentities(b sesapi.Binder) {
	verifyEmail := registry.Partial{Note: "the identity stays Pending until its verification link under " + VerificationLinkPath +
		" is opened or understudy.json lists it; the verification email is recorded, never sent; Failed after 24 hours on the emulator clock; " +
		"chosen without a recording (docs/research/ses.md 11.4): which addresses are invalid, beyond one without @ (SES-04)"}
	b.VerifyEmailIdentity(s.verifyEmailIdentity, verifyEmail,
		kernel.WithoutSecondaryActions(), v1ConditionKeys(),
		identitiesNamed(func(in *sesapi.VerifyEmailIdentityInput) []string { return []string{in.EmailAddress} }))
	b.VerifyEmailAddress(s.verifyEmailAddress, verifyEmail,
		kernel.WithoutSecondaryActions(), v1ConditionKeys(),
		identitiesNamed(func(in *sesapi.VerifyEmailAddressInput) []string { return []string{in.EmailAddress} }))
	b.ListVerifiedEmailAddresses(s.listVerifiedEmailAddresses, registry.Full{}, kernel.WithoutSecondaryActions(), v1ConditionKeys())
	b.DeleteVerifiedEmailAddress(s.deleteVerifiedEmailAddress, registry.Full{},
		kernel.WithoutSecondaryActions(), v1ConditionKeys(),
		identitiesNamed(func(in *sesapi.DeleteVerifiedEmailAddressInput) []string { return []string{in.EmailAddress} }))
	b.VerifyDomainIdentity(s.verifyDomainIdentity,
		registry.Partial{Note: "the domain stays Pending until understudy.json lists it; DNS is never queried; Failed after 72 hours on the emulator clock"},
		kernel.WithoutSecondaryActions(), v1ConditionKeys(),
		identitiesNamed(func(in *sesapi.VerifyDomainIdentityInput) []string { return []string{in.Domain} }))
	b.GetIdentityVerificationAttributes(s.getIdentityVerificationAttributes, registry.Full{},
		kernel.WithoutSecondaryActions(), v1ConditionKeys(),
		identitiesNamed(func(in *sesapi.GetIdentityVerificationAttributesInput) []string { return in.Identities }))
	b.GetIdentityNotificationAttributes(s.getIdentityNotificationAttributes,
		registry.Partial{Note: "every identity reports no SNS topics, ForwardingEnabled true and headers disabled; " +
			"SetIdentityNotificationTopic and the other setters are not implemented"},
		kernel.WithoutSecondaryActions(), v1ConditionKeys(),
		identitiesNamed(func(in *sesapi.GetIdentityNotificationAttributesInput) []string { return in.Identities }))
	b.ListIdentities(s.listIdentities, registry.Full{}, kernel.WithoutSecondaryActions(), v1ConditionKeys())
	b.DeleteIdentity(s.deleteIdentity, registry.Full{},
		kernel.WithoutSecondaryActions(), v1ConditionKeys(),
		identitiesNamed(func(in *sesapi.DeleteIdentityInput) []string { return []string{in.Identity} }))
}

// identitiesNamed names the identities an input addresses as the call's
// resources, so the journal names what a call acted on.
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

// errIdentityLimit answers a new identity past MaxIdentities in its
// account and Region in api (ses.md 5.1; the text is inferred).
func errIdentityLimit(api API) *awserr.Error {
	text := fmt.Sprintf(IdentityLimitText, MaxIdentities)
	if api == APIv2 {
		return sesv2api.ErrLimitExceededException(text)
	}
	return sesapi.ErrLimitExceededException(text)
}

// create appends a new identity of kind named name at now, created through
// v.api: Success with a StateChange from NotStarted when
// ses.verifiedIdentities lists it (no verification email, no timer), else
// Pending with its expiry timer armed. A creation to
// Pending is not a transition, so it journals none. It refuses the
// identity past MaxIdentities in v.
func (s *Service) create(st *state, v view, scope aws.Scope, kind sesapi.IdentityType, name string, now clock.Instant, tx *kernel.Tx) (*identity, *awserr.Error) {
	if st.identities(v) >= MaxIdentities {
		return nil, errIdentityLimit(v.api)
	}
	st.Identities = append(st.Identities, identity{Name: name, Kind: kind, API: v.api, Created: now})
	id := &st.Identities[len(st.Identities)-1]
	if s.opts.VerifiedIdentities.verifies(kind, name) {
		id.Status = statusSuccess{}
		tx.Record(scope, journal.StateChange{
			Resource: identityARN(scope, name),
			From:     journal.ResourceState(sesapi.VerificationStatusNotStarted), To: journal.ResourceState(sesapi.VerificationStatusSuccess),
		})
		return id, nil
	}
	s.pend(id, scope, now, tx)
	return id, nil
}

// createEmail creates email identity address through v.api as create does
// and, when it starts Pending, gives it a link token and records its
// verification email. It returns the blobs of a
// message the email evicted, to release once the write commits.
func (s *Service) createEmail(c *kernel.Call, st *state, v view, address string, now clock.Instant, tx *kernel.Tx) ([]blob.Ref, *awserr.Error) {
	id, err := s.create(st, v, c.Scope(), sesapi.IdentityTypeEmailAddress, address, now, tx)
	if err != nil {
		return nil, err
	}
	if _, verified := id.Status.(statusSuccess); verified {
		return nil, nil
	}
	id.LinkToken = s.linkToken()
	return st.record(s.verificationEmail(c, v.api, id.Name, id.LinkToken, now)), nil
}

// pend makes id Pending from now and arms its expiry timer, replacing any
// earlier one.
func (s *Service) pend(id *identity, scope aws.Scope, now clock.Instant, tx *kernel.Tx) {
	id.Status = statusPending{Since: now}
	tx.Arm(s.expiry, expiryTimerID(scope, id), id.deadline(now))
}

// verifyEmailIdentity starts verifying an email address: a new identity is
// Pending (ses.md 5.2), or Success when ses.verifiedIdentities lists it; a
// Pending, Failed or TemporaryFailure one restarts as Pending, its expiry
// counted again, and a Success one is left as it is (inferred, SES-09).
// Each start and restart records a verification email, always with the
// identity's one link token. An address a send would refuse
// answers VerifyAddressText (recorded for an address without "@", SES-04,
// ses.md 4.6).
func (s *Service) verifyEmailIdentity(ctx context.Context, c *kernel.Call, in *sesapi.VerifyEmailIdentityInput) (*sesapi.VerifyEmailIdentityOutput, *awserr.Error) {
	if _, r := parseAddress(in.EmailAddress); r != nil {
		return nil, sesapi.ErrInvalidParameterValue(fmt.Sprintf(VerifyAddressText, in.EmailAddress))
	}
	scope := c.Scope()
	v := s.view(APIv1)
	// The write returns the blobs of a message the verification email
	// evicted, released once it commits.
	released, err := kernel.Write(c, s.cells, scope, func(st *state, now clock.Instant, tx *kernel.Tx) ([]blob.Ref, *awserr.Error) {
		i := st.find(v, in.EmailAddress)
		if i < 0 {
			return s.createEmail(c, st, v, in.EmailAddress, now, tx)
		}
		id := &st.Identities[i]
		old := id.Status
		switch old.(type) {
		case statusSuccess:
			tx.Unchanged()
			return nil, nil
		case statusPending:
		default:
			tx.Record(scope, journal.StateChange{
				Resource: identityARN(scope, in.EmailAddress),
				From:     journal.ResourceState(old.v1()), To: journal.ResourceState(sesapi.VerificationStatusPending),
			})
		}
		s.pend(id, scope, now, tx)
		if id.LinkToken == "" {
			id.LinkToken = s.linkToken()
		}
		return st.record(s.verificationEmail(c, APIv1, id.Name, id.LinkToken, now)), nil
	})
	if err != nil {
		return nil, err
	}
	s.release(ctx, released...)
	return &sesapi.VerifyEmailIdentityOutput{}, nil
}

// linkToken draws a verification link token: 32 bytes as base64url without
// padding.
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

// verifyDomainIdentity starts verifying a domain: a new one is Pending with
// a token of 32 bytes as 44 base64 characters (ses.md 5.3), or Success when
// ses.verifiedIdentities lists it. A repeat returns the same token and
// changes nothing (inferred, SES-10), so Terraform reads a stable token
// (ses.md 9.2).
func (s *Service) verifyDomainIdentity(_ context.Context, c *kernel.Call, in *sesapi.VerifyDomainIdentityInput) (*sesapi.VerifyDomainIdentityOutput, *awserr.Error) {
	scope := c.Scope()
	v := s.view(APIv1)
	token, err := kernel.Write(c, s.cells, scope, func(st *state, now clock.Instant, tx *kernel.Tx) (string, *awserr.Error) {
		if i := st.find(v, in.Domain); i >= 0 {
			tx.Unchanged()
			return st.Identities[i].DomainToken, nil
		}
		id, err := s.create(st, v, scope, sesapi.IdentityTypeDomain, in.Domain, now, tx)
		if err != nil {
			return "", err
		}
		id.DomainToken = base64.StdEncoding.EncodeToString(s.ids.Draw(ids.KindSESDomainToken, 32))
		return id.DomainToken, nil
	})
	if err != nil {
		return nil, err
	}
	return &sesapi.VerifyDomainIdentityOutput{VerificationToken: token}, nil
}

// getIdentityVerificationAttributes answers each named identity's status,
// keyed by the request string byte for byte (ses.md 9.1), with a domain's
// token; an identity the store does not hold follows switch
// ses.verification.unknown-identity. An email identity carries no token
// (inferred, SES-08). More than MaxVerificationAttributesIdentities is
// refused (ses.md 5.3; the answer is inferred, SES-08).
func (s *Service) getIdentityVerificationAttributes(_ context.Context, c *kernel.Call, in *sesapi.GetIdentityVerificationAttributesInput) (*sesapi.GetIdentityVerificationAttributesOutput, *awserr.Error) {
	if len(in.Identities) > MaxVerificationAttributesIdentities {
		return nil, sesapi.ErrInvalidParameterValue(fmt.Sprintf(TooManyIdentitiesText, MaxVerificationAttributesIdentities))
	}
	known := map[string]sesapi.IdentityVerificationAttributes{}
	if cell, ok := s.cells.Lookup(c.Scope()); ok {
		v := s.view(APIv1)
		known = store.Read(cell, func(st *state) map[string]sesapi.IdentityVerificationAttributes {
			out := make(map[string]sesapi.IdentityVerificationAttributes, len(in.Identities))
			for _, name := range in.Identities {
				if i := st.find(v, name); i >= 0 {
					id := &st.Identities[i]
					a := sesapi.IdentityVerificationAttributes{VerificationStatus: id.Status.v1()}
					if id.DomainToken != "" {
						a.VerificationToken = new(id.DomainToken)
					}
					out[name] = a
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

// getIdentityNotificationAttributes answers each named identity the store
// holds, keyed by the request string byte for byte (ses.md 9.1), with no
// topics, forwarding on and headers off, since no notification setter is
// bound, as SES-08 recorded (ses.md 9.1). A topic is set only when one is
// configured, so the three topic elements stay out of the answer, as AWS
// leaves them out although the model marks them required (the overlay's
// optionalOutput). An unknown identity is omitted under either value of
// ses.verification.unknown-identity, as SES-08 recorded.
func (s *Service) getIdentityNotificationAttributes(_ context.Context, c *kernel.Call, in *sesapi.GetIdentityNotificationAttributesInput) (*sesapi.GetIdentityNotificationAttributesOutput, *awserr.Error) {
	out := &sesapi.GetIdentityNotificationAttributesOutput{NotificationAttributes: map[string]sesapi.IdentityNotificationAttributes{}}
	cell, ok := s.cells.Lookup(c.Scope())
	if !ok {
		return out, nil
	}
	v := s.view(APIv1)
	store.Read(cell, func(st *state) struct{} {
		for _, name := range in.Identities {
			if st.find(v, name) >= 0 {
				out.NotificationAttributes[name] = sesapi.IdentityNotificationAttributes{ForwardingEnabled: true}
			}
		}
		return struct{}{}
	})
	return out, nil
}

// listIdentities lists the identities of the caller's scope "regardless of
// verification status", narrowed by IdentityType (ses.md 5.3), in
// descending byte order of name (inferred from the one page SES-14
// recorded). MaxItems is 1 to ListIdentitiesMaxItems, its default the
// largest (aws-doc; the refusal's text is recorded, SES-29). NextToken is
// understudy's own: the decimal position of the next identity among those
// the filter keeps, base64url; an identity created or deleted between
// pages shifts the later ones by one.
func (s *Service) listIdentities(_ context.Context, c *kernel.Call, in *sesapi.ListIdentitiesInput) (*sesapi.ListIdentitiesOutput, *awserr.Error) {
	limit := ListIdentitiesMaxItems
	if in.MaxItems != nil {
		if *in.MaxItems < 1 || *in.MaxItems > ListIdentitiesMaxItems {
			return nil, sesapi.ErrInvalidParameterValue(fmt.Sprintf(ListIdentitiesMaxItemsText, *in.MaxItems, ListIdentitiesMaxItems))
		}
		limit = int(*in.MaxItems)
	}
	start := 0
	if in.NextToken != nil {
		n, ok := parseNextToken(*in.NextToken)
		if !ok {
			return nil, sesapi.ErrInvalidParameterValue("NextToken " + strconv.Quote(*in.NextToken) + " was not returned by ListIdentities")
		}
		start = n
	}
	var names []string
	if cell, ok := s.cells.Lookup(c.Scope()); ok {
		v := s.view(APIv1)
		names = store.Read(cell, func(st *state) []string {
			var out []string
			for i := range st.Identities {
				id := &st.Identities[i]
				if v.sees(id) && (in.IdentityType == nil || id.Kind == *in.IdentityType) {
					out = append(out, id.Name)
				}
			}
			return out
		})
		slices.SortFunc(names, func(a, b string) int { return strings.Compare(b, a) })
	}
	items, next := page(names, start, limit)
	if items == nil {
		items = []string{}
	}
	return &sesapi.ListIdentitiesOutput{Identities: items, NextToken: next}, nil
}

// parseNextToken reads a ListIdentities NextToken back to its position.
func parseNextToken(token string) (int, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(string(raw))
	return n, err == nil && n > 0
}

// deleteIdentity removes an identity and its expiry timer; one the store
// does not hold succeeds unchanged (inferred, SES-11). Sent messages stay.
func (s *Service) deleteIdentity(_ context.Context, c *kernel.Call, in *sesapi.DeleteIdentityInput) (*sesapi.DeleteIdentityOutput, *awserr.Error) {
	scope := c.Scope()
	v := s.view(APIv1)
	_, err := kernel.Write(c, s.cells, scope, func(st *state, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		i := st.find(v, in.Identity)
		if i < 0 {
			tx.Unchanged()
			return struct{}{}, nil
		}
		s.drop(st, scope, i, tx)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sesapi.DeleteIdentityOutput{}, nil
}

// drop removes identity i of st and disarms its expiry timer.
func (s *Service) drop(st *state, scope aws.Scope, i int, tx *kernel.Tx) {
	tx.Disarm(s.expiry, expiryTimerID(scope, &st.Identities[i]))
	st.Identities = append(st.Identities[:i:i], st.Identities[i+1:]...)
}

// page is the part of items a list call answers: limit of them from
// position start, and the NextToken naming the position after them when
// more remain (understudy's own token: the decimal position, base64url).
func page[T any](items []T, start, limit int) (part []T, next *string) {
	if start >= len(items) {
		return nil, nil
	}
	end := min(start+limit, len(items))
	if end < len(items) {
		return items[start:end], new(base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(end))))
	}
	return items[start:end], nil
}
