package ruler

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func values(prefix string, n int) string {
	vs := make([]string, n)
	for i := range vs {
		vs[i] = strconv.Quote(prefix + strconv.Itoa(i))
	}
	return "[" + strings.Join(vs, ",") + "]"
}

// TestUsesWildcard finds a wildcard wherever a pattern holds one: at the
// top, nested, inside anything-but or in one $or branch; an object inside
// $or is a sub-pattern, never an operator.
func TestUsesWildcard(t *testing.T) {
	t.Parallel()
	for pattern, want := range map[string]bool{
		`{"source":["a"]}`: false,
		`{"source":[{"prefix":"a"}],"detail":{"n":[{"numeric":[">",1]}]}}`: false,
		`{"source":[{"wildcard":"a*"}]}`:                                   true,
		`{"detail":{"key":[{"wildcard":"dir/*.png"}]}}`:                    true,
		`{"source":[{"anything-but":{"wildcard":"*.tmp"}}]}`:               true,
		`{"$or":[{"a":["x"]},{"b":[{"wildcard":"y*"}]}]}`:                  true,
		`{"$or":[{"a":["x"]},{"b":[{"prefix":"y"}]}]}`:                     false,
	} {
		p, err := Compile([]byte(pattern), EventBridge{})
		if err != nil {
			t.Fatalf("Compile(%s) = %v", pattern, err)
		}
		if got := p.UsesWildcard(); got != want {
			t.Errorf("UsesWildcard(%s) = %t, want %t", pattern, got, want)
		}
	}
}

// TestSNSCombinations replays the documented examples and the AWS-recorded
// boundaries of sns.md 6.5 (test_policy_complexity, _with_or and
// test_sub_filter_policy_nested_property_constraints).
func TestSNSCombinations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		policy string
		scope  SNSScope
		ok     bool
	}{
		{"doc flat 3x1x2=6", `{"key_a":["1","2","3"],"key_b":["1"],"key_c":["1","2"]}`, MessageAttributes, true},
		{"doc nested 4x3x3x2=72", `{"key_a":{"key_b":{"key_c":["1","2","3","4"]}},"key_d":{"key_e":["1","2","3"]}}`, MessageBody, true},
		{"doc $or 2+2+3=7", `{"source":["aws.cloudwatch"],"$or":[{"metricName":["CPUUtilization","ReadLatency"]},{"metricType":["MetricType"],"$or":[{"metricId":[1234,4321]},{"spaceId":[1000,2000,3000]}]}]}`, MessageAttributes, true},
		{"doc nested $or 4x8=32", `{"$or":[{"metricName":["a","b"]},{"namespace":["c","d"]}],"detail":{"scope":["Service"],"$or":[{"source":["aws.cloudwatch"]},{"type":["x"]}]}}`, MessageBody, true},
		{"recorded 151 values", `{"key":` + values("v", 151) + `}`, MessageAttributes, false},
		{"recorded 150 values", `{"key":` + values("v", 150) + `}`, MessageAttributes, true},
		{"recorded 100x51", `{"key1":` + values("v", 100) + `,"key2":` + values("v", 51) + `}`, MessageAttributes, false},
		{"recorded nested 216", `{"key_a":{"key_b":{"key_c":["1","2","3","4"]}},"key_d":{"key_e":["1","2","3"]},"key_f":["1","2","3"]}`, MessageBody, false},
		{"recorded flat $or 152", `{"source":` + values("s", 4) + `,"$or":[{"metricName":` + values("m", 6) + `},{"metricType":` + values("t", 4) + `,"$or":[{"metricId":[1234,4321,5678,9012]},{"spaceId":[1,2,3,4]}]}]}`, MessageAttributes, false},
		{"recorded flat $or 150", `{"source":` + values("s", 3) + `,"$or":[{"metricName":` + values("m", 6) + `},{"metricType":` + values("t", 4) + `,"$or":[{"metricId":[1234,4321,5678,9012]},{"spaceId":[1,2,3,4,5,6,7]}]}]}`, MessageAttributes, true},
		{"recorded nested $or 160", `{"$or":[{"metricName":` + values("m", 3) + `},{"namespace":` + values("n", 2) + `}],"detail":{"scope":` + values("s", 2) + `,"$or":[{"source":["aws.cloudwatch"]},{"type":` + values("t", 3) + `}]}}`, MessageBody, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Compile([]byte(tc.policy), SNS{Scope: tc.scope})
			if tc.ok {
				if err != nil {
					t.Fatalf("Compile rejected the policy: %s", err.Detail)
				}
				return
			}
			if err == nil || err.Cause != (LimitExceeded{Limit: LimitCombinations}) || err.Detail != "Filter policy is too complex" {
				t.Fatalf("Compile = %#v, want the combinations limit", err)
			}
		})
	}
}

