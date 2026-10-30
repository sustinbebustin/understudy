package delivery

import (
	"fmt"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
)

// Principal is whom a delivery runs as when it is not its mechanism's
// service principal (Request.As).
//
//sumtype:decl
type Principal interface{ isPrincipal() }

// ExecutionRole is a session of an IAM role: the schedule's execution role
// for the Scheduler mechanisms (scheduler.md 9, 14.4.4).
type ExecutionRole struct {
	// Role is arn:<partition>:iam::<account>:role/<path><name>.
	Role aws.ARN
	// RoleID is the role's AROA... id, "" when IAM does not hold the role;
	// SQS shows it in SenderId as <role id>:<session> (scheduler.md 7.2).
	RoleID string
	// Session is the role session name: 32 lower-case hex digits per
	// invocation for Scheduler (scheduler.md 9); "" keeps the kernel's
	// "understudy".
	Session string
}

func (ExecutionRole) isPrincipal() {}

// SchedulerInvocation is one schedule invocation: what the outbox renders
// a Scheduler delivery's payload from before each attempt (scheduler.md
// 6.3, 14.4.2).
type SchedulerInvocation struct {
	// ScheduleARN is arn:<partition>:scheduler:<region>:<account>:schedule/<group>/<name>.
	ScheduleARN aws.ARN
	// TargetARN is the target's ARN as configured.
	TargetARN string
	// Scheduled is the scheduled instant, which <aws.scheduler.scheduled-time>
	// and SCHEDULED_TIME carry; the delivery is due at Scheduled plus the
	// flexible-window offset (scheduler.md 14.3.7).
	Scheduled time.Time
	// ExecutionID is 16 lower-case hex digits, one per invocation across its
	// retries (scheduler.md 7.2; inferred, SCH-08).
	ExecutionID string
	// Template is the target's Input as accepted, or the payload of
	// scheduler.input.default when it has none.
	Template string
	// OnPermanent is scheduler.retry.permanent-errors as the schedule was
	// written under.
	OnPermanent PermanentErrors
}

// Payload is Template with the four context attributes replaced by plain
// string replacement (scheduler.md 6.3): <aws.scheduler.schedule-arn>,
// <aws.scheduler.scheduled-time> (RFC 3339, whole seconds, Z),
// <aws.scheduler.execution-id> and <aws.scheduler.attempt-number>, which is
// attempt in decimal, 1 for the first attempt. Not implemented yet: it
// returns "".
func (SchedulerInvocation) Payload(int) string { return "" }

// PermanentErrors is what a Scheduler delivery does with a client-side
// target error: switch scheduler.retry.permanent-errors (scheduler.md 7.1).
type PermanentErrors uint8

// PermanentErrors values; the zero value is PermanentNotRetried.
const (
	// PermanentNotRetried sends the delivery to its dead-letter queue at
	// once, without EXHAUSTED_RETRY_CONDITION.
	PermanentNotRetried PermanentErrors = iota
	// PermanentRetried retries it like a server-side error until the retry
	// policy is exhausted.
	PermanentRetried
)

// String returns the switch value: "not-retried" or "retried".
func (p PermanentErrors) String() string {
	switch p {
	case PermanentNotRetried:
		return "not-retried"
	case PermanentRetried:
		return "retried"
	}
	return fmt.Sprintf("PermanentErrors(%d)", uint8(p))
}
