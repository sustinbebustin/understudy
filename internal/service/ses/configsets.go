package ses

import (
	"context"
	"fmt"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// configurationSetCoverage is the coverage note of the configuration-set
// operations of both APIs (ses.md 11.1, second slice).
const configurationSetCoverage = "stored and checked by name on sends; no event destinations, tracking, reputation, delivery or suppression options take effect"

// configurationSet is one configuration set, v1's or v2's (ses.md 8). A
// send names it and records it; nothing it configures takes effect
// (ses.md 11.8).
type configurationSet struct {
	Name string `json:"name"`
	// Options are the option members v2 CreateConfigurationSet gave,
	// echoed by GetConfigurationSet.
	Options setOptions `json:"options,omitzero"`
	// Tags are the set's v2 tags in the order they were first set; v1
	// neither sets nor answers them.
	Tags []tag `json:"tags,omitempty"`
	// API is the API whose call created the set, which switch
	// ses.store.shared-v1-v2 reads when it keeps the APIs apart.
	API     API           `json:"api"`
	Created clock.Instant `json:"created"`
}

// setOptions are the option members of v2 CreateConfigurationSet as given,
// each nil when absent. They are stored only to be echoed; no operation
// changes one after creation, so a read may share them with the state.
type setOptions struct {
	Tracking        *sesv2api.TrackingOptions        `json:"tracking,omitempty"`
	Delivery        *sesv2api.DeliveryOptions        `json:"delivery,omitempty"`
	Reputation      *sesv2api.ReputationOptions      `json:"reputation,omitempty"`
	Sending         *sesv2api.SendingOptions         `json:"sending,omitempty"`
	Suppression     *sesv2api.SuppressionOptions     `json:"suppression,omitempty"`
	Vdm             *sesv2api.VdmOptions             `json:"vdm,omitempty"`
	Archiving       *sesv2api.ArchivingOptions       `json:"archiving,omitempty"`
	MessageSecurity *sesv2api.MessageSecurityOptions `json:"messageSecurity,omitempty"`
}

// configurationSetARN is the ARN of configuration set name in scope,
// arn:aws:ses:<region>:<account>:configuration-set/<name>, the form
// Terraform builds (ses.md 11.7).
func configurationSetARN(scope aws.Scope, name string) aws.ARN {
	return aws.ARN{
		Partition: scope.Partition.String(),
		Service:   "ses",
		Region:    scope.Region.String(),
		Account:   scope.Account.String(),
		Resource:  "configuration-set/" + name,
	}
}

// findConfigurationSet is the index of the configuration set in v named
// name, byte for byte, or -1.
func (s *state) findConfigurationSet(v view, name string) int {
	for i := range s.ConfigurationSets {
		if s.ConfigurationSets[i].Name == name && v.owns(s.ConfigurationSets[i].API) {
			return i
		}
	}
	return -1
}

// configurationSets is the number of configuration sets in v, which
// MaxConfigurationSets bounds.
func (s *state) configurationSets(v view) int {
	n := 0
	for i := range s.ConfigurationSets {
		if v.owns(s.ConfigurationSets[i].API) {
			n++
		}
	}
	return n
}

// configurationSetNames is the names of the configuration sets in v, in
// creation order.
func (s *state) configurationSetNames(v view) []string {
	var out []string
	for i := range s.ConfigurationSets {
		if v.owns(s.ConfigurationSets[i].API) {
			out = append(out, s.ConfigurationSets[i].Name)
		}
	}
	return out
}

// setRefusalClass is how a configuration-set operation fails, whichever
// API it came through.
type setRefusalClass uint8

const (
	// setInvalidName is a name breaking the ses.md 8 rules: v1
	// InvalidConfigurationSet, v2 BadRequestException.
	setInvalidName setRefusalClass = iota
	// setExists is a duplicate name: v1 ConfigurationSetAlreadyExists, v2
	// AlreadyExistsException.
	setExists
	// setLimit is a set past MaxConfigurationSets: LimitExceeded in both.
	setLimit
	// setMissing is a set the view does not hold: v1
	// ConfigurationSetDoesNotExist, v2 NotFoundException.
	setMissing
)

// setRefusal is a failed configuration-set check, answered in either API.
type setRefusal struct {
	class setRefusalClass
	name  string
}

// v1 is r as a v1 error. A missing set carries no ConfigurationSetName
// element, as SES-17 recorded on a send.
func (r *setRefusal) v1() *awserr.Error {
	switch r.class {
	case setInvalidName:
		return sesapi.ErrInvalidConfigurationSetException(fmt.Sprintf(ConfigurationSetNameText, r.name))
	case setExists:
		return sesapi.ErrConfigurationSetAlreadyExistsExceptionMembers(fmt.Sprintf(ConfigurationSetExistsText, r.name),
			sesapi.ConfigurationSetAlreadyExistsException{ConfigurationSetName: new(r.name)})
	case setLimit:
		return sesapi.ErrLimitExceededException(fmt.Sprintf(ConfigurationSetLimitText, MaxConfigurationSets))
	case setMissing:
	}
	return sesapi.ErrConfigurationSetDoesNotExistException(fmt.Sprintf(ConfigurationSetMissingText, r.name))
}

// v2 is r as a v2 error, with v1's texts (inferred).
func (r *setRefusal) v2() *awserr.Error {
	switch r.class {
	case setInvalidName:
		return sesv2api.ErrBadRequestException(fmt.Sprintf(ConfigurationSetNameText, r.name))
	case setExists:
		return sesv2api.ErrAlreadyExistsException(fmt.Sprintf(ConfigurationSetExistsText, r.name))
	case setLimit:
		return sesv2api.ErrLimitExceededException(fmt.Sprintf(ConfigurationSetLimitText, MaxConfigurationSets))
	case setMissing:
	}
	return sesv2api.ErrNotFoundException(fmt.Sprintf(ConfigurationSetMissingText, r.name))
}

// validConfigurationSetName reports whether name has 1 to
// ConfigurationSetNameMaxLength characters, each an ASCII letter, digit,
// "-" or "_" (ses.md 8).
func validConfigurationSetName(name string) bool {
	return name != "" && len(name) <= ConfigurationSetNameMaxLength && onlyTagChars(name, "")
}

// addConfigurationSet stores cs in st, checked in order: the name rules,
// a free name, then MaxConfigurationSets (ses.md 8; the order is
// inferred).
func (s *state) addConfigurationSet(v view, cs configurationSet) *setRefusal {
	switch {
	case !validConfigurationSetName(cs.Name):
		return &setRefusal{class: setInvalidName, name: cs.Name}
	case s.findConfigurationSet(v, cs.Name) >= 0:
		return &setRefusal{class: setExists, name: cs.Name}
	case s.configurationSets(v) >= MaxConfigurationSets:
		return &setRefusal{class: setLimit}
	}
	s.ConfigurationSets = append(s.ConfigurationSets, cs)
	return nil
}

// removeConfigurationSet deletes the set in v named name.
func (s *state) removeConfigurationSet(v view, name string) *setRefusal {
	i := s.findConfigurationSet(v, name)
	if i < 0 {
		return &setRefusal{class: setMissing, name: name}
	}
	s.ConfigurationSets = append(s.ConfigurationSets[:i:i], s.ConfigurationSets[i+1:]...)
	return nil
}

// configurationSetNamed names the configuration set an input addresses as
// the call's resource.
func configurationSetNamed[In any](nameOf func(*In) string) kernel.BindOption {
	return kernel.WithResources(func(c *kernel.Call, in *In) []aws.ARN {
		return []aws.ARN{configurationSetARN(c.Scope(), nameOf(in))}
	})
}

// bindConfigurationSets binds the v1 configuration-set operations over the
// store v2 shares (switch ses.store.shared-v1-v2). Each uses its primary
// action alone and touches the set's ARN; ListConfigurationSets names no
// resource.
func (s *Service) bindConfigurationSets(b sesapi.Binder) {
	cov := registry.StoredOnly{Note: configurationSetCoverage}
	b.CreateConfigurationSet(s.createConfigurationSet, cov,
		kernel.WithoutSecondaryActions(), v1ConditionKeys(),
		configurationSetNamed(func(in *sesapi.CreateConfigurationSetInput) string { return in.ConfigurationSet.Name }))
	b.DescribeConfigurationSet(s.describeConfigurationSet, cov,
		kernel.WithoutSecondaryActions(), v1ConditionKeys(),
		configurationSetNamed(func(in *sesapi.DescribeConfigurationSetInput) string { return in.ConfigurationSetName }))
	b.DeleteConfigurationSet(s.deleteConfigurationSet, cov,
		kernel.WithoutSecondaryActions(), v1ConditionKeys(),
		configurationSetNamed(func(in *sesapi.DeleteConfigurationSetInput) string { return in.ConfigurationSetName }))
	b.ListConfigurationSets(s.listConfigurationSets, cov, kernel.WithoutSecondaryActions(), v1ConditionKeys())
}

// createConfigurationSet stores a new configuration set (addConfigurationSet).
func (s *Service) createConfigurationSet(_ context.Context, c *kernel.Call, in *sesapi.CreateConfigurationSetInput) (*sesapi.CreateConfigurationSetOutput, *awserr.Error) {
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *state, now clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		if r := st.addConfigurationSet(s.view(APIv1), configurationSet{Name: in.ConfigurationSet.Name, API: APIv1, Created: now}); r != nil {
			return struct{}{}, r.v1()
		}
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sesapi.CreateConfigurationSetOutput{}, nil
}

// describeConfigurationSet answers a set's name and, of the attributes
// asked for, what v1 can express of the options v2 stored: tracking's
// redirect domain, delivery's TLS policy, and reputation with v2's sending
// option. A set v1 created holds none, so only its name answers; whether
// AWS then answers default options is unrecorded. No set has event
// destinations, which stay unbound.
func (s *Service) describeConfigurationSet(_ context.Context, c *kernel.Call, in *sesapi.DescribeConfigurationSetInput) (*sesapi.DescribeConfigurationSetOutput, *awserr.Error) {
	var out *sesapi.DescribeConfigurationSetOutput
	if cell, ok := s.cells.Lookup(c.Scope()); ok {
		out = store.Read(cell, func(st *state) *sesapi.DescribeConfigurationSetOutput {
			i := st.findConfigurationSet(s.view(APIv1), in.ConfigurationSetName)
			if i < 0 {
				return nil
			}
			cs := &st.ConfigurationSets[i]
			return describe(cs.Name, cs.Options, in.ConfigurationSetAttributeNames)
		})
	}
	if out == nil {
		return nil, (&setRefusal{class: setMissing, name: in.ConfigurationSetName}).v1()
	}
	return out, nil
}

// describe is DescribeConfigurationSet's answer for a set named name with
// options o, given the attributes asked for.
func describe(name string, o setOptions, asked []sesapi.ConfigurationSetAttribute) *sesapi.DescribeConfigurationSetOutput {
	out := &sesapi.DescribeConfigurationSetOutput{ConfigurationSet: &sesapi.ConfigurationSet{Name: name}}
	for _, a := range asked {
		switch a {
		case sesapi.ConfigurationSetAttributeTrackingOptions:
			out.TrackingOptions = o.v1Tracking()
		case sesapi.ConfigurationSetAttributeDeliveryOptions:
			out.DeliveryOptions = o.v1Delivery()
		case sesapi.ConfigurationSetAttributeReputationOptions:
			out.ReputationOptions = o.v1Reputation()
		case sesapi.ConfigurationSetAttributeEventDestinations:
		}
	}
	return out
}

// v1Tracking is o's tracking option as v1 has it, nil when none is stored.
func (o setOptions) v1Tracking() *sesapi.TrackingOptions {
	if o.Tracking == nil {
		return nil
	}
	return &sesapi.TrackingOptions{CustomRedirectDomain: new(o.Tracking.CustomRedirectDomain)}
}

// v1Delivery is o's TLS policy as v1 spells it, in title case where v2
// uses upper case; nil when none is stored.
func (o setOptions) v1Delivery() *sesapi.DeliveryOptions {
	if o.Delivery == nil || o.Delivery.TlsPolicy == nil {
		return nil
	}
	p := sesapi.TlsPolicyOptional
	if *o.Delivery.TlsPolicy == sesv2api.TlsPolicyRequire {
		p = sesapi.TlsPolicyRequire
	}
	return &sesapi.DeliveryOptions{TlsPolicy: &p}
}

// v1Reputation is o's reputation and sending options as v1's one
// ReputationOptions, the unstored one at its default; nil when neither is
// stored.
func (o setOptions) v1Reputation() *sesapi.ReputationOptions {
	if o.Reputation == nil && o.Sending == nil {
		return nil
	}
	r := &sesapi.ReputationOptions{SendingEnabled: o.Sending == nil || o.Sending.SendingEnabled}
	if o.Reputation != nil {
		r.ReputationMetricsEnabled = o.Reputation.ReputationMetricsEnabled
		r.LastFreshStart = o.Reputation.LastFreshStart
	}
	return r
}

// deleteConfigurationSet removes a set; a missing one is
// ConfigurationSetDoesNotExist (third-party-recorded, ses.md 8). Messages
// sent with it keep its name.
func (s *Service) deleteConfigurationSet(_ context.Context, c *kernel.Call, in *sesapi.DeleteConfigurationSetInput) (*sesapi.DeleteConfigurationSetOutput, *awserr.Error) {
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *state, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		if r := st.removeConfigurationSet(s.view(APIv1), in.ConfigurationSetName); r != nil {
			return struct{}{}, r.v1()
		}
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sesapi.DeleteConfigurationSetOutput{}, nil
}

// listConfigurationSets lists the sets in v1's view in creation order, []
// when there are none. MaxItems defaults to ListConfigurationSetsMaxItems
// and a larger one is clamped (model); below 1 is refused (the answer is
// inferred). NextToken is understudy's own, as ListTemplates' is.
func (s *Service) listConfigurationSets(_ context.Context, c *kernel.Call, in *sesapi.ListConfigurationSetsInput) (*sesapi.ListConfigurationSetsOutput, *awserr.Error) {
	limit := ListConfigurationSetsMaxItems
	if in.MaxItems != nil {
		if *in.MaxItems < 1 {
			return nil, sesapi.ErrInvalidParameterValue(fmt.Sprintf(MaxItemsText, ListConfigurationSetsMaxItems))
		}
		limit = min(int(*in.MaxItems), ListConfigurationSetsMaxItems)
	}
	start := 0
	if in.NextToken != nil {
		n, ok := parseNextToken(*in.NextToken)
		if !ok {
			return nil, sesapi.ErrInvalidParameterValue("NextToken " + strconv.Quote(*in.NextToken) + " was not returned by ListConfigurationSets")
		}
		start = n
	}
	var names []string
	if cell, ok := s.cells.Lookup(c.Scope()); ok {
		names = store.Read(cell, func(st *state) []string { return st.configurationSetNames(s.view(APIv1)) })
	}
	part, next := page(names, start, limit)
	items := make([]sesapi.ConfigurationSet, 0, len(part))
	for _, n := range part {
		items = append(items, sesapi.ConfigurationSet{Name: n})
	}
	return &sesapi.ListConfigurationSetsOutput{ConfigurationSets: items, NextToken: next}, nil
}

// checkConfigurationSet runs CheckConfigurationSet: a send naming a
// configuration set needs one in v (recorded, SES-17 step 02). It returns
// the set's ARN, which the send touches, or the refusal.
func (s *state) checkConfigurationSet(v view, scope aws.Scope, name string) (aws.ARN, *refusal) {
	if s.findConfigurationSet(v, name) < 0 {
		return aws.ARN{}, &refusal{class: refusalConfigurationSetMissing, text: fmt.Sprintf(ConfigurationSetMissingText, name)}
	}
	return configurationSetARN(scope, name), nil
}
