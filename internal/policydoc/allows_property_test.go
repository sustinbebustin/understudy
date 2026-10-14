package policydoc_test

import (
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// refEscapes matches one of IAM's escapes, or failing that one character.
var refEscapes = regexp.MustCompile(`(?s)\$\{[*?$]\}|.`)

// refPieces splits a pattern into the pieces the reference matchers read:
// single characters and, when escapes is set (a 2012-10-17 document),
// whole escapes ${*}, ${?} and ${$}, taken leftmost first.
func refPieces(pattern string, escapes bool) []string {
	if escapes {
		return refEscapes.FindAllString(pattern, -1)
	}
	return strings.Split(pattern, "")
}

// refUnescape is the text a pattern stands for without wildcards: each
// escape, when escapes is set, replaced by the character it escapes.
func refUnescape(pattern string, escapes bool) string {
	var b strings.Builder
	for _, piece := range refPieces(pattern, escapes) {
		switch piece {
		case "${*}", "${?}", "${$}":
			b.WriteString(piece[2:3])
		default:
			b.WriteString(piece)
		}
	}
	return b.String()
}

// refGlob is the reference wildcard matcher: the pattern as an anchored
// regular expression, with * as .* and ? as one character, and an escape
// as the character it escapes.
func refGlob(pattern, s string, foldCase, escapes bool) bool {
	var b strings.Builder
	if foldCase {
		b.WriteString("(?i)")
	}
	b.WriteString("(?s)^")
	for _, piece := range refPieces(pattern, escapes) {
		switch piece {
		case "*":
			b.WriteString(".*")
		case "?":
			b.WriteString(".")
		case "${*}", "${?}", "${$}":
			b.WriteString(regexp.QuoteMeta(piece[2:3]))
		default:
			b.WriteString(regexp.QuoteMeta(piece))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String()).MatchString(s)
}

// refArnLike is the reference ARN matcher: the pattern as an anchored
// regular expression of six colon-separated elements, where a wildcard in
// the first five matches no colon and one in the resource element matches
// any character, and an escape is the character it escapes. A pattern with
// fewer than six elements matches nothing.
func refArnLike(pattern, s string, escapes bool) bool {
	if strings.Count(pattern, ":") < 5 {
		return false
	}
	var b strings.Builder
	b.WriteString("(?s)^")
	element := 0
	for _, piece := range refPieces(pattern, escapes) {
		last := element == 5
		switch {
		case piece == ":" && !last:
			element++
			b.WriteString(":")
		case piece == "*" && last:
			b.WriteString(".*")
		case piece == "*":
			b.WriteString("[^:]*")
		case piece == "?" && last:
			b.WriteString(".")
		case piece == "?":
			b.WriteString("[^:]")
		case piece == "${*}" || piece == "${?}" || piece == "${$}":
			b.WriteString(regexp.QuoteMeta(piece[2:3]))
		default:
			b.WriteString(regexp.QuoteMeta(piece))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String()).MatchString(s)
}

// refFragments are what generated patterns and values are built from:
// single characters, IAM's escapes and their fragments, so the property
// meets whole escapes, partial ones such as "${*" and "$${*}}", and
// lookalikes such as "${a}".
var refFragments = []string{"a", "A", "b", ":", "*", "?", "$", "{", "}", "${", "${*}", "${?}", "${$}"}

// genRefText draws a string of at most maxLen fragments.
func genRefText(maxLen int) *rapid.Generator[string] {
	return rapid.Map(rapid.SliceOfN(rapid.SampledFrom(refFragments), 0, maxLen), func(fs []string) string { return strings.Join(fs, "") })
}

// genRefValue draws a condition or context value: a short string, or one of
// five to seven short colon-separated elements so ARN matching sees values
// with six elements and more.
func genRefValue() *rapid.Generator[string] {
	return rapid.OneOf(
		genRefText(5),
		rapid.Map(rapid.SliceOfN(genRefText(2), 5, 7), func(es []string) string { return strings.Join(es, ":") }),
	)
}

// The callers and principal values the generator draws from; the reference
// spells out which value names which caller.
var (
	refRole    = policydoc.AWSCaller{ARN: "arn:aws:iam::111111111111:role/r"}
	refUser    = policydoc.AWSCaller{ARN: "arn:aws:iam::222222222222:user/u"}
	refSNS     = policydoc.ServiceCaller{Name: "sns.amazonaws.com"}
	refAnon    = policydoc.Anonymous{}
	refCallers = []policydoc.Caller{refRole, refUser, refSNS, refAnon}
	refNames   = map[string][]policydoc.Caller{
		"*":                                {refRole, refUser, refSNS, refAnon},
		"111111111111":                     {refRole},
		"arn:aws:iam::111111111111:root":   {refRole},
		"arn:aws:iam::111111111111:role/r": {refRole},
		"arn:aws:iam::222222222222:root":   {refUser},
		"arn:aws:iam::333333333333:root":   nil,
	}
	refServices = map[string][]policydoc.Caller{
		"sns.amazonaws.com":    {refSNS},
		"events.amazonaws.com": nil,
	}
	// refOperators are the condition operators the generator uses: three
	// Allows evaluates and one it never does.
	refOperators = map[string]policydoc.Test{
		"StringEquals":    policydoc.StringEquals,
		"StringLike":      policydoc.StringLike,
		"ArnLike":         policydoc.ArnLike,
		"NumericLessThan": policydoc.NumericLessThan,
	}
)

// refQuery is a generated request in plain data. The condition key is
// aws:SourceArn, which the context supplies under contextKey when
// hasContext is set.
type refQuery struct {
	caller           policydoc.Caller
	action, resource string
	hasContext       bool
	contextKey       string
	contextValue     string
}

// genRefQuery draws query values that may hold *, ? and escapes themselves,
// which are plain text in a request and special only in a policy. A value
// is often the text of one of the statements' patterns, where the versions
// disagree: ${*} matches the text "${*}" in a 2008-10-17 document and not
// in a 2012-10-17 one. A context value is often a condition value itself or
// its decoded text, the one StringEquals matches in a 2012-10-17 document.
func genRefQuery(t *rapid.T, statements []refStatement) refQuery {
	patterns := []string{""}
	conditions := make([]string, 0, 2*len(statements))
	for i := range statements {
		r := &statements[i]
		patterns = append(append(patterns, r.actionValues...), r.resourceValues...)
		patterns = append(patterns, r.conditionValue)
		conditions = append(conditions, r.conditionValue, refUnescape(r.conditionValue, true))
	}
	value := rapid.OneOf(genRefText(5), rapid.SampledFrom(patterns))
	return refQuery{
		caller:       rapid.SampledFrom(refCallers).Draw(t, "caller"),
		action:       value.Draw(t, "query-action"),
		resource:     value.Draw(t, "query-resource"),
		hasContext:   rapid.Bool().Draw(t, "has-context"),
		contextKey:   rapid.SampledFrom([]string{"aws:SourceArn", "AWS:SOURCEARN"}).Draw(t, "context-key"),
		contextValue: rapid.OneOf(genRefValue(), rapid.SampledFrom(patterns), rapid.SampledFrom(conditions)).Draw(t, "context-value"),
	}
}

func (q refQuery) query() policydoc.Query {
	out := policydoc.Query{Principal: q.caller, Action: q.action, Resource: q.resource}
	if q.hasContext {
		out.Context = map[string]string{q.contextKey: q.contextValue}
	}
	return out
}

// refStatement is a generated statement in plain data, so the reference
// evaluator never reads the policydoc types it checks; statement is the
// same statement for Allows.
type refStatement struct {
	allow                        bool
	principal                    string // "", "Principal" or "NotPrincipal"
	anyone                       bool
	awsValues, serviceValues     []string
	action, resource             string // "", "Action"/"NotAction", "Resource"/"NotResource"
	actionValues, resourceValues []string
	operator                     string // "" for no condition, else a key of refOperators
	conditionValue               string
	statement                    policydoc.Statement
}

func genRefStatement(t *rapid.T) refStatement {
	pattern := genRefText(5)
	r := refStatement{
		allow:          rapid.Bool().Draw(t, "allow"),
		principal:      rapid.SampledFrom([]string{"", "Principal", "NotPrincipal"}).Draw(t, "principal"),
		action:         rapid.SampledFrom([]string{"", "Action", "NotAction"}).Draw(t, "action"),
		resource:       rapid.SampledFrom([]string{"", "Resource", "NotResource"}).Draw(t, "resource"),
		actionValues:   rapid.SliceOfN(pattern, 0, 3).Draw(t, "actions"),
		resourceValues: rapid.SliceOfN(pattern, 0, 3).Draw(t, "resources"),
		operator:       rapid.SampledFrom(append([]string{""}, sortedKeys(refOperators)...)).Draw(t, "operator"),
		conditionValue: genRefValue().Draw(t, "condition-value"),
	}
	st := policydoc.Statement{Effect: policydoc.Deny}
	if r.allow {
		st.Effect = policydoc.Allow
	}
	if r.principal != "" {
		var who policydoc.Who
		if r.anyone = rapid.Bool().Draw(t, "anyone"); r.anyone {
			who = policydoc.Anyone{}
		} else {
			r.awsValues = rapid.SliceOfN(rapid.SampledFrom(sortedKeys(refNames)), 0, 2).Draw(t, "aws")
			r.serviceValues = rapid.SliceOfN(rapid.SampledFrom(sortedKeys(refServices)), 0, 2).Draw(t, "services")
			who = policydoc.Named{AWS: r.awsValues, Service: r.serviceValues}
		}
		if r.principal == "Principal" {
			st.Principal = policydoc.Principal{Who: who}
		} else {
			st.Principal = policydoc.NotPrincipal{Who: who}
		}
	}
	switch r.action {
	case "Action":
		st.Action = policydoc.Action(r.actionValues)
	case "NotAction":
		st.Action = policydoc.NotAction(r.actionValues)
	}
	switch r.resource {
	case "Resource":
		st.Resource = policydoc.Resource(r.resourceValues)
	case "NotResource":
		st.Resource = policydoc.NotResource(r.resourceValues)
	}
	if r.operator != "" {
		st.Condition = policydoc.Condition{{
			Operator: policydoc.Operator{Test: refOperators[r.operator]},
			Key:      "aws:SourceArn",
			Values:   []policydoc.Value{policydoc.StringValue(r.conditionValue)},
		}}
	}
	r.statement = st
	return r
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// matches is the brute-force test of the principal, action and resource
// elements: each present element names the query, and a statement without
// actions matches nothing. escapes is set for a 2012-10-17 document.
func (r refStatement) matches(q refQuery, escapes bool) bool {
	named := r.anyone
	for _, v := range r.awsValues {
		named = named || slices.Contains(refNames[v], q.caller)
	}
	for _, v := range r.serviceValues {
		named = named || slices.Contains(refServices[v], q.caller)
	}
	principalOK := r.principal == "" || (r.principal == "Principal") == named

	actionHit := slices.ContainsFunc(r.actionValues, func(p string) bool { return refGlob(p, q.action, true, escapes) })
	actionOK := r.action != "" && (r.action == "Action") == actionHit

	resourceHit := slices.ContainsFunc(r.resourceValues, func(p string) bool { return refGlob(p, q.resource, false, escapes) })
	resourceOK := r.resource == "" || (r.resource == "Resource") == resourceHit

	return principalOK && actionOK && resourceOK
}

// condition is the brute-force condition test: NumericLessThan is never
// evaluated, the others only when the query supplies the key.
func (r refStatement) condition(q refQuery, escapes bool) (holds, evaluated bool) {
	switch {
	case r.operator == "":
		return true, true
	case r.operator == "NumericLessThan" || !q.hasContext:
		return false, false
	case r.operator == "StringEquals":
		return refUnescape(r.conditionValue, escapes) == q.contextValue, true
	case r.operator == "ArnLike":
		return refArnLike(r.conditionValue, q.contextValue, escapes), true
	default:
		return refGlob(r.conditionValue, q.contextValue, false, escapes), true
	}
}

// refDecide combines the statements with IAM's single-policy logic: the
// first Deny that matches with its condition evaluated and holding wins,
// then any matching Allow; unevaluated conditions are assumed for Allow
// statements and leave Deny statements unsettled. Patterns read escapes
// only in a 2012-10-17 document.
func refDecide(version string, statements []refStatement, q refQuery) policydoc.Decision {
	escapes := version == "2012-10-17"
	var (
		allowed, firm bool
		assumed       []policydoc.Clause
		unsettled     []policydoc.UnsettledDeny
	)
	for i := range statements {
		r := &statements[i]
		if !r.matches(q, escapes) {
			continue
		}
		holds, evaluated := r.condition(q, escapes)
		switch {
		case evaluated && !holds:
		case !r.allow && evaluated:
			return policydoc.ExplicitDeny{Statement: policydoc.StatementRef{Index: i}}
		case !r.allow:
			unsettled = append(unsettled, policydoc.UnsettledDeny{Statement: policydoc.StatementRef{Index: i}, Unevaluated: r.statement.Condition})
		case evaluated:
			allowed, firm = true, true
		default:
			allowed = true
			assumed = append(assumed, r.statement.Condition...)
		}
	}
	if !allowed {
		return policydoc.ImplicitDeny{}
	}
	if firm {
		assumed = nil
	}
	return policydoc.Allowed{Assumed: assumed, Unsettled: unsettled}
}

// refVersions maps the Version element's text, "" for an absent one, to
// the document's Version.
var refVersions = map[string]policydoc.Version{
	"":           policydoc.VersionAbsent,
	"2008-10-17": policydoc.Version20081017,
	"2012-10-17": policydoc.Version20121017,
}

// TestAllowsAgreesWithBruteForce checks Allows against refDecide on random
// documents and queries. rapid runs it 100 times by default; -rapid.checks
// raises that.
func TestAllowsAgreesWithBruteForce(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		statements := rapid.SliceOfN(rapid.Custom(genRefStatement), 1, 3).Draw(t, "statements")
		q := genRefQuery(t, statements)
		version := rapid.SampledFrom([]string{"", "2008-10-17", "2012-10-17"}).Draw(t, "version")
		doc := policydoc.Document{Version: refVersions[version]}
		for _, r := range statements {
			doc.Statements = append(doc.Statements, r.statement)
		}
		got := policydoc.Allows(doc, q.query())
		if want := refDecide(version, statements, q); !reflect.DeepEqual(got, want) {
			t.Fatalf("Allows(%s) for %#v = %#v, want %#v", policydoc.Encode(doc), q.query(), got, want)
		}
	})
}

// StringEquals compares the context with the condition value's text, whose
// escapes a 2012-10-17 document decodes and other versions keep.
func TestStringEqualsAgreesWithTheReferenceOnEscapes(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		value := genRefText(5).Draw(t, "condition-value")
		version := rapid.SampledFrom([]string{"", "2008-10-17", "2012-10-17"}).Draw(t, "version")
		escapes := version == "2012-10-17"
		context := rapid.SampledFrom([]string{value, refUnescape(value, true), genRefText(5).Draw(t, "other")}).Draw(t, "context")
		doc := policydoc.Document{Version: refVersions[version], Statements: []policydoc.Statement{{
			Effect:   policydoc.Allow,
			Action:   policydoc.Action{"*"},
			Resource: policydoc.Resource{"*"},
			Condition: policydoc.Condition{{
				Operator: policydoc.Operator{Test: policydoc.StringEquals},
				Key:      "aws:SourceArn",
				Values:   []policydoc.Value{policydoc.StringValue(value)},
			}},
		}}}
		got := allowed(policydoc.Allows(doc, policydoc.Query{Action: "sns:Publish", Resource: "*", Context: map[string]string{"aws:SourceArn": context}}))
		if want := refUnescape(value, escapes) == context; got != want {
			t.Fatalf("Allows(%s) with aws:SourceArn %q = %t, want %t", policydoc.Encode(doc), context, got, want)
		}
	})
}

// Literal names exactly its input: in a rendered policy, the escaped name
// of a resource grants that resource and no other, whatever *, ?, $ and
// braces the name holds.
func TestLiteralMatchesOnlyItsInput(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		name := rapid.StringOfN(rapid.SampledFrom([]rune("a$*?{}")), 0, 6, -1)
		resource := "arn:aws:s3:::b/" + name.Draw(t, "granted")
		query := rapid.OneOf(rapid.Just(resource), rapid.Map(name, func(s string) string { return "arn:aws:s3:::b/" + s })).Draw(t, "queried")
		doc, ok := policydoc.Render([]policydoc.Grant{{Actions: []string{"s3:GetObject"}, Resources: []string{policydoc.Literal(resource)}}})
		if !ok {
			t.Fatal("Render reported no policy for one grant")
		}
		_, got := policydoc.Allows(doc, policydoc.Query{Action: "s3:GetObject", Resource: query}).(policydoc.Allowed)
		if want := query == resource; got != want {
			t.Fatalf("policy %s allows %s: %t, want %t", policydoc.Encode(doc), query, got, want)
		}
	})
}
