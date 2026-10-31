package scheduler_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// G6: ListSchedules always returns a Schedules list, empty when the
// account holds none (localstack@8b9a79f
// tests/aws/services/scheduler/test_scheduler.py:L13-L20, validated
// 2024-06-11; scheduler.md 4.5).
func TestListSchedulesG6(t *testing.T) {
	run(t, func(h *harness) {
		out := must(h, schedulerapi.OpListSchedules, &schedulerapi.ListSchedulesInput{})
		if out.Schedules == nil || len(out.Schedules) != 0 || out.NextToken != nil {
			h.Errorf("ListSchedules with none = %#v, %v; want an empty list and no NextToken", out.Schedules, out.NextToken)
		}
	})
}

// ListSchedules in the default group: NamePrefix and State filter, the
// default group can be named, entries carry the ARN, State, whole-second
// dates and Target {Arn} (scheduler.md 4.5). Group then name order, pages
// of MaxResults, and a NextToken of another listing refused with
// ValidationException are UNVERIFIED (SCH-01), scheduler.md 4.5.
func TestListSchedulesInTheDefaultGroup(t *testing.T) {
	run(t, func(h *harness) {
		h.advance(1500 * time.Millisecond)
		for _, name := range []string{"b-1", "a-2", "a-1"} {
			h.create(schedule(name, "rate(1 hour)", h.queueTarget("q", nil)))
		}
		off := schedule("a-off", "rate(1 hour)", h.queueTarget("q", nil))
		off.State = new(schedulerapi.ScheduleStateDisabled)
		h.create(off)

		var pages [][]string
		in := schedulerapi.ListSchedulesInput{NamePrefix: new("a-"), MaxResults: new(int32(2))}
		for {
			out := must(h, schedulerapi.OpListSchedules, &in)
			page := make([]string, 0, len(out.Schedules))
			for _, s := range out.Schedules {
				page = append(page, val(s.Name))
				if val(s.Arn) != h.scheduleArn("default", val(s.Name)) || val(s.GroupName) != "default" || s.Target == nil || s.Target.Arn != h.queueArn("q") {
					h.Errorf("summary %+v, want its ARN, group default and Target {Arn}", s)
				}
				if s.CreationDate == nil || s.CreationDate.Nanosecond() != 0 || s.LastModificationDate == nil {
					h.Errorf("summary dates %v, %v; want whole seconds", s.CreationDate, s.LastModificationDate)
				}
			}
			pages = append(pages, page)
			if out.NextToken == nil {
				break
			}
			in.NextToken = out.NextToken
		}
		if want := [][]string{{"a-1", "a-2"}, {"a-off"}}; fmt.Sprint(pages) != fmt.Sprint(want) {
			h.Errorf("pages = %v, want %v (name order, 2 per page)", pages, want)
		}

		states := map[string]schedulerapi.ScheduleState{}
		for _, s := range must(h, schedulerapi.OpListSchedules, &schedulerapi.ListSchedulesInput{GroupName: new("default")}).Schedules {
			states[val(s.Name)] = *s.State
		}
		if len(states) != 4 || states["a-off"] != schedulerapi.ScheduleStateDisabled || states["b-1"] != schedulerapi.ScheduleStateEnabled {
			h.Errorf("GroupName default lists %v, want all four with their State", states)
		}
		list := must(h, schedulerapi.OpListSchedules, &schedulerapi.ListSchedulesInput{State: new(schedulerapi.ScheduleStateEnabled)}).Schedules
		enabled := make([]string, 0, len(list))
		for _, s := range list {
			enabled = append(enabled, val(s.Name))
		}
		if !slices.Equal(enabled, []string{"a-1", "a-2", "b-1"}) {
			h.Errorf("State ENABLED = %v", enabled)
		}

		wantError(h, errOf(call(h, schedulerapi.OpListSchedules, &schedulerapi.ListSchedulesInput{GroupName: new("nope")})),
			"ResourceNotFoundException", 404, "Schedule group nope does not exist.")
		wantError(h, errOf(call(h, schedulerapi.OpListSchedules, &schedulerapi.ListSchedulesInput{NextToken: new("not-a-token")})),
			"ValidationException", 400, "")
	})
}

