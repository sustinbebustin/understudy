package scheduler

import (
	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// TimerFire is the timer of one schedule's next invocation, keyed by the
// schedule ARN and armed at the invocation's due instant: the scheduled
// instant plus the flexible-window offset (scheduler.md 14.2.5, 14.3.7).
const TimerFire kernel.TimerKind = "scheduler.fire"

// stateVersion is the schema version of the service's snapshot section.
const stateVersion = 1

// Service is Amazon EventBridge Scheduler: one state cell per account and
// region.
type Service struct {
	opts  Options
	k     *kernel.Kernel
	ids   *ids.Source
	cells *store.Partitioned[aws.Scope, State]
	// fire is TimerFire as registered with the kernel.
	fire kernel.HandledKind
}

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// createNote is CreateSchedule's coverage note while tickets 2 to 6 are
// what is built (spec "Coverage marks" is the note once every ticket is).
const createNote = "rate(), at() and cron() schedules in the default group with a templated Lambda, SQS, SNS or EventBridge event bus target in the schedule's Region " +
	"are invoked on the emulator clock as the execution role, " +
	"at() and cron() in any IANA time zone, which a rate ignores, between StartDate and EndDate, and not while State is DISABLED; " +
	"universal (aws-sdk) targets and ECS, Kinesis, Firehose, CodeBuild, CodePipeline, Inspector, SageMaker and Step Functions targets answer NotImplemented; " +
	"Lambda, SQS and SNS targets in another Region, other schedule groups, a FLEXIBLE window and ActionAfterCompletion DELETE " +
	"answer NotImplemented once the expression, time zone, window, dates and target are validated; " +
	"KmsKeyArn is stored and echoed, the payload is not encrypted; " +
	"the execution role's trust is not checked, so a missing or untrusting role still delivers; " +
	"ClientToken is accepted and not used for idempotency; the 10,000,000-schedule quota is not counted; " +
	"unrecorded, understudy's choice: a target ARN of a service with no templated target is refused at create with ValidationException, " +
	"rate units are accepted singular or plural with any value, a rate schedule without StartDate first fires at the write instant, " +
	"a rate schedule whose StartDate has passed first fires at its first interval at or after the write instant, " +
	"an absent Input delivers {}, an at() in the past fires at once, an at() fires at its seconds, a fired at() with NONE stays with its State unchanged, " +
	"client-side target errors go to the dead-letter queue without retries and one execution id covers an invocation's retries, " +
	"a dead letter's TARGET_ARN is the universal aws-sdk form and its body the payload after substitution, " +
	"a qualified Lambda target's version or alias is passed as Invoke's Qualifier, " +
	"an EventBridge target's event has no resources and its time is the delivery instant, an SNS target's message has no Subject, " +
	"an event bus target in another Region or without EventBridgeParameters is refused with ValidationException, " +
	"parameter blocks that do not match the target are stored, echoed and ignored, a MaximumWindowInMinutes sent with OFF is ignored, " +
	"Input on a Lambda or EventBridge target is not checked for JSON at create, Input is limited to 262,144 bytes, " +
	"checks run in the order expression, time zone, window, dates and the future-fire check, target, " +
	"a present date in the future-fire text renders as RFC 3339 UTC, and every refusal message except the expression, future-fire and missing-group texts is understudy's own"

// New binds the Scheduler operations built so far (scheduler.md 2.2) and
// registers the fire timer; the rest stay unbound and answer 501 until
// their tickets build them.
func New(d kernel.ServiceDeps, o Options) *Service {
	s := &Service{opts: o, k: d.Kernel, ids: d.IDs, cells: store.NewPartitioned(d.Kernel.Gate(), codec(), newState)}
	s.fire = d.Kernel.Timers().Handle(TimerFire, s.onFire)
	b := schedulerapi.NewBinder(d.Binder)
	b.CreateSchedule(s.createSchedule, registry.Partial{Note: createNote})
	b.UpdateSchedule(s.updateSchedule, registry.Partial{Note: "as CreateSchedule; every member is replaced and omitted ones take their defaults; " +
		"unrecorded, understudy's choice: an update that leaves the expression, time zone, dates, window and State unchanged keeps the next fire, any other restarts the plan at the write instant"})
	b.ListSchedules(s.listSchedules, registry.Partial{Note: "unrecorded, understudy's choice: schedules in group then name order, 100 per page without MaxResults"})
	b.GetSchedule(s.getSchedule, registry.Partial{Note: "unrecorded, understudy's choice: ScheduleExpressionTimezone, Description, StartDate and EndDate are absent when they were not sent, " +
		"MaximumWindowInMinutes is absent under OFF, State defaults to ENABLED, and CreationDate and LastModificationDate are whole seconds"})
	b.DeleteSchedule(s.deleteSchedule, registry.Partial{Note: "unrecorded, understudy's choice: ClientToken is accepted and not used for idempotency"})
	return s
}
