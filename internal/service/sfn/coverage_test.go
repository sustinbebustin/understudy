package sfn_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/sfn"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// The coverage marks and exact notes of the Step Functions minimum
// (stepfunctions-asl.md 15.1 and stepfunctions-api.md 13.1, "Step Functions
// minimum"): 16 operations bound, every other one unbound.
var wantCoverage = map[string]registry.BindCoverage{
	"CreateStateMachine":             registry.Partial{Note: "Standard workflows only: type EXPRESS, publish true and a CUSTOMER_MANAGED_KMS_KEY encryptionConfiguration answer 501. Definitions are JSONPath with Pass, Task, Choice, Wait, Succeed, Fail, Parallel and inline Map states, and Task resources are Lambda only (a function ARN or arn:aws:states:::lambda:invoke). These answer InvalidDefinition with code UNSUPPORTED_BY_UNDERSTUDY: JSONata, Assign and variables, Distributed Map and its fields, Credentials, HeartbeatSeconds, callbacks, activities, every other Task resource, a literal lambda:invoke InvocationType other than RequestResponse, and JSONPath multi-clause filters and filter scripts, which AWS accepts (ASL-19). A JSONPath function such as length() is accepted and fails at run time, as AWS fails length() (ASL-19). ToleratedFailurePercentage, which AWS accepts on an inline Map, is refused with Distributed Map's fields (ASL-01). A dynamodb resource outside getItem, putItem, updateItem and deleteItem answers AWS's SCHEMA_VALIDATION_FAILED text. Every problem in a definition is reported in one message, as AWS joins them; wording for problems no recording covers is understudy's (ASL-01). loggingConfiguration and tracingConfiguration are validated, stored and echoed; no log events or traces are emitted. The role ARN is checked for syntax. AWS checks neither the role's existence nor its trust at create; understudy checks its trust policy under iam.enforce=enforce, with understudy's message (SFN-08)"},
	"UpdateStateMachine":             registry.Partial{Note: "The CreateStateMachine definition and configuration rules apply; publish true and versionDescription answer 501. Running executions keep the definition and role they started with"},
	"ValidateStateMachineDefinition": registry.Partial{Note: "Diagnostics use AWS's codes, locations and order; messages are AWS's for the recorded cases (stepfunctions-asl.md 3.4-3.6) and understudy's wording elsewhere (ASL-01). Features understudy refuses are ERROR diagnostics with code UNSUPPORTED_BY_UNDERSTUDY; type EXPRESS answers 501"},
	"ListStateMachineVersions":       registry.Partial{Note: "Always empty: publishing versions is not implemented (publish is refused and PublishStateMachineVersion is unbound)"},
	"StartExecution":                 registry.Partial{Note: "Standard executions. Unrecorded AWS behavior understudy chooses, each with its recording case: malformed input other than the recorded cases answers 'Invalid execution input.' (SFN-08); an input over 262,144 UTF-8 bytes is checked once the other members pass, so it is not listed beside another member's violation (SFN-08); a missing path in a Task or Parallel template fails with States.Runtime (ASL-04); a ResultPath conflict deeper than one step names the node it stops at (ASL-03); a Wait past the one-year execution limit ends with States.Timeout at the limit (ASL-09); a Task or Map leaves a wider margin before the 25,000-event history limit than the recorded Pass and Choice steps (ASL-16); FULL jitter is drawn uniformly from understudy's seeded stream (ASL-10); IsTimestamp follows the documented RFC 3339 profile (ASL-07); a JSONPath function other than length() fails at run time as length() does (ASL-19). Known differences from AWS: a runtime error's cause quotes the state's input as compact JSON, where AWS quotes the input as sent (ASL-04). A lambda:invoke InvocationType that resolves to anything but RequestResponse fails with States.Runtime (SFN-13). lambda:invoke results carry SdkHttpMetadata with AWS's recorded header names; the X-Amzn-Trace-Id value is understudy's. traceHeader, or the X-Amzn-Trace-Id request header when both are sent, is stored and returned by DescribeExecution; no X-Ray trace is emitted. A Lambda task in flight when a snapshot was saved is invoked again after load. States.MathRandom never reproduces AWS's sequence for a seed"},
	"StopExecution":                  registry.Full{},
	"ListExecutions":                 registry.Partial{Note: "mapRunArn and redriveFilter are validated; no Map Run or redrive exists, so they match nothing"},
	"GetExecutionHistory":            registry.Partial{Note: "Events of concurrent Parallel branches and Map iterations are appended in the order understudy runs them, steps due at the same emulator instant in branch or item order, as AWS recorded for the runs of ASL-13; other interleavings are unrecorded. Payload-template output keeps template key order, where AWS writes an order of its own, hash-like (ASL-02)"},
	"DescribeStateMachine":           registry.Full{},
	"DeleteStateMachine":             registry.Partial{Note: "An idle machine is removed at once, and one with running executions once the last of them ends; AWS still describes a deleted machine as DELETING, idle or not, for a time no recording has measured (at least 45 seconds after its last execution ended; SFN-06, ASL-17)"},
	"ListStateMachines":              registry.Full{},
	// Partial, not Full: AWS documents NOT_REDRIVABLE reasons understudy
	// does not give (a RUNNING execution, a DELETING machine, the 14-day
	// redrive period; aws-doc api/API_DescribeExecution
	// redriveStatusReason, dg/redrive-executions; stepfunctions-api.md 6.3).
	"DescribeExecution": registry.Partial{Note: "redriveStatus is NOT_REDRIVABLE for SUCCEEDED executions and for one failed at the history event limit, " +
		"and REDRIVABLE for other FAILED, TIMED_OUT and ABORTED ones, however long ago they closed; a RUNNING execution has none. " +
		"The other NOT_REDRIVABLE reasons AWS documents (a running execution, a DELETING machine, the 14-day redrive period) are not given: none is recorded"},
	"DescribeStateMachineForExecution": registry.Full{},
	"TagResource":                      registry.Full{},
	"UntagResource":                    registry.Full{},
	"ListTagsForResource":              registry.Full{},
}

