package schedexpr

import (
	"testing"
	"time"
	// The scheduler service embeds the zone database (scheduler.md 14.3.2);
	// the tests embed it too, so they do not depend on the host's zone files.
	_ "time/tzdata"
)

// g3Refusals are the 17 AWS-validated invalid expressions, each answered
// "Invalid Schedule Expression <expr>." with the expression verbatim
// (third-party-recorded, localstack@8b9a79f
// tests/aws/services/scheduler/test_scheduler.snapshot.json:L31-L302,
// validated 2025-01-26; scheduler.md 2.4, 5.1-5.3).
var g3Refusals = []struct {
	expr, lines string
}{
	{"cron(0 1 * * * *)", "L31-L126"},
	{"cron(7 20 * * NOT *)", "L31-L126"},
	{"cron(INVALID)", "L31-L126"},
	{"cron(0 dummy ? * MON-FRI *)", "L31-L126"},
	{"cron(71 8 1 * ? *)", "L31-L126"},
	{"cron()", "L31-L126"},
	{"rate(10 seconds)", "L127-L270"},
	{"rate(10 years)", "L127-L270"},
	{"rate()", "L127-L270"},
	{"rate(10)", "L127-L270"},
	{"rate(10 minutess)", "L127-L270"},
	{"rate(foo minutes)", "L127-L270"},
	{"rate(-10 minutes)", "L127-L270"},
	{"rate( 10 minutes )", "L127-L270"},
	{" rate(10 minutes)", "L255-L270"}, // the leading space is echoed: "Invalid Schedule Expression  rate(10 minutes)."
	{"at(2021-12-31T23:59:59Z)", "L271-L302"},
	{"at(2021-12-31)", "L271-L302"},
}

// G3: every recorded refusal answers AWS's text byte for byte under both
// rate-unit settings (none of them turns on unit agreement).
func TestParseSchedulerRecordedRefusals(t *testing.T) {
	t.Parallel()
	if len(g3Refusals) != 17 {
		t.Fatalf("%d refusals, the snapshot has 17", len(g3Refusals))
	}
	for _, units := range []RateUnits{AnyUnitAgreement, SingularIffOne} {
		for _, c := range g3Refusals {
			_, err := ParseScheduler(c.expr, units)
			want := "Invalid Schedule Expression " + c.expr + "."
			switch {
			case err == nil:
				t.Errorf("ParseScheduler(%q, %v) accepted, want refused (snapshot %s)", c.expr, units, c.lines)
			case err.Message() != want || err.Error() != want:
				t.Errorf("ParseScheduler(%q, %v) = %q / %q, want %q (snapshot %s)", c.expr, units, err.Message(), err.Error(), want, c.lines)
			}
		}
	}
}

// G5: the six AWS-validated cron expressions are accepted as Cron
// (third-party-recorded, localstack@8b9a79f
// tests/aws/services/scheduler/test_scheduler.snapshot.json:L315-L387,
// validated 2026-01-17; scheduler.md 5.3).
func TestParseSchedulerRecordedCrons(t *testing.T) {
	t.Parallel()
	for _, expr := range []string{
		"cron(0-15/1 8-8 ? * 2,3,4,5,6,7 *)",
		"cron(30-30 13-13 ? * 2,4,6 *)",
		"cron(1-31/30 1-1 ? * 1,7 *)",
		"cron(0 18 ? * MON-FRI *)",
		"cron(*/15 * * * ? *)",
		"cron(0-59/10 0-23/2 ? * * *)",
	} {
		ex, err := ParseScheduler(expr, AnyUnitAgreement)
		if err != nil {
			t.Errorf("ParseScheduler(%q) = %v, want accepted", expr, err)
			continue
		}
		if _, ok := ex.(Cron); !ok {
			t.Errorf("ParseScheduler(%q) = %T, want Cron", expr, ex)
		}
	}
}

