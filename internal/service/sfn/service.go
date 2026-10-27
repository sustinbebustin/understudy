package sfn

import (
	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Options are the Step Functions settings. There are none yet; the zero
// value is AWS's behavior.
type Options struct{}

// Service is AWS Step Functions: one state cell per account and region.
type Service struct {
	k     *kernel.Kernel
	ids   *ids.Source
	cells *store.Partitioned[aws.Scope, State]
	// step is TimerStep as registered with the kernel.
	step kernel.HandledKind
}

// New binds the Step Functions operations understudy implements and
// registers the step timer. Every other operation stays unbound and
// answers 501.
func New(d kernel.ServiceDeps, _ Options) *Service {
	s := &Service{k: d.Kernel, ids: d.IDs, cells: store.NewPartitioned(d.Kernel.Gate(), stateCodec(), newState)}
	s.step = d.Kernel.Timers().Handle(TimerStep, s.onStep)
	b := sfnapi.NewBinder(d.Binder)
	s.bindMachines(b)
	s.bindExecutions(b)
	return s
}

// AcceptedFeatures is the set of definition features whose runtime has
// landed; Create and Validate parse with it, so any other construct is
// refused with code UNSUPPORTED_BY_UNDERSTUDY.
func AcceptedFeatures() asl.Features { return asl.NewFeatures(asl.FeatPass, asl.FeatSucceed) }

// The coverage notes of the bound operations.
const (
	noteCreate = "Standard workflows only: type EXPRESS, publish true and a CUSTOMER_MANAGED_KMS_KEY encryptionConfiguration answer 501. " +
		"Definitions are JSONPath with Pass and Succeed states only, and a Pass state takes Result, Next, End and Comment only; " +
		"every other state type and field answers InvalidDefinition with code UNSUPPORTED_BY_UNDERSTUDY, as do JSONata, Assign and variables, " +
		"Distributed Map and its fields, Credentials and HeartbeatSeconds. Only the first problem in a definition is reported (UNVERIFIED, ASL-01, SFN-08). " +
		"loggingConfiguration and tracingConfiguration are stored and echoed without validation; no log events or traces are emitted. " +
		"The role ARN is stored as sent, unchecked"
	noteValidate = "Diagnostics use AWS's codes; messages are AWS's for the recorded blank, empty-object, missing-field and unknown-Type cases " +
		"(stepfunctions-asl.md 3.5-3.6) and understudy's wording elsewhere, including a definition that is not JSON (UNVERIFIED, ASL-01). " +
		"Features understudy refuses are ERROR diagnostics with code UNSUPPORTED_BY_UNDERSTUDY; type EXPRESS answers 501"
	noteVersions = "Always empty: publishing versions is not implemented (publish is refused and PublishStateMachineVersion is unbound)"
	noteDelete   = "The machine is removed at once and its running executions run on to their end; AWS keeps it DELETING until they finish " +
		"and ends them at their next transition (switch sfn.delete.running-execution is not applied yet)"
	noteStart = "Standard executions of Pass and Succeed states. Unrecorded AWS behavior understudy chooses, each with its recording case: " +
		"same-name idempotency compares input byte for byte (SFN-03); input that is not JSON answers 'Invalid execution input.', " +
		"not AWS's parser text (SFN-08); the 25,000-event history limit fails the execution with States.Runtime (ASL-16). " +
		"The 262,144-byte limit on state data is not enforced. traceHeader, or the X-Amzn-Trace-Id request header when both are sent, " +
		"is stored and returned by DescribeExecution; no X-Ray trace is emitted"
	noteDescribeExecution = "Closed executions are kept until a reset, not removed 90 days after they close. " +
		"redriveStatus and redriveStatusReason are given for SUCCEEDED executions only"
	noteHistory = "includeExecutionData false removes input and output and keeps the *Details members (UNVERIFIED, SFN-05)"
)

// bindMachines binds the state machine control plane and tags.
func (s *Service) bindMachines(b sfnapi.Binder) {
	b.CreateStateMachine(s.createStateMachine, registry.Partial{Note: noteCreate},
		kernel.WithActions(func(_ *kernel.Call, in *sfnapi.CreateStateMachineInput) []registry.IAMAction {
			// Tags on create need states:TagResource (stepfunctions-api.md
			// 9.5); iam:PassRole is the execution role's check, not one of
			// this service's actions.
			if len(in.Tags) == 0 {
				return nil
			}
			return []registry.IAMAction{{Prefix: "states", Name: "TagResource"}}
		}))
	b.DescribeStateMachine(s.describeStateMachine, registry.Full{})
	b.DeleteStateMachine(s.deleteStateMachine, registry.Partial{Note: noteDelete})
	b.ListStateMachines(s.listStateMachines, registry.Full{})
	b.ValidateStateMachineDefinition(s.validateStateMachineDefinition, registry.Partial{Note: noteValidate})
	b.ListStateMachineVersions(s.listStateMachineVersions, registry.Partial{Note: noteVersions})
	b.TagResource(s.tagResource, registry.Full{})
	b.UntagResource(s.untagResource, registry.Full{})
	b.ListTagsForResource(s.listTagsForResource, registry.Full{})
}

// bindExecutions binds the execution operations.
func (s *Service) bindExecutions(b sfnapi.Binder) {
	// StartExecution's generated list also holds states:DescribeExecution,
	// which a start never needs.
	b.StartExecution(s.startExecution, registry.Partial{Note: noteStart}, kernel.WithoutSecondaryActions())
	b.DescribeExecution(s.describeExecution, registry.Partial{Note: noteDescribeExecution})
	b.DescribeStateMachineForExecution(s.describeStateMachineForExecution, registry.Full{})
	b.GetExecutionHistory(s.getExecutionHistory, registry.Partial{Note: noteHistory})
}
