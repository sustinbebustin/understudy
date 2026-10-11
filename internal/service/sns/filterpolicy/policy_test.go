package filterpolicy_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ruler"
	"github.com/sustinbebustin/understudy/internal/service/sns/filterpolicy"
)

// TestRecordedValidationTexts replays every AWS-recorded filter policy error
// of sns.md 6.5 and matching-engines.md 3. The reason is the text after
// "Invalid parameter: " on SetSubscriptionAttributes; Jackson's location
// line, which AWS appends to structural errors, is compared away as sns.md
// 6.5 says to.
func TestRecordedValidationTexts(t *testing.T) {
	t.Parallel()
	attrs, body := filterpolicy.MessageAttributes, filterpolicy.MessageBody
	tests := []struct {
		policy string
		scope  filterpolicy.Scope
		reason string
	}{
		{`invalid`, attrs, "FilterPolicy: failed to parse JSON. Unrecognized token 'invalid': was expecting (JSON String, Number, Array, Object or token 'null', 'true' or 'false')"},
		{`{"key":[]}`, attrs, "FilterPolicy: Empty arrays are not allowed"},
		{`{"key":"value_one"}`, attrs, `FilterPolicy: "key" must be an object or an array`},
		{`{"key":{"suffix":"v"}}`, attrs, `FilterPolicy: "suffix" must be an object or an array`},
		{`{"key":{"suffix":"v"}}`, body, `FilterPolicy: "suffix" must be an object or an array`},
		{`{"key":[["value"]]}`, attrs, "FilterPolicy: Match value must be String, number, true, false, or null"},
		{`{"key":[{"wrong-operator":true}]}`, attrs, "FilterPolicy: Unrecognized match type wrong-operator"},
		{`{"key":[{"suffix":"v","prefix":"w"}]}`, attrs, "FilterPolicy: Only one key allowed in match expression"},
		{`{"key":[{"suffix":100}]}`, attrs, "FilterPolicy: suffix match pattern must be a string"},
		{`{"key":[{"suffix":null}]}`, attrs, "FilterPolicy: suffix match pattern must be a string"},
		{`{"key":[{"suffix":[]}]}`, attrs, "FilterPolicy: suffix match pattern must be a string"},
		{`{"key":[{"suffix":["a","b"]}]}`, attrs, "FilterPolicy: suffix match pattern must be a string"},
		{`{"key":[{"prefix":100}]}`, attrs, "FilterPolicy: prefix match pattern must be a string"},
		{`{"key":[{"equals-ignore-case":100}]}`, attrs, "FilterPolicy: equals-ignore-case match pattern must be a string"},
		{`{"key":[{"exists":null}]}`, attrs, "FilterPolicy: exists match pattern must be either true or false."},
		{`{"key":[{"exists":"no"}]}`, attrs, "FilterPolicy: exists match pattern must be either true or false."},
		{`{"key":[{"anything-but":{"wrong-operator":null}}]}`, attrs, "FilterPolicy: Unsupported anything-but pattern: wrong-operator"},
		{`{"key":[{"anything-but":{"exists":false}}]}`, attrs, "FilterPolicy: Unsupported anything-but pattern: exists"},
		{`{"key":[{"anything-but":{"prefix":false}}]}`, attrs, "FilterPolicy: prefix match pattern must be a string"},
		{`{"key":[{"cidr":["bad-filter"]}]}`, attrs, "FilterPolicy: prefix match pattern must be a string"},
		{`{"key":[{"cidr":"bad-filter"}]}`, attrs, "FilterPolicy: Malformed CIDR, one '/' required"},
		{`{"key":[{"cidr":"bad-filter/64"}]}`, attrs, "FilterPolicy: Nonstandard IP address: bad-filter"},
		{`{"key":[{"cidr":"bad-/64filter"}]}`, attrs, "FilterPolicy: Malformed CIDR, mask bits must be an integer"},
		{`{"key":[{"cidr":"xx.11.xx/8"}]}`, attrs, "FilterPolicy: Nonstandard IP address: xx.11.xx"},
		{`{"key":[{"cidr":"xxxx:db8:1234:1a00::/64"}]}`, attrs, "FilterPolicy: Nonstandard IP address: xxxx:db8:1234:1a00::"},
		{`{"key":{"not-an-operator":"value"}}`, attrs, `FilterPolicy: "not-an-operator" must be an object or an array`},
		{`{"key":[{"numeric":[]}]}`, attrs, "FilterPolicy: Invalid member in numeric match: ]"},
		{`{"key":[{"numeric":["operator"]}]}`, attrs, "FilterPolicy: Unrecognized numeric range operator: operator"},
		{`{"key":[{"numeric":[1,"<="]}]}`, attrs, "FilterPolicy: Invalid member in numeric match: 1"},
		{`{"key":[{"numeric":["=","000"]}]}`, attrs, "FilterPolicy: Value of equals must be numeric"},
		{`{"key":[{"numeric":[">="]}]}`, attrs, "FilterPolicy: Value of >= must be numeric"},
		{`{"key":[{"numeric":["<",100,">",10]}]}`, attrs, "FilterPolicy: Too many elements in numeric expression"},
		{`{"key":[{"numeric":[">=",1,">",2]}]}`, attrs, "FilterPolicy: Bad numeric range operator: >"},
		{`{"key":[{"numeric":[">",3,"<",1]}]}`, attrs, "FilterPolicy: Bottom must be less than top"},
		{`{"key":[{"numeric":[">",20,"<="]}]}`, attrs, "FilterPolicy: Value of <= must be numeric"},
		{`{"key":[{"numeric":[">",20,30]}]}`, attrs, "FilterPolicy: Bad value in numeric range: 30"},
		{`{"key":[{"numeric":[">",20,"test"]}]}`, attrs, "FilterPolicy: Bad numeric range operator: test"},
		{`{"key":[{"numeric":[">","20","<","30"]}]}`, attrs, "FilterPolicy: Value of > must be numeric"},
		{`{"key":[{"numeric":[">",20,"<","30"]}]}`, attrs, "FilterPolicy: Value of < must be numeric"},
		{`{"key":[{"numeric":[">",20,"<",30,"<",50]}]}`, attrs, "FilterPolicy: Too many terms in numeric range expression"},
		{`{"a":["1"],"b":["1"],"c":["1"],"d":["1"],"e":["1"],"f":["1"]}`, attrs, "FilterPolicy: Filter policy can not have more than 5 keys"},
		{`{"key":[` + numbers(151) + `]}`, attrs, "FilterPolicy: Filter policy is too complex"},
		{`{"key1":[` + numbers(3) + `],"key2":[` + numbers(51) + `]}`, attrs, "FilterPolicy: Filter policy is too complex"},
		// Nested under the default scope: the scope text alone, without
		// "FilterPolicy: " (sns.md 6.5).
		{`{"object":{"key":[{"prefix":"auto-"}]}}`, attrs, "Filter policy scope MessageAttributes does not support nested filter policy"},
	}
	for _, tc := range tests {
		t.Run(tc.policy, func(t *testing.T) {
			t.Parallel()
			_, err := filterpolicy.Compile(tc.policy, tc.scope)
			if err == nil {
				t.Fatalf("Compile accepted %s under %s; want %q", tc.policy, tc.scope, tc.reason)
			}
			if first, _, _ := strings.Cut(err.Reason, "\n"); first != tc.reason {
				t.Fatalf("Compile(%s) reason = %q\nwant %q", tc.policy, err.Reason, tc.reason)
			}
		})
	}
}

