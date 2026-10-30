package scheduler_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/schedexpr"
	"github.com/sustinbebustin/understudy/internal/service/scheduler"
)

// targetsNote is the spec's <targets> clause with ticket 10 and the SNS
// tail landed: SNS and Step Functions targets are invoked (spec "Coverage
// marks"; scheduler.md 14.1).
const targetsNote = "templated Lambda, SQS, SNS, EventBridge event bus and Step Functions targets in the schedule's Region are invoked on the emulator clock as the execution role; " +
	"universal (aws-sdk) targets and ECS, Kinesis, Firehose, CodeBuild, CodePipeline, Inspector and SageMaker targets answer NotImplemented; " +
	"a Step Functions execution's name is understudy's"

const createNote = targetsNote + "; KmsKeyArn is stored and echoed, the payload is not encrypted, and under iam.enforce=enforce the caller's KMS permissions are evaluated against identity policies only (key policies are advised); " +
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
	"Input on a Lambda or EventBridge target is not checked for JSON at create, Input is limited to 262,144 bytes, " +
	"and every refusal message except the expression, future-fire, role-trust and missing-group texts is understudy's own"

// wantCoverage is the spec's "Coverage marks" table: all 12 operations
// bound Partial with these exact notes (scheduler.md 14.1).
var wantCoverage = map[string]registry.BindCoverage{
	"CreateSchedule": registry.Partial{Note: createNote},
	"UpdateSchedule": registry.Partial{Note: "as CreateSchedule; every member is replaced and omitted ones take their defaults; " +
		"unrecorded, understudy's choice: an update that leaves the expression, time zone, dates, window and State unchanged keeps the next fire, any other restarts the plan at the write instant"},
	"GetSchedule": registry.Partial{Note: "unrecorded, understudy's choice: ScheduleExpressionTimezone, Description, StartDate and EndDate are absent when they were not sent, " +
		"MaximumWindowInMinutes is absent under OFF, State defaults to ENABLED, and CreationDate and LastModificationDate are whole seconds"},
	"ListSchedules":  registry.Partial{Note: "unrecorded, understudy's choice: schedules in group then name order, 100 per page without MaxResults"},
	"DeleteSchedule": registry.Partial{Note: "unrecorded, understudy's choice: ClientToken is accepted and not used for idempotency"},
	"CreateScheduleGroup": registry.Partial{Note: "unrecorded, understudy's choice: ClientToken is accepted and not used for idempotency, " +
		"creating default or an existing name answers ConflictException, and the 500-group quota answers ServiceQuotaExceededException with understudy's message"},
	"GetScheduleGroup": registry.Partial{Note: "the default group always reads ACTIVE; unrecorded, understudy's choice: " +
		"its CreationDate and LastModificationDate are the instant the emulator's Scheduler state was created, loaded or reset"},
	"ListScheduleGroups": registry.Partial{Note: "unrecorded, understudy's choice: the default group is listed first, then groups in name order, 100 per page without MaxResults"},
	"DeleteScheduleGroup": registry.Partial{Note: "the group and its schedules are deleted before the call returns, so DELETING is never reported (ARCHITECTURE.md 9, provisioning settle); " +
		"unrecorded, understudy's choice: ClientToken is accepted and not used for idempotency, and the default group answers ValidationException"},
	"TagResource": registry.Partial{Note: "schedule groups only, as AWS; unrecorded, understudy's choice: the default group takes tags"},
	"UntagResource": registry.Partial{Note: "schedule groups only, as AWS; unrecorded, understudy's choice: " +
		"a key the group does not carry is ignored with 200, and the default group can be untagged"},
	"ListTagsForResource": registry.Partial{Note: "schedule groups only, as AWS; unrecorded, understudy's choice: tags are listed in insertion order and an updated key keeps its place"},
}

func TestOperationCoverage(t *testing.T) {
	run(t, func(h *harness) {
		if n := len(schedulerapi.Service().Operations); n != len(wantCoverage) {
			h.Fatalf("the model has %d operations, want the 12 of scheduler.md 2.2", n)
		}
		for _, op := range schedulerapi.Service().Operations {
			status := h.Kernel.Bindings().Status(op)
			if b, ok := status.(kernel.Bound); !ok || b.Coverage != wantCoverage[op.ID.Name] {
				h.Errorf("%s is %#v\nwant bound as %#v", op.ID.Name, status, wantCoverage[op.ID.Name])
			}
		}
		if got := kerneltest.UndeclaredActions(h.Kernel, schedulerapi.Service()); len(got) != 0 {
			h.Errorf("operations recording their whole generated action list: %v (05-iam: only the primary action plus WithActions)", got)
		}
	})
}

