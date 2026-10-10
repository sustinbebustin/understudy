package ruler

import (
	"slices"
	"testing"
)

// TestAllowLists checks each profile's operator allow-list
// (matching-engines.md 5.1): EventBridge takes every documented operator,
// SNS leaves out the equals-ignore-case forms of prefix, suffix and
// anything-but, and Lambda takes only the operators its filtering table
// lists.
func TestAllowLists(t *testing.T) {
	t.Parallel()
	operands := []struct {
		op      Operator
		pattern string
	}{
		{OperatorPrefix, `{"x":[{"prefix":"a"}]}`},
		{OperatorPrefixEqualsIgnoreCase, `{"x":[{"prefix":{"equals-ignore-case":"a"}}]}`},
		{OperatorSuffix, `{"x":[{"suffix":"a"}]}`},
		{OperatorSuffixEqualsIgnoreCase, `{"x":[{"suffix":{"equals-ignore-case":"a"}}]}`},
		{OperatorEqualsIgnoreCase, `{"x":[{"equals-ignore-case":"a"}]}`},
		{OperatorWildcard, `{"x":[{"wildcard":"a*"}]}`},
		{OperatorAnythingBut, `{"x":[{"anything-but":"a"}]}`},
		{OperatorAnythingButPrefix, `{"x":[{"anything-but":{"prefix":"a"}}]}`},
		{OperatorAnythingButSuffix, `{"x":[{"anything-but":{"suffix":"a"}}]}`},
		{OperatorAnythingButEqualsIgnoreCase, `{"x":[{"anything-but":{"equals-ignore-case":"a"}}]}`},
		{OperatorAnythingButWildcard, `{"x":[{"anything-but":{"wildcard":"a*"}}]}`},
		{OperatorNumeric, `{"x":[{"numeric":[">",1]}]}`},
		{OperatorExists, `{"x":[{"exists":true}]}`},
		{OperatorCIDR, `{"x":[{"cidr":"10.0.0.0/8"}]}`},
		{OperatorOr, `{"$or":[{"x":["a"]},{"y":["b"]}]}`},
	}
	rejected := map[string]map[Operator]string{
		"EventBridge": {},
		"SNS": {
			OperatorPrefixEqualsIgnoreCase:      "prefix match pattern must be a string",
			OperatorSuffixEqualsIgnoreCase:      "suffix match pattern must be a string",
			OperatorAnythingButEqualsIgnoreCase: "Unsupported anything-but pattern: equals-ignore-case",
		},
		"LambdaESM": {
			OperatorPrefixEqualsIgnoreCase:      "prefix match pattern must be a string",
			OperatorSuffixEqualsIgnoreCase:      "suffix match pattern must be a string",
			OperatorWildcard:                    "Unrecognized match type wildcard",
			OperatorAnythingButPrefix:           "Unsupported anything-but pattern: prefix",
			OperatorAnythingButSuffix:           "Unsupported anything-but pattern: suffix",
			OperatorAnythingButEqualsIgnoreCase: "Unsupported anything-but pattern: equals-ignore-case",
			OperatorAnythingButWildcard:         "Unsupported anything-but pattern: wildcard",
			OperatorCIDR:                        "Unrecognized match type cidr",
		},
	}
	profiles := map[string]Profile{"EventBridge": EventBridge{}, "SNS": SNS{Scope: MessageBody}, "LambdaESM": LambdaESM{}}
	for name, p := range profiles {
		for _, o := range operands {
			_, err := Compile([]byte(o.pattern), p)
			detail, wantReject := rejected[name][o.op]
			switch {
			case !wantReject && err != nil:
				t.Errorf("%s rejected %s: %s", name, o.op, err.Detail)
			case wantReject && err == nil:
				t.Errorf("%s accepted %s; want %q", name, o.op, detail)
			case wantReject && (err.Cause != UnsupportedOperator{Operator: o.op} || err.Detail != detail):
				t.Errorf("%s on %s = %#v %q, want %q", name, o.op, err.Cause, err.Detail, detail)
			}
		}
	}
}

// TestExactlyIsUnknown: Ruler's "exactly" is documented by no service.
func TestExactlyIsUnknown(t *testing.T) {
	t.Parallel()
	for _, p := range []Profile{EventBridge{}, SNS{}, LambdaESM{}} {
		_, err := Compile([]byte(`{"x":[{"exactly":"a"}]}`), p)
		if err == nil || err.Detail != "Unrecognized match type exactly" {
			t.Errorf("%T: Compile(exactly) = %v", p, err)
		}
	}
}

