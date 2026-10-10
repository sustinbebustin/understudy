package ruler

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	"pgregory.net/rapid"
)

// TestSNSRecordedErrors replays the AWS-recorded SNS filter policy errors of
// sns.md 6.5; the detail is the text after "Invalid parameter: FilterPolicy: ".
func TestSNSRecordedErrors(t *testing.T) {
	t.Parallel()
	body := SNS{Scope: MessageBody}
	attrs := SNS{Scope: MessageAttributes}
	tests := []struct {
		policy  string
		profile SNS
		detail  string
		cause   Cause
	}{
		{`invalid`, attrs, "failed to parse JSON. Unrecognized token 'invalid': was expecting (JSON String, Number, Array, Object or token 'null', 'true' or 'false')", InvalidJSON{}},
		{`{"key":[]}`, attrs, "Empty arrays are not allowed", InvalidStructure{}},
		{`{"key":"value_one"}`, attrs, `"key" must be an object or an array`, InvalidStructure{}},
		{`{"key":{"suffix":"v"}}`, attrs, `"suffix" must be an object or an array`, InvalidStructure{}},
		{`{"key":{"suffix":"v"}}`, body, `"suffix" must be an object or an array`, InvalidStructure{}},
		{`{"key":[["value"]]}`, attrs, "Match value must be String, number, true, false, or null", InvalidStructure{}},
		{`{"key":[{"wrong-operator":true}]}`, attrs, "Unrecognized match type wrong-operator", UnknownOperator{Name: "wrong-operator"}},
		{`{"key":[{"suffix":"v","prefix":"w"}]}`, attrs, "Only one key allowed in match expression", InvalidOperand{Operator: OperatorSuffix}},
		{`{"key":[{"suffix":100}]}`, attrs, "suffix match pattern must be a string", InvalidOperand{Operator: OperatorSuffix}},
		{`{"key":[{"suffix":null}]}`, attrs, "suffix match pattern must be a string", InvalidOperand{Operator: OperatorSuffix}},
		{`{"key":[{"suffix":[]}]}`, attrs, "suffix match pattern must be a string", InvalidOperand{Operator: OperatorSuffix}},
		{`{"key":[{"suffix":["a","b"]}]}`, attrs, "suffix match pattern must be a string", InvalidOperand{Operator: OperatorSuffix}},
		{`{"key":[{"prefix":100}]}`, attrs, "prefix match pattern must be a string", InvalidOperand{Operator: OperatorPrefix}},
		{`{"key":[{"equals-ignore-case":100}]}`, attrs, "equals-ignore-case match pattern must be a string", InvalidOperand{Operator: OperatorEqualsIgnoreCase}},
		{`{"key":[{"exists":null}]}`, attrs, "exists match pattern must be either true or false.", InvalidOperand{Operator: OperatorExists}},
		{`{"key":[{"exists":"no"}]}`, attrs, "exists match pattern must be either true or false.", InvalidOperand{Operator: OperatorExists}},
		{`{"key":[{"anything-but":{"wrong-operator":null}}]}`, attrs, "Unsupported anything-but pattern: wrong-operator", InvalidOperand{Operator: OperatorAnythingBut}},
		{`{"key":[{"anything-but":{"exists":false}}]}`, attrs, "Unsupported anything-but pattern: exists", InvalidOperand{Operator: OperatorAnythingBut}},
		{`{"key":[{"anything-but":{"prefix":false}}]}`, attrs, "prefix match pattern must be a string", InvalidOperand{Operator: OperatorAnythingButPrefix}},
		{`{"key":[{"cidr":["bad-filter"]}]}`, attrs, "prefix match pattern must be a string", InvalidOperand{Operator: OperatorCIDR}},
		{`{"key":[{"cidr":"bad-filter"}]}`, attrs, "Malformed CIDR, one '/' required", InvalidOperand{Operator: OperatorCIDR}},
		{`{"key":[{"cidr":"bad-filter/64"}]}`, attrs, "Nonstandard IP address: bad-filter", InvalidOperand{Operator: OperatorCIDR}},
		{`{"key":[{"cidr":"bad-/64filter"}]}`, attrs, "Malformed CIDR, mask bits must be an integer", InvalidOperand{Operator: OperatorCIDR}},
		{`{"key":[{"cidr":"xx.11.xx/8"}]}`, attrs, "Nonstandard IP address: xx.11.xx", InvalidOperand{Operator: OperatorCIDR}},
		{`{"key":[{"cidr":"xxxx:db8:1234:1a00::/64"}]}`, attrs, "Nonstandard IP address: xxxx:db8:1234:1a00::", InvalidOperand{Operator: OperatorCIDR}},
		{`{"key":{"not-an-operator":"value"}}`, attrs, `"not-an-operator" must be an object or an array`, InvalidStructure{}},
		{`{"key":[{"numeric":[]}]}`, attrs, "Invalid member in numeric match: ]", InvalidOperand{Operator: OperatorNumeric}},
		{`{"key":[{"numeric":["operator"]}]}`, attrs, "Unrecognized numeric range operator: operator", InvalidOperand{Operator: OperatorNumeric}},
		{`{"key":[{"numeric":[1,"<="]}]}`, attrs, "Invalid member in numeric match: 1", InvalidOperand{Operator: OperatorNumeric}},
		{`{"key":[{"numeric":["=","000"]}]}`, attrs, "Value of equals must be numeric", InvalidOperand{Operator: OperatorNumeric}},
		{`{"key":[{"numeric":[">="]}]}`, attrs, "Value of >= must be numeric", InvalidOperand{Operator: OperatorNumeric}},
		{`{"key":[{"numeric":["<",100,">",10]}]}`, attrs, "Too many elements in numeric expression", InvalidOperand{Operator: OperatorNumeric}},
		{`{"key":[{"numeric":[">=",1,">",2]}]}`, attrs, "Bad numeric range operator: >", InvalidOperand{Operator: OperatorNumeric}},
		{`{"key":[{"numeric":[">",3,"<",1]}]}`, attrs, "Bottom must be less than top", InvalidOperand{Operator: OperatorNumeric}},
		{`{"key":[{"numeric":[">",20,"<="]}]}`, attrs, "Value of <= must be numeric", InvalidOperand{Operator: OperatorNumeric}},
		{`{"key":[{"numeric":[">",20,30]}]}`, attrs, "Bad value in numeric range: 30", InvalidOperand{Operator: OperatorNumeric}},
		{`{"key":[{"numeric":[">",20,"test"]}]}`, attrs, "Bad numeric range operator: test", InvalidOperand{Operator: OperatorNumeric}},
		{`{"key":[{"numeric":[">","20","<","30"]}]}`, attrs, "Value of > must be numeric", InvalidOperand{Operator: OperatorNumeric}},
		{`{"key":[{"numeric":[">",20,"<","30"]}]}`, attrs, "Value of < must be numeric", InvalidOperand{Operator: OperatorNumeric}},
		{`{"key":[{"numeric":[">",20,"<",30,"<",50]}]}`, attrs, "Too many terms in numeric range expression", InvalidOperand{Operator: OperatorNumeric}},
		{`{"a":["1"],"b":["1"],"c":["1"],"d":["1"],"e":["1"],"f":["1"]}`, attrs, "Filter policy can not have more than 5 keys", LimitExceeded{Limit: LimitKeys}},
		{`{"object":{"key":[{"prefix":"auto-"}]}}`, attrs, "Filter policy scope MessageAttributes does not support nested filter policy", LimitExceeded{Limit: LimitNesting}},
	}
	for _, tc := range tests {
		t.Run(tc.policy, func(t *testing.T) {
			t.Parallel()
			_, err := Compile([]byte(tc.policy), tc.profile)
			if err == nil {
				t.Fatalf("Compile accepted %s; want %q", tc.policy, tc.detail)
			}
			if err.Detail != tc.detail || err.Cause != tc.cause {
				t.Fatalf("Compile(%s) = %#v %q, want %#v %q", tc.policy, err.Cause, err.Detail, tc.cause, tc.detail)
			}
		})
	}
}

