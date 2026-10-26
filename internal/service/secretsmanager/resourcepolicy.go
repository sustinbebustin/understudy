package secretsmanager

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the resource policy of the secret whose full ARN
// target is (authz.PolicyProvider, iam-enforcement-surfaces.md 7.6); nil when
// it has none. ok is false for a missing secret and a partial ARN: only the
// full ARN names one secret, since a secret recreated with the same name
// has another suffix.
func (s *Service) ResourcePolicy(target aws.ARN) (doc *policydoc.Document, ok bool) {
	rest, isSecret := strings.CutPrefix(target.Resource, "secret:")
	name, suffix, full := cutSuffix(rest)
	scope, serr := aws.ScopeOf(target)
	if target.Service != "secretsmanager" || !isSecret || !full || serr != nil {
		return nil, false
	}
	type found struct {
		policy *string
		ok     bool
	}
	f := readState(s, scope, func(st *State) found {
		sec, ok := st.Secrets[name]
		if !ok || sec.Suffix != suffix {
			return found{}
		}
		return found{policy: sec.Policy, ok: true}
	})
	if !f.ok {
		return nil, false
	}
	if f.policy == nil {
		return nil, true
	}
	// PutResourcePolicy refuses a text policydoc refuses, so a stored
	// policy always parses.
	return policydoc.ParseStored(*f.policy), true
}
