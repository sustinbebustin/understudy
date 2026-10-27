package ssm

import (
	"context"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/store"
)

// defaultDataType is the DataType of a parameter written without one
// (ssm-parameters.md 4.3).
const defaultDataType = "text"

// describePageSize is how many entries DescribeParameters answers without
// MaxResults. UNVERIFIED (SSM-20), ssm-parameters.md 7.10.
const describePageSize = 50

// errAlreadyExists is PutParameter's answer for an existing name without
// Overwrite (ssm-parameters.md 12.4).
func errAlreadyExists() *awserr.Error {
	return ssmapi.ErrParameterAlreadyExists("The parameter already exists. To overwrite this value, set the overwrite option in the request to true.")
}

// errNotFound is ParameterNotFound with the empty message AWS sends
// (ssm-parameters.md 2.2; localstack@8b9a79f
// tests/aws/services/cloudformation/resources/test_ssm.snapshot.json:L105-L114).
func errNotFound() *awserr.Error { return ssmapi.ErrParameterNotFound("") }

// readState runs fn on scope's state, or on an empty state when the scope
// has no cell yet, without creating one.
func readState[R any](s *Service, scope aws.Scope, fn func(st *State) R) R {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		st := newState(scope)
		return fn(&st)
	}
	return store.Read(cell, fn)
}

// lookup copies out the parameter canonical of scope.
func (s *Service) lookup(scope aws.Scope, canonical string) (Parameter, bool) {
	type found struct {
		p  Parameter
		ok bool
	}
	f := readState(s, scope, func(st *State) found { p, ok := st.Parameters[canonical]; return found{p, ok} })
	return f.p, f.ok
}

// latest is the parameter's newest version. A stored parameter always has
// one.
func (p *Parameter) latest() Version { return p.Versions[len(p.Versions)-1] }

// plainText is the text of a String or StringList value. Not implemented
// yet: a SecureString value, which PutParameter does not store yet.
func plainText(v value) (string, *awserr.Error) {
	switch v := v.(type) {
	case plainValue:
		return v.Text, nil
	case sealedValue:
	}
	return "", awserr.Internalf(&sealedReadError{})
}

// sealedReadError is a read of a SecureString value before reads decrypt.
type sealedReadError struct{}

func (*sealedReadError) Error() string {
	return "ssm: a SecureString value was stored, but reading one is not implemented yet"
}

// putParameter creates a String parameter. Not implemented yet: overwrite,
// the other types, tiers, data types, AllowedPattern, tags and policies,
// each of which answers NotImplemented naming it.
func (s *Service) putParameter(_ context.Context, c *kernel.Call, in *ssmapi.PutParameterInput) (*ssmapi.PutParameterOutput, *awserr.Error) {
	const op = "PutParameter"
	if err := checkReserved(op, in.Name); err != nil {
		return nil, err
	}
	scope := c.Scope()
	n, err := parseName(in.Name, scope)
	if err != nil {
		return nil, err
	}
	switch {
	case in.Policies != nil:
		return nil, errFeatureNotYet(op, "Policies")
	case len(in.Tags) > 0:
		return nil, errFeatureNotYet(op, "Tags")
	case in.Type == nil || *in.Type != ssmapi.ParameterTypeString:
		return nil, errFeatureNotYet(op, "a Type other than String")
	case in.KeyId != nil:
		return nil, errFeatureNotYet(op, "KeyId")
	case in.Tier != nil && *in.Tier != ssmapi.ParameterTierStandard:
		return nil, errFeatureNotYet(op, "a Tier other than Standard")
	case in.DataType != nil && *in.DataType != defaultDataType:
		return nil, errFeatureNotYet(op, "a DataType other than text")
	case in.AllowedPattern != nil && *in.AllowedPattern != "":
		// Terraform sends AllowedPattern "" when none is configured
		// (ssm-parameters.md 10.1), which is no pattern.
		return nil, errFeatureNotYet(op, "AllowedPattern")
	}
	user := identity.Render(c.Principal())
	_, err = kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		if _, ok := st.Parameters[n.canonical]; ok {
			if in.Overwrite != nil && *in.Overwrite {
				return struct{}{}, errFeatureNotYet(op, "Overwrite")
			}
			return struct{}{}, errAlreadyExists()
		}
		st.Parameters[n.canonical] = Parameter{
			Name: n.canonical,
			Versions: []Version{{
				Number: 1, Type: paramType(ssmapi.ParameterTypeString), Value: plainValue{Text: in.Value},
				Description: in.Description, DataType: defaultDataType, Tier: tier(ssmapi.ParameterTierStandard),
				LastModified: c.Admitted(), LastModifiedUser: user,
			}},
			LastVersion: 1,
			Labels:      map[string]int64{},
		}
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	c.Touch(parameterARN(scope, n.canonical))
	return &ssmapi.PutParameterOutput{Version: 1, Tier: new(ssmapi.ParameterTierStandard)}, nil
}

// getParameter reads a parameter's latest version by name or ARN. Not
// implemented yet: a version or label selector.
func (s *Service) getParameter(_ context.Context, c *kernel.Call, in *ssmapi.GetParameterInput) (*ssmapi.GetParameterOutput, *awserr.Error) {
	const op = "GetParameter"
	if err := checkReserved(op, in.Name); err != nil {
		return nil, err
	}
	if hasSelector(in.Name) {
		return nil, errFeatureNotYet(op, "a Name with a version or label selector")
	}
	scope := c.Scope()
	n, err := parseNameOrARN(in.Name, scope)
	if err != nil {
		return nil, err
	}
	p, ok := s.lookup(scope, n.canonical)
	if !ok {
		return nil, errNotFound()
	}
	v := p.latest()
	text, err := plainText(v.Value)
	if err != nil {
		return nil, err
	}
	arn := parameterARN(scope, p.Name)
	c.Touch(arn)
	return &ssmapi.GetParameterOutput{Parameter: &ssmapi.Parameter{
		Name: new(n.spelling), Type: new(ssmapi.ParameterType(v.Type)), Value: new(text), Version: v.Number,
		LastModifiedDate: new(v.LastModified.Wire()), ARN: new(arn.String()), DataType: new(v.DataType),
	}}, nil
}

