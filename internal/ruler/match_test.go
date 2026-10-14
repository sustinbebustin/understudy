package ruler

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func mustCompile(t *testing.T, pattern string, p Profile) *Pattern {
	t.Helper()
	c, err := Compile([]byte(pattern), p)
	if err != nil {
		t.Fatalf("Compile(%s): %s", pattern, err.Detail)
	}
	return c
}

func matchEvent(t *testing.T, p *Pattern, event string) bool {
	t.Helper()
	ok, err := Match(p, []byte(event))
	if err != nil {
		t.Fatalf("Match(%s): %v", event, err)
	}
	return ok
}

func matchAttrs(t *testing.T, p *Pattern, attrs map[string]Attribute) bool {
	t.Helper()
	ok, err := MatchAttributes(p, attrs)
	if err != nil {
		t.Fatalf("MatchAttributes(%v): %v", attrs, err)
	}
	return ok
}

// TestSemantics covers behaviors from matching-engines.md 4 beyond the seed
// vectors.
func TestSemantics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, pattern, event string
		want                 bool
	}{
		{"exists false honors array consistency", `{"arr":{"a":["1"],"b":[{"exists":false}]}}`, `{"arr":[{"a":"1"},{"b":"x"}]}`, true},
		{"exists false sees a field in the same element", `{"arr":{"a":["1"],"b":[{"exists":false}]}}`, `{"arr":[{"a":"1","b":"x"}]}`, false},
		// Ruler checks an absence when its sorted traversal reaches the key,
		// against the elements the keys before it pinned: arr.a comes first
		// with nothing pinned, so the x in the other element breaks it.
		{"exists false before the key that pins the element", `{"arr":{"a":[{"exists":false}],"b":["1"]}}`, `{"arr":[{"a":"x"},{"b":"1"}]}`, false},
		{"exists false after the key that pins the element", `{"arr":{"b":[{"exists":false}],"a":["1"]}}`, `{"arr":[{"a":"1"},{"b":"x"}]}`, true},
		// Keys sort by UTF-16 code unit, as Java's String.compareTo: the
		// emoji's high surrogate (U+D83D) sorts before U+FF01, although its
		// UTF-8 bytes sort after.
		{"keys sort by UTF-16 code unit", "{\"arr\":{\"\U0001F600\":[{\"exists\":false}],\"！\":[\"1\"]}}", "{\"arr\":[{\"！\":\"1\"},{\"\U0001F600\":\"x\"}]}", false},
		{"array consistency tries later elements", `{"a":{"x":["1"],"y":["2"]}}`, `{"a":[{"x":"1"},{"x":"1","y":"2"}]}`, true},
		{"exists false tries later elements", `{"a":{"p":["1"],"q":[{"exists":false}]},"b":["z"]}`, `{"a":[{"p":"1","q":"x"},{"p":"1"}],"b":"z"}`, true},
		{"a field outside a pinned array stays a candidate", `{"a.y":[5],"a.y.k":[2]}`, `{"a":{"y":[[5],{"k":1}],"y":{"k":2}}}`, true},
		{"arrays of arrays flatten", `{"x":[3]}`, `{"x":[[1,2],[3]]}`, true},
		{"arrays of objects flatten", `{"Records":{"s3":{"object":{"key":["k"]}}}}`, `{"Records":[{"s3":{"object":{"key":"j"}}},{"s3":{"object":{"key":"k"}}}]}`, true},
		{"empty object is absent", `{"x":[{"exists":false}]}`, `{"x":{}}`, true},
		{"or branch overrides an earlier sibling", `{"s":["a"],"$or":[{"s":["b"]},{"t":["c"]}]}`, `{"s":"b"}`, true},
		{"or branch override is per branch", `{"s":["a"],"$or":[{"s":["b"]},{"t":["c"]}]}`, `{"s":"b","t":"c"}`, true},
		{"or nested in an object", `{"detail":{"$or":[{"a":["1"]},{"b":["2"]}]}}`, `{"detail":{"b":"2"}}`, true},
		{"or nested in an object does not leak", `{"detail":{"$or":[{"a":["1"]},{"b":["2"]}]}}`, `{"b":"2"}`, false},
		{"anything-but numbers compare by value", `{"x":[{"anything-but":[300]}]}`, `{"x":3.0e2}`, false},
		{"anything-but missing key", `{"x":[{"anything-but":"a"}]}`, `{"y":"b"}`, false},
		{"suffix ignore case", `{"f":[{"suffix":{"equals-ignore-case":".PNG"}}]}`, `{"f":"a.png"}`, true},
		{"prefix ignore case", `{"f":[{"prefix":{"equals-ignore-case":"EventB"}}]}`, `{"f":"eventbridge"}`, true},
		{"equals-ignore-case needs equal length", `{"f":[{"equals-ignore-case":"ab"}]}`, `{"f":"abc"}`, false},
		{"literal star in event", `{"f":[{"wildcard":"a*"}]}`, `{"f":"a*"}`, true},
		{"cidr ipv6", `{"ip":[{"cidr":"2001:db8::/32"}]}`, `{"ip":"2001:db8:1::5"}`, true},
		{"cidr misses a number whose text is outside its hex range", `{"ip":[{"cidr":"10.0.0.0/8"}]}`, `{"ip":10}`, false},
		{"numeric negative zero", `{"x":[{"numeric":["=",0]}]}`, `{"x":-0}`, true},
		{"numeric upper bound only", `{"x":[{"numeric":["<",0]}]}`, `{"x":-1e-300}`, true},
		{"keys are case-sensitive", `{"Key":["a"]}`, `{"key":"a"}`, false},
		{"escaped strings compare decoded", `{"x":["a\nbAc"]}`, `{"x":"a\u000AbAc"}`, true},
		{"surrogate pair escapes decode to one character", `{"x":["😀"]}`, "{\"x\":\"\U0001F600\"}", true},
		{"lone high surrogate before a literal character", `{"x":[{"suffix":"A"}]}`, `{"x":"\ud800A"}`, true},
		// \x5c is a backslash: the event is {"x":"\ud800" followed by the escape for A.
		{"lone high surrogate keeps the next escape", `{"x":[{"suffix":"A"}]}`, "{\"x\":\"\x5cud800\x5cu0041\"}", true},
		{"nested arrays pin each level separately", `{"b":{"b":{"a":["x"],"b":["x"]}}}`, `{"b":[{"b":[{"a":"x"},{"a":"x","b":"x"}]}]}`, true},
		{"inner array elements conflict under one outer element", `{"a":{"b":{"b":["y"],"a":["x"]}}}`, `{"a":[{"b":[{"a":"x"},{"b":"y"}]}]}`, false},
		{"duplicate event keys are all values", `{"x":["b"]}`, `{"x":"a","x":"b"}`, true},
		// Ruler's Event joins every step of a path with ".", so a member
		// under an empty key sits at ".a", not at "a".
		{"a member under an empty key is not at the top level", `{"a":["x"]}`, `{"":{"a":"x"}}`, false},
		{"a dotted key reaches a member under an empty key", `{".a":["x"]}`, `{"":{"a":"x"}}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := Match(mustCompile(t, tc.pattern, EventBridge{}), []byte(tc.event))
			if err != nil {
				t.Fatalf("Match: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Match(%s, %s) = %t, want %t", tc.pattern, tc.event, got, tc.want)
			}
		})
	}
}

// searchFirstBranch searches the first branch of p against event as Match
// does and returns the outcome and the work of every component searched,
// leaving out the indexing of the fields at its paths that a Match call does
// once for every branch.
func searchFirstBranch(t *testing.T, p *Pattern, event string) (matched bool, steps int) {
	t.Helper()
	fs, err := flattenEvent([]byte(event))
	if err != nil {
		t.Fatalf("flattenEvent: %v", err)
	}
	work := &budget{limit: maxSearchSteps}
	b := p.branches[0]
	ln := newLinks([][]*condition{b}, fs, work)
	indexed := work.used
	matched = branchSatisfied(b, ln, p.profile.options(), work)
	if work.exceeded() {
		t.Fatalf("search exceeded the %d-step budget", maxSearchSteps)
	}
	return matched, work.used - indexed
}

// TestSearchRetriesNoIndependentChoice: when a late condition fails, the
// search does not retry the choices made in arrays that condition cannot
// see, so the work grows linearly with the array sizes instead of as their
// product.
func TestSearchRetriesNoIndependentChoice(t *testing.T) {
	t.Parallel()
	const n = 200
	p := mustCompile(t, `{"a":{"x":["v"]},"b":{"x":["v"]},"c":{"x":["v"]},"d":{"x":["v"]},"e":{"x":["v"],"y":["w"]}}`, EventBridge{})
	elems := "[" + strings.Repeat(`{"x":"v"},`, n-1) + `{"x":"v"}]`
	event := `{"a":` + elems + `,"b":` + elems + `,"c":` + elems + `,"d":` + elems + `,"e":[{"x":"v"},{"y":"w"}]}`
	matched, steps := searchFirstBranch(t, p, event)
	if matched {
		t.Fatal("matched although e.x and e.y sit in different elements of e")
	}
	if limit := 2 * len(p.branches[0]) * (n + 1); steps > limit {
		t.Fatalf("search took %d steps, want at most %d", steps, limit)
	}
}

// TestSearchSplitsIndependentArrays: a branch whose sorted paths alternate
// between independent arrays (a.b, a.m.b, a.m.m.b, a.m.m.z, a.m.z, a.z)
// costs work linear in the number of fields, not one search state per
// combination of elements of those arrays. The arrays sit under dotted keys
// ("a", "a.m", ...) or under repeated keys.
func TestSearchSplitsIndependentArrays(t *testing.T) {
	t.Parallel()
	const levels, size = 6, 16
	pattern := `{"b":[1],"z":[1]}`
	for range levels - 1 {
		pattern = `{"b":[1],"z":[1],"m":` + pattern + `}`
	}
	p := mustCompile(t, `{"a":`+pattern+`,"zz":[1]}`, EventBridge{})
	elems := "[" + strings.Repeat(`{"b":1,"z":1},`, size-1) + `{"b":1,"z":1}]`
	dotted := make([]string, levels)
	for j := range levels {
		dotted[j] = `"a` + strings.Repeat(".m", j) + `":` + elems
	}
	// repeated nests the arrays below the first one under repeated "m" keys:
	// {"a":E,"a":{"m":E,"m":{"m":E,...}}}.
	repeated := `{"m":` + elems + `}`
	for range levels - 2 {
		repeated = `{"m":` + elems + `,"m":` + repeated + `}`
	}
	events := map[string]string{
		"dotted keys":   `{` + strings.Join(dotted, ",") + `}`,
		"repeated keys": `{"a":` + elems + `,"a":` + repeated + `}`,
	}
	for name, event := range events {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			matched, steps := searchFirstBranch(t, p, event)
			if matched {
				t.Fatal(`matched although the event has no "zz"`)
			}
			fieldCount := 2 * levels * size
			if limit := 4 * (fieldCount + len(p.branches[0])); steps > limit {
				t.Fatalf("search took %d steps, want at most %d", steps, limit)
			}
		})
	}
}

// joinedArrays builds the case search cannot split (see its doc): every
// element of the dotted-key arrays "a", "a.m", ... also holds "m":{"b":1},
// so the path a.m.b has fields in two arrays, which joins all of them into
// one component. The innermost "z":[2] never matches, so the search must
// exhaust the states, about 16 times more of them per level at 16 elements
// per array.
func joinedArrays(levels int) (pattern, event string) {
	const size = 16
	pattern = `{"b":[1],"z":[2]}`
	for range levels - 1 {
		pattern = `{"b":[1],"z":[1],"m":` + pattern + `}`
	}
	elems := "[" + strings.Repeat(`{"b":1,"z":1,"m":{"b":1}},`, size-1) + `{"b":1,"z":1,"m":{"b":1}}]`
	dotted := make([]string, levels)
	for j := range levels {
		dotted[j] = `"a` + strings.Repeat(".m", j) + `":` + elems
	}
	return `{"a":` + pattern + `}`, `{` + strings.Join(dotted, ",") + `}`
}

// TestSearchBudget: an event that joins many arrays into one component ends
// in a *SearchBudgetError once the shared budget is spent, instead of
// running for minutes, while a shallower one of the same shape is decided.
func TestSearchBudget(t *testing.T) {
	t.Parallel()
	pattern, event := joinedArrays(3)
	if matched, err := Match(mustCompile(t, pattern, LambdaESM{}), []byte(event)); matched || err != nil {
		t.Fatalf(`3 levels: Match = %t, %v; want no match, since no element has "z":2`, matched, err)
	}

	// 6 levels (a 133-byte pattern, a 2.5 KB event) took 20 s before the
	// budget existed.
	pattern, event = joinedArrays(6)
	p := mustCompile(t, pattern, LambdaESM{})
	_, err := Match(p, []byte(event))
	if be, ok := errors.AsType[*SearchBudgetError](err); !ok || be.Steps != maxSearchSteps {
		t.Fatalf("6 levels: Match = %v, want *SearchBudgetError{Steps: %d}", err, maxSearchSteps)
	}

	// The budget is shared: a search stops at whatever limit it is given.
	fs, flatErr := flattenEvent([]byte(event))
	if flatErr != nil {
		t.Fatalf("flattenEvent: %v", flatErr)
	}
	const limit = 1 << 10
	_, err = p.matches(fs, limit)
	if be, ok := errors.AsType[*SearchBudgetError](err); !ok || be.Steps != limit {
		t.Fatalf("matches with a %d-step budget = %v, want *SearchBudgetError{Steps: %d}", limit, err, limit)
	}
}

// TestSearchBudgetCoversChosenFields: the work of a search step grows with
// the fields chosen so far (the failure memo's key, the consistency check of
// each field examined, the pin scan), and the budget counts it. joinedArrays
// with extra exact-match keys at every level, a pattern under the 4096-byte
// Lambda and EventBridge limit, once ran for 9 s and allocated 3.6 GB before
// the budget stopped it.
func TestSearchBudgetCoversChosenFields(t *testing.T) {
	for _, tc := range []struct{ levels, extra int }{{4, 100}, {6, 40}, {6, 10}} {
		pattern, event := joinedArrays(tc.levels)
		var keys, members strings.Builder
		for k := range tc.extra {
			fmt.Fprintf(&keys, `"%d":[1],`, k)
			fmt.Fprintf(&members, `"%d":1,`, k)
		}
		pattern = strings.ReplaceAll(pattern, `{"b":`, `{`+keys.String()+`"b":`)
		event = strings.ReplaceAll(event, `{"b":1,"z"`, `{`+members.String()+`"b":1,"z"`)
		if len(pattern) > 4096 {
			t.Fatalf("%d levels, %d keys: the pattern has %d bytes, over the 4096-byte limit", tc.levels, tc.extra, len(pattern))
		}
		p := mustCompile(t, pattern, LambdaESM{})
		var err error
		bytes := allocated(func() { _, err = Match(p, []byte(event)) })
		if err != nil {
			if be, ok := errors.AsType[*SearchBudgetError](err); !ok || be.Steps != maxSearchSteps {
				t.Fatalf("%d levels, %d keys: Match = %v, want a decision or *SearchBudgetError{Steps: %d}", tc.levels, tc.extra, err, maxSearchSteps)
			}
		}
		if limit := uint64(64 << 20); bytes > limit {
			t.Fatalf("%d levels, %d keys: Match allocated %d bytes, want at most %d", tc.levels, tc.extra, bytes, limit)
		}
	}
}

// TestSearchBudgetCoversBranchSetup: splitting each branch into components
// counts toward the budget and costs work in proportion to the branch, not
// to the arrays at its paths. 16384 Lambda branches (a 579-byte pattern)
// against a 234 KB event with a thousand arrays under each key they name
// once took 18 s and gigabytes of allocations that the budget never saw.
// The event has no "k0", so every branch fails at once and the event is
// decided.
func TestSearchBudgetCoversBranchSetup(t *testing.T) {
	sizes := make([]int, 14)
	for i := range sizes {
		sizes[i] = 2
	}
	p := mustCompile(t, orArrays(sizes...), LambdaESM{})
	const n = 1000
	// nested holds each key's thousand arrays in one outer array; repeated
	// gives each its own outermost array under a repeated key.
	nested, repeated := make([]string, 0, len(sizes)), make([]string, 0, len(sizes)*n)
	for i := 1; i < len(sizes); i++ {
		key := `"k` + strconv.Itoa(i) + `":`
		nested = append(nested, key+"["+strings.Repeat(`[{"a0":1,"a1":1}],`, n-1)+`[{"a0":1,"a1":1}]]`)
		for range n {
			repeated = append(repeated, key+`[{"a0":1,"a1":1}]`)
		}
	}
	events := map[string]string{
		"nested arrays": "{" + strings.Join(nested, ",") + "}",
		"repeated keys": "{" + strings.Join(repeated, ",") + "}",
	}
	for name, event := range events {
		var matched bool
		var err error
		bytes := allocated(func() { matched, err = Match(p, []byte(event)) })
		if matched || err != nil {
			t.Fatalf(`%s: Match = %t, %v; want no match, since the event has no "k0"`, name, matched, err)
		}
		if limit := uint64(64 << 20); bytes > limit {
			t.Fatalf("%s: Match allocated %d bytes, want at most %d", name, bytes, limit)
		}
	}
}

// TestSearchLooksUpPinnedElements: once a condition pins an element of an
// array, a later condition under the same array examines that element only,
// so a long array costs linear work instead of quadratic.
func TestSearchLooksUpPinnedElements(t *testing.T) {
	t.Parallel()
	const n = 20000
	p := mustCompile(t, `{"a":{"x":["v"],"y":["w"]}}`, EventBridge{})
	event := `{"a":[` + strings.Repeat(`{"x":"v","y":"u"},`, n-1) + `{"x":"v","y":"u"}]}`
	matched, steps := searchFirstBranch(t, p, event)
	if matched {
		t.Fatal(`matched although no element has "y":"w"`)
	}
	if limit := 4 * (n + 1); steps > limit {
		t.Fatalf("search took %d steps, want at most %d", steps, limit)
	}
}

// TestSearchDecidesAbsencesAtOnce: an {"exists": false} alternative is
// decided when the search reaches its condition, so a pattern of many
// optional keys followed by a failing one costs work linear in the number of
// keys, whether the keys sit in separate arrays or share one.
func TestSearchDecidesAbsencesAtOnce(t *testing.T) {
	t.Parallel()
	const k = 40
	const linear = 8 * (k + 2)
	tests := []struct {
		name string
		// key returns the n-th optional key of the pattern and of the event.
		key func(n int) (pattern, event string)
		// wrap places the members inside the top-level object.
		wrap  func(pattern, event string) (string, string)
		limit int
	}{
		{
			"scalar keys",
			func(n int) (string, string) {
				name := fmt.Sprintf(`"a%02d"`, n)
				return name + `:["v",{"exists":false}]`, name + `:"v"`
			},
			func(p, e string) (string, string) { return `{` + p + `,"zz":["nope"]}`, `{` + e + `}` },
			linear,
		},
		{
			"array-wrapped keys",
			func(n int) (string, string) {
				name := fmt.Sprintf(`"a%02d"`, n)
				return name + `:{"q":["v",{"exists":false}]}`, name + `:[{"q":"v"}]`
			},
			func(p, e string) (string, string) { return `{` + p + `,"zz":["nope"]}`, `{` + e + `}` },
			linear,
		},
		{
			"keys in one shared array",
			func(n int) (string, string) {
				name := fmt.Sprintf(`"k%02d"`, n)
				return name + `:["v",{"exists":false}]`, name + `:"v"`
			},
			func(p, e string) (string, string) {
				return `{"a":{` + p + `,"zz":["nope"]}}`, `{"a":[{` + e + `}]}`
			},
			linear,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ps, es := make([]string, k), make([]string, k)
			for n := range k {
				ps[n], es[n] = tc.key(n)
			}
			pattern, event := tc.wrap(strings.Join(ps, ","), strings.Join(es, ","))
			p := mustCompile(t, pattern, EventBridge{})
			matched, steps := searchFirstBranch(t, p, event)
			if matched {
				t.Fatal(`matched although the event has no "zz":"nope"`)
			}
			if steps > tc.limit {
				t.Fatalf("search took %d steps, want at most %d", steps, tc.limit)
			}
		})
	}
}

func TestEventErrors(t *testing.T) {
	t.Parallel()
	p := mustCompile(t, `{"x":["a"]}`, EventBridge{})
	const notObject = "Event must be a JSON object"
	tests := []struct{ event, detail string }{
		{`"x"`, notObject},
		{`[1]`, notObject},
		{``, notObject},
		{`{"x":`, "Unexpected end-of-input within/between Object entries"},
		{`{"x":tru}`, "Unrecognized token 'tru': was expecting (JSON String, Number, Array, Object or token 'null', 'true' or 'false')"},
		// Ruler parses events with Jackson's defaults, which leave the
		// source out of a location; it parses patterns with it.
		{`{"x":"a"`, "Unexpected end-of-input: expected close marker for Object (start marker at " +
			"[Source: REDACTED (`StreamReadFeature.INCLUDE_SOURCE_IN_LOCATION` disabled); line: 1, column: 1])"},
		{`{"x":"a"} trailing`, "Event must be a single JSON object; found content after it"},
		{`{"x":"a"}{}`, "Event must be a single JSON object; found content after it"},
	}
	for _, tc := range tests {
		_, err := Match(p, []byte(tc.event))
		ee, ok := errors.AsType[*EventError](err)
		if !ok || ee.Detail != tc.detail {
			t.Errorf("Match(%q) = %v, want *EventError %q", tc.event, err, tc.detail)
		}
	}
}

// TestLambdaBodyFormats derives the SQS body format table of
// lambda-events.md 2.4 from Ruler semantics: a JSON body is nested under
// "body" and a plain-string body is the string value of "body".
func TestLambdaBodyFormats(t *testing.T) {
	t.Parallel()
	plainPattern := mustCompile(t, `{"body":["plain"]}`, LambdaESM{})
	jsonPattern := mustCompile(t, `{"body":{"k":["v"]}}`, LambdaESM{})
	tests := []struct {
		name    string
		pattern *Pattern
		event   string
		want    bool
	}{
		{"plain body, plain pattern", plainPattern, `{"body":"plain"}`, true},
		{"plain body, JSON pattern", jsonPattern, `{"body":"plain"}`, false},
		{"JSON body, plain pattern", plainPattern, `{"body":{"k":"v"}}`, false},
		{"JSON body, JSON pattern", jsonPattern, `{"body":{"k":"v"}}`, true},
	}
	for _, tc := range tests {
		got, err := Match(tc.pattern, []byte(tc.event))
		if err != nil || got != tc.want {
			t.Errorf("%s: Match = %t, %v; want %t", tc.name, got, err, tc.want)
		}
	}
}

// TestMatchAttributes covers the SNS MessageAttributes scope (sns.md 6.3).
func TestMatchAttributes(t *testing.T) {
	t.Parallel()
	attrs := SNS{Scope: MessageAttributes}
	tests := []struct {
		name   string
		policy string
		attrs  map[string]Attribute
		want   bool
	}{
		{"string array any element", `{"store":["value1"]}`, map[string]Attribute{"store": StringArrayAttribute{JSON: `["value1","value2"]`}}, true},
		{"string array no element", `{"store":["value1"]}`, map[string]Attribute{"store": StringArrayAttribute{JSON: `["value2","value3"]`}}, false},
		{"number attribute numeric range", `{"price":[{"numeric":[">",0,"<=",100]}]}`, map[string]Attribute{"price": NumberAttribute{Value: "99"}}, true},
		{"number attribute exponent", `{"price":[{"numeric":["=",301.5]}]}`, map[string]Attribute{"price": NumberAttribute{Value: "3.015e2"}}, true},
		{"string attribute is not numeric", `{"price":[{"numeric":[">",0]}]}`, map[string]Attribute{"price": StringAttribute{Value: "99"}}, false},
		{"exists on a number attribute", `{"store":[{"exists":true}]}`, map[string]Attribute{"store": NumberAttribute{Value: "1"}}, true},
		// A Number attribute is a JSON number, not its StringValue text
		// (switch sns.filter-policy.number-attribute-type).
		{"bare number on a number attribute", `{"p":[100]}`, map[string]Attribute{"p": NumberAttribute{Value: "100"}}, true},
		{"string rule on a number attribute", `{"p":["100"]}`, map[string]Attribute{"p": NumberAttribute{Value: "100"}}, false},
		{"prefix on a number attribute", `{"p":[{"prefix":"1"}]}`, map[string]Attribute{"p": NumberAttribute{Value: "100"}}, false},
		{"anything-but string on a number attribute", `{"p":[{"anything-but":"100"}]}`, map[string]Attribute{"p": NumberAttribute{Value: "100"}}, true},
		{"missing key with null and anything-but", `{"attr1":[null,{"anything-but":"whatever"}]}`, map[string]Attribute{}, false},
		{"extra attributes are ignored", `{"a":["x"]}`, map[string]Attribute{"a": StringAttribute{Value: "x"}, "b": StringAttribute{Value: "y"}}, true},
		{"or across attributes", `{"source":["aws.cloudwatch"],"$or":[{"metricName":["CPUUtilization"]},{"namespace":["AWS/EC2"]}]}`, map[string]Attribute{"source": StringAttribute{Value: "aws.cloudwatch"}, "namespace": StringAttribute{Value: "AWS/EC2"}}, true},
		{"anything-but over a string array", `{"x":[{"anything-but":["rugby","tennis"]}]}`, map[string]Attribute{"x": StringArrayAttribute{JSON: `["rugby","baseball"]`}}, true},
		{"string arrays of different attributes are different arrays", `{"x":["b"],"y":["c"]}`, map[string]Attribute{"x": StringArrayAttribute{JSON: `[["a"],["b"]]`}, "y": StringArrayAttribute{JSON: `[["c"]]`}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := MatchAttributes(mustCompile(t, tc.policy, attrs), tc.attrs)
			if err != nil {
				t.Fatalf("MatchAttributes: %v", err)
			}
			if got != tc.want {
				t.Fatalf("MatchAttributes = %t, want %t", got, tc.want)
			}
		})
	}
	notArray := `String.Array attribute "a" is not a JSON array`
	for value, detail := range map[string]string{
		`"x"`:            notArray,
		`["x"] trailing`: notArray,
		`[][]`:           notArray,
		`["x"],`:         notArray,
		`["x"`:           `String.Array attribute "a" is not valid JSON: Unexpected end-of-input: expected close marker for Array (start marker at [Source: (String)"["x""; line: 1, column: 1])`,
	} {
		_, err := MatchAttributes(mustCompile(t, `{"a":["x"]}`, attrs), map[string]Attribute{"a": StringArrayAttribute{JSON: value}})
		if ee, ok := errors.AsType[*EventError](err); !ok || ee.Detail != detail {
			t.Errorf("String.Array %s = %v, want *EventError %q", value, err, detail)
		}
	}
}

// TestInputMismatch: a pattern matches only the input its profile compiles
// for, JSON events or SNS message attributes, and says which call fits.
func TestInputMismatch(t *testing.T) {
	t.Parallel()
	attrs := mustCompile(t, `{"a":["x"]}`, SNS{Scope: MessageAttributes})
	_, err := Match(attrs, []byte(`{"a":"x"}`))
	if ie, ok := errors.AsType[*InputError](err); !ok || ie.Want != InputAttributes {
		t.Errorf("Match with an attribute-scope pattern = %v, want an InputError for attributes", err)
	}
	for _, p := range []Profile{EventBridge{}, LambdaESM{}, SNS{Scope: MessageBody}} {
		event := mustCompile(t, `{"a":["x"]}`, p)
		_, err := MatchAttributes(event, map[string]Attribute{"a": StringAttribute{Value: "x"}})
		if ie, ok := errors.AsType[*InputError](err); !ok || ie.Want != InputEvent {
			t.Errorf("%T: MatchAttributes with an event pattern = %v, want an InputError for events", p, err)
		}
	}
}

// TestNestingLimit: patterns and events may nest at most 1000 objects and
// arrays, Jackson's default limit, which Ruler keeps.
func TestNestingLimit(t *testing.T) {
	t.Parallel()
	// nested returns objects nested objects around leaf.
	nested := func(objects int, leaf string) string {
		return strings.Repeat(`{"a":`, objects) + leaf + strings.Repeat("}", objects)
	}
	const want = "Document nesting depth (1001) exceeds the maximum allowed (1000, " +
		"from `StreamReadConstraints.getMaxNestingDepth()`)"
	if _, err := Compile([]byte(nested(999, `["x"]`)), EventBridge{}); err != nil {
		t.Errorf("a pattern 1000 deep was rejected: %s", err.Detail)
	}
	if _, err := Compile([]byte(nested(1000, `["x"]`)), EventBridge{}); err == nil || err.Detail != want {
		t.Errorf("a pattern 1001 deep = %v, want %q", err, want)
	}
	p := mustCompile(t, `{"a":["x"]}`, EventBridge{})
	if _, err := Match(p, []byte(nested(1000, `"x"`))); err != nil {
		t.Errorf("an event 1000 deep was rejected: %v", err)
	}
	_, err := Match(p, []byte(`{"a":`+strings.Repeat("[", 1000)+strings.Repeat("]", 1000)+`}`))
	if ee, ok := errors.AsType[*EventError](err); !ok || ee.Detail != want {
		t.Errorf("an event 1001 deep = %v, want %q", err, want)
	}
}

// TestDeepEventIsBounded: a 256 KB event of arrays nested to the depth limit,
// with tens of thousands of fields at the bottom, is flattened and matched
// with memory proportional to its size: every field shares its elements'
// array memberships instead of copying them.
func TestDeepEventIsBounded(t *testing.T) {
	const depth = 998
	var b strings.Builder
	b.WriteString(`{"a":` + strings.Repeat("[", depth))
	for b.Len() < 256*1024-depth-16 {
		b.WriteString(`[1],`)
	}
	b.WriteString(`[2]` + strings.Repeat("]", depth) + `,"b":"x"}`)
	event := []byte(b.String())
	p := mustCompile(t, `{"a":[2],"b":["y"]}`, EventBridge{})
	var got bool
	var err error
	bytes := allocated(func() { got, err = Match(p, event) })
	if err != nil || got {
		t.Fatalf("Match = %t, %v; want false, nil", got, err)
	}
	if limit := uint64(96 * len(event)); bytes > limit {
		t.Fatalf("Match of %d bytes allocated %d bytes, want at most %d", len(event), bytes, limit)
	}
}

// TestIgnoreCase follows Ruler's EqualsIgnoreCaseParser: each UTF-16 code
// unit of the operand accepts exactly its Java lower- and upper-case forms
// under Locale.ROOT, which can change length, and a character above U+FFFF
// accepts only "??", the bytes Java's encoder writes for its two surrogates.
func TestIgnoreCase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, rule, value string
		want              bool
	}{
		{"sharp s matches its upper case SS", `{"equals-ignore-case":"ß"}`, "SS", true},
		{"sharp s matches itself", `{"equals-ignore-case":"ß"}`, "ß", true},
		{"sharp s does not match ss", `{"equals-ignore-case":"ß"}`, "ss", false},
		{"SS does not match sharp s", `{"equals-ignore-case":"SS"}`, "ß", false},
		{"titlecase dz matches its lower case", `{"equals-ignore-case":"ǅ"}`, "ǆ", true},
		{"titlecase dz matches its upper case", `{"equals-ignore-case":"ǅ"}`, "Ǆ", true},
		{"titlecase dz does not match itself", `{"equals-ignore-case":"ǅ"}`, "ǅ", false},
		{"dotted capital I matches i and a combining dot", `{"equals-ignore-case":"İ"}`, "i̇", true},
		{"dotted capital I does not match plain i", `{"equals-ignore-case":"İ"}`, "i", false},
		{"ligature matches FI", `{"equals-ignore-case":"ﬁle"}`, "FILE", true},
		{"prefix with a length-changing mapping", `{"prefix":{"equals-ignore-case":"straß"}}`, "STRASSE", true},
		{"suffix with a length-changing mapping", `{"suffix":{"equals-ignore-case":"ße"}}`, "STRASSE", true},
		{"suffix needs the whole mapping", `{"suffix":{"equals-ignore-case":"ß"}}`, "STRAS", false},
		{"anything-but uses the same folding", `{"anything-but":{"equals-ignore-case":["ß"]}}`, "SS", false},
		{"emoji matches only two question marks", `{"equals-ignore-case":"😀"}`, "??", true},
		{"emoji does not match itself", `{"equals-ignore-case":"😀"}`, "😀", false},
		{"ascii folds both ways", `{"equals-ignore-case":"AbC"}`, "aBc", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := mustCompile(t, `{"x":[`+tc.rule+`]}`, EventBridge{})
			event, err := json.Marshal(map[string]string{"x": tc.value})
			if err != nil {
				t.Fatalf("encoding %q: %v", tc.value, err)
			}
			if got := matchEvent(t, p, string(event)); got != tc.want {
				t.Fatalf("%s against %q = %t, want %t", tc.rule, tc.value, got, tc.want)
			}
		})
	}
}

// TestSNSDocumentedEmptyAttributes: with the SNS documentation's options,
// exists:false needs at least one attribute and exists:true needs a
// non-empty value. "An empty set of attributes" counts attributes, or the
// members of a message body, not the values they hold.
func TestSNSDocumentedEmptyAttributes(t *testing.T) {
	t.Parallel()
	options := Options{ExistsTrue: ExistsTrueNonEmpty, EmptyInput: EmptyInputNeverMatches}
	documented := SNS{Options: options}
	absent := mustCompile(t, `{"store":[{"exists":false}]}`, documented)
	for name, attrs := range map[string]map[string]Attribute{
		"empty set":          {},
		"another attribute":  {"other": StringAttribute{Value: "x"}},
		"empty String.Array": {"other": StringArrayAttribute{JSON: `[]`}},
		"Binary attribute":   {"other": BinaryAttribute{}},
	} {
		if got, want := matchAttrs(t, absent, attrs), name != "empty set"; got != want {
			t.Errorf("exists:false on %s = %t, want %t", name, got, want)
		}
	}
	body := mustCompile(t, `{"store":[{"exists":false}]}`, SNS{Scope: MessageBody, Options: options})
	for event, want := range map[string]bool{`{}`: false, `{"other":"x"}`: true, `{"y":[]}`: true, `{"y":{}}`: true} {
		if got := matchEvent(t, body, event); got != want {
			t.Errorf("exists:false on body %s = %t, want %t", event, got, want)
		}
	}
	present := mustCompile(t, `{"store":[{"exists":true}]}`, documented)
	if matchAttrs(t, present, map[string]Attribute{"store": StringAttribute{Value: ""}}) {
		t.Error("exists:true matched an empty string attribute")
	}
}
