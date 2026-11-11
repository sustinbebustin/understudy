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
	// deleting is set by MachineDeleting under Rules.EndOnDelete: the next
	// transition fails the execution.
	deleting bool
	// lastTask is the id of the last Lambda invocation asked for.
	lastTask TaskID
	// tasks are the Lambda invocations awaiting a result, in TaskID order.
	tasks []inflight
	// jitterSeed is Start's Jitter; jitterDraws counts the FULL jitter
	// draws made so far.
	jitterSeed, jitterDraws uint64
	// lastRun is the id of the last Parallel or Map run started.
	lastRun int64
	// runs are the running Parallel and Map states, in id order.
	runs []run
}

// frame is the scope a job runs in: the top level (the zero frame) or one
// branch or iteration of a running Parallel or Map state.
type frame struct {
	// run is the run's id; 0 at the top level.
	run int64
	// index is the branch or item index within the run.
	index int
}

// run is one running Parallel or inline Map state (stepfunctions-asl.md
// 9.1, 9.2): its visit, and its branches or iterations.
type run struct {
	id int64
	visit
	// inputs are the inputs of the branches or iterations, in index order.
	inputs []document.Value
	// results hold the output of each branch or iteration that finished;
	// nil for one still running or not yet started.
	results []document.Value
	// launched counts the branches or iterations started, running those
	// started and not finished, finished those finished; limit is how many
	// may run at once.
	launched, running, finished, limit int
}

// visit is one visit of a Task, Parallel or Map state, across its retries.
type visit struct {
	// fr is the frame the state runs in.
	fr frame
	// state is the state's name; input its raw input, which ResultPath
	// and a catcher's ResultPath place into.
	state string
	input document.Value
	// enteredAt is when the state was entered: $$.State.EnteredTime.
	enteredAt time.Time
	// entered is the id of its TaskStateEntered event.
	entered int64
	// attempts holds, per retrier of the state, the retries it has made in
	// this visit (stepfunctions-asl.md 8.2); nil before the first retry.
	attempts []int
}

// retries is $$.State.RetryCount: the retries of the visit so far, over
// all its retriers.
func (v visit) retries() int {
	n := 0
	for _, a := range v.attempts {
		n += a
	}
	return n
}

// inflight is one Task state waiting on its Lambda invocation.
type inflight struct {
	invoke InvokeLambda
	visit
	// started is the id of its LambdaFunctionStarted or TaskStarted event.
	started int64
	// timer is the task timeout's timer.
	timer TimerID
}

// armed is one armed timer and what it does when it fires.
type armed struct {
	id  TimerID
	at  time.Time
	job job
}

// job is what a timer does: stepJob, waitJob, deadlineJob, taskTimeoutJob,
// retryJob or runRetryJob.
type job interface{ isJob() }

// stepJob runs one state in frame fr: the state named state on input, its
// first event pointing at previous (stepfunctions-asl.md 11.2 rules 2 and
// 4).
type stepJob struct {
	fr       frame
	state    string
	input    document.Value
	previous int64
}

// waitJob ends the Wait state named state in frame fr: it writes
// WaitStateExited pointing at entered with output, then moves on.
type waitJob struct {
	fr      frame
	state   string
	output  document.Value
	entered int64
}

// deadlineJob ends the execution TIMED_OUT: its TimeoutSeconds or the
// one-year limit has passed.
type deadlineJob struct{}

// taskTimeoutJob fails the Task waiting on task with States.Timeout: its
// TimeoutSeconds has passed (stepfunctions-asl.md 8.6).
type taskTimeoutJob struct{ task TaskID }

// retryJob runs the Task of v again after a retrier's delay; its first
// event points at previous, the failure retried (stepfunctions-asl.md 11.2
// rule 7).
type retryJob struct {
	visit
	previous int64
}

// runRetryJob starts the Parallel or Map state of v again after a
// retrier's delay, every branch or iteration anew (stepfunctions-asl.md
// 8.2); its *StateStarted event points at previous, the failure retried
// (aws-recorded localstack@8b9a79f
// v2/scenarios/test_base_scenarios.snapshot.json:L1484).
type runRetryJob struct {
	visit
	previous int64
}

func (stepJob) isJob()        {}
func (waitJob) isJob()        {}
func (deadlineJob) isJob()    {}
func (taskTimeoutJob) isJob() {}
func (retryJob) isJob()       {}
func (runRetryJob) isJob()    {}

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
// note).
func (x Execution) InFlight() []InvokeLambda {
	out := make([]InvokeLambda, 0, len(x.tasks))
	for i := range x.tasks {
		out = append(out, x.tasks[i].invoke)
	}
	return out
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
// rest. Jitter seeds the execution's FULL retry jitter draws (the service
// draws it from the ids jitter stream).
type Start struct {
	At             time.Time
	Input, Context document.Value
	Jitter         uint64
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

// Invocation is how a Task invokes its function: Lambda's InvocationType.
type Invocation int

// Invocations.
const (
	// SyncInvocation (RequestResponse) waits for the function's result. It
	// is the zero value: a function ARN Task and a lambda:invoke Task
	// without InvocationType invoke so.
	SyncInvocation Invocation = iota
	// AsyncInvocation (Event) hands the payload to Lambda's asynchronous
	// invoke; Lambda's acceptance is the result (SFN-13).
	AsyncInvocation
)

// InvokeLambda asks the service to invoke a function as Invocation says
// and answer with LambdaSucceeded or LambdaFailed for Task.
type InvokeLambda struct {
	Task       TaskID
	Resource   ResourceKind
	Invocation Invocation
	Function   string // the function name or ARN, qualifier included when given
	Payload    document.Value
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
