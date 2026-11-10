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
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The v2 coverage notes (ses.md 11.1).
const (
	createEmailIdentityCoverage = "email identities start PENDING with DKIM NOT_STARTED; domains get three random Easy DKIM tokens and stay PENDING; " +
		"DkimSigningAttributes are stored, never used; verification as VerifyEmailIdentity; ConfigurationSetName answers NotImplemented"
	listEmailIdentitiesCoverage = "the GET /v2/email/identities binding sent by SDKs generated before 2026-09-29 is not routed and answers sesv2's unknown operation"
	getAccountCoverage          = "ProductionAccessEnabled and SendQuota from understudy state; SendingEnabled true, since account sending cannot be paused yet; " +
		"EnforcementStatus HEALTHY; DedicatedIpAutoWarmupEnabled true and SuppressedReasons BOUNCE and COMPLAINT, the recorded defaults (SES-15), " +
		"which nothing changes; no VDM, pricing plan or account details"
	v2TagsCoverage = "identity and template ARNs; an ARN with an empty account field names the caller's account, " +
		"as Terraform sends an identity's under skip_requesting_account_id (what AWS answers is unrecorded)"
)

// enforcementHealthy is GetAccount's EnforcementStatus: an emulated account
// is never on probation (ses.md 6.3).
const enforcementHealthy = "HEALTHY"

// bindV2 binds the v2 identity, tag and account operations over the
// Service store. Each uses its primary action alone, except
// CreateEmailIdentity with Tags, which also declares ses:TagResource.
func (v *V2) bindV2(b sesv2api.Binder) {
	b.CreateEmailIdentity(v.createEmailIdentity, registry.Partial{Note: createEmailIdentityCoverage},
		kernel.WithActions(func(_ *kernel.Call, in *sesv2api.CreateEmailIdentityInput) []registry.IAMAction {
			if len(in.Tags) == 0 {
				return nil
			}
			return []registry.IAMAction{{Prefix: "ses", Name: "TagResource"}}
		}),
		refusedConditionKeys(),
		identitiesNamed(func(in *sesv2api.CreateEmailIdentityInput) []string { return []string{in.EmailIdentity} }))
	b.GetEmailIdentity(v.getEmailIdentity, registry.Full{},
		kernel.WithoutSecondaryActions(), refusedConditionKeys(),
		identitiesNamed(func(in *sesv2api.GetEmailIdentityInput) []string { return []string{in.EmailIdentity} }))
	b.DeleteEmailIdentity(v.deleteEmailIdentity, registry.Full{},
		kernel.WithoutSecondaryActions(), refusedConditionKeys(),
		identitiesNamed(func(in *sesv2api.DeleteEmailIdentityInput) []string { return []string{in.EmailIdentity} }))
	b.ListEmailIdentities(v.listEmailIdentities, registry.Partial{Note: listEmailIdentitiesCoverage}, kernel.WithoutSecondaryActions(), refusedConditionKeys())
	b.TagResource(v.tagResource, registry.Partial{Note: v2TagsCoverage},
		kernel.WithoutSecondaryActions(), refusedConditionKeys(),
		resourceNamed(func(in *sesv2api.TagResourceInput) string { return in.ResourceArn }))
	b.UntagResource(v.untagResource, registry.Partial{Note: v2TagsCoverage},
		kernel.WithoutSecondaryActions(), refusedConditionKeys(),
		resourceNamed(func(in *sesv2api.UntagResourceInput) string { return in.ResourceArn }))
	b.ListTagsForResource(v.listTagsForResource, registry.Partial{Note: v2TagsCoverage},
		kernel.WithoutSecondaryActions(), refusedConditionKeys(),
		resourceNamed(func(in *sesv2api.ListTagsForResourceInput) string { return in.ResourceArn }))
	b.GetAccount(v.getAccount, registry.Partial{Note: getAccountCoverage}, kernel.WithoutSecondaryActions(), refusedConditionKeys())
}

