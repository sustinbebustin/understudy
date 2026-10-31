package scheduler

import (
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/schedexpr"
)

// members are the schedule members CreateSchedule sends, and UpdateSchedule
// replaces (scheduler.md 4.1, 4.3).
type members struct {
	expression  string
	start, end  *time.Time
	description *string
	timezone    *string
	state       *schedulerapi.ScheduleState
	kmsKeyArn   *string
	target      schedulerapi.Target
	window      schedulerapi.FlexibleTimeWindow
	action      *schedulerapi.ActionAfterCompletion
}

func createMembers(in *schedulerapi.CreateScheduleInput) members {
	return members{
		expression: in.ScheduleExpression, start: in.StartDate, end: in.EndDate, description: in.Description,
		timezone: in.ScheduleExpressionTimezone, state: in.State, kmsKeyArn: in.KmsKeyArn,
		target: in.Target, window: in.FlexibleTimeWindow, action: in.ActionAfterCompletion,
	}
}

// plan is what a schedule does, parsed once from its members at the
// boundary: when it fires and what it invokes (spec "Contract").
type plan struct {
	expression expression
	target     target
}

// expression is a parsed ScheduleExpression.
//
//sumtype:decl
type expression interface{ isExpression() }

// rateExpr fires every interval from its first fire (scheduler.md 5.2).
type rateExpr struct{ every time.Duration }

func (rateExpr) isExpression() {}

// target is a parsed templated target.
//
//sumtype:decl
type target interface{ isTarget() }

// sqsTarget is an SQS queue, with the FIFO message group SqsParameters
// names ("" for a standard queue; scheduler.md 6.4).
type sqsTarget struct {
	queue   aws.ARN
	groupID string
}

func (sqsTarget) isTarget() {}

// run is where a schedule is in its plan; scheduled is the only one built
// yet: the next invocation's scheduled instant and the instant it is due,
// which the fire timer is armed at (scheduler.md 14.3.7).
//
//sumtype:decl
type run interface{ isRun() }

// scheduled is a schedule with a next invocation: At is its scheduled
// instant, Due the instant it is made, At plus the flexible-window offset.
type scheduled struct {
	At  clock.Instant `json:"at"`
	Due clock.Instant `json:"due"`
}

func (scheduled) isRun() {}

// accept parses m into the schedule's plan, refusing what AWS refuses and,
// with 501, what no ticket has built yet, in the validation order of
// scheduler.md 14.9.3: expression, time zone, window, dates, target. units
// is scheduler.rate.unit-agreement.
func accept(scope aws.Scope, m *members, units schedexpr.RateUnits) (plan, *awserr.Error) {
	expr, err := parseExpression(m.expression, units)
	if err != nil {
		return plan{}, err
	}
	switch {
	case m.timezone != nil && *m.timezone != "UTC":
		return plan{}, errNotYet("ScheduleExpressionTimezone " + *m.timezone)
	case m.window.Mode != schedulerapi.FlexibleTimeWindowModeOff:
		return plan{}, errNotYet("FlexibleTimeWindow mode " + string(m.window.Mode))
	case m.start != nil || m.end != nil:
		return plan{}, errNotYet("StartDate and EndDate")
	case m.state != nil && *m.state != schedulerapi.ScheduleStateEnabled:
		return plan{}, errNotYet("State " + string(*m.state))
	}
	tgt, err := parseTarget(scope, &m.target)
	if err != nil {
		return plan{}, err
	}
	return plan{expression: expr, target: tgt}, nil
}

// parseExpression parses a ScheduleExpression; at() and cron() answer 501
// until they are built.
func parseExpression(expr string, units schedexpr.RateUnits) (expression, *awserr.Error) {
	if strings.HasPrefix(expr, "at(") || strings.HasPrefix(expr, "cron(") {
		return nil, errNotYet("ScheduleExpression " + expr + " (at() and cron())")
	}
	parsed, perr := schedexpr.ParseScheduler(expr, units)
	if perr != nil {
		return nil, schedulerapi.ErrValidationException(perr.Message())
	}
	switch p := parsed.(type) {
	case schedexpr.Rate:
		return rateExpr{every: p.Interval()}, nil
	case schedexpr.At, schedexpr.Cron:
	}
	return nil, errNotYet("ScheduleExpression " + expr + " (at() and cron())")
}

// parseTarget parses the target: an SQS queue in scope's Region, with an
// execution role. Other target services, a dead-letter queue and the
// parameter blocks of other targets answer 501 until they are built.
func parseTarget(scope aws.Scope, t *schedulerapi.Target) (target, *awserr.Error) {
	role, err := aws.ParseARN(t.RoleArn)
	if name, ok := strings.CutPrefix(role.Resource, "role/"); err != nil || role.Service != "iam" || !ok || name == "" {
		return nil, errNotRole(t.RoleArn)
	}
	queue, err := aws.ParseARN(t.Arn)
	if err != nil || queue.Service != "sqs" || queue.Region != scope.Region.String() || queue.Partition != scope.Partition.String() {
		return nil, errNotYet("Target.Arn " + t.Arn + " (targets other than an SQS queue in the schedule's Region)")
	}
	if _, err := aws.ScopeOf(queue); err != nil {
		return nil, errNotYet("Target.Arn " + t.Arn + " (targets other than an SQS queue in the schedule's Region)")
	}
	switch {
	case t.DeadLetterConfig != nil:
		return nil, errNotYet("Target.DeadLetterConfig")
	case t.EcsParameters != nil || t.EventBridgeParameters != nil || t.KinesisParameters != nil || t.SageMakerPipelineParameters != nil:
		return nil, errNotYet("a parameter block other than SqsParameters on an SQS target")
	}
	tgt := sqsTarget{queue: queue}
	if t.SqsParameters != nil && t.SqsParameters.MessageGroupId != nil {
		tgt.groupID = *t.SqsParameters.MessageGroupId
	}
	return tgt, nil
}

