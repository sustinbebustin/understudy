package asl

import (
	"slices"
	"strconv"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/intrinsic"
)

// maxHistoryEvents is the history quota of a Standard execution
// (stepfunctions-asl.md 11.1, aws-doc dg/service-quotas).
const maxHistoryEvents = 25_000

// errRuntime is the name of the terminal runtime error
// (stepfunctions-asl.md 8.1).
const errRuntime = "States.Runtime"

// errDataLimit is the error of state data over maxDataBytes
// (stepfunctions-asl.md 8.5).
const errDataLimit = "States.DataLimitExceeded"

// maxDataBytes is the UTF-8 size limit on state input and output
// (stepfunctions-asl.md 12, aws-doc dg/service-quotas).
const maxDataBytes = 262_144

// Step applies ev to x and returns the new execution, the history events it
// wrote (ids continuing from x's last) and the effects, in order. It is
// pure: equal arguments give equal results. An event that does not apply (a
// timer or task x does not know, anything after the outcome) returns x
// unchanged with no events or effects.
//
// LambdaSucceeded and LambdaFailed apply only to a task in flight: a late
// result after the task timed out is dropped (stepfunctions-asl.md 9.1).
// MachineDeleting writes no event and asks for nothing: under
// Rules.AbortOnDelete it marks the execution, which ends ABORTED at its
// next transition instead of taking it.
func Step(d *Definition, rules Rules, src intrinsic.Source, x Execution, ev Event) (Execution, []HistoryEvent, []Effect) {
	if _, done := x.Outcome(); done {
		return x, nil, nil
	}
	if _, isStart := ev.(Start); isStart == x.started {
		// Start applies only before the execution started, everything
		// else only after.
		return x, nil, nil
	}
	s := stepper{d: d, rules: rules, src: src, x: x}
	s.x.timers = slices.Clone(x.timers)
	s.x.tasks = slices.Clone(x.tasks)
	switch ev := ev.(type) {
	case Start:
		s.start(ev)
	case TimerFired:
		i := slices.IndexFunc(x.timers, func(t armed) bool { return t.id == ev.Timer })
		if i < 0 {
			return x, nil, nil
		}
		fired := s.x.timers[i]
		s.x.timers = slices.Delete(s.x.timers, i, i+1)
		s.at = ev.At
		if _, deadline := fired.job.(deadlineJob); s.x.deleting && !deadline {
			s.abort("", "")
			break
		}
		s.run(fired.job)
	case LambdaSucceeded:
		t, ok := s.takeTask(ev.Task)
		if !ok {
			return x, nil, nil
		}
		s.at = ev.At
		if s.x.deleting {
			s.abort("", "")
			break
		}
		s.lambdaSucceeded(t, ev.Output)
	case LambdaFailed:
		t, ok := s.takeTask(ev.Task)
		if !ok {
			return x, nil, nil
		}
		s.at = ev.At
		if s.x.deleting {
			s.abort("", "")
			break
		}
		s.taskError(t, "Failed", ev.Err)
	case Stop:
		s.at = ev.At
		s.abort(ev.Error, ev.Cause)
	case MachineDeleting:
		if !rules.AbortOnDelete || x.deleting {
			return x, nil, nil
		}
		s.x.deleting = true
	default:
		return x, nil, nil
	}
	return s.x, s.events, s.effects()
}

// stepper is one Step in progress: the execution it changes, the instant
// its events carry, and what it writes.
type stepper struct {
	d       *Definition
	rules   Rules
	src     intrinsic.Source
	x       Execution
	at      time.Time
	events  []HistoryEvent
	cancels []Effect
	allocs  []Effect
	finish  []Effect
}

// effects are the step's effects in contract order: cancellations, then
// timers and invocations in allocation order, then Finish.
func (s *stepper) effects() []Effect {
	return slices.Concat(s.cancels, s.allocs, s.finish)
}

// emit writes one history event and returns its id.
func (s *stepper) emit(typ string, previous int64, details document.Value) int64 {
	s.x.lastEvent++
	s.events = append(s.events, HistoryEvent{ID: s.x.lastEvent, PreviousEventID: previous, Type: typ, Timestamp: s.at, Details: details})
	return s.x.lastEvent
}

