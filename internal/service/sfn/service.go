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
	// stepKind, expireKind and invokeKind are TimerStep, TimerExpire and
	// TimerInvoke as registered with the kernel.
	stepKind, expireKind, invokeKind kernel.HandledKind
	// running are the Lambda tasks whose function is running.
	running invocations
	// definitions are the parsed definitions of running executions.
	definitions parsedDefinitions
}

// New binds the Step Functions operations understudy implements and
// registers the step timer. Every other operation stays unbound and
// answers 501.
func New(d kernel.ServiceDeps, _ Options) *Service {
	s := &Service{k: d.Kernel, ids: d.IDs, cells: store.NewPartitioned(d.Kernel.Gate(), stateCodec(), newState)}
	s.stepKind = d.Kernel.Timers().Handle(TimerStep, s.onStep)
	s.expireKind = d.Kernel.Timers().Handle(TimerExpire, s.onExpire)
	s.invokeKind = d.Kernel.Timers().Handle(TimerInvoke, s.onInvoke)
	b := sfnapi.NewBinder(d.Binder)
	s.bindMachines(b)
	s.bindExecutions(b)
	return s
}

// AcceptedFeatures is the set of definition features whose runtime has
// landed; Create and Validate parse with it, so any other construct is
// refused with code UNSUPPORTED_BY_UNDERSTUDY.
func AcceptedFeatures() asl.Features {
	return asl.NewFeatures(asl.FeatPass, asl.FeatSucceed, asl.FeatFail, asl.FeatChoice, asl.FeatWait, asl.FeatTimeout,
		asl.FeatPathDialect, asl.FeatPayloadTemplates, asl.FeatIntrinsics, asl.FeatLambdaTask, asl.FeatRetryCatch,
		asl.FeatParallel, asl.FeatMap)
}

