package scheduler

import (
	"fmt"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/schedexpr"
)

// The refusal texts (scheduler.md 14.9.2), one table: each names its
// evidence label, and an inferred one the SCH case that records it.

// errInvalidExpression answers a malformed ScheduleExpression with
// "Invalid Schedule Expression <expr>.", the expression verbatim.
// Third-party-recorded: localstack@8b9a79f
// tests/aws/services/scheduler/test_scheduler.snapshot.json:L31-L302
// (scheduler.md 2.4).
func errInvalidExpression(err *schedexpr.Error) *awserr.Error {
	return schedulerapi.ErrValidationException(err.Message())
}

// errNoFutureFire answers a cron or rate schedule with no fire in
// [max(now, StartDate), EndDate): "The schedule expression <expr>, with
// StartDate <s> and EndDate <e>, is invalid because it will never schedule
// an invocation.", each date as localDateTime renders it and null when
// absent (SCH-03 03-e24, 03-past-cron-enabled, 03-past-cron-dates,
// recorded 2026-11-03; scheduler.md 2.4).
func errNoFutureFire(expr string, start, end *time.Time) *awserr.Error {
	render := func(d *time.Time) string {
		if d == nil {
			return "null"
		}
		return localDateTime(*d)
	}
	return schedulerapi.ErrValidationException("The schedule expression " + expr + ", with StartDate " + render(start) +
		" and EndDate " + render(end) + ", is invalid because it will never schedule an invocation.")
}

// localDateTime renders t in UTC without a zone, as Java's
// LocalDateTime.toString does: yyyy-MM-ddTHH:mm, then :ss unless the
// seconds and fraction are zero, then the milliseconds unless they are
// zero. AWS rendered "2020-01-01T00:00" and "2026-11-03T16:37:19.612"
// (SCH-03, recorded 2026-11-03; scheduler.md 2.4); the seconds-only form
// and the cut below the millisecond are inferred from that rule, since
// dates reach Scheduler with millisecond precision.
func localDateTime(t time.Time) string {
	t = t.UTC().Truncate(time.Millisecond)
	switch {
	case t.Nanosecond() != 0:
		return t.Format("2006-01-02T15:04:05.000")
	case t.Second() != 0:
		return t.Format("2006-01-02T15:04:05")
	}
	return t.Format("2006-01-02T15:04")
}

// errBadZone answers a ScheduleExpressionTimezone the IANA database does
// not name: "Invalid timezone <name>." (SCH-04 04-bad-timezone, recorded
// 2026-11-03; scheduler.md 2.4).
func errBadZone(name string) *awserr.Error {
	return schedulerapi.ErrValidationException("Invalid timezone " + name + ".")
}

// errFlexibleWithoutWindow answers FLEXIBLE without MaximumWindowInMinutes
// (SCH-04 04-flexible-no-window, recorded 2026-11-03; scheduler.md 2.4).
func errFlexibleWithoutWindow() *awserr.Error {
	return schedulerapi.ErrValidationException("MaximumWindowInMinutes must be provided when FlexibleTimeWindowMode is set to FLEXIBLE.")
}

// errOffWithWindow answers OFF with MaximumWindowInMinutes (SCH-04
// 04-off-with-window, recorded 2026-11-03; scheduler.md 2.4).
func errOffWithWindow() *awserr.Error {
	return schedulerapi.ErrValidationException("MaximumWindowInMinutes must not be provided when FlexibleTimeWindowMode is set to OFF.")
}

// errEndNotAfterStart answers an EndDate at or before StartDate, or before
// the request instant when no StartDate is sent (SCH-04
// 04-end-before-start, SCH-03 03-past-rate-end, recorded 2026-11-03;
// scheduler.md 2.4, 5.5).
func errEndNotAfterStart() *awserr.Error {
	return schedulerapi.ErrValidationException("The StartDate you specify must come before the EndDate.")
}

// errScheduleMissing answers a schedule the group does not hold:
// "Schedule <name> does not exist.", the group not named (SCH-02,
// recorded 2026-11-03; scheduler.md 2.4).
func errScheduleMissing(name string) *awserr.Error {
	return schedulerapi.ErrResourceNotFoundException("Schedule " + name + " does not exist.")
}