// numbers is a JSON list of n distinct numbers.
func numbers(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = strings.Repeat("1", i%9+1) + "0" + strings.Repeat("0", i/9)
	}
	return strings.Join(parts, ",")
}

// TestComplexityBoundaries checks the recorded complexity boundaries
// (sns.md 6.5): a flat $or policy computing 150 passes and 152 fails, and
// the nested body-scope $or policy computing 160 fails.
func TestComplexityBoundaries(t *testing.T) {
	t.Parallel()
	// (2 + 1) * 50 combinations: source[2] x (metric[50] or namespace[1]) is
	// 2*50 + 2*1 = 102; scale the branches to hit the boundaries exactly.
	or := func(a, b int) string {
		return `{"source":["s1","s2"],"$or":[{"metricName":[` + numbers(a) + `]},{"namespace":[` + numbers(b) + `]}]}`
	}
	if _, err := filterpolicy.Compile(or(50, 25), filterpolicy.MessageAttributes); err != nil {
		t.Errorf("150 combinations refused: %q", err.Reason)
	}
	if _, err := filterpolicy.Compile(or(50, 26), filterpolicy.MessageAttributes); err == nil || err.Reason != "FilterPolicy: Filter policy is too complex" {
		t.Errorf("152 combinations: %v, want too complex", err)
	}
	// Nested keys weigh their nesting level: 4 rules at level 2 in each of
	// 20 branches is 160.
	nested := `{"$or":[` + strings.Repeat(`{"a":{"b":["1","2","3","4"]}},`, 19) + `{"a":{"c":["1","2","3","4"]}}]}`
	if _, err := filterpolicy.Compile(nested, filterpolicy.MessageBody); err == nil || err.Reason != "FilterPolicy: Filter policy is too complex" {
		t.Errorf("nested 160: %v, want too complex", err)
	}
}

