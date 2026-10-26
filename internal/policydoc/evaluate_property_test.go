package policydoc_test

import (
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// The rules of iam-evaluation.md 6.14 and 7 as properties (12.1 item 6):
// negation duality, IfExists, the set operators on empty and absent keys,
// Null, an unsupplied key reading as absent, and key-name and action case.
// None needs an oracle for the operators themselves: each compares two
// evaluations of the same request.

// opPair is a positive operator and its negation (iam-evaluation.md 6.1,
// 6.14: the negated operator is NOR over the policy values of the positive
// test).
type opPair struct {
	positive, negated string
	// values are request and policy values the pair compares, including
	// ones the operator cannot parse.
	values []string
}

var opPairs = []opPair{
	{"StringEquals", "StringNotEquals", []string{"a", "A", "b", "", "a b"}},
	{"StringEqualsIgnoreCase", "StringNotEqualsIgnoreCase", []string{"a", "A", "b", "ab"}},
	{"StringLike", "StringNotLike", []string{"a", "a*", "*", "?", "ab", "b?"}},
	{"NumericEquals", "NumericNotEquals", []string{"1", "2", "10", "1.5", "x"}},
	{"DateEquals", "DateNotEquals", []string{"2020-01-01T00:00:00Z", "2020-06-01T00:00:00Z", "1577836800", "never"}},
	{"IpAddress", "NotIpAddress", []string{"203.0.113.0/24", "203.0.113.1", "198.51.100.1", "2001:db8::/32", "2001:db8::1", "bad"}},
	{"ArnEquals", "ArnNotEquals", []string{"arn:aws:sns:us-east-1:111122223333:t", "arn:aws:sns:*:111122223333:t", "arn:aws:sqs:us-east-1:111122223333:q", "not-an-arn"}},
	{"ArnLike", "ArnNotLike", []string{"arn:aws:s3:::b", "arn:aws:s3:::b*", "arn:aws:s3:::*", "arn:aws:s3:::c"}},
}

// propKey is the key the properties test, spelled in the policy.
const propKey = "test:Key"

// clauseDoc is a one-statement condition document with one clause on
// propKey.
func clauseDoc(operator string, values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = `"` + v + `"`
	}
	return `{"` + operator + `":{"` + propKey + `":[` + strings.Join(quoted, ",") + `]}}`
}

// drawValues draws one to three policy values from pool.
func drawValues(t *rapid.T, pool []string) []string {
	return rapid.SliceOfN(rapid.SampledFrom(pool), 1, 3).Draw(t, "policy values")
}

// For a present single-valued key, the negated operator holds exactly when
// the positive one does not (iam-evaluation.md 6.1, 6.14).
func TestPropertyNegationDuality(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := rapid.SampledFrom(opPairs).Draw(t, "operators")
		policy := drawValues(t, p.values)
		ctx := contextOf(one(propKey, rapid.SampledFrom(p.values).Draw(t, "request value")))
		pos := condHolds(t, "Allow", clauseDoc(p.positive, policy), ctx)
		neg := condHolds(t, "Allow", clauseDoc(p.negated, policy), ctx)
		if pos == neg {
			t.Fatalf("%s and %s of %v both evaluate %t for a present key", p.positive, p.negated, policy, pos)
		}
	})
}

// For an absent key every positive operator is false and every negated one
// true (iam-evaluation.md 6.2, 6.14, S-OPS Important).
func TestPropertyAbsentKeyRule(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := rapid.SampledFrom(opPairs).Draw(t, "operators")
		policy := drawValues(t, p.values)
		if condHolds(t, "Allow", clauseDoc(p.positive, policy), contextOf()) {
			t.Fatalf("%s of %v holds for an absent key", p.positive, policy)
		}
		if !condHolds(t, "Allow", clauseDoc(p.negated, policy), contextOf()) {
			t.Fatalf("%s of %v fails for an absent key", p.negated, policy)
		}
	})
}

