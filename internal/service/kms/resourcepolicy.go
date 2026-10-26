package kms

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the stored key policy of the key target names
// (authz.PolicyProvider, iam-enforcement-surfaces.md 7.5), the one CreateKey
// or PutKeyPolicy sent or the default one: every key has a key policy. ok is
// false for a missing key, an alias ARN and any other KMS ARN.
func (s *Service) ResourcePolicy(target aws.ARN) (doc *policydoc.Document, ok bool) {
	id, isKey := strings.CutPrefix(target.Resource, "key/")
	scope, serr := aws.ScopeOf(target)
	if target.Service != "kms" || !isKey || id == "" || serr != nil {
		return nil, false
	}
	type found struct {
		policy string
		ok     bool
	}
	f := readState(s, scope, func(st *State) found {
		k, ok := st.Keys[id]
		return found{policy: k.Policy, ok: ok}
	})
	if !f.ok {
		return nil, false
	}
	// CreateKey and PutKeyPolicy refuse a text policydoc refuses
	// (checkPolicy), so the key's policy always parses.
	return policydoc.ParseStored(f.policy), true
}
