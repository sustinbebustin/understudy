package asl

import (
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// wait enters a Wait state (stepfunctions-asl.md 7.1, 11.3): it writes
// WaitStateEntered, works out when the wait ends from the effective input,
// and arms the exit then. Its output is the effective input through
// OutputPath, worked out on entry, where its paths see the state's
// EnteredTime.
func (s *stepper) wait(st waitState, j stepJob) {
	entered := s.emit("WaitStateEntered", j.previous, stateEntered(st.name, j.input))
	sc := s.scopeOf(st.name)
	effective, f := sc.selectFrom(st.io.input, j.input)
	if f != nil {
		s.stateFailed(st.name, entered, f)
		return
	}
	until, f := sc.until(st.until, effective, s.at)
	if f != nil {
		s.stateFailed(st.name, entered, f)
		return
	}
	output, f := sc.selectFrom(st.io.output, effective)
	if f == nil {
		f = dataLimit(st.name, output)
	}
	if f != nil {
		s.stateFailed(st.name, entered, f)
		return
	}
	s.arm(until, waitJob{state: st.name, output: output, entered: entered})
}

// waitExit ends a Wait state: WaitStateExited points at WaitStateEntered
// (stepfunctions-asl.md 11.3 Wait row), then the state's transition.
func (s *stepper) waitExit(j waitJob) {
	st, ok := s.d.states[j.state].(waitState)
	if !ok {
		// Parse built the state the job names; a definition without it was
		// changed under a running execution.
		s.fail(errRuntime, "The state '"+j.state+"' is not in the definition the execution runs.", j.entered)
		return
	}
	exited := s.emit("WaitStateExited", j.entered, stateExited(st.name, j.output))
	s.transition(st.next, j.output, exited)
}

// longestWait bounds a wait: no execution runs past maxExecution, so a
// later end is never reached, and the bound keeps the instant
// representable. A wait past the execution's deadline ends with the
// deadline, never with "Wait state delay is too long." (unrecorded,
// stepfunctions-asl.md 7.1; inferred, ASL-09).
const longestWait = maxExecution + time.Second

// until is the instant a Wait ends, at the earliest now. A path that does
// not select a usable value is States.Runtime with the recorded texts
// (stepfunctions-asl.md 4.6); a timestamp counts to the second, its
// fraction truncated (7.1, aws-doc dg/state-wait).
func (sc scope) until(w wait, input document.Value, now time.Time) (time.Time, Failure) {
	var at time.Time
	switch w := w.(type) {
	case waitSeconds:
		return now.Add(time.Duration(w.seconds) * time.Second), nil
	case waitTimestamp:
		at = w.at
	case waitSecondsPath:
		field := "The SecondsPath parameter "
		v, err := w.path.Eval(input, sc.context)
		if err != nil {
			return time.Time{}, Terminal{Name: errRuntime, Cause: field + "does not reference an input value: " + w.path.String()}
		}
		n, isNumber := v.(document.Number)
		seconds, err := n.Int64()
		if !isNumber || err != nil {
			return time.Time{}, Terminal{Name: errRuntime, Cause: field + "cannot be parsed as a long value: " + w.path.String() + " == " + shown(v)}
		}
		if seconds < 0 {
			return time.Time{}, Terminal{Name: errRuntime, Cause: field + "references a negative value: " + w.path.String() + " == " + shown(v)}
		}
		if seconds > int64(longestWait/time.Second) {
			seconds = int64(longestWait / time.Second)
		}
		return now.Add(time.Duration(seconds) * time.Second), nil
	case waitTimestampPath:
		field := "The TimestampPath parameter "
		v, err := w.path.Eval(input, sc.context)
		if err != nil {
			// The SecondsPath text, for the other path field (inferred,
			// ASL-09).
			return time.Time{}, Terminal{Name: errRuntime, Cause: field + "does not reference an input value: " + w.path.String()}
		}
		str, isString := v.(document.String)
		t, ok := parseTimestamp(string(str))
		if !isString || !ok {
			return time.Time{}, Terminal{Name: errRuntime, Cause: field + "does not reference a valid ISO-8601 extended offset date-time format string: " + w.path.String() + " == " + shown(v)}
		}
		at = t
	}
	at = at.Truncate(time.Second)
	if latest := now.Add(longestWait); at.After(latest) {
		return latest, nil
	}
	if at.Before(now) {
		return now, nil
	}
	return at, nil
}

// shown is a value as a Wait path text quotes it: a string's characters,
// anything else its JSON text (recorded for strings and numbers,
// stepfunctions-asl.md 4.6).
func shown(v document.Value) string {
	if s, ok := v.(document.String); ok {
		return string(s)
	}
	return text(v)
}
