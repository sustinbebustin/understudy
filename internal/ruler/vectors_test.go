package ruler

import "testing"

// outcome is what a seed vector expects: a match, no match, a compile
// rejection with Ruler's detail text, or an event error.
type outcome struct {
	match      bool
	reject     string
	eventError string
}

var (
	matches   = outcome{match: true}
	noMatch   = outcome{}
	rejectsAs = func(detail string) outcome { return outcome{reject: detail} }
)

// seedVectors are matching-engines.md section 9, executed against Ruler
// v2.2.0. All of them run under the EventBridge profile, whose allow-list is
// Ruler's operator set minus the undocumented "exactly".
var seedVectors = []struct {
	id, pattern, event string
	want               outcome
}{
	{"V01", `{"x":["a","b"]}`, `{"x":"b"}`, matches},
	{"V02", `{"x":["a","b"]}`, `{"x":["c","b"]}`, matches},
	{"V03", `{"x":["a","b"]}`, `{"x":["c","d"]}`, noMatch},
	{"V04", `{"x":["abc"]}`, `{"x":"ABC"}`, noMatch},
	{"V05", `{"x":[""]}`, `{"x":""}`, matches},
	{"V06", `{"x":[""]}`, `{"x":null}`, noMatch},
	{"V07", `{"x":[null]}`, `{"x":null}`, matches},
	{"V08", `{"x":[null]}`, `{"x":""}`, noMatch},
	{"V09", `{"x":[true]}`, `{"x":"true"}`, noMatch},
	{"V10", `{"x":["true"]}`, `{"x":true}`, noMatch},
	{"V11", `{"x":[300]}`, `{"x":"300"}`, noMatch},
	{"V12", `{"x":[{"numeric":[">",100]}]}`, `{"x":"300"}`, noMatch},
	{"V13", `{"x":[{"numeric":[">",100]}]}`, `{"x":null}`, noMatch},
	{"V14", `{"x":[300]}`, `{"x":3.0e2}`, matches},
	{"V15", `{"x":[{"numeric":["=",300]}]}`, `{"x":300.0}`, matches},
	{"V16", `{"x":[{"numeric":[">",5]}]}`, `{"x":5}`, noMatch},
	{"V17", `{"x":[{"numeric":[">=",5]}]}`, `{"x":5}`, matches},
	{"V18", `{"x":[{"numeric":[">",0,"<=",150]}]}`, `{"x":150}`, matches},
	{"V19", `{"x":[{"numeric":[">",0,"<=",150]}]}`, `{"x":150.00001}`, noMatch},
	{"V20", `{"x":[{"numeric":["=",9007199254740993]}]}`, `{"x":1}`, rejectsAs("Cannot compare number : 9007199254740993")},
	{"V21", `{"x":[{"numeric":["=",1e309]}]}`, `{"x":1}`, rejectsAs("Cannot compare number : 1e309")},
	{"V22", `{"x":[12345678901234567890]}`, `{"x":12345678901234567890}`, matches},
	{"V23", `{"x":[12345678901234567890]}`, `{"x":12345678901234567891}`, noMatch},
	{"V24", `{"x":[{"numeric":[">",5]}]}`, `{"x":[1,10]}`, matches},
	{"V25", `{"account":[123456789012]}`, `{"account":123456789012}`, matches},
	{"V26", `{"x":[{"exists":false}]}`, `{"y":1}`, matches},
	{"V27", `{"x":[{"exists":false}]}`, `{"x":null}`, noMatch},
	{"V28", `{"x":[{"exists":false}]}`, `{"x":[]}`, matches},
	{"V29", `{"x":[{"exists":true}]}`, `{"x":[]}`, noMatch},
	{"V30", `{"x":[{"exists":true}]}`, `{"x":null}`, matches},
	{"V31", `{"x":[{"exists":true}]}`, `{"x":""}`, matches},
	{"V32", `{"a":{"b":[{"exists":false}]}}`, `{"a":{"b":{"c":1}}}`, matches},
	{"V33", `{"x":[{"exists":false}]}`, `{}`, matches},
	{"V34", `{"x":[{"exists":true}]}`, `{"x":{"y":1}}`, noMatch},
	{"V35", `{"x":[{"anything-but":["rugby","tennis"]}]}`, `{"x":["rugby","baseball"]}`, matches},
	{"V36", `{"x":[{"anything-but":["rugby","tennis"]}]}`, `{"x":["rugby"]}`, noMatch},
	{"V37", `{"x":[{"anything-but":["rugby","tennis"]}]}`, `{"x":["rugby","tennis"]}`, noMatch},
	{"V38", `{"x":[{"anything-but":"a"}]}`, `{"x":null}`, matches},
	{"V39", `{"x":[{"anything-but":"5"}]}`, `{"x":5}`, matches},
	{"V40", `{"x":[{"anything-but":[111,222]}]}`, `{"x":"111"}`, matches},
	{"V41", `{"x":[{"anything-but":[111,222]}]}`, `{"x":111}`, noMatch},
	{"V42", `{"x":[{"anything-but":{"prefix":"a"}}]}`, `{"x":12}`, matches},
	{"V43", `{"x":[{"anything-but":{"prefix":""}}]}`, `{"x":"a"}`, rejectsAs("Null prefix/suffix not allowed")},
	{"V44", `{"x":[{"anything-but":["a",1]}]}`, `{"x":"a"}`, rejectsAs("Inside anything but list, either all values are number or string, mixed type is not supported")},
	{"V45", `{"x":[{"anything-but":{"suffix":"ball"}}]}`, `{"x":["baseball","hockey"]}`, matches},
	{"V46", `{"x":[{"anything-but":{"suffix":"ball"}}]}`, `{"x":["baseball","basketball"]}`, noMatch},
	{"V47", `{"x":[{"anything-but":{"wildcard":"*ball"}}]}`, `{"x":["hockey","rugby"]}`, matches},
	{"V48", `{"x":[{"anything-but":{"wildcard":"*ball"}}]}`, `{"x":["baseball","basketball"]}`, noMatch},
	{"V49", `{"x":[{"anything-but":"a"},{"exists":false}]}`, `{"y":1}`, matches},
	{"V50", `{"x":[{"anything-but":{"equals-ignore-case":"ABC"}}]}`, `{"x":"abc"}`, noMatch},
	{"V51", `{"x":[{"wildcard":"a*c"}]}`, `{"x":"ac"}`, matches},
	{"V52", `{"x":[{"wildcard":"a\\*c"}]}`, `{"x":"a*c"}`, matches},
	{"V53", `{"x":[{"wildcard":"a\\*c"}]}`, `{"x":"abc"}`, noMatch},
	{"V54", `{"x":[{"wildcard":"a**c"}]}`, `{"x":"abc"}`, rejectsAs("Consecutive wildcard characters at pos 2")},
	{"V55", `{"x":[{"wildcard":"a\\nc"}]}`, `{"x":"abc"}`, rejectsAs("Invalid escape character at pos 2")},
	{"V56", `{"x":[{"wildcard":"*"}]}`, `{"x":5}`, noMatch},
	{"V57", `{"x":[{"wildcard":""}]}`, `{"x":""}`, matches},
	{"V58", `{"x":[{"wildcard":""}]}`, `{"x":"a"}`, noMatch},
	{"V59", `{"x":[{"prefix":""}]}`, `{"x":"abc"}`, matches},
	{"V60", `{"x":[{"prefix":"1"}]}`, `{"x":123}`, noMatch},
	{"V61", `{"x":[{"equals-ignore-case":"alice"}]}`, `{"x":"ALICE"}`, matches},
	{"V62", `{"x":[{"cidr":"10.0.0.0/24"}]}`, `{"x":"10.0.0.255"}`, matches},
	{"V63", `{"x":[{"cidr":"10.0.0.0/24"}]}`, `{"x":"10.0.1.0"}`, noMatch},
	{"V64", `{"x":[{"cidr":"10.0.0.1/32"}]}`, `{"x":"10.0.0.1"}`, rejectsAs("IPv4 mask bits must be < 32")},
	{"V65", `{"x":[{"cidr":"bad-filter/64"}]}`, `{"x":"10.0.0.1"}`, rejectsAs("Nonstandard IP address: bad-filter")},
	{"V66", `{"x":["10.0.0.1"]}`, `{"x":"10.0.0.1"}`, matches},
	{"V67", `{"x":["2001:db8::1"]}`, `{"x":"2001:0db8:0:0:0:0:0:1"}`, matches},
	{"V68", `{"s":["a"],"$or":[{"m":["x"]},{"n":["y"]}]}`, `{"s":"a","n":"y"}`, matches},
	{"V69", `{"s":["a"],"$or":[{"m":["x"]},{"n":["y"]}]}`, `{"s":"a"}`, noMatch},
	{"V70", `{"$or":[{"m":["x"]}]}`, `{"m":"x"}`, rejectsAs("There must have at least 2 Objects in $or relationship.")},
	{"V71", `{"s":["a"],"$or":{"m":["x"]}}`, `{"s":"a","$or":{"m":"x"}}`, rejectsAs("It must be an Array followed with $or.")},
	{"V72", `{"$or":[{"numeric":123},{"prefix":"abc"}]}`, `{"a":1}`, rejectsAs("numeric is Ruler reserved fieldName which cannot be used inside $or.")},
	{"V73", `{"employees":{"firstName":["Anna"],"lastName":["Jones"]}}`, `{"employees":[{"firstName":"John","lastName":"Doe"},{"firstName":"Anna","lastName":"Smith"},{"firstName":"Peter","lastName":"Jones"}]}`, noMatch},
	{"V74", `{"employees":{"firstName":["Anna"],"lastName":["Smith"]}}`, `{"employees":[{"firstName":"John","lastName":"Doe"},{"firstName":"Anna","lastName":"Smith"}]}`, matches},
	{"V75", `{"detail":{"state":{"status":["running"]}}}`, `{"detail":{"state.status":"running"}}`, matches},
	{"V76", `{"detail":{"location":[{"prefix":"us-"}],"location":[{"anything-but":"us-east"}]}}`, `{"detail":{"location":"us-east"}}`, noMatch},
	{"V77", `{"x":"a"}`, `{"x":"a"}`, rejectsAs(`"x" must be an object or an array`)},
	{"V78", `{"x":[]}`, `{"x":"a"}`, rejectsAs("Empty arrays are not allowed")},
	{"V79", `{"x":[["a"]]}`, `{"x":"a"}`, rejectsAs("Match value must be String, number, true, false, or null")},
	{"V80", `{"x":[{"prefix":"a","suffix":"b"}]}`, `{"x":"ab"}`, rejectsAs("Only one key allowed in match expression")},
	{"V81", `[1,2]`, `{"x":1}`, rejectsAs("Filter is not an object")},
	{"V82", `{"x":[{"EXISTS":true}]}`, `{"x":1}`, rejectsAs("Unrecognized match type EXISTS")},
	{"V83", `{"x":["a"]}`, `[1]`, outcome{eventError: "Event must be a JSON object"}},
	{"V84", `{"x":[{"numeric":[">=",1,">",2]}]}`, `{"x":3}`, rejectsAs("Bad numeric range operator: >")},
	{"V85", `{"x":[{"numeric":["=","300"]}]}`, `{"x":300}`, rejectsAs("Value of equals must be numeric")},
}