// TestSNSKeys: at most five keys; for a nested policy the leaf keys count
// (sns.md 6.5), and each "$or" branch counts its own keys.
func TestSNSKeys(t *testing.T) {
	t.Parallel()
	tests := []struct {
		policy string
		ok     bool
	}{
		{`{"a":["1"],"b":["1"],"c":["1"],"d":["1"],"e":["1"]}`, true},
		{`{"a":{"b":["1"],"c":["1"],"d":["1"]},"e":{"f":["1"],"g":["1"],"h":["1"]}}`, false},
		{`{"a":{"b":["1"],"c":["1"],"d":["1"],"e":["1"],"f":["1"]}}`, true},
		{`{"$or":[{"a":["1"],"b":["1"],"c":["1"]},{"d":["1"],"e":["1"],"f":["1"]}]}`, true},
		{`{"a":["1"],"$or":[{"b":["1"],"c":["1"]},{"d":["1"],"e":["1"],"f":["1"],"g":["1"],"h":["1"]}]}`, false},
	}
	for _, tc := range tests {
		_, err := Compile([]byte(tc.policy), SNS{Scope: MessageBody})
		if tc.ok != (err == nil) {
			t.Errorf("Compile(%s) = %v, want ok=%t", tc.policy, err, tc.ok)
		}
		if err != nil && err.Cause != (LimitExceeded{Limit: LimitKeys}) {
			t.Errorf("Compile(%s) cause = %#v, want the key limit", tc.policy, err.Cause)
		}
	}
}

// TestSNSWildcardLimits: at most 3 wildcards per pattern and 100 points in
// total, a field scoring (sum of pattern points) x (number of patterns).
func TestSNSWildcardLimits(t *testing.T) {
	t.Parallel()
	field := func(n int) string {
		ps := make([]string, n)
		for i := range ps {
			ps[i] = `{"wildcard":"a` + strconv.Itoa(i) + `*"}`
		}
		return "[" + strings.Join(ps, ",") + "]"
	}
	tests := []struct {
		name   string
		policy string
		limit  Limit
	}{
		{"doc example scores 4", `{"filename":[{"wildcard":"*.txt"},{"wildcard":"log*"}]}`, 0},
		{"anything-but and wildcard score 4", `{"greeting":[{"anything-but":{"prefix":"Hello"}},{"wildcard":"H*"}]}`, 0},
		{"10 patterns score 100", `{"f":` + field(10) + `}`, 0},
		{"11 patterns score 121", `{"f":` + field(11) + `}`, LimitWildcardComplexity},
		{"3 wildcards in a pattern", `{"f":[{"wildcard":"a*b*c*d"}]}`, 0},
		{"4 wildcards in a pattern", `{"f":[{"wildcard":"a*b*c*d*e"}]}`, LimitWildcardsPerPattern},
		{"anything-but wildcard counts", `{"f":[{"anything-but":{"wildcard":["*a*b*c","*d*e*f"]}},{"wildcard":"*g*h*i"},{"wildcard":"*j*k*l"}]}`, LimitWildcardComplexity},
		{"no wildcard scores nothing", `{"f":[{"anything-but":` + values("x", 140) + `}]}`, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Compile([]byte(tc.policy), SNS{Scope: MessageBody})
			if tc.limit == 0 {
				if err != nil {
					t.Fatalf("Compile rejected the policy: %s", err.Detail)
				}
				return
			}
			if err == nil || err.Cause != (LimitExceeded{Limit: tc.limit}) {
				t.Fatalf("Compile = %#v, want limit %s", err, tc.limit)
			}
		})
	}
}

