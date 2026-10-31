package scheduler

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// principal is the service principal Scheduler reads the execution role
// as (scheduler.md 9).
const principal = "scheduler.amazonaws.com"

// onFire invokes the target of the schedule the timer names, due at due:
// it resolves the execution role's id, then in one write hands the
// invocation to the outbox as a session of that role, moves the plan to
// its next invocation and arms it (scheduler.md 14.3, 14.4; contract
// section 1). A timer whose schedule is gone or was re-planned since it
// was armed does nothing.
func (s *Service) onFire(c *kernel.Call, key clock.TimerKey, due clock.Instant) {
	ctx := context.Background()
	r, ok := parseScheduleARN(key.ID)
	if !ok {
		s.k.Log().LogAttrs(ctx, slog.LevelError, "a Scheduler fire timer names no schedule; it was dropped", slog.String("timer", key.ID))
		return
	}
	sc, ok := s.read(r)
	if !ok || !dueAt(sc.Run, due) {
		return
	}
	c.Touch(r.arn())
	roleID := s.resolveRole(ctx, c, r, sc.Echo.Target.RoleArn)
	_, err := kernel.Write(c, s.cells, r.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		sc, ok := st.get(r)
		at, isDue := sc.Run.(scheduled)
		if !ok || !isDue || at.Due.Compare(due) != 0 {
			tx.Unchanged()
			return struct{}{}, nil
		}
		req, err := s.invocation(r, &sc, at, roleID)
		if err != nil {
			return struct{}{}, err
		}
		tx.Deliver(req)
		sc.Run = nextRun(sc.Plan, at)
		s.arm(tx, r, sc.Run)
		st.put(r, sc)
		return struct{}{}, nil
	})
	if err != nil {
		s.k.Log().LogAttrs(ctx, slog.LevelError, "a Scheduler fire failed; its invocation was not delivered and the schedule's next fire was not armed",
			slog.String("schedule", r.arn().String()), slog.String("error", err.Message))
	}
}

// dueAt reports whether next is an invocation due at due.
func dueAt(next run, due clock.Instant) bool {
	at, ok := next.(scheduled)
	return ok && at.Due.Compare(due) == 0
}

// resolveRole is the execution role's id, read through IAM's internal
// ResolveRole as the Scheduler service principal (as
// internal/service/sts/assumerole.go resolves a role). A role IAM does not
// hold has none: the invocation is still made as a session of the named
// role, with no role id in SenderId, under iam.enforce off and advise.
// UNVERIFIED (SCH-08), scheduler.md 9.
func (s *Service) resolveRole(ctx context.Context, c *kernel.Call, r scheduleRef, role string) string {
	a, err := aws.ParseARN(role)
	if err != nil {
		return ""
	}
	scope := r.scope
	account, err := aws.ParseAccountID(a.Account)
	if err != nil {
		return ""
	}
	scope.Account = account
	out, aerr := kernel.Invoke(ctx, s.k, c, iamapi.OpResolveRole,
		kernel.Target{Scope: scope, As: kernel.AsPlatform{Name: principal}, Via: ref.SchedulerTarget},
		&iamapi.ResolveRoleInput{RoleArn: role})
	switch {
	case aerr == nil:
		return out.Role.RoleId
	case aerr.ID.Shape != iamapi.IdentityNoSuchEntityException().Shape:
		s.k.Log().LogAttrs(ctx, slog.LevelWarn, "Scheduler could not read a schedule's execution role; the invocation runs as the role without its id",
			slog.String("schedule", r.arn().String()), slog.String("role", role), slog.String("error", aerr.Message))
	}
	return ""
}

// invocation is the delivery of sc's invocation scheduled at at: its
// target, retry policy and payload template, run as a fresh session of the
// execution role, with one execution id for the invocation and its retries
// (scheduler.md 6.3, 7.1, 7.2, 9). UNVERIFIED (SCH-07, SCH-08): one
// execution id across retries and a 32-hex session per invocation are
// inferred.
func (s *Service) invocation(r scheduleRef, sc *schedule, at scheduled, roleID string) (delivery.Request, *awserr.Error) {
	role, err := aws.ParseARN(sc.Echo.Target.RoleArn)
	if err != nil {
		return delivery.Request{}, awserr.Internalf(err)
	}
	age, retries := sc.Echo.Target.retryPolicy()
	tmpl := sc.Echo.Target.template()
	req := delivery.Request{
		Source: r.arn(),
		Policy: delivery.EventBridgeTarget{MaxAge: time.Duration(age) * time.Second, MaxAttempts: int(retries)},
		DLQ:    sc.Plan.deadLetter,
		As:     delivery.ExecutionRole{Role: role, RoleID: roleID, Session: hex.EncodeToString(s.ids.Draw(ids.KindRoleSession, 16))},
		Invocation: &delivery.SchedulerInvocation{
			ScheduleARN: r.arn(),
			TargetARN:   sc.Echo.Target.Arn,
			Scheduled:   at.At.Wire(),
			ExecutionID: hex.EncodeToString(s.ids.Draw(ids.KindSchedulerExecution, 8)),
			Template:    tmpl,
			OnPermanent: s.opts.PermanentErrors,
		},
	}
	switch t := sc.Plan.target.(type) {
	case lambdaTarget:
		req.Mechanism = delivery.SchedulerToLambda
		req.Target = delivery.LambdaFunction{ARN: t.function, Qualifier: t.qualifier, Invocation: delivery.InvocationEvent, Event: []byte(tmpl)}
	case sqsTarget:
		req.Mechanism = delivery.SchedulerToSQS
		req.Target = delivery.SQSQueue{ARN: t.queue, Body: tmpl, GroupID: t.groupID}
	case snsTarget:
		req.Mechanism = delivery.SchedulerToSNS
		req.Target = delivery.SNSTopic{ARN: t.topic, Message: tmpl}
	case busTarget:
		// UNVERIFIED (SCH-07), scheduler.md 14.4.5: the event has no
		// resources and PutEvents stamps its time at delivery.
		req.Mechanism = delivery.SchedulerToEvents
		req.Target = delivery.EventBus{ARN: t.bus, Entries: []delivery.EventEntry{{Source: t.source, DetailType: t.detailType, Detail: tmpl}}}
	default:
		return delivery.Request{}, awserr.Internalf(fmt.Errorf("scheduler: schedule %s has a %T target, which no invocation is built for", r.arn(), sc.Plan.target))
	}
	return req, nil
}
