package asl

// Failure is a state's error: a StateError, which Retry and Catch see, or a
// Terminal, which ends the execution at once (stepfunctions-asl.md 15.2
// item 9).
type Failure interface{ failure() }

// StateError is a catchable error: Name is matched by ErrorEquals.
type StateError struct{ Name, Cause string }

// Terminal is an error Retry and Catch never see: States.Runtime, and
// States.DataLimitExceeded from a state that is not a Task.
type Terminal struct{ Name, Cause string }

func (StateError) failure() {}
func (Terminal) failure()   {}

// ErrorRule is the error a fidelity switch chose for a situation, and
// whether it is terminal.
type ErrorRule struct {
	Name     string
	Terminal bool
}

// Rules are the engine-level fidelity switch values. The service builds them
// from its switches; sfn.integration.lambda-error and sfn.denial.status
// apply in the service, not here.
type Rules struct {
	NoMatch          ErrorRule // sfn.choice.no-match
	MissingVariable  ErrorRule // sfn.choice.missing-variable
	IntrinsicFailure ErrorRule // sfn.intrinsic.failure-error
	AbortOnDelete    bool      // sfn.delete.running-execution
}