// TestScopes checks that a policy is compiled for its scope: nesting is a
// body-scope feature, and each scope reads its own input (sns.md 6.1, 6.3).
func TestScopes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		text  string
		scope filterpolicy.Scope
		ok    bool
	}{{"MessageAttributes", filterpolicy.MessageAttributes, true}, {"MessageBody", filterpolicy.MessageBody, true}, {"messagebody", 0, false}, {"", 0, false}} {
		got, ok := filterpolicy.ParseScope(tc.text)
		if ok != tc.ok || got != tc.scope || (ok && got.String() != tc.text) {
			t.Errorf("ParseScope(%q) = %v %v, want %v %v", tc.text, got, ok, tc.scope, tc.ok)
		}
	}

	nested := `{"detail":{"store":["north"]}}`
	p, err := filterpolicy.Compile(nested, filterpolicy.MessageBody)
	if err != nil {
		t.Fatalf("Compile body: %q", err.Reason)
	}
	for body, want := range map[string]bool{
		`{"detail":{"store":"north"}}`:           true,
		`{"detail":{"store":["south","north"]}}`: true,
		`{"detail":{"store":"south"}}`:           false,
		`{"detail.store":"north"}`:               true,
		// Not a JSON object: filtered out (sns.md 6.3).
		`plain text`:                           false,
		`"{\"detail\":{\"store\":\"north\"}}"`: false,
		`{"detail":{"store":"north"}} x`:       false,
	} {
		got, err := p.Match(nil, body)
		if err != nil || got != want {
			t.Errorf("body %s: Match = %v, %v; want %v", body, got, err, want)
		}
	}
	// The body scope ignores attributes.
	if got, err := p.Match([]delivery.MessageAttribute{{Name: "detail.store", Value: delivery.StringAttribute{Value: "north"}}}, `{}`); got || err != nil {
		t.Errorf("a body-scope policy against an attribute: Match = %v, %v; want false, nil", got, err)
	}
	if p.String() != nested {
		t.Errorf("String = %q, want the policy as given", p.String())
	}
}

// TestAttributeTypes checks the MessageAttributes scope over each data type
// (sns.md 6.3): String.Array elements are separate values, Number is a
// number, Binary is ignored.
func TestAttributeTypes(t *testing.T) {
	t.Parallel()
	str := func(v string) delivery.AttributeValue { return delivery.StringAttribute{Value: v} }
	num := func(v string) delivery.AttributeValue { return delivery.NumberAttribute{Value: v} }
	arr := func(v string) delivery.AttributeValue { return delivery.StringAttribute{Label: "Array", Value: v} }
	tests := []struct {
		policy string
		value  delivery.AttributeValue
		want   bool
	}{
		{`{"store":["value1"]}`, arr(`["value1","value2"]`), true},
		{`{"store":["value1"]}`, arr(`["value2","value3"]`), false},
		{`{"store":[{"exists":true}]}`, num("3"), true},
		{`{"price":[{"numeric":[">",100]}]}`, num("150.5"), true},
		{`{"price":[{"numeric":[">",100]}]}`, str("150.5"), false},
		{`{"price":[{"numeric":["=",301.5]}]}`, num("3.015e2"), true},
		{`{"price":[100]}`, num("1.0e2"), true},
		{`{"price":["100"]}`, num("100"), false},
		{`{"price":[100]}`, str("100"), false},
		{`{"store":[{"anything-but":["rugby","tennis"]}]}`, arr(`["rugby","baseball"]`), true},
		{`{"store":[{"anything-but":["rugby","tennis"]}]}`, arr(`["rugby"]`), false},
		{`{"store":[{"prefix":"ru"}]}`, str("rugby"), true},
		{`{"store":[{"suffix":"by"}]}`, str("rugby"), true},
		{`{"store":[{"equals-ignore-case":"RUGBY"}]}`, str("Rugby"), true},
		{`{"store":[{"wildcard":"r*y"}]}`, str("rugby"), true},
		{`{"ip":[{"cidr":"10.0.0.0/24"}]}`, str("10.0.0.7"), true},
		{`{"blob":[{"exists":true}]}`, delivery.BinaryAttribute{Value: []byte{1}}, false},
	}
	for _, tc := range tests {
		p, err := filterpolicy.Compile(tc.policy, filterpolicy.MessageAttributes)
		if err != nil {
			t.Fatalf("Compile(%s): %q", tc.policy, err.Reason)
		}
		name := strings.Split(tc.policy, `"`)[1]
		got, merr := p.Match([]delivery.MessageAttribute{{Name: name, Value: tc.value}}, "{}")
		if merr != nil || got != tc.want {
			t.Errorf("%s against %s %+v: Match = %v, %v; want %v", tc.policy, tc.value.DataType(), tc.value, got, merr, tc.want)
		}
	}
	// A missing key does not match, except under exists:false (sns.md 6.3).
	p, err := filterpolicy.Compile(`{"attr1":[null,{"anything-but":"whatever"}]}`, filterpolicy.MessageAttributes)
	if err != nil {
		t.Fatal(err.Reason)
	}
	if got, err := p.Match(nil, "m"); got || err != nil {
		t.Errorf("a policy on attr1 against a message without attributes: Match = %v, %v; want false, nil", got, err)
	}
}

