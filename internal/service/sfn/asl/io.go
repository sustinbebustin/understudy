package asl

import (
	"errors"
	"strconv"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/intrinsic"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/jsonpath"
)

// scope is what a state's paths and templates read: the input "$" and the
// context object "$$" (stepfunctions-asl.md 4.3, 4.7).
type scope struct {
	context document.Value
	src     intrinsic.Source
	rules   Rules
}

// scopeOf is what the state named name reads, entered at the step's
// instant.
func (s *stepper) scopeOf(name string) scope {
	return s.scopeAt(name, s.at)
}

// scopeAt is what the state named name reads, entered at entered.
func (s *stepper) scopeAt(name string, entered time.Time) scope {
	return scope{context: stateContext(s.x.context, name, entered), src: s.src, rules: s.rules}
}

// pass is a Pass state's output (stepfunctions-asl.md 4.1): InputPath, then
// Parameters, give the effective input; Result, when given, is the result
// and otherwise the effective input is; ResultPath places it into the raw
// input and OutputPath selects the output.
func (s *stepper) pass(st passState, raw document.Value) (document.Value, Failure) {
	sc := s.scopeOf(st.name)
	effective, f := sc.selectFrom(st.io.input, raw)
	if f != nil {
		return nil, f
	}
	if st.parameters != nil {
		if effective, f = sc.render(st.parameters, effective); f != nil {
			return nil, f
		}
	}
	result := effective
	if st.result != nil {
		result = st.result
	}
	placed, f := placeResult(st.resultPath, raw, result)
	if f != nil {
		return nil, f
	}
	return sc.selectFrom(st.io.output, placed)
}

// passThrough is the output of a state whose output is its input filtered by
// InputPath, then OutputPath (Succeed; stepfunctions-asl.md 4.1).
func (s *stepper) passThrough(name string, io paths, raw document.Value) (document.Value, Failure) {
	sc := s.scopeOf(name)
	effective, f := sc.selectFrom(io.input, raw)
	if f != nil {
		return nil, f
	}
	return sc.selectFrom(io.output, effective)
}

// stateFailed ends the execution with a failure the state named name
// raised before writing its exited event; ExecutionFailed points at its
// entered event. A States.Runtime cause starts with the recorded prefix
// naming the state (stepfunctions-asl.md 4.6).
func (s *stepper) stateFailed(name string, entered int64, f Failure) {
	s.stateFailedAfter(name, entered, entered, f)
}

// stateFailedAfter is stateFailed for a state that wrote events after its
// entered event: ExecutionFailed points at last, the last of them
// (stepfunctions-asl.md 11.2 rule 8), while a States.Runtime cause still
// names the entered event.
func (s *stepper) stateFailedAfter(name string, entered, last int64, f Failure) {
	if t, ok := f.(Terminal); ok && t.Name == errRuntime {
		t.Cause = runtimeCause(name, entered, t.Cause)
		f = t
	}
	s.raise(f, last)
}

// raise ends the step's frame with f, whose cause is complete; the
// failure's first event points at last. At the top level it ends the
// execution; in a branch or iteration it fails the enclosing Parallel or
// Map (scopeFailed). It serves the states without Retry or Catch of their
// own; a Task's failures go through taskFailed.
func (s *stepper) raise(f Failure, last int64) {
	s.scopeFailed(f, last)
}

// runtimeCause is rest after the recorded prefix of a States.Runtime cause
// naming the state and its entered event (stepfunctions-asl.md 4.6).
func runtimeCause(name string, entered int64, rest string) string {
	return "An error occurred while executing the state '" + name + "' (entered at the event id #" +
		strconv.FormatInt(entered, 10) + "). " + rest
}

// stateContext is the context object a state sees: Start's Execution and
// StateMachine members and State {Name, EnteredTime}, in the recorded order
// (stepfunctions-asl.md 4.7; aws-recorded localstack@8b9a79f
// v2/base/test_base.snapshot.json:L597-L615).
func stateContext(start document.Value, name string, entered time.Time) document.Map {
	return document.Map{
		{Key: "Execution", Value: at(start, "Execution")},
		{Key: "StateMachine", Value: at(start, "StateMachine")},
		{Key: "State", Value: document.Map{
			{Key: "Name", Value: document.String(name)},
			{Key: "EnteredTime", Value: document.String(ContextTime(entered))},
		}},
	}
}

// ContextTime renders a context-object timestamp in UTC with zero, three,
// six or nine fraction digits (aws-doc dg/input-output-contextobject,
// stepfunctions-asl.md 4.7).
func ContextTime(t time.Time) string {
	t = t.UTC()
	switch ns := t.Nanosecond(); {
	case ns == 0:
		return t.Format("2006-01-02T15:04:05Z")
	case ns%1_000_000 == 0:
		return t.Format("2006-01-02T15:04:05.000Z")
	case ns%1_000 == 0:
		return t.Format("2006-01-02T15:04:05.000000Z")
	default:
		return t.Format("2006-01-02T15:04:05.000000000Z")
	}
}

