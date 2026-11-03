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

// createNote is CreateSchedule's coverage note: what is not invoked, the
// deviations from what SCH recorded on 2026-11-03, and the unrecorded
// choices the binary makes (invariant 3; scheduler.md 14.1).
const createNote = "templated Lambda, SQS, SNS, EventBridge event bus and Step Functions targets in the schedule's Region are invoked on the emulator clock as the execution role; " +
	"universal (aws-sdk) targets and ECS, Kinesis, Firehose, CodeBuild, CodePipeline, Inspector and SageMaker targets answer NotImplemented; " +
	"a Step Functions execution's name is understudy's; " +
	"Lambda, SQS, SNS and Step Functions targets in another Region " +
	"answer NotImplemented once the expression, time zone, window, dates and target are validated; " +
	"KmsKeyArn is stored and echoed, the payload is not encrypted, and under iam.enforce=enforce the caller's KMS permissions at create and update and the execution role's kms:Decrypt at invocation are evaluated against identity policies and the key's policy, which for a key understudy's KMS does not hold allows nothing, " +
	"so under enforce a KmsKeyArn naming no key is refused at create where AWS accepted one (SCH-04); " +
	"the execution role's trust is checked only under iam.enforce=enforce, at create, update and invocation, so under off and advise a missing or untrusting role still delivers; " +
	"the 10,000,000-schedule quota is not counted; " +
	"cron() takes the rules' grammar, so years outside 1970-2199 are refused as malformed where AWS refuses them as never firing (SCH-03); " +
	"unrecorded, understudy's choice: a FLEXIBLE window delays each invocation by a seeded whole-second offset inside it, " +
	"one execution id covers an invocation's retries, the Scheduled Event payload's id is the execution id and a Step Functions target without Input receives it too, " +
	"a dead letter of a Lambda, SNS, Step Functions or event bus target carries the payload as its body, " +
	"DELETE removes the schedule in the write that hands its last invocation over (before any retry), " +
	"a parameter block other than SqsParameters on a target that does not take it is refused as SqsParameters on a Lambda target is, " +
	"Step Functions and event bus targets refuse Input that is not JSON as Lambda targets do, with the decoder's line and column, " +
	"a rate schedule whose StartDate has passed first fires at its first interval at or after the write instant, an at() fires at its seconds, a rate ignores its time zone, " +
	"a qualified Lambda target's version or alias is passed as Invoke's Qualifier, " +
	"a Step Functions version or alias ARN is passed to StartExecution as given, " +
	"an event bus target in another Region or without EventBridgeParameters is refused with ValidationException, " +
	"the flexible window is checked after the expression and before the dates, then iam:PassRole and KMS after the role's trust, " +
	"under iam.enforce=enforce an execution role that cannot be assumed or may not decrypt at invocation fails the invocation before its target call as a client-side target error does, " +
	"and the refusal of a target ARN that is not a templated target's resource carries understudy's own message"

// New binds the Scheduler operations built so far (scheduler.md 2.2) and
// registers the fire timer; the rest stay unbound and answer 501 until
// their tickets build them.
func New(d kernel.ServiceDeps, o Options) *Service {
	s := &Service{opts: o, k: d.Kernel, ids: d.IDs, cells: store.NewPartitioned(d.Kernel.Gate(), codec(), newState)}
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
	b.ListSchedules(s.listSchedules, registry.Partial{Note: "schedules come in group then name order, where AWS's order is neither name nor creation order (SCH-01); " +
		"NextToken is understudy's own form; unrecorded, understudy's choice: 100 per page without MaxResults"})
	b.GetSchedule(s.getSchedule, registry.Partial{Note: "members never set are left out where AWS sends them as explicit JSON null, which SDKs read alike (SCH-01)"},
		kernel.WithResources(func(c *kernel.Call, in *schedulerapi.GetScheduleInput) []aws.ARN {
			return scheduleResource(c, in.GroupName, in.Name)
		}))
	b.DeleteSchedule(s.deleteSchedule, registry.Partial{Note: "unrecorded, understudy's choice: ClientToken is accepted and not used for idempotency"},
		kernel.WithResources(func(c *kernel.Call, in *schedulerapi.DeleteScheduleInput) []aws.ARN {
			return scheduleResource(c, in.GroupName, in.Name)
		}))
	b.CreateScheduleGroup(s.createScheduleGroup, registry.Partial{Note: "a name held by a group still DELETING in AWS is free at once, since deletes settle before they return (SCH-06); " +
		"unrecorded, understudy's choice: ClientToken is accepted and not used for idempotency, " +
		"and the 500-group quota answers ServiceQuotaExceededException with understudy's message"},
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
	b.GetScheduleGroup(s.getScheduleGroup, registry.Partial{Note: "a deleted group is gone at once, never DELETING (ARCHITECTURE.md 9, provisioning settle); " +
		"members AWS sends as explicit JSON null, the default group's ARN and dates, are left out (SCH-06)"},
		kernel.WithResources(func(c *kernel.Call, in *schedulerapi.GetScheduleGroupInput) []aws.ARN {
			return groupResource(c, in.Name)
		}))
	b.ListScheduleGroups(s.listScheduleGroups, registry.Partial{Note: "the default group is listed first, with its ARN and no dates, as AWS (SCH-06); " +
		"unrecorded, understudy's choice: the other groups in name order, 100 per page without MaxResults"})
	b.DeleteScheduleGroup(s.deleteScheduleGroup, registry.Partial{Note: "the group and its schedules are deleted before the call returns, so DELETING is never reported, " +
		"where AWS reports it for some seconds (SCH-06; ARCHITECTURE.md 9, provisioning settle); " +
		"unrecorded, understudy's choice: ClientToken is accepted and not used for idempotency, and scheduler:DeleteSchedule is asked only of a group that holds schedules"},
		kernel.WithResources(func(c *kernel.Call, in *schedulerapi.DeleteScheduleGroupInput) []aws.ARN {
			return groupResource(c, in.Name)
		}),
		// scheduler:DeleteSchedule is asked on schedule/<group>/*, not the
		// group's ARN (SCH-10 step 03): the handler authorizes it
		// (authorizeMembers), so step 8 asks about none.
		kernel.WithoutSecondaryActions())
	b.TagResource(s.tagResource, registry.Partial{Note: "schedule groups only, the default group included, as AWS (SCH-06)"},
		kernel.WithResources(func(_ *kernel.Call, in *schedulerapi.TagResourceInput) []aws.ARN {
			return taggedResource(in.ResourceArn)
		}))
	b.UntagResource(s.untagResource, registry.Partial{Note: "schedule groups only, the default group included, and a key the group does not carry is ignored with 200, as AWS (SCH-06)"},
		kernel.WithResources(func(_ *kernel.Call, in *schedulerapi.UntagResourceInput) []aws.ARN {
			return taggedResource(in.ResourceArn)
		}))
	b.ListTagsForResource(s.listTagsForResource, registry.Partial{Note: "schedule groups only, tags sorted by key, as AWS (SCH-06)"},
		kernel.WithResources(func(_ *kernel.Call, in *schedulerapi.ListTagsForResourceInput) []aws.ARN {
			return taggedResource(in.ResourceArn)
		}))
	return s
}