func TestSeedVectors(t *testing.T) {
	t.Parallel()
	if len(seedVectors) != 85 {
		t.Fatalf("seed vector table has %d rows, matching-engines.md section 9 has 85", len(seedVectors))
	}
	for _, v := range seedVectors {
		t.Run(v.id, func(t *testing.T) {
			t.Parallel()
			checkVector(t, v.pattern, v.event, v.want)
		})
	}
}

// TestIPv4LeadingZeros: Ruler reads an IPv4 literal with Java's
// InetAddress.getByName, whose octets are decimal with any leading zeros, in
// a text of at most 15 characters. Outcomes executed against Ruler v2.2.0.
func TestIPv4LeadingZeros(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern, event string
		want           outcome
	}{
		{`{"a":["10.0.0.1"]}`, `{"a":"010.0.0.1"}`, matches},
		{`{"a":["010.0.0.1"]}`, `{"a":"10.0.0.1"}`, matches},
		{`{"a":["10.0.0.01"]}`, `{"a":"10.0.0.1"}`, matches},
		{`{"a":["10.0.0.1"]}`, `{"a":"0010.0.000.0001"}`, matches},
		{`{"a":["10.0.0.1"]}`, `{"a":"00010.0.000.0001"}`, noMatch},
		{`{"a":["00010.0.000.0001"]}`, `{"a":"00010.0.000.0001"}`, matches},
		{`{"a":["00010.0.000.0001"]}`, `{"a":"10.0.0.1"}`, noMatch},
		{`{"a":[{"cidr":"010.0.0.0/8"}]}`, `{"a":"10.0.0.1"}`, matches},
		{`{"a":[{"cidr":"010.010.010.010/8"}]}`, `{"a":"10.200.0.1"}`, matches},
		{`{"a":[{"cidr":"0010.010.010.010/8"}]}`, `{"a":"10.0.0.1"}`, rejectsAs("Invalid IP address: 0010.010.010.010")},
		{`{"a":[{"cidr":"10.0.0.256/8"}]}`, `{"a":"10.0.0.1"}`, rejectsAs("Invalid IP address: 10.0.0.256")},
	}
	for _, tc := range tests {
		t.Run(tc.pattern+" "+tc.event, func(t *testing.T) {
			t.Parallel()
			checkVector(t, tc.pattern, tc.event, tc.want)
		})
	}
}

