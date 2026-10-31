package scheduler

import (
	"context"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/store"
)

// createSchedule stores a schedule in its group and arms its first fire
// (scheduler.md 4.1, 14.3). ClientToken is accepted and not used for
// idempotency (SCH-02).
func (s *Service) createSchedule(_ context.Context, c *kernel.Call, in *schedulerapi.CreateScheduleInput) (*schedulerapi.CreateScheduleOutput, *awserr.Error) {
	r := scheduleRef{scope: c.Scope(), group: groupName(in.GroupName), name: in.Name}
	if r.group != defaultGroup {
		return nil, errNotYet("GroupName " + r.group + " (schedule groups other than default)")
	}
	m := createMembers(in)
	p, err := accept(r.scope, &m, s.opts.RateUnits)
	if err != nil {
		return nil, err
	}
	_, err = kernel.Write(c, s.cells, r.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if _, exists := st.get(r); exists {
			return struct{}{}, errScheduleExists(r.group, r.name)
		}
		sc := schedule{Echo: echoOf(&m), Plan: p, Run: firstRun(now), Created: now, Modified: now}
		st.put(r, sc)
		s.arm(tx, r, sc.Run)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &schedulerapi.CreateScheduleOutput{ScheduleArn: r.arn().String()}, nil
}

// getSchedule reads a schedule back as accepted, with the defaults AWS
// fills (scheduler.md 4.2, 11.3, 14.10.1).
func (s *Service) getSchedule(_ context.Context, c *kernel.Call, in *schedulerapi.GetScheduleInput) (*schedulerapi.GetScheduleOutput, *awserr.Error) {
	r := scheduleRef{scope: c.Scope(), group: groupName(in.GroupName), name: in.Name}
	sc, ok := s.read(r)
	if !ok {
		return nil, errScheduleMissing(r.group, r.name)
	}
	c.Touch(r.arn())
	return readShape(r, &sc), nil
}

// deleteSchedule removes a schedule and disarms its fire (scheduler.md
// 4.4). ClientToken is accepted and not used for idempotency (SCH-02).
func (s *Service) deleteSchedule(_ context.Context, c *kernel.Call, in *schedulerapi.DeleteScheduleInput) (*schedulerapi.DeleteScheduleOutput, *awserr.Error) {
	r := scheduleRef{scope: c.Scope(), group: groupName(in.GroupName), name: in.Name}
	_, err := kernel.Write(c, s.cells, r.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if _, ok := st.get(r); !ok {
			return struct{}{}, errScheduleMissing(r.group, r.name)
		}
		st.remove(r)
		tx.Disarm(s.fire, r.arn().String())
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &schedulerapi.DeleteScheduleOutput{}, nil
}

// read returns the schedule r names, if its scope holds it.
func (s *Service) read(r scheduleRef) (schedule, bool) {
	cell, ok := s.cells.Lookup(r.scope)
	if !ok {
		return schedule{}, false
	}
	type found struct {
		sc schedule
		ok bool
	}
	got := store.Read(cell, func(st *State) found {
		sc, ok := st.get(r)
		return found{sc: sc, ok: ok}
	})
	return got.sc, got.ok
}

// arm arms the fire timer of a schedule whose run has a next invocation.
func (s *Service) arm(tx *kernel.Tx, r scheduleRef, next run) {
	if n, ok := next.(scheduled); ok {
		tx.Arm(s.fire, r.arn().String(), n.Due)
	}
}

// readShape is GetSchedule's answer: the members as accepted, with
// ActionAfterCompletion NONE, RetryPolicy 86400/185, GroupName and State
// ENABLED filled when unset (tf-acc schedule_test.go:L199-L223), the
// members never sent absent, MaximumWindowInMinutes absent under OFF, and
// the dates as whole seconds. UNVERIFIED (SCH-01), scheduler.md 4.2: the
// absent members and whole-second dates are understudy's reading.
func readShape(r scheduleRef, sc *schedule) *schedulerapi.GetScheduleOutput {
	e := &sc.Echo
	action := schedulerapi.ActionAfterCompletionNone
	if e.Action != nil {
		action = *e.Action
	}
	state := schedulerapi.ScheduleStateEnabled
	if e.State != nil {
		state = *e.State
	}
	age, retries := e.Target.retryPolicy()
	target := &schedulerapi.Target{
		Arn: e.Target.Arn, RoleArn: e.Target.RoleArn, Input: e.Target.Input,
		RetryPolicy: &schedulerapi.RetryPolicy{MaximumEventAgeInSeconds: &age, MaximumRetryAttempts: &retries},
	}
	if e.Target.MessageGroupID != nil {
		target.SqsParameters = &schedulerapi.SqsParameters{MessageGroupId: e.Target.MessageGroupID}
	}
	arn := r.arn().String()
	created, modified := wholeSeconds(sc.Created), wholeSeconds(sc.Modified)
	return &schedulerapi.GetScheduleOutput{
		Arn: &arn, GroupName: &r.group, Name: &r.name,
		ScheduleExpression: &e.Expression, ScheduleExpressionTimezone: e.Timezone,
		Description: e.Description, State: &state, KmsKeyArn: e.KmsKeyArn,
		CreationDate: &created, LastModificationDate: &modified,
		Target: target, FlexibleTimeWindow: &schedulerapi.FlexibleTimeWindow{Mode: e.Window},
		ActionAfterCompletion: &action,
	}
}

// wholeSeconds is at on the wire, cut to the second.
func wholeSeconds(at clock.Instant) time.Time { return at.Wire().Truncate(time.Second) }
