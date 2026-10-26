package lambda

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// StoredPolicy returns the owner and permissions of what target names, as
// IAM evaluation reads them (authz.StoredPolicyReader,
// iam-enforcement-surfaces.md 7.4): for a function or qualifier,
// arn:...:function:<name>[:<qualifier>], the function's account and the
// policy GetPolicy returns; for a layer version,
// arn:...:layer:<name>:<version>, the layer's account and the policy
// GetLayerVersionPolicy returns (lambda-destinations-layers.md 7.4). The
// policy is NoPolicy when there are no statements, else the text, parsed.
// It is false when no such function or layer version exists.
func (s *Service) StoredPolicy(target aws.ARN) (authz.StoredPolicy, bool) {
	scope, serr := aws.ScopeOf(target)
	if target.Service != "lambda" || serr != nil {
		return authz.StoredPolicy{}, false
	}
	if strings.HasPrefix(target.Resource, "layer:") {
		return s.layerVersionStoredPolicy(target)
	}
	rest, isFunction := strings.CutPrefix(target.Resource, "function:")
	if !isFunction {
		return authz.StoredPolicy{}, false
	}
	name, qualifier, _ := strings.Cut(rest, ":")
	type found struct {
		text string
		ok   bool
	}
	f, _ := view(s, scope, func(st *State) (found, *awserr.Error) {
		fn, ok := st.Functions[name]
		if !ok {
			return found{}, nil
		}
		p, ok := fn.Policies[qualifier]
		if !ok || len(p.Statements) == 0 {
			return found{ok: true}, nil
		}
		return found{text: p.document(), ok: true}, nil
	})
	if !f.ok {
		return authz.StoredPolicy{}, false
	}
	return authz.StoredPolicy{Owner: scope.Account, Policy: authz.ReadStored(f.text)}, true
}

// layerVersionStoredPolicy is StoredPolicy for a layer version ARN.
func (s *Service) layerVersionStoredPolicy(target aws.ARN) (authz.StoredPolicy, bool) {
	ref, n, ok := parseLayerVersionARN(target.String())
	if !ok {
		return authz.StoredPolicy{}, false
	}
	type found struct {
		text string
		ok   bool
	}
	f, _ := view(s, ref.scope, func(st *State) (found, *awserr.Error) {
		v, ok := st.layerVersion(ref, n)
		return found{text: v.policyDocument(), ok: ok}, nil
	})
	if !f.ok {
		return authz.StoredPolicy{}, false
	}
	return authz.StoredPolicy{Owner: ref.scope.Account, Policy: authz.ReadStored(f.text)}, true
}