// The coverage notes of the bound operations.
const (
	noteCreate = "Standard workflows only: type EXPRESS, publish true and a CUSTOMER_MANAGED_KMS_KEY encryptionConfiguration answer 501. " +
		"Definitions are JSONPath with Pass, Task, Choice, Wait, Succeed, Fail, Parallel and inline Map states, and Task resources are " +
		"Lambda only (a function ARN or arn:aws:states:::lambda:invoke). These answer InvalidDefinition with code UNSUPPORTED_BY_UNDERSTUDY: " +
		"JSONata, Assign and variables, Distributed Map and its fields, Credentials, HeartbeatSeconds, callbacks, activities, " +
		"every other Task resource, a literal lambda:invoke InvocationType other than RequestResponse, and JSONPath functions such as length(), " +
		"multi-clause filters and filter scripts, though AWS accepts length() and fails only at run time (ASL-19). " +
		"ToleratedFailurePercentage, which AWS accepts on an inline Map, is refused with Distributed Map's fields (ASL-01). " +
		"A dynamodb resource outside getItem, putItem, updateItem and deleteItem answers AWS's SCHEMA_VALIDATION_FAILED text. " +
		"Every problem in a definition is reported in one message, as AWS joins them; wording for problems no recording covers is understudy's (ASL-01). " +
		"loggingConfiguration and tracingConfiguration are validated, stored and echoed; no log events or traces are emitted. " +
		"The role ARN is checked for syntax. AWS checks neither the role's existence nor its trust at create; understudy checks its trust " +
		"policy under iam.enforce=enforce, with understudy's message (SFN-08)"
	noteUpdate = "The CreateStateMachine definition and configuration rules apply; publish true and versionDescription answer 501. " +
		"Running executions keep the definition and role they started with"
	noteValidate = "Diagnostics use AWS's codes, locations and order; messages are AWS's for the recorded cases (stepfunctions-asl.md 3.4-3.6) " +
		"and understudy's wording elsewhere (ASL-01). Features understudy refuses are ERROR diagnostics with code UNSUPPORTED_BY_UNDERSTUDY; type EXPRESS answers 501"
	noteVersions = "Always empty: publishing versions is not implemented (publish is refused and PublishStateMachineVersion is unbound)"
	noteDelete   = "An idle machine is removed at once, and one with running executions once the last of them ends; " +
		"AWS still describes a deleted machine as DELETING, idle or not, for a time no recording has measured " +
		"(at least 45 seconds after its last execution ended; SFN-06, ASL-17)"
	noteStart = "Standard executions. Unrecorded AWS behavior understudy chooses, each with its recording case: " +
		"malformed input other than the recorded cases answers 'Invalid execution input.' (SFN-08); " +
		"a missing path in a Task or Parallel template fails with States.Runtime (ASL-04); " +
		"a ResultPath conflict deeper than one step names the node it stops at (ASL-03); " +
		"a Wait past the one-year execution limit ends with States.Timeout at the limit (ASL-09); " +
		"a Task or Map leaves a wider margin before the 25,000-event history limit than the recorded Pass and Choice steps (ASL-16); " +
		"FULL jitter is drawn uniformly from understudy's seeded stream (ASL-10); " +
		"IsTimestamp follows the documented RFC 3339 profile (ASL-07). " +
		"Known differences from AWS: a runtime error's cause quotes the state's input as compact JSON, where AWS quotes the input as sent (ASL-04); " +
		"JSONPath functions such as length() are refused at create, where AWS accepts them and fails at run time (ASL-19); " +
		"an input over 262,144 is counted in characters with the 'Member must have length' wording, where AWS counts UTF-8 bytes " +
		"and says 'Member must have size less than or equal to 262144 bytes in UTF-8 encoding' (SFN-08). " +
		"A lambda:invoke InvocationType that resolves to anything but RequestResponse fails with States.Runtime (SFN-13). " +
		"lambda:invoke results carry SdkHttpMetadata with AWS's recorded header names; the X-Amzn-Trace-Id value is understudy's. " +
		"traceHeader, or the X-Amzn-Trace-Id request header when both are sent, " +
		"is stored and returned by DescribeExecution; no X-Ray trace is emitted. A Lambda task in flight when a snapshot was saved is invoked again after load. " +
		"States.MathRandom never reproduces AWS's sequence for a seed"
	noteListExecutions    = "mapRunArn and redriveFilter are validated; no Map Run or redrive exists, so they match nothing"
	noteDescribeExecution = "redriveStatus is NOT_REDRIVABLE for SUCCEEDED executions and for one failed at the history event limit, " +
		"and REDRIVABLE for other FAILED, TIMED_OUT and ABORTED ones, however long ago they closed; a RUNNING execution has none. " +
		"The other NOT_REDRIVABLE reasons AWS documents (a running execution, a DELETING machine, the 14-day redrive period) are not given: none is recorded"
	noteHistory = "Events of concurrent Parallel branches and Map iterations are appended in the order understudy runs them, " +
		"steps due at the same emulator instant in branch or item order, as AWS recorded for the runs of ASL-13; other interleavings are unrecorded. " +
		"Payload-template output keeps template key order, where AWS writes an order of its own, hash-like (ASL-02)"
)

// bindMachines binds the state machine control plane and tags.
func (s *Service) bindMachines(b sfnapi.Binder) {
	b.CreateStateMachine(s.createStateMachine, registry.Partial{Note: noteCreate}, createAuthorization()...)
	b.UpdateStateMachine(s.updateStateMachine, registry.Partial{Note: noteUpdate}, updateAuthorization()...)
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
	// which a start never needs. The action's resource is the state
	// machine it names (aws-doc service-authorization
	// list_awsstepfunctions), so a policy scoped to the machine allows it.
	b.StartExecution(s.startExecution, registry.Partial{Note: noteStart}, kernel.WithoutSecondaryActions(),
		kernel.WithResources(func(_ *kernel.Call, in *sfnapi.StartExecutionInput) []aws.ARN {
			a, err := aws.ParseARN(in.StateMachineArn)
			if err != nil {
				return nil
			}
			return []aws.ARN{a}
		}))
	b.StopExecution(s.stopExecution, registry.Full{})
	b.ListExecutions(s.listExecutions, registry.Partial{Note: noteListExecutions}, kernel.WithValidator(validateListExecutions))
	b.DescribeExecution(s.describeExecution, registry.Partial{Note: noteDescribeExecution})
	b.DescribeStateMachineForExecution(s.describeStateMachineForExecution, registry.Full{})
	b.GetExecutionHistory(s.getExecutionHistory, registry.Partial{Note: noteHistory})
}
