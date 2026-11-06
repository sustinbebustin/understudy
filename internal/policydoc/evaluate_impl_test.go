package policydoc_test

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// Operator spellings the research leaves UNVERIFIED. Each row pins
// understudy's inferred reading, named by its recording queue item, and the
// negated operator, which is NOR over the policy values (iam-evaluation.md
// 6.1), is checked to be its complement on a present key.
func TestEvaluateInferredOperatorSpellings(t *testing.T) {
	cases := []struct {
		id, operator, negated, policy, request string
		want                                   bool
	}{
		// IAM-EVAL-09: decimal numbers with an optional sign, fraction and
		// exponent; anything else never compares equal.
		{"IAM-EVAL-09 exponent", "NumericEquals", "NumericNotEquals", "1e1", "10", true},
		{"IAM-EVAL-09 sign", "NumericEquals", "NumericNotEquals", "+10", "10", true},
		{"IAM-EVAL-09 leading zero", "NumericEquals", "NumericNotEquals", "010", "10", true},
		{"IAM-EVAL-09 trailing point", "NumericEquals", "NumericNotEquals", "10.", "10", false},
		{"IAM-EVAL-09 hexadecimal", "NumericEquals", "NumericNotEquals", "0xA", "10", false},
		{"IAM-EVAL-09 infinity", "NumericEquals", "NumericNotEquals", "Inf", "Inf", false},
		// IAM-EVAL-06: the W3C profile of ISO 8601, then epoch seconds.
		{"IAM-EVAL-06 year", "DateEquals", "DateNotEquals", "2020", "2020-01-01T00:00:00Z", true},
		{"IAM-EVAL-06 month", "DateEquals", "DateNotEquals", "2020-06", "2020-06-01T00:00:00Z", true},
		{"IAM-EVAL-06 minutes with offset", "DateEquals", "DateNotEquals", "2020-06-01T02:00+02:00", "2020-06-01T00:00:00Z", true},
		{"IAM-EVAL-06 fraction", "DateEquals", "DateNotEquals", "2020-06-01T00:00:00.5Z", "2020-06-01T00:00:00Z", false},
		{"IAM-EVAL-06 epoch", "DateEquals", "DateNotEquals", "1591660800", "2020-06-09T00:00:00Z", true},
		{"IAM-EVAL-06 prose", "DateEquals", "DateNotEquals", "yesterday", "yesterday", false},
		// IAM-EVAL-08: true and false in any case, nothing else.
		{"IAM-EVAL-08 upper case", "Bool", "", "true", "TRUE", true},
		{"IAM-EVAL-08 one", "Bool", "", "true", "1", false},
		{"IAM-EVAL-08 fold", "StringEqualsIgnoreCase", "StringNotEqualsIgnoreCase", "STRASSE", "strasse", true},
		// BinaryEquals compares texts that do not decode as base64
		// (iam-evaluation.md 6.7 gives no rule for them; inferred).
		{"binary undecodable equal", "BinaryEquals", "", "not base64!", "not base64!", true},
		{"binary undecodable different", "BinaryEquals", "", "not base64!", "not base64?", false},
		// A CIDR block with host bits set is the block (iam-evaluation.md
		// 6.8); a request value that is not an address is in no block.
		{"cidr with host bits", "IpAddress", "NotIpAddress", "203.0.113.7/24", "203.0.113.200", true},
		{"ipv6 block", "IpAddress", "NotIpAddress", "2001:db8::/32", "2001:db8:ffff::1", true},
		{"request not an address", "IpAddress", "NotIpAddress", "203.0.113.0/24", "203.0.113.0/24", false},
		// IAM-EVAL-11: an ARN value without six fields never matches.
		{"IAM-EVAL-11 short pattern", "ArnLike", "ArnNotLike", "arn:aws:sqs", "arn:aws:sqs:us-east-1:111122223333:q", false},
		{"IAM-EVAL-11 short request", "ArnLike", "ArnNotLike", "arn:aws:sqs:*:*:*", "arn:aws:sqs", false},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			ctx := contextOf(one("test:Key", c.request))
			if got := condHolds(t, "Allow", clauseDoc(c.operator, []string{c.policy}), ctx); got != c.want {
				t.Errorf("%s %q on %q = %t, want %t", c.operator, c.policy, c.request, got, c.want)
			}
			if c.negated == "" {
				return
			}
			if got := condHolds(t, "Allow", clauseDoc(c.negated, []string{c.policy}), ctx); got == c.want {
				t.Errorf("%s %q on %q = %t, want %t", c.negated, c.policy, c.request, got, !c.want)
			}
		})
	}
}