// TestOverriddenLeavesTakeNoPartInLimits: a later duplicate key replaces the
// earlier rule array (last one wins), so the replaced array is not in the rule
// and the wildcard limits do not see it; the array that replaces it is seen.
func TestOverriddenLeavesTakeNoPartInLimits(t *testing.T) {
	t.Parallel()
	tooComplex := `[{"wildcard":"*:*:*:*:*:event-bus/*"}]`
	tooManyStars := `[{"wildcard":"a*b*c*d*e"}]`
	tests := []struct {
		name    string
		policy  string
		profile Profile
		limit   Limit
	}{
		{"EventBridge replaced complex wildcard", `{"x":` + tooComplex + `,"x":["a"]}`, EventBridge{}, 0},
		{"EventBridge replacing complex wildcard", `{"x":["a"],"x":` + tooComplex + `}`, EventBridge{}, LimitWildcardComplexity},
		{"EventBridge complex wildcard kept by one $or branch", `{"x":` + tooComplex + `,"$or":[{"x":["a"]},{"y":["b"]}]}`, EventBridge{}, LimitWildcardComplexity},
		{"SNS replaced wildcard over 3 stars", `{"x":` + tooManyStars + `,"x":["a"]}`, SNS{Scope: MessageBody}, 0},
		{"SNS replacing wildcard over 3 stars", `{"x":["a"],"x":` + tooManyStars + `}`, SNS{Scope: MessageBody}, LimitWildcardsPerPattern},
		{"SNS replaced wildcard points", `{"x":[{"wildcard":"a*b*"},{"wildcard":"c*d*"},{"wildcard":"e*f*"},{"wildcard":"g*h*"},{"wildcard":"i*j*"}],"x":["a"]}`, SNS{Scope: MessageBody}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Compile([]byte(tc.policy), tc.profile)
			if tc.limit == 0 {
				if err != nil {
					t.Fatalf("Compile rejected the pattern: %s", err.Detail)
				}
				return
			}
			if err == nil || err.Cause != (LimitExceeded{Limit: tc.limit}) {
				t.Fatalf("Compile = %#v, want limit %s", err, tc.limit)
			}
		})
	}
}

func TestSNSPolicySize(t *testing.T) {
	t.Parallel()
	policy := `{"k":["` + strings.Repeat("v", snsMaxBytes) + `"]}`
	_, err := Compile([]byte(policy), SNS{Scope: MessageBody})
	if err == nil || err.Cause != (LimitExceeded{Limit: LimitSize}) {
		t.Fatalf("Compile of a %d-byte policy = %#v, want the size limit", len(policy), err)
	}
}