// TestEventBridgeRecordedErrors covers the validation texts recorded for
// EventBridge in secrets-logs-events-alarms.md 4.4 that the seed vectors do
// not.
func TestEventBridgeRecordedErrors(t *testing.T) {
	t.Parallel()
	tests := []struct{ pattern, detail string }{
		{`{"x":[{"prefix":1}]}`, "prefix match pattern must be a string"},
		{`{"x":[{"suffix":true}]}`, "suffix match pattern must be a string"},
		{`{"x":[{"anything-but":{"prefix":1}}]}`, "prefix/suffix match pattern must be a string"},
		{`{"x":[{"equals-ignore-case":1}]}`, "equals-ignore-case match pattern must be a string"},
		{`{"x":[{"wildcard":1}]}`, "wildcard match pattern must be a string"},
		{`{"x":[{"anything-but":{"prefix":{"equals-ignore-case":"b"}}}]}`, "Value of anything-but must be an array or single string/number value."},
		{`{"x":[{"anything-but":{"prefix":"a","equals-ignore-case":"b"}}]}`, "Too many elements in numeric expression"},
		{`{"x":[{"anything-but":true}]}`, "Value of anything-but must be an array or single string/number value."},
		{`{"x":[{"anything-but":["a",null]}]}`, "Inside anything but list, start|null|boolean is not supported."},
		{`{"x":[{"numeric":5}]}`, "Value of numeric must be an array."},
		{`{"x":[{"wildcard":"a:*:*:*:*:*:*:*:*:*:*:*:*"}]}`, "Rule is too complex - try using fewer wildcard characters or fewer repeating character sequences after a wildcard character"},
		{`{'x':["a"]}`, "Unexpected character (''' (code 39)): was expecting double-quote to start field name"},
		{`{"x":[closed]}`, "Unrecognized token 'closed': was expecting (JSON String, Number, Array, Object or token 'null', 'true' or 'false')"},
		{`{"x":["a"]}{"y":["b"]}`, "Filter must be a single JSON object; found content after it"},
		{`{}`, "Empty objects are not allowed"},
		{`{"x":[{}]}`, "Match expression name not found"},
	}
	for _, tc := range tests {
		t.Run(tc.pattern, func(t *testing.T) {
			t.Parallel()
			_, err := Compile([]byte(tc.pattern), EventBridge{})
			if err == nil || err.Detail != tc.detail {
				t.Fatalf("Compile(%s) = %v, want detail %q", tc.pattern, err, tc.detail)
			}
		})
	}
}

