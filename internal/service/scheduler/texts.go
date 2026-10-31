package scheduler

import (
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

// futureFireDateLayout renders a present StartDate or EndDate in the
// future-fire text. UNVERIFIED (SCH-03), scheduler.md 14.3.6: only the
// absent rendering, null, is captured.
const futureFireDateLayout = time.RFC3339

// errNoFutureFire answers a cron or rate schedule with no fire in
// [max(now, StartDate), EndDate): "The schedule expression <expr>, with
// StartDate <s> and EndDate <e>, is invalid.", null for an absent date.
// Captured (qiita.com/taskforce_Hisui, 2022-12-02; scheduler.md 2.4).
func errNoFutureFire(expr string, start, end *time.Time) *awserr.Error {
	render := func(d *time.Time) string {
		if d == nil {
			return "null"
		}
		return d.UTC().Format(futureFireDateLayout)
	}
	return schedulerapi.ErrValidationException("The schedule expression " + expr + ", with StartDate " + render(start) +
		" and EndDate " + render(end) + ", is invalid.")
}

// errBadZone answers a ScheduleExpressionTimezone the IANA database does
// not name. UNVERIFIED (SCH-04), scheduler.md 5.4: the text is inferred.
func errBadZone(name string) *awserr.Error {
	return schedulerapi.ErrValidationException("Invalid ScheduleExpressionTimezone " + name + ": not a time zone of the IANA Time Zone Database.")
}

// errFlexibleWithoutWindow answers FLEXIBLE without MaximumWindowInMinutes.
// UNVERIFIED (SCH-04), scheduler.md 5.6: the text is inferred.
func errFlexibleWithoutWindow() *awserr.Error {
	return schedulerapi.ErrValidationException("FlexibleTimeWindow MaximumWindowInMinutes is required when Mode is FLEXIBLE.")
}

// errEndNotAfterStart answers an EndDate at or before StartDate.
// UNVERIFIED (SCH-04), scheduler.md 5.5: the text is inferred.
func errEndNotAfterStart(start, end time.Time) *awserr.Error {
	return schedulerapi.ErrValidationException("EndDate " + end.UTC().Format(futureFireDateLayout) +
		" must be after StartDate " + start.UTC().Format(futureFireDateLayout) + ".")
}

// errScheduleMissing answers a schedule the group does not hold.
// UNVERIFIED (SCH-02), scheduler.md 2.4: the code and status are the
// model's, the text is inferred.
func errScheduleMissing(group, name string) *awserr.Error {
	return schedulerapi.ErrResourceNotFoundException("Schedule " + group + "/" + name + " does not exist.")
}

// errScheduleExists answers CreateSchedule of a name the group holds.
// UNVERIFIED (SCH-02), scheduler.md 2.4: the code and status are the
// model's, the text is inferred.
func errScheduleExists(group, name string) *awserr.Error {
	return schedulerapi.ErrConflictException("Schedule " + group + "/" + name + " already exists.")
}

// errGroupMissing answers a schedule group the scope does not hold:
// "Schedule group <name> does not exist." (aws-doc
// managing-schedule-group-delete; scheduler.md 2.4, 14.9.2).
func errGroupMissing(name string) *awserr.Error {
	return schedulerapi.ErrResourceNotFoundException("Schedule group " + name + " does not exist.")
}

// errBadNextToken answers a NextToken no ListSchedules page issued.
// UNVERIFIED (SCH-01), scheduler.md 4.5: the code and text are inferred.
func errBadNextToken() *awserr.Error {
	return schedulerapi.ErrValidationException("Invalid NextToken: it was not issued by a ListSchedules page of this listing; start the listing again without NextToken.")
}

// errNotRole answers a Target.RoleArn that names no IAM role.
// UNVERIFIED (SCH-04), scheduler.md 4.7: the text is inferred.
func errNotRole(roleArn string) *awserr.Error {
	return schedulerapi.ErrValidationException("Target.RoleArn " + roleArn + " is not an IAM role ARN, arn:<partition>:iam::<account>:role/<name>.")
}

// issuesURL is where users ask for what understudy does not implement.
const issuesURL = "https://github.com/sustinbebustin/understudy/issues"

// errNotYet answers a member or value no ticket has built yet: what was
// sent, that nothing was stored, and what is served instead.
func errNotYet(what string) *awserr.Error {
	return schedulerapi.ErrNotImplemented(what + " is not implemented in understudy yet; nothing was stored. " +
		"Served today: rate(), at() and cron() schedules in the default group with an SQS queue target in the schedule's Region " +
		"with window OFF and ActionAfterCompletion NONE. Ask for it at " + issuesURL + ".")
}