// errScheduleExists answers CreateSchedule of a name the group holds,
// whatever its ClientToken: "Schedule <name> already exists." (SCH-02,
// recorded 2026-11-03; scheduler.md 2.4).
func errScheduleExists(name string) *awserr.Error {
	return schedulerapi.ErrConflictException("Schedule " + name + " already exists.")
}

// errGroupMissing answers a schedule group the scope does not hold:
// "Schedule group <name> does not exist." (aws-doc
// managing-schedule-group-delete; scheduler.md 2.4, 14.9.2).
func errGroupMissing(name string) *awserr.Error {
	return schedulerapi.ErrResourceNotFoundException("Schedule group " + name + " does not exist.")
}

// errGroupExists answers CreateScheduleGroup of a name the scope holds
// (SCH-06 06-create-dup, recorded 2026-11-03; scheduler.md 3.2).
func errGroupExists(name string) *awserr.Error {
	return schedulerapi.ErrConflictException("Schedule group " + name + " already exists.")
}

// errDefaultGroup answers CreateScheduleGroup and DeleteScheduleGroup of
// default (SCH-06 06-create-default, 06-delete-default, recorded
// 2026-11-03; scheduler.md 3.1).
func errDefaultGroup() *awserr.Error {
	return schedulerapi.ErrValidationException("This operation cannot be performed on schedule group default.")
}

// errGroupQuota answers a CreateScheduleGroup past maxGroups
// (ServiceQuotaExceededException 402, model; aws-doc scheduler-quotas).
// UNVERIFIED (SCH-06), scheduler.md 10, 14.7.5: the text is inferred.
func errGroupQuota() *awserr.Error {
	return schedulerapi.ErrServiceQuotaExceededException(fmt.Sprintf("The account holds %d schedule groups in this Region, the default group included, "+
		"which is the quota; delete a schedule group with DeleteScheduleGroup before creating another.", maxGroups))
}

// errBadNextToken answers a NextToken no page of the listing op issued.
// UNVERIFIED (SCH-01, SCH-06), scheduler.md 3.2, 4.5: the code and text
// are inferred.
func errBadNextToken(op string) *awserr.Error {
	return schedulerapi.ErrValidationException("Invalid NextToken: it was not issued by a " + op + " page of this listing; start the listing again without NextToken.")
}

// errRoleUntrusted answers, under iam.enforce=enforce, an execution role
// that is missing or does not let scheduler.amazonaws.com assume it: "The
// execution role you provide must allow AWS EventBridge Scheduler to
// assume the role." Captured; the provider retries on exactly this text
// (tf schedule.go:L659-L665; scheduler.md 1 item 10, 2.4).
func errRoleUntrusted() *awserr.Error {
	return schedulerapi.ErrValidationException("The execution role you provide must allow AWS EventBridge Scheduler to assume the role.")
}

// errNotRole answers a Target.RoleArn that names no IAM role.
// UNVERIFIED (SCH-04), scheduler.md 4.7: the text is inferred.
func errNotRole(roleArn string) *awserr.Error {
	return schedulerapi.ErrValidationException("Target.RoleArn " + roleArn + " is not an IAM role ARN, arn:<partition>:iam::<account>:role/<name>.")
}

// errUnsupportedService answers, at create, a Target.Arn of a service
// with no templated target: "<service> is not a supported service for a
// target." (SCH-04 04-unsupported-service, recorded 2026-11-03; the
// troubleshooting page's "Invalid request provided: " prefix is not sent;
// scheduler.md 2.4, 6.1).
func errUnsupportedService(service string) *awserr.Error {
	return schedulerapi.ErrValidationException(service + " is not a supported service for a target.")
}

// errParametersNotSupported answers a parameter block that does not match
// the target: "Parameters <block> not supported for the target." (SCH-04
// 04-sqs-params-on-lambda, recorded 2026-11-03 for SqsParameters on a
// Lambda target; scheduler.md 6.1). The other blocks and targets are
// inferred to answer alike.
func errParametersNotSupported(block string) *awserr.Error {
	return schedulerapi.ErrValidationException("Parameters " + block + " not supported for the target.")
}