// Null reads JSON booleans as their text, and a value other than true or
// false never holds (iam-evaluation.md 6.11). A present key with no values
// is null (inferred), unlike one whose value is "" (IAM-EVAL-05,
// aws-recorded).
func TestEvaluateNullValues(t *testing.T) {
	cases := []struct {
		name, cond string
		ctx        *policydoc.Context
		want       bool
	}{
		{"JSON true on an absent key", `{"Null":{"test:Key":true}}`, contextOf(), true},
		{"JSON false on a present key", `{"Null":{"test:Key":false}}`, contextOf(one("test:Key", "v")), true},
		{"TRUE in upper case", `{"Null":{"test:Key":"TRUE"}}`, contextOf(), true},
		{"yes", `{"Null":{"test:Key":"yes"}}`, contextOf(), false},
		{"no values is null", `{"Null":{"test:Key":"true"}}`, contextOf(many("test:Key")), true},
		{"either value", `{"Null":{"test:Key":["false","true"]}}`, contextOf(), true},
	}
	for _, c := range cases {
		if got := condHolds(t, "Allow", c.cond, c.ctx); got != c.want {
			t.Errorf("%s: %s applies = %t, want %t", c.name, c.cond, got, c.want)
		}
	}
}

// The set operators on present keys beyond the documented rows of
// iam-evaluation.md 6.14: IfExists on a set operator holds on an absent key
// (6.14, inferred), and a set operator on a key whose only value is ""
// tests that value: AWS's simulator allowed ForAnyValue:StringNotEquals
// "a" and denied ForAllValues:StringEquals "a" and ForAnyValue:StringEquals
// "a" on aws:TagKeys [""] (IAM-EVAL-04 steps 02, 05 and 06, aws-recorded).
func TestEvaluateSetOperatorEdges(t *testing.T) {
	if !condHolds(t, "Allow", `{"ForAnyValue:StringEqualsIfExists":{"test:Key":"a"}}`, contextOf()) {
		t.Error("ForAnyValue:StringEqualsIfExists on an absent key = false, want true")
	}
	if !condHolds(t, "Allow", `{"ForAnyValue:StringNotEquals":{"test:Key":"a"}}`, contextOf(many("test:Key", ""))) {
		t.Error(`ForAnyValue:StringNotEquals "a" on [""] = false, want true`)
	}
	if condHolds(t, "Allow", `{"ForAllValues:StringEquals":{"test:Key":"a"}}`, contextOf(many("test:Key", ""))) {
		t.Error(`ForAllValues:StringEquals "a" on [""] = true, want false`)
	}
	if condHolds(t, "Allow", `{"ForAnyValue:StringEquals":{"test:Key":"a"}}`, contextOf(many("test:Key", ""))) {
		t.Error(`ForAnyValue:StringEquals "a" on [""] = true, want false`)
	}
	if condHolds(t, "Allow", `{"StringNotEquals":{"test:Key":"a"}}`, contextOf(many("test:Key"))) {
		t.Error("plain StringNotEquals on a multivalued key with no values = true, want false (IAM-EVAL-02)")
	}
}