// <op>IfExists is <op> when the key is present and true when it is absent
// (iam-evaluation.md 6.10, 6.14), for positive and negated operators.
func TestPropertyIfExists(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := rapid.SampledFrom(opPairs).Draw(t, "operators")
		op := rapid.SampledFrom([]string{p.positive, p.negated}).Draw(t, "operator")
		policy := drawValues(t, p.values)
		present := contextOf(one(propKey, rapid.SampledFrom(p.values).Draw(t, "request value")))
		if base, ifExists := condHolds(t, "Allow", clauseDoc(op, policy), present), condHolds(t, "Allow", clauseDoc(op+"IfExists", policy), present); base != ifExists {
			t.Fatalf("present key: %s = %t but %sIfExists = %t", op, base, op, ifExists)
		}
		if !condHolds(t, "Allow", clauseDoc(op+"IfExists", policy), contextOf()) {
			t.Fatalf("absent key: %sIfExists of %v is false", op, policy)
		}
	})
}

// ForAllValues on an absent key, on a present key with no values and on a
// key whose only value is "" is true; ForAnyValue on an absent key or one
// with no values is false (iam-evaluation.md 6.12, 6.14; S-SETS
// #reference_policies_condition-forallvalues, -foranyvalue; S-OPS
// #conditions_arn_multivalued "resolves to a null dataset, such as an
// empty string").
func TestPropertySetOperatorsOnEmptySets(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := rapid.SampledFrom(opPairs).Draw(t, "operators")
		op := rapid.SampledFrom([]string{p.positive, p.negated}).Draw(t, "operator")
		policy := drawValues(t, p.values)
		for name, ctx := range map[string]*policydoc.Context{
			"absent":       contextOf(),
			"no values":    contextOf(many(propKey)),
			"empty string": contextOf(many(propKey, "")),
		} {
			if !condHolds(t, "Allow", clauseDoc("ForAllValues:"+op, policy), ctx) {
				t.Fatalf("ForAllValues:%s of %v is false on a key that is %s", op, policy, name)
			}
		}
		for name, ctx := range map[string]*policydoc.Context{"absent": contextOf(), "no values": contextOf(many(propKey))} {
			if condHolds(t, "Allow", clauseDoc("ForAnyValue:"+op, policy), ctx) {
				t.Fatalf("ForAnyValue:%s of %v is true on a key that is %s", op, policy, name)
			}
		}
	})
}

// ForAllValues holds exactly when every request value satisfies the
// operator alone, and ForAnyValue when some does (S-SETS; 6.14 rows), so
// each is the conjunction or disjunction of single-valued evaluations.
// Request values are non-empty: an empty value has its own rule
// (IAM-EVAL-04).
func TestPropertySetOperatorsAgreeWithSingleValues(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := rapid.SampledFrom(opPairs).Draw(t, "operators")
		op := rapid.SampledFrom([]string{p.positive, p.negated}).Draw(t, "operator")
		policy := drawValues(t, p.values)
		nonEmpty := slices.DeleteFunc(slices.Clone(p.values), func(v string) bool { return v == "" })
		request := rapid.SliceOfN(rapid.SampledFrom(nonEmpty), 1, 3).Draw(t, "request values")
		every, some := true, false
		for _, r := range request {
			holds := condHolds(t, "Allow", clauseDoc(op, policy), contextOf(one(propKey, r)))
			every = every && holds
			some = some || holds
		}
		ctx := contextOf(many(propKey, request...))
		if got := condHolds(t, "Allow", clauseDoc("ForAllValues:"+op, policy), ctx); got != every {
			t.Fatalf("ForAllValues:%s of %v on %v = %t, want %t", op, policy, request, got, every)
		}
		if got := condHolds(t, "Allow", clauseDoc("ForAnyValue:"+op, policy), ctx); got != some {
			t.Fatalf("ForAnyValue:%s of %v on %v = %t, want %t", op, policy, request, got, some)
		}
	})
}

// Null "true" holds exactly when the key is absent and "false" exactly
// when it is present with a value (iam-evaluation.md 6.11, 6.14), whatever
// the value's text.
func TestPropertyNull(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		value := rapid.StringMatching(`[a-zA-Z0-9:/*?.-]{1,8}`).Draw(t, "value")
		present := contextOf(one(propKey, value))
		if condHolds(t, "Allow", clauseDoc("Null", []string{"true"}), present) || !condHolds(t, "Allow", clauseDoc("Null", []string{"false"}), present) {
			t.Fatalf("Null on a key present with %q: want true false, got the reverse", value)
		}
		if !condHolds(t, "Allow", clauseDoc("Null", []string{"true"}), contextOf()) || condHolds(t, "Allow", clauseDoc("Null", []string{"false"}), contextOf()) {
			t.Fatal(`Null on an absent key: want "true" to hold and "false" not to`)
		}
	})
}

