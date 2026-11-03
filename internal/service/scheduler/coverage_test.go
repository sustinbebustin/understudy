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

// createNote names what is not invoked, the deviations from what SCH
// recorded on 2026-11-03 (scheduler.md 15) and the unrecorded choices the
// binary makes (invariant 3). Key policies are evaluated under enforce
// (iam-enforcement-surfaces.md 3, 6.3).
const createNote = targetsNote + "; Lambda, SQS, SNS and Step Functions targets in another Region " +
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

// wantCoverage is every operation's mark: all 12 bound Partial with these
// exact notes (scheduler.md 14.1).
var wantCoverage = map[string]registry.BindCoverage{
	"CreateSchedule": registry.Partial{Note: createNote},
	"UpdateSchedule": registry.Partial{Note: "as CreateSchedule; every member is replaced and omitted ones take their defaults; " +
		"unrecorded, understudy's choice: an update that leaves the expression, time zone, dates, window and State unchanged keeps the next fire, any other restarts the plan at the write instant"},
	"GetSchedule": registry.Partial{Note: "members never set are left out where AWS sends them as explicit JSON null, which SDKs read alike (SCH-01)"},
	"ListSchedules": registry.Partial{Note: "schedules come in group then name order, where AWS's order is neither name nor creation order (SCH-01); " +
		"NextToken is understudy's own form; unrecorded, understudy's choice: 100 per page without MaxResults"},
	"DeleteSchedule": registry.Partial{Note: "unrecorded, understudy's choice: ClientToken is accepted and not used for idempotency"},
	"CreateScheduleGroup": registry.Partial{Note: "a name held by a group still DELETING in AWS is free at once, since deletes settle before they return (SCH-06); " +
		"unrecorded, understudy's choice: ClientToken is accepted and not used for idempotency, " +
		"and the 500-group quota answers ServiceQuotaExceededException with understudy's message"},
	"GetScheduleGroup": registry.Partial{Note: "a deleted group is gone at once, never DELETING (ARCHITECTURE.md 9, provisioning settle); " +
		"members AWS sends as explicit JSON null, the default group's ARN and dates, are left out (SCH-06)"},
	"ListScheduleGroups": registry.Partial{Note: "the default group is listed first, with its ARN and no dates, as AWS (SCH-06); " +
		"unrecorded, understudy's choice: the other groups in name order, 100 per page without MaxResults"},
	"DeleteScheduleGroup": registry.Partial{Note: "the group and its schedules are deleted before the call returns, so DELETING is never reported, " +
		"where AWS reports it for some seconds (SCH-06; ARCHITECTURE.md 9, provisioning settle); " +
		"unrecorded, understudy's choice: ClientToken is accepted and not used for idempotency, and scheduler:DeleteSchedule is asked only of a group that holds schedules"},
	"TagResource":         registry.Partial{Note: "schedule groups only, the default group included, as AWS (SCH-06)"},
	"UntagResource":       registry.Partial{Note: "schedule groups only, the default group included, and a key the group does not carry is ignored with 200, as AWS (SCH-06)"},
	"ListTagsForResource": registry.Partial{Note: "schedule groups only, tags sorted by key, as AWS (SCH-06)"},
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

// The six switches of scheduler.md 14.12, in order, with their values,
// labels, refs, records, operations and NoScenario settles: the five SCH
// recorded on 2026-11-03 are aws-recorded with the recorded value as the
// default, and scheduler.flexible-window.offset, which a recording only
// bounds, stays inferred.
func TestSwitches(t *testing.T) {
	createUpdate := []string{"scheduler.CreateSchedule", "scheduler.UpdateSchedule"}
	all := make([]string, 0, len(schedulerapi.Service().Operations))
	for _, op := range schedulerapi.Service().Operations {
		all = append(all, "scheduler."+op.ID.Name)
	}
	slices.Sort(all)
	want := []struct {
		name, value, ref, record string
		label                    evidence.Label
		ops                      []string
	}{
		{"scheduler.rate.unit-agreement", "any", "docs/research/scheduler.md#52-rate", "SCH-03", evidence.AWSRecorded, createUpdate},
		{"scheduler.input.default", "scheduled-event", "docs/research/scheduler.md#63-input-the-default-payload-and-context-attributes", "SCH-07", evidence.AWSRecorded, createUpdate},
		{"scheduler.at.past-due", "fire-at-once", "docs/research/scheduler.md#51-at", "SCH-09", evidence.AWSRecorded, createUpdate},
		{"scheduler.flexible-window.offset", "seeded-uniform", "docs/research/scheduler.md#56-precision-and-flexible-time-windows", "SCH-09", evidence.Inferred, createUpdate},
		{"scheduler.retry.permanent-errors", "not-retried", "docs/research/scheduler.md#71-retrypolicy", "SCH-08", evidence.AWSRecorded, createUpdate},
		{"scheduler.denial.status", "403", "docs/research/iam-enforcement-surfaces.md#123-error-identities-to-emit", "SCH-10", evidence.AWSRecorded, all},
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
			if e.Name != w.name || e.Value != w.value || e.Ref != w.ref || e.Record != w.record || e.Label != w.label || !slices.Equal(ops, wops) {
				h.Errorf("switch %d = %+v\nwant name %s, value %s, ref %s, record %s, label %s, ops %v", i, e, w.name, w.value, w.ref, w.record, w.label, wops)
			}
			if ns, ok := e.Settles.(fidelity.NoScenario); !ok || !strings.Contains(ns.Reason, "2026-11-03") {
				h.Errorf("%s Settles = %#v, want NoScenario naming the 2026-11-03 recording", e.Name, e.Settles)
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
	if scheduler.InputDefault().Value != "scheduled-event" {
		t.Errorf("InputDefault = %q, want scheduled-event", scheduler.InputDefault().Value)
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
