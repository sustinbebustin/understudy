package authz

import (
	"fmt"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Request is one (action, resource) pair a principal asks for.
type Request struct {
	Principal identity.Principal
	Action    registry.IAMAction
	Resource  Resource
	Context   *policydoc.Context
}

// Resource is what a pair is on.
//
//sumtype:decl
type Resource interface{ isResource() }

// OnResource is one resource and the account that owns it.
type OnResource struct {
	ARN   aws.ARN
	Owner aws.AccountID
}

// AnyResource is a pair on no resource ("*"), asked in Account.
type AnyResource struct{ Account aws.AccountID }

func (OnResource) isResource()  {}
func (AnyResource) isResource() {}

// Policy is one policy document and what names it in a denial.
type Policy struct {
	// ARN is a managed policy's ARN; "" for an inline policy, whose denials
	// carry no ARN (iam-enforcement-surfaces.md 12.3).
	ARN string
	// Name is the inline policy's name or the managed policy's name.
	Name     string
	Document policydoc.Document
}

// Boundary is the permissions boundary of a user or of a session's role.
//
//sumtype:decl
type Boundary interface{ isBoundary() }

// NoBoundary is an entity without a permissions boundary.
type NoBoundary struct{}

// BoundedBy is an entity whose permissions boundary is Policy.
type BoundedBy struct{ Policy Policy }

func (NoBoundary) isBoundary() {}
func (BoundedBy) isBoundary()  {}

// ResourcePolicy is the resource-based policy of a pair's resource and how
// AWS reads it (iam-evaluation.md 2.2 step 3).
//
//sumtype:decl
type ResourcePolicy interface{ isResourcePolicy() }

// NoResourcePolicy is a resource without a resource-based policy, or a pair
// on no resource.
type NoResourcePolicy struct{}

// Generic is a queue, topic, bucket, function or secret policy: within one
// account, an Allow in it or in the identity policy is enough.
type Generic struct{ Document policydoc.Document }

// Trust is a role's trust policy, which must allow explicitly
// (iam-evaluation.md 3.1).
type Trust struct{ Document policydoc.Document }

// KeyPolicy is a KMS key policy, which must allow the caller or the
// caller's account (iam-evaluation.md 4).
type KeyPolicy struct{ Document policydoc.Document }

func (NoResourcePolicy) isResourcePolicy() {}
func (Generic) isResourcePolicy()          {}
func (Trust) isResourcePolicy()            {}
func (KeyPolicy) isResourcePolicy()        {}

// Policies are the policies that apply to a pair.
type Policies struct {
	// Identity are the identity-based policies of the user or of the
	// session's role: inline, then attached managed, in stored order.
	Identity []Policy
	Boundary Boundary
	// Session are a role session's session policies, inline then the managed
	// PolicyArns; empty when none were passed, which allows
	// (iam-evaluation.md 2.2 step 6).
	Session  []Policy
	Resource ResourcePolicy
}

// PolicyType is the policy type a denial names (iam-evaluation.md 2.11).
type PolicyType uint8

// PolicyType values.
const (
	// IdentityBased is "identity-based policy".
	IdentityBased PolicyType = iota + 1
	// ResourceBased is "resource-based policy"; a KMS key policy denial
	// names it too (inferred, IAM-EVAL-28).
	ResourceBased
	// PermissionsBoundary is "permissions boundary".
	PermissionsBoundary
	// SessionPolicy is "session policy".
	SessionPolicy
	// RoleTrust is "role trust policy".
	RoleTrust
)

// String returns the type as the denial grammar writes it.
func (t PolicyType) String() string {
	switch t {
	case IdentityBased:
		return "identity-based policy"
	case ResourceBased:
		return "resource-based policy"
	case PermissionsBoundary:
		return "permissions boundary"
	case SessionPolicy:
		return "session policy"
	case RoleTrust:
		return "role trust policy"
	default:
		return fmt.Sprintf("PolicyType(%d)", uint8(t))
	}
}

// Decision is the answer for one pair.
//
//sumtype:decl
type Decision interface{ isDecision() }

// Allow is a pair AWS allows.
type Allow struct{}

// ImplicitDeny is a pair no policy allows; PolicyType is the step of
// iam-evaluation.md 2.2 that failed.
type ImplicitDeny struct{ PolicyType PolicyType }

// ExplicitDeny is a pair a Deny statement matches.
type ExplicitDeny struct {
	PolicyType PolicyType
	// PolicyARN is the denying managed policy's ARN; "" for an inline policy
	// or a resource-based policy.
	PolicyARN string
}

func (Allow) isDecision()        {}
func (ImplicitDeny) isDecision() {}
func (ExplicitDeny) isDecision() {}

// Outcome is Decide's answer.
type Outcome struct {
	Decision   Decision
	Unsupplied []Unsupplied
}

// Unsupplied is a condition key a policy tested that understudy could not
// supply (iam-enforcement-surfaces.md 12.2).
type Unsupplied struct {
	Key        string
	PolicyType PolicyType
	// Policy is the managed policy's ARN or the inline policy's name; "" for
	// a resource-based policy.
	Policy    string
	Statement policydoc.StatementRef
}

// Decide is the multi-policy decision for one pair (iam-evaluation.md 2.2,
// 2.4, 2.6, 3 and 4.2): explicit deny first, then the root, service-principal
// and anonymous rules, then the resource policy by the principal form it
// names, the identity policies, the permissions boundary and the session
// policies, with both sides required across accounts.
//
// Every document is evaluated, whatever decides, so Outcome.Unsupplied lists
// every unsupplied key any of them tested. Decide reads r.Context as given and
// builds none (BuildContext does). A nil Principal is anonymous, a nil
// Boundary is NoBoundary and a nil Resource policy is NoResourcePolicy.
//
// Grants, SCPs, RCPs and VPC endpoint policies do not exist in understudy, so
// no step asks for them (iam-evaluation.md 2.5, 4.6).
func Decide(r Request, p Policies) Outcome {
	e := evaluate(r, p)
	return Outcome{Decision: e.decide(r), Unsupplied: e.unsupplied}
}

// evaluated is a pair's policies, each document evaluated once.
type evaluated struct {
	identity []evaluatedPolicy
	// boundary is nil for NoBoundary.
	boundary *evaluatedPolicy
	session  []evaluatedPolicy
	resource ResourcePolicy
	// onResource is the resource policy's result, empty for NoResourcePolicy.
	onResource policydoc.Result
	unsupplied []Unsupplied
}

type evaluatedPolicy struct {
	policy Policy
	result policydoc.Result
}

func evaluate(r Request, p Policies) evaluated {
	bounded, hasBoundary := p.Boundary.(BoundedBy)
	req := policydoc.Request{
		Requester:   requesterOf(r.Principal),
		Action:      r.Action.String(),
		Resource:    resourceText(r.Resource),
		Context:     r.Context,
		HasBoundary: hasBoundary,
	}
	e := evaluated{resource: p.Resource}
	if e.resource == nil {
		e.resource = NoResourcePolicy{}
	}
	identityLike := func(t PolicyType, pol Policy) evaluatedPolicy {
		res := policydoc.Evaluate(pol.Document, policydoc.IdentityPolicy, req)
		e.gather(res, t, policyName(pol))
		return evaluatedPolicy{policy: pol, result: res}
	}
	for _, pol := range p.Identity {
		e.identity = append(e.identity, identityLike(IdentityBased, pol))
	}
	if hasBoundary {
		b := identityLike(PermissionsBoundary, bounded.Policy)
		e.boundary = &b
	}
	for _, pol := range p.Session {
		e.session = append(e.session, identityLike(SessionPolicy, pol))
	}
	if doc, ok := resourceDocument(e.resource); ok {
		e.onResource = policydoc.Evaluate(doc, policydoc.ResourcePolicy, req)
		e.gather(e.onResource, resourceType(e.resource), "")
	}
	return e
}

func (e *evaluated) gather(res policydoc.Result, t PolicyType, policy string) {
	for _, k := range res.Unsupplied {
		e.unsupplied = append(e.unsupplied, Unsupplied{Key: k.Key, PolicyType: t, Policy: policy, Statement: k.Statement})
	}
}

// policyName is what names a policy in an Unsupplied entry: the managed
// policy's ARN, else the inline policy's name.
func policyName(p Policy) string {
	if p.ARN != "" {
		return p.ARN
	}
	return p.Name
}

func resourceDocument(rp ResourcePolicy) (policydoc.Document, bool) {
	switch rp := rp.(type) {
	case Generic:
		return rp.Document, true
	case Trust:
		return rp.Document, true
	case KeyPolicy:
		return rp.Document, true
	case NoResourcePolicy:
	}
	return policydoc.Document{}, false
}

// resourceType is the policy type a resource policy's denial names: "role
// trust policy" for a trust policy, else "resource-based policy", which a key
// policy's denial names too (inferred, IAM-EVAL-28).
func resourceType(rp ResourcePolicy) PolicyType {
	if _, ok := rp.(Trust); ok {
		return RoleTrust
	}
	return ResourceBased
}

// requesterOf is the principal with every identifier a principal element can
// name: a user's ARN and unique id, a session's ARN, its role's ARN with the
// role's path, and the role's unique id (iam-evaluation.md 3.4).
func requesterOf(p identity.Principal) policydoc.Requester {
	switch p := p.(type) {
	case identity.Root:
		return policydoc.RootRequester{Account: p.Account.String()}
	case identity.User:
		return policydoc.UserRequester{Account: p.Account.String(), ARN: identity.Render(p), ID: p.ID}
	case identity.AssumedRole:
		return policydoc.SessionRequester{Account: p.Account.String(), ARN: identity.Render(p), RoleARN: roleARN(p), RoleID: p.Role.ID}
	case identity.ServicePrincipal:
		return policydoc.ServiceRequester{Name: p.Name}
	case identity.Anonymous:
	}
	return policydoc.AnonymousRequester{}
}

// roleARN is a session's role as arn:aws:iam::<account>:role<path><name>.
func roleARN(p identity.AssumedRole) string {
	path := p.Role.Path
	if path == "" {
		path = "/"
	}
	return aws.ARN{Partition: aws.PartitionAWS().String(), Service: "iam", Account: p.Account.String(), Resource: "role" + path + p.Role.Name}.String()
}

// resourceText is the resource as a statement's Resource element matches it:
// the ARN, or "*" for a pair on no resource.
func resourceText(r Resource) string {
	if on, ok := r.(OnResource); ok {
		return on.ARN.String()
	}
	return "*"
}

// ownerOf is the account a pair is asked in: the resource's owner, or the
// account of a pair on no resource; zero for a nil Resource.
func ownerOf(r Resource) aws.AccountID {
	switch r := r.(type) {
	case OnResource:
		return r.Owner
	case AnyResource:
		return r.Account
	}
	return aws.AccountID{}
}

// decide runs the steps of iam-evaluation.md 2.2, in order.
func (e *evaluated) decide(r Request) Decision {
	if d, denied := e.explicitDeny(r); denied {
		return d
	}
	switch p := r.Principal.(type) {
	case identity.Root:
		return e.root(p, r.Resource)
	case identity.User:
		return e.iamPrincipal(p.Account, r.Resource)
	case identity.AssumedRole:
		return e.iamPrincipal(p.Account, r.Resource)
	case identity.ServicePrincipal, identity.Anonymous, nil:
	}
	return e.serviceOrAnonymous()
}

// explicitDeny is step 1 (iam-evaluation.md 2.2): the first policy with an
// applying Deny, in the order identity, boundary, session, resource
// (iam-evaluation.md 12.4, inferred), names the denial.
func (e *evaluated) explicitDeny(r Request) (ExplicitDeny, bool) {
	if arn, denied := firstDeny(e.identity); denied {
		return ExplicitDeny{PolicyType: IdentityBased, PolicyARN: arn}, true
	}
	if e.boundary != nil && len(e.boundary.result.Denies) > 0 {
		return ExplicitDeny{PolicyType: PermissionsBoundary, PolicyARN: e.boundary.policy.ARN}, true
	}
	if arn, denied := firstDeny(e.session); denied {
		return ExplicitDeny{PolicyType: SessionPolicy, PolicyARN: arn}, true
	}
	if len(e.onResource.Denies) > 0 && !rootKeepsBucketPolicy(r) {
		return ExplicitDeny{PolicyType: resourceType(e.resource)}, true
	}
	return ExplicitDeny{}, false
}

// rootKeepsBucketPolicy reports whether r is the bucket owner's root calling
// GetBucketPolicy, PutBucketPolicy or DeleteBucketPolicy, which a bucket
// policy's Deny cannot refuse "even if their bucket policy explicitly denies
// the root principal's access" (iam-evaluation.md 2.6, S3-PBP; for Delete
// also iam-enforcement-surfaces.md 6.3). The three actions are switch
// s3.root.bucket-policy-lockout at its value "get-put-delete" (IAM-ENF-31).
func rootKeepsBucketPolicy(r Request) bool {
	root, isRoot := r.Principal.(identity.Root)
	on, isOn := r.Resource.(OnResource)
	if !isRoot || !isOn || on.ARN.Service != "s3" || on.Owner != root.Account || !strings.EqualFold(r.Action.Prefix, "s3") {
		return false
	}
	return slices.ContainsFunc([]string{"GetBucketPolicy", "PutBucketPolicy", "DeleteBucketPolicy"}, func(name string) bool {
		return strings.EqualFold(name, r.Action.Name)
	})
}

// root is step 2 for the account root user (iam-evaluation.md 2.6): full
// access in its own account, modeled as an identity-policy Allow of every
// action on every resource of that account. It cannot call AssumeRole
// (iam-evaluation.md 3.5, S-STSCMP), reaches a KMS key only through a key
// policy statement naming its account or "*" (4.3, K-POL), and in another
// account needs the resource policy to allow it (2.4).
func (e *evaluated) root(p identity.Root, r Resource) Decision {
	switch e.resource.(type) {
	case Trust:
		return ImplicitDeny{PolicyType: IdentityBased}
	case KeyPolicy:
		if e.resourceAllowsBy(func(m policydoc.Match) bool {
			switch m.(type) {
			case policydoc.MatchAccount, policydoc.MatchWildcard:
				return true
			case policydoc.MatchNoPrincipal, policydoc.MatchSession, policydoc.MatchUser, policydoc.MatchRole,
				policydoc.MatchService, policydoc.MatchNotPrincipal:
			}
			return false
		}) {
			return Allow{}
		}
		return ImplicitDeny{PolicyType: ResourceBased}
	case Generic, NoResourcePolicy:
	}
	if ownerOf(r) == p.Account || len(e.onResource.Allows) > 0 {
		return Allow{}
	}
	return ImplicitDeny{PolicyType: ResourceBased}
}

// serviceOrAnonymous is step 3 for a service principal or an anonymous caller,
// which have no identity policy: only the resource policy can allow them
// (iam-evaluation.md 2.9, 2.10).
func (e *evaluated) serviceOrAnonymous() Decision {
	if e.resourceAllowsBy(func(m policydoc.Match) bool {
		switch m.(type) {
		case policydoc.MatchService, policydoc.MatchWildcard:
			return true
		case policydoc.MatchNoPrincipal, policydoc.MatchSession, policydoc.MatchUser, policydoc.MatchRole,
			policydoc.MatchAccount, policydoc.MatchNotPrincipal:
		}
		return false
	}) {
		return Allow{}
	}
	return ImplicitDeny{PolicyType: resourceType(e.resource)}
}

// iamPrincipal is steps 3 to 6 for a user or a role session of account.
//
// In the resource's account a resource-policy Allow is worth what the
// principal form it names makes it (iam-evaluation.md 2.3). The session, the
// user, or "*" allow alone: "*" with an aws:PrincipalArn condition always,
// and with none for a role session too, under switch
// iam.wildcard-principal.role-session "direct" (IAM-EVAL-19). The role allows
// when the boundary and the session policies do; for a key policy too, under
// switch kms.key-policy.role-boundary "limited" (IAM-EVAL-27). The account
// sends the request on to the identity policies. In a key policy "*" reads as
// the account (4.4, K-OVR). A trust policy and a key policy must allow at all
// (2.2 step 3).
//
// From another account the resource policy must allow by any form, and then
// the identity policies, boundary and session policies must allow, with no
// shortcut (2.4).
func (e *evaluated) iamPrincipal(account aws.AccountID, r Resource) Decision {
	if ownerOf(r) != account {
		if len(e.onResource.Allows) == 0 {
			return ImplicitDeny{PolicyType: resourceType(e.resource)}
		}
		return e.identityPath()
	}
	_, isKey := e.resource.(KeyPolicy)
	direct := e.resourceAllowsBy(func(m policydoc.Match) bool {
		switch m.(type) {
		case policydoc.MatchSession, policydoc.MatchUser:
			return true
		case policydoc.MatchWildcard:
			return !isKey
		case policydoc.MatchNoPrincipal, policydoc.MatchRole, policydoc.MatchAccount,
			policydoc.MatchService, policydoc.MatchNotPrincipal:
		}
		return false
	})
	if direct {
		return Allow{}
	}
	byRole := e.resourceAllowsBy(func(m policydoc.Match) bool {
		_, ok := m.(policydoc.MatchRole)
		return ok
	})
	if byRole && e.boundaryAllows() && e.sessionAllows() {
		return Allow{}
	}
	switch e.resource.(type) {
	case Trust, KeyPolicy:
		if len(e.onResource.Allows) == 0 {
			return ImplicitDeny{PolicyType: resourceType(e.resource)}
		}
	case Generic, NoResourcePolicy:
	}
	return e.identityPath()
}

// identityPath is steps 4 to 6 (iam-evaluation.md 2.2): an identity policy
// must allow, then the boundary when there is one, then one of the session
// policies when any were passed.
func (e *evaluated) identityPath() Decision {
	if !anyAllows(e.identity) {
		return ImplicitDeny{PolicyType: IdentityBased}
	}
	if !e.boundaryAllows() {
		return ImplicitDeny{PolicyType: PermissionsBoundary}
	}
	if !e.sessionAllows() {
		return ImplicitDeny{PolicyType: SessionPolicy}
	}
	return Allow{}
}

func (e *evaluated) boundaryAllows() bool {
	return e.boundary == nil || len(e.boundary.result.Allows) > 0
}

// sessionAllows is step 6: no session policy passed allows (iam-evaluation.md
// 2.2), else one of them must.
func (e *evaluated) sessionAllows() bool {
	return len(e.session) == 0 || anyAllows(e.session)
}

// firstDeny returns the ARN of the first of ps with an applying Deny, "" for
// an inline policy.
func firstDeny(ps []evaluatedPolicy) (arn string, denied bool) {
	for i := range ps {
		if len(ps[i].result.Denies) > 0 {
			return ps[i].policy.ARN, true
		}
	}
	return "", false
}

func anyAllows(ps []evaluatedPolicy) bool {
	return slices.ContainsFunc(ps, func(ep evaluatedPolicy) bool { return len(ep.result.Allows) > 0 })
}

// resourceAllowsBy reports whether an applying Allow of the resource policy
// names the requester in a way form accepts.
func (e *evaluated) resourceAllowsBy(form func(policydoc.Match) bool) bool {
	return slices.ContainsFunc(e.onResource.Allows, func(a policydoc.Applied) bool {
		return slices.ContainsFunc(a.Matches, form)
	})
}