// G4: an at() with seconds and no zone suffix is accepted
// (third-party-recorded, localstack@8b9a79f
// tests/aws/services/scheduler/test_scheduler.snapshot.json:L303-L314,
// validated 2025-02-02; scheduler.md 5.1), and names its civil time in
// the schedule's zone: at(2022-11-20T13:00:00) in America/Los_Angeles
// "runs once at 1pm UTC-8" (aws-doc
// https://docs.aws.amazon.com/scheduler/latest/UserGuide/schedule-types.html).
func TestParseSchedulerAt(t *testing.T) {
	t.Parallel()
	ex, err := ParseScheduler("at(2022-12-31T23:59:59)", AnyUnitAgreement)
	if err != nil {
		t.Fatalf("ParseScheduler(at(2022-12-31T23:59:59)) = %v, want accepted", err)
	}
	at, ok := ex.(At)
	if !ok {
		t.Fatalf("ParseScheduler(at(...)) = %T, want At", ex)
	}
	if got, want := at.In(time.UTC), time.Date(2022, 12, 31, 23, 59, 59, 0, time.UTC); !got.Equal(want) {
		t.Errorf("At.In(UTC) = %v, want %v (seconds are kept, snapshot L303-L314)", got, want)
	}

	la, lerr := time.LoadLocation("America/Los_Angeles")
	if lerr != nil {
		t.Fatal(lerr)
	}
	ex, err = ParseScheduler("at(2022-11-20T13:00:00)", AnyUnitAgreement)
	if err != nil {
		t.Fatalf("ParseScheduler(at(2022-11-20T13:00:00)) = %v", err)
	}
	at, ok = ex.(At)
	if !ok {
		t.Fatalf("ParseScheduler(at(...)) = %T, want At", ex)
	}
	if got, want := at.In(la), time.Date(2022, 11, 20, 21, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("at(2022-11-20T13:00:00) in America/Los_Angeles = %v, want %v (1pm UTC-8, aws-doc schedule-types)", got.UTC(), want)
	}
}

// Rate units: AnyUnitAgreement takes each of the six words the API
// reference lists with any positive value, as the census uses them
// (`rate(5 minute)`, `rate(1 minutes)`; scheduler.md 5.2, 12; aws-doc
// https://docs.aws.amazon.com/scheduler/latest/APIReference/API_CreateSchedule.html);
// SingularIffOne is the rules grammar and refuses both with the Scheduler
// text (scheduler.md 2.4). Which side AWS takes is switch
// scheduler.rate.unit-agreement; the service test reads the switch.
func TestParseSchedulerRateUnits(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		expr  string
		every time.Duration
	}{
		{"rate(5 minute)", 5 * time.Minute},
		{"rate(1 minutes)", time.Minute},
		{"rate(5 minutes)", 5 * time.Minute},
		{"rate(1 minute)", time.Minute},
		{"rate(2 hour)", 2 * time.Hour},
		{"rate(1 hours)", time.Hour},
		{"rate(3 day)", 72 * time.Hour},
		{"rate(1 days)", 24 * time.Hour}, // "days represents a 24-hour duration" (aws-doc schedule-types; scheduler.md 5.2)
	} {
		ex, err := ParseScheduler(c.expr, AnyUnitAgreement)
		if err != nil {
			t.Errorf("ParseScheduler(%q, any) = %v, want accepted", c.expr, err)
			continue
		}
		r, ok := ex.(Rate)
		if !ok {
			t.Errorf("ParseScheduler(%q, any) = %T, want Rate", c.expr, ex)
			continue
		}
		if r.Interval() != c.every {
			t.Errorf("ParseScheduler(%q, any) interval = %v, want %v", c.expr, r.Interval(), c.every)
		}
	}
	for _, expr := range []string{"rate(5 minute)", "rate(1 minutes)", "rate(2 hour)", "rate(1 days)"} {
		_, err := ParseScheduler(expr, SingularIffOne)
		if want := "Invalid Schedule Expression " + expr + "."; err == nil || err.Message() != want {
			t.Errorf("ParseScheduler(%q, singular-iff-one) = %v, want %q", expr, err, want)
		}
	}
	for _, expr := range []string{"rate(5 minutes)", "rate(1 minute)", "rate(1 hour)", "rate(2 days)"} {
		if _, err := ParseScheduler(expr, SingularIffOne); err != nil {
			t.Errorf("ParseScheduler(%q, singular-iff-one) = %v, want accepted", expr, err)
		}
	}
	if AnyUnitAgreement.String() != "any" || SingularIffOne.String() != "singular-iff-one" {
		t.Errorf("RateUnits strings = %q, %q; want the switch values any, singular-iff-one", AnyUnitAgreement, SingularIffOne)
	}
}