// Variables beyond the Q vectors (iam-evaluation.md 7.1, 7.4): where they are
// substituted and how they spell, each UNVERIFIED detail named by its
// recording queue item.
func TestEvaluateVariableDetails(t *testing.T) {
	cases := []struct {
		name, cond string
		ctx        []kv
		want       bool
	}{
		// IAM-EVAL-15: other spacing is part of the name, which no key has.
		{"default without its space", `{"StringEquals":{"test:Key":"${aws:username,'x'}"}}`, []kv{one("test:Key", "x")}, false},
		{"default with its space", `{"StringEquals":{"test:Key":"${aws:username, 'x'}"}}`, []kv{one("test:Key", "x")}, true},
		// IAM-EVAL-14: a multivalued key gives a variable no value.
		{"multivalued key", `{"StringEquals":{"test:Key":"${aws:TagKeys}"}}`, []kv{many("aws:TagKeys", "a"), one("test:Key", "a")}, false},
		{"multivalued key with a default", `{"StringEquals":{"test:Key":"${aws:TagKeys, 'd'}"}}`, []kv{many("aws:TagKeys", "a"), one("test:Key", "d")}, true},
		// A substituted value is literal text (inferred): its * is no wildcard.
		{"value with a star, same text", `{"StringLike":{"test:Key":"${aws:username}"}}`, []kv{one("aws:username", "a*"), one("test:Key", "a*")}, true},
		{"value with a star, other text", `{"StringLike":{"test:Key":"${aws:username}"}}`, []kv{one("aws:username", "a*"), one("test:Key", "abc")}, false},
		// Variable names are not case-sensitive (iam-evaluation.md 6.2).
		{"name in another case", `{"StringEquals":{"test:Key":"${AWS:UserName}"}}`, []kv{one("aws:username", "ana"), one("test:Key", "ana")}, true},
		// Numeric, Date and IP operators take no variables (7.1).
		{"numeric", `{"NumericEquals":{"test:Key":"${aws:n}"}}`, []kv{one("aws:n", "10"), one("test:Key", "10")}, false},
		{"ip", `{"IpAddress":{"test:Key":"${aws:n}"}}`, []kv{one("aws:n", "203.0.113.1"), one("test:Key", "203.0.113.1")}, false},
		// ARN operators take them (7.1).
		{"arn", `{"ArnEquals":{"test:Key":"arn:aws:sqs:us-east-1:111122223333:${aws:username}"}}`, []kv{one("aws:username", "q"), one("test:Key", "arn:aws:sqs:us-east-1:111122223333:q")}, true},
	}
	for _, c := range cases {
		if got := condHolds(t, "Allow", c.cond, contextOf(c.ctx...)); got != c.want {
			t.Errorf("%s: %s under %+v applies = %t, want %t", c.name, c.cond, c.ctx, got, c.want)
		}
	}
}

