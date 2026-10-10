package policydoc

import (
	"slices"
	"strconv"
	"strings"
)

// Caller is who makes a request, in the forms a principal element names.
//
//sumtype:decl
type Caller interface{ isCaller() }

// AWSCaller is an IAM identity: a root, user, role or assumed-role ARN.
type AWSCaller struct{ ARN string }

// ServiceCaller is a service principal, such as "sns.amazonaws.com".
type ServiceCaller struct{ Name string }

// Anonymous is an unsigned request.
type Anonymous struct{}

func (AWSCaller) isCaller()     {}
func (ServiceCaller) isCaller() {}
func (Anonymous) isCaller()     {}

// Query is one request to evaluate against a policy.
type Query struct {
	// Principal is the caller; nil is Anonymous. Statements without a
	// principal element (identity policies) apply to every caller.
	Principal Caller
	// Action is the IAM action, such as "sqs:SendMessage".
	Action string
	// Resource is the resource ARN, or "*" for an action without one.
	Resource string
	// Context holds the condition keys the caller knows, such as
	// aws:SourceArn. Key names match case-insensitively, so differently
	// cased spellings are one key; when they carry different values the key
	// counts as not supplied.
	Context map[string]string
}

// Decision is the outcome of Allows, and says what it rests on.
//
//sumtype:decl
type Decision interface{ isDecision() }

// Allowed is a request an Allow statement matches and no Deny statement
// firmly matches.
type Allowed struct {
	// Assumed lists the clauses of the matching Allow statements that
	// Allows could not evaluate and treated as holding; it is empty when an
	// Allow statement matched with every clause evaluated.
	Assumed []Clause
	// Unsettled lists the Deny statements that match except for clauses
	// Allows could not evaluate; AWS denies the request if those clauses
	// hold.
	Unsettled []UnsettledDeny
}

// ImplicitDeny is a request no Allow statement matches and no Deny
// statement firmly matches.
type ImplicitDeny struct{}

// ExplicitDeny is a request a Deny statement matches with every condition
// clause evaluated and holding; Statement is the first such statement.
type ExplicitDeny struct{ Statement StatementRef }

func (Allowed) isDecision()      {}
func (ImplicitDeny) isDecision() {}
func (ExplicitDeny) isDecision() {}

// StatementRef names a statement by its position in Document.Statements
// and its Sid, "" when it has none.
type StatementRef struct {
	Index int
	Sid   string
}

// UnsettledDeny is a Deny statement whose Unevaluated clauses decide
// whether it applies.
type UnsettledDeny struct {
	Statement   StatementRef
	Unevaluated []Clause
}

// Allows evaluates doc for q with IAM's single-policy logic: an explicit
// Deny wins, then any Allow, else the request is implicitly denied. Actions
// match case-insensitively and resources case-sensitively, both with the *
// and ? wildcards; policy variables are not substituted. In a 2012-10-17
// document, ${*}, ${?} and ${$} stand for a literal *, ? and $ (see
// Literal).
//
// A condition clause is evaluated when its operator is a single-valued
// string or ARN test (with or without IfExists) and q.Context holds its key.
// A clause that is evaluated and fails makes its statement not apply. A
// clause Allows cannot evaluate is assumed to hold for an Allow statement
// and reported in Allowed.Assumed; a Deny statement with such a clause
// never denies, because an unknown condition must not produce a firm
// denial, and is reported in Allowed.Unsettled instead.
func Allows(doc Document, q Query) Decision {
	var (
		allowed, firm bool
		assumed       []Clause
		unsettled     []UnsettledDeny
	)
	for i, st := range doc.Statements {
		applies, unevaluated := st.appliesTo(q, doc.Version)
		if !applies {
			continue
		}
		ref := StatementRef{Index: i, Sid: st.Sid}
		switch st.Effect {
		case Allow:
			allowed = true
			firm = firm || len(unevaluated) == 0
			assumed = append(assumed, unevaluated...)
		case Deny:
			if len(unevaluated) == 0 {
				return ExplicitDeny{Statement: ref}
			}
			unsettled = append(unsettled, UnsettledDeny{Statement: ref, Unevaluated: unevaluated})
		}
	}
	if !allowed {
		return ImplicitDeny{}
	}
	if firm {
		assumed = nil
	}
	return Allowed{Assumed: assumed, Unsettled: unsettled}
}

