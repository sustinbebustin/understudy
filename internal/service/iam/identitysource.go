package iam

import (
	"fmt"
	"strings"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// The service is the evaluator's identity-policy source
// (iam-enforcement-surfaces.md 12.2).
var _ authz.IdentitySource = (*Service)(nil)

// Identity returns p's identity-based policies (authz.IdentitySource): a
// user's or a session's role's inline policies, then its attached customer
// managed policies at their default version and attached AWS managed
// policies from the catalogue, in attachment order (iam-evaluation.md 2.1);
// the permissions boundary (2.8); a session's session policies, the inline
// one then the managed PolicyARNs, resolved in the role's account or the
// catalogue (2.7); and the principal tags, a role's overridden by its
// session's regardless of case (2.7, S-TAGS). A root, a service principal
// and an anonymous caller have none (2.6, 2.9, 2.10).
//
// The user or role is the one at the principal's path and name; a session's
// role keeps the path its session ARN drops. Unique ids are not compared,
// so a role deleted and created again at the same path and name is the
// role of its earlier sessions; what AWS does then is unrecorded.
func (s *Service) Identity(p identity.Principal) (authz.IdentitySet, error) {
	switch p := p.(type) {
	case identity.User:
		return s.userIdentity(p)
	case identity.AssumedRole:
		return s.sessionIdentity(p)
	case identity.Root, identity.ServicePrincipal, identity.Anonymous, nil:
	}
	return authz.IdentitySet{Boundary: authz.NoBoundary{}}, nil
}

func (s *Service) userIdentity(p identity.User) (authz.IdentitySet, error) {
	at := userARN(p.Account, pathOr(p.Path), p.Name)
	e, ok := entityOf(s, p.Account, func(st *State) (entity, bool) {
		u, ok := st.Users[key(p.Name)]
		if !ok || userARN(p.Account, u.Path, u.Name) != at {
			return entity{}, false
		}
		return st.entity(p.Account, u.Inline, u.Attached, u.Boundary, u.Tags), true
	})
	if !ok {
		return authz.IdentitySet{}, &authz.NoSuchPrincipalError{Principal: identity.Render(p)}
	}
	return s.identitySet(identity.Render(p), e, nil)
}

func (s *Service) sessionIdentity(p identity.AssumedRole) (authz.IdentitySet, error) {
	at := roleARN(p.Account, pathOr(p.Role.Path), p.Role.Name)
	attrs := identity.SessionAttributes{}
	if p.Attributes != nil {
		attrs = *p.Attributes
	}
	type read struct {
		role    entity
		managed []held
	}
	r, ok := entityOf(s, p.Account, func(st *State) (read, bool) {
		role, ok := st.Roles[key(p.Role.Name)]
		if !ok || roleARN(p.Account, role.Path, role.Name) != at {
			return read{}, false
		}
		out := read{role: st.entity(p.Account, role.Inline, role.Attached, role.Boundary, role.Tags)}
		for _, arn := range attrs.PolicyARNs {
			out.managed = append(out.managed, st.holdManaged(p.Account, arn))
		}
		return out, true
	})
	if !ok {
		return authz.IdentitySet{}, &authz.NoSuchPrincipalError{Principal: identity.Render(p)}
	}
	var session []held
	if attrs.Policy != "" {
		// The inline session policy has no name, so its denials name none.
		session = append(session, heldText{text: attrs.Policy})
	}
	set, err := s.identitySet(identity.Render(p), r.role, append(session, r.managed...))
	if err != nil {
		return authz.IdentitySet{}, err
	}
	set.Tags = sessionTags(attrs.Tags, set.Tags)
	return set, nil
}

// sessionTags are a session's principal tags: its session tags, then the
// role's tags whose keys no session tag has, keys compared regardless of
// case (session tags "override existing assumed role ... tags with the same
// tag key, regardless of character case", iam-evaluation.md 2.7).
func sessionTags(session []identity.SessionTag, role []authz.Tag) []authz.Tag {
	var out []authz.Tag
	for _, t := range session {
		out = append(out, authz.Tag{Key: t.Key, Value: t.Value})
	}
	for _, t := range role {
		overridden := false
		for _, st := range session {
			overridden = overridden || strings.EqualFold(st.Key, t.Key)
		}
		if !overridden {
			out = append(out, t)
		}
	}
	return out
}

// PrincipalOf returns the principal an IAM ARN names
// (authz.IdentitySource): an account's root, the user at that path and name
// with its id, or the role at that path and name as a session named
// "understudy" with no attributes. It is false for a user or role IAM does
// not hold and for any other ARN.
func (s *Service) PrincipalOf(arn aws.ARN) (identity.Principal, bool) {
	account, aerr := aws.ParseAccountID(arn.Account)
	if arn.Partition != aws.PartitionAWS().String() || arn.Service != "iam" || arn.Region != "" || aerr != nil {
		return nil, false
	}
	switch {
	case arn.Resource == "root":
		return identity.Root{Account: account}, true
	case strings.HasPrefix(arn.Resource, "user/"):
		return entityOf(s, account, func(st *State) (identity.Principal, bool) {
			u, ok := st.Users[key(lastSegment(arn.Resource))]
			if !ok || userARN(account, u.Path, u.Name) != arn {
				return nil, false
			}
			return u.principal(account), true
		})
	case strings.HasPrefix(arn.Resource, "role/"):
		_, r, ok := s.roleAt(arn)
		if !ok {
			return nil, false
		}
		return identity.AssumedRole{
			Account: account, Role: identity.RoleRef{Name: r.Name, Path: r.Path, ID: string(r.ID)},
			Session: evaluatedSession, Source: identity.FromAssumeRole{},
		}, true
	}
	return nil, false
}

// evaluatedSession is the session name PrincipalOf gives a role.
const evaluatedSession = "understudy"

// roleAt returns the role whose ARN, path included, is target, and its
// account. Whether AWS reads a role ARN that spells the name in another
// case as that role is unrecorded; such an ARN names none here.
func (s *Service) roleAt(target aws.ARN) (aws.AccountID, Role, bool) {
	account, aerr := aws.ParseAccountID(target.Account)
	if target.Service != "iam" || !strings.HasPrefix(target.Resource, "role/") || aerr != nil {
		return aws.AccountID{}, Role{}, false
	}
	r, ok := entityOf(s, account, func(st *State) (Role, bool) {
		r, ok := st.Roles[key(lastSegment(target.Resource))]
		if !ok || roleARN(account, r.Path, r.Name) != target {
			return Role{}, false
		}
		return r, true
	})
	return account, r, ok
}

// entityOf runs fn on the account's state, as view does, for a lookup that
// finds what it reads or does not.
func entityOf[R any](s *Service, account aws.AccountID, fn func(st *State) (R, bool)) (R, bool) {
	type found struct {
		r  R
		ok bool
	}
	f, _ := view(s, account, func(st *State) (found, *awserr.Error) {
		r, ok := fn(st)
		return found{r: r, ok: ok}, nil
	})
	return f.r, f.ok
}

// lastSegment is the name in an IAM ARN's resource: the text after its last
// slash.
func lastSegment(resource string) string {
	return resource[strings.LastIndex(resource, "/")+1:]
}

// pathOr is a principal's path, "/" when empty.
func pathOr(path string) string {
	if path == "" {
		return "/"
	}
	return path
}

// entity is what a user or role holds that IAM evaluation reads, copied out
// of the account's state: its inline then attached policies, its boundary
// (nil for none) and its tags.
type entity struct {
	policies []held
	boundary held
	tags     []Tag
}

// entity reads an entity's holdings in account.
func (st *State) entity(account aws.AccountID, inline []InlinePolicy, attached []string, boundary string, tags []Tag) entity {
	e := entity{tags: tags}
	for _, p := range inline {
		e.policies = append(e.policies, heldText{name: p.Name, text: p.Document})
	}
	for _, arn := range attached {
		e.policies = append(e.policies, st.holdManaged(account, arn))
	}
	if boundary != "" {
		e.boundary = st.holdManaged(account, boundary)
	}
	return e
}

// held is one policy an entity or session holds, as read under the state
// lock. An AWS managed policy is resolved from the catalogue after the lock
// is released, since the catalogue loads on first use.
//
//sumtype:decl
type held interface{ isHeld() }

// heldText is an inline policy (arn "") or a customer managed policy's
// default version.
type heldText struct{ arn, name, text string }

// heldAWSManaged is an AWS managed policy ARN.
type heldAWSManaged struct{ ref policyRef }

// heldMissing is a managed policy ARN that names no policy of the account.
// IAM refuses to attach such an ARN or to delete an attached policy, but
// STS does not check the PolicyArns of AssumeRole.
type heldMissing struct{ arn string }

func (heldText) isHeld()       {}
func (heldAWSManaged) isHeld() {}
func (heldMissing) isHeld()    {}

// holdManaged reads the managed policy arn names as account holds it.
func (st *State) holdManaged(account aws.AccountID, arn string) held {
	ref, ok := parsePolicyARN(arn)
	switch {
	case !ok:
		return heldMissing{arn: arn}
	case ref.awsManaged():
		return heldAWSManaged{ref: ref}
	}
	p, _, err := st.localPolicy(account, arn)
	if err != nil {
		return heldMissing{arn: arn}
	}
	v, ok := p.version(p.Default)
	if !ok {
		return heldMissing{arn: arn}
	}
	return heldText{arn: arn, name: ref.name, text: v.Document}
}

// identitySet resolves and parses an entity's holdings and a session's
// session policies for principal.
func (s *Service) identitySet(principal string, e entity, session []held) (authz.IdentitySet, error) {
	set := authz.IdentitySet{Boundary: authz.NoBoundary{}}
	for _, h := range e.policies {
		p, err := s.resolve(principal, h)
		if err != nil {
			return authz.IdentitySet{}, err
		}
		set.Policies = append(set.Policies, p)
	}
	if e.boundary != nil {
		p, err := s.resolve(principal, e.boundary)
		if err != nil {
			return authz.IdentitySet{}, err
		}
		set.Boundary = authz.BoundedBy{Policy: p}
	}
	for _, h := range session {
		p, err := s.resolve(principal, h)
		if err != nil {
			return authz.IdentitySet{}, err
		}
		set.Session = append(set.Session, p)
	}
	for _, t := range e.tags {
		set.Tags = append(set.Tags, authz.Tag{Key: t.Key, Value: t.Value})
	}
	return set, nil
}

// resolve reads one held policy as the evaluator takes it. A document that
// does not parse, or an ARN that names no policy, is an
// *authz.UnreadablePolicyError, never no policy.
func (s *Service) resolve(principal string, h held) (authz.Policy, error) {
	var arn, name, text string
	switch h := h.(type) {
	case heldText:
		arn, name, text = h.arn, h.name, h.text
	case heldAWSManaged:
		arn, name = h.ref.arn.String(), h.ref.name
		doc, err := s.catalogued(arn)
		if err != nil {
			return authz.Policy{}, &authz.UnreadablePolicyError{Principal: principal, Policy: arn, Err: err}
		}
		text = doc
	case heldMissing:
		return authz.Policy{}, &authz.UnreadablePolicyError{Principal: principal, Policy: h.arn, Err: &unresolvedPolicyError{arn: h.arn}}
	}
	doc, perr := policydoc.Parse([]byte(text))
	if perr != nil {
		which := arn
		if which == "" {
			which = name
		}
		return authz.Policy{}, &authz.UnreadablePolicyError{Principal: principal, Policy: which, Err: perr}
	}
	return authz.Policy{ARN: arn, Name: name, Document: doc}, nil
}

// catalogued returns the document of the catalogued AWS managed policy arn
// names. A catalogue that fails to load is a *catalogueError, and an ARN it
// does not hold an *unresolvedPolicyError.
func (s *Service) catalogued(arn string) (string, error) {
	if s.catalogue == nil {
		return "", &unresolvedPolicyError{arn: arn}
	}
	cat, err := s.catalogue()
	if err != nil {
		return "", &catalogueError{err: err}
	}
	p, ok := cat.Lookup(arn)
	if !ok {
		return "", &unresolvedPolicyError{arn: arn}
	}
	return p.Document, nil
}

// unresolvedPolicyError is a managed policy ARN a user, role or session
// holds that names no customer managed policy of the account and no
// catalogued AWS managed policy.
type unresolvedPolicyError struct{ arn string }

func (e *unresolvedPolicyError) Error() string {
	return fmt.Sprintf("iam: %s names no customer managed policy of the account and no AWS managed policy in the shipped catalogue", e.arn)
}