// The rules grammar is untouched: Parse still refuses with the rules'
// text, never the Scheduler one (secrets-logs-events-alarms.md 4.2,
// AWS-REC; scheduler.md 14.3.1).
func TestParseKeepsTheRulesText(t *testing.T) {
	t.Parallel()
	for _, expr := range []string{"rate(5 minute)", "at(2022-12-31T23:59:59)", "cron()"} {
		_, err := Parse(expr)
		if err == nil || err.Message() != "Parameter ScheduleExpression is not valid." {
			t.Errorf("Parse(%q) = %v, want the rules' refusal", expr, err)
		}
	}
}

func mustCron(t *testing.T, expr string) Cron {
	t.Helper()
	ex, err := ParseScheduler(expr, AnyUnitAgreement)
	if err != nil {
		t.Fatalf("ParseScheduler(%q): %v", expr, err)
	}
	c, ok := ex.(Cron)
	if !ok {
		t.Fatalf("ParseScheduler(%q) = %T, want Cron", expr, ex)
	}
	return c
}

// Daylight saving time in America/Los_Angeles (aws-doc
// https://docs.aws.amazon.com/scheduler/latest/UserGuide/schedule-types.html,
// scheduler.md 5.4): "if a cron expression falls on a non-existent date
// and time, your schedule invocation is skipped", and in a repeated hour
// "your schedule runs only once and does not repeat its invocation". In
// 2026 the clocks spring forward on 8 March at 02:00 PST and fall back on
// 1 November at 02:00 PDT.
func TestNextInDaylightSavingTime(t *testing.T) {
	t.Parallel()
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	utc := func(y int, m time.Month, d, h, mi int) time.Time { return time.Date(y, m, d, h, mi, 0, 0, time.UTC) }

	// 02:30 does not exist on 8 March: the next fire is 9 March 02:30 PDT.
	spring := mustCron(t, "cron(30 2 * * ? *)")
	got, ok := NextIn(spring, utc(2026, 3, 8, 8, 0), la) // 00:00 PST
	if want := utc(2026, 3, 9, 9, 30); !ok || !got.Equal(want) {
		t.Errorf("cron(30 2 * * ? *) after 8 March 00:00 PST = %v %v, want %v (skipped in the gap)", got, ok, want)
	}

	// 01:30 happens twice on 1 November: only the first, 01:30 PDT, fires.
	fall := mustCron(t, "cron(30 1 * * ? *)")
	first, ok := NextIn(fall, utc(2026, 11, 1, 7, 0), la) // 00:00 PDT
	if want := utc(2026, 11, 1, 8, 30); !ok || !first.Equal(want) {
		t.Fatalf("cron(30 1 * * ? *) after 1 November 00:00 PDT = %v %v, want %v", first, ok, want)
	}
	second, ok := NextIn(fall, first, la)
	if want := utc(2026, 11, 2, 9, 30); !ok || !second.Equal(want) {
		t.Errorf("cron(30 1 * * ? *) after %v = %v %v, want %v (the repeated 01:30 PST does not fire)", first, second, ok, want)
	}

	// UTC has no DST adjustment (aws-doc schedule-types).
	daily := mustCron(t, "cron(30 1 * * ? *)")
	if got, ok := NextIn(daily, utc(2026, 11, 1, 0, 0), time.UTC); !ok || !got.Equal(utc(2026, 11, 1, 1, 30)) {
		t.Errorf("cron(30 1 * * ? *) in UTC = %v %v, want 01:30Z", got, ok)
	}

	// A cron that never fires again reports no fire.
	past := mustCron(t, "cron(30 0 1 1 ? 2022)")
	if got, ok := NextIn(past, utc(2023, 1, 1, 0, 0), la); ok {
		t.Errorf("cron(30 0 1 1 ? 2022) after 2023 = %v, want none", got)
	}
}