// appliesTo reports whether every element of st matches q with no
// evaluated clause failing, and which clauses it could not evaluate. v is
// the version of the document st belongs to, which decides how its
// patterns read.
func (st Statement) appliesTo(q Query, v Version) (bool, []Clause) {
	if !principalMatches(st.Principal, q.Principal) || !actionMatches(st.Action, q.Action, v) || !resourceMatches(st.Resource, q.Resource, v) {
		return false, nil
	}
	var unevaluated []Clause
	for _, cl := range st.Condition {
		holds, evaluated := cl.evaluate(q.Context, v)
		if !evaluated {
			unevaluated = append(unevaluated, cl)
			continue
		}
		if !holds {
			return false, nil
		}
	}
	return true, unevaluated
}

func principalMatches(e PrincipalElement, c Caller) bool {
	switch p := e.(type) {
	case nil:
		return true
	case Principal:
		return whoMatches(p.Who, c)
	case NotPrincipal:
		return !whoMatches(p.Who, c)
	default:
		return false
	}
}

func whoMatches(who Who, c Caller) bool {
	switch w := who.(type) {
	case Anyone:
		return true
	case Named:
		if slices.Contains(w.AWS, "*") {
			return true
		}
		switch x := c.(type) {
		case AWSCaller:
			return slices.ContainsFunc(w.AWS, func(v string) bool { return awsValueMatches(v, x.ARN) })
		case ServiceCaller:
			return slices.Contains(w.Service, x.Name)
		case Anonymous, nil:
			return false
		}
	}
	return false
}

// awsValueMatches reports whether an AWS principal value names the caller:
// its exact ARN, or its account by id or root ARN.
func awsValueMatches(value, arn string) bool {
	if value == arn {
		return true
	}
	account := arnAccount(arn)
	if account == "" {
		return false
	}
	if value == account {
		return true
	}
	parts := strings.SplitN(value, ":", 6)
	return len(parts) == 6 && parts[0] == "arn" && parts[2] == "iam" && parts[4] == account && parts[5] == "root"
}

// arnAccount returns the account element of an ARN, "" when there is none.
func arnAccount(arn string) string {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 || parts[0] != "arn" {
		return ""
	}
	return parts[4]
}

func actionMatches(a Actions, action string, v Version) bool {
	matches := func(p string) bool { return glob(strings.ToLower(p), strings.ToLower(action), v) }
	switch x := a.(type) {
	case Action:
		return slices.ContainsFunc(x, matches)
	case NotAction:
		return !slices.ContainsFunc(x, matches)
	default:
		return false
	}
}

func resourceMatches(r Resources, resource string, v Version) bool {
	matches := func(p string) bool { return glob(p, resource, v) }
	switch x := r.(type) {
	case nil:
		return true
	case Resource:
		return slices.ContainsFunc(x, matches)
	case NotResource:
		return !slices.ContainsFunc(x, matches)
	default:
		return false
	}
}

// evaluate tests the clause against the context when it can; evaluated is
// false when the operator is not supported or the key is not in the context.
// v is the version of the clause's document.
func (cl Clause) evaluate(context map[string]string, v Version) (holds, evaluated bool) {
	if cl.Operator.Set != SingleValued {
		return false, false
	}
	actual, found := contextValue(context, cl.Key)
	if !found {
		return false, false
	}
	var match func(policy string) bool
	negate := false
	switch cl.Operator.Test {
	case StringEquals, StringNotEquals:
		match = func(p string) bool { return unescape(p, v) == actual }
		negate = cl.Operator.Test == StringNotEquals
	case StringEqualsIgnoreCase, StringNotEqualsIgnoreCase:
		match = func(p string) bool { return strings.EqualFold(unescape(p, v), actual) }
		negate = cl.Operator.Test == StringNotEqualsIgnoreCase
	case StringLike, StringNotLike:
		match = func(p string) bool { return glob(p, actual, v) }
		negate = cl.Operator.Test == StringNotLike
	case ArnEquals, ArnLike, ArnNotEquals, ArnNotLike:
		match = func(p string) bool { return arnMatches(p, actual, v) }
		negate = cl.Operator.Test == ArnNotEquals || cl.Operator.Test == ArnNotLike
	case NumericEquals, NumericNotEquals, NumericLessThan, NumericLessThanEquals, NumericGreaterThan, NumericGreaterThanEquals,
		DateEquals, DateNotEquals, DateLessThan, DateLessThanEquals, DateGreaterThan, DateGreaterThanEquals,
		Bool, BinaryEquals, IPAddress, NotIPAddress, Null:
		return false, false
	default:
		return false, false
	}
	matched := slices.ContainsFunc(cl.Values, func(v Value) bool { return match(valueText(v)) })
	return matched != negate, true
}