// TestJavaDoubleToString: Ruler compares a rule number only when it equals
// Java's Double.toString of its double, which writes the smallest
// subnormals with two digits. Outcomes executed against Ruler v2.2.0.
func TestJavaDoubleToString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		number string
		ok     bool
	}{
		{"4.9e-324", true},
		{"5e-324", false},
		{"4.94e-324", false},
		{"9.9e-324", true},
		{"1e-323", false},
		{"1.5e-323", true},
		{"2e-323", true},
		{"3e-322", true},
		{"1e-320", true},
		{"0.1", true},
	}
	for _, tc := range tests {
		want := matches
		if !tc.ok {
			want = rejectsAs("Cannot compare number : " + tc.number)
		}
		checkVector(t, `{"a":[{"numeric":["=",`+tc.number+`]}]}`, `{"a":`+tc.number+`}`, want)
	}
}

// TestRulerDefectsNotCopied pins the logical outcome where executed Ruler
// v2.2.0 gives another (doc.go lists them).
func TestRulerDefectsNotCopied(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, pattern, event string
		want                 outcome
	}{
		// Ruler's Machine.rulesForJSONEvent gives NO here and MATCH with the
		// members swapped; rulesForEvent gives MATCH both ways, but only for
		// this exists:false variant.
		{"or member order", `{"$or":[{"a":["x"]},{"a":[{"exists":false},"x"]}]}`, `{"b":1}`, matches},
		{"or member order swapped", `{"$or":[{"a":[{"exists":false},"x"]},{"a":["x"]}]}`, `{"b":1}`, matches},
		// Both entry points give NO for the first order and MATCH for the
		// swapped one: Ruler loses the values of a later member's array
		// when an earlier member has the same key and shares a value ("q").
		{"or members sharing a value", `{"$or":[{"x":["q"]},{"x":["q","y"]}]}`, `{"x":"y"}`, matches},
		{"or members sharing a value swapped", `{"$or":[{"x":["q","y"]},{"x":["q"]}]}`, `{"x":"y"}`, matches},
		{"or members sharing a middle value", `{"$or":[{"x":["q"]},{"x":["a","q","y"]}]}`, `{"x":"a"}`, matches},
		// Ruler: NO, although "xy" is not "q" in any case.
		{
			"anything-but wildcard with anything-but ignore-case",
			`{"a":[{"anything-but":{"wildcard":"*y"}},{"anything-but":{"equals-ignore-case":"Q"}}]}`, `{"a":"xy"}`, matches,
		},
		// Ruler: MATCH, comparing hex strings of different lengths.
		{"cidr /0 across families", `{"a":[{"cidr":"0.0.0.0/0"}]}`, `{"a":"::1"}`, noMatch},
		// Ruler throws ArrayIndexOutOfBoundsException adding the rule.
		{"numeric bound at the largest double", `{"a":[{"numeric":[">",1.7976931348623157e308]}]}`, `{"a":1}`, noMatch},
		// Ruler: NO and MATCH. Its compiler leaves out the separator after an
		// empty key (Path.extendedName) while its Event joins every step
		// (Path.name()), so the pattern reads the field "a" and no event
		// nested like the pattern can match it.
		{"pattern under an empty key matches the same nesting", `{"":{"a":["x"]}}`, `{"":{"a":"x"}}`, matches},
		{"pattern under an empty key misses the top level", `{"":{"a":["x"]}}`, `{"a":"x"}`, noMatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			checkVector(t, tc.pattern, tc.event, tc.want)
		})
	}
}

// checkVector compiles pattern for EventBridge and matches event against it.
func checkVector(t *testing.T, pattern, event string, want outcome) {
	t.Helper()
	p, cerr := Compile([]byte(pattern), EventBridge{})
	if want.reject != "" {
		if cerr == nil {
			t.Fatalf("Compile(%s) accepted the pattern; want rejection %q", pattern, want.reject)
		}
		if cerr.Detail != want.reject {
			t.Fatalf("Compile(%s) detail = %q, want %q", pattern, cerr.Detail, want.reject)
		}
		return
	}
	if cerr != nil {
		t.Fatalf("Compile(%s) rejected the pattern: %s", pattern, cerr.Detail)
	}
	got, err := Match(p, []byte(event))
	if want.eventError != "" {
		if err == nil || err.Error() != want.eventError {
			t.Fatalf("Match(%s) error = %v, want %q", event, err, want.eventError)
		}
		return
	}
	if err != nil {
		t.Fatalf("Match(%s) error: %v", event, err)
	}
	if got != want.match {
		t.Fatalf("Match(%s, %s) = %t, want %t", pattern, event, got, want.match)
	}
}
