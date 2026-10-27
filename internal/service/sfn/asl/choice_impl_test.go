package asl_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// A Choice reads its rules on the effective input after InputPath and
// writes that input through OutputPath (stepfunctions-asl.md 4.1, 11.3).
func TestChoiceInputAndOutputPath(t *testing.T) {
	def := `{"StartAt":"C","States":{
		"C":{"Type":"Choice","InputPath":"$.in","OutputPath":"$.v","Choices":[{"Variable":"$.v","NumericEquals":1,"Next":"Yes"}],"Default":"No"},
		"Yes":{"Type":"Pass","End":true},
		"No":{"Type":"Pass","Result":"no","End":true}}}`
	d := newDriver(t, def, rules())
	d.start(`{"in":{"v":1},"v":2}`)
	o := d.runToEnd(10)
	if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, decode(t, `1`)) {
		t.Fatalf("outcome = %#v, want the rule to read $.in.v and the output to be $.in.v", o)
	}
	if got := detailJSON(t, d.event("ChoiceStateExited"), "output"); !equal(got, decode(t, `1`)) {
		t.Errorf("ChoiceStateExited output = %s, want 1", encode(got))
	}
}

// Variable reads the context object (stepfunctions-asl.md 4.7).
func TestChoiceContextVariable(t *testing.T) {
	if got := choose(t, `{"Variable":"$$.State.Name","StringEquals":"C","Next":"Yes"}`, `{}`); got != "yes" {
		t.Fatalf("$$.State.Name took %q, want yes", got)
	}
}

// inferred, ASL-08
// No rule matching and no Default fails with sfn.choice.no-match's error
// and understudy's cause, pointing at ChoiceStateEntered.
func TestChoiceNoMatchCause(t *testing.T) {
	d := newDriver(t, `{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"Variable":"$.v","NumericEquals":1,"Next":"Y"}]},"Y":{"Type":"Succeed"}}}`, rules())
	d.start(`{"v":2}`)
	failedWith(t, d.runToEnd(10), rules().NoMatch.Name, func(c string) bool {
		return c == "No Choice rule of the state 'C' matched its input, and the state has no Default."
	})
	d.wantShape("inferred, ASL-08", "ExecutionStarted 1<-0", "ChoiceStateEntered 2<-0", "ExecutionFailed 3<-2")
}

// inferred, ASL-08
// A Variable, or the operand of a Path form, that selects nothing fails
// with sfn.choice.missing-variable's error; its cause is the 4.6 prefix and
// the path's missing-path text. Only IsPresent tests an absent Variable.
func TestChoiceMissingPaths(t *testing.T) {
	missing := rules().MissingVariable
	for _, tt := range []struct {
		rule, input, cause string
	}{
		{`{"Variable":"$.nope","NumericEquals":1,"Next":"Yes"}`, `{}`, "Invalid path '$.nope' : No results for path: $['nope']"},
		{`{"Variable":"$.a.b","IsNull":true,"Next":"Yes"}`, `{}`, "Invalid path '$.a.b' : Missing property in path $['a']"},
		{`{"Variable":"$.v","NumericLessThanPath":"$.w","Next":"Yes"}`, `{"v":1}`, "Invalid path '$.w' : No results for path: $['w']"},
		{`{"Not":{"Variable":"$.nope","StringEquals":"x"},"Next":"Yes"}`, `{}`, "Invalid path '$.nope' : No results for path: $['nope']"},
	} {
		d := newDriver(t, fmt.Sprintf(choiceMachine, tt.rule), rules())
		d.start(tt.input)
		failedWith(t, d.runToEnd(10), missing.Name, func(c string) bool { return c == runtimePrefix("C", 2)+tt.cause })
	}
	if got := choose(t, `{"Variable":"$.nope","IsPresent":false,"Next":"Yes"}`, `{}`); got != "yes" {
		t.Errorf("IsPresent false on an absent Variable took %q, want yes", got)
	}
}

