package iam

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ResourcePolicy returns the trust policy of the role target names
// (authz.PolicyProvider; "The trust policy is the role's resource policy",
// iam-evaluation.md 3.1, iam-enforcement-surfaces.md 7.9). target must be
// the role's ARN as IAM builds it, path included; ok is false for a missing
// role, a path or name that differs from the stored one, and any other IAM
// ARN, which has no resource policy. Whether AWS reads a role ARN that
// spells the name in another case as that role is unrecorded; such an ARN
// names none here.
func (s *Service) ResourcePolicy(target aws.ARN) (doc *policydoc.Document, ok bool) {
	rest, isRole := strings.CutPrefix(target.Resource, "role/")
	account, aerr := aws.ParseAccountID(target.Account)
	if target.Service != "iam" || !isRole || aerr != nil {
		return nil, false
	}
	name := rest[strings.LastIndex(rest, "/")+1:]
	r, err := view(s, account, func(st *State) (Role, *awserr.Error) { return st.role(name) })
	if err != nil || roleARN(account, r.Path, r.Name) != target {
		return nil, false
	}
	// CreateRole and UpdateAssumeRolePolicy refuse a text policydoc
	// refuses (checkTrustPolicy), so a trust policy always parses.
	return policydoc.ParseStored(r.TrustPolicy), true
}
