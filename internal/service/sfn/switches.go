package sfn

import (
	"net/http"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// boundOps are the operations item 7 binds (stepfunctions-api.md 13.1, Item
// 7 minimum); sfn.denial.status governs every one.
func boundOps() []string {
	return []string{
		"sfn.CreateStateMachine", "sfn.UpdateStateMachine", "sfn.ValidateStateMachineDefinition",
		"sfn.ListStateMachineVersions", "sfn.DescribeStateMachine", "sfn.DeleteStateMachine",
		"sfn.ListStateMachines", "sfn.StartExecution", "sfn.StopExecution", "sfn.ListExecutions",
		"sfn.GetExecutionHistory", "sfn.DescribeExecution", "sfn.DescribeStateMachineForExecution",
		"sfn.TagResource", "sfn.UntagResource", "sfn.ListTagsForResource",
	}
}

// ChoiceNoMatch is the error of a Choice state no rule matches and that has
// no Default (stepfunctions-asl.md 6.4).
func ChoiceNoMatch() fidelity.Switch[asl.ErrorRule] {
	return fidelity.Switch[asl.ErrorRule]{
		Name:    "sfn.choice.no-match",
		Value:   asl.ErrorRule{Name: "States.NoChoiceMatched", Terminal: false},
		Label:   evidence.Inferred,
		Ref:     "docs/research/stepfunctions-asl.md#64-no-match-and-missing-variables",
		Record:  "ASL-08",
		Ops:     []string{"sfn.StartExecution"},
		Settles: fidelity.NoScenario{Reason: "unrecorded: recording case ASL-08 (stepfunctions-asl.md 16.2)"},
	}
}

// ChoiceMissingVariable is the error of a Choice rule whose Variable path
// selects nothing (stepfunctions-asl.md 6.4); its cause carries the 4.6
// prefix.
func ChoiceMissingVariable() fidelity.Switch[asl.ErrorRule] {
	return fidelity.Switch[asl.ErrorRule]{
		Name:    "sfn.choice.missing-variable",
		Value:   asl.ErrorRule{Name: "States.Runtime", Terminal: true},
		Label:   evidence.Inferred,
		Ref:     "docs/research/stepfunctions-asl.md#64-no-match-and-missing-variables",
		Record:  "ASL-08",
		Ops:     []string{"sfn.StartExecution"},
		Settles: fidelity.NoScenario{Reason: "unrecorded: recording case ASL-08 (stepfunctions-asl.md 16.2)"},
	}
}

// IntrinsicFailure is the error of an intrinsic function failing at run time
// (stepfunctions-asl.md 5.4).
func IntrinsicFailure() fidelity.Switch[asl.ErrorRule] {
	return fidelity.Switch[asl.ErrorRule]{
		Name:    "sfn.intrinsic.failure-error",
		Value:   asl.ErrorRule{Name: "States.IntrinsicFailure", Terminal: false},
		Label:   evidence.Inferred,
		Ref:     "docs/research/stepfunctions-asl.md#54-failures",
		Record:  "ASL-05",
		Ops:     []string{"sfn.StartExecution"},
		Settles: fidelity.NoScenario{Reason: "unrecorded: recording case ASL-05 (stepfunctions-asl.md 16.2)"},
	}
}

// LambdaErrorNaming is how a Lambda service error becomes a task error
// (stepfunctions-api.md 9.4).
type LambdaErrorNaming struct {
	// Prefix goes before the shape name of an error Lambda's model declares.
	Prefix string
	// Unmodeled names every error Lambda's model does not declare.
	Unmodeled string
	// Cause is the fmt format of the cause, from the message, the status,
	// the error code and the request id.
	Cause string
}

// LambdaError names a Lambda service error a task meets
// (stepfunctions-api.md 9.4 row 4 recorded; the unmodeled rule follows rows
// 2-3).
func LambdaError() fidelity.Switch[LambdaErrorNaming] {
	return fidelity.Switch[LambdaErrorNaming]{
		Name: "sfn.integration.lambda-error",
		Value: LambdaErrorNaming{
			Prefix:    "Lambda.",
			Unmodeled: "Lambda.AWSLambdaException",
			Cause:     "%s (Service: AWSLambda; Status Code: %d; Error Code: %s; Request ID: %s; Proxy: null)",
		},
		Label:   evidence.Inferred,
		Ref:     "docs/research/stepfunctions-api.md#94-error-names-and-causes-recorded",
		Record:  "SFN-15",
		Ops:     []string{"sfn.StartExecution"},
		Settles: fidelity.NoScenario{Reason: "unrecorded for an unmodeled code: recording case SFN-15 (stepfunctions-api.md 14.16)"},
	}
}

// LambdaTaskError is the StateError for e, a Lambda service error a task's
// invocation returned with request id requestID, named by n: Prefix plus the
// shape name when e's shape is in Lambda's model namespace, else Unmodeled;
// the cause is Cause formatted with e's message, status, code and
// requestID.
//
// Not implemented yet: it returns the zero StateError.
func LambdaTaskError(n LambdaErrorNaming, e *awserr.Error, requestID string) asl.StateError {
	_, _, _ = n, e, requestID
	return asl.StateError{}
}

// DeleteRunningExecution says what DeleteStateMachine does to running
// executions: true keeps the machine DELETING and ends each running
// execution ABORTED at its next transition; an idle machine is removed at
// once either way (stepfunctions-api.md 5.4).
func DeleteRunningExecution() fidelity.Switch[bool] {
	return fidelity.Switch[bool]{
		Name:    "sfn.delete.running-execution",
		Value:   true,
		Label:   evidence.Inferred,
		Ref:     "docs/research/stepfunctions-api.md#54-deletestatemachine",
		Record:  "SFN-06",
		Ops:     []string{"sfn.StartExecution", "sfn.DeleteStateMachine"},
		Settles: fidelity.NoScenario{Reason: "unrecorded: recording case SFN-06 (stepfunctions-api.md 14.7)"},
	}
}

// DenialStatus is the HTTP status of SFN's AccessDeniedException
// (iam-enforcement-surfaces.md 12.3).
func DenialStatus() fidelity.Switch[int] {
	return fidelity.Switch[int]{
		Name:    "sfn.denial.status",
		Value:   http.StatusBadRequest,
		Label:   evidence.Inferred,
		Ref:     "docs/research/iam-enforcement-surfaces.md#123-error-identities-to-emit",
		Record:  "SFN-15",
		Ops:     boundOps(),
		Settles: fidelity.NoScenario{Reason: "unrecorded: recording case SFN-15 (stepfunctions-api.md 14.16)"},
	}
}

// EngineRules are the engine-level switch values.
func EngineRules() asl.Rules {
	return asl.Rules{
		NoMatch:          ChoiceNoMatch().Value,
		MissingVariable:  ChoiceMissingVariable().Value,
		IntrinsicFailure: IntrinsicFailure().Value,
		AbortOnDelete:    DeleteRunningExecution().Value,
	}
}

// Switches lists the behaviors chosen without AWS-recorded evidence.
func (*Service) Switches() []fidelity.Entry {
	return []fidelity.Entry{
		ChoiceNoMatch().Entry(), ChoiceMissingVariable().Entry(), IntrinsicFailure().Entry(),
		LambdaError().Entry(), DeleteRunningExecution().Entry(), DenialStatus().Entry(),
	}
}
