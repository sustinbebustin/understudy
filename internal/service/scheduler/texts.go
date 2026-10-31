package scheduler

import (
	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The refusal texts (scheduler.md 14.9.2). Only the expression text is
// evidenced for the cases built so far; every other is understudy's own,
// inferred, and named here so one table holds them all.

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
		"Served today: rate() schedules in the default group with an SQS queue target in the schedule's Region, " +
		"window OFF, no dates and time zone UTC. Ask for it at " + issuesURL + ".")
}