// An unsupplied key decides exactly as an absent one (iam-enforcement-surfaces.md
// 12.2; the spec's settled row 2) and is always reported.
func TestPropertyUnsuppliedReadsAsAbsent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := rapid.SampledFrom(opPairs).Draw(t, "operators")
		op := rapid.SampledFrom([]string{
			p.positive, p.negated, p.positive + "IfExists", p.negated + "IfExists",
			"ForAllValues:" + p.positive, "ForAnyValue:" + p.negated, "Null",
		}).Draw(t, "operator")
		policy := drawValues(t, p.values)
		if op == "Null" {
			policy = []string{rapid.SampledFrom([]string{"true", "false"}).Draw(t, "null value")}
		}
		effect := rapid.SampledFrom([]string{"Allow", "Deny"}).Draw(t, "effect")
		doc := conditionDoc(effect, clauseDoc(op, policy))

		unsupplied := policydoc.NewContext("")
		unsupplied.Unsupplied(propKey)
		evaluate := func(ctx *policydoc.Context) policydoc.Result {
			return policydoc.Evaluate(parseDoc(t, doc), policydoc.IdentityPolicy, policydoc.Request{
				Requester: evalUser, Action: "s3:ListBucket", Resource: "arn:aws:s3:::vector-bucket", Context: ctx,
			})
		}
		absent, missing := evaluate(contextOf()), evaluate(unsupplied)
		if len(absent.Allows) != len(missing.Allows) || len(absent.Denies) != len(missing.Denies) {
			t.Fatalf("%s: unsupplied %+v, absent %+v; want the same statements", doc, missing, absent)
		}
		want := []policydoc.UnsuppliedKey{{Key: propKey, Statement: policydoc.StatementRef{Index: 0}}}
		if !sameUnsupplied(missing.Unsupplied, want) {
			t.Fatalf("%s: Unsupplied = %+v, want %+v", doc, missing.Unsupplied, want)
		}
		if len(absent.Unsupplied) != 0 {
			t.Fatalf("%s: an absent key is reported as unsupplied: %+v", doc, absent.Unsupplied)
		}
	})
}

// Context key names are not case-sensitive (iam-evaluation.md 6.2, S-COND):
// spelling the request's key in any case decides as the policy's spelling.
func TestPropertyKeyNamesFoldCase(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := rapid.SampledFrom(opPairs).Draw(t, "operators")
		op := rapid.SampledFrom([]string{p.positive, p.negated}).Draw(t, "operator")
		policy := drawValues(t, p.values)
		value := rapid.SampledFrom(p.values).Draw(t, "request value")
		spelled := foldRandomly(t, propKey)
		same := condHolds(t, "Allow", clauseDoc(op, policy), contextOf(one(propKey, value)))
		folded := condHolds(t, "Allow", clauseDoc(op, policy), contextOf(one(spelled, value)))
		if same != folded {
			t.Fatalf("%s: key %q decides %t, %q decides %t", op, propKey, same, spelled, folded)
		}
	})
}

// foldRandomly changes the case of each letter of s at random.
func foldRandomly(t *rapid.T, s string) string {
	var b strings.Builder
	for i, r := range s {
		if rapid.Bool().Draw(t, "upper "+string(rune('a'+i%26))) {
			b.WriteString(strings.ToUpper(string(r)))
		} else {
			b.WriteString(strings.ToLower(string(r)))
		}
	}
	return b.String()
}

// Actions match case-insensitively, with * and ? anywhere (iam-evaluation.md
// 5.2, S-ACTION), agreeing with the reference glob of allows_property_test.go.
func TestPropertyActionPatternsAgreeWithTheReference(t *testing.T) {
	names := []string{"s3:GetObject", "s3:getobject", "sqs:SendMessage", "S3:PutObject", "iam:ListAccessKeys"}
	patterns := []string{"s3:*", "S3:Get*", "*", "sqs:Send?essage", "iam:*AccessKey*", "s3:?etObject", "s3:Put*", "sqs:*Message"}
	rapid.Check(t, func(t *rapid.T) {
		pattern := rapid.SampledFrom(patterns).Draw(t, "pattern")
		action := rapid.SampledFrom(names).Draw(t, "action")
		if got, want := actionApplies(t, pattern, action), refGlob(pattern, action, true, false); got != want {
			t.Fatalf("Action %s on %s applies = %t, want %t", pattern, action, got, want)
		}
	})
}