// resourceNamed names a tag operation's ResourceArn as its resource: the
// identity's or template's full ARN when it names one
// of the caller's, else the ARN as given; an ARN that does not parse names
// none.
func resourceNamed[In any](arnOf func(*In) string) kernel.BindOption {
	return kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		if r, ok := taggableOf(c.Scope(), arnOf(in)); ok {
			return []aws.ARN{r.arn(c.Scope())}
		}
		arn, err := aws.ParseARN(arnOf(in))
		if err != nil {
			return nil
		}
		return []aws.ARN{arn}
	})
}

// errIdentityMissing answers a v2 call but GetEmailIdentity on an identity
// the caller's view does not hold (ses.md 5.4; recorded for
// DeleteEmailIdentity, SES-11, inferred for the tag operations).
func errIdentityMissing(name string) *awserr.Error {
	return sesv2api.ErrNotFoundException(fmt.Sprintf(IdentityMissingText, name))
}

// createConfigurationSetText refuses CreateEmailIdentity's
// ConfigurationSetName (invariant 3).
const createConfigurationSetText = "CreateEmailIdentity with ConfigurationSetName uses configuration sets, which understudy does not implement; " +
	"no identity was created. Leave ConfigurationSetName out. Run 'understudy coverage --service sesv2' for what is supported."

// createEmailIdentity creates an email address or domain identity through
// v2 (ses.md 5.4). An address starts as VerifyEmailIdentity
// starts one, its verification email recorded under sesv2; a domain gets
// three Easy DKIM tokens and a verification token. Both are Success at
// once when ses.verifiedIdentities lists them. Tags and the BYODKIM
// selector of DkimSigningAttributes are stored; nothing signs.
func (v *V2) createEmailIdentity(ctx context.Context, c *kernel.Call, in *sesv2api.CreateEmailIdentityInput) (*sesv2api.CreateEmailIdentityOutput, *awserr.Error) {
	if err := checkCreateEmailIdentity(in); err != nil {
		return nil, err
	}
	address := strings.Contains(in.EmailIdentity, "@")
	s := v.svc
	scope := c.Scope()
	vw := s.view(APIv2)
	type created struct {
		out      *sesv2api.CreateEmailIdentityOutput
		released []blob.Ref
	}
	got, err := kernel.Write(c, s.cells, scope, func(st *state, now clock.Instant, tx *kernel.Tx) (created, *awserr.Error) {
		if st.find(vw, in.EmailIdentity) >= 0 {
			return created{}, sesv2api.ErrAlreadyExistsException(fmt.Sprintf(IdentityExistsText, in.EmailIdentity))
		}
		var released []blob.Ref
		if address {
			r, err := s.createEmail(c, st, vw, in.EmailIdentity, now, tx)
			if err != nil {
				return created{}, err
			}
			released = r
		} else if err := s.createDomain(st, vw, scope, in, now, tx); err != nil {
			return created{}, err
		}
		id := &st.Identities[len(st.Identities)-1]
		id.Tags = mergeTags(nil, in.Tags)
		return created{
			out: &sesv2api.CreateEmailIdentityOutput{
				IdentityType:             new(id.v2Type()),
				VerifiedForSendingStatus: id.verified(),
				DkimAttributes:           id.dkim(),
			},
			released: released,
		}, nil
	})
	if err != nil {
		return nil, err
	}
	s.release(ctx, got.released...)
	return got.out, nil
}

// checkCreateEmailIdentity refuses what CreateEmailIdentity checks before
// state: ConfigurationSetName (NotImplemented), an address net/mail
// refuses, as VerifyEmailIdentity refuses it (ses.md 4.6, SES-04), and
// DkimSigningAttributes on an address, which the model documents as
// domains only (ses.md 5.4).
func checkCreateEmailIdentity(in *sesv2api.CreateEmailIdentityInput) *awserr.Error {
	if in.ConfigurationSetName != nil {
		return sesv2api.ErrNotImplemented(createConfigurationSetText)
	}
	if !strings.Contains(in.EmailIdentity, "@") {
		return nil
	}
	if _, r := parseAddress(in.EmailIdentity); r != nil {
		return r.v2()
	}
	if in.DkimSigningAttributes != nil {
		return sesv2api.ErrBadRequestException(DkimOnAddressText)
	}
	return nil
}

