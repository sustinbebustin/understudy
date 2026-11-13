package ses

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// bindV2ConfigurationSets binds the v2 configuration-set operations over
// the store v1 shares (switch ses.store.shared-v1-v2). Each uses its
// primary action alone and touches the set's ARN, except
// CreateConfigurationSet with Tags, which also declares ses:TagResource;
// ListConfigurationSets, in both its bindings (ses.md 3.2), names no
// resource.
func (v *V2) bindV2ConfigurationSets(b sesv2api.Binder) {
	cov := registry.StoredOnly{Note: configurationSetCoverage}
	b.CreateConfigurationSet(v.createConfigurationSet, cov,
		kernel.WithActions(func(_ *kernel.Call, in *sesv2api.CreateConfigurationSetInput) []registry.IAMAction {
			if len(in.Tags) == 0 {
				return nil
			}
			return []registry.IAMAction{{Prefix: "ses", Name: "TagResource"}}
		}),
		v2ConditionKeys(),
		configurationSetNamed(func(in *sesv2api.CreateConfigurationSetInput) string { return in.ConfigurationSetName }))
	b.GetConfigurationSet(v.getConfigurationSet, cov,
		kernel.WithoutSecondaryActions(), v2ConditionKeys(),
		configurationSetNamed(func(in *sesv2api.GetConfigurationSetInput) string { return in.ConfigurationSetName }))
	b.DeleteConfigurationSet(v.deleteConfigurationSet, cov,
		kernel.WithoutSecondaryActions(), v2ConditionKeys(),
		configurationSetNamed(func(in *sesv2api.DeleteConfigurationSetInput) string { return in.ConfigurationSetName }))
	// The GET binding is a codegen/services additionalRoutes entry.
	b.ListConfigurationSets(v.listConfigurationSets, cov, kernel.WithoutSecondaryActions(), v2ConditionKeys())
}

// createConfigurationSet stores a new configuration set with its options
// and tags, checked as v1's (addConfigurationSet).
func (v *V2) createConfigurationSet(_ context.Context, c *kernel.Call, in *sesv2api.CreateConfigurationSetInput) (*sesv2api.CreateConfigurationSetOutput, *awserr.Error) {
	s := v.svc
	cs := configurationSet{
		Name: in.ConfigurationSetName,
		Options: setOptions{
			Tracking: in.TrackingOptions, Delivery: in.DeliveryOptions, Reputation: in.ReputationOptions,
			Sending: in.SendingOptions, Suppression: in.SuppressionOptions, Vdm: in.VdmOptions,
			Archiving: in.ArchivingOptions, MessageSecurity: in.MessageSecurityOptions,
		},
		Tags: mergeTags(nil, in.Tags),
		API:  APIv2,
	}
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *state, now clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		cs.Created = now
		if r := st.addConfigurationSet(s.view(APIv2), cs); r != nil {
			return struct{}{}, r.v2()
		}
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sesv2api.CreateConfigurationSetOutput{}, nil
}

// getConfigurationSet answers a set's name, the options it was created
// with, each omitted when it was not given, and its tags, [] when it has
// none. Whether AWS answers default options for ones not given is
// unrecorded.
func (v *V2) getConfigurationSet(_ context.Context, c *kernel.Call, in *sesv2api.GetConfigurationSetInput) (*sesv2api.GetConfigurationSetOutput, *awserr.Error) {
	var out *sesv2api.GetConfigurationSetOutput
	v.read(c.Scope(), func(st *state) {
		i := st.findConfigurationSet(v.svc.view(APIv2), in.ConfigurationSetName)
		if i < 0 {
			return
		}
		cs := &st.ConfigurationSets[i]
		o := cs.Options
		out = &sesv2api.GetConfigurationSetOutput{
			ConfigurationSetName: new(cs.Name),
			TrackingOptions:      o.Tracking, DeliveryOptions: o.Delivery, ReputationOptions: o.Reputation,
			SendingOptions: o.Sending, SuppressionOptions: o.Suppression, VdmOptions: o.Vdm,
			ArchivingOptions: o.Archiving, MessageSecurityOptions: o.MessageSecurity,
			Tags: tagList(cs.Tags),
		}
	})
	if out == nil {
		return nil, (&setRefusal{class: setMissing, name: in.ConfigurationSetName}).v2()
	}
	return out, nil
}

// deleteConfigurationSet removes a set; a missing one is
// NotFoundException, which the model declares (the text is inferred).
func (v *V2) deleteConfigurationSet(_ context.Context, c *kernel.Call, in *sesv2api.DeleteConfigurationSetInput) (*sesv2api.DeleteConfigurationSetOutput, *awserr.Error) {
	s := v.svc
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *state, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		if r := st.removeConfigurationSet(s.view(APIv2), in.ConfigurationSetName); r != nil {
			return struct{}{}, r.v2()
		}
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sesv2api.DeleteConfigurationSetOutput{}, nil
}

// listConfigurationSets lists the sets in v2's view in creation order,
// [] when there are none, narrowed by a CONFIGURATION_SET_NAME_CONTAINS
// Filter, a case-sensitive substring (inferred, as
// ListEmailIdentities' IDENTITY_NAME_CONTAINS). PageSize is 1 to
// ListConfigurationSetsMaxItems, its default (both inferred). NextToken is
// understudy's own, as ListEmailTemplates' is.
func (v *V2) listConfigurationSets(_ context.Context, c *kernel.Call, in *sesv2api.ListConfigurationSetsInput) (*sesv2api.ListConfigurationSetsOutput, *awserr.Error) {
	limit := ListConfigurationSetsMaxItems
	if in.PageSize != nil {
		if *in.PageSize < 1 || *in.PageSize > ListConfigurationSetsMaxItems {
			return nil, sesv2api.ErrBadRequestException(fmt.Sprintf(TemplatePageSizeText, ListConfigurationSetsMaxItems))
		}
		limit = int(*in.PageSize)
	}
	start := 0
	if in.NextToken != nil {
		n, ok := parseNextToken(*in.NextToken)
		if !ok {
			return nil, sesv2api.ErrBadRequestException("NextToken " + strconv.Quote(*in.NextToken) + " was not returned by ListConfigurationSets")
		}
		start = n
	}
	contains := in.Filter[sesv2api.ConfigurationSetFilterKeyConfigurationSetNameContains]
	var names []string
	v.read(c.Scope(), func(st *state) {
		for _, n := range st.configurationSetNames(v.svc.view(APIv2)) {
			if strings.Contains(n, contains) {
				names = append(names, n)
			}
		}
	})
	items, next := page(names, start, limit)
	if items == nil {
		items = []string{}
	}
	return &sesv2api.ListConfigurationSetsOutput{ConfigurationSets: items, NextToken: next}, nil
}
