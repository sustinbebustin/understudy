package asl

import (
	"errors"
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl/intrinsic"
)

// errNotImplemented is what the engine's unbuilt parts return.
var errNotImplemented = errors.New("asl: not implemented yet")

// Execution is one execution's interpreter state: an opaque value, zero
// before Start, changed only by Step. It persists through jsonx with
// MarshalJSON and UnmarshalJSON.
type Execution struct {
	_ struct{}
}

// Outcome reports how the execution ended; false while it runs or before
// Start.
//
// Not implemented yet: it reports a running execution.
func (x Execution) Outcome() (Outcome, bool) {
	_ = x
	return nil, false
}

// Timers lists the timers x has armed and not yet seen fire or cancelled,
// in TimerID order: what the service re-arms after a snapshot load, since
// timers are never saved (ADR 0009).
//
// Not implemented yet: it lists none.
func (x Execution) Timers() []ArmTimer {
	_ = x
	return nil
}

// InFlight lists the Lambda tasks x is waiting on, in TaskID order: what the
// service invokes again after a snapshot load (the StartExecution coverage
// note).
//
// Not implemented yet: it lists none.
func (x Execution) InFlight() []InvokeLambda {
	_ = x
	return nil
}

// MarshalJSON writes x in the jsonx tagged-union convention
// (ARCHITECTURE.md 13).
//
// Not implemented yet: it fails.
func (x Execution) MarshalJSON() ([]byte, error) {
	_ = x
	return nil, errNotImplemented
}

// UnmarshalJSON reads what MarshalJSON wrote.
//
// Not implemented yet: it fails.
func (x *Execution) UnmarshalJSON(b []byte) error {
	_, _ = x, b
	return errNotImplemented
}

// Outcome is how an execution ended: Succeeded, Failed, TimedOut or Aborted.
type Outcome interface{ outcome() }

// Succeeded is a SUCCEEDED execution.
type Succeeded struct{ Output document.Value }

// Failed is a FAILED execution.
type Failed struct{ Error, Cause string }

// TimedOut is a TIMED_OUT execution.
type TimedOut struct{ Error, Cause string }

// Aborted is an ABORTED execution.
type Aborted struct{ Error, Cause string }

func (Succeeded) outcome() {}
func (Failed) outcome()    {}
func (TimedOut) outcome()  {}
func (Aborted) outcome()   {}

// TaskID names one Lambda invocation of an execution; ids increase in the
// order the engine asks for invocations.
type TaskID int64

// TimerID names one timer of an execution; ids increase in the order the
// engine arms timers.
type TimerID int64

// Event is what Step consumes: Start, TimerFired, LambdaSucceeded,
// LambdaFailed, Stop or MachineDeleting.
type Event interface{ event() }

// Start begins an execution. Context holds the stepfunctions-asl.md 4.7
// Execution and StateMachine members; the engine adds State, Map and the
// rest.
type Start struct {
	At             time.Time
	Input, Context document.Value
}

// TimerFired reports that a timer the engine armed is due.
type TimerFired struct {
	At    time.Time
	Timer TimerID
}

// LambdaSucceeded is an invocation's result: the stepfunctions-api.md 9.2
// result for lambda:invoke, the function's payload for a function ARN.
type LambdaSucceeded struct {
	At     time.Time
	Task   TaskID
	Output document.Value
}

// LambdaFailed is an invocation's failure, named by the service
// (stepfunctions-asl.md 8.1, switch sfn.integration.lambda-error).
type LambdaFailed struct {
	At   time.Time
	Task TaskID
	Err  StateError
}

// Stop is StopExecution; "" is an absent error or cause
// (stepfunctions-asl.md 11.3).
type Stop struct {
	At           time.Time
	Error, Cause string
}

// MachineDeleting reports DeleteStateMachine of the execution's machine
// (switch sfn.delete.running-execution).
type MachineDeleting struct{ At time.Time }

func (Start) event()           {}
func (TimerFired) event()      {}
func (LambdaSucceeded) event() {}
func (LambdaFailed) event()    {}
func (Stop) event()            {}
func (MachineDeleting) event() {}

// HistoryEvent is one GetExecutionHistory event (stepfunctions-asl.md 11).
type HistoryEvent struct {
	ID, PreviousEventID int64
	Type                string // a com.amazonaws.sfn#HistoryEventType value
	Timestamp           time.Time
	// Details is the <type>EventDetails member as a wire-named document.Map,
	// nil for a type that carries none (stepfunctions-asl.md 11.3).
	Details document.Value
}

// Effect is what Step asks of the service: InvokeLambda, ArmTimer,
// CancelTimer or Finish.
type Effect interface{ effect() }

// ResourceKind is how a Task names its Lambda function.
type ResourceKind int

// Resource kinds.
const (
	// FunctionARN: the function ARN is the Task's Resource (the legacy form).
	FunctionARN ResourceKind = iota + 1
	// LambdaInvoke: arn:aws:states:::lambda:invoke with FunctionName in
	// Parameters.
	LambdaInvoke
)

// InvokeLambda asks the service to invoke a function synchronously
// (RequestResponse) and answer with LambdaSucceeded or LambdaFailed for
// Task.
type InvokeLambda struct {
	Task     TaskID
	Resource ResourceKind
	Function string // the function name or ARN, qualifier included when given
	Payload  document.Value
	// Timeout is the Task's TimeoutSeconds (or TimeoutSecondsPath value);
	// the engine enforces it with its own timer.
	Timeout time.Duration
}

// ArmTimer asks the service to fire Timer at At.
type ArmTimer struct {
	Timer TimerID
	At    time.Time
}

// CancelTimer withdraws a timer armed before.
type CancelTimer struct{ Timer TimerID }

// Finish reports the execution's end; it is the last effect of the Step
// that ends the execution.
type Finish struct{ Outcome Outcome }

func (InvokeLambda) effect() {}
func (ArmTimer) effect()     {}
func (CancelTimer) effect()  {}
func (Finish) effect()       {}

// Step applies ev to x and returns the new execution, the history events it
// wrote (ids continuing from x's last) and the effects, in order. It is
// pure: equal arguments give equal results. An event that does not apply (a
// timer or task x does not know, anything after the outcome) returns x
// unchanged with no events or effects.
//
// Not implemented yet: no event applies.
func Step(d *Definition, rules Rules, src intrinsic.Source, x Execution, ev Event) (Execution, []HistoryEvent, []Effect) {
	_, _, _, _ = d, rules, src, ev
	return x, nil, nil
}
