package lambda

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/store"
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
	text, ok := store.View2(s.cells, scope, func(st *State) (string, bool) {
		fn, ok := st.Functions[name]
		if !ok {
			return "", false
		}
		p, ok := fn.Policies[qualifier]
		if !ok || len(p.Statements) == 0 {
			return "", true
		}
		return p.document(), true
	})
	if !ok {
		return authz.StoredPolicy{}, false
	}
	return authz.StoredPolicy{Owner: scope.Account, Policy: authz.ReadStored(text)}, true
}

// layerVersionStoredPolicy is StoredPolicy for a layer version ARN.
func (s *Service) layerVersionStoredPolicy(target aws.ARN) (authz.StoredPolicy, bool) {
	ref, n, ok := parseLayerVersionARN(target.String())
	if !ok {
		return authz.StoredPolicy{}, false
	}
	text, ok := store.View2(s.cells, ref.scope, func(st *State) (string, bool) {
		v, ok := st.layerVersion(ref, n)
		return v.policyDocument(), ok
	})
	if !ok {
		return authz.StoredPolicy{}, false
	}
	return authz.StoredPolicy{Owner: ref.scope.Account, Policy: authz.ReadStored(text)}, true
}
