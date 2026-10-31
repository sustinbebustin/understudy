package scheduler

import (
	"slices"
	"sync/atomic"

	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
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
	// born holds the emulator-clock instants the state was created, loaded
	// or reset at: the dates of a default group no write has materialized
	// (spec "Contract"; scheduler.md 14.2.2).
	born atomic.Pointer[origins]
}

// origins is when each scope's state began: base, the instant the whole
// state was created, loaded or reset, unless a later reset covered the
// scope. Never changed in place: a reset stores a new one.
type origins struct {
	base   clock.Instant
	resets []scopedReset
}

// scopedReset is a reset of some scopes and its instant.
type scopedReset struct {
	scope kernel.ResetScope
	at    clock.Instant
}

// origin is the instant scope's state was created, loaded or last reset.
// UNVERIFIED (SCH-06), scheduler.md 3.1: AWS's CreationDate of the
// default group is unrecorded.
func (s *Service) origin(scope aws.Scope) clock.Instant {
	o := s.born.Load()
	for _, r := range slices.Backward(o.resets) {
		if r.scope.Covers(scope) {
			return r.at
		}
	}
	return o.base
}

// restart sets every scope's origin to the clock's instant.
func (s *Service) restart() {
	s.born.Store(&origins{base: s.k.Clock().Now()})
}

// restartScopes sets the origin of the scopes rs covers to the clock's
// instant, leaving the others' as they were; a reset of every scope is a
// restart. Reset is the only caller, and resets do not run concurrently.
func (s *Service) restartScopes(rs kernel.ResetScope) {
	if len(rs.Accounts) == 0 && len(rs.Regions) == 0 {
		s.restart()
		return
	}
	o := s.born.Load()
	resets := slices.DeleteFunc(slices.Clone(o.resets), func(r scopedReset) bool {
		return slices.Equal(r.scope.Accounts, rs.Accounts) && slices.Equal(r.scope.Regions, rs.Regions)
	})
	s.born.Store(&origins{base: o.base, resets: append(resets, scopedReset{scope: rs, at: s.k.Clock().Now()})})
}

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// createNote is CreateSchedule's coverage note: the spec's "Coverage
// marks" note with ticket 10 and the SNS tail landed, clause for clause and
// in its order, except where the binary does otherwise (the KMS clause:
// 05b evaluates key policies; Step Functions Input is not JSON-checked
// either) and for the clauses after the Input limit, which name further
// unrecorded choices and deviations the binary makes (invariant 3).
const createNote = "templated Lambda, SQS, SNS, EventBridge event bus and Step Functions targets in the schedule's Region are invoked on the emulator clock as the execution role; " +
	"universal (aws-sdk) targets and ECS, Kinesis, Firehose, CodeBuild, CodePipeline, Inspector and SageMaker targets answer NotImplemented; " +
	"a Step Functions execution's name is understudy's; " +
	"Lambda, SQS, SNS and Step Functions targets in another Region " +
	"answer NotImplemented once the expression, time zone, window, dates and target are validated; " +
	"KmsKeyArn is stored and echoed, the payload is not encrypted, and under iam.enforce=enforce the caller's KMS permissions at create and update and the execution role's kms:Decrypt at invocation are evaluated against identity policies and the key's policy, which for a key understudy's KMS does not hold allows nothing; " +
	"the execution role's trust is checked only under iam.enforce=enforce, at create, update and invocation, so under off and advise a missing or untrusting role still delivers; " +
	"ClientToken is accepted and not used for idempotency; the 10,000,000-schedule quota is not counted; " +
	"unrecorded, understudy's choice: a target ARN of a service with no templated target is refused at create with ValidationException, " +
	"rate units are accepted singular or plural with any value, a rate schedule without StartDate first fires at the write instant, an absent Input delivers {}, " +
	"an at() in the past fires at once, a FLEXIBLE window delays each invocation by a seeded whole-second offset inside it, " +
	"client-side target errors go to the dead-letter queue without retries and one execution id covers an invocation's retries, " +
	"a dead letter's TARGET_ARN is the universal aws-sdk form and its body the payload after substitution, " +
	"an EventBridge target's event has no resources and its time is the delivery instant, an SNS target's message has no Subject, " +
	"DELETE removes the schedule in the write that hands its last invocation over (before any retry), a fired at() with NONE stays with its State unchanged, " +
	"parameter blocks that do not match the target are stored, echoed and ignored, a MaximumWindowInMinutes sent with OFF is ignored, " +
	"Input on a Lambda, Step Functions or EventBridge target is not checked for JSON at create, Input is limited to 262,144 bytes, " +
	"a rate schedule whose StartDate has passed first fires at its first interval at or after the write instant, an at() fires at its seconds, a rate ignores its time zone, " +
	"a qualified Lambda target's version or alias is passed as Invoke's Qualifier, " +
	"a Step Functions version or alias ARN is passed to StartExecution as given, " +
	"an event bus target in another Region or without EventBridgeParameters is refused with ValidationException, " +
	"checks run in the order expression, time zone, window, dates and the future-fire check, target, group, role trust, iam:PassRole, KMS, " +
	"iam:PassRole carries iam:PassedToService scheduler.amazonaws.com, " +
	"under iam.enforce=enforce an execution role that cannot be assumed or may not decrypt at invocation fails the invocation before its target call as a client-side target error does, " +
	"a present date in the future-fire text renders as RFC 3339 UTC, and every refusal message except the expression, future-fire, role-trust and missing-group texts is understudy's own"

