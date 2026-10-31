package scheduler

import (
	"context"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/paging"
	"github.com/sustinbebustin/understudy/internal/store"
)

// createSchedule stores a schedule in its group and arms its first fire
// (scheduler.md 4.1, 14.3). The group is looked up after the members are
// validated (14.9.3); a missing one answers "Schedule group <name> does
// not exist." (14.7.4). Then the role's trust, iam:PassRole and KMS are
// authorized (authorizeWrite). ClientToken is accepted and not used for
// idempotency (SCH-02).
func (s *Service) createSchedule(ctx context.Context, c *kernel.Call, in *schedulerapi.CreateScheduleInput) (*schedulerapi.CreateScheduleOutput, *awserr.Error) {
	r := scheduleRef{scope: c.Scope(), group: groupName(in.GroupName), name: in.Name}
	m := createMembers(in)
	p, err := s.authorized(ctx, c, r, &m)
	if err != nil {
		return nil, err
	}
	_, err = kernel.Write(c, s.cells, r.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if !st.hasGroup(r.group) {
			return struct{}{}, errGroupMissing(r.group)
		}
		if _, exists := st.get(r); exists {
			return struct{}{}, errScheduleExists(r.group, r.name)
		}
		sc := schedule{Echo: echoOf(&m), Plan: p, Run: s.firstRun(&p, now), Created: now, Modified: now}
		st.put(r, sc)
		s.arm(tx, r, sc.Run)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &schedulerapi.CreateScheduleOutput{ScheduleArn: r.arn().String()}, nil
}

// updateSchedule replaces every member of a schedule; members left out
// take their defaults (scheduler.md 4.3). An update that leaves the timing
// as it was keeps the next invocation; any other, enabling included,
// re-plans from the write instant (14.3.10, echo.sameTiming). ClientToken
// is accepted and not used for idempotency (SCH-02).
func (s *Service) updateSchedule(ctx context.Context, c *kernel.Call, in *schedulerapi.UpdateScheduleInput) (*schedulerapi.UpdateScheduleOutput, *awserr.Error) {
	r := scheduleRef{scope: c.Scope(), group: groupName(in.GroupName), name: in.Name}
	m := updateMembers(in)
	p, err := s.authorized(ctx, c, r, &m)
	if err != nil {
		return nil, err
	}
	_, err = kernel.Write(c, s.cells, r.scope, func(st *State, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if !st.hasGroup(r.group) {
			return struct{}{}, errGroupMissing(r.group)
		}
		old, ok := st.get(r)
		if !ok {
			return struct{}{}, errScheduleMissing(r.group, r.name)
		}
		sc := schedule{Echo: echoOf(&m), Plan: p, Run: old.Run, Created: old.Created, Modified: now}
		if !old.Echo.sameTiming(&sc.Echo) {
			sc.Run = s.firstRun(&p, now)
		}
		st.put(r, sc)
		if !s.arm(tx, r, sc.Run) {
			tx.Disarm(s.fire, r.arn().String())
		}
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &schedulerapi.UpdateScheduleOutput{ScheduleArn: r.arn().String()}, nil
}

// authorized is the plan of a create or update of r with members m, once
// the members are validated, the group is found and the caller and role
// are authorized (scheduler.md 14.9.3); the write checks the group again.
func (s *Service) authorized(ctx context.Context, c *kernel.Call, r scheduleRef, m *members) (plan, *awserr.Error) {
	p, err := accept(r.scope, m, s.opts.RateUnits, atWrite(c.Admitted().Wire()))
	if err != nil {
		return plan{}, err
	}
	if !s.groupHeld(r.scope, r.group) {
		return plan{}, errGroupMissing(r.group)
	}
	if err := authorizeWrite(ctx, c, r, &p); err != nil {
		return plan{}, err
	}
	return p, nil
}

// listSchedulesPage is ListSchedules' page without MaxResults.
// UNVERIFIED (SCH-01), scheduler.md 4.5: AWS's default page is unrecorded.
const listSchedulesPage = 100

// listingSchedules names ListSchedules' markers.
const listingSchedules paging.Listing = "scheduler.ListSchedules"

// listSchedules lists the scope's schedules, filtered by group, name
// prefix and State, a page at a time (scheduler.md 4.5). Every group's
// schedules without a group filter; a group the scope does not hold is
// ResourceNotFoundException "Schedule group <name> does not exist."
// (inferred, 14.7.4). Schedules come in group, then name order.
// UNVERIFIED (SCH-01): order and token format are unrecorded.
func (s *Service) listSchedules(_ context.Context, c *kernel.Call, in *schedulerapi.ListSchedulesInput) (*schedulerapi.ListSchedulesOutput, *awserr.Error) {
	scope := c.Scope()
	type found struct {
		items []schedulerapi.ScheduleSummary
		group bool
	}
	got := found{group: in.GroupName == nil || *in.GroupName == defaultGroup}
	if cell, ok := s.cells.Lookup(scope); ok {
		got = store.Read(cell, func(st *State) found {
			out := found{group: in.GroupName == nil || st.hasGroup(*in.GroupName)}
			for group, g := range st.Schedules {
				for name := range g {
					sc := g[name]
					if listed(in, group, name, &sc) {
						out.items = append(out.items, summary(scheduleRef{scope: scope, group: group, name: name}, &sc))
					}
				}
			}
			return out
		})
	}
	if !got.group {
		return nil, errGroupMissing(*in.GroupName)
	}
	items := got.items
	n := listSchedulesPage
	if in.MaxResults != nil {
		n = int(*in.MaxResults)
	}
	p, perr := paging.SortedKeyset(listingSchedules, items, summaryKey, n, in.NextToken)
	if perr != nil {
		return nil, errBadNextToken("ListSchedules")
	}
	out := &schedulerapi.ListSchedulesOutput{Schedules: make([]schedulerapi.ScheduleSummary, 0, len(p.Items)), NextToken: p.Next}
	out.Schedules = append(out.Schedules, p.Items...)
	return out, nil
}

// listed reports whether ListSchedules' filters keep the schedule.
func listed(in *schedulerapi.ListSchedulesInput, group, name string, sc *schedule) bool {
	return (in.GroupName == nil || *in.GroupName == group) &&
		(in.NamePrefix == nil || strings.HasPrefix(name, *in.NamePrefix)) &&
		(in.State == nil || *in.State == sc.Echo.state())
}

// summaryKey orders summaries by group, then name: the separator, a
// space, sorts before every character a group or name may hold
// ([0-9a-zA-Z-_.]+, model).
func summaryKey(s schedulerapi.ScheduleSummary) string {
	var group, name string
	if s.GroupName != nil {
		group = *s.GroupName
	}
	if s.Name != nil {
		name = *s.Name
	}
	return group + " " + name
}

// summary is a schedule's ListSchedules entry: its ARN, names, State,
// dates and target ARN only (scheduler.md 4.5).
func summary(r scheduleRef, sc *schedule) schedulerapi.ScheduleSummary {
	arn := r.arn().String()
	state := sc.Echo.state()
	created, modified := wholeSeconds(sc.Created), wholeSeconds(sc.Modified)
	return schedulerapi.ScheduleSummary{
		Arn: &arn, Name: &r.name, GroupName: &r.group, State: &state,
		CreationDate: &created, LastModificationDate: &modified,
		Target: &schedulerapi.TargetSummary{Arn: sc.Echo.Target.Arn},
	}
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

// firstRun is the run of p written at now, its due instant moved inside a
// FLEXIBLE window (offset).
func (s *Service) firstRun(p *plan, now clock.Instant) run {
	return offset(p, firstRun(*p, now, s.opts.AtPastDue), s.opts.FlexibleOffset, s.ids)
}

// arm arms the fire timer of a schedule whose run has a next invocation,
// and reports whether it has one.
func (s *Service) arm(tx *kernel.Tx, r scheduleRef, next run) bool {
	n, ok := next.(scheduled)
	if ok {
		tx.Arm(s.fire, r.arn().String(), n.Due)
	}
	return ok
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
	state := e.state()
	age, retries := e.Target.retryPolicy()
	target := &schedulerapi.Target{
		Arn: e.Target.Arn, RoleArn: e.Target.RoleArn, Input: e.Target.Input, DeadLetterConfig: e.Target.DeadLetter,
		RetryPolicy: &schedulerapi.RetryPolicy{MaximumEventAgeInSeconds: &age, MaximumRetryAttempts: &retries},
	}
	e.Target.parameters(target)
	arn := r.arn().String()
	created, modified := wholeSeconds(sc.Created), wholeSeconds(sc.Modified)
	return &schedulerapi.GetScheduleOutput{
		Arn: &arn, GroupName: &r.group, Name: &r.name,
		ScheduleExpression: &e.Expression, ScheduleExpressionTimezone: e.Timezone,
		StartDate: secondOf(e.StartDate), EndDate: secondOf(e.EndDate),
		Description: e.Description, State: &state, KmsKeyArn: e.KmsKeyArn,
		CreationDate: &created, LastModificationDate: &modified,
		Target: target, FlexibleTimeWindow: &schedulerapi.FlexibleTimeWindow{Mode: e.Window, MaximumWindowInMinutes: e.MaxWindow},
		ActionAfterCompletion: &action,
	}
}

// wholeSeconds is at on the wire, cut to the second.
func wholeSeconds(at clock.Instant) time.Time { return at.Wire().Truncate(time.Second) }

// secondOf is a sent StartDate or EndDate as read back, to the second
// (tf-acc schedule_test.go:L365-L419; scheduler.md 5.5); nil when it was
// not sent.
func secondOf(d *time.Time) *time.Time {
	if d == nil {
		return nil
	}
	t := d.UTC().Truncate(time.Second)
	return &t
}
