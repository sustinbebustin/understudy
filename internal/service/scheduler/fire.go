package scheduler

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// principal is the service principal Scheduler reads the execution role
// as (scheduler.md 9).
const principal = "scheduler.amazonaws.com"

// scheduleDeleted is the state the journal records a schedule moving to
// when ActionAfterCompletion DELETE removes it after its last invocation.
const scheduleDeleted journal.ResourceState = "DELETED"

// onFire invokes the target of the schedule the timer names, due at due:
// it resolves the execution role's id, then in one write hands the
// invocation to the outbox as a session of that role, moves the plan to
// its next invocation and arms it, or, under ActionAfterCompletion DELETE
// with no invocation left, deletes the schedule (scheduler.md 14.3, 14.4,
// 14.6). A timer whose schedule is gone or was re-planned since it was
// armed does nothing.
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
	resolved := sc.Plan.role
	roleID := s.resolveRole(ctx, c, r, resolved)
	_, err := kernel.Write(c, s.cells, r.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		sc, ok := st.get(r)
		at, isDue := sc.Run.(scheduled)
		if !ok || !isDue || at.Due.Compare(due) != 0 {
			tx.Unchanged()
			return struct{}{}, nil
		}
		id := roleID
		if sc.Plan.role != resolved {
			// An UpdateSchedule changed only Target.RoleArn since the read,
			// which keeps the due instant: the id resolved belongs to the
			// old role, so this invocation carries none.
			id = ""
		}
		req, err := s.invocation(r, &sc, at, id)
		if err != nil {
			return struct{}{}, err
		}
		tx.Deliver(req)
		sc.Run = offset(&sc.Plan, nextRun(sc.Plan, at), s.opts.FlexibleOffset, s.ids)
		if s.arm(tx, r, sc.Run) || !sc.Plan.deletesWhenDone() {
			st.put(r, sc)
			return struct{}{}, nil
		}
		// ActionAfterCompletion DELETE: this was the last invocation, so the
		// schedule goes in the write that hands it over, before any retry
		// of it runs, journaled as a child of the fire (scheduler.md 8,
		// 14.6). UNVERIFIED (SCH-07): the deletion's timing against the
		// invocation's retries is unrecorded.
		st.remove(r)
		tx.Record(r.scope, journal.StateChange{Resource: r.arn(), From: journal.ResourceState(sc.Echo.state()), To: scheduleDeleted})
		return struct{}{}, nil
	})
	if err != nil {
		// kernel.Write keeps a state change fn made when an effect it
		// requested is refused, so the plan may have moved on.
		s.k.Log().LogAttrs(ctx, slog.LevelError, "a Scheduler fire failed; this invocation was not delivered, and the schedule may have moved to its next fire",
			slog.String("schedule", r.arn().String()), slog.String("error", string(err.ID.Shape)), slog.String("message", err.Message))
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
// UNVERIFIED (SCH-08), scheduler.md 9. Any other failure is understudy's
// own (an IAM fault or bug, journaled as the internal call's outcome): it
// is logged as an error and the invocation runs without the id, rather
// than dropping it.
func (s *Service) resolveRole(ctx context.Context, c *kernel.Call, r scheduleRef, role aws.ARN) string {
	scope := r.scope
	account, err := aws.ParseAccountID(role.Account)
	if err != nil {
		return ""
	}
	scope.Account = account
	out, aerr := kernel.Invoke(ctx, s.k, c, iamapi.OpResolveRole,
		kernel.Target{Scope: scope, As: kernel.AsPlatform{Name: principal}, Via: ref.SchedulerTarget},
		&iamapi.ResolveRoleInput{RoleArn: role.String()})
	switch {
	case aerr == nil:
		return out.Role.RoleId
	case aerr.ID.Shape != iamapi.IdentityNoSuchEntityException().Shape:
		s.k.Log().LogAttrs(ctx, slog.LevelError, "Scheduler could not read a schedule's execution role from IAM; the invocation runs as the role without its id in SenderId; see the journal's iam.ResolveRole outcome for the cause",
			slog.String("schedule", r.arn().String()), slog.String("role", role.String()), slog.String("error", string(aerr.ID.Shape)), slog.String("message", aerr.Message))
	}
	return ""
}

// invocation is the delivery of sc's invocation scheduled at at: its
// target, retry policy and payload template, run as a fresh session of the
// execution role, with one execution id, a lower-case UUID, for the
// invocation and its retries (scheduler.md 6.3, 7.1, 7.2, 9; the UUID form
// SCH-07, SCH-08, recorded 2026-11-03). UNVERIFIED (SCH-08 saw no retry):
// one execution id across retries is inferred; the 32-hex session is
// recorded (SenderId <role id>:<32 hex>, SCH-07).
func (s *Service) invocation(r scheduleRef, sc *schedule, at scheduled, roleID string) (delivery.Request, *awserr.Error) {
	age, retries := sc.Echo.Target.retryPolicy()
	execID := s.ids.UUID(ids.KindSchedulerExecution).String()
	tmpl, err := sc.payloadTemplate(r, at.At.Wire(), execID)
	if err != nil {
		return delivery.Request{}, err
	}
	req := delivery.Request{
		Source: r.arn(),
		Policy: delivery.EventBridgeTarget{MaxAge: time.Duration(age) * time.Second, MaxAttempts: int(retries)},
		DLQ:    sc.Plan.deadLetter,
		As:     delivery.ExecutionRole{Role: sc.Plan.role, RoleID: roleID, Session: hex.EncodeToString(s.ids.Draw(ids.KindRoleSession, 16))},
		Invocation: &delivery.SchedulerInvocation{
			ScheduleARN: r.arn(),
			TargetARN:   sc.Echo.Target.Arn,
			Scheduled:   at.At.Wire(),
			ExecutionID: execID,
			Template:    tmpl,
			OnPermanent: s.opts.PermanentErrors,
			Key:         sc.Plan.key,
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
		// The event has no resources and PutEvents stamps its id and its
		// time at delivery (SCH-07, recorded 2026-11-03; scheduler.md 6.4).
		req.Mechanism = delivery.SchedulerToEvents
		req.Target = delivery.EventBus{ARN: t.bus, Entries: []delivery.EventEntry{{Source: t.source, DetailType: t.detailType, Detail: tmpl}}}
	case stateMachineTarget:
		// The execution's name is the invocation's execution id, so a
		// retry names the same execution. UNVERIFIED (SCH-07),
		// scheduler.md 6.4: the name Scheduler gives is unrecorded.
		req.Mechanism = delivery.SchedulerToStates
		req.Target = delivery.StateMachine{ARN: t.machine, Input: tmpl, Name: req.Invocation.ExecutionID}
	default:
		return delivery.Request{}, awserr.Internalf(fmt.Errorf("scheduler: schedule %s has a %T target, which no invocation is built for", r.arn(), sc.Plan.target))
	}
	return req, nil
}

// payloadTemplate is the payload template of sc's invocation scheduled at
// with execution id execID: the target's Input, or, without one, the
// scheduler.input.default payload. An event bus target's is "{}", the
// entry's detail; any other target's is the Scheduled Event envelope,
// compact, members in this order (SCH-07 07-sqs-no-input, 07-sns-no-input,
// 07-lambda-no-input, 07-eventbridge-no-input, recorded 2026-11-03;
// scheduler.md 6.3):
//
//	{"version":"0","id":"<uuid>","detail-type":"Scheduled Event","source":"aws.scheduler","account":"<account>",
//	 "time":"<scheduled time>","region":"<region>","resources":["<schedule ARN>"],"detail":"{}"}
//
// The id is the execution id here: AWS's has the same UUID form, and
// whether it is the execution id is unrecorded. A Step Functions target
// takes the envelope too: inferred, SCH-07 recorded SQS, SNS and Lambda.
func (sc *schedule) payloadTemplate(r scheduleRef, at time.Time, execID string) (string, *awserr.Error) {
	if sc.Echo.Target.Input != nil {
		return *sc.Echo.Target.Input, nil
	}
	if _, ok := sc.Plan.target.(busTarget); ok {
		return emptyJSONObject, nil
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(scheduledEvent{
		Version: "0", ID: execID, DetailType: "Scheduled Event", Source: "aws.scheduler",
		Account: r.scope.Account.String(), Time: at.UTC().Format("2006-01-02T15:04:05Z"), Region: r.scope.Region.String(),
		Resources: []string{r.arn().String()}, Detail: emptyJSONObject,
	}); err != nil {
		return "", awserr.Internalf(fmt.Errorf("scheduler: writing schedule %s's Scheduled Event payload: %w", r.arn(), err))
	}
	return string(bytes.TrimSuffix(out.Bytes(), []byte("\n"))), nil
}

// scheduledEvent is the default payload's envelope, its members in AWS's
// order (payloadTemplate).
type scheduledEvent struct {
	Version    string   `json:"version"`
	ID         string   `json:"id"`
	DetailType string   `json:"detail-type"`
	Source     string   `json:"source"`
	Account    string   `json:"account"`
	Time       string   `json:"time"`
	Region     string   `json:"region"`
	Resources  []string `json:"resources"`
	Detail     string   `json:"detail"`
}
