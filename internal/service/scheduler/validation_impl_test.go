package scheduler_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
)

// A time zone is resolved in the embedded IANA database: the census's
// legacy link name US/Eastern and Europe/Paris are accepted with a rate,
// which ignores the zone (scheduler.md 5.2, 5.4); a name the database does
// not hold, Go's "Local" and a path are ValidationException "Invalid
// timezone <name>." (SCH-04 04-bad-timezone, recorded 2026-11-03;
// scheduler.md 2.4). An empty name fails the model's length constraint
// first.
func TestScheduleExpressionTimezone(t *testing.T) {
	run(t, func(h *harness) {
		for i, zone := range []string{"Europe/Paris", "US/Eastern", "America/Los_Angeles"} {
			in := schedule(fmt.Sprintf("ok-%d", i), "rate(1 hour)", h.queueTarget("q", nil))
			in.ScheduleExpressionTimezone = new(zone)
			h.create(in)
			if got := val(h.get("", in.Name).ScheduleExpressionTimezone); got != zone {
				h.Errorf("case %d: time zone read back %q, want %q as sent", i, got, zone)
			}
		}
		for _, zone := range []string{"Mars/Olympus", "Local", "../etc/passwd"} {
			in := schedule("bad", "rate(1 hour)", h.queueTarget("q", nil))
			in.ScheduleExpressionTimezone = new(zone)
			wantError(h, errOf(call(h, schedulerapi.OpCreateSchedule, in)), "ValidationException", 400, "Invalid timezone "+zone+".")
		}
	})
}

// The future-fire check refuses a rate or cron with no fire in
// [max(now, StartDate), EndDate) with "The schedule expression <expr>,
// with StartDate <s> and EndDate <e>, is invalid because it will never
// schedule an invocation." (SCH-03, recorded 2026-11-03; scheduler.md
// 2.4, 5.3). A date renders as Java's LocalDateTime does, without zone:
// minutes when the seconds are zero, else seconds, then milliseconds when
// there are any; an ENABLED schedule without a StartDate renders the
// request instant, a DISABLED one null (03-past-cron-enabled, 03-e24). A
// schedule that passes the check is accepted.
func TestFutureFireCheck(t *testing.T) {
	run(t, func(h *harness) {
		now := h.now().Truncate(time.Second)
		local := func(x time.Time) string {
			x = x.UTC()
			switch {
			case x.Nanosecond() >= int(time.Millisecond):
				return x.Format("2006-01-02T15:04:05.000")
			case x.Second() != 0:
				return x.Format("2006-01-02T15:04:05")
			}
			return x.Format("2006-01-02T15:04")
		}
		refused := []struct {
			expr       string
			start, end *time.Time
			state      schedulerapi.ScheduleState
			want       string
		}{
			{
				"rate(1 hour)", new(now.Add(-3 * time.Hour)), new(now.Add(-time.Minute)), schedulerapi.ScheduleStateEnabled,
				local(now.Add(-3*time.Hour)) + " and EndDate " + local(now.Add(-time.Minute)),
			},
			{
				"cron(0 0 1 1 ? 2021)", new(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)), new(time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)),
				schedulerapi.ScheduleStateDisabled, "2020-01-01T00:00 and EndDate 2020-06-01T00:00",
			},
			{"cron(0 0 1 1 ? 1999)", nil, nil, schedulerapi.ScheduleStateDisabled, "null and EndDate null"},
			{"cron(0 0 1 1 ? 1999)", nil, nil, schedulerapi.ScheduleStateEnabled, local(h.now()) + " and EndDate null"},
			// Years outside 1970-2199 parse and never fire (03-e17, 03-e18).
			{"cron(0 12 * * ? 1969)", nil, nil, schedulerapi.ScheduleStateDisabled, "null and EndDate null"},
			{"cron(0 12 * * ? 2200)", nil, nil, schedulerapi.ScheduleStateDisabled, "null and EndDate null"},
			{
				"cron(0 8 1 1 ? 2099)", new(time.Date(2098, 1, 1, 0, 0, 30, 250_000_000, time.UTC)), new(time.Date(2098, 6, 1, 0, 0, 0, 0, time.UTC)),
				schedulerapi.ScheduleStateEnabled, "2098-01-01T00:00:30.250 and EndDate 2098-06-01T00:00",
			},
		}
		for _, c := range refused {
			in := schedule("never", c.expr, h.queueTarget("q", nil))
			in.StartDate, in.EndDate, in.State = c.start, c.end, new(c.state)
			wantError(h, errOf(call(h, schedulerapi.OpCreateSchedule, in)), "ValidationException", 400,
				"The schedule expression "+c.expr+", with StartDate "+c.want+", is invalid because it will never schedule an invocation.")
		}

		// A rate whose StartDate is past fires in its phase before EndDate;
		// a cron in Paris fires 08:00 local inside its window.
		passing := []*schedulerapi.CreateScheduleInput{
			schedule("phase", "rate(1 hour)", h.queueTarget("q", nil)),
			schedule("paris", "cron(0 8 1 1 ? 2099)", h.queueTarget("q", nil)),
			schedule("past-at", "at(1999-12-31T23:59:59)", h.queueTarget("q", nil)),
		}
		passing[0].StartDate, passing[0].EndDate = new(now.Add(-150*time.Minute)), new(now.Add(31*time.Minute))
		passing[1].ScheduleExpressionTimezone = new("Europe/Paris")
		passing[1].StartDate, passing[1].EndDate = new(time.Date(2099, 1, 1, 6, 0, 0, 0, time.UTC)), new(time.Date(2099, 1, 1, 7, 0, 1, 0, time.UTC))
		passing[2].EndDate = new(now.Add(-time.Hour)) // at() ignores the dates (scheduler.md 5.1)
		for _, in := range passing {
			h.create(in)
		}
	})
}

