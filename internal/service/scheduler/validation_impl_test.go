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
// not hold, Go's "Local" and a path are ValidationException (an empty
// name fails the model's length constraint first).
// UNVERIFIED (SCH-04), scheduler.md 5.4: the refusal text is understudy's.
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
		for _, zone := range []string{"Mars/Olympus_Mons", "Local", "../etc/passwd"} {
			in := schedule("bad", "rate(1 hour)", h.queueTarget("q", nil))
			in.ScheduleExpressionTimezone = new(zone)
			wantError(h, errOf(call(h, schedulerapi.OpCreateSchedule, in)), "ValidationException", 400,
				"Invalid ScheduleExpressionTimezone "+zone+": not a time zone of the IANA Time Zone Database.")
		}
	})
}

// The future-fire check refuses a rate or cron with no fire in
// [max(now, StartDate), EndDate) with the captured text (scheduler.md
// 2.4, 14.3.6); present dates render as RFC 3339 UTC, UNVERIFIED (SCH-03).
// A schedule that passes the check reaches the 501 of what is not built
// yet, not a refusal.
func TestFutureFireCheck(t *testing.T) {
	run(t, func(h *harness) {
		now := h.now().Truncate(time.Second)
		rfc := func(x time.Time) string { return x.UTC().Format(time.RFC3339) }
		refused := []struct {
			expr       string
			start, end *time.Time
		}{
			{"rate(1 hour)", nil, new(now.Add(-time.Hour))},
			{"rate(1 hour)", new(now.Add(-3 * time.Hour)), new(now.Add(-time.Minute))},
			{"cron(0 8 1 1 ? 2099)", nil, new(time.Date(2098, 1, 1, 0, 0, 0, 0, time.UTC))},
		}
		for _, c := range refused {
			in := schedule("never", c.expr, h.queueTarget("q", nil))
			in.StartDate, in.EndDate = c.start, c.end
			s, e := "null", "null"
			if c.start != nil {
				s = rfc(*c.start)
			}
			if c.end != nil {
				e = rfc(*c.end)
			}
			wantError(h, errOf(call(h, schedulerapi.OpCreateSchedule, in)), "ValidationException", 400,
				"The schedule expression "+c.expr+", with StartDate "+s+" and EndDate "+e+", is invalid.")
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
			wantError(h, errOf(call(h, schedulerapi.OpCreateSchedule, in)), "NotImplemented", 501, "")
		}
	})
}

// Dates and window: an EndDate not after StartDate and a FLEXIBLE window
// without MaximumWindowInMinutes are ValidationException. UNVERIFIED
// (SCH-04), scheduler.md 5.5, 5.6: both texts are understudy's.
func TestDatesAndWindowRefusals(t *testing.T) {
	run(t, func(h *harness) {
		start := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
		in := schedule("s", "rate(1 hour)", h.queueTarget("q", nil))
		in.StartDate, in.EndDate = new(start), new(start)
		wantError(h, errOf(call(h, schedulerapi.OpCreateSchedule, in)), "ValidationException", 400,
			"EndDate 2099-01-01T00:00:00Z must be after StartDate 2099-01-01T00:00:00Z.")

		in = schedule("s", "rate(1 hour)", h.queueTarget("q", nil))
		in.FlexibleTimeWindow = schedulerapi.FlexibleTimeWindow{Mode: schedulerapi.FlexibleTimeWindowModeFlexible}
		wantError(h, errOf(call(h, schedulerapi.OpCreateSchedule, in)), "ValidationException", 400,
			"FlexibleTimeWindow MaximumWindowInMinutes is required when Mode is FLEXIBLE.")
	})
}

// The validation order of scheduler.md 14.9.3, UNVERIFIED past
// "expression before role" (SCH-04): with two faults the earlier check
// answers; target faults come after every expression, zone, window and
// date fault.
func TestValidationOrder(t *testing.T) {
	run(t, func(h *harness) {
		// A Lambda target answers 501 at the target step until ticket 5.
		laterTarget := schedulerapi.Target{Arn: h.functionArn("f"), RoleArn: h.roleArn()}
		cases := []struct {
			name string
			edit func(in *schedulerapi.CreateScheduleInput)
			want string
		}{
			{"expression before zone", func(in *schedulerapi.CreateScheduleInput) {
				in.ScheduleExpression, in.ScheduleExpressionTimezone = "rate(10 seconds)", new("Nowhere/Nada")
			}, "Invalid Schedule Expression rate(10 seconds)."},
			{"zone before window", func(in *schedulerapi.CreateScheduleInput) {
				in.ScheduleExpressionTimezone = new("Nowhere/Nada")
				in.FlexibleTimeWindow = schedulerapi.FlexibleTimeWindow{Mode: schedulerapi.FlexibleTimeWindowModeFlexible}
			}, "Invalid ScheduleExpressionTimezone Nowhere/Nada: not a time zone of the IANA Time Zone Database."},
			{"window before dates", func(in *schedulerapi.CreateScheduleInput) {
				in.FlexibleTimeWindow = schedulerapi.FlexibleTimeWindow{Mode: schedulerapi.FlexibleTimeWindowModeFlexible}
				in.EndDate = new(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
			}, "FlexibleTimeWindow MaximumWindowInMinutes is required when Mode is FLEXIBLE."},
			{"dates before target", func(in *schedulerapi.CreateScheduleInput) {
				in.EndDate = new(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
				in.Target = laterTarget
			}, "The schedule expression rate(1 hour), with StartDate null and EndDate 2000-01-01T00:00:00Z, is invalid."},
		}
		for _, c := range cases {
			in := schedule("s", "rate(1 hour)", h.queueTarget("q", nil))
			c.edit(in)
			wantError(h, errOf(call(h, schedulerapi.OpCreateSchedule, in)), "ValidationException", 400, c.want)
		}
	})
}