// TestJacksonSyntaxErrors: malformed JSON is worded as Jackson 2.22.2's
// ReaderBasedJsonParser words it, the text EventBridge returns after
// "Reason: ". A number ends at the first character that cannot continue it,
// which the enclosing array then rejects.
func TestJacksonSyntaxErrors(t *testing.T) {
	t.Parallel()
	const plus = "Unexpected character ('+' (code 43)) in numeric value: JSON spec does not allow numbers to have " +
		"plus signs: enable `JsonReadFeature.ALLOW_LEADING_PLUS_SIGN_FOR_NUMBERS` to allow"
	tests := []struct{ value, detail string }{
		{`01`, "Invalid numeric value: Leading zeroes not allowed"},
		{`-00`, "Invalid numeric value: Leading zeroes not allowed"},
		{`1.`, "Unexpected character (']' (code 93)) in numeric value: Decimal point not followed by a digit"},
		{`1.e5`, "Unexpected character ('e' (code 101)) in numeric value: Decimal point not followed by a digit"},
		{`1e`, "Unexpected character (']' (code 93)) in numeric value: Exponent indicator not followed by a digit"},
		{`1e+`, "Unexpected character (']' (code 93)) in numeric value: Exponent indicator not followed by a digit"},
		{`-`, "Unexpected character (']' (code 93)) in numeric value: expected digit (0-9) to follow minus sign, " +
			"for valid numeric value"},
		{`-a`, "Unexpected character ('a' (code 97)) in numeric value: expected digit (0-9) to follow minus sign, " +
			"for valid numeric value"},
		{`+1`, plus},
		{`+`, plus},
		{`-Infinity`, "Non-standard token '-Infinity': enable `JsonReadFeature.ALLOW_NON_NUMERIC_NUMBERS` to allow"},
		{`NaN`, "Non-standard token 'NaN': enable `JsonReadFeature.ALLOW_NON_NUMERIC_NUMBERS` to allow"},
		{`1x`, "Unexpected character ('x' (code 120)): was expecting comma to separate Array entries"},
		{`.5`, "Unexpected character ('.' (code 46)): expected a valid value " +
			"(JSON String, Number, Array, Object or token 'null', 'true' or 'false')"},
		{`#`, "Unexpected character ('#' (code 35)): expected a valid value " +
			"(JSON String, Number, Array, Object or token 'null', 'true' or 'false')"},
		{`1Ā`, "Unexpected character ('Ā' (code 256 / 0x100)): was expecting comma to separate Array entries"},
		{"1\x7f", "Unexpected character ((CTRL-CHAR, code 127)): was expecting comma to separate Array entries"},
	}
	for _, tc := range tests {
		pattern := `{"x":[` + tc.value + `]}`
		_, err := Compile([]byte(pattern), EventBridge{})
		if err == nil || err.Cause != (InvalidJSON{}) || err.Detail != tc.detail {
			t.Errorf("Compile(%s) = %v, want %q", pattern, err, tc.detail)
		}
	}
	for _, src := range []string{`{"x":[1.`, `{"x":[1e`, `{"x":[-`} {
		if _, err := Compile([]byte(src), EventBridge{}); err == nil || err.Cause != (InvalidJSON{}) {
			t.Errorf("Compile(%s) = %v, want a JSON error", src, err)
		}
	}
}

