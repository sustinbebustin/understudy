package policydoc

// Kind is the role a document plays in an evaluation, which decides whether
// its statements need a principal element and how an incomplete Resource ARN
// reads.
type Kind uint8

// Kind values.
const (
	// IdentityPolicy is an identity-based policy, a permissions boundary or a
	// session policy: its statements have no principal element, and an
	// incomplete ARN in Resource or NotResource is completed with wildcards
	// (iam-evaluation.md 5.3).
	IdentityPolicy Kind = iota
	// ResourcePolicy is a resource-based policy, a role trust policy or a
	// KMS key policy: every statement needs a principal element, and an
	// incomplete ARN is matched as written (iam-evaluation.md 5.3,
	// IAM-EVAL-11).
	ResourcePolicy
)

// Requester is who makes a request, with every identifier a principal element
// can name (iam-evaluation.md 3.4, 5.4).
//
//sumtype:decl
type Requester interface{ isRequester() }

// RootRequester is an account's root user.
type RootRequester struct{ Account string }

// UserRequester is an IAM user. ARN is arn:aws:iam::<account>:user<path><name>
// and ID its unique AIDA... id, "" when unknown.
type UserRequester struct{ Account, ARN, ID string }

// SessionRequester is a role session. ARN is the session's
// arn:aws:sts::<account>:assumed-role/<role>/<session>, RoleARN the role's
// ARN with its path, and RoleID the role's unique AROA... id, "" when unknown.
type SessionRequester struct{ Account, ARN, RoleARN, RoleID string }

// ServiceRequester is a service principal, such as "sns.amazonaws.com".
type ServiceRequester struct{ Name string }

// AnonymousRequester is an unsigned request.
type AnonymousRequester struct{}

func (RootRequester) isRequester()      {}
func (UserRequester) isRequester()      {}
func (SessionRequester) isRequester()   {}
func (ServiceRequester) isRequester()   {}
func (AnonymousRequester) isRequester() {}

// Request is one request to evaluate against one document.
type Request struct {
	Requester Requester
	// Action is the IAM action, such as "sqs:SendMessage".
	Action string
	// Resource is the resource ARN, or "*" for a request on no resource.
	Resource string
	// Context is the request context; nil is an empty context.
	Context *Context
	// HasBoundary is set when the requester is an IAM user or role session
	// whose entity has a permissions boundary, which a NotPrincipal Deny
	// never exempts (iam-evaluation.md 2.8).
	HasBoundary bool
}

// Result lists the statements of a document that apply to a request.
type Result struct {
	// Allows and Denies are the applying Allow and Deny statements, in
	// document order.
	Allows, Denies []Applied
	// Unsupplied are the condition keys and variable names, as the document
	// spells them, that a statement whose principal, action and resource
	// matched tested while the context marks them Unsupplied.
	Unsupplied []UnsuppliedKey
}

// Applied is one statement that applies, and how its principal element named
// the requester.
type Applied struct {
	Statement StatementRef
	// Matches are the ways the principal element matched, in the order the
	// Match variants are declared; for an IdentityPolicy document exactly
	// MatchNoPrincipal.
	Matches []Match
}

// UnsuppliedKey is a key a statement tested that the context could not
// supply.
type UnsuppliedKey struct {
	Key       string
	Statement StatementRef
}

// Match is one way a statement's principal element named the requester
// (iam-evaluation.md 2.3).
//
//sumtype:decl
type Match interface{ isMatch() }

// MatchNoPrincipal is a statement of an identity policy, which has no
// principal element.
type MatchNoPrincipal struct{}

// MatchSession is the role session's own ARN.
type MatchSession struct{}

// MatchUser is the user's ARN or unique id.
type MatchUser struct{}

// MatchRole is the session's role ARN or role unique id.
type MatchRole struct{}

// MatchAccount is the requester's account id or arn:aws:iam::<account>:root.
type MatchAccount struct{}

// MatchWildcard is "*" or {"AWS": "*"}. PrincipalARNCondition is set when the
// statement's Condition holds a single-valued positive ArnEquals, ArnLike,
// StringEquals or StringLike clause, without IfExists, on aws:PrincipalArn,
// which then names the principal (iam-evaluation.md 2.3).
type MatchWildcard struct{ PrincipalARNCondition bool }

// MatchService is a Service value naming the service principal.
type MatchService struct{}

// MatchNotPrincipal is a NotPrincipal element that does not exempt the
// requester.
type MatchNotPrincipal struct{}

func (MatchNoPrincipal) isMatch()  {}
func (MatchSession) isMatch()      {}
func (MatchUser) isMatch()         {}
func (MatchRole) isMatch()         {}
func (MatchAccount) isMatch()      {}
func (MatchWildcard) isMatch()     {}
func (MatchService) isMatch()      {}
func (MatchNotPrincipal) isMatch() {}

// Evaluate returns the statements of doc that apply to r when doc plays the
// role kind, by the rules of iam-evaluation.md 5 to 7: principal forms,
// actions, resources with policy variables, and every condition operator with
// the absent-key truth table of 6.14. It never assumes a clause holds.
//
// Not implemented yet: it returns an empty Result.
func Evaluate(_ Document, _ Kind, _ Request) Result {
	return Result{}
}

// RequesterOf maps a Caller onto a Requester for a caller that knows only an
// ARN: a root ARN is a RootRequester, an assumed-role ARN a SessionRequester
// whose RoleARN has path "/" and no RoleID, and any other ARN a UserRequester
// with no ID; a ServiceCaller and Anonymous, or nil, map to their variants.
//
// Not implemented yet: it returns AnonymousRequester.
func RequesterOf(_ Caller) Requester {
	return AnonymousRequester{}
}