// The 21 operations that stay unbound and answer 501 (stepfunctions-api.md
// 13.1, Step Functions minimum); nothing is StoredOnly.
var wantUnbound = []string{
	"StartSyncExecution", "SendTaskSuccess", "SendTaskFailure", "SendTaskHeartbeat",
	"CreateActivity", "DescribeActivity", "DeleteActivity", "ListActivities", "GetActivityTask",
	"CreateStateMachineAlias", "DescribeStateMachineAlias", "UpdateStateMachineAlias", "DeleteStateMachineAlias", "ListStateMachineAliases",
	"PublishStateMachineVersion", "DeleteStateMachineVersion", "RedriveExecution", "TestState",
	"DescribeMapRun", "ListMapRuns", "UpdateMapRun",
}

func TestOperationCoverage(t *testing.T) {
	run(t, func(h *harness) {
		if n := len(sfnapi.Service().Operations); n != len(wantCoverage)+len(wantUnbound) {
			h.Fatalf("the model has %d operations, want %d bound plus %d unbound", n, len(wantCoverage), len(wantUnbound))
		}
		for _, op := range sfnapi.Service().Operations {
			status := h.Kernel.Bindings().Status(op)
			cov, bound := wantCoverage[op.ID.Name]
			if !bound {
				if _, unbound := status.(kernel.Unbound); !unbound || !slices.Contains(wantUnbound, op.ID.Name) {
					h.Errorf("%s is %#v, want unbound", op.ID.Name, status)
				}
				continue
			}
			if b, ok := status.(kernel.Bound); !ok || b.Coverage != cov {
				h.Errorf("%s is %#v\nwant bound as %#v", op.ID.Name, status, cov)
			}
		}
		if got := kerneltest.UndeclaredActions(h.Kernel, sfnapi.Service()); len(got) != 0 {
			h.Errorf("operations recording their whole generated action list: %v (05-iam: only the primary action, PassRole and TagResource)", got)
		}
	})
}