// createDomain creates the domain identity in names through v2: a
// verification token as VerifyDomainIdentity mints (ses.md 5.3), three
// Easy DKIM tokens, and any BYODKIM selector stored.
func (s *Service) createDomain(st *state, v view, scope aws.Scope, in *sesv2api.CreateEmailIdentityInput, now clock.Instant, tx *kernel.Tx) *awserr.Error {
	id, err := s.create(st, v, scope, sesapi.IdentityTypeDomain, in.EmailIdentity, now, tx)
	if err != nil {
		return err
	}
	id.DomainToken = base64.StdEncoding.EncodeToString(s.ids.Draw(ids.KindSESDomainToken, 32))
	id.DkimTokens = s.dkimTokens()
	if a := in.DkimSigningAttributes; a != nil && a.DomainSigningSelector != nil {
		id.DkimSigningAttributes = &dkimSigning{Selector: *a.DomainSigningSelector}
	}
	return nil
}

// dkimTokens draws a domain's three Easy DKIM tokens, each 32 lower-case
// letters and digits (inferred, SES-13).
func (s *Service) dkimTokens() []string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]string, 3)
	for i := range out {
		raw := s.ids.Draw(ids.KindSESDkimToken, 32)
		for j, b := range raw {
			raw[j] = alphabet[int(b)%len(alphabet)]
		}
		out[i] = string(raw)
	}
	return out
}

// getEmailIdentity answers an identity per the ses.md 5.4 table. Members
// whose raw form is unrecorded (MailFromAttributes, Policies,
// VerificationInfo, SES-13) are absent; no configuration set is ever
// attached.
func (v *V2) getEmailIdentity(_ context.Context, c *kernel.Call, in *sesv2api.GetEmailIdentityInput) (*sesv2api.GetEmailIdentityOutput, *awserr.Error) {
	var out *sesv2api.GetEmailIdentityOutput
	v.read(c.Scope(), func(st *state) {
		i := st.find(v.svc.view(APIv2), in.EmailIdentity)
		if i < 0 {
			return
		}
		id := &st.Identities[i]
		out = &sesv2api.GetEmailIdentityOutput{
			IdentityType:             new(id.v2Type()),
			FeedbackForwardingStatus: true,
			VerifiedForSendingStatus: id.verified(),
			DkimAttributes:           id.dkim(),
			Tags:                     tagList(id.Tags),
			VerificationStatus:       new(id.Status.v2()),
		}
	})
	if out == nil {
		return nil, sesv2api.ErrNotFoundException(fmt.Sprintf(GetIdentityMissingText, in.EmailIdentity))
	}
	return out, nil
}

// read runs fn on scope's state, if the scope has any.
func (v *V2) read(scope aws.Scope, fn func(st *state)) {
	if cell, ok := v.svc.cells.Lookup(scope); ok {
		store.Read(cell, func(st *state) struct{} { fn(st); return struct{}{} })
	}
}