// An update that leaves the timing alone keeps the next invocation; one
// that changes the expression restarts the plan at the write instant.
// UNVERIFIED (SCH-09), scheduler.md 14.3.10.
func TestUpdateKeepsOrRestartsTheNextFire(t *testing.T) {
	run(t, func(h *harness) {
		t0 := h.now()
		in := schedule("s", "rate(1 hour)", h.queueTarget("s", new("first")))
		h.create(in)
		h.settled()
		h.advance(20 * time.Minute)

		in.Target.Input = new("second")
		must(h, schedulerapi.OpUpdateSchedule, update(in))
		h.settled()
		if n := len(h.sentTo("s")); n != 1 {
			h.Fatalf("an update of Input alone delivered at once: %d sends, want the create's only", n)
		}
		h.advance(40 * time.Minute)
		got := h.sentTo("s")
		if len(got) != 2 || got[1].sqs.MessageBody != "second" || !got[1].at.Wire().Equal(t0.Add(time.Hour)) {
			h.Fatalf("sends %+v, want the second at %v with the new Input", got, t0.Add(time.Hour))
		}

		h.advance(10 * time.Minute)
		in.ScheduleExpression = "rate(2 hours)"
		must(h, schedulerapi.OpUpdateSchedule, update(in))
		restart := h.now()
		h.settled()
		h.advance(2 * time.Hour)
		got = h.sentTo("s")
		if len(got) != 4 || !got[2].at.Wire().Equal(restart) || !got[3].at.Wire().Equal(restart.Add(2*time.Hour)) {
			h.Errorf("after the expression changed: %d sends, want ones at %v and %v", len(got), restart, restart.Add(2*time.Hour))
		}
		read := h.get("", "s")
		if !read.CreationDate.Equal(t0) || !read.LastModificationDate.Equal(restart) {
			h.Errorf("CreationDate %v, LastModificationDate %v; want %v, %v", read.CreationDate, read.LastModificationDate, t0, restart)
		}
	})
}

// A rate whose StartDate has passed first fires at its first interval at
// or after the write instant, in StartDate's phase. UNVERIFIED (SCH-09),
// scheduler.md 5.2, 5.5.
func TestRateWithAPastStartDateKeepsItsPhase(t *testing.T) {
	run(t, func(h *harness) {
		h.advance(10 * time.Hour)
		start := h.now().Add(-150 * time.Minute)
		in := schedule("s", "rate(1 hour)", h.queueTarget("s", new("x")))
		in.StartDate = new(start)
		h.create(in)
		h.advance(29 * time.Minute)
		if n := len(h.sentTo("s")); n != 0 {
			h.Fatalf("sent %d before the phase's next fire", n)
		}
		h.advance(time.Minute)
		if got := h.sentTo("s"); len(got) != 1 || !got[0].at.Wire().Equal(start.Add(3*time.Hour)) {
			h.Errorf("sends %+v, want one at %v", got, start.Add(3*time.Hour))
		}
	})
}

// A cron in a zone stops before its EndDate: cron(0/15 * * * ? *) with an
// EndDate at 00:40 fires at 00:00, 00:15 and 00:30, then never (scheduler.md
// 5.3, 5.5, 14.3.4).
func TestCronStopsBeforeEndDate(t *testing.T) {
	run(t, func(h *harness) {
		t0 := h.now()
		in := schedule("q", "cron(0/15 * * * ? *)", h.queueTarget("q", new("x")))
		in.ScheduleExpressionTimezone = new("Asia/Kolkata")
		in.EndDate = new(t0.Add(40 * time.Minute))
		h.create(in)
		h.advanceBy(3*time.Hour, 15*time.Minute)
		got := h.sentTo("q")
		if len(got) != 3 {
			h.Fatalf("sent %d, want 3 before EndDate", len(got))
		}
		for i, c := range got {
			if want := t0.Add(time.Duration(i) * 15 * time.Minute); !c.at.Wire().Equal(want) {
				h.Errorf("fire %d at %v, want %v", i+1, c.at.Wire(), want)
			}
		}
	})
}

