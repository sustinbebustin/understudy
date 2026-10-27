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
		asl.FeatPathDialect, asl.FeatPayloadTemplates, asl.FeatIntrinsics, asl.FeatLambdaTask, asl.FeatRetryCatch)
}

// The coverage notes of the bound operations.
const (
	noteCreate = "Standard workflows only: type EXPRESS, publish true and a CUSTOMER_MANAGED_KMS_KEY encryptionConfiguration answer 501. " +
		"Definitions are JSONPath with Pass, Succeed, Fail, Choice, Wait and Task states only, and a top-level TimeoutSeconds. " +
		"A Pass state takes Result, InputPath, Parameters, ResultPath, OutputPath, " +
		"Next, End and Comment, a Succeed state InputPath, OutputPath and Comment, a Fail state Error or ErrorPath, Cause or CausePath " +
		"and Comment, a Choice state Choices (every JSONPath operator and its Path form, And, Or, Not), Default, InputPath, OutputPath " +
		"and Comment, a Wait state one of Seconds, SecondsPath, Timestamp and TimestampPath, InputPath, OutputPath, Next, End and Comment, " +
		"and a Task state a Resource that is a Lambda function ARN (qualifier allowed) or arn:aws:states:::lambda:invoke, InputPath, Parameters, " +
		"ResultSelector, ResultPath, OutputPath, TimeoutSeconds or TimeoutSecondsPath, Retry, Catch, Next, End and Comment; " +
		"'.$' values are paths, '$$' context paths and calls of the 18 States.* intrinsic functions. " +
		"Every other state type and field answers InvalidDefinition with code UNSUPPORTED_BY_UNDERSTUDY, as do JSONata, Assign and variables, " +
		"Distributed Map and its fields, Credentials, HeartbeatSeconds, callbacks, activities, every other Task resource, " +
		"a literal lambda:invoke InvocationType other than RequestResponse, and JSONPath functions such as length(), " +
		"multi-clause filters and filter scripts (ASL-19). A dynamodb resource outside getItem, putItem, updateItem and deleteItem answers " +
		"AWS's SCHEMA_VALIDATION_FAILED text. Only the first problem in a definition is reported (UNVERIFIED, ASL-01, SFN-08). " +
		"loggingConfiguration and tracingConfiguration are validated, stored and echoed; no log events or traces are emitted. " +
		"The role ARN is checked for syntax only, with understudy's message (UNVERIFIED, SFN-08); its trust policy is not checked"
	noteUpdate = "The CreateStateMachine definition and configuration rules apply; publish true and versionDescription answer 501. " +
		"Running executions keep the definition and role they started with"
	noteValidate = "Diagnostics use AWS's codes; messages are AWS's for the recorded cases (stepfunctions-asl.md 3.4-3.6) and understudy's wording elsewhere " +
		"(UNVERIFIED, ASL-01). Features understudy refuses are ERROR diagnostics with code UNSUPPORTED_BY_UNDERSTUDY; type EXPRESS answers 501"
	noteVersions = "Always empty: publishing versions is not implemented (publish is refused and PublishStateMachineVersion is unbound)"
	noteStop     = "error and cause are kept in DescribeExecution and ExecutionAborted; stopping a closed execution answers 200 with its stopDate " +
		"and changes nothing (UNVERIFIED, SFN-04)"
	noteStart = "Standard executions of Pass, Succeed, Fail, Choice, Wait and Lambda Task states. Unrecorded AWS behavior understudy chooses, each with its recording case: " +
		"same-name idempotency compares input byte for byte (SFN-03); malformed input other than the recorded cases answers " +
		"'Invalid execution input.' (SFN-08); ResultPath into a non-object and a missing path in a Task template fail with States.Runtime (ASL-03, ASL-04); " +
		"States.DataLimitExceeded from a non-Task state, or from a Task's output after ResultPath and OutputPath, is terminal (ASL-15); " +
		"a Choice no rule matches without Default fails with switch sfn.choice.no-match's error, and a Variable or Path operand " +
		"that selects nothing (IsPresent aside) with switch sfn.choice.missing-variable's, each with understudy's cause (ASL-08); " +
		"IsTimestamp and the Timestamp operators follow the documented RFC 3339 profile and compare instants, and a StringMatches " +
		"backslash escaping neither '*' nor '\\' fails with States.Runtime (ASL-07); " +
		"a Wait past the one-year execution limit ends with States.Timeout at the limit (ASL-09); " +
		"the 25,000-event history limit fails the execution with States.Runtime (ASL-16); " +
		"retry delays keep milliseconds, FULL jitter is uniform from understudy's seeded stream and applies after MaxDelaySeconds, " +
		"and $$.State.RetryCount counts the retries of every retrier of the state (ASL-10); " +
		"a catcher's error output has no Cause member when the error has no cause (ASL-13); " +
		"an intrinsic function failing at run time fails with switch sfn.intrinsic.failure-error's error and understudy's cause (ASL-05); " +
		"ArrayContains and ArrayUnique compare numbers by value and objects as member sets, and ArrayPartition, ArrayRange and " +
		"MathRandom round non-integers half up as MathAdd does (ASL-06). " +
		"A lambda:invoke InvocationType that resolves to anything but RequestResponse fails with States.Runtime (SFN-13). " +
		"lambda:invoke passes FunctionName, Qualifier and Payload to Invoke and ignores its other Parameters. " +
		"A TimeoutSecondsPath that selects no positive integer fails with States.Runtime and understudy's cause. " +
		"A Lambda service error is named by switch sfn.integration.lambda-error; a function error without an errorType is Lambda.Unknown. " +
		"lambda:invoke results carry SdkHttpMetadata with AWS's recorded header names; the X-Amzn-Trace-Id value is understudy's. " +
		"traceHeader, or the X-Amzn-Trace-Id request header when both are sent, " +
		"is stored and returned by DescribeExecution; no X-Ray trace is emitted. A Lambda task in flight when a snapshot was saved is invoked again after load. " +
		"States.MathRandom never reproduces AWS's sequence for a seed"
	noteListExecutions    = "mapRunArn and redriveFilter are validated; no Map Run or redrive exists, so they match nothing"
	noteDescribeExecution = "redriveStatus is given for SUCCEEDED and FAILED executions only, and redriveStatusReason for SUCCEEDED ones"
	noteHistory           = "includeExecutionData false removes input and output and keeps the *Details members (UNVERIFIED, SFN-05). " +
		"Payload-template output keeps template key order (UNVERIFIED, ASL-02)"
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
	// UpdateStateMachine's generated list also holds iam:PassRole, the
	// execution role's check rather than one of this service's actions, and
	// states:PublishStateMachineVersion, which publish true needs and is
	// refused.
	b.UpdateStateMachine(s.updateStateMachine, registry.Partial{Note: noteUpdate}, kernel.WithoutSecondaryActions())
	b.DescribeStateMachine(s.describeStateMachine, registry.Full{})
	b.DeleteStateMachine(s.deleteStateMachine, registry.Full{})
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
	b.StopExecution(s.stopExecution, registry.Partial{Note: noteStop})
	b.ListExecutions(s.listExecutions, registry.Partial{Note: noteListExecutions}, kernel.WithValidator(validateListExecutions))
	b.DescribeExecution(s.describeExecution, registry.Partial{Note: noteDescribeExecution})
	b.DescribeStateMachineForExecution(s.describeStateMachineForExecution, registry.Full{})
	b.GetExecutionHistory(s.getExecutionHistory, registry.Partial{Note: noteHistory})
}