// Dates and window (SCH-03, SCH-04, recorded 2026-11-03; scheduler.md
// 2.4, 5.5, 5.6): an EndDate not after StartDate, or not after the
// request instant when no StartDate is sent, in either State, is "The
// StartDate you specify must come before the EndDate." (04-end-before-start,
// 03-past-rate-end); FLEXIBLE without MaximumWindowInMinutes and OFF with
// one are refused with their own texts (04-flexible-no-window,
// 04-off-with-window).
func TestDatesAndWindowRefusals(t *testing.T) {
	run(t, func(h *harness) {
		const order = "The StartDate you specify must come before the EndDate."
		start := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
		in := schedule("s", "rate(1 hour)", h.queueTarget("q", nil))
		in.StartDate, in.EndDate = new(start.Add(time.Hour)), new(start)
		wantError(h, errOf(call(h, schedulerapi.OpCreateSchedule, in)), "ValidationException", 400, order)

		for _, state := range []schedulerapi.ScheduleState{schedulerapi.ScheduleStateDisabled, schedulerapi.ScheduleStateEnabled} {
			in = schedule("s", "rate(1 hour)", h.queueTarget("q", nil))
			in.State, in.EndDate = new(state), new(h.now().Add(-time.Hour))
			wantError(h, errOf(call(h, schedulerapi.OpCreateSchedule, in)), "ValidationException", 400, order)
		}

		in = schedule("s", "rate(1 hour)", h.queueTarget("q", nil))
		in.FlexibleTimeWindow = schedulerapi.FlexibleTimeWindow{Mode: schedulerapi.FlexibleTimeWindowModeFlexible}
		wantError(h, errOf(call(h, schedulerapi.OpCreateSchedule, in)), "ValidationException", 400,
			"MaximumWindowInMinutes must be provided when FlexibleTimeWindowMode is set to FLEXIBLE.")

		in = schedule("s", "rate(1 hour)", h.queueTarget("q", nil))
		in.FlexibleTimeWindow = schedulerapi.FlexibleTimeWindow{Mode: schedulerapi.FlexibleTimeWindowModeOff, MaximumWindowInMinutes: new(int32(5))}
		wantError(h, errOf(call(h, schedulerapi.OpCreateSchedule, in)), "ValidationException", 400,
			"MaximumWindowInMinutes must not be provided when FlexibleTimeWindowMode is set to OFF.")
	})
}

// The validation order SCH-04 recorded with two faults per request
// (recorded 2026-11-03; scheduler.md 4.7): the time zone before the
// expression and the group, the future-fire check and the group before
// the target, the target before the role.
func TestValidationOrder(t *testing.T) {
	run(t, func(h *harness) {
		dynamo := schedulerapi.Target{Arn: fmt.Sprintf("arn:aws:dynamodb:%s:%s:table/t", h.Scope.Region, h.Scope.Account), RoleArn: h.roleArn()}
		cases := []struct {
			name   string
			edit   func(in *schedulerapi.CreateScheduleInput)
			status int
			want   string
		}{
			{"zone before expression (04-order-expr-tz)", func(in *schedulerapi.CreateScheduleInput) {
				in.ScheduleExpression, in.ScheduleExpressionTimezone = "rate(10 seconds)", new("Mars/Olympus")
			}, 400, "Invalid timezone Mars/Olympus."},
			{"zone before group (04-order-tz-group)", func(in *schedulerapi.CreateScheduleInput) {
				in.ScheduleExpressionTimezone, in.GroupName = new("Mars/Olympus"), new("nogroup")
			}, 400, "Invalid timezone Mars/Olympus."},
			{"group before target", func(in *schedulerapi.CreateScheduleInput) {
				in.GroupName, in.Target = new("nogroup"), dynamo
			}, 404, "Schedule group nogroup does not exist."},
			{"future fire before target", func(in *schedulerapi.CreateScheduleInput) {
				in.ScheduleExpression, in.State, in.Target = "cron(0 0 1 1 ? 1999)", new(schedulerapi.ScheduleStateDisabled), dynamo
			}, 400, "The schedule expression cron(0 0 1 1 ? 1999), with StartDate null and EndDate null, is invalid because it will never schedule an invocation."},
		}
		for _, c := range cases {
			in := schedule("s", "rate(1 hour)", h.queueTarget("q", nil))
			c.edit(in)
			err := errOf(call(h, schedulerapi.OpCreateSchedule, in))
			if err == nil || err.ID.Status != c.status || err.Message != c.want {
				h.Errorf("%s: error = %v, want %d %q", c.name, err, c.status, c.want)
			}
		}
	})
}
