package scheduler

import (
	"net/http"

	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/schedexpr"
)

// writeOps are the operations that parse a schedule's plan.
func writeOps() []string { return []string{"scheduler.CreateSchedule", "scheduler.UpdateSchedule"} }

// boundOps are the 12 operations the service binds; scheduler.denial.status
// governs every one.
func boundOps() []string {
	return []string{
		"scheduler.CreateSchedule", "scheduler.GetSchedule", "scheduler.UpdateSchedule", "scheduler.DeleteSchedule",
		"scheduler.ListSchedules", "scheduler.CreateScheduleGroup", "scheduler.GetScheduleGroup",
		"scheduler.ListScheduleGroups", "scheduler.DeleteScheduleGroup", "scheduler.TagResource",
		"scheduler.UntagResource", "scheduler.ListTagsForResource",
	}
}

// Switches lists the behaviors chosen without AWS-recorded evidence, with
// the values s was built with (scheduler.md 14.12).
func (s *Service) Switches() []fidelity.Entry {
	return []fidelity.Entry{
		UnitAgreement(s.opts).Entry(), InputDefault().Entry(), PastDue(s.opts).Entry(),
		FlexOffset(s.opts).Entry(), PermanentErrors(s.opts).Entry(), DenialStatus().Entry(),
	}
}

// UnitAgreement is scheduler.rate.unit-agreement: whether a rate's unit
// must be singular exactly when its value is 1, as the rules grammar
// requires, or may be any of the six words with any value, as the API
// reference and the census suggest (scheduler.md 5.2, 12).
func UnitAgreement(o Options) fidelity.Switch[schedexpr.RateUnits] {
	return fidelity.Switch[schedexpr.RateUnits]{
		Name:    "scheduler.rate.unit-agreement",
		Value:   o.RateUnits,
		Label:   evidence.Inferred,
		Ref:     "docs/research/scheduler.md#52-rate",
		Record:  "SCH-03",
		Ops:     writeOps(),
		Settles: fidelity.NoScenario{Reason: "unrecorded: SCH-03 not yet recorded (scheduler.md 15.4)"},
	}
}

// InputDefault is scheduler.input.default: the payload delivered when a
// target has no Input, "{}" for "empty-json-object" (scheduler.md 6.3).
func InputDefault() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:    "scheduler.input.default",
		Value:   "empty-json-object",
		Label:   evidence.Inferred,
		Ref:     "docs/research/scheduler.md#63-input-the-default-payload-and-context-attributes",
		Record:  "SCH-07",
		Ops:     writeOps(),
		Settles: fidelity.NoScenario{Reason: "unrecorded: SCH-07 not yet recorded (scheduler.md 15.8)"},
	}
}

// PastDue is scheduler.at.past-due: what an at() whose time has passed
// does at create, update and enable (scheduler.md 4.6, 5.1).
func PastDue(o Options) fidelity.Switch[AtPastDue] {
	return fidelity.Switch[AtPastDue]{
		Name:    "scheduler.at.past-due",
		Value:   o.AtPastDue,
		Label:   evidence.Inferred,
		Ref:     "docs/research/scheduler.md#51-at",
		Record:  "SCH-09",
		Ops:     writeOps(),
		Settles: fidelity.NoScenario{Reason: "unrecorded: SCH-09 not yet recorded (scheduler.md 15.10)"},
	}
}

// FlexOffset is scheduler.flexible-window.offset: where inside a FLEXIBLE
// window an invocation is due (scheduler.md 5.6, 14.3.7). AWS documents no
// distribution; SCH-09 only bounds the offsets.
func FlexOffset(o Options) fidelity.Switch[FlexibleOffset] {
	return fidelity.Switch[FlexibleOffset]{
		Name:    "scheduler.flexible-window.offset",
		Value:   o.FlexibleOffset,
		Label:   evidence.Inferred,
		Ref:     "docs/research/scheduler.md#56-precision-and-flexible-time-windows",
		Record:  "SCH-09",
		Ops:     writeOps(),
		Settles: fidelity.NoScenario{Reason: "unrecorded: SCH-09 not yet recorded (scheduler.md 15.10), and it bounds the offsets only"},
	}
}

// PermanentErrors is scheduler.retry.permanent-errors: whether a
// client-side target error goes to the dead-letter queue at once or is
// retried like a server-side one (scheduler.md 7.1).
func PermanentErrors(o Options) fidelity.Switch[delivery.PermanentErrors] {
	return fidelity.Switch[delivery.PermanentErrors]{
		Name:    "scheduler.retry.permanent-errors",
		Value:   o.PermanentErrors,
		Label:   evidence.Inferred,
		Ref:     "docs/research/scheduler.md#71-retrypolicy",
		Record:  "SCH-08",
		Ops:     writeOps(),
		Settles: fidelity.NoScenario{Reason: "unrecorded: SCH-08 not yet recorded (scheduler.md 15.9)"},
	}
}

// DenialStatus is scheduler.denial.status: the HTTP status of Scheduler's
// AccessDeniedException, the generic identity the overlay names
// (iam-enforcement-surfaces.md 12.3; scheduler.md 2.4).
func DenialStatus() fidelity.Switch[int] {
	return fidelity.Switch[int]{
		Name:    "scheduler.denial.status",
		Value:   http.StatusForbidden,
		Label:   evidence.Inferred,
		Ref:     "docs/research/iam-enforcement-surfaces.md#123-error-identities-to-emit",
		Record:  "SCH-10",
		Ops:     boundOps(),
		Settles: fidelity.NoScenario{Reason: "unrecorded: SCH-10 step 01 not yet recorded (scheduler.md 15.11)"},
	}
}