// New binds the Scheduler operations built so far (scheduler.md 2.2) and
// registers the fire timer; the rest stay unbound and answer 501 until
// their tickets build them.
func New(d kernel.ServiceDeps, o Options) *Service {
	s := &Service{opts: o, k: d.Kernel, ids: d.IDs, cells: store.NewPartitioned(d.Kernel.Gate(), codec(), newState)}
	s.restart()
	s.fire = d.Kernel.Timers().Handle(TimerFire, s.onFire)
	// Each operation is asked about its own action at step 8 on the
	// schedule or group ARN, "*" for the two lists, plus what WithActions
	// declares, never the whole generated list (05-iam; scheduler.md 9,
	// 14.11). iam:PassRole, role trust, KMS and DeleteScheduleGroup's
	// per-member scheduler:DeleteSchedule are asked in the handlers
	// (authorize.go).
	b := schedulerapi.NewBinder(d.Binder)
	b.CreateSchedule(s.createSchedule, registry.Partial{Note: createNote},
		kernel.WithResources(func(c *kernel.Call, in *schedulerapi.CreateScheduleInput) []aws.ARN {
			return scheduleResource(c, in.GroupName, in.Name)
		}))
	b.UpdateSchedule(s.updateSchedule, registry.Partial{Note: "as CreateSchedule; every member is replaced and omitted ones take their defaults; " +
		"unrecorded, understudy's choice: an update that leaves the expression, time zone, dates, window and State unchanged keeps the next fire, any other restarts the plan at the write instant"},
		kernel.WithResources(func(c *kernel.Call, in *schedulerapi.UpdateScheduleInput) []aws.ARN {
			return scheduleResource(c, in.GroupName, in.Name)
		}))
	b.ListSchedules(s.listSchedules, registry.Partial{Note: "unrecorded, understudy's choice: schedules in group then name order, 100 per page without MaxResults"})
	b.GetSchedule(s.getSchedule, registry.Partial{Note: "unrecorded, understudy's choice: ScheduleExpressionTimezone, Description, StartDate and EndDate are absent when they were not sent, " +
		"MaximumWindowInMinutes is absent under OFF, State defaults to ENABLED, and CreationDate and LastModificationDate are whole seconds"},
		kernel.WithResources(func(c *kernel.Call, in *schedulerapi.GetScheduleInput) []aws.ARN {
			return scheduleResource(c, in.GroupName, in.Name)
		}))
	b.DeleteSchedule(s.deleteSchedule, registry.Partial{Note: "unrecorded, understudy's choice: ClientToken is accepted and not used for idempotency"},
		kernel.WithResources(func(c *kernel.Call, in *schedulerapi.DeleteScheduleInput) []aws.ARN {
			return scheduleResource(c, in.GroupName, in.Name)
		}))
	b.CreateScheduleGroup(s.createScheduleGroup, registry.Partial{Note: "unrecorded, understudy's choice: ClientToken is accepted and not used for idempotency, " +
		"creating default or an existing name answers ConflictException, and the 500-group quota answers ServiceQuotaExceededException with understudy's message"},
		kernel.WithResources(func(c *kernel.Call, in *schedulerapi.CreateScheduleGroupInput) []aws.ARN {
			return groupResource(c, in.Name)
		}),
		// A tagged create also needs scheduler:TagResource (scheduler.md 2.2, 14.8).
		kernel.WithActions(func(_ *kernel.Call, in *schedulerapi.CreateScheduleGroupInput) []registry.IAMAction {
			if len(in.Tags) == 0 {
				return nil
			}
			return []registry.IAMAction{schedulerAction("TagResource")}
		}))
	b.GetScheduleGroup(s.getScheduleGroup, registry.Partial{Note: "the default group always reads ACTIVE; unrecorded, understudy's choice: " +
		"its CreationDate and LastModificationDate are the instant the emulator's Scheduler state was created, loaded or reset"},
		kernel.WithResources(func(c *kernel.Call, in *schedulerapi.GetScheduleGroupInput) []aws.ARN {
			return groupResource(c, in.Name)
		}))
	b.ListScheduleGroups(s.listScheduleGroups, registry.Partial{Note: "unrecorded, understudy's choice: the default group is listed first, then groups in name order, 100 per page without MaxResults"})
	b.DeleteScheduleGroup(s.deleteScheduleGroup, registry.Partial{Note: "the group and its schedules are deleted before the call returns, so DELETING is never reported (ARCHITECTURE.md 9, provisioning settle); " +
		"unrecorded, understudy's choice: ClientToken is accepted and not used for idempotency, and the default group answers ValidationException"},
		kernel.WithResources(func(c *kernel.Call, in *schedulerapi.DeleteScheduleGroupInput) []aws.ARN {
			return groupResource(c, in.Name)
		}),
		// scheduler:DeleteSchedule is a per-member action on each schedule's
		// ARN, not the group's (SCH-10 step 03): the handler authorizes it
		// once per member (authorizeMembers), so step 8 asks about none.
		kernel.WithoutSecondaryActions())
	b.TagResource(s.tagResource, registry.Partial{Note: "schedule groups only, as AWS; unrecorded, understudy's choice: the default group takes tags"},
		kernel.WithResources(func(_ *kernel.Call, in *schedulerapi.TagResourceInput) []aws.ARN {
			return taggedResource(in.ResourceArn)
		}))
	b.UntagResource(s.untagResource, registry.Partial{Note: "schedule groups only, as AWS; unrecorded, understudy's choice: " +
		"a key the group does not carry is ignored with 200, and the default group can be untagged"},
		kernel.WithResources(func(_ *kernel.Call, in *schedulerapi.UntagResourceInput) []aws.ARN {
			return taggedResource(in.ResourceArn)
		}))
	b.ListTagsForResource(s.listTagsForResource, registry.Partial{Note: "schedule groups only, as AWS; unrecorded, understudy's choice: tags are listed in insertion order and an updated key keeps its place"},
		kernel.WithResources(func(_ *kernel.Call, in *schedulerapi.ListTagsForResourceInput) []aws.ARN {
			return taggedResource(in.ResourceArn)
		}))
	return s
}