// TestJacksonStructureErrors: whole patterns whose JSON is malformed around
// the values, with the detail executed Ruler v2.2.0 returned (Jackson's
// location suffix, "\n at [Source: ...]", is not part of the detail).
func TestJacksonStructureErrors(t *testing.T) {
	t.Parallel()
	const value = "expected a valid value " + validValues
	unrecognized := func(token string) string {
		return "Unrecognized token '" + token + "': was expecting " + validValues
	}
	tests := []struct{ pattern, detail string }{
		{`{"a":["x",]}`, "Unexpected character (']' (code 93)): " + value},
		{`{"a":[,]}`, "Unexpected character (',' (code 44)): " + value},
		{`{"a":[:]}`, "Unexpected character (':' (code 58)): " + value},
		{`{"a":["x"]},`, "Unexpected character (',' (code 44)): " + value},
		{`{"x":[{"EXISTS":}]}`, "Unexpected character ('}' (code 125)): expected a value"},
		{`{"x":[{"EXISTS":]}]}`, "Unexpected character (']' (code 93)): " + value},
		{"{\"a\x01\":[\"x\"]}", "Illegal unquoted character ((CTRL-CHAR, code 1)): has to be escaped using backslash to be included in name"},
		{"{\"a\":[\"x\x01\"]}", "Illegal unquoted character ((CTRL-CHAR, code 1)): has to be escaped using backslash to be included in string value"},
		{`{"a`, "Unexpected end-of-input in field name"},
		{`{"a":["x`, "Unexpected end-of-input: was expecting closing quote for a string value"},
		{"{\"a\":\x01[\"x\"]}", `Illegal character ((CTRL-CHAR, code 1)): only regular white space (\r, \n, \t) is allowed between tokens`},
		{"{\"a\":[1]\x1e}", `Illegal character ((CTRL-CHAR, code 30)): only regular white space (\r, \n, \t) is allowed between tokens` +
			" (consider enabling `JsonReadFeature.ALLOW_RS_CONTROL_CHAR` to allow use of Record Separators (\\u001E))"},
		{`{"a":[/"x"]}`, "Unexpected character ('/' (code 47)): maybe a (non-standard) comment? " +
			"(not recognized as one since Feature 'ALLOW_COMMENTS' not enabled for parser)"},
		{`{"a":["x"]`, `Unexpected end-of-input: expected close marker for Object (start marker at [Source: (String)"{"a":["x"]"; line: 1, column: 1])`},
		{`{"a":["x"`, `Unexpected end-of-input: expected close marker for Array (start marker at [Source: (String)"{"a":["x""; line: 1, column: 6])`},
		{"\n\n  {\"a\":\r\n [\"x\"", "Unexpected end-of-input: expected close marker for Array (start marker at [Source: (String)\"\n\n  {\"a\":\r\n [\"x\"\"; line: 4, column: 2])"},
		{`{"a😀b":{"c":["x"`, `Unexpected end-of-input: expected close marker for Array (start marker at [Source: (String)"{"a😀b":{"c":["x""; line: 1, column: 14])`},
		{"{\"a\":[\"x\",\x01", `Illegal character ((CTRL-CHAR, code 1)): only regular white space (\r, \n, \t) is allowed between tokens`},
		{`{"a":["x",`, "Unexpected end-of-input within/between Array entries"},
		{`{"a":`, "Unexpected end-of-input within/between Object entries"},
		{`{"a"`, "Unexpected end-of-input within/between Object entries"},
		{`{"a":["x"}`, `Unexpected close marker '}': expected ']' (for Array starting at [Source: (String)"{"a":["x"}"; line: 1, column: 6])`},
		{`{"a":["x"]]`, `Unexpected close marker ']': expected '}' (for Object starting at [Source: (String)"{"a":["x"]]"; line: 1, column: 1])`},
		{`{"a":["x"]}]`, "Unexpected close marker ']': no open Array to close"},
		{`}`, "Unexpected close marker '}': no open Object to close"},
		{`{"a":[1],}`, "Unexpected character ('}' (code 125)): was expecting double-quote to start field name"},
		{`{"a":[x*]}`, unrecognized("x")},
		{`{"a":[fal1.e]}`, unrecognized("fal1")},
		{`{"x":[{"EXISTS":tru}]}`, unrecognized("tru")},
		{`{"a":[true_]}`, unrecognized("true_")},
		{`{"a":[true$]}`, "Unexpected character ('$' (code 36)): was expecting comma to separate Array entries"},
		{`{"a":[Infinityx]}`, unrecognized("Infinityx")},
		{`{"a":[Infinity:]}`, "Non-standard token 'Infinity': enable `JsonReadFeature.ALLOW_NON_NUMERIC_NUMBERS` to allow"},
		{`{"a":[INF]}`, unrecognized("INF")},
		{`{"a":[-INF]}`, "Non-standard token '-INF': enable `JsonReadFeature.ALLOW_NON_NUMERIC_NUMBERS` to allow"},
		{`{"a":[-INx]}`, unrecognized("-INx")},
		{`{"a":[-Inf]}`, unrecognized("-Inf")},
		{`{"a":[-Ix]}`, "Unexpected character ('x' (code 120)) in numeric value: expected digit (0-9) to follow minus sign, for valid numeric value"},
		{`{"a":[-I`, "Unexpected end-of-input in a Number value"},
		{`{"a":[+Ix]}`, "Unexpected character ('+' (code 43)) in numeric value: JSON spec does not allow numbers to have plus signs: " +
			"enable `JsonReadFeature.ALLOW_LEADING_PLUS_SIGN_FOR_NUMBERS` to allow"},
		{`{"a":[😀]}`, "Unexpected character ('?' (code 55357 / 0xd83d)): " + value},
		{`{"a":[` + strings.Repeat("a", 300) + `]}`, unrecognized(strings.Repeat("a", maxErrorTokenLength) + "...")},
		{`{"a":["\q"]}`, "Unrecognized character escape 'q' (code 113)"},
		{"{\"a\":[\"\\\x01\"]}", "Unrecognized character escape (CTRL-CHAR, code 1)"},
		{`{"a":["\u12x4"]}`, "Unexpected character ('x' (code 120)): expected a hex-digit for character escape sequence"},
		{`{"a":["\u12`, "Unexpected end-of-input in character escape sequence"},
		{`1x`, "Unexpected character ('x' (code 120)): Expected space separating root-level values"},
		{`1e5]`, "Unexpected character (']' (code 93)): Expected space separating root-level values"},
		{`{"a":[` + strings.Repeat("1", 1001) + `]}`, "Number value length (1001) exceeds the maximum allowed (1000, " +
			"from `StreamReadConstraints.getMaxNumberLength()`)"},
		{`{"a":[1e` + strings.Repeat("1", 1000) + `]}`, "Number value length (1001) exceeds the maximum allowed (1000, " +
			"from `StreamReadConstraints.getMaxNumberLength()`)"},
		{`{"` + strings.Repeat("a", 50001) + `":["x"]}`, "Name length (50001) exceeds the maximum allowed (50000, " +
			"from `StreamReadConstraints.getMaxNameLength()`)"},
		{`{"a":["` + strings.Repeat("x", 600) + `"]`, `Unexpected end-of-input: expected close marker for Object (start marker at ` +
			`[Source: (String)"{"a":["` + strings.Repeat("x", 493) + `"[truncated 109 chars]; line: 1, column: 1])`},
		{`{"a":["` + strings.Repeat("x", 40000) + `"]`, "Unexpected end-of-input: expected close marker for Object (start marker at " +
			"[Source: (StringReader); line: 1, column: 1])"},
	}
	for _, tc := range tests {
		_, err := Compile([]byte(tc.pattern), EventBridge{})
		if err == nil || err.Cause != (InvalidJSON{}) || err.Detail != tc.detail {
			t.Errorf("Compile(%.80q) = %v, want %q", tc.pattern, err, tc.detail)
		}
	}
}