// TestMatchUndecided: a policy and a body inside SNS's limits (five keys,
// 5 KB) that exhaust the engine's search budget make Match an error,
// neither a match nor a filtered message. Repeating the key "a" puts the
// fields of every a.* path in four arrays, which joins them into one search
// component, and "z" never matches, so the search must try every
// combination of their elements.
func TestMatchUndecided(t *testing.T) {
	t.Parallel()
	p, err := filterpolicy.Compile(`{"a":{"b":["1"],"c":["1"],"d":["1"],"e":["1"],"z":["2"]}}`, filterpolicy.MessageBody)
	if err != nil {
		t.Fatal(err.Reason)
	}
	elem := `{"b":"1","c":"1","d":"1","e":"1","z":"1"}`
	array := `[` + strings.Repeat(elem+",", 31) + elem + `]`
	body := `{` + strings.Repeat(`"a":`+array+",", 3) + `"a":` + array + `}`
	matched, merr := p.Match(nil, body)
	if budget, ok := errors.AsType[*ruler.SearchBudgetError](merr); !ok || matched || budget.Steps <= 0 {
		t.Fatalf("Match = %v, %v; want false and a *ruler.SearchBudgetError after a positive step count", matched, merr)
	}
}

// TestSwitches lists every unverified choice as a fidelity switch with its
// record-backlog reference and the value in effect: the four conflicts
// between Ruler and the SNS documentation keep Ruler's behavior (ADR 0015,
// matching-engines.md 3 and 10), and anything-but suffix is accepted
// although a 2024 recording refused it (matching-engines.md H7).
func TestSwitches(t *testing.T) {
	t.Parallel()
	type setting struct{ value, record string }
	got := map[string]setting{}
	for _, e := range filterpolicy.Switches() {
		got[e.Name] = setting{e.Value, e.Record}
	}
	for name, want := range map[string]setting{
		"sns.filter-policy.exists-true-empty-value": {"any-value", "matching-engines.md H2"},
		"sns.filter-policy.empty-input":             {"matchable", "R09/6 (matching-engines.md H1)"},
		"sns.filter-policy.bare-number":             {"numeric-or-text", "R09/1 (matching-engines.md H6)"},
		"sns.filter-policy.malformed-or":            {"rejected", "matching-engines.md H10"},
		"sns.filter-policy.anything-but-suffix":     {"accepted", "R09/7 (matching-engines.md H7)"},
		"sns.filter-policy.anything-but-arrays":     {"any-element", "R09/8 (matching-engines.md H4)"},
	} {
		if g, ok := got[name]; !ok || g != want {
			t.Errorf("switch %s = %+v (present %v), want %+v", name, g, ok, want)
		}
	}
}

// TestAnythingButSuffixAccepted backs the anything-but-suffix switch: the
// policy compiles and excludes the values with the suffix.
func TestAnythingButSuffixAccepted(t *testing.T) {
	t.Parallel()
	p, err := filterpolicy.Compile(`{"key":[{"anything-but":{"suffix":"test"}}]}`, filterpolicy.MessageAttributes)
	if err != nil {
		t.Fatalf("Compile refused anything-but suffix: %q", err.Reason)
	}
	for v, want := range map[string]bool{"a-test": false, "a-prod": true} {
		got, merr := p.Match([]delivery.MessageAttribute{{Name: "key", Value: delivery.StringAttribute{Value: v}}}, "{}")
		if merr != nil || got != want {
			t.Errorf("key %q: Match = %v, %v; want %v", v, got, merr, want)
		}
	}
}