// arm arms a timer for j at at.
func (s *stepper) arm(at time.Time, j job) {
	s.x.lastTimer++
	s.x.timers = append(s.x.timers, armed{id: s.x.lastTimer, at: at, job: j})
	s.allocs = append(s.allocs, ArmTimer{Timer: s.x.lastTimer, At: at})
}

// end records the outcome, withdraws every armed timer and finishes.
func (s *stepper) end(o Outcome) {
	for _, t := range s.x.timers {
		s.cancels = append(s.cancels, CancelTimer{Timer: t.id})
	}
	s.x.timers = nil
	s.x.tasks = nil
	s.x.outcome = o
	s.finish = []Effect{Finish{Outcome: o}}
}

func (s *stepper) start(ev Start) {
	s.at = ev.At
	s.x.started = true
	s.x.context = ev.Context
	input := ev.Input
	if input == nil {
		input = document.Null{}
	}
	details := document.Map{
		{Key: "input", Value: document.String(text(input))},
		{Key: "inputDetails", Value: untruncated()},
	}
	if role, ok := at(at(ev.Context, "Execution"), "RoleArn").(document.String); ok {
		details = append(details, document.Entry{Key: "roleArn", Value: role})
	}
	s.emit("ExecutionStarted", 0, details)
	s.arm(ev.At, stepJob{state: s.d.startAt, input: input, previous: 0})
	s.arm(ev.At.Add(s.d.limit()), deadlineJob{})
}

// run does what a fired timer's job asks.
func (s *stepper) run(j job) {
	switch j := j.(type) {
	case stepJob:
		s.step(j)
	case waitJob:
		s.waitExit(j)
	case deadlineJob:
		s.timedOut()
	case taskTimeoutJob:
		// The timer is the task's own, so the task is in flight.
		if t, ok := s.takeTask(j.task); ok {
			s.taskError(t, "TimedOut", StateError{Name: errTimeout})
		}
	}
}

// maxExecution is the longest a Standard execution runs: one year
// (stepfunctions-asl.md 3.1, aws-doc dg/service-quotas "Maximum execution
// time"). AWS's length of that year is unrecorded; understudy takes 365
// days (inferred, ASL-09).
const maxExecution = 365 * 24 * time.Hour

// limit is how long an execution of d may run: its TimeoutSeconds, at most
// maxExecution.
func (d *Definition) limit() time.Duration {
	if d.timeoutSeconds > 0 && time.Duration(d.timeoutSeconds)*time.Second < maxExecution {
		return time.Duration(d.timeoutSeconds) * time.Second
	}
	return maxExecution
}

// timedOut ends the execution TIMED_OUT at its deadline. DescribeExecution
// then has no error or cause (stepfunctions-asl.md 8.6; aws-recorded
// localstack@8b9a79f v2/timeouts/test_timeouts.snapshot.json:L688-L708), so
// ExecutionTimedOut's details are {}. Its previousEventId is 0, as
// ExecutionAborted's from StopExecution is, and a running Wait writes no
// WaitStateAborted (UNVERIFIED, ASL-12).
func (s *stepper) timedOut() {
	s.emit("ExecutionTimedOut", 0, errorDetails("", ""))
	s.end(TimedOut{})
}

// abort ends the execution ABORTED: ExecutionAborted has previousEventId 0
// and {error?, cause?} (stepfunctions-asl.md 11.2 rule 10, 11.3).
func (s *stepper) abort(name, cause string) {
	s.emit("ExecutionAborted", 0, errorDetails(name, cause))
	s.end(Aborted{Error: name, Cause: cause})
}