// In Resource, a variable is substituted only after the fifth colon
// (iam-evaluation.md 7.1, #policy-vars-resourceelement), as literal text;
// in Action it stays literal (IAM-EVAL-14, inferred).
func TestEvaluateVariablePlacement(t *testing.T) {
	user := []kv{one("aws:username", "ana"), one("aws:userid", "111122223333")}
	cases := []struct {
		name, pattern, resource string
		ctx                     []kv
		want                    bool
	}{
		{"after the fifth colon", "arn:aws:s3:::b/${aws:username}/*", "arn:aws:s3:::b/ana/k", user, true},
		{"before the fifth colon", "arn:aws:sqs:us-east-1:${aws:userid}:q", "arn:aws:sqs:us-east-1:111122223333:q", user, false},
		{"the literal text before the fifth colon", "arn:aws:sqs:us-east-1:${aws:userid}:q", "arn:aws:sqs:us-east-1:${aws:userid}:q", user, true},
		{"a value with a star", "arn:aws:s3:::b/${aws:username}/k", "arn:aws:s3:::b/x/k", []kv{one("aws:username", "*")}, false},
		{"a value with a star, same text", "arn:aws:s3:::b/${aws:username}/k", "arn:aws:s3:::b/*/k", []kv{one("aws:username", "*")}, true},
	}
	for _, c := range cases {
		if got := resourceApplies(t, v2012, c.pattern, c.resource, contextOf(c.ctx...)); got != c.want {
			t.Errorf("%s: Resource %s on %s = %t, want %t", c.name, c.pattern, c.resource, got, c.want)
		}
	}

	d := parseDoc(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:${aws:username}","Resource":"*"}]}`)
	r := policydoc.Evaluate(d, policydoc.IdentityPolicy, policydoc.Request{
		Requester: evalUser, Action: "s3:GetObject", Resource: "*", Context: contextOf(one("aws:username", "GetObject")),
	})
	if len(r.Allows) != 0 {
		t.Errorf("Action s3:${aws:username} with aws:username GetObject applies to s3:GetObject; want the variable literal (IAM-EVAL-14)")
	}
}

// A variable in a condition value that names an unsupplied key is reported
// (iam-evaluation.md 7.3; iam-enforcement-surfaces.md 12.2), and a key a
// statement tests twice is reported once, in the spelling first met.
func TestEvaluateReportsEachUnsuppliedNameOnce(t *testing.T) {
	d := parseDoc(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*","Condition":{`+
		`"StringEquals":{"s3:ExistingObjectTag/team":"${aws:PrincipalTag/team}"},"StringLike":{"S3:EXISTINGOBJECTTAG/TEAM":"b*"}}}]}`)
	ctx := policydoc.NewContext("s3")
	ctx.UnsuppliedFamily("aws:PrincipalTag/")
	r := policydoc.Evaluate(d, policydoc.IdentityPolicy, policydoc.Request{Requester: evalUser, Action: "s3:GetObject", Resource: "arn:aws:s3:::b/k", Context: ctx})
	want := []policydoc.UnsuppliedKey{
		{Key: "s3:ExistingObjectTag/team", Statement: policydoc.StatementRef{Index: 0}},
	}
	if !reflect.DeepEqual(r.Unsupplied, want) {
		t.Errorf("Unsupplied = %+v, want %+v: the key is absent, so its values, and their variable, are not evaluated", r.Unsupplied, want)
	}

	ctx.Set("s3:ExistingObjectTag/team", "blue")
	r = policydoc.Evaluate(d, policydoc.IdentityPolicy, policydoc.Request{Requester: evalUser, Action: "s3:GetObject", Resource: "arn:aws:s3:::b/k", Context: ctx})
	want = []policydoc.UnsuppliedKey{{Key: "aws:PrincipalTag/team", Statement: policydoc.StatementRef{Index: 0}}}
	if !reflect.DeepEqual(r.Unsupplied, want) || len(r.Allows) != 0 {
		t.Errorf("Evaluate = %+v, want no Allow and Unsupplied %+v", r, want)
	}
}

// Incomplete ARNs are completed only in identity policies
// (iam-evaluation.md 5.3; a resource policy reads them as written,
// IAM-EVAL-11, inferred). A resource-policy statement without a resource
// element (a trust policy's) applies to the resource; an identity-policy
// statement without one applies to nothing (S-GRAMMAR, 5.3).
func TestEvaluateResourceElementByKind(t *testing.T) {
	const queue = "arn:aws:sqs:us-east-1:111122223333:q"
	evaluate := func(doc string, kind policydoc.Kind) bool {
		r := policydoc.Evaluate(parseDoc(t, doc), kind, policydoc.Request{Requester: exUser, Action: "sqs:SendMessage", Resource: queue})
		return len(r.Allows) == 1
	}
	short := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"arn:aws:sqs"}]}`
	if evaluate(short, policydoc.ResourcePolicy) {
		t.Error("a resource policy's Resource arn:aws:sqs matches a queue; want it read as written")
	}
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage"}]}`
	if !evaluate(trust, policydoc.ResourcePolicy) {
		t.Error("a resource-policy statement without Resource does not apply")
	}
	identity := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage"}]}`
	if evaluate(identity, policydoc.IdentityPolicy) {
		t.Error("an identity-policy statement without Resource applies")
	}
}

// A statement whose Effect is neither Allow nor Deny applies to nothing
// (iam-evaluation.md 5.1: Effect is Allow or Deny).
func TestEvaluateZeroEffectNeverApplies(t *testing.T) {
	doc := policydoc.Document{Version: policydoc.Version20121017, Statements: []policydoc.Statement{
		{Action: policydoc.Action{"*"}, Resource: policydoc.Resource{"*"}},
	}}
	if r := policydoc.Evaluate(doc, policydoc.IdentityPolicy, policydoc.Request{Requester: exUser, Action: "s3:GetObject", Resource: "*"}); !reflect.DeepEqual(r, policydoc.Result{}) {
		t.Errorf("Evaluate = %+v, want nothing", r)
	}
}

