package sfn

import (
	"fmt"
	"net/http"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
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

// sfnUnreplayed is why no scenario settles a switch whose recording exists:
// the step that shows it is a DescribeExecution or GetExecutionHistory
// answer, whose body AWS writes with nested __type members and members in
// alphabetical order, which understudy's serializer does not reproduce, so
// the step cannot match. recorded names the fixtures, with "is" or "are".
func sfnUnreplayed(recorded string) string {
	return recorded + " recorded 2026-11-03; not replayed: AWS's execution bodies carry nested __type members and alphabetical member order"
}

// ChoiceNoMatch is the error of a Choice state no rule matches and that has
// no Default: States.Runtime, uncatchable (ASL-08, recorded 2026-11-03;
// stepfunctions-asl.md 6.4).
func ChoiceNoMatch() fidelity.Switch[asl.ErrorRule] {
	return fidelity.Switch[asl.ErrorRule]{
		Name:    "sfn.choice.no-match",
		Value:   asl.ErrorRule{Name: "States.Runtime", Terminal: true},
		Label:   evidence.AWSRecorded,
		Ref:     "docs/research/stepfunctions-asl.md#64-no-match-and-missing-variables",
		Record:  "ASL-08",
		Ops:     []string{"sfn.StartExecution"},
		Settles: fidelity.NoScenario{Reason: sfnUnreplayed("sfn/ASL-08/01-no-match-history.json is")},
	}
}

// ChoiceMissingVariable is the error of a Choice rule whose Variable path
// selects nothing: States.Runtime, uncatchable, its cause carrying the 4.6
// prefix (ASL-08, recorded 2026-11-03; stepfunctions-asl.md 6.4).
func ChoiceMissingVariable() fidelity.Switch[asl.ErrorRule] {
	return fidelity.Switch[asl.ErrorRule]{
		Name:    "sfn.choice.missing-variable",
		Value:   asl.ErrorRule{Name: "States.Runtime", Terminal: true},
		Label:   evidence.AWSRecorded,
		Ref:     "docs/research/stepfunctions-asl.md#64-no-match-and-missing-variables",
		Record:  "ASL-08",
		Ops:     []string{"sfn.StartExecution"},
		Settles: fidelity.NoScenario{Reason: sfnUnreplayed("sfn/ASL-08/02-missing-history.json is")},
	}
}

// IntrinsicFailure is the error of an intrinsic function failing at run
// time: States.Runtime, uncatchable, not the spec's States.IntrinsicFailure
// (ASL-05, recorded 2026-11-03; stepfunctions-asl.md 5.4).
func IntrinsicFailure() fidelity.Switch[asl.ErrorRule] {
	return fidelity.Switch[asl.ErrorRule]{
		Name:    "sfn.intrinsic.failure-error",
		Value:   asl.ErrorRule{Name: "States.Runtime", Terminal: true},
		Label:   evidence.AWSRecorded,
		Ref:     "docs/research/stepfunctions-asl.md#54-failures",
		Record:  "ASL-05",
		Ops:     []string{"sfn.StartExecution"},
		Settles: fidelity.NoScenario{Reason: sfnUnreplayed("sfn/ASL-05/02-history.json is")},
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
func LambdaTaskError(n LambdaErrorNaming, e *awserr.Error, requestID string) asl.StateError {
	name := n.Unmodeled
	// Every error shape Lambda's model declares shares one namespace,
	// com.amazonaws.lambda; generic errors carry another. Lambda's denial,
	// com.amazonaws.lambda#AccessDeniedException, is the overlay's: the
	// model declares no such shape (codegen/overlays/lambda.json;
	// iam-enforcement-surfaces.md 3.1).
	lambda := lambdaapi.Service()
	if e.ID.Shape.Namespace() == lambdaapi.IdentityServiceException().Shape.Namespace() && e.ID.Shape != lambda.Denial.Identity.Shape {
		name = n.Prefix + e.ID.Shape.Name()
	}
	return asl.StateError{Name: name, Cause: fmt.Sprintf(n.Cause, e.Message, e.ID.Status, e.ID.QueryCode, requestID)}
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
