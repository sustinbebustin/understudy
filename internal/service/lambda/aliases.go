package lambda

import (
	"context"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

func (s *Service) bindAliases(b lambdaapi.Binder) {
	b.CreateAlias(s.createAlias, partial("weighted routing is stored, not applied to invocations"))
	b.GetAlias(s.getAlias, full())
	b.UpdateAlias(s.updateAlias, partial("weighted routing is stored, not applied to invocations"))
	b.DeleteAlias(s.deleteAlias, full())
	b.ListAliases(s.listAliases, full())
}

// aliasARN is the ARN of alias name of the function ref names.
func (r functionRef) aliasARN(name string) string { return r.arn() + ":" + name }

func (s *Service) createAlias(_ context.Context, c *kernel.Call, in *lambdaapi.CreateAliasInput) (*lambdaapi.AliasConfiguration, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, nil)
	if err != nil {
		return nil, err
	}
	out, err := write(s, c, ref.scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (lambdaapi.AliasConfiguration, *awserr.Error) {
		f, err := st.function(ref)
		if err != nil {
			return lambdaapi.AliasConfiguration{}, err
		}
		if _, ok := f.Aliases[in.Name]; ok {
			return lambdaapi.AliasConfiguration{}, errAliasExists(ref.aliasARN(in.Name))
		}
		weights, err := routing(ref, &f, in.FunctionVersion, in.RoutingConfig)
		if err != nil {
			return lambdaapi.AliasConfiguration{}, err
		}
		a := Alias{Name: in.Name, Version: in.FunctionVersion, Description: deref(in.Description), Weights: weights, RevisionID: st.revision(ref.scope)}
		f = f.clone()
		if f.Aliases == nil {
			f.Aliases = map[string]Alias{}
		}
		f.Aliases[a.Name] = a
		st.Functions[ref.name] = f
		return renderAlias(ref, a), nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, ref.aliasARN(in.Name))
	return &out, nil
}

// routing checks an alias's primary version and routing configuration and
// returns the weights to store; nil when the alias routes to one version
// (an empty RoutingConfig included, which Terraform always sends).
func routing(ref functionRef, f *Function, primary string, cfg *lambdaapi.AliasRoutingConfiguration) (map[string]float64, *awserr.Error) {
	if _, ok := f.version(primary); !ok {
		return nil, errFunctionNotFound(ref.arn() + ":" + primary)
	}
	if cfg == nil || len(cfg.AdditionalVersionWeights) == 0 {
		return nil, nil
	}
	if len(cfg.AdditionalVersionWeights) > 1 {
		return nil, errTooManyWeights()
	}
	if primary == latest {
		return nil, errWeightedLatest()
	}
	for v := range cfg.AdditionalVersionWeights {
		if v == primary {
			return nil, errWeightOnPrimary(v)
		}
		if _, ok := f.version(v); !ok {
			return nil, errFunctionNotFound(ref.arn() + ":" + v)
		}
	}
	return maps.Clone(cfg.AdditionalVersionWeights), nil
}

func (s *Service) getAlias(_ context.Context, c *kernel.Call, in *lambdaapi.GetAliasInput) (*lambdaapi.AliasConfiguration, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, nil)
	if err != nil {
		return nil, err
	}
	return view(s, ref.scope, func(st *State) (*lambdaapi.AliasConfiguration, *awserr.Error) {
		f, err := st.function(ref)
		if err != nil {
			return nil, err
		}
		a, ok := f.Aliases[in.Name]
		if !ok {
			return nil, errAliasNotFound(ref.aliasARN(in.Name))
		}
		return new(renderAlias(ref, a)), nil
	})
}