// NotPrincipal exemptions beyond users and sessions, under switch
// iam.not-principal.match "account-and-principal" (IAM-EVAL-18): a root by
// its account, a service by its name, everyone by "*" except a principal
// with a permissions boundary (iam-evaluation.md 2.8), and an anonymous
// caller by nothing else.
func TestEvaluateNotPrincipalExemptions(t *testing.T) {
	if mode := switchValue(t, "iam.not-principal.match"); mode != "account-and-principal" {
		t.Skipf("iam.not-principal.match = %s; these rows pin account-and-principal", mode)
	}
	denied := []policydoc.Match{policydoc.MatchNotPrincipal{}}
	cases := []struct {
		name, listed string
		requester    policydoc.Requester
		bounded      bool
		want         []policydoc.Match
	}{
		{"root by its account", `{"AWS":"` + acct + `"}`, exRoot, false, nil},
		{"root of another account", `{"AWS":"444455556666"}`, exRoot, false, denied},
		{"service by name", `{"Service":"sns.amazonaws.com"}`, snsSvc, false, nil},
		{"another service", `{"Service":"events.amazonaws.com"}`, snsSvc, false, denied},
		{"everyone", `"*"`, exUser, false, nil},
		{"everyone, but a bounded user", `"*"`, exUser, true, denied},
		{"anonymous by an account", `{"AWS":"` + acct + `"}`, policydoc.AnonymousRequester{}, false, denied},
		{"a session by its own ARN and account", `{"AWS":["` + sessionARN + `","` + acct + `"]}`, exSession, false, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, denies := matchesOf(t, queuePolicy("Deny", `"NotPrincipal":`+c.listed, ""), c.requester, nil, c.bounded)
			if !reflect.DeepEqual(denies, c.want) {
				t.Errorf("Deny Matches = %#v, want %#v", denies, c.want)
			}
		})
	}
}

// "*" names the principal through aws:PrincipalArn only with a clause that
// requires the key to equal or be like a value (iam-evaluation.md 2.3): not
// with IfExists, which also holds when the key is absent, a set qualifier,
// or a negated operator. Federated values never name a requester (5.4).
func TestEvaluatePrincipalARNConditionForms(t *testing.T) {
	ctx := contextOf(one("aws:PrincipalArn", exRoleARN))
	plain := []policydoc.Match{policydoc.MatchWildcard{}}
	cases := []struct {
		condition string
		want      []policydoc.Match
	}{
		{`{"StringLike":{"aws:principalarn":"arn:aws:iam::111122223333:role/*"}}`, []policydoc.Match{policydoc.MatchWildcard{PrincipalARNCondition: true}}},
		{`{"ArnEqualsIfExists":{"aws:PrincipalArn":"` + exRoleARN + `"}}`, plain},
		{`{"ForAnyValue:ArnEquals":{"aws:PrincipalArn":"` + exRoleARN + `"}}`, plain},
		{`{"ArnNotEquals":{"aws:PrincipalArn":"arn:aws:iam::111122223333:role/other"}}`, plain},
	}
	for _, c := range cases {
		allows, _ := matchesOf(t, queuePolicy("Allow", `"Principal":"*"`, `,"Condition":`+c.condition), exSession, ctx, false)
		if !reflect.DeepEqual(allows, c.want) {
			t.Errorf("%s: Matches = %#v, want %#v", c.condition, allows, c.want)
		}
	}
	if allows, _ := matchesOf(t, queuePolicy("Allow", `"Principal":{"Federated":"cognito-identity.amazonaws.com"}`, ""), exUser, nil, false); allows != nil {
		t.Errorf("a Federated principal names a user: %#v", allows)
	}
}

// RequesterOf on ARNs other than root, user and session ARNs keeps the
// account element, so an account principal still names the caller as Allows
// does; text that is not an ARN carries no account.
func TestRequesterOfOtherCallers(t *testing.T) {
	cases := []struct {
		caller policydoc.Caller
		want   policydoc.Requester
	}{
		{policydoc.AWSCaller{ARN: exRoleARN}, policydoc.UserRequester{Account: acct, ARN: exRoleARN}},
		{
			policydoc.AWSCaller{ARN: "arn:aws-cn:sts::111122223333:assumed-role/r/s"},
			policydoc.SessionRequester{Account: acct, ARN: "arn:aws-cn:sts::111122223333:assumed-role/r/s", RoleARN: "arn:aws-cn:iam::111122223333:role/r"},
		},
		{policydoc.AWSCaller{ARN: "not an arn"}, policydoc.UserRequester{ARN: "not an arn"}},
	}
	for _, c := range cases {
		if got := policydoc.RequesterOf(c.caller); !reflect.DeepEqual(got, c.want) {
			t.Errorf("RequesterOf(%#v) = %#v, want %#v", c.caller, got, c.want)
		}
	}
}