// describeParameters lists the metadata of the parameters a Name Equals
// filter names, Terraform's one request (ssm-parameters.md 10.1). Not
// implemented yet: every other filter and option, Shared, paging and the
// deprecated Filters.
func (s *Service) describeParameters(_ context.Context, c *kernel.Call, in *ssmapi.DescribeParametersInput) (*ssmapi.DescribeParametersOutput, *awserr.Error) {
	const op = "DescribeParameters"
	for _, f := range in.ParameterFilters {
		if f.Key != "Name" && f.Key != "Path" {
			continue
		}
		if err := checkReserved(op, f.Values...); err != nil {
			return nil, err
		}
	}
	switch {
	case len(in.Filters) > 0:
		return nil, errFeatureNotYet(op, "Filters")
	case in.Shared != nil && *in.Shared:
		return nil, errFeatureNotYet(op, "Shared")
	case in.NextToken != nil:
		return nil, errFeatureNotYet(op, "NextToken")
	case len(in.ParameterFilters) != 1 || in.ParameterFilters[0].Key != "Name" ||
		(in.ParameterFilters[0].Option != nil && *in.ParameterFilters[0].Option != "Equals"):
		return nil, errFeatureNotYet(op, "ParameterFilters other than one Name filter with Option Equals")
	}
	wanted := make([]string, 0, len(in.ParameterFilters[0].Values))
	for _, raw := range in.ParameterFilters[0].Values {
		wanted = append(wanted, canonicalOf(raw))
	}
	slices.Sort(wanted)
	wanted = slices.Compact(wanted)
	scope := c.Scope()
	found := readState(s, scope, func(st *State) []Parameter {
		var out []Parameter
		for _, w := range wanted {
			if p, ok := st.Parameters[w]; ok {
				out = append(out, p)
			}
		}
		return out
	})
	size := describePageSize
	if in.MaxResults != nil {
		size = int(*in.MaxResults)
	}
	if len(found) > size {
		return nil, errFeatureNotYet(op, "paging")
	}
	out := &ssmapi.DescribeParametersOutput{Parameters: make([]ssmapi.ParameterMetadata, 0, len(found))}
	for _, p := range found {
		v := p.latest()
		out.Parameters = append(out.Parameters, ssmapi.ParameterMetadata{
			Name: new(p.Name), ARN: new(parameterARN(scope, p.Name).String()), Type: new(ssmapi.ParameterType(v.Type)),
			LastModifiedDate: new(v.LastModified.Wire()), LastModifiedUser: new(v.LastModifiedUser),
			Description: v.Description, AllowedPattern: v.AllowedPattern, Version: v.Number,
			Tier: new(ssmapi.ParameterTier(v.Tier)), Policies: []ssmapi.ParameterInlinePolicy{}, DataType: new(v.DataType),
		})
	}
	return out, nil
}

// deleteParameter drops a parameter with its versions, labels and tags.
func (s *Service) deleteParameter(_ context.Context, c *kernel.Call, in *ssmapi.DeleteParameterInput) (*ssmapi.DeleteParameterOutput, *awserr.Error) {
	if err := checkReserved("DeleteParameter", in.Name); err != nil {
		return nil, err
	}
	scope := c.Scope()
	n, err := parseName(in.Name, scope)
	if err != nil {
		return nil, err
	}
	_, err = kernel.Write(c, s.cells, scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if _, ok := st.Parameters[n.canonical]; !ok {
			tx.Unchanged()
			return struct{}{}, errNotFound()
		}
		delete(st.Parameters, n.canonical)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	c.Touch(parameterARN(scope, n.canonical))
	return &ssmapi.DeleteParameterOutput{}, nil
}

// listTagsForResource lists a parameter's tags in the order they were
// first set, [] when it has none. UNVERIFIED (SSM-25), ssm-parameters.md
// 7.9: the empty list rather than an absent one.
func (s *Service) listTagsForResource(_ context.Context, c *kernel.Call, in *ssmapi.ListTagsForResourceInput) (*ssmapi.ListTagsForResourceOutput, *awserr.Error) {
	if err := checkReserved("ListTagsForResource", in.ResourceId); err != nil {
		return nil, err
	}
	if in.ResourceType != ssmapi.ResourceTypeForTaggingParameter {
		return nil, ssmapi.ErrInvalidResourceId()
	}
	scope := c.Scope()
	// A ResourceId that is not a valid name names no parameter
	// (contract 0: InvalidResourceId).
	n, err := parseName(in.ResourceId, scope)
	if err != nil {
		return nil, ssmapi.ErrInvalidResourceId()
	}
	p, ok := s.lookup(scope, n.canonical)
	if !ok {
		return nil, ssmapi.ErrInvalidResourceId()
	}
	tags := make([]ssmapi.Tag, 0, len(p.Tags))
	for _, t := range p.Tags {
		tags = append(tags, ssmapi.Tag{Key: t.Key, Value: t.Value})
	}
	c.Touch(parameterARN(scope, p.Name))
	return &ssmapi.ListTagsForResourceOutput{TagList: tags}, nil
}