// TestJacksonReadsLazily: Jackson reads a scalar value together with its
// field name, so its syntax error comes first, but reads a string value only
// when Ruler asks for its text, so a rejection for the token's kind comes
// first. Details from executed Ruler v2.2.0.
func TestJacksonReadsLazily(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern, detail string
		cause           Cause
	}{
		{`{"x":[{"EXISTS":1x}]}`, "Unrecognized match type EXISTS", UnknownOperator{Name: "EXISTS"}},
		{`{"x":[{"EXISTS":"a\q"}]}`, "Unrecognized match type EXISTS", UnknownOperator{Name: "EXISTS"}},
		{`"`, "Filter is not an object", InvalidStructure{}},
		{`{"a":"x`, `"a" must be an object or an array`, InvalidStructure{}},
		{`{"a":["x"]}"`, "Filter must be a single JSON object; found content after it", InvalidStructure{}},
		{`{"a":[{"prefix":"\q"}]}`, "Unrecognized character escape 'q' (code 113)", InvalidJSON{}},
		{`{"a":[{"cidr":"10.0.0.0/8\q"}]}`, "Unrecognized character escape 'q' (code 113)", InvalidJSON{}},
		{`{"a":[{"numeric":["\q"]}]}`, "Unrecognized character escape 'q' (code 113)", InvalidJSON{}},
		{`{"a":[0e` + strings.Repeat("1", 1000) + `]}`, "", nil},
	}
	for _, tc := range tests {
		_, err := Compile([]byte(tc.pattern), EventBridge{})
		if tc.cause == nil {
			if err != nil {
				t.Errorf("Compile(%.80q) = %v, want it accepted", tc.pattern, err)
			}
			continue
		}
		if err == nil || err.Cause != tc.cause || err.Detail != tc.detail {
			t.Errorf("Compile(%.80q) = %v, want %#v %q", tc.pattern, err, tc.cause, tc.detail)
		}
	}
}