// step runs one state transition (contract 5.5 item 2).
func (s *stepper) step(j stepJob) {
	// A state writes at most its entered and exited events before the
	// execution's last event; reaching the quota fails the execution
	// (inferred, ASL-16).
	if s.x.lastEvent+4 > maxHistoryEvents {
		s.fail(errRuntime, "The execution reached the limit of "+strconv.Itoa(maxHistoryEvents)+" history events.", s.x.lastEvent)
		return
	}
	switch st := s.d.states[j.state].(type) {
	case passState:
		entered := s.emit("PassStateEntered", j.previous, stateEntered(st.name, j.input))
		output, f := s.pass(st, j.input)
		if f == nil {
			f = dataLimit(st.name, output)
		}
		if f != nil {
			s.stateFailed(st.name, entered, f)
			return
		}
		exited := s.emit("PassStateExited", entered, stateExited(st.name, output))
		s.transition(st.next, output, exited)
	case succeedState:
		entered := s.emit("SucceedStateEntered", j.previous, stateEntered(st.name, j.input))
		output, f := s.passThrough(st.name, st.io, j.input)
		if f == nil {
			f = dataLimit(st.name, output)
		}
		if f != nil {
			s.stateFailed(st.name, entered, f)
			return
		}
		exited := s.emit("SucceedStateExited", entered, stateExited(st.name, output))
		s.succeed(output, exited)
	case failState:
		entered := s.emit("FailStateEntered", j.previous, stateEntered(st.name, j.input))
		name, cause, f := s.failMessages(st, j.input)
		if f != nil {
			s.stateFailed(st.name, entered, f)
			return
		}
		s.fail(name, cause, entered)
	case choiceState:
		s.choice(st, j)
	case waitState:
		s.wait(st, j)
	case taskState:
		s.task(st, j)
	case parallelState, mapState:
		// Parse accepts these types only under features whose runtime has
		// not landed, which the service does not accept yet.
		s.fail(errRuntime, "The state '"+j.state+"' is of a type this engine does not run yet.", j.previous)
	case nil:
		// Parse admits no transition to a missing state; a definition that
		// lacks one was changed under a running execution.
		s.fail(errRuntime, "The state '"+j.state+"' is not in the definition the execution runs.", j.previous)
	}
}

// transition moves to next on output, or ends the execution when next is
// "".
func (s *stepper) transition(next string, output document.Value, previous int64) {
	if next == "" {
		s.succeed(output, previous)
		return
	}
	s.arm(s.at, stepJob{state: next, input: output, previous: previous})
}

func (s *stepper) succeed(output document.Value, previous int64) {
	s.emit("ExecutionSucceeded", previous, document.Map{
		{Key: "output", Value: document.String(text(output))},
		{Key: "outputDetails", Value: untruncated()},
	})
	s.end(Succeeded{Output: output})
}

// fail ends the execution FAILED; an empty error or cause leaves its key
// out (stepfunctions-asl.md 11.3).
func (s *stepper) fail(name, cause string, previous int64) {
	s.emit("ExecutionFailed", previous, errorDetails(name, cause))
	s.end(Failed{Error: name, Cause: cause})
}

// errorDetails are {error?, cause?}.
func errorDetails(name, cause string) document.Map {
	m := document.Map{}
	if name != "" {
		m = append(m, document.Entry{Key: "error", Value: document.String(name)})
	}
	if cause != "" {
		m = append(m, document.Entry{Key: "cause", Value: document.String(cause)})
	}
	return m
}

func stateEntered(name string, input document.Value) document.Map {
	return document.Map{
		{Key: "name", Value: document.String(name)},
		{Key: "input", Value: document.String(text(input))},
		{Key: "inputDetails", Value: untruncated()},
	}
}

func stateExited(name string, output document.Value) document.Map {
	return document.Map{
		{Key: "name", Value: document.String(name)},
		{Key: "output", Value: document.String(text(output))},
		{Key: "outputDetails", Value: untruncated()},
	}
}

// untruncated is a HistoryEventExecutionDataDetails of data shown whole.
func untruncated() document.Map {
	return document.Map{{Key: "truncated", Value: document.Bool(false)}}
}

// at reads the member key of v; nil when v is not an object or lacks it.
func at(v document.Value, key string) document.Value {
	m, ok := v.(document.Map)
	if !ok {
		return nil
	}
	out, _ := member(m, key)
	return out
}

// text is the compact JSON text of v, members in document order; nil is
// null.
func text(v document.Value) string {
	if v == nil {
		v = document.Null{}
	}
	e := jsonx.NewEncoder(jsonx.WireOrder)
	e.Value(v)
	b, err := e.Bytes()
	if err != nil {
		// Encoder.Value fails only for a nil value, which is null here.
		return "null"
	}
	return string(b)
}
