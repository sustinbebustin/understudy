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

// target is a parsed templated target understudy invokes (scheduler.md
// 6.1, 14.4.1).
//
//sumtype:decl
type target interface{ isTarget() }

// lambdaTarget is a Lambda function, invoked asynchronously: its
// unqualified ARN and the version or alias a qualified ARN names ("" for
// none), passed through as Invoke's Qualifier (scheduler.md 6.4).
// UNVERIFIED (SCH-07): the qualifier pass-through is inferred.
type lambdaTarget struct {
	function  aws.ARN
	qualifier string
}

// sqsTarget is an SQS queue, with the FIFO message group SqsParameters
// names ("" for a standard queue; scheduler.md 6.4).
type sqsTarget struct {
	queue   aws.ARN
	groupID string
}

// snsTarget is an SNS topic, published to with the payload as its message
// and no Subject (scheduler.md 6.4). UNVERIFIED (SCH-07): no Subject is
// inferred.
type snsTarget struct{ topic aws.ARN }

// busTarget is an EventBridge event bus in the schedule's Region, put to
// with EventBridgeParameters' Source and DetailType and the payload as the
// detail (scheduler.md 6.4, 14.4.3).
type busTarget struct {
	bus        aws.ARN
	source     string
	detailType string
}

func (lambdaTarget) isTarget() {}
func (sqsTarget) isTarget()    {}
func (snsTarget) isTarget()    {}
func (busTarget) isTarget()    {}

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

// maxInputBytes is the largest Input a target takes: "The maximum size of
// the Input field is 256 KB" (aws-doc API_Target; scheduler.md 6.3), read
// as 262,144 bytes. UNVERIFIED (SCH-04): the exact bound and its unit are
// unrecorded.
const maxInputBytes = 262144

// unimplementedTarget is the name the 501 gives a templated target
// service understudy does not invoke, by ARN service; false for any other
// (scheduler.md 6.1, 14.1; spec "Scope"). Step Functions is among them
// until its target is built (spec ticket 10).
func unimplementedTarget(service string) (string, bool) {
	switch service {
	case "ecs":
		return "Amazon ECS", true
	case "kinesis":
		return "Amazon Kinesis Data Streams", true
	case "firehose":
		return "Amazon Data Firehose", true
	case "codebuild":
		return "AWS CodeBuild", true
	case "codepipeline":
		return "AWS CodePipeline", true
	case "inspector":
		return "Amazon Inspector", true
	case "sagemaker":
		return "Amazon SageMaker AI", true
	case "states":
		return "AWS Step Functions", true
	}
	return "", false
}

// parseTarget parses the target and its execution role (scheduler.md
// 14.4.1): a Lambda function (qualifier allowed), an SQS queue or an SNS
// topic in scope's Region, or an event bus in scope's Region, each in any
// served account. A universal (aws-sdk) target or another templated
// service answers 501 naming it; any other service ValidationException.
// The target need not exist (4.1). A parameter block that does not match
// the target is stored, echoed and ignored, and Input is not checked for
// JSON. UNVERIFIED (SCH-04), scheduler.md 6.1, 6.3: both are inferred. A
// dead-letter queue answers 501 until it is built.
func parseTarget(scope aws.Scope, t *schedulerapi.Target) (target, *awserr.Error) {
	role, err := aws.ParseARN(t.RoleArn)
	if name, ok := strings.CutPrefix(role.Resource, "role/"); err != nil || role.Service != "iam" || !ok || name == "" {
		return nil, errNotRole(t.RoleArn)
	}
	tgt, aerr := parseTargetARN(scope, t)
	if aerr != nil {
		return nil, aerr
	}
	switch {
	case t.Input != nil && len(*t.Input) > maxInputBytes:
		return nil, errInputTooLarge(len(*t.Input))
	case t.DeadLetterConfig != nil:
		return nil, errNotYet("Target.DeadLetterConfig")
	}
	return tgt, nil
}