// Each unbound operation answers 501 NotImplemented through the pipeline.
// StartSyncExecution is sent without the sync- host prefix, as a client
// with host-prefix injection off does (stepfunctions-api.md 3.3).
func TestUnboundOperationsAnswer501(t *testing.T) {
	run(t, func(h *harness) {
		arn := h.machineArn("m")
		for name, err := range map[string]*awserr.Error{
			"StartSyncExecution":         errOf(call(h, sfnapi.OpStartSyncExecution, &sfnapi.StartSyncExecutionInput{StateMachineArn: arn})),
			"SendTaskSuccess":            errOf(call(h, sfnapi.OpSendTaskSuccess, &sfnapi.SendTaskSuccessInput{TaskToken: "t", Output: "{}"})),
			"SendTaskFailure":            errOf(call(h, sfnapi.OpSendTaskFailure, &sfnapi.SendTaskFailureInput{TaskToken: "t"})),
			"SendTaskHeartbeat":          errOf(call(h, sfnapi.OpSendTaskHeartbeat, &sfnapi.SendTaskHeartbeatInput{TaskToken: "t"})),
			"CreateActivity":             errOf(call(h, sfnapi.OpCreateActivity, &sfnapi.CreateActivityInput{Name: "a"})),
			"ListActivities":             errOf(call(h, sfnapi.OpListActivities, &sfnapi.ListActivitiesInput{})),
			"ListStateMachineAliases":    errOf(call(h, sfnapi.OpListStateMachineAliases, &sfnapi.ListStateMachineAliasesInput{StateMachineArn: arn})),
			"PublishStateMachineVersion": errOf(call(h, sfnapi.OpPublishStateMachineVersion, &sfnapi.PublishStateMachineVersionInput{StateMachineArn: arn})),
			"ListMapRuns":                errOf(call(h, sfnapi.OpListMapRuns, &sfnapi.ListMapRunsInput{ExecutionArn: h.executionArn("m", "x")})),
		} {
			if err == nil || err.ID.Status != 501 || err.ID.Shape.Name() != "NotImplemented" {
				h.Errorf("%s = %+v, want 501 NotImplemented", name, err)
			}
		}
	})
}

// Every inferred choice is a switch with its value, label, record, ref,
// operations and no settling scenario; a switch the 2026-11-03 recording
// session settled keeps its
// row, labelled aws-recorded. Values are read from the constructors, never
// restated.
func TestSwitches(t *testing.T) {
	bound := make([]string, 0, len(wantCoverage))
	for name := range wantCoverage {
		bound = append(bound, "sfn."+name)
	}
	slices.Sort(bound)
	want := []struct {
		entry       fidelity.Entry
		name, ref   string
		record      string
		label       evidence.Label
		ops         []string
		sortOpsLike bool
	}{
		{sfn.ChoiceNoMatch().Entry(), "sfn.choice.no-match", "docs/research/stepfunctions-asl.md#64-no-match-and-missing-variables", "ASL-08", evidence.AWSRecorded, []string{"sfn.StartExecution"}, false},
		{sfn.ChoiceMissingVariable().Entry(), "sfn.choice.missing-variable", "docs/research/stepfunctions-asl.md#64-no-match-and-missing-variables", "ASL-08", evidence.AWSRecorded, []string{"sfn.StartExecution"}, false},
		{sfn.IntrinsicFailure().Entry(), "sfn.intrinsic.failure-error", "docs/research/stepfunctions-asl.md#54-failures", "ASL-05", evidence.AWSRecorded, []string{"sfn.StartExecution"}, false},
		{sfn.LambdaError().Entry(), "sfn.integration.lambda-error", "docs/research/stepfunctions-api.md#94-error-names-and-causes-recorded", "SFN-15", evidence.AWSRecorded, []string{"sfn.StartExecution"}, false},
		{sfn.DeleteRunningExecution().Entry(), "sfn.delete.running-execution", "docs/research/stepfunctions-api.md#54-deletestatemachine", "SFN-06", evidence.AWSRecorded, []string{"sfn.DeleteStateMachine", "sfn.StartExecution"}, true},
		{sfn.DenialStatus().Entry(), "sfn.denial.status", "docs/research/iam-enforcement-surfaces.md#123-error-identities-to-emit", "SFN-15", evidence.AWSRecorded, bound, true},
	}
	got := (&sfn.Service{}).Switches()
	if len(got) != len(want) {
		t.Fatalf("Switches() lists %d entries, want %d", len(got), len(want))
	}
	for i, w := range want {
		e := got[i]
		ops := e.Ops
		if w.sortOpsLike {
			ops = slices.Sorted(slices.Values(ops))
		}
		if e.Name != w.name || e.Ref != w.ref || e.Record != w.record || e.Label != w.label || !slices.Equal(ops, w.ops) || e.Value != w.entry.Value {
			t.Errorf("switch %d = %+v\nwant name %s, ref %s, record %s, label %s, ops %v, value %s", i, e, w.name, w.ref, w.record, w.label, w.ops, w.entry.Value)
		}
		ns, ok := e.Settles.(fidelity.NoScenario)
		if !ok || !strings.Contains(ns.Reason, w.record) {
			t.Errorf("%s Settles = %#v, want NoScenario naming %s", e.Name, e.Settles, w.record)
		}
	}
	rules := sfn.EngineRules()
	if rules != (asl.Rules{
		NoMatch: sfn.ChoiceNoMatch().Value, MissingVariable: sfn.ChoiceMissingVariable().Value,
		IntrinsicFailure: sfn.IntrinsicFailure().Value, EndOnDelete: sfn.DeleteRunningExecution().Value,
	}) {
		t.Errorf("EngineRules() = %+v, want the switch values", rules)
	}
}

