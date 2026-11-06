package secretsmanager

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
)

// StoredPolicy returns a secret's owner, the secret's account, and its
// resource policy as IAM evaluation reads them (authz.StoredPolicyReader,
// iam-enforcement-surfaces.md 7.6), for the secret's full ARN: NoPolicy for
// a secret without one, else Parsed, since PutResourcePolicy refuses a text
// policydoc refuses. It is false for a missing secret and a partial ARN:
// only the full ARN names one secret, since a secret recreated with the
// same name has another suffix.
func (s *Service) StoredPolicy(target aws.ARN) (authz.StoredPolicy, bool) {
	rest, isSecret := strings.CutPrefix(target.Resource, "secret:")
	name, suffix, full := cutSuffix(rest)
	scope, serr := aws.ScopeOf(target)
	if target.Service != "secretsmanager" || !isSecret || !full || serr != nil {
		return authz.StoredPolicy{}, false
	}
	type found struct {
		policy string
		ok     bool
	}
	f := readState(s, scope, func(st *State) found {
		sec, ok := st.Secrets[name]
		if !ok || sec.Suffix != suffix {
			return found{}
		}
		if sec.Policy == nil {
			return found{ok: true}
		}
		return found{policy: *sec.Policy, ok: true}
	})
	if !f.ok {
		return authz.StoredPolicy{}, false
	}
	return authz.StoredPolicy{Owner: scope.Account, Policy: authz.ReadStored(f.policy)}, true
}
