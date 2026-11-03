package delivery

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
)

// Principal is whom a delivery runs as when it is not its mechanism's
// service principal (Request.As).
//
//sumtype:decl
type Principal interface{ isPrincipal() }

// ExecutionRole is a session of an IAM role: the schedule's execution role
// for the Scheduler mechanisms (scheduler.md 9, 14.4.4), the event bus
// target's RoleArn for EventsToEvents (iam-enforcement-surfaces.md 8.4).
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
	// TargetARN is the target's ARN as configured, which a dead letter's
	// TARGET_ARN carries (SCH-08, recorded 2026-11-03; scheduler.md 7.2).
	TargetARN string
	// Scheduled is the scheduled instant, which <aws.scheduler.scheduled-time>
	// and SCHEDULED_TIME carry; the delivery is due at Scheduled plus the
	// flexible-window offset (scheduler.md 14.3.7).
	Scheduled time.Time
	// ExecutionID is a lower-case UUID (SCH-07, recorded 2026-11-03), one
	// per invocation across its retries (scheduler.md 7.2; inferred, SCH-08
	// recorded no retry).
	ExecutionID string
	// Template is the target's Input as accepted, or the payload of
	// scheduler.input.default when it has none.
	Template string
	// OnPermanent is scheduler.retry.permanent-errors as the schedule was
	// written under.
	OnPermanent PermanentErrors
	// Key is the schedule's KmsKeyArn, nil when it has none: the role
	// session needs kms:Decrypt on it before each target call (aws-doc
	// encryption-rest; scheduler.md 9, 14.11). Nothing is encrypted.
	Key *aws.ARN
}

// GroupARN is the ARN of the schedule group ScheduleARN is in,
// arn:<partition>:scheduler:<region>:<account>:schedule-group/<group>: the
// aws:SourceArn Scheduler assumes the execution role with
// (scheduler.md 9); zero when ScheduleARN names no schedule.
func (s SchedulerInvocation) GroupARN() aws.ARN {
	rest, ok := strings.CutPrefix(s.ScheduleARN.Resource, "schedule/")
	group, _, named := strings.Cut(rest, "/")
	if !ok || !named || group == "" {
		return aws.ARN{}
	}
	g := s.ScheduleARN
	g.Resource = "schedule-group/" + group
	return g
}

// Payload is Template with the four context attributes replaced by plain
// string replacement (scheduler.md 6.3): <aws.scheduler.schedule-arn>,
// <aws.scheduler.scheduled-time> (RFC 3339, whole seconds, Z),
// <aws.scheduler.execution-id> and <aws.scheduler.attempt-number>, which is
// attempt in decimal, 1 for the first attempt. The replacement is one pass,
// so a value that contains an attribute name is not replaced again.
func (s SchedulerInvocation) Payload(attempt int) string {
	return strings.NewReplacer(
		"<aws.scheduler.schedule-arn>", s.ScheduleARN.String(),
		"<aws.scheduler.scheduled-time>", s.Scheduled.UTC().Format("2006-01-02T15:04:05Z"),
		"<aws.scheduler.execution-id>", s.ExecutionID,
		"<aws.scheduler.attempt-number>", strconv.Itoa(attempt),
	).Replace(s.Template)
}

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

// PermanentErrorsError is a PermanentErrors that is not a declared value.
type PermanentErrorsError struct{ Text string }

func (e *PermanentErrorsError) Error() string {
	return "delivery: " + e.Text + " is not a scheduler.retry.permanent-errors value; want not-retried or retried"
}

// MarshalText encodes the switch value; an undeclared value is an error.
func (p PermanentErrors) MarshalText() ([]byte, error) {
	if p != PermanentNotRetried && p != PermanentRetried {
		return nil, &PermanentErrorsError{Text: p.String()}
	}
	return []byte(p.String()), nil
}

// UnmarshalText decodes a switch value.
func (p *PermanentErrors) UnmarshalText(text []byte) error {
	for _, v := range []PermanentErrors{PermanentNotRetried, PermanentRetried} {
		if v.String() == string(text) {
			*p = v
			return nil
		}
	}
	return &PermanentErrorsError{Text: string(text)}
}