// The six switches of the spec's switch table, in order, with their
// values, labels, refs, records, operations and NoScenario settles
// (spec "Fidelity switches"; contract section 3; scheduler.md 14.12).
func TestSwitches(t *testing.T) {
	createUpdate := []string{"scheduler.CreateSchedule", "scheduler.UpdateSchedule"}
	all := make([]string, 0, len(schedulerapi.Service().Operations))
	for _, op := range schedulerapi.Service().Operations {
		all = append(all, "scheduler."+op.ID.Name)
	}
	slices.Sort(all)
	want := []struct {
		name, value, ref, record string
		ops                      []string
	}{
		{"scheduler.rate.unit-agreement", "any", "docs/research/scheduler.md#52-rate", "SCH-03", createUpdate},
		{"scheduler.input.default", "empty-json-object", "docs/research/scheduler.md#63-input-the-default-payload-and-context-attributes", "SCH-07", createUpdate},
		{"scheduler.at.past-due", "fire-at-once", "docs/research/scheduler.md#51-at", "SCH-09", createUpdate},
		{"scheduler.flexible-window.offset", "seeded-uniform", "docs/research/scheduler.md#56-precision-and-flexible-time-windows", "SCH-09", createUpdate},
		{"scheduler.retry.permanent-errors", "not-retried", "docs/research/scheduler.md#71-retrypolicy", "SCH-08", createUpdate},
		{"scheduler.denial.status", "403", "docs/research/iam-enforcement-surfaces.md#123-error-identities-to-emit", "SCH-10", all},
	}
	run(t, func(h *harness) {
		got := h.svc.Switches()
		if len(got) != len(want) {
			h.Fatalf("Switches() lists %d entries, want %d", len(got), len(want))
		}
		for i, w := range want {
			e := got[i]
			ops := slices.Sorted(slices.Values(e.Ops))
			wops := slices.Sorted(slices.Values(w.ops))
			if e.Name != w.name || e.Value != w.value || e.Ref != w.ref || !strings.HasPrefix(e.Record, w.record) || e.Label != evidence.Inferred || !slices.Equal(ops, wops) {
				h.Errorf("switch %d = %+v\nwant name %s, value %s, ref %s, record %s, label inferred, ops %v", i, e, w.name, w.value, w.ref, w.record, wops)
			}
			ns, ok := e.Settles.(fidelity.NoScenario)
			if !ok || !strings.HasPrefix(ns.Reason, "unrecorded: ") || !strings.Contains(ns.Reason, w.record) {
				h.Errorf("%s Settles = %#v, want NoScenario \"unrecorded: %s ...\"", e.Name, e.Settles, w.record)
			}
		}
	})
}

// Each switch function reports the Options value it was given, so a flip
// test builds the service with one field set to the other side (contract
// section 3).
func TestSwitchesFollowOptions(t *testing.T) {
	def := scheduler.Options{}
	flipped := scheduler.Options{
		RateUnits: schedexpr.SingularIffOne, AtPastDue: scheduler.PastDueNever,
		FlexibleOffset: scheduler.OffsetWindowStart, PermanentErrors: delivery.PermanentRetried,
	}
	if scheduler.UnitAgreement(def).Value != schedexpr.AnyUnitAgreement || scheduler.UnitAgreement(flipped).Value != schedexpr.SingularIffOne {
		t.Error("UnitAgreement does not follow Options.RateUnits")
	}
	if scheduler.PastDue(def).Value != scheduler.PastDueFireAtOnce || scheduler.PastDue(flipped).Value != scheduler.PastDueNever {
		t.Error("PastDue does not follow Options.AtPastDue")
	}
	if scheduler.FlexOffset(def).Value != scheduler.OffsetSeededUniform || scheduler.FlexOffset(flipped).Value != scheduler.OffsetWindowStart {
		t.Error("FlexOffset does not follow Options.FlexibleOffset")
	}
	if scheduler.PermanentErrors(def).Value != delivery.PermanentNotRetried || scheduler.PermanentErrors(flipped).Value != delivery.PermanentRetried {
		t.Error("PermanentErrors does not follow Options.PermanentErrors")
	}
	if scheduler.InputDefault().Value != "empty-json-object" {
		t.Errorf("InputDefault = %q, want empty-json-object", scheduler.InputDefault().Value)
	}
	if scheduler.DenialStatus().Value != schedulerapi.IdentityAccessDeniedException().Status {
		t.Errorf("DenialStatus = %d, the generated AccessDeniedException identity is %d", scheduler.DenialStatus().Value, schedulerapi.IdentityAccessDeniedException().Status)
	}
	if scheduler.PastDueNever.String() != "never" || scheduler.OffsetWindowStart.String() != "window-start" {
		t.Errorf("switch strings = %q, %q; want never, window-start", scheduler.PastDueNever, scheduler.OffsetWindowStart)
	}
	if scheduler.TimerFire != "scheduler.fire" {
		t.Errorf("TimerFire = %q, want scheduler.fire (contract section 1)", scheduler.TimerFire)
	}
}

// The service is a kernel.Stateful named scheduler at schema version 1
// (spec "Contract").
func TestStatefulIdentity(t *testing.T) {
	run(t, func(h *harness) {
		var s kernel.Stateful = h.svc
		if s.Name() != "scheduler" || s.SchemaVersion() != 1 {
			h.Errorf("Name, SchemaVersion = %s, %d; want scheduler, 1", s.Name(), s.SchemaVersion())
		}
	})
}