// TestCompareUTF16 orders strings as Java's String.compareTo does.
func TestCompareUTF16(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		a, b := rapid.String().Draw(t, "a"), rapid.String().Draw(t, "b")
		want := slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
		if got := compareUTF16(a, b); got != want {
			t.Fatalf("compareUTF16(%q, %q) = %d, want %d", a, b, got, want)
		}
	})
	if compareUTF16("\U0001F600", "！") >= 0 {
		t.Error("an emoji does not sort before U+FF01")
	}
}

// TestMessages checks each service's wire wording around the detail
// (matching-engines.md 5.3).
func TestMessages(t *testing.T) {
	t.Parallel()
	tests := []struct {
		profile Profile
		pattern string
		want    string
	}{
		{EventBridge{}, `{"x":[]}`, "Event pattern is not valid. Reason: Empty arrays are not allowed"},
		{SNS{}, `{"x":[]}`, "FilterPolicy: Empty arrays are not allowed"},
		{SNS{}, `{"o":{"k":["v"]}}`, "Filter policy scope MessageAttributes does not support nested filter policy"},
		{LambdaESM{}, `{"x":[]}`, "Invalid filter pattern definition."},
		{LambdaESM{}, `{"body":"plain"}`, "Invalid filter pattern definition."},
	}
	for _, tc := range tests {
		_, err := Compile([]byte(tc.pattern), tc.profile)
		if err == nil {
			t.Fatalf("Compile(%s, %T) accepted the pattern", tc.pattern, tc.profile)
		}
		if got := err.Message(); got != tc.want {
			t.Errorf("Compile(%s, %T).Message() = %q, want %q", tc.pattern, tc.profile, got, tc.want)
		}
	}
	double := &CompileError{Cause: InvalidStructure{}, Detail: "Empty arrays are not allowed"}
	if got := double.Message(); got != double.Detail {
		t.Errorf("Message() of a CompileError built outside Compile = %q, want its Detail", got)
	}
}

