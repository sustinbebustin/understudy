package asl_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// The engine-level switches are inferred, so these tests read each value
// from its switch and check the engine follows it, then flip the value and
// check the behavior changes (spec "Fidelity switches": a flip test per
// switch). An ErrorRule's Name is the error the execution reports; its
// Terminal decides whether a catcher sees it. Catchability is observed
// through an inline Map's catcher, since an iteration's error becomes the
// Map's (aws-recorded localstack@8b9a79f
// v2/scenarios/test_base_scenarios.snapshot.json:L1398-L1430).

// flipped is r with another name and the other catchability.
func flipped(r asl.ErrorRule) asl.ErrorRule {
	return asl.ErrorRule{Name: "Flipped." + r.Name, Terminal: !r.Terminal}
}

// withRule returns the switch rules with one ErrorRule replaced.
func withRule(set func(*asl.Rules)) asl.Rules {
	r := sfn.EngineRules()
	set(&r)
	return r
}

// inMap wraps one inner state, named Inner, in a one-item Map that
// catches States.ALL into Done.
func inMap(inner string) string {
	return `{"StartAt":"M","States":{
		"M":{"Type":"Map","ItemsPath":"$.items","ItemSelector":{"v.$":"$.v","a.$":"$.a"},"Next":"Done",
			"Catch":[{"ErrorEquals":["States.ALL"],"Next":"Done"}],
			"ItemProcessor":{"StartAt":"Inner","States":{"Inner":` + inner + `,"Fallback":{"Type":"Pass","End":true}}}},
		"Done":{"Type":"Pass","End":true}}}`
}

// checkRule runs inner at the top level and inside a catching Map under
// rules, and checks both against rule. enteredInMap is the id of Inner's
// *Entered event inside the Map.
func checkRule(t *testing.T, rule asl.ErrorRule, rules asl.Rules, inner, input string, cause func(entered int64) func(string) bool) {
	t.Helper()
	top := newDriver(t, `{"StartAt":"Inner","States":{"Inner":`+inner+`,"Fallback":{"Type":"Pass","End":true}}}`, rules)
	top.start(input)
	failedWith(t, top.runToEnd(20), rule.Name, cause(2))

	nested := newDriver(t, inMap(inner), rules)
	nested.start(strings.Replace(input, "{", `{"items":[0],`, 1))
	o := nested.runToEnd(50)
	if rule.Terminal {
		// Map Entered 2, Started 3, IterationStarted 4, Inner entered 5.
		failedWith(t, o, rule.Name, cause(5))
		return
	}
	s, ok := o.(asl.Succeeded)
	if !ok || !equal(at(s.Output, "Error"), document.String(rule.Name)) {
		t.Fatalf("catchable %s: outcome = %#v, want the Map's catcher to see it", rule.Name, o)
	}
}

func anyCause(int64) func(string) bool { return func(string) bool { return true } }

// sfn.choice.no-match: a Choice no rule matches, without Default.
func TestSwitchChoiceNoMatch(t *testing.T) {
	inner := `{"Type":"Choice","Choices":[{"Variable":"$.v","NumericEquals":1,"Next":"Fallback"}]}`
	rule := sfn.ChoiceNoMatch().Value
	for _, r := range []asl.ErrorRule{rule, flipped(rule)} {
		rules := withRule(func(x *asl.Rules) { x.NoMatch = r })
		checkRule(t, r, rules, inner, `{"v":2,"a":[]}`, anyCause)
	}
}

// sfn.choice.missing-variable: a Choice Variable that selects nothing. Its
// cause carries the stepfunctions-asl.md 4.6 prefix (the switch's declared
// form).
func TestSwitchChoiceMissingVariable(t *testing.T) {
	inner := `{"Type":"Choice","Choices":[{"Variable":"$.nope","NumericEquals":1,"Next":"Fallback"}],"Default":"Fallback"}`
	rule := sfn.ChoiceMissingVariable().Value
	for _, r := range []asl.ErrorRule{rule, flipped(rule)} {
		rules := withRule(func(x *asl.Rules) { x.MissingVariable = r })
		checkRule(t, r, rules, inner, `{"v":2,"a":[]}`, func(entered int64) func(string) bool {
			return func(c string) bool { return strings.HasPrefix(c, runtimePrefix("Inner", entered)) }
		})
	}
}

// sfn.intrinsic.failure-error: an intrinsic function failing at run time.
func TestSwitchIntrinsicFailure(t *testing.T) {
	inner := `{"Type":"Pass","Parameters":{"x.$":"States.ArrayGetItem($.a, -1)"},"Next":"Fallback"}`
	rule := sfn.IntrinsicFailure().Value
	for _, r := range []asl.ErrorRule{rule, flipped(rule)} {
		rules := withRule(func(x *asl.Rules) { x.IntrinsicFailure = r })
		checkRule(t, r, rules, inner, `{"v":2,"a":[1]}`, anyCause)
	}
}

// sfn.delete.running-execution: with the switch on, MachineDeleting ends
// the execution ABORTED, with no error or cause, at its next transition;
// off, the execution runs on (stepfunctions-api.md 5.4 quotes "On the next
// state transition, the state machine's executions are terminated"; the
// outcome is inferred, SFN-06).
func TestSwitchDeleteRunningExecution(t *testing.T) {
	def := `{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":60,"Next":"P"},"P":{"Type":"Pass","End":true}}}`
	value := sfn.DeleteRunningExecution().Value
	for _, abort := range []bool{value, !value} {
		d := newDriver(t, def, withRule(func(x *asl.Rules) { x.AbortOnDelete = abort }))
		d.start(`{}`)
		d.runUntilWaiting()
		d.apply(asl.MachineDeleting{At: t0.Add(time.Second)})
		if _, done := d.x.Outcome(); done {
			t.Fatalf("AbortOnDelete %v: the execution ended before its next transition", abort)
		}
		o := d.runToEnd(20)
		if abort {
			if a, ok := o.(asl.Aborted); !ok || a.Error != "" || a.Cause != "" {
				t.Errorf("AbortOnDelete true: outcome = %#v, want Aborted without error or cause", o)
			}
			if d.events("PassStateEntered") != nil {
				t.Errorf("AbortOnDelete true: the execution transitioned:\n%s", d.shape())
			}
			continue
		}
		if _, ok := o.(asl.Succeeded); !ok {
			t.Errorf("AbortOnDelete false: outcome = %#v, want the execution to finish", o)
		}
	}
}
