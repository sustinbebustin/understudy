package asl_test

import (
	"testing"
)

// These tests run the definitions of the 2026-11-03 recording session
// (testdata/parity/sfn/ASL-*) with the service's rules and check the
// outcome and history AWS recorded.

// choiceInParallel is ASL-08's definition: a Choice without Default inside
// a Parallel whose catcher would catch any catchable error.
const choiceInParallel = `{"StartAt":"P","States":{"P":{"Type":"Parallel","Branches":[{"StartAt":"C","States":{
	"C":{"Type":"Choice","Choices":[{"Variable":"$.x","NumericEquals":1,"Next":"S"}]},"S":{"Type":"Succeed"}}}],
	"Catch":[{"ErrorEquals":["States.ALL"],"Next":"Caught"}],"End":true},"Caught":{"Type":"Succeed"}}}`

// A Choice no rule matches, without Default, fails the execution with an
// uncatchable States.Runtime; a numeric string never matches a numeric rule
// (ASL-08, recorded 2026-11-03; stepfunctions-asl.md 6.4).
func TestASL08ChoiceNoMatchIsRuntime(t *testing.T) {
	for _, input := range []string{`{"x":2}`, `{"x":"1"}`} {
		d := newDriver(t, choiceInParallel, rules())
		d.start(input)
		o := d.runToEnd(20)
		failedWith(t, o, "States.Runtime", func(c string) bool {
			return c == runtimePrefix("C", 4)+"Failed to transition out of the state. The state does not point to a next state."
		})
		d.wantShape("ASL-08 01-no-match-history",
			"ExecutionStarted 1<-0", "ParallelStateEntered 2<-0", "ParallelStateStarted 3<-2", "ChoiceStateEntered 4<-3", "ExecutionFailed 5<-4")
	}
}

// A Choice Variable that selects nothing fails the execution with an
// uncatchable States.Runtime and the recorded cause (ASL-08, recorded
// 2026-11-03; stepfunctions-asl.md 6.4).
func TestASL08ChoiceMissingVariableIsRuntime(t *testing.T) {
	d := newDriver(t, choiceInParallel, rules())
	d.start(`{}`)
	failedWith(t, d.runToEnd(20), "States.Runtime", func(c string) bool {
		return c == runtimePrefix("C", 4)+"Invalid path '$.x': The choice state's condition path references an invalid value."
	})
	d.wantShape("ASL-08 02-missing-history",
		"ExecutionStarted 1<-0", "ParallelStateEntered 2<-0", "ParallelStateStarted 3<-2", "ChoiceStateEntered 4<-3", "ExecutionFailed 5<-4")
}