// orArrays builds sibling "$or" arrays of the given sizes, each in its own
// nested object with distinct keys, as in matching-engines.md H12.
func orArrays(sizes ...int) string {
	parts := make([]string, len(sizes))
	for i, n := range sizes {
		members := make([]string, n)
		for j := range members {
			members[j] = `{"a` + strconv.Itoa(j) + `":["1"]}`
		}
		parts[i] = `"k` + strconv.Itoa(i) + `":{"$or":[` + strings.Join(members, ",") + `]}`
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// TestEventBridgeOrCombinations: the product of the "$or" array sizes may
// not exceed 1000 (EventBridge operators page); Ruler has no cap.
func TestEventBridgeOrCombinations(t *testing.T) {
	t.Parallel()
	if _, err := Compile([]byte(orArrays(10, 10, 10)), EventBridge{}); err != nil {
		t.Fatalf("1000 combinations rejected: %s", err.Detail)
	}
	_, err := Compile([]byte(orArrays(7, 11, 13)), EventBridge{})
	if err == nil || err.Cause != (LimitExceeded{Limit: LimitCombinations}) {
		t.Fatalf("1001 combinations = %#v, want the combinations limit", err)
	}
	if _, err := Compile([]byte(orArrays(3, 3, 3, 3, 3, 3, 3)), LambdaESM{}); err != nil {
		t.Fatalf("Lambda rejected 2187 combinations, which Ruler accepts: %s", err.Detail)
	}
}

// allocated returns the bytes f allocates. Tests that use it do not run in
// parallel, so the count is f's alone.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// TestExpansionBudget: Lambda has no combination limit, so the engine stops
// expanding "$or" after maxExpansion steps, and the work and memory stay
// bounded; a pattern well inside the budget still compiles.
func TestExpansionBudget(t *testing.T) {
	sizes := make([]int, 20)
	for i := range sizes {
		sizes[i] = 2
	}
	pattern := []byte(orArrays(sizes...))
	var err *CompileError
	bytes := allocated(func() { _, err = Compile(pattern, LambdaESM{}) })
	if err == nil || err.Cause != (LimitExceeded{Limit: LimitCombinations}) || err.Detail != expansionDetail() {
		t.Fatalf("2^20 branches = %#v, want the expansion budget", err)
	}
	if limit := uint64(64 << 20); bytes > limit {
		t.Fatalf("Compile allocated %d bytes, want at most %d", bytes, limit)
	}
	if _, err := Compile([]byte(orArrays(2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2)), LambdaESM{}); err != nil {
		t.Fatalf("16384 branches rejected: %s", err.Detail)
	}
}

// TestExpansionStopsAfterOverrun: once a pattern overruns a limit, later
// "$or" arrays no longer multiply the branches, in every profile. Without
// that stop, thirty arrays of three members (under 1.7 KB) allocate without
// bound.
func TestExpansionStopsAfterOverrun(t *testing.T) {
	sizes := make([]int, 30)
	for i := range sizes {
		sizes[i] = 3
	}
	pattern := []byte(orArrays(sizes...))
	for _, profile := range []Profile{EventBridge{}, SNS{Scope: MessageBody}, LambdaESM{}} {
		var err *CompileError
		bytes := allocated(func() { _, err = Compile(pattern, profile) })
		if err == nil || err.Cause != (LimitExceeded{Limit: LimitCombinations}) {
			t.Fatalf("%T: 3^30 branches = %#v, want the combinations limit", profile, err)
		}
		if limit := uint64(64 << 20); bytes > limit {
			t.Errorf("%T: Compile allocated %d bytes, want at most %d", profile, bytes, limit)
		}
	}
}

// TestLimitsStopTheExpansion: once a policy is certain to exceed an SNS or
// EventBridge limit, the compiler stops building "$or" branches, so a large
// policy is rejected without expanding it; a later grammar error still wins.
func TestLimitsStopTheExpansion(t *testing.T) {
	ors := make([]int, 13)
	for i := range ors {
		ors[i] = 2
	}
	siblings := make([]string, 4000)
	for i := range siblings {
		siblings[i] = `"s` + strconv.Itoa(i) + `":["1"]`
	}
	snsPolicy := strings.TrimSuffix(orArrays(ors...), "}") + "," + strings.Join(siblings, ",") + "}"
	ebPattern := strings.TrimSuffix(orArrays(11, 11, 11), "}") + "," + strings.Join(siblings[:300], ",") + "}"
	tests := []struct {
		name    string
		pattern string
		profile Profile
		limit   Limit
	}{
		{"SNS keys over 13 $or arrays and 4000 keys", snsPolicy, SNS{Scope: MessageBody}, LimitKeys},
		{"EventBridge 1331 combinations and 300 keys", ebPattern, EventBridge{}, LimitCombinations},
	}
	for _, tc := range tests {
		var err *CompileError
		bytes := allocated(func() { _, err = Compile([]byte(tc.pattern), tc.profile) })
		if err == nil || err.Cause != (LimitExceeded{Limit: tc.limit}) {
			t.Fatalf("%s: Compile = %#v, want limit %s", tc.name, err, tc.limit)
		}
		if limit := uint64(64 * len(tc.pattern)); bytes > limit {
			t.Errorf("%s: Compile of %d bytes allocated %d bytes, want at most %d", tc.name, len(tc.pattern), bytes, limit)
		}
		late := strings.TrimSuffix(tc.pattern, "}") + `,"z":[]}`
		if _, err := Compile([]byte(late), tc.profile); err == nil || err.Detail != "Empty arrays are not allowed" {
			t.Errorf("%s: Compile with a late grammar error = %v, want the grammar error", tc.name, err)
		}
	}
}

// TestLongEventBridgePatternIsBounded: a 4096-character pattern at the 1000
// combination cap, every branch holding every sibling key, compiles within a
// bounded allocation.
func TestLongEventBridgePatternIsBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString(strings.TrimSuffix(orArrays(10, 10, 10), "}"))
	keys := 0
	for b.Len()+len(`,"s000":[1]}`) <= 4096 {
		fmt.Fprintf(&b, `,"s%03d":[1]`, keys)
		keys++
	}
	b.WriteString("}")
	pattern := []byte(b.String())
	var err *CompileError
	bytes := allocated(func() { _, err = Compile(pattern, EventBridge{}) })
	if err != nil {
		t.Fatalf("Compile rejected the %d-character pattern: %s", len(pattern), err.Detail)
	}
	if limit := uint64(16 << 20); bytes > limit {
		t.Fatalf("Compile of %d characters with %d sibling keys allocated %d bytes, want at most %d",
			len(pattern), keys, bytes, limit)
	}
}

// TestWildcardComplexity checks the evaluator against Ruler v2.2.0's values
// (matching-engines.md 4.8) and the EventBridge accept/reject fixtures.
func TestWildcardComplexity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern string
		want    int
		ok      bool
	}{
		{"arn:aws:events:us-east-1:*:event-bus/*", 3, true},
		{"/Users/*/doc.txt", 2, true},
		{"/Users/*/dir/dir/dir/dir/dir/doc.txt", 7, true},
		{"*a*a*a*a*", 9, true},
		{"*:*:*:*:*:event-bus/*", 11, false},
		{"plain", 1, true},
		{"*", 2, true},
		{"a*", 2, true},
	}
	for _, tc := range tests {
		g, detail := parseGlob(tc.pattern)
		if detail != "" {
			t.Fatalf("parseGlob(%q): %s", tc.pattern, detail)
		}
		if got := wildcardComplexity([]glob{g}, 100); got != tc.want {
			t.Errorf("wildcardComplexity(%q) = %d, want %d", tc.pattern, got, tc.want)
		}
		_, err := Compile([]byte(`{"x":[{"wildcard":`+strconv.Quote(tc.pattern)+`}]}`), EventBridge{})
		if tc.ok != (err == nil) {
			t.Errorf("EventBridge Compile(wildcard %q) = %v, want ok=%t", tc.pattern, err, tc.ok)
		}
	}
	// Sets of patterns on one field, with Ruler's values: a repeated pattern
	// counts once.
	sets := []struct {
		patterns []string
		want     int
	}{
		{[]string{"*b", ":"}, 3},
		{[]string{"ab", "ab"}, 1},
		{[]string{"*:*:*:event-bus/*", "*:*:*:*:x/*"}, 16},
		{[]string{"*a*a*", "*a*a*a*"}, 12},
	}
	for _, tc := range sets {
		var gs []glob
		for _, p := range tc.patterns {
			g, _ := parseGlob(p)
			gs = append(gs, g)
		}
		if got := wildcardComplexity(gs, 100); got != tc.want {
			t.Errorf("wildcardComplexity(%q) = %d, want %d", tc.patterns, got, tc.want)
		}
	}
	// "$or" branches that reach a field from the same name state share one
	// ByteMachine, and Ruler (v2.2.0, Machine.evaluateComplexity) scores
	// their wildcards together: 16 and 12, over the cap of 11.
	shared := []string{
		`{"$or":[{"f":[{"wildcard":"*:*:*:event-bus/*"}]},{"f":[{"wildcard":"*:*:*:*:x/*"}]}]}`,
		`{"$or":[{"f":[{"wildcard":"*a*a*"}]},{"f":[{"wildcard":"*a*a*a*"}]}]}`,
		`{"g":["1"],"$or":[{"f":[{"wildcard":"*a*a*"}]},{"f":[{"wildcard":"*a*a*a*"}]}]}`,
		`{"$or":[{"f":[{"wildcard":"*a*a*"}],"g":["1"]},{"f":[{"wildcard":"*a*a*a*"}],"h":["1"]}]}`,
	}
	for _, p := range shared {
		_, err := Compile([]byte(p), EventBridge{})
		if err == nil || err.Cause != (LimitExceeded{Limit: LimitWildcardComplexity}) {
			t.Errorf("EventBridge Compile(%s) = %v, want the wildcard complexity limit", p, err)
		}
	}
}