// Lookup hands out a copy of a key's values, and a key marked Absent by name
// wins over its unsupplied family and its service.
func TestContextLookupIsolation(t *testing.T) {
	c := policydoc.NewContext("s3")
	c.SetMulti("aws:TagKeys", "a", "b")
	got, _ := c.Lookup("aws:TagKeys")
	got.Values[0] = "changed"
	if again, _ := c.Lookup("aws:TagKeys"); !slices.Equal(again.Values, []string{"a", "b"}) {
		t.Errorf("Lookup after changing a returned value = %v, want [a b]", again.Values)
	}
	c.UnsuppliedFamily("s3:ExistingObjectTag/")
	c.Absent("s3:ExistingObjectTag/team")
	if _, p := c.Lookup("s3:existingobjecttag/TEAM"); p != policydoc.Absent {
		t.Errorf("a key marked Absent in its unsupplied family = %d, want Absent", p)
	}
}

// Epoch seconds and their ISO 8601 form are one instant to the Date
// operators (iam-evaluation.md 6.5: "one of the W3C implementations of the
// ISO 8601 date formats or in epoch (UNIX) time"). Four digits read as a
// W3C year first (IAM-EVAL-06, inferred), so the epochs drawn have five or
// more.
func TestPropertyEpochAndISODatesAgree(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.Int64Range(10000, 4102444800).Draw(t, "epoch seconds")
		iso := time.Unix(n, 0).UTC().Format(time.RFC3339)
		epoch := strconv.FormatInt(n, 10)
		if !condHolds(t, "Allow", clauseDoc("DateEquals", []string{epoch}), contextOf(one(propKey, iso))) {
			t.Fatalf("DateEquals %s on %s = false", epoch, iso)
		}
		if condHolds(t, "Allow", clauseDoc("DateLessThan", []string{epoch}), contextOf(one(propKey, iso))) {
			t.Fatalf("DateLessThan %s on %s = true", epoch, iso)
		}
	})
}

// The Numeric operators order integers as integers (iam-evaluation.md 6.4),
// whatever the spelling of the policy value.
func TestPropertyNumericOrderAgreesWithIntegers(t *testing.T) {
	ops := map[string]func(r, p int) bool{
		"NumericEquals":            func(r, p int) bool { return r == p },
		"NumericNotEquals":         func(r, p int) bool { return r != p },
		"NumericLessThan":          func(r, p int) bool { return r < p },
		"NumericLessThanEquals":    func(r, p int) bool { return r <= p },
		"NumericGreaterThan":       func(r, p int) bool { return r > p },
		"NumericGreaterThanEquals": func(r, p int) bool { return r >= p },
	}
	names := slices.Sorted(func(yield func(string) bool) {
		for k := range ops {
			if !yield(k) {
				return
			}
		}
	})
	rapid.Check(t, func(t *rapid.T) {
		op := rapid.SampledFrom(names).Draw(t, "operator")
		r, p := rapid.IntRange(-1000, 1000).Draw(t, "request"), rapid.IntRange(-1000, 1000).Draw(t, "policy")
		spelled := rapid.SampledFrom([]string{strconv.Itoa(p), strconv.Itoa(p) + ".0", strconv.Itoa(p) + "e0"}).Draw(t, "spelling")
		if got, want := condHolds(t, "Allow", clauseDoc(op, []string{spelled}), contextOf(one(propKey, strconv.Itoa(r)))), ops[op](r, p); got != want {
			t.Fatalf("%s %s on %d = %t, want %t", op, spelled, r, got, want)
		}
	})
}

// A substituted variable is literal text (inferred, iam-evaluation.md 7.4):
// whatever wildcards, escapes or braces the value holds, StringEquals and
// StringLike match exactly the value itself.
func TestPropertySubstitutedValuesAreLiteral(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		value := strings.Join(rapid.SliceOfN(rapid.SampledFrom([]string{"a", "*", "?", "$", "{", "}", "${*}", "${aws:username}"}), 0, 5).Draw(t, "value"), "")
		other := value + rapid.SampledFrom([]string{"x", "*", "?"}).Draw(t, "suffix")
		for _, op := range []string{"StringEquals", "StringLike"} {
			cond := `{"` + op + `":{"` + propKey + `":"${aws:username}"}}`
			if !condHolds(t, "Allow", cond, contextOf(one("aws:username", value), one(propKey, value))) {
				t.Fatalf("%s ${aws:username} with %q on itself = false", op, value)
			}
			if condHolds(t, "Allow", cond, contextOf(one("aws:username", value), one(propKey, other))) {
				t.Fatalf("%s ${aws:username} with %q on %q = true", op, value, other)
			}
		}
	})
}
