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

// createNote is CreateSchedule's coverage note while the tracer bullet is
// what is built (spec "Tracer bullet").
const createNote = "rate() schedules in the default group with an SQS queue target in the schedule's Region are invoked on the emulator clock as the execution role, in any IANA time zone, which a rate ignores; " +
	"at() and cron() expressions, other target services, other schedule groups, StartDate and EndDate, State DISABLED, " +
	"a FLEXIBLE window, DeadLetterConfig and the parameter blocks of other targets answer NotImplemented once the expression, time zone, window and dates are validated; " +
	"KmsKeyArn is stored and echoed, the payload is not encrypted; " +
	"the execution role's trust is not checked, so a missing or untrusting role still delivers; " +
	"ClientToken is accepted and not used for idempotency; the 10,000,000-schedule quota is not counted; " +
	"unrecorded, understudy's choice: rate units are accepted singular or plural with any value, a rate schedule without StartDate first fires at the write instant, " +
	"an absent Input delivers {}, client-side target errors are not retried and one execution id covers an invocation's retries, " +
	"a MaximumWindowInMinutes sent with OFF is ignored, checks run in the order expression, time zone, window, dates and the future-fire check, target, " +
	"a present date in the future-fire text renders as RFC 3339 UTC, and every refusal message except the expression and future-fire texts is understudy's own"

// New binds the Scheduler operations built so far (scheduler.md 2.2) and
// registers the fire timer; the rest stay unbound and answer 501 until
// their tickets build them.
func New(d kernel.ServiceDeps, o Options) *Service {
	s := &Service{opts: o, k: d.Kernel, ids: d.IDs, cells: store.NewPartitioned(d.Kernel.Gate(), codec(), newState)}
	s.fire = d.Kernel.Timers().Handle(TimerFire, s.onFire)
	b := schedulerapi.NewBinder(d.Binder)
	b.CreateSchedule(s.createSchedule, registry.Partial{Note: createNote})
	b.GetSchedule(s.getSchedule, registry.Partial{Note: "unrecorded, understudy's choice: ScheduleExpressionTimezone, Description, StartDate and EndDate are absent when they were not sent, " +
		"MaximumWindowInMinutes is absent under OFF, State defaults to ENABLED, and CreationDate and LastModificationDate are whole seconds"})
	b.DeleteSchedule(s.deleteSchedule, registry.Partial{Note: "unrecorded, understudy's choice: ClientToken is accepted and not used for idempotency"})
	return s
}