// firstRun is a new plan's first invocation: a rate schedule without
// StartDate, the only kind built yet, first fires at the write instant now.
// UNVERIFIED (SCH-09), scheduler.md 5.2, 14.3.4: whether AWS fires at the
// write instant or one interval later is unrecorded.
func firstRun(now clock.Instant) run { return scheduled{At: now, Due: now} }

// nextRun is the run after the invocation scheduled at prev, counted from
// its scheduled instant, not its due one (scheduler.md 14.3.7).
func nextRun(p plan, prev scheduled) run {
	e, ok := p.expression.(rateExpr)
	if !ok {
		return prev
	}
	at := prev.At.Add(e.every)
	return scheduled{At: at, Due: at}
}

// echo is a schedule's members as accepted, which GetSchedule reads back
// with AWS's defaults filled (scheduler.md 4.2).
type echo struct {
	Expression  string                              `json:"expression"`
	Timezone    *string                             `json:"timezone,omitempty"`
	State       *schedulerapi.ScheduleState         `json:"state,omitempty"`
	Description *string                             `json:"description,omitempty"`
	KmsKeyArn   *string                             `json:"kmsKeyArn,omitempty"`
	Window      schedulerapi.FlexibleTimeWindowMode `json:"window"`
	Action      *schedulerapi.ActionAfterCompletion `json:"actionAfterCompletion,omitempty"`
	Target      targetEcho                          `json:"target"`
}

// targetEcho is a target's members as accepted.
type targetEcho struct {
	Arn            string  `json:"arn"`
	RoleArn        string  `json:"roleArn"`
	Input          *string `json:"input,omitempty"`
	MaxAgeSeconds  *int32  `json:"maxAgeSeconds,omitempty"`
	MaxRetries     *int32  `json:"maxRetries,omitempty"`
	MessageGroupID *string `json:"messageGroupId,omitempty"`
}

// echoOf is m as accepted. A MaximumWindowInMinutes sent with OFF is not
// kept: it is ignored and not read back. UNVERIFIED (SCH-04), scheduler.md
// 4.2.
func echoOf(m *members) echo {
	e := echo{
		Expression: m.expression, Timezone: m.timezone, State: m.state, Description: m.description,
		KmsKeyArn: m.kmsKeyArn, Window: m.window.Mode, Action: m.action,
		Target: targetEcho{Arn: m.target.Arn, RoleArn: m.target.RoleArn, Input: m.target.Input},
	}
	if r := m.target.RetryPolicy; r != nil {
		e.Target.MaxAgeSeconds, e.Target.MaxRetries = r.MaximumEventAgeInSeconds, r.MaximumRetryAttempts
	}
	if p := m.target.SqsParameters; p != nil {
		e.Target.MessageGroupID = p.MessageGroupId
	}
	return e
}

// members is e as members again, so a loaded schedule is parsed the way
// its write was.
func (e *echo) members() members {
	m := members{
		expression: e.Expression, timezone: e.Timezone, state: e.State, description: e.Description,
		kmsKeyArn: e.KmsKeyArn, window: schedulerapi.FlexibleTimeWindow{Mode: e.Window}, action: e.Action,
		target: schedulerapi.Target{Arn: e.Target.Arn, RoleArn: e.Target.RoleArn, Input: e.Target.Input},
	}
	if e.Target.MaxAgeSeconds != nil || e.Target.MaxRetries != nil {
		m.target.RetryPolicy = &schedulerapi.RetryPolicy{MaximumEventAgeInSeconds: e.Target.MaxAgeSeconds, MaximumRetryAttempts: e.Target.MaxRetries}
	}
	if e.Target.MessageGroupID != nil {
		m.target.SqsParameters = &schedulerapi.SqsParameters{MessageGroupId: e.Target.MessageGroupID}
	}
	return m
}

// The retry policy AWS fills when RetryPolicy is not sent (tf-acc
// schedule_test.go:L219-L220; scheduler.md 4.2, 7.1).
const (
	defaultMaxAgeSeconds = 86400
	defaultMaxRetries    = 185
)

// retryPolicy is the target's RetryPolicy with AWS's defaults filled.
func (t *targetEcho) retryPolicy() (age, retries int32) {
	age, retries = defaultMaxAgeSeconds, defaultMaxRetries
	if t.MaxAgeSeconds != nil {
		age = *t.MaxAgeSeconds
	}
	if t.MaxRetries != nil {
		retries = *t.MaxRetries
	}
	return age, retries
}

// emptyJSONObject is the payload of a target without Input under
// scheduler.input.default empty-json-object (scheduler.md 6.3).
const emptyJSONObject = "{}"

// template is the payload template the target is sent: its Input, or the
// scheduler.input.default payload.
func (t *targetEcho) template() string {
	if t.Input != nil {
		return *t.Input
	}
	return emptyJSONObject
}
