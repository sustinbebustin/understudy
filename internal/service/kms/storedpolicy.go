package kms

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/store"
)

// StoredPolicy returns a key's owner, the key's account, and its key policy,
// the one CreateKey or PutKeyPolicy sent or the default one, as IAM
// evaluation reads them (authz.StoredPolicyReader, iam-enforcement-surfaces.md
// 7.5): "Every KMS key must have exactly one key policy" (iam-evaluation.md
// 4.1). CreateKey and PutKeyPolicy refuse a text policydoc refuses
// (checkPolicy), so the policy is Parsed unless the stored state was edited.
// It is false for a missing key, an alias ARN and any other KMS ARN.
func (s *Service) StoredPolicy(target aws.ARN) (authz.StoredPolicy, bool) {
	id, isKey := strings.CutPrefix(target.Resource, "key/")
	scope, serr := aws.ScopeOf(target)
	if target.Service != "kms" || !isKey || id == "" || serr != nil {
		return authz.StoredPolicy{}, false
	}
	policy, ok := store.View2(s.cells, scope, func(st *State) (string, bool) {
		k, ok := st.Keys[id]
		return k.Policy, ok
	})
	if !ok {
		return authz.StoredPolicy{}, false
	}
	return authz.StoredPolicy{Owner: scope.Account, Policy: authz.ReadStored(policy)}, true
}