// deleteEmailIdentity removes an identity and its expiry timer; a missing
// one is NotFoundException, which Terraform reads as deleted (ses.md 5.4).
// Sent messages stay.
func (v *V2) deleteEmailIdentity(_ context.Context, c *kernel.Call, in *sesv2api.DeleteEmailIdentityInput) (*sesv2api.DeleteEmailIdentityOutput, *awserr.Error) {
	s := v.svc
	scope := c.Scope()
	vw := s.view(APIv2)
	_, err := kernel.Write(c, s.cells, scope, func(st *state, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		i := st.find(vw, in.EmailIdentity)
		if i < 0 {
			return struct{}{}, errIdentityMissing(in.EmailIdentity)
		}
		s.drop(st, scope, i, tx)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sesv2api.DeleteEmailIdentityOutput{}, nil
}

// listEmailIdentities lists verified and unverified identities (ses.md
// 5.4) in descending byte order of name, or ascending when a Filter is
// given (each inferred from the one page SES-14 recorded), narrowed by
// every Filter key given: IDENTITY_NAME_CONTAINS a
// case-sensitive substring, IDENTITY_TYPE and VERIFICATION_STATUS exact.
// PageSize 0 or absent is
// ListEmailIdentitiesDefaultPageSize, at most
// ListEmailIdentitiesMaxPageSize (model; the default and the refusal are
// inferred, SES-29). NextToken is understudy's own, as ListIdentities'.
func (v *V2) listEmailIdentities(_ context.Context, c *kernel.Call, in *sesv2api.ListEmailIdentitiesInput) (*sesv2api.ListEmailIdentitiesOutput, *awserr.Error) {
	limit := ListEmailIdentitiesDefaultPageSize
	if in.PageSize != nil {
		switch n := int(*in.PageSize); {
		case n < 0 || n > ListEmailIdentitiesMaxPageSize:
			return nil, sesv2api.ErrBadRequestException(fmt.Sprintf(PageSizeText, ListEmailIdentitiesMaxPageSize))
		case n > 0:
			limit = n
		}
	}
	start := 0
	if in.NextToken != nil {
		n, ok := parseNextToken(*in.NextToken)
		if !ok {
			return nil, sesv2api.ErrBadRequestException("NextToken " + strconv.Quote(*in.NextToken) + " was not returned by ListEmailIdentities")
		}
		start = n
	}
	var infos []sesv2api.IdentityInfo
	vw := v.svc.view(APIv2)
	v.read(c.Scope(), func(st *state) {
		for i := range st.Identities {
			id := &st.Identities[i]
			if vw.sees(id) && id.matches(in.Filter) {
				infos = append(infos, sesv2api.IdentityInfo{
					IdentityType:       new(id.v2Type()),
					IdentityName:       new(id.Name),
					SendingEnabled:     id.verified(),
					VerificationStatus: new(id.Status.v2()),
				})
			}
		}
	})
	slices.SortFunc(infos, func(a, b sesv2api.IdentityInfo) int {
		if len(in.Filter) > 0 {
			return strings.Compare(*a.IdentityName, *b.IdentityName)
		}
		return strings.Compare(*b.IdentityName, *a.IdentityName)
	})
	items, next := page(infos, start, limit)
	if items == nil {
		items = []sesv2api.IdentityInfo{}
	}
	return &sesv2api.ListEmailIdentitiesOutput{EmailIdentities: items, NextToken: next}, nil
}

// matches reports whether id passes every key of a ListEmailIdentities
// Filter.
func (id *identity) matches(filter map[sesv2api.IdentityFilterKey]string) bool {
	for key, want := range filter {
		switch key {
		case sesv2api.IdentityFilterKeyIdentityNameContains:
			if !strings.Contains(id.Name, want) {
				return false
			}
		case sesv2api.IdentityFilterKeyIdentityType:
			if string(id.v2Type()) != want {
				return false
			}
		case sesv2api.IdentityFilterKeyVerificationStatus:
			if string(id.Status.v2()) != want {
				return false
			}
		}
	}
	return true
}

// tagged runs fn on the tags of the identity or template resourceARN names
// in the caller's view, in a write; a missing one, or any other ARN, is
// NotFoundException naming its resource.
func (v *V2) tagged(c *kernel.Call, resourceARN string, fn func(tags *[]tag, tx *kernel.Tx)) *awserr.Error {
	s := v.svc
	scope := c.Scope()
	r, ok := taggableOf(scope, resourceARN)
	if !ok {
		return errIdentityMissing(arnResourceName(resourceARN))
	}
	vw := s.view(APIv2)
	_, err := kernel.Write(c, s.cells, scope, func(st *state, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if r.template {
			i := st.findTemplate(vw, r.name)
			if i < 0 {
				return struct{}{}, errEmailTemplateMissing(r.name)
			}
			fn(&st.Templates[i].Tags, tx)
			return struct{}{}, nil
		}
		i := st.find(vw, r.name)
		if i < 0 {
			return struct{}{}, errIdentityMissing(r.name)
		}
		fn(&st.Identities[i].Tags, tx)
		return struct{}{}, nil
	})
	return err
}

// taggable is what a tag operation's ResourceArn names in the caller's
// scope: an identity, or a template when template is set.
type taggable struct {
	template bool
	name     string
}

// arn is r's full ARN in scope.
func (r taggable) arn(scope aws.Scope) aws.ARN {
	if r.template {
		return templateARN(scope, r.name)
	}
	return identityARN(scope, r.name)
}

// taggableOf reads an SES identity or template ARN in scope. An empty
// account field is read as the caller's: Terraform builds an identity's
// ARN from its own account, which is empty under
// skip_requesting_account_id, the setting understudy's Terraform guide
// needs at an IP endpoint (apigateway-http.md 13.6 explains why). What AWS
// answers for such an ARN is unrecorded.
func taggableOf(scope aws.Scope, resourceARN string) (taggable, bool) {
	arn, err := aws.ParseARN(resourceARN)
	if err != nil || arn.Service != "ses" || arn.Partition != scope.Partition.String() ||
		arn.Region != scope.Region.String() || (arn.Account != "" && arn.Account != scope.Account.String()) {
		return taggable{}, false
	}
	if name, ok := strings.CutPrefix(arn.Resource, "identity/"); ok {
		return taggable{name: name}, true
	}
	if name, ok := strings.CutPrefix(arn.Resource, "template/"); ok {
		return taggable{template: true, name: name}, true
	}
	return taggable{}, false
}

// arnResourceName is the name a NotFoundException names for an ARN that is
// no identity of the caller's: the part of its resource after the type, or
// the whole string when it does not parse.
func arnResourceName(resourceARN string) string {
	arn, err := aws.ParseARN(resourceARN)
	if err != nil {
		return resourceARN
	}
	if _, name, ok := strings.Cut(arn.Resource, "/"); ok {
		return name
	}
	return arn.Resource
}

// tagResource sets tags on an identity or template, a key already set
// taking the new value (ses.md 5.4).
func (v *V2) tagResource(_ context.Context, c *kernel.Call, in *sesv2api.TagResourceInput) (*sesv2api.TagResourceOutput, *awserr.Error) {
	err := v.tagged(c, in.ResourceArn, func(tags *[]tag, _ *kernel.Tx) { *tags = mergeTags(*tags, in.Tags) })
	if err != nil {
		return nil, err
	}
	return &sesv2api.TagResourceOutput{}, nil
}

// untagResource removes the named tag keys from an identity or template; a
// key it does not carry is ignored.
func (v *V2) untagResource(_ context.Context, c *kernel.Call, in *sesv2api.UntagResourceInput) (*sesv2api.UntagResourceOutput, *awserr.Error) {
	err := v.tagged(c, in.ResourceArn, func(tags *[]tag, tx *kernel.Tx) {
		before := len(*tags)
		*tags = slices.DeleteFunc(*tags, func(t tag) bool { return slices.Contains(in.TagKeys, t.Key) })
		if len(*tags) == before {
			tx.Unchanged()
		}
	})
	if err != nil {
		return nil, err
	}
	return &sesv2api.UntagResourceOutput{}, nil
}

// listTagsForResource answers an identity's or template's tags, which
// Terraform reads for an identity's tags (ses.md 9.3).
func (v *V2) listTagsForResource(_ context.Context, c *kernel.Call, in *sesv2api.ListTagsForResourceInput) (*sesv2api.ListTagsForResourceOutput, *awserr.Error) {
	var tags []sesv2api.Tag
	err := v.tagged(c, in.ResourceArn, func(stored *[]tag, tx *kernel.Tx) {
		tx.Unchanged()
		tags = tagList(*stored)
	})
	if err != nil {
		return nil, err
	}
	return &sesv2api.ListTagsForResourceOutput{Tags: tags}, nil
}

// getAccount answers the sandbox state and the quota GetSendQuota answers
// (ses.md 6.3). Sending cannot be paused yet, so
// SendingEnabled is always true. Automatic dedicated-IP warm-up and the
// account suppression list for bounces and complaints are on, the defaults
// SES-15 recorded for a sandbox account; no setter for either is bound.
func (v *V2) getAccount(_ context.Context, c *kernel.Call, _ *sesv2api.GetAccountInput) (*sesv2api.GetAccountOutput, *awserr.Error) {
	s := v.svc
	q := &sesv2api.SendQuota{Max24HourSend: SandboxMax24HourSend, MaxSendRate: SandboxMaxSendRate}
	if s.opts.ProductionAccess {
		q.Max24HourSend, q.MaxSendRate = ProductionMax24HourSend, ProductionMaxSendRate
	}
	v.read(c.Scope(), func(st *state) { q.SentLast24Hours = float64(st.sentLast24Hours(c.Admitted())) })
	return &sesv2api.GetAccountOutput{
		ProductionAccessEnabled: s.opts.ProductionAccess,
		SendQuota:               q,
		SendingEnabled:          true,
		EnforcementStatus:       new(enforcementHealthy),
		// The defaults SES-15 recorded.
		DedicatedIpAutoWarmupEnabled: true,
		SuppressionAttributes: &sesv2api.SuppressionAttributes{SuppressedReasons: []sesv2api.SuppressionListReason{
			sesv2api.SuppressionListReasonBounce, sesv2api.SuppressionListReasonComplaint,
		}},
	}, nil
}

// mergeTags sets add on tags: a new key is appended, an existing one takes
// the new value.
func mergeTags(tags []tag, add []sesv2api.Tag) []tag {
	for _, t := range add {
		if i := slices.IndexFunc(tags, func(o tag) bool { return o.Key == t.Key }); i >= 0 {
			tags[i].Value = t.Value
			continue
		}
		tags = append(tags, tag{Key: t.Key, Value: t.Value})
	}
	return tags
}

// tagList is tags as v2 answers them, an empty list when there are none.
func tagList(tags []tag) []sesv2api.Tag {
	out := make([]sesv2api.Tag, 0, len(tags))
	for _, t := range tags {
		out = append(out, sesv2api.Tag{Key: t.Key, Value: t.Value})
	}
	return out
}

// v2Type is id's kind as v2 IdentityType spells it.
func (id *identity) v2Type() sesv2api.IdentityType {
	if id.Kind == sesapi.IdentityTypeDomain {
		return sesv2api.IdentityTypeDomain
	}
	return sesv2api.IdentityTypeEmailAddress
}

// verified is VerifiedForSendingStatus and IdentityInfo SendingEnabled:
// the identity is Success.
func (id *identity) verified() bool {
	_, ok := id.Status.(statusSuccess)
	return ok
}

// dkim is id's DkimAttributes per the ses.md 5.4 table: with Easy DKIM
// tokens (a domain v2 created) PENDING, three tokens, RSA_2048_BIT both and
// the creation time as the key's; otherwise the address column,
// NOT_STARTED with no tokens or current key and RSA_1024_BIT next. What AWS
// answers for a domain v1 created is UNVERIFIED (SES-13); understudy
// answers the address column. A stored BYODKIM selector makes the origin
// EXTERNAL.
func (id *identity) dkim() *sesv2api.DkimAttributes {
	origin := sesv2api.DkimSigningAttributesOriginAwsSes
	if id.DkimSigningAttributes != nil && id.DkimSigningAttributes.Selector != "" {
		origin = sesv2api.DkimSigningAttributesOriginExternal
	}
	if len(id.DkimTokens) == 0 {
		return &sesv2api.DkimAttributes{
			Status:                  new(sesv2api.DkimStatusNotStarted),
			NextSigningKeyLength:    new(sesv2api.DkimSigningKeyLengthRsa1024Bit),
			SigningAttributesOrigin: &origin,
		}
	}
	return &sesv2api.DkimAttributes{
		SigningEnabled:             true,
		Status:                     new(sesv2api.DkimStatusPending),
		Tokens:                     slices.Clone(id.DkimTokens),
		SigningAttributesOrigin:    &origin,
		NextSigningKeyLength:       new(sesv2api.DkimSigningKeyLengthRsa2048Bit),
		CurrentSigningKeyLength:    new(sesv2api.DkimSigningKeyLengthRsa2048Bit),
		LastKeyGenerationTimestamp: new(id.Created.Wire()),
	}
}
