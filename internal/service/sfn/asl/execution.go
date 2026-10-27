package asl

import (
	"time"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// Execution is one execution's interpreter state: an opaque value, zero
// before Start, changed only by Step. It persists through jsonx with
// MarshalJSON and UnmarshalJSON.
type Execution struct {
	started bool
	// lastEvent is the id of the last history event written; 0 before
	// Start.
	lastEvent int64
	// lastTimer is the id of the last timer armed.
	lastTimer TimerID
	// timers are the armed timers, in TimerID order.
	timers []armed
	// context is Start's context: the stepfunctions-asl.md 4.7 Execution
	// and StateMachine members.
	context document.Value
	// outcome is nil while the execution runs.
	outcome Outcome
}

// armed is one armed timer and what it does when it fires.
type armed struct {
	id  TimerID
	at  time.Time
	job job
}

// job is what a timer does: stepJob.
type job interface{ isJob() }

// stepJob runs one state: the state named state on input, its first event
// pointing at previous (stepfunctions-asl.md 11.2 rules 2 and 4).
type stepJob struct {
	state    string
	input    document.Value
	previous int64
}

func (stepJob) isJob() {}

// Outcome reports how the execution ended; false while it runs or before
// Start.
func (x Execution) Outcome() (Outcome, bool) {
	return x.outcome, x.outcome != nil
}

// Timers lists the timers x has armed and not yet seen fire or cancelled,
// in TimerID order: what the service re-arms after a snapshot load, since
// timers are never saved (ADR 0009).
func (x Execution) Timers() []ArmTimer {
	out := make([]ArmTimer, 0, len(x.timers))
	for _, t := range x.timers {
		out = append(out, ArmTimer{Timer: t.id, At: t.at})
	}
	return out
}

// InFlight lists the Lambda tasks x is waiting on, in TaskID order: what the
// service invokes again after a snapshot load (the StartExecution coverage
// note). No built state invokes Lambda yet, so it lists none.
func (x Execution) InFlight() []InvokeLambda {
	_ = x
	return nil
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