// TestSNSSyntaxErrorsWinOverGrammar: SNS parses the whole policy as JSON
// before compiling it, so a syntax error late in the policy is reported even
// when an earlier key is invalid; EventBridge reports the first problem in
// source order.
func TestSNSSyntaxErrorsWinOverGrammar(t *testing.T) {
	t.Parallel()
	policy := []byte(`{"a":[],"b":[nope]}`)
	_, err := Compile(policy, SNS{})
	if err == nil || !strings.HasPrefix(err.Detail, "failed to parse JSON. Unrecognized token 'nope'") {
		t.Fatalf("SNS Compile = %v, want a JSON parse failure", err)
	}
	_, err = Compile(policy, EventBridge{})
	if err == nil || err.Detail != "Empty arrays are not allowed" {
		t.Fatalf("EventBridge Compile = %v, want the empty-array error", err)
	}
}

// TestCIDRMaskBitsOutsideInt: Ruler reads mask bits with Java's
// Integer.parseInt, so a value outside int32 is not an integer at all
// rather than a mask that is too long.
func TestCIDRMaskBitsOutsideInt(t *testing.T) {
	t.Parallel()
	_, err := Compile([]byte(`{"ip":[{"cidr":"10.0.0.0/4294967296"}]}`), EventBridge{})
	want := "Malformed CIDR, mask bits must be an integer"
	if err == nil || err.Cause != (InvalidOperand{Operator: OperatorCIDR}) || err.Detail != want {
		t.Fatalf("Compile = %#v, want %q", err, want)
	}
}