// The recorded Lambda service error (stepfunctions-api.md 9.4 row 4,
// aws-recorded localstack@8b9a79f
// v2/error_handling/test_task_service_lambda.snapshot.json:L972-L973):
// a missing function is Lambda.ResourceNotFoundException with the AWSLambda
// cause. The naming is switch sfn.integration.lambda-error: a shape Lambda's
// model declares takes the prefix, any other the unmodeled name; the cause
// of a lambda:invoke task is in the AWSLambda form and that of a
// function-ARN task in the SDK v2 form (SFN-15 02 and 03, recorded
// 2026-11-03); flipping the switch's fields changes the result.
func TestLambdaTaskError(t *testing.T) {
	n := sfn.LambdaError().Value
	fn := "arn:aws:lambda:us-east-1:000000000000:function:nope"
	got := sfn.LambdaTaskError(n, asl.LambdaInvoke, lambdaapi.ErrResourceNotFoundException("Function not found: "+fn+":$LATEST"), "req-1")
	want := asl.StateError{
		Name:  "Lambda.ResourceNotFoundException",
		Cause: "Function not found: " + fn + ":$LATEST (Service: AWSLambda; Status Code: 404; Error Code: ResourceNotFoundException; Request ID: req-1; Proxy: null)",
	}
	if got != want {
		t.Errorf("LambdaTaskError(ResourceNotFoundException) = %+v\nwant %+v", got, want)
	}
	if got := sfn.LambdaTaskError(n, asl.LambdaInvoke, lambdaapi.ErrTooManyRequestsException("Rate exceeded"), "r"); got.Name != n.Prefix+"TooManyRequestsException" {
		t.Errorf("TooManyRequestsException named %q, want %q", got.Name, n.Prefix+"TooManyRequestsException")
	}
	unmodeled := sfnapi.ErrAccessDeniedException("User is not authorized")
	got = sfn.LambdaTaskError(n, asl.LambdaInvoke, unmodeled, "r")
	if wantCause := "User is not authorized (Service: AWSLambda; Status Code: 400; Error Code: AccessDeniedException; Request ID: r; Proxy: null)"; got.Name != "Lambda.AWSLambdaException" || got.Cause != wantCause {
		t.Errorf("generic AccessDeniedException = %+v, want Lambda.AWSLambdaException with cause %q", got, wantCause)
	}
	got = sfn.LambdaTaskError(n, asl.FunctionARN, unmodeled, "r")
	if wantCause := "User is not authorized (Service: Lambda, Status Code: 400, Request ID: r) (SDK Attempt Count: 1)"; got.Name != "Lambda.AWSLambdaException" || got.Cause != wantCause {
		t.Errorf("function-ARN task AccessDeniedException = %+v, want Lambda.AWSLambdaException with cause %q", got, wantCause)
	}

	flip := sfn.LambdaErrorNaming{Prefix: "X.", Unmodeled: "X.Other", Cause: awserr.SDKv2("X"), FunctionARNCause: awserr.SDKv1("Y")}
	if got := sfn.LambdaTaskError(flip, asl.LambdaInvoke, lambdaapi.ErrResourceNotFoundException("m"), "id"); got.Name != "X.ResourceNotFoundException" ||
		got.Cause != "m (Service: X, Status Code: 404, Request ID: id) (SDK Attempt Count: 1)" {
		t.Errorf("flipped naming = %+v", got)
	}
	if got := sfn.LambdaTaskError(flip, asl.FunctionARN, unmodeled, "id"); got.Name != "X.Other" ||
		got.Cause != "User is not authorized (Service: Y; Status Code: 400; Error Code: AccessDeniedException; Request ID: id; Proxy: null)" {
		t.Errorf("flipped unmodeled naming = %+v", got)
	}
}

// SFN's denial is AccessDeniedException at the switch's status (switch
// sfn.denial.status; iam-enforcement-surfaces.md 12.3).
func TestDenialIdentity(t *testing.T) {
	d := sfnapi.Service().Denial.Identity
	if d.Shape.Name() != "AccessDeniedException" || d.Status != sfn.DenialStatus().Value || sfnapi.IdentityAccessDeniedException().Status != sfn.DenialStatus().Value {
		t.Errorf("denial identity = %+v, want AccessDeniedException at %d", d, sfn.DenialStatus().Value)
	}
}