// resourceFragments build resource patterns and values without "$", so no
// fragment sequence spells a policy variable; the escapes are whole tokens.
var resourceFragments = []string{"a", "A", "b", "/", ":", "*", "?", "${*}", "${?}", "${$}"}

// Resources match case-sensitively, * spanning ":" and "/", ? one
// character, and in a 2012-10-17 document ${*}, ${?} and ${$} are literal
// (iam-evaluation.md 5.3, 7.4; S-RESOURCE), agreeing with the reference
// glob. Every pattern and value is a full six-field ARN, so the
// incomplete-ARN completion does not apply.
func TestPropertyResourcePatternsAgreeWithTheReference(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		const prefix = "arn:aws:s3:::"
		pattern := prefix + strings.Join(rapid.SliceOfN(rapid.SampledFrom(resourceFragments), 0, 6).Draw(t, "pattern"), "")
		value := prefix + refUnescape(strings.Join(rapid.SliceOfN(rapid.SampledFrom(resourceFragments), 0, 6).Draw(t, "value"), ""), true)
		if got, want := resourceApplies(t, v2012, pattern, value, nil), refGlob(pattern, value, false, true); got != want {
			t.Fatalf("Resource %q on %q applies = %t, want %t", pattern, value, got, want)
		}
	})
}

// Statements of one policy combine with a logical OR (iam-evaluation.md
// 2.1): a statement applies in a document exactly when it applies alone.
func TestPropertyStatementsApplyIndependently(t *testing.T) {
	statements := []string{
		`{"Effect":"Allow","Action":"s3:*","Resource":"*"}`,
		`{"Effect":"Deny","Action":"s3:Put*","Resource":"arn:aws:s3:::b/*"}`,
		`{"Effect":"Allow","Action":"sqs:*","Resource":"*"}`,
		`{"Effect":"Allow","NotAction":"s3:Get*","Resource":"*"}`,
		`{"Effect":"Deny","Action":"*","NotResource":"arn:aws:s3:::b/*"}`,
		`{"Effect":"Allow","Action":"*","Resource":"*","Condition":{"StringEquals":{"aws:username":"ana"}}}`,
	}
	rapid.Check(t, func(t *rapid.T) {
		chosen := rapid.SliceOfN(rapid.SampledFrom(statements), 1, 4).Draw(t, "statements")
		action := rapid.SampledFrom([]string{"s3:GetObject", "s3:PutObject", "sqs:SendMessage"}).Draw(t, "action")
		resource := rapid.SampledFrom([]string{"arn:aws:s3:::b/k", "arn:aws:s3:::c/k"}).Draw(t, "resource")
		ctx := contextOf(one("aws:username", rapid.SampledFrom([]string{"ana", "bob"}).Draw(t, "user")))
		req := policydoc.Request{Requester: evalUser, Action: action, Resource: resource, Context: ctx}
		whole := policydoc.Evaluate(parseDoc(t, `{"Version":"2012-10-17","Statement":[`+strings.Join(chosen, ",")+`]}`), policydoc.IdentityPolicy, req)
		var allows, denies []int
		for i, s := range chosen {
			alone := policydoc.Evaluate(parseDoc(t, `{"Version":"2012-10-17","Statement":[`+s+`]}`), policydoc.IdentityPolicy, req)
			if len(alone.Allows) == 1 {
				allows = append(allows, i)
			}
			if len(alone.Denies) == 1 {
				denies = append(denies, i)
			}
		}
		if got := indexes(whole.Allows); !slices.Equal(got, allows) {
			t.Fatalf("Allows indexes = %v, want %v", got, allows)
		}
		if got := indexes(whole.Denies); !slices.Equal(got, denies) {
			t.Fatalf("Denies indexes = %v, want %v", got, denies)
		}
	})
}

func indexes(applied []policydoc.Applied) []int {
	out := make([]int, 0, len(applied))
	for _, a := range applied {
		out = append(out, a.Statement.Index)
	}
	return out
}
