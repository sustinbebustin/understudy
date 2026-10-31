package scheduler

import (
	"fmt"
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

func updateMembers(in *schedulerapi.UpdateScheduleInput) members {
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
	// loc is the zone an at() or cron() is read in, UTC when none was
	// sent; a rate ignores it (scheduler.md 5.4, 14.3.4).
	loc *time.Location
	// start and end bound a rate or cron (scheduler.md 5.5); an at()
	// ignores them (5.1).
	start, end *time.Time
	// state is State with its default, ENABLED, filled (scheduler.md 4.6).
	state  schedulerapi.ScheduleState
	target target
}

// expression is a parsed ScheduleExpression.
//
//sumtype:decl
type expression interface{ isExpression() }

// rateExpr fires every interval from its first fire (scheduler.md 5.2).
type rateExpr struct{ every time.Duration }

// atExpr fires once at its civil time in the schedule's zone
// (scheduler.md 5.1).
type atExpr struct{ at schedexpr.At }

// cronExpr fires at every matching civil minute in the schedule's zone
// (scheduler.md 5.3, 5.4).
type cronExpr struct{ cron schedexpr.Cron }

func (rateExpr) isExpression() {}
func (atExpr) isExpression()   {}
func (cronExpr) isExpression() {}

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

// run is where a schedule is in its plan: a next invocation, disabled,
// completed (a fired at()) or ended (no invocation before EndDate, or a
// cron that never fires again) (scheduler.md 14.2.4).
//
//sumtype:decl
type run interface{ isRun() }

// scheduled is a schedule with a next invocation: At is its scheduled
// instant, Due the instant it is made: At plus the flexible-window offset,
// or the write instant for a past-due at() under scheduler.at.past-due
// fire-at-once.
type scheduled struct {
	At  clock.Instant `json:"at"`
	Due clock.Instant `json:"due"`
}

// disabled is a schedule whose State is DISABLED: it does not invoke
// (scheduler.md 4.6).
type disabled struct{}

// completed is an at() schedule that has invoked its target, or whose time
// had passed under scheduler.at.past-due never (scheduler.md 5.1, 14.2.4).
type completed struct{}

// ended is a rate or cron schedule with no invocation left before its
// EndDate, or a cron that never matches again (scheduler.md 5.5).
type ended struct{}

func (scheduled) isRun() {}
func (disabled) isRun()  {}
func (completed) isRun() {}
func (ended) isRun()     {}

// dateCheck is the dates step of accept: at a write, the EndDate order and
// the future-fire check against the write instant; at a snapshot load,
// nothing, since the write checked them and time has moved on.
type dateCheck func(m *members, e expression, loc *time.Location) *awserr.Error

// atWrite is the dates step of a write admitted at now.
func atWrite(now time.Time) dateCheck {
	return func(m *members, e expression, loc *time.Location) *awserr.Error { return checkDates(m, e, loc, now) }
}

// atLoad is the dates step of a snapshot load.
func atLoad(*members, expression, *time.Location) *awserr.Error { return nil }

// accept parses m into the schedule's plan, refusing what AWS refuses and,
// with 501, what no ticket has built yet. The order is scheduler.md
// 14.9.3's: expression, time zone, flexible window, dates and the
// future-fire check (in either State), target ARN; only "expression
// before role" is evidenced (G3; scheduler.md 4.7), the rest is
// UNVERIFIED (SCH-04). The 501s for what is not built come last, so a
// request AWS refuses is refused as AWS would. units is
// scheduler.rate.unit-agreement.
func accept(scope aws.Scope, m *members, units schedexpr.RateUnits, dates dateCheck) (plan, *awserr.Error) {
	expr, err := parseExpression(m.expression, units)
	if err != nil {
		return plan{}, err
	}
	loc, err := parseZone(m.timezone)
	if err != nil {
		return plan{}, err
	}
	if m.window.Mode == schedulerapi.FlexibleTimeWindowModeFlexible && m.window.MaximumWindowInMinutes == nil {
		return plan{}, errFlexibleWithoutWindow()
	}
	if err := dates(m, expr, loc); err != nil {
		return plan{}, err
	}
	tgt, err := parseTarget(scope, &m.target)
	if err != nil {
		return plan{}, err
	}
	switch {
	case m.window.Mode != schedulerapi.FlexibleTimeWindowModeOff:
		return plan{}, errNotYet("FlexibleTimeWindow mode " + string(m.window.Mode))
	case m.action != nil && *m.action == schedulerapi.ActionAfterCompletionDelete:
		return plan{}, errNotYet("ActionAfterCompletion DELETE")
	}
	state := schedulerapi.ScheduleStateEnabled
	if m.state != nil {
		state = *m.state
	}
	return plan{expression: expr, loc: loc, start: m.start, end: m.end, state: state, target: tgt}, nil
}

// parseExpression parses a ScheduleExpression, refusing it with
// Scheduler's recorded text (scheduler.md 2.4).
func parseExpression(expr string, units schedexpr.RateUnits) (expression, *awserr.Error) {
	parsed, perr := schedexpr.ParseScheduler(expr, units)
	if perr != nil {
		return nil, errInvalidExpression(perr)
	}
	switch p := parsed.(type) {
	case schedexpr.Rate:
		return rateExpr{every: p.Interval()}, nil
	case schedexpr.At:
		return atExpr{at: p}, nil
	case schedexpr.Cron:
		return cronExpr{cron: p}, nil
	}
	return nil, awserr.Internalf(fmt.Errorf("schedexpr.ParseScheduler(%q) returned %T, not an At, Rate or Cron", expr, parsed))
}

// parseZone resolves ScheduleExpressionTimezone in the IANA database the
// binary embeds (time/tzdata; scheduler.md 5.4, 14.3.2); UTC when it is
// not sent. "Local" names the host's zone in Go, not an IANA zone, so it
// is refused.
func parseZone(name *string) (*time.Location, *awserr.Error) {
	if name == nil {
		return time.UTC, nil
	}
	if *name == "" || *name == "Local" {
		return nil, errBadZone(*name)
	}
	loc, err := time.LoadLocation(*name)
	if err != nil {
		return nil, errBadZone(*name)
	}
	return loc, nil
}

// checkDates refuses an EndDate not after StartDate, then a cron or rate
// schedule with no fire in [max(now, StartDate), EndDate) (scheduler.md
// 14.3.6). An at() ignores both dates (scheduler.md 5.1).
func checkDates(m *members, e expression, loc *time.Location, now time.Time) *awserr.Error {
	if _, ok := e.(atExpr); ok {
		return nil
	}
	if m.start != nil && m.end != nil && !m.end.After(*m.start) {
		return errEndNotAfterStart(*m.start, *m.end)
	}
	from := now
	if m.start != nil && m.start.After(now) {
		from = *m.start
	}
	first, ok := firstFireFrom(e, m.start, from, loc)
	if !ok || (m.end != nil && !first.Before(*m.end)) {
		return errNoFutureFire(m.expression, m.start, m.end)
	}
	return nil
}

// firstFireFrom is e's first scheduled instant at or after from (an at()'s
// one instant, whatever from is). A rate
// counts from StartDate when it is sent, else from the write instant
// (scheduler.md 5.2, 14.3.4), so from is its first fire then; a cron
// matches civil minutes in loc.
func firstFireFrom(e expression, start *time.Time, from time.Time, loc *time.Location) (time.Time, bool) {
	switch e := e.(type) {
	case rateExpr:
		if start == nil || !start.Before(from) {
			return from, true
		}
		late := from.Sub(*start) % e.every
		if late == 0 {
			return from, true
		}
		return from.Add(e.every - late), true
	case cronExpr:
		return schedexpr.NextIn(e.cron, from.Add(-time.Nanosecond), loc)
	case atExpr:
		return e.at.In(loc), true
	}
	return time.Time{}, false
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

// firstRun is the run of a plan written at now: by a create, by an
// update that changes its timing, or by enabling it (scheduler.md 14.3.10).
//
//   - DISABLED: no invocation (4.6).
//   - at(): its civil time in the zone; one already past is due at now
//     under scheduler.at.past-due fire-at-once and completed under never
//     (5.1, 4.6). The invocation honours the expression's seconds.
//     UNVERIFIED (SCH-09), scheduler.md 5.1.
//   - rate(): StartDate when it is in the future, else the write instant
//     when no StartDate was sent, else StartDate's first interval at or
//     after now, in its phase. UNVERIFIED (SCH-09), scheduler.md 5.2, 5.5,
//     14.3.4: the write-instant fire and the phase of a past StartDate are
//     unrecorded.
//   - cron(): the first matching civil minute in the zone at or after
//     max(now, StartDate) (5.3, 5.4).
//
// A rate or cron with no invocation before EndDate is ended (5.5).
func firstRun(p plan, now clock.Instant, pastDue AtPastDue) run {
	if p.state == schedulerapi.ScheduleStateDisabled {
		return disabled{}
	}
	if e, ok := p.expression.(atExpr); ok {
		at := clock.FromWire(e.at.In(p.loc))
		switch {
		case !at.Before(now):
			return scheduled{At: at, Due: at}
		case pastDue == PastDueNever:
			return completed{}
		}
		return scheduled{At: at, Due: now}
	}
	from := now.Wire()
	if p.start != nil && p.start.After(from) {
		from = *p.start
	}
	return p.bounded(firstFireFrom(p.expression, p.start, from, p.loc))
}

// bounded is the run of an invocation scheduled at t, ended when there is
// none (ok false) or t is at or after EndDate (scheduler.md 5.5).
func (p *plan) bounded(t time.Time, ok bool) run {
	if !ok || (p.end != nil && !t.Before(*p.end)) {
		return ended{}
	}
	at := clock.FromWire(t)
	return scheduled{At: at, Due: at}
}

// nextRun is the run after the invocation scheduled at prev, counted from
// its scheduled instant, not its due one (scheduler.md 14.3.7): an at()
// is completed, a rate steps one interval, a cron takes its next match.
func nextRun(p plan, prev scheduled) run {
	switch e := p.expression.(type) {
	case atExpr:
		return completed{}
	case rateExpr:
		return p.bounded(prev.At.Wire().Add(e.every), true)
	case cronExpr:
		return p.bounded(schedexpr.NextIn(e.cron, prev.At.Wire(), p.loc))
	}
	return ended{}
}

// catchUp is the run of a plan whose invocation at, scheduled before now,
// came due before now and is skipped (scheduler.md 14.3.8): a rate's first interval
// at or after now in its phase, a cron's first match at or after now, and
// an at() completed, since only an explicit advance delivers a missed fire.
func catchUp(p plan, at scheduled, now clock.Instant) run {
	switch e := p.expression.(type) {
	case atExpr:
		return completed{}
	case rateExpr:
		next := now
		if late := now.Sub(at.At) % e.every; late != 0 {
			next = now.Add(e.every - late)
		}
		return p.bounded(next.Wire(), true)
	case cronExpr:
		return p.bounded(schedexpr.NextIn(e.cron, now.Wire().Add(-time.Nanosecond), p.loc))
	}
	return ended{}
}

// echo is a schedule's members as accepted, which GetSchedule reads back
// with AWS's defaults filled (scheduler.md 4.2).
type echo struct {
	Expression  string                              `json:"expression"`
	Timezone    *string                             `json:"timezone,omitempty"`
	StartDate   *time.Time                          `json:"startDate,omitempty"`
	EndDate     *time.Time                          `json:"endDate,omitempty"`
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
		Expression: m.expression, Timezone: m.timezone, StartDate: m.start, EndDate: m.end, State: m.state, Description: m.description,
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
		expression: e.Expression, timezone: e.Timezone, start: e.StartDate, end: e.EndDate, state: e.State, description: e.Description,
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

// sameTiming reports whether o leaves e's expression, time zone, dates,
// window and State (ENABLED when not sent) as they were: an update that
// does keeps the schedule's next invocation, any other restarts its plan
// at the write instant. UNVERIFIED (SCH-09), scheduler.md 4.3, 14.3.10:
// whether AWS restarts a rate's phase on update is unrecorded; this is
// the events rule's choice (internal/service/events/rules.go).
func (e *echo) sameTiming(o *echo) bool {
	return e.Expression == o.Expression && equalPtr(e.Timezone, o.Timezone) &&
		equalTime(e.StartDate, o.StartDate) && equalTime(e.EndDate, o.EndDate) &&
		e.Window == o.Window && e.state() == o.state()
}

// state is State with its default, ENABLED, filled (scheduler.md 4.6).
func (e *echo) state() schedulerapi.ScheduleState {
	if e.State == nil {
		return schedulerapi.ScheduleStateEnabled
	}
	return *e.State
}

func equalPtr[T comparable](a, b *T) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

func equalTime(a, b *time.Time) bool {
	return (a == nil) == (b == nil) && (a == nil || a.Equal(*b))
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
