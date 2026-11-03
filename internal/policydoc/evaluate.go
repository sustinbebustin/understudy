package policydoc

import (
	"slices"
	"strings"
)

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
// A statement applies when its Effect is Allow or Deny, its principal element
// names the requester (principalMatches), some Action pattern (or no
// NotAction pattern) matches, some Resource pattern (or no NotResource
// pattern) matches, and every Condition clause holds. Once the principal and
// action match, every resource pattern and every clause is evaluated, so each
// key or variable the context marks Unsupplied is reported once per
// statement, even after another clause failed.
func Evaluate(doc Document, kind Kind, r Request) Result {
	var res Result
	for i, st := range doc.Statements {
		ref := StatementRef{Index: i, Sid: st.Sid}
		matches, applies, unsupplied := st.evaluate(doc.Version, kind, r)
		for _, key := range unsupplied {
			res.Unsupplied = append(res.Unsupplied, UnsuppliedKey{Key: key, Statement: ref})
		}
		if !applies {
			continue
		}
		applied := Applied{Statement: ref, Matches: matches}
		switch st.Effect {
		case Allow:
			res.Allows = append(res.Allows, applied)
		case Deny:
			res.Denies = append(res.Denies, applied)
		}
	}
	return res
}

// evaluate reports how st's principal element named the requester, whether
// st applies, and the unsupplied keys and variables it tested.
func (st Statement) evaluate(v Version, kind Kind, r Request) (matches []Match, applies bool, unsupplied []string) {
	if st.Effect != Allow && st.Effect != Deny {
		return nil, false, nil
	}
	matches = st.principalMatches(kind, r)
	if len(matches) == 0 || !actionMatches(st.Action, r.Action, v) {
		return nil, false, nil
	}
	s := &statementEval{ctx: r.Context, version: v}
	applies = s.resourceMatches(st.Resource, r.Resource, kind)
	if applies {
		for _, cl := range st.Condition {
			applies = s.holds(cl) && applies
		}
	}
	if !applies {
		return nil, false, s.unsupplied
	}
	return matches, true, s.unsupplied
}

// statementEval evaluates one statement's resource and condition against a
// request context, collecting the unsupplied names it meets.
type statementEval struct {
	ctx     *Context
	version Version
	// unsupplied are the key and variable names met, as the document spells
	// them, each once whatever its case.
	unsupplied []string
}

// lookup is ctx.Lookup, with an Unsupplied key reported and read as Absent.
func (s *statementEval) lookup(key string) (ContextValue, Presence) {
	value, presence := s.ctx.Lookup(key)
	if presence == Unsupplied {
		s.report(key)
		return ContextValue{}, Absent
	}
	return value, presence
}

func (s *statementEval) report(name string) {
	if !slices.ContainsFunc(s.unsupplied, func(k string) bool { return strings.EqualFold(k, name) }) {
		s.unsupplied = append(s.unsupplied, name)
	}
}

// resourceMatches is the resource element's test (iam-evaluation.md 5.3). An
// identity-policy statement needs a resource element; a resource-policy
// statement without one (a trust policy's) applies to the resource. Every
// pattern is evaluated, so every unsupplied variable is reported. A log
// group is matched as either of its ARNs, logGroupArn and the arn with the
// trailing ":*" (logGroupForms).
func (s *statementEval) resourceMatches(element Resources, resource string, kind Kind) bool {
	forms := logGroupForms(resource)
	matchesAny := func(patterns []string) bool {
		matched := false
		for _, p := range patterns {
			for _, r := range forms {
				matched = s.patternMatches(p, r, kind) || matched
			}
		}
		return matched
	}
	switch x := element.(type) {
	case nil:
		return kind == ResourcePolicy
	case Resource:
		return matchesAny(x)
	case NotResource:
		return !matchesAny(x)
	}
	return false
}

// patternMatches matches one Resource or NotResource pattern, case-sensitive
// with * spanning ":" and "/". In an identity policy an ARN with fewer than
// six fields is completed with * fields (iam-evaluation.md 5.3; a resource
// policy matches it as written, IAM-EVAL-11). In a 2012-10-17 document,
// variables are substituted after the fifth colon only (iam-evaluation.md
// 7.1); a pattern whose variable has no value matches nothing
// (iam-evaluation.md 7.3, IAM-EVAL-13). A request on no resource ("*")
// matches a pattern only as the text "*".
func (s *statementEval) patternMatches(pattern, resource string, kind Kind) bool {
	if kind == IdentityPolicy && strings.HasPrefix(pattern, "arn:") {
		if missing := 5 - strings.Count(pattern, ":"); missing > 0 {
			pattern += strings.Repeat(":*", missing)
		}
	}
	head, tail := pattern, ""
	if i := fifthColon(pattern); i >= 0 {
		head, tail = pattern[:i+1], pattern[i+1:]
	}
	e := expand(tail, s.version, s.ctx)
	for _, name := range e.unsupplied {
		s.report(name)
	}
	return e.resolved && match(append(compile(head, s.version), e.tokens...), resource)
}

