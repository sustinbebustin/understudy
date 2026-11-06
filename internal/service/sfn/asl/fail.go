package asl

import (
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// failMessages are a Fail state's error and cause (stepfunctions-asl.md
// 7.2): literals, or ErrorPath and CausePath evaluated on the state's input.
// "" is an absent error or cause, which the history leaves out (11.3).
func (s *stepper) failMessages(st failState, input document.Value) (name, cause string, f Failure) {
	sc := s.scopeOf(st.name)
	if name, f = sc.message(st.error, "ErrorPath", input); f != nil {
		return "", "", f
	}
	if cause, f = sc.message(st.cause, "CausePath", input); f != nil {
		return "", "", f
	}
	return name, cause, nil
}

// message evaluates one Fail message. A path that selects nothing is
// States.Runtime with the recorded "could not be found in the input" text
// (stepfunctions-asl.md 4.6, aws-recorded localstack@8b9a79f
// v2/scenarios/test_base_scenarios.snapshot.json:L26310, L26499); a failing
// intrinsic is the sfn.intrinsic.failure-error switch's error.
func (sc scope) message(m message, field string, input document.Value) (string, Failure) {
	var v document.Value
	switch m := m.(type) {
	case noMessage:
		return "", nil
	case literalMessage:
		return m.text, nil
	case pathMessage:
		got, err := m.path.Eval(input, sc.context)
		if err != nil {
			return "", missingInTemplate(field, input, err)
		}
		v = got
	case callMessage:
		got, err := m.call.Eval(input, sc.context, sc.src)
		if err != nil {
			return "", sc.callFailure(m.call, field, input, err)
		}
		v = got
	}
	s, ok := v.(document.String)
	if !ok {
		// AWS's answer to a path that selects a value other than a string is
		// unrecorded; understudy fails the state as it does for a path that
		// selects nothing (UNVERIFIED).
		return "", Terminal{Name: errRuntime, Cause: "The JSONPath specified for the field '" + field +
			"' selected " + text(v) + ", which is not a string"}
	}
	return string(s), nil
}