// selectFrom applies an InputPath or OutputPath: absent is the whole value,
// null is {} (stepfunctions-asl.md 4.2), and a missing definite path is
// States.Runtime with the path's recorded text (4.6).
func (sc scope) selectFrom(sel selector, v document.Value) (document.Value, Failure) {
	switch sel := sel.(type) {
	case selectAll:
		return v, nil
	case selectNone:
		return document.Map{}, nil
	case selectPath:
		out, err := sel.path.Eval(v, sc.context)
		if err != nil {
			return nil, Terminal{Name: errRuntime, Cause: err.Error()}
		}
		return out, nil
	}
	return v, nil
}

// render evaluates a payload template on input (stepfunctions-asl.md 4.3):
// ".$" members are evaluated and renamed, everything else is copied.
func (sc scope) render(t template, input document.Value) (document.Value, Failure) {
	switch t := t.(type) {
	case templateObject:
		out := make(document.Map, 0, len(t.members))
		for _, m := range t.members {
			v, f := sc.render(m.value, input)
			if f != nil {
				return nil, f
			}
			out = append(out, document.Entry{Key: m.key, Value: v})
		}
		return out, nil
	case templateLiteral:
		return t.value, nil
	case templatePath:
		v, err := t.path.Eval(input, sc.context)
		if err != nil {
			return nil, missingInTemplate(t.field, input, err)
		}
		return v, nil
	case templateCall:
		v, err := t.call.Eval(input, sc.context, sc.src)
		if err != nil {
			return nil, sc.callFailure(t.call, t.field, input, err)
		}
		return v, nil
	}
	return input, nil
}

// missingInTemplate is a ".$" path that selects nothing: States.Runtime with
// the recorded text (stepfunctions-asl.md 4.6). The input inside the quotes
// is its compact JSON text, member order kept; AWS's serialisation is
// unrecorded (ASL-04).
func missingInTemplate(field string, input document.Value, err error) Failure {
	var missing *jsonpath.MissingError
	if !errors.As(err, &missing) {
		return Terminal{Name: errRuntime, Cause: err.Error()}
	}
	return Terminal{Name: errRuntime, Cause: "The JSONPath '" + missing.Path + "' specified for the field '" + field +
		"' could not be found in the input '" + text(input) + "'"}
}

// callFailure names a failure of call: a function failing is the
// sfn.intrinsic.failure-error switch's error, its cause quoting the call as
// written before the function's detail (ASL-05, recorded 2026-11-03;
// stepfunctions-asl.md 5.4); anything else, a path argument that selects
// nothing included, reads as a missing ".$" path.
func (sc scope) callFailure(call intrinsic.Call, field string, input document.Value, err error) Failure {
	if fe, ok := errors.AsType[*intrinsic.FunctionError](err); ok {
		return ruleFailure(sc.rules.IntrinsicFailure, "There was an error while evaluating the intrinsic function: "+call.Text()+". "+fe.Detail)
	}
	return missingInTemplate(field, input, err)
}

// dataLimit is States.DataLimitExceeded when the output of the state named
// name is over maxDataBytes of UTF-8 JSON text, with the recorded cause and
// no States.Runtime prefix (stepfunctions-asl.md 8.5; aws-recorded
// localstack@8b9a79f v2/error_handling/test_states_errors.snapshot.json:L608).
// It is terminal: a Pass state has no Catch to show otherwise, and the error
// stays terminal inside a Parallel or Map (inferred, ASL-15).
func dataLimit(name string, output document.Value) Failure {
	if len(text(output)) <= maxDataBytes {
		return nil
	}
	return Terminal{Name: errDataLimit, Cause: "The state/task '" + name + "' returned a result with a size exceeding the maximum number of bytes service limit."}
}

// ruleFailure is a switch's error with cause.
func ruleFailure(r ErrorRule, cause string) Failure {
	if r.Terminal {
		return Terminal{Name: r.Name, Cause: cause}
	}
	return StateError{Name: r.Name, Cause: cause}
}

// placeResult applies a ResultPath (stepfunctions-asl.md 4.2, 4.4): absent
// replaces the raw input with the result, null keeps the raw input, a path
// places the result into the raw input. A path that cannot be applied, such
// as a name into a non-object, is States.Runtime: AWS's error is
// unrecorded (ASL-03).
func placeResult(t target, raw, result document.Value) (document.Value, Failure) {
	switch t := t.(type) {
	case replaceInput:
		return result, nil
	case discardResult:
		return raw, nil
	case placeAt:
		out, err := t.path.Place(raw, result)
		if err != nil {
			return nil, Terminal{Name: errRuntime, Cause: err.Error()}
		}
		return out, nil
	}
	return result, nil
}