// logGroupForms are the ARNs a request on resource is matched as: a Logs
// log group ARN (arn:<p>:logs:<r>:<a>:log-group:<name>) and its ":*" form,
// which DescribeLogGroups reports as arn, since a statement on either form
// allows the group-level actions (secrets-logs-events-alarms.md 3.10;
// IAM-ENF-42, aws-recorded); any other resource as itself.
func logGroupForms(resource string) []string {
	fields := strings.SplitN(resource, ":", 6)
	if len(fields) != 6 || fields[0] != "arn" || fields[2] != "logs" {
		return []string{resource}
	}
	if name, ok := strings.CutPrefix(fields[5], "log-group:"); !ok || name == "" || strings.Contains(name, ":") {
		return []string{resource}
	}
	return []string{resource, resource + ":*"}
}

// fifthColon returns the index of the fifth ":" in s, or -1.
func fifthColon(s string) int {
	at := -1
	for range 5 {
		i := strings.IndexByte(s[at+1:], ':')
		if i < 0 {
			return -1
		}
		at += i + 1
	}
	return at
}

// principalMatches lists how st's principal element names r's requester
// (iam-evaluation.md 2.3, 5.4), nil when it does not. An identity-policy
// statement has no principal element and matches as MatchNoPrincipal; a
// resource-policy statement needs one.
func (st Statement) principalMatches(kind Kind, r Request) []Match {
	switch kind {
	case IdentityPolicy:
		if st.Principal != nil {
			return nil
		}
		return []Match{MatchNoPrincipal{}}
	case ResourcePolicy:
		switch p := st.Principal.(type) {
		case Principal:
			return principalForms(p.Who, r.Requester, st.Condition)
		case NotPrincipal:
			// NotPrincipal "must be used with Deny" (iam-evaluation.md 5.4).
			if st.Effect != Deny || exempt(p.Who, r) {
				return nil
			}
			return []Match{MatchNotPrincipal{}}
		case nil:
		}
	}
	return nil
}

// identifiers are the values a principal element can name a requester by.
type identifiers struct {
	account string
	user    []string // the user's ARN and unique id
	session string   // the session's ARN
	role    []string // the session's role ARN and role unique id
	service string
}

func identify(r Requester) identifiers {
	switch x := r.(type) {
	case RootRequester:
		return identifiers{account: x.Account}
	case UserRequester:
		return identifiers{account: x.Account, user: nonEmpty(x.ARN, x.ID)}
	case SessionRequester:
		return identifiers{account: x.Account, session: x.ARN, role: nonEmpty(x.RoleARN, x.RoleID)}
	case ServiceRequester:
		return identifiers{service: x.Name}
	case AnonymousRequester, nil:
	}
	return identifiers{}
}

func nonEmpty(values ...string) []string {
	return slices.DeleteFunc(values, func(v string) bool { return v == "" })
}

// principalForms lists, in the order the Match variants are declared, how a
// Principal value names the requester. AWS values compare exactly and
// case-sensitively, with no partial wildcards; Federated and CanonicalUser
// values never name a requester (iam-evaluation.md 5.4). "*" names every
// requester, a service principal included (switch
// iam.wildcard-principal.service "matches", IAM-EVAL-33).
func principalForms(who Who, requester Requester, cond Condition) []Match {
	var named Named
	wildcard := false
	switch w := who.(type) {
	case Anyone:
		wildcard = true
	case Named:
		named, wildcard = w, slices.Contains(w.AWS, "*")
	}
	ids := identify(requester)
	var out []Match
	if ids.session != "" && slices.Contains(named.AWS, ids.session) {
		out = append(out, MatchSession{})
	}
	if listsAny(named.AWS, ids.user) {
		out = append(out, MatchUser{})
	}
	if listsAny(named.AWS, ids.role) {
		out = append(out, MatchRole{})
	}
	if namesAccount(named.AWS, ids.account) {
		out = append(out, MatchAccount{})
	}
	if wildcard {
		out = append(out, MatchWildcard{PrincipalARNCondition: conditionNamesPrincipal(cond)})
	}
	if ids.service != "" && slices.Contains(named.Service, ids.service) {
		out = append(out, MatchService{})
	}
	return out
}

