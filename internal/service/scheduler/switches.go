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

// unreplayed is why no parity scenario settles a switch whose item is
// recorded: the replayer routes awsJson and awsQuery requests only, and
// Scheduler's are restJson1 (testdata/README.md, Replay scenarios).
func unreplayed(steps string) fidelity.Settles {
	return fidelity.NoScenario{Reason: "recorded in scheduler/SCH (" + steps + ", 2026-11-03); the replayer does not route Scheduler's restJson1 requests"}
}

// Switches lists the behaviors chosen without AWS-recorded evidence, and
// those SCH has since recorded, with the values s was built with
// (scheduler.md 14.12).
func (s *Service) Switches() []fidelity.Entry {
	return []fidelity.Entry{
		UnitAgreement(s.opts).Entry(), InputDefault().Entry(), PastDue(s.opts).Entry(),
		FlexOffset(s.opts).Entry(), PermanentErrors(s.opts).Entry(), DenialStatus().Entry(),
	}
}

// UnitAgreement is scheduler.rate.unit-agreement: whether a rate's unit
// must be singular exactly when its value is 1, as the rules grammar
// requires, or may be any of the six words with any value. AWS accepted
// rate(1 minutes), rate(5 minute), rate(1 hours) and rate(2 hour): any
// (SCH-03, recorded 2026-11-03; scheduler.md 5.2).
func UnitAgreement(o Options) fidelity.Switch[schedexpr.RateUnits] {
	return fidelity.Switch[schedexpr.RateUnits]{
		Name:    "scheduler.rate.unit-agreement",
		Value:   o.RateUnits,
		Label:   evidence.AWSRecorded,
		Ref:     "docs/research/scheduler.md#52-rate",
		Record:  "SCH-03",
		Ops:     writeOps(),
		Settles: unreplayed("03-e01 to 03-e25"),
	}
}

// InputDefault is scheduler.input.default: the payload delivered when a
// target has no Input. AWS sends SQS, SNS and Lambda targets the
// Scheduled Event envelope and an event bus target the detail {}:
// "scheduled-event" (SCH-07, recorded 2026-11-03; scheduler.md 6.3).
func InputDefault() fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:    "scheduler.input.default",
		Value:   "scheduled-event",
		Label:   evidence.AWSRecorded,
		Ref:     "docs/research/scheduler.md#63-input-the-default-payload-and-context-attributes",
		Record:  "SCH-07",
		Ops:     writeOps(),
		Settles: unreplayed("07-sqs-no-input, 07-sns-no-input, 07-lambda-no-input, 07-eventbridge-no-input"),
	}
}

// PastDue is scheduler.at.past-due: what an at() whose time has passed
// does at create, update and enable. AWS delivered one at once in both
// cases: fire-at-once (SCH-09 w3, w4, recorded 2026-11-03; scheduler.md
// 4.6, 5.1).
func PastDue(o Options) fidelity.Switch[AtPastDue] {
	return fidelity.Switch[AtPastDue]{
		Name:    "scheduler.at.past-due",
		Value:   o.AtPastDue,
		Label:   evidence.AWSRecorded,
		Ref:     "docs/research/scheduler.md#51-at",
		Record:  "SCH-09",
		Ops:     writeOps(),
		Settles: unreplayed("09-at-past, 09-enable-past"),
	}
}

// FlexOffset is scheduler.flexible-window.offset: where inside a FLEXIBLE
// window an invocation is due (scheduler.md 5.6, 14.3.7). AWS documents no
// distribution; SCH-09 recorded five offsets of 95 to 263 seconds in a
// 5-minute window, which bound it and do not decide it.
func FlexOffset(o Options) fidelity.Switch[FlexibleOffset] {
	return fidelity.Switch[FlexibleOffset]{
		Name:    "scheduler.flexible-window.offset",
		Value:   o.FlexibleOffset,
		Label:   evidence.Inferred,
		Ref:     "docs/research/scheduler.md#56-precision-and-flexible-time-windows",
		Record:  "SCH-09",
		Ops:     writeOps(),
		Settles: fidelity.NoScenario{Reason: "SCH-09 (recorded 2026-11-03) bounds the offsets only: five draws cannot settle a distribution"},
	}
}

// PermanentErrors is scheduler.retry.permanent-errors: whether a
// client-side target error goes to the dead-letter queue at once or is
// retried like a server-side one. AWS dead-lettered a missing queue, an
// access denial and SQS InvalidParameterValue at once, RETRY_ATTEMPTS 0:
// not-retried (SCH-08, recorded 2026-11-03; scheduler.md 7.1).
func PermanentErrors(o Options) fidelity.Switch[delivery.PermanentErrors] {
	return fidelity.Switch[delivery.PermanentErrors]{
		Name:    "scheduler.retry.permanent-errors",
		Value:   o.PermanentErrors,
		Label:   evidence.AWSRecorded,
		Ref:     "docs/research/scheduler.md#71-retrypolicy",
		Record:  "SCH-08",
		Ops:     writeOps(),
		Settles: unreplayed("08-recv-dlq-1 to 08-recv-dlq-3, in sqs/SCH"),
	}
}

// DenialStatus is scheduler.denial.status: the HTTP status of Scheduler's
// AccessDeniedException, the generic identity the overlay names. AWS
// answered 403 (SCH-10 10-01-denied-get, recorded 2026-11-03;
// iam-enforcement-surfaces.md 12.3; scheduler.md 2.4).
func DenialStatus() fidelity.Switch[int] {
	return fidelity.Switch[int]{
		Name:    "scheduler.denial.status",
		Value:   http.StatusForbidden,
		Label:   evidence.AWSRecorded,
		Ref:     "docs/research/iam-enforcement-surfaces.md#123-error-identities-to-emit",
		Record:  "SCH-10",
		Ops:     boundOps(),
		Settles: unreplayed("10-01-denied-get"),
	}
}