// contextValue returns the value of key in context, whose keys match
// case-insensitively. found is false when no spelling of key is present, or
// when two spellings carry different values, because then the caller's
// context does not say which value holds.
func contextValue(context map[string]string, key string) (value string, found bool) {
	for k, v := range context {
		if !strings.EqualFold(k, key) {
			continue
		}
		if found && v != value {
			return "", false
		}
		value, found = v, true
	}
	return value, found
}

// arnMatches is the ARN operators' test: pattern and arn each have six
// colon-separated elements, and each element of pattern globs the same
// element of arn, so a * never spans a colon outside the resource element.
func arnMatches(pattern, arn string, v Version) bool {
	p, a := strings.SplitN(pattern, ":", 6), strings.SplitN(arn, ":", 6)
	if len(p) != 6 || len(a) != 6 {
		return false
	}
	for i := range p {
		if !glob(p[i], a[i], v) {
			return false
		}
	}
	return true
}

// valueText is the string a condition value compares as.
func valueText(v Value) string {
	switch x := v.(type) {
	case StringValue:
		return string(x)
	case BoolValue:
		return strconv.FormatBool(bool(x))
	case NumberValue:
		return string(x)
	default:
		return ""
	}
}

// glob reports whether s matches pattern, a pattern of a document of
// version v, where * matches any run of characters, ":" and "/" included,
// and ? matches one character. Only the pattern has wildcards: a * or ? in
// s is a plain character.
func glob(pattern, s string, v Version) bool {
	p, t := compile(pattern, v), []rune(s)
	pi, ti := 0, 0
	star, mark := -1, 0
	for ti < len(t) {
		switch {
		case pi < len(p) && p[pi].kind == anyRun:
			star, mark = pi, ti
			pi++
		case pi < len(p) && (p[pi].kind == anyOne || p[pi].kind == literal && p[pi].r == t[ti]):
			pi++
			ti++
		case star >= 0:
			pi = star + 1
			mark++
			ti = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi].kind == anyRun {
		pi++
	}
	return pi == len(p)
}

// tokenKind is what one element of a compiled pattern matches.
type tokenKind uint8

const (
	literal tokenKind = iota // its rune
	anyRun                   // any run of characters, the * wildcard
	anyOne                   // one character, the ? wildcard
)

type token struct {
	kind tokenKind
	r    rune // the rune a literal matches
}

// compile splits pattern into tokens. In a 2012-10-17 document the escapes
// ${*}, ${?} and ${$} are literal tokens; other versions have no escapes.
func compile(pattern string, v Version) []token {
	rs := []rune(pattern)
	out := make([]token, 0, len(rs))
	for i := 0; i < len(rs); i++ {
		if r, ok := escapeAt(rs, i, v); ok {
			out = append(out, token{kind: literal, r: r})
			i += 3
			continue
		}
		switch rs[i] {
		case '*':
			out = append(out, token{kind: anyRun})
		case '?':
			out = append(out, token{kind: anyOne})
		default:
			out = append(out, token{kind: literal, r: rs[i]})
		}
	}
	return out
}

// escapeAt reports whether rs[i:] opens one of a 2012-10-17 document's
// escapes ${*}, ${?} and ${$}, and the character it stands for. Other
// versions have no escapes.
func escapeAt(rs []rune, i int, v Version) (rune, bool) {
	if v == Version20121017 && i+3 < len(rs) && rs[i] == '$' && rs[i+1] == '{' && rs[i+3] == '}' && strings.ContainsRune("*?$", rs[i+2]) {
		return rs[i+2], true
	}
	return 0, false
}

// unescape decodes the escapes of p, a value of a document of version v, for
// the operators that compare text without wildcards: IAM reads the escapes
// in any string that takes policy variables.
func unescape(p string, v Version) string {
	if v != Version20121017 || !strings.Contains(p, "${") {
		return p
	}
	rs := []rune(p)
	var b strings.Builder
	for i := 0; i < len(rs); i++ {
		if r, ok := escapeAt(rs, i, v); ok {
			b.WriteRune(r)
			i += 3
			continue
		}
		b.WriteRune(rs[i])
	}
	return b.String()
}

// Literal returns s as a 2012-10-17 pattern that matches s alone: * and ?
// become IAM's escapes ${*} and ${?}, and a $ that opens "${" becomes ${$},
// so that no part of s reads as a wildcard or a policy variable. Other
// characters, a lone $ included, are already literal. The result is valid
// only in a 2012-10-17 document, the version Render writes.
func Literal(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == '*':
			b.WriteString("${*}")
		case r == '?':
			b.WriteString("${?}")
		case r == '$' && strings.HasPrefix(s[i+1:], "{"):
			b.WriteString("${$}")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