// errInputNotJSON answers a Lambda, Step Functions or event bus target
// whose Input is not well-formed JSON, line and column (1-based) being
// where the decoder stopped: "JSON syntax error in input for the target:
// [Source: REDACTED (`StreamReadFeature.INCLUDE_SOURCE_IN_LOCATION`
// disabled); line: 1, column: 1]" (SCH-04 04-lambda-not-json, recorded
// 2026-11-03 for "not json" on a Lambda target; scheduler.md 6.3). The
// Step Functions and event bus refusals follow aws-doc API_Target's "must
// be a well-formed JSON"; their text and Jackson's column for an error
// past the first character are inferred.
func errInputNotJSON(line, column int) *awserr.Error {
	return schedulerapi.ErrValidationException(fmt.Sprintf("JSON syntax error in input for the target: [Source: REDACTED "+
		"(`StreamReadFeature.INCLUDE_SOURCE_IN_LOCATION` disabled); line: %d, column: %d]", line, column))
}

// errNotTargetARN answers a Target.Arn that is not an ARN at all.
// UNVERIFIED (SCH-04), scheduler.md 6.1: the text is inferred.
func errNotTargetARN(arn string) *awserr.Error {
	return schedulerapi.ErrValidationException("Target.Arn " + arn + " is not an ARN; name a templated target's resource ARN or a universal target, arn:aws:scheduler:::aws-sdk:<service>:<apiAction>.")
}

// errNotTargetResource answers a Target.Arn of a templated service that
// names no resource of the kind the service's target takes, want.
// UNVERIFIED (SCH-04), scheduler.md 6.1: the text is inferred.
func errNotTargetResource(arn, want string) *awserr.Error {
	return schedulerapi.ErrValidationException("Target.Arn " + arn + " is not " + want + ".")
}

// errCrossRegionBus answers an event bus target outside the schedule's
// Region: "EventBridge Scheduler does not support cross-Region delivery
// when using EventBridge PutEvents as a templated target" (aws-doc
// managing-targets-templated; scheduler.md 6.1). UNVERIFIED (SCH-04): the
// code, the timing and the text are inferred.
func errCrossRegionBus(arn, region string) *awserr.Error {
	return schedulerapi.ErrValidationException("Target.Arn " + arn + " is an event bus outside the schedule's Region " + region +
		"; EventBridge Scheduler does not support cross-Region delivery to an event bus.")
}

// errBusWithoutParameters answers an event bus target without
// EventBridgeParameters, which carry the event's Source and DetailType
// (scheduler.md 6.4). UNVERIFIED (SCH-04): the code and text are inferred.
func errBusWithoutParameters(arn string) *awserr.Error {
	return schedulerapi.ErrValidationException("Target.EventBridgeParameters is required for the event bus target " + arn + ": it names the event's Source and DetailType.")
}

// errInputTooLarge answers an Input over maxInputBytes (SCH-04
// 04-input-too-big, recorded 2026-11-03; scheduler.md 6.3).
func errInputTooLarge() *awserr.Error {
	return schedulerapi.ErrValidationException(fmt.Sprintf("The input size for the target cannot exceed %d bytes.", maxInputBytes))
}

// errTargetNotImplemented answers a target understudy does not invoke,
// what naming its kind (scheduler.md 14.1, 14.4.1; spec "Scope").
func errTargetNotImplemented(what, arn string) *awserr.Error {
	return schedulerapi.ErrNotImplemented(what + " are not implemented in understudy (Target.Arn " + arn + "); nothing was stored. " +
		"Served targets: Lambda functions, SQS queues, SNS topics, Step Functions state machines and EventBridge event buses in the schedule's Region. " +
		"Ask for it at " + issuesURL + ".")
}

// issuesURL is where users ask for what understudy does not implement.
const issuesURL = "https://github.com/sustinbebustin/understudy/issues"

// errNotYet answers a member or value no ticket has built yet: what was
// sent, that nothing was stored, and what is served instead.
func errNotYet(what string) *awserr.Error {
	return schedulerapi.ErrNotImplemented(what + " is not implemented in understudy yet; nothing was stored. " +
		"Served today: rate(), at() and cron() schedules in any schedule group with a Lambda, SQS, SNS, Step Functions or event bus target in the schedule's Region. " +
		"Ask for it at " + issuesURL + ".")
}