// parseTargetARN reads Target.Arn into the target it names.
func parseTargetARN(scope aws.Scope, t *schedulerapi.Target) (target, *awserr.Error) {
	if universal(t.Arn) {
		return nil, errTargetNotImplemented("Universal (aws-sdk) targets", t.Arn)
	}
	a, err := aws.ParseARN(t.Arn)
	if err != nil {
		return nil, errNotTargetARN(t.Arn)
	}
	if name, ok := unimplementedTarget(a.Service); ok {
		return nil, errTargetNotImplemented(name+" targets", t.Arn)
	}
	switch a.Service {
	case "lambda", "sqs", "sns", "events":
	default:
		return nil, errUnsupportedService(a.Service)
	}
	if _, err := aws.ScopeOf(a); err != nil || a.Partition != scope.Partition.String() {
		return nil, errNotYet("Target.Arn " + t.Arn + " (a target outside the schedule's partition or in a Region or account understudy does not serve)")
	}
	if a.Region != scope.Region.String() {
		if a.Service == "events" {
			return nil, errCrossRegionBus(t.Arn, scope.Region.String())
		}
		return nil, errNotYet("Target.Arn " + t.Arn + " (Lambda, SQS and SNS targets outside the schedule's Region)")
	}
	switch a.Service {
	case "lambda":
		return lambdaOf(a)
	case "sqs":
		if strings.ContainsAny(a.Resource, ":/") {
			return nil, errNotTargetResource(t.Arn, "an SQS queue ARN, arn:<partition>:sqs:<region>:<account>:<queue>")
		}
		tgt := sqsTarget{queue: a}
		if p := t.SqsParameters; p != nil && p.MessageGroupId != nil {
			tgt.groupID = *p.MessageGroupId
		}
		return tgt, nil
	case "sns":
		if strings.ContainsAny(a.Resource, ":/") {
			return nil, errNotTargetResource(t.Arn, "an SNS topic ARN, arn:<partition>:sns:<region>:<account>:<topic>")
		}
		return snsTarget{topic: a}, nil
	}
	if name, ok := strings.CutPrefix(a.Resource, "event-bus/"); !ok || name == "" || strings.Contains(name, "/") {
		return nil, errNotTargetResource(t.Arn, "an EventBridge event bus ARN, arn:<partition>:events:<region>:<account>:event-bus/<name>")
	}
	p := t.EventBridgeParameters
	if p == nil {
		return nil, errBusWithoutParameters(t.Arn)
	}
	return busTarget{bus: a, source: p.Source, detailType: p.DetailType}, nil
}

// universal reports whether arn is a universal target,
// arn:<partition>:scheduler:::aws-sdk:<service>:<apiAction> (aws-doc
// managing-targets-universal; scheduler.md 6.2).
func universal(arn string) bool {
	parts := strings.SplitN(arn, ":", 6)
	return len(parts) == 6 && parts[0] == "arn" && parts[2] == "scheduler" && parts[3] == "" && parts[4] == "" &&
		strings.HasPrefix(parts[5], "aws-sdk:")
}

// lambdaOf reads a Lambda ARN as a function target: function:<name> with
// at most one qualifier, each part non-empty (as
// internal/service/events/fanout.go reads a rule's function target).
func lambdaOf(a aws.ARN) (target, *awserr.Error) {
	rest, ok := strings.CutPrefix(a.Resource, "function:")
	name, qualifier, qualified := strings.Cut(rest, ":")
	if !ok || name == "" || qualified && (qualifier == "" || strings.Contains(qualifier, ":")) {
		return nil, errNotTargetResource(a.String(), "a Lambda function ARN, arn:<partition>:lambda:<region>:<account>:function:<name>[:<version or alias>]")
	}
	fn := a
	fn.Resource = "function:" + name
	return lambdaTarget{function: fn, qualifier: qualifier}, nil
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

// targetEcho is a target's members as accepted, every parameter block
// included, whether or not it matches the target (scheduler.md 4.2, 6.1).
type targetEcho struct {
	Arn           string                                    `json:"arn"`
	RoleArn       string                                    `json:"roleArn"`
	Input         *string                                   `json:"input,omitempty"`
	MaxAgeSeconds *int32                                    `json:"maxAgeSeconds,omitempty"`
	MaxRetries    *int32                                    `json:"maxRetries,omitempty"`
	Ecs           *schedulerapi.EcsParameters               `json:"ecsParameters,omitempty"`
	EventBridge   *schedulerapi.EventBridgeParameters       `json:"eventBridgeParameters,omitempty"`
	Kinesis       *schedulerapi.KinesisParameters           `json:"kinesisParameters,omitempty"`
	SageMaker     *schedulerapi.SageMakerPipelineParameters `json:"sageMakerPipelineParameters,omitempty"`
	Sqs           *schedulerapi.SqsParameters               `json:"sqsParameters,omitempty"`
}

// parameters sets t's parameter blocks on out.
func (t *targetEcho) parameters(out *schedulerapi.Target) {
	out.EcsParameters, out.EventBridgeParameters, out.KinesisParameters = t.Ecs, t.EventBridge, t.Kinesis
	out.SageMakerPipelineParameters, out.SqsParameters = t.SageMaker, t.Sqs
}

// echoOf is m as accepted. A MaximumWindowInMinutes sent with OFF is not
// kept: it is ignored and not read back. UNVERIFIED (SCH-04), scheduler.md
// 4.2.
func echoOf(m *members) echo {
	e := echo{
		Expression: m.expression, Timezone: m.timezone, StartDate: m.start, EndDate: m.end, State: m.state, Description: m.description,
		KmsKeyArn: m.kmsKeyArn, Window: m.window.Mode, Action: m.action,
		Target: targetEcho{
			Arn: m.target.Arn, RoleArn: m.target.RoleArn, Input: m.target.Input,
			Ecs: m.target.EcsParameters, EventBridge: m.target.EventBridgeParameters, Kinesis: m.target.KinesisParameters,
			SageMaker: m.target.SageMakerPipelineParameters, Sqs: m.target.SqsParameters,
		},
	}
	if r := m.target.RetryPolicy; r != nil {
		e.Target.MaxAgeSeconds, e.Target.MaxRetries = r.MaximumEventAgeInSeconds, r.MaximumRetryAttempts
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
	e.Target.parameters(&m.target)
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