func (s *Service) updateAlias(_ context.Context, c *kernel.Call, in *lambdaapi.UpdateAliasInput) (*lambdaapi.AliasConfiguration, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, nil)
	if err != nil {
		return nil, err
	}
	out, err := write(s, c, ref.scope, func(st *State, _ clock.Instant, _ *kernel.Tx) (lambdaapi.AliasConfiguration, *awserr.Error) {
		f, err := st.function(ref)
		if err != nil {
			return lambdaapi.AliasConfiguration{}, err
		}
		a, ok := f.Aliases[in.Name]
		if !ok {
			return lambdaapi.AliasConfiguration{}, errAliasNotFoundForUpdate(ref.aliasARN(in.Name))
		}
		if in.RevisionId != nil && *in.RevisionId != a.RevisionID {
			return lambdaapi.AliasConfiguration{}, errRevisionMismatch()
		}
		if in.FunctionVersion != nil {
			a.Version = *in.FunctionVersion
		}
		cfg := in.RoutingConfig
		if cfg == nil {
			cfg = &lambdaapi.AliasRoutingConfiguration{AdditionalVersionWeights: a.Weights}
		}
		weights, err := routing(ref, &f, a.Version, cfg)
		if err != nil {
			return lambdaapi.AliasConfiguration{}, err
		}
		a.Weights = weights
		if in.Description != nil {
			a.Description = *in.Description
		}
		a.RevisionID = st.revision(ref.scope)
		f = f.clone()
		f.Aliases[a.Name] = a
		st.Functions[ref.name] = f
		return renderAlias(ref, a), nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, ref.aliasARN(in.Name))
	return &out, nil
}

// deleteAlias deletes the alias with the resource policy, function URL
// and asynchronous invocation configuration it qualifies, so an alias
// created again under the name starts without them (error-matrix.md 5.6).
// An alias that does not exist is deleted already: AWS answers 204
// (test_non_existent_alias_deletion).
func (s *Service) deleteAlias(_ context.Context, c *kernel.Call, in *lambdaapi.DeleteAliasInput) (*lambdaapi.DeleteAliasOutput, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, nil)
	if err != nil {
		return nil, err
	}
	_, err = write(s, c, ref.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		f, err := st.function(ref)
		if err != nil {
			return struct{}{}, err
		}
		if _, ok := f.Aliases[in.Name]; !ok {
			tx.Unchanged()
			return struct{}{}, nil
		}
		f = f.clone()
		if u, ok := f.URLs[in.Name]; ok {
			st.retireURLs(u)
		}
		delete(f.Aliases, in.Name)
		delete(f.Policies, in.Name)
		delete(f.URLs, in.Name)
		delete(f.EventInvoke, in.Name)
		st.Functions[ref.name] = f
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	touch(c, ref.aliasARN(in.Name))
	return &lambdaapi.DeleteAliasOutput{}, nil
}

func (s *Service) listAliases(_ context.Context, c *kernel.Call, in *lambdaapi.ListAliasesInput) (*lambdaapi.ListAliasesOutput, *awserr.Error) {
	ref, err := parseFunctionRef(c, in.FunctionName, nil)
	if err != nil {
		return nil, err
	}
	aliases, err := view(s, ref.scope, func(st *State) ([]lambdaapi.AliasConfiguration, *awserr.Error) {
		f, err := st.function(ref)
		if err != nil {
			return nil, err
		}
		var out []lambdaapi.AliasConfiguration
		for _, name := range slices.Sorted(maps.Keys(f.Aliases)) {
			if a := f.Aliases[name]; in.FunctionVersion == nil || a.Version == *in.FunctionVersion {
				out = append(out, renderAlias(ref, a))
			}
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	p, err := page(listingAliases, aliases, func(a lambdaapi.AliasConfiguration) string { return deref(a.Name) }, in.MaxItems, listPageCap, in.Marker)
	if err != nil {
		return nil, err
	}
	return &lambdaapi.ListAliasesOutput{Aliases: append([]lambdaapi.AliasConfiguration{}, p.Items...), NextMarker: p.Next}, nil
}

// renderAlias reports an alias; RoutingConfig only when it has weights,
// since Terraform reads an empty one as a configured block (tf-remaining.md
// 4.1).
func renderAlias(ref functionRef, a Alias) lambdaapi.AliasConfiguration {
	out := lambdaapi.AliasConfiguration{
		AliasArn:        new(ref.aliasARN(a.Name)),
		Name:            new(a.Name),
		FunctionVersion: new(a.Version),
		Description:     new(a.Description),
		RevisionId:      new(a.RevisionID),
	}
	if len(a.Weights) > 0 {
		out.RoutingConfig = &lambdaapi.AliasRoutingConfiguration{AdditionalVersionWeights: maps.Clone(a.Weights)}
	}
	return out
}