// GetSchedule echoes the dates to the second, the State, Description and
// KmsKeyArn as sent (tf-acc schedule_test.go:L365-L419, L480-L510;
// scheduler.md 4.2, 5.5); disabling a rate keeps it from firing and
// re-enabling it re-plans from the write instant (scheduler.md 4.6,
// 14.3.10).
func TestGetScheduleEchoesDatesAndState(t *testing.T) {
	run(t, func(h *harness) {
		in := schedule("s", "rate(1 hour)", h.queueTarget("s", nil))
		in.StartDate = new(time.Date(2099, 1, 1, 1, 0, 0, 250_000_000, time.UTC))
		in.EndDate = new(time.Date(2100, 1, 1, 1, 2, 3, 0, time.UTC))
		in.State = new(schedulerapi.ScheduleStateDisabled)
		in.Description = new("d")
		in.KmsKeyArn = new(h.keyArn())
		h.create(in)
		got := h.get("", "s")
		switch {
		case got.StartDate == nil || !got.StartDate.Equal(time.Date(2099, 1, 1, 1, 0, 0, 0, time.UTC)):
			h.Errorf("StartDate = %v, want 2099-01-01T01:00:00Z", got.StartDate)
		case got.EndDate == nil || !got.EndDate.Equal(*in.EndDate):
			h.Errorf("EndDate = %v, want %v", got.EndDate, *in.EndDate)
		case got.State == nil || *got.State != schedulerapi.ScheduleStateDisabled:
			h.Errorf("State = %v, want DISABLED", got.State)
		case val(got.Description) != "d" || val(got.KmsKeyArn) != h.keyArn():
			h.Errorf("Description, KmsKeyArn = %s, %s", val(got.Description), val(got.KmsKeyArn))
		}
	})
}

// A snapshot keeps each run: a DISABLED schedule stays silent, a fired
// at() stays completed, a cron keeps its next fire; a reset drops the
// schedules (ADR 0009; scheduler.md 14.2.4, 14.2.5).
func TestSnapshotKeepsEachRun(t *testing.T) {
	run(t, func(h *harness) {
		t0 := h.now()
		off := schedule("off", "rate(1 hour)", h.queueTarget("off", new("x")))
		off.State = new(schedulerapi.ScheduleStateDisabled)
		h.create(off)
		h.create(schedule("once", atExpr(t0.Add(time.Minute)), h.queueTarget("once", new("x"))))
		cron := schedule("cron", "cron(0 2 * * ? *)", h.queueTarget("cron", new("x")))
		cron.ScheduleExpressionTimezone = new("Europe/Paris")
		cron.StartDate = new(t0.Add(time.Hour))
		cron.Description = new("nightly")
		h.create(cron)
		h.advance(2 * time.Minute)
		if n := len(h.sentTo("once")); n != 1 {
			h.Fatalf("the at() sent %d, want 1", n)
		}
		before := deref(h.get("", "cron"))
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		h.Kernel.Timers().Scheduler().Clear()
		if n := len(must(h, schedulerapi.OpListSchedules, &schedulerapi.ListSchedulesInput{}).Schedules); n != 0 {
			h.Fatalf("after reset ListSchedules = %d, want 0", n)
		}
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		h.svc.Rearm(h.Clock.Now())
		if after := deref(h.get("", "cron")); after != before {
			h.Errorf("after import:\n%s\nbefore:\n%s", after, before)
		}
		h.advance(48 * time.Hour)
		if n := len(h.sentTo("off")); n != 0 {
			h.Errorf("the DISABLED schedule sent %d after import", n)
		}
		if n := len(h.sentTo("once")); n != 1 {
			h.Errorf("the completed at() sent %d in all, want 1", n)
		}
		got := h.sentTo("cron")
		if len(got) != 2 || !got[0].at.Wire().Equal(time.Date(2000, 1, 1, 1, 0, 0, 0, time.UTC)) {
			h.Errorf("cron sends %d, first %v; want 2 from 2000-01-01T01:00Z (02:00 Paris)", len(got), got)
		}
	})
}