// inferred, ASL-07
// IsTimestamp and the Timestamp operators accept the documented profile:
// upper-case T, seconds, an optional fraction, and Z or a numeric offset;
// instants compare across offsets and precisions.
func TestChoiceTimestampProfile(t *testing.T) {
	for _, tt := range []struct {
		value string
		ok    bool
	}{
		{"2016-03-14T01:59:00Z", true},
		{"2016-03-14T01:59:00.123456789Z", true},
		{"2016-03-14T01:59:00+05:30", true},
		{"2016-03-14t01:59:00Z", false},
		{"2016-03-14T01:59:00z", false},
		{"2016-03-14T01:59Z", false},
		{"2016-03-14T01:59:00", false},
		{"2016-03-14 01:59:00Z", false},
		{"2016-02-30T01:59:00Z", false},
	} {
		want := map[bool]string{true: "yes", false: "no"}[tt.ok]
		if got := choose(t, `{"Variable":"$.v","IsTimestamp":true,"Next":"Yes"}`, `{"v":"`+tt.value+`"}`); got != want {
			t.Errorf("IsTimestamp on %q took %q, want %q", tt.value, got, want)
		}
	}
	if got := choose(t, `{"Variable":"$.v","TimestampEquals":"2016-03-14T01:59:00Z","Next":"Yes"}`, `{"v":"2016-03-14T03:59:00.000+02:00"}`); got != "yes" {
		t.Errorf("TimestampEquals across offsets took %q, want yes", got)
	}
	if got := choose(t, `{"Variable":"$.v","IsTimestamp":true,"Next":"Yes"}`, `{"v":1}`); got != "no" {
		t.Errorf("IsTimestamp on a number took %q, want no", got)
	}
}

// StringMatches: '\*' and '\\' are a literal star and backslash
// (stepfunctions-asl.md 6.3; JSON doubles each backslash).
func TestChoiceStringMatchesEscapes(t *testing.T) {
	for _, tt := range []struct {
		pattern, value, want string
	}{
		{`a\\*b`, `a*b`, "yes"},
		{`a\\*b`, `axb`, "no"},
		{`a\\\\b`, `a\\b`, "yes"},
		{`*`, ``, "yes"},
		{`**x`, `x`, "yes"},
		{`a*b*c`, `abc`, "yes"},
		{`a*b*c`, `acb`, "no"},
		{`a*a`, `a`, "no"},
	} {
		rule := `{"Variable":"$.v","StringMatches":"` + tt.pattern + `","Next":"Yes"}`
		if got := choose(t, rule, `{"v":"`+tt.value+`"}`); got != tt.want {
			t.Errorf("StringMatches %s on %s took %q, want %q", tt.pattern, tt.value, got, tt.want)
		}
	}
}

// inferred, ASL-07
// A backslash that escapes neither '*' nor '\' "will throw a runtime error"
// (stepfunctions-asl.md 6.3): understudy fails with States.Runtime when the
// rule is evaluated.
func TestChoiceStringMatchesUnpairedBackslash(t *testing.T) {
	for _, pattern := range []string{`a\\b`, `a\\`} {
		d := newDriver(t, fmt.Sprintf(choiceMachine, `{"Variable":"$.v","StringMatches":"`+pattern+`","Next":"Yes"}`), rules())
		d.start(`{"v":"ab"}`)
		failedWith(t, d.runToEnd(10), "States.Runtime", func(c string) bool { return strings.HasPrefix(c, runtimePrefix("C", 2)) })
	}
}

// StringMatches agrees with the regular expression that turns each '*'
// into ".*" and quotes the rest.
func TestChoiceStringMatchesProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		pattern := rapid.StringOfN(rapid.SampledFrom([]rune("ab*")), 0, 6, -1).Draw(t, "pattern")
		value := rapid.StringOfN(rapid.SampledFrom([]rune("ab")), 0, 6, -1).Draw(t, "value")
		parts := strings.Split(pattern, "*")
		for i, p := range parts {
			parts[i] = regexp.QuoteMeta(p)
		}
		want := map[bool]string{true: "yes", false: "no"}[regexp.MustCompile(`^`+strings.Join(parts, ".*")+`$`).MatchString(value)]
		d := newDriver(t, fmt.Sprintf(choiceMachine, `{"Variable":"$.v","StringMatches":"`+pattern+`","Next":"Yes"}`), rules())
		d.start(`{"v":"` + value + `"}`)
		o := d.runToEnd(20)
		if s, ok := o.(asl.Succeeded); !ok || !equal(s.Output, decode(t, `"`+want+`"`)) {
			t.Fatalf("StringMatches %q on %q: outcome %#v, want %q", pattern, value, o, want)
		}
	})
}