func listsAny(values, ids []string) bool {
	return slices.ContainsFunc(ids, func(id string) bool { return slices.Contains(values, id) })
}

// namesAccount reports whether values name account by its id or its root ARN.
func namesAccount(values []string, account string) bool {
	if account == "" {
		return false
	}
	return slices.ContainsFunc(values, func(v string) bool {
		if v == account {
			return true
		}
		parts := strings.SplitN(v, ":", 6)
		return len(parts) == 6 && parts[0] == "arn" && parts[2] == "iam" && parts[3] == "" && parts[4] == account && parts[5] == "root"
	})
}

// conditionNamesPrincipal reports whether cond holds a positive ArnEquals,
// ArnLike, StringEquals or StringLike clause on aws:PrincipalArn with no set
// qualifier and no IfExists, which names the principal a "*" statement
// grants to (iam-evaluation.md 2.3).
func conditionNamesPrincipal(cond Condition) bool {
	return slices.ContainsFunc(cond, func(cl Clause) bool {
		return strings.EqualFold(cl.Key, "aws:PrincipalArn") && cl.Operator.Set == SingleValued && !cl.Operator.IfExists &&
			slices.Contains([]Test{ArnEquals, ArnLike, StringEquals, StringLike}, cl.Operator.Test)
	})
}

// exempt reports whether a NotPrincipal value exempts r's requester (switch
// iam.not-principal.match "every-level", aws-recorded, IAM-EVAL-18): "*"
// exempts everyone; a root is exempt when its account is listed, a user
// when its account and its ARN or unique id are, a session when its
// account, its role ARN or role unique id, and its session ARN all are (the
// one recorded list that let a session through), and a service when
// Service lists it; an anonymous requester never is. A user or session
// whose entity has a permissions boundary is never exempt
// (iam-evaluation.md 2.8).
func exempt(who Who, r Request) bool {
	ids := identify(r.Requester)
	switch r.Requester.(type) {
	case UserRequester, SessionRequester:
		if r.HasBoundary {
			return false
		}
	case RootRequester, ServiceRequester, AnonymousRequester, nil:
	}
	var named Named
	switch w := who.(type) {
	case Anyone:
		return true
	case Named:
		named = w
	}
	if slices.Contains(named.AWS, "*") {
		return true
	}
	switch r.Requester.(type) {
	case RootRequester:
		return namesAccount(named.AWS, ids.account)
	case UserRequester:
		return namesAccount(named.AWS, ids.account) && listsAny(named.AWS, ids.user)
	case SessionRequester:
		return namesAccount(named.AWS, ids.account) && listsAny(named.AWS, ids.role) && slices.Contains(named.AWS, ids.session)
	case ServiceRequester:
		return slices.Contains(named.Service, ids.service)
	case AnonymousRequester, nil:
	}
	return false
}

// RequesterOf maps a Caller onto a Requester for a caller that knows only an
// ARN: a root ARN is a RootRequester, an assumed-role ARN a SessionRequester
// whose RoleARN has path "/" and no RoleID, and any other ARN a UserRequester
// with no ID, whose Account is the ARN's account element; a ServiceCaller and
// Anonymous, or nil, map to their variants.
func RequesterOf(c Caller) Requester {
	switch x := c.(type) {
	case AWSCaller:
		return requesterOfARN(x.ARN)
	case ServiceCaller:
		return ServiceRequester(x)
	case Anonymous, nil:
	}
	return AnonymousRequester{}
}

func requesterOfARN(arn string) Requester {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" {
		return UserRequester{ARN: arn}
	}
	partition, service, account, resource := parts[1], parts[2], parts[4], parts[5]
	if service == "iam" && resource == "root" {
		return RootRequester{Account: account}
	}
	if rest, ok := strings.CutPrefix(resource, "assumed-role/"); ok && service == "sts" {
		if role, _, ok := strings.Cut(rest, "/"); ok {
			return SessionRequester{Account: account, ARN: arn, RoleARN: "arn:" + partition + ":iam::" + account + ":role/" + role}
		}
	}
	return UserRequester{Account: account, ARN: arn}
}