// TestOptions flips each Options field away from Ruler and checks the
// documented alternative (matching-engines.md 10).
func TestOptions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		options Options
		pattern string
		event   string
		ruler   bool
	}{
		{"exists true on null", Options{ExistsTrue: ExistsTrueNonEmpty}, `{"x":[{"exists":true}]}`, `{"x":null}`, true},
		{"exists true on empty string", Options{ExistsTrue: ExistsTrueNonEmpty}, `{"x":[{"exists":true}]}`, `{"x":""}`, true},
		{"exists false on empty event", Options{EmptyInput: EmptyInputNeverMatches}, `{"x":[{"exists":false}]}`, `{}`, true},
		{"bare number exponent form", Options{BareNumber: BareNumberTextOnly}, `{"x":[300]}`, `{"x":3.0e2}`, true},
		{"bare number decimal form", Options{BareNumber: BareNumberTextOnly}, `{"x":[300]}`, `{"x":300.0}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, opts := range []Options{{}, tc.options} {
				p, err := Compile([]byte(tc.pattern), EventBridge{Options: opts})
				if err != nil {
					t.Fatalf("Compile: %s", err.Detail)
				}
				got, merr := Match(p, []byte(tc.event))
				if merr != nil {
					t.Fatalf("Match: %v", merr)
				}
				want := tc.ruler == (opts == Options{})
				if got != want {
					t.Errorf("options %+v: Match = %t, want %t", opts, got, want)
				}
			}
		})
	}
	identical := mustCompile(t, `{"x":[300]}`, LambdaESM{Options: Options{BareNumber: BareNumberTextOnly}})
	if !matchEvent(t, identical, `{"x":300}`) {
		t.Error("text-only bare number does not match identical text")
	}
}

// TestMalformedOr: the strict compiler rejects malformed "$or" shapes; the
// ordinary-key option recompiles with "$or" as a plain key, as
// Machine.addRule does (V70 to V72).
func TestMalformedOr(t *testing.T) {
	t.Parallel()
	ordinary := SNS{Scope: MessageBody, Options: Options{MalformedOr: MalformedOrOrdinaryKey}}
	p := mustCompile(t, `{"s":["a"],"$or":{"m":["x"]}}`, ordinary)
	if !matchEvent(t, p, `{"s":"a","$or":{"m":"x"}}`) {
		t.Error("V71 with ordinary-key does not match the $or.m event")
	}
	if matchEvent(t, p, `{"s":"a","m":"x"}`) {
		t.Error("V71 with ordinary-key matched without the $or key")
	}
	p = mustCompile(t, `{"$or":["x","y"]}`, ordinary)
	if !matchEvent(t, p, `{"$or":"y"}`) {
		t.Error(`{"$or":["x","y"]} does not match {"$or":"y"}`)
	}
	for pattern, detail := range map[string]string{
		`{"$or":[{"m":["x"]}]}`:                      "Unrecognized match type m",
		`{"$or":[{"numeric":123},{"prefix":"abc"}]}`: "Value of numeric must be an array.",
	} {
		_, err := Compile([]byte(pattern), ordinary)
		if err == nil || err.Detail != detail {
			t.Errorf("ordinary-key Compile(%s) = %v, want %q", pattern, err, detail)
		}
	}
}

// TestSwitches: each profile names its switches, the Options fields and then
// the allow-list and limit choices made without AWS-recorded evidence, and
// points each one at the record-backlog item or matching-engines.md section 8
// item that covers its service, or at none.
func TestSwitches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		profile Profile
		want    []Switch
	}{
		{EventBridge{Options: Options{EmptyInput: EmptyInputNeverMatches}}, []Switch{
			{Name: "events.pattern.exists-true-empty-value", Value: "any-value", Ref: switchRef, Record: ""},
			{Name: "events.pattern.empty-input", Value: "never-matches", Ref: switchRef, Record: "matching-engines.md H1"},
			{Name: "events.pattern.bare-number", Value: "numeric-or-text", Ref: switchRef, Record: ""},
			{Name: "events.pattern.malformed-or", Value: "rejected", Ref: switchRef, Record: "matching-engines.md H10"},
			{Name: "events.pattern.exactly-operator", Value: "rejected", Ref: operatorsRef, Record: "matching-engines.md H14"},
			{Name: "events.pattern.or-shared-values", Value: "documented", Ref: humanRecordsRef, Record: "matching-engines.md H14"},
			{Name: "events.pattern.wildcard-complexity-cap", Value: "11", Ref: humanRecordsRef, Record: "matching-engines.md H11"},
			{Name: "events.pattern.or-combination-counting", Value: "product-including-nested", Ref: humanRecordsRef, Record: "matching-engines.md H12"},
		}},
		{SNS{Options: Options{ExistsTrue: ExistsTrueNonEmpty}}, []Switch{
			{Name: "sns.filter-policy.exists-true-empty-value", Value: "non-empty", Ref: switchRef, Record: "matching-engines.md H2"},
			{Name: "sns.filter-policy.empty-input", Value: "matchable", Ref: switchRef, Record: "R09/6 (matching-engines.md H1)"},
			{Name: "sns.filter-policy.bare-number", Value: "numeric-or-text", Ref: switchRef, Record: "R09/1 (matching-engines.md H6)"},
			{Name: "sns.filter-policy.malformed-or", Value: "rejected", Ref: switchRef, Record: "matching-engines.md H10"},
			{Name: "sns.filter-policy.exactly-operator", Value: "rejected", Ref: operatorsRef, Record: "matching-engines.md H7"},
			{Name: "sns.filter-policy.anything-but-suffix", Value: "accepted", Ref: humanRecordsRef, Record: "R09/7 (matching-engines.md H7)"},
			{Name: "sns.filter-policy.anything-but-list-forms", Value: "accepted", Ref: operatorsRef, Record: "matching-engines.md H7"},
			{Name: "sns.filter-policy.ignore-case-forms", Value: "rejected", Ref: humanRecordsRef, Record: "matching-engines.md H7"},
			{Name: "sns.filter-policy.key-counting", Value: "leaf-keys-per-branch", Ref: humanRecordsRef, Record: "R09/11, R09/12 (matching-engines.md H3)"},
			{Name: "sns.filter-policy.wildcard-points", Value: "3-per-star", Ref: humanRecordsRef, Record: "matching-engines.md H11"},
			{Name: "sns.filter-policy.limit-order", Value: "nesting-keys-combinations-wildcards", Ref: "docs/research/phase1/sns.md#6", Record: "R09/13"},
			{Name: "sns.filter-policy.number-attribute-type", Value: "json-number", Ref: "docs/research/phase1/matching-engines.md#5.4", Record: "R09/19 (matching-engines.md H6)"},
			{Name: "sns.filter-policy.numeric-range", Value: "binary64", Ref: "docs/research/phase1/matching-engines.md#5.2", Record: "R09/15 (matching-engines.md H5)"},
			{Name: "sns.filter-policy.anything-but-arrays", Value: "any-element", Ref: "docs/research/phase1/matching-engines.md#4.3", Record: "R09/8 (matching-engines.md H4)"},
			{Name: "sns.filter-policy.array-consistency", Value: "enforced", Ref: "docs/research/phase1/matching-engines.md#4.3", Record: "R09/14"},
		}},
		{LambdaESM{Options: Options{BareNumber: BareNumberTextOnly, MalformedOr: MalformedOrOrdinaryKey}}, []Switch{
			{Name: "lambda.esm-filter.exists-true-empty-value", Value: "any-value", Ref: switchRef, Record: "matching-engines.md H2"},
			{Name: "lambda.esm-filter.empty-input", Value: "matchable", Ref: switchRef, Record: ""},
			{Name: "lambda.esm-filter.bare-number", Value: "text-only", Ref: switchRef, Record: "matching-engines.md H7"},
			{Name: "lambda.esm-filter.malformed-or", Value: "ordinary-key", Ref: switchRef, Record: "matching-engines.md H10"},
			{Name: "lambda.esm-filter.exactly-operator", Value: "rejected", Ref: operatorsRef, Record: "matching-engines.md H13"},
			{Name: "lambda.esm-filter.operator-allow-list", Value: "documented-table", Ref: humanRecordsRef, Record: "matching-engines.md H13"},
		}},
	}
	for _, tc := range tests {
		got := Switches(tc.profile)
		if !slices.Equal(got, tc.want) {
			t.Errorf("Switches(%#v) = %+v, want %+v", tc.profile, got, tc.want)
		}
	}
}
