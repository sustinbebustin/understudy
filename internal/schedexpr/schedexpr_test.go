package schedexpr

import (
	"testing"
	"time"
)

// Accepted and rejected expressions recorded against AWS
// (secrets-logs-events-alarms.md 4.2, AWS-REC).
func TestParseAcceptsRecordedExpressions(t *testing.T) {
	t.Parallel()
	for _, expr := range []string{
		"rate(1 minute)", "cron(0 10 * * ? *)", "cron(0 12 * * ? *)", "cron(15 12 * * ? *)", "cron(0 18 ? * MON-FRI *)",
		"cron(0 8 1 * ? *)", "cron(0/15 * * * ? *)", "cron(0/10 * ? * MON-FRI *)", "cron(0/5 8-17 ? * MON-FRI *)",
		"cron(0/30 20-23 ? * MON-FRI *)", "cron(0/30 0-2 ? * MON-FRI *)", "cron(5,35 14 * * ? *)",
		"cron(15 10 ? * 6L 2002-2005)", "cron(0 2 ? * SAT *)", "cron(0 2 ? * SAT#3 *)", "cron(* * ? * SAT#3 *)",
		"cron(0/5 5 ? JAN 1-5 2022)",
	} {
		if _, err := Parse(expr); err != nil {
			t.Errorf("Parse(%q) rejected: %v", expr, err)
		}
	}
}

// Rate forms the grammar allows that the recorded corpus does not contain:
// the documentation's examples and one more plural hour value.
func TestParseAcceptsDocumentedRates(t *testing.T) {
	t.Parallel()
	for _, expr := range []string{
		"rate(5 minutes)", "rate(1 hour)", "rate(12 hours)", "rate(1 day)", "rate(7 days)",
	} {
		if _, err := Parse(expr); err != nil {
			t.Errorf("Parse(%q) rejected: %v", expr, err)
		}
	}
}

// Cron fields split on runs of ASCII spaces and tabs (doc.go).
func TestParseAcceptsSpaceAndTabRunsBetweenCronFields(t *testing.T) {
	t.Parallel()
	for _, expr := range []string{"cron(0  0 * * ? *)", "cron(0\t0 * * ? *)", "cron(0 \t 0 * * ? *)"} {
		if _, err := Parse(expr); err != nil {
			t.Errorf("Parse(%q) rejected: %v", expr, err)
		}
	}
}

func TestParseRejectsRecordedExpressions(t *testing.T) {
	t.Parallel()
	for _, expr := range []string{
		"rate(10 seconds)", "rate(10 years)", "rate(1 minutes)", "rate(1 hours)", "rate(1 days)",
		"rate(10 minute)", "rate(10 hour)", "rate(10 day)", "rate()", "rate(10)", "rate(10 minutess)",
		"rate(foo minutes)", "rate(0 minutes)", "rate(-10 minutes)", "rate(10 MINUTES)", "rate( 10 minutes )",
		" rate(10 minutes)",
		"cron(0 1 * * * *)", "cron(0 dummy ? * MON-FRI *)", "cron(71 8 1 * ? *)", "cron(7 20 * * NOT *)",
		"cron(INVALID)",
	} {
		_, err := Parse(expr)
		if err == nil {
			t.Errorf("Parse(%q) accepted", expr)
			continue
		}
		if got := err.Message(); got != "Parameter ScheduleExpression is not valid." {
			t.Errorf("Parse(%q) message = %q", expr, got)
		}
	}
}

// Inputs the documentation rules out, beyond the recorded list.
func TestParseRejectsGrammarViolations(t *testing.T) {
	t.Parallel()
	for _, expr := range []string{
		"", "cron()", "cron(0 0 * * ?)", "cron(0 0 * * ? * *)", "cron(0 0 ? * ? *)", "cron(60 0 * * ? *)",
		"cron(0 24 * * ? *)", "cron(0 0 0 * ? *)", "cron(0 0 32 * ? *)", "cron(0 0 * 13 ? *)", "cron(0 0 ? * 8 *)",
		"cron(0 0 * * ? 1969)", "cron(0 0 * * ? 2200)", "cron(0 0 ? * 3#1,6#3 *)", "cron(0 0 ? * 3#6 *)",
		"cron(0 0 ? * 3#0 *)", "cron(0 0 L,1 * ? *)", "cron(0 0 32W * ? *)", "cron(0 0 * * L *)",
		"cron(0/0 0 * * ? *)", "cron(0- 0 * * ? *)", "cron(,0 0 * * ? *)", "cron(0 0 * * ? 2030-2020)",
		"cron( 0 0 * * ? *)", "cron(0 0 * * ? * )", "cron(0 0 * * ? *", "rate(5  minutes)", "rate(5 minutes) ",
		"rate(+5 minutes)", "cron(0 0 ? * MON-FOO *)", "cron(0 ? * * ? *)", "cron(0 0 * JAN#2 ? *)",
		"cron(0 0 ? * 2/2 *)", "cron(0 0 ? * */2 *)", "cron(0 0 ? * MON-FRI/2 *)",
		"cron(0\U000000a00 * * ? *)", "cron(0 0\U00000085* * ? *)", "cron(0 0 ? * SATL *)", "cron(0 0 ? * satl *)",
		"cron(0 0 ? * 6l *)",
	} {
		if _, err := Parse(expr); err == nil {
			t.Errorf("Parse(%q) accepted", expr)
		}
	}
}

func TestRateIntervalUsesTheUnit(t *testing.T) {
	t.Parallel()
	for expr, want := range map[string]time.Duration{
		"rate(1 minute)":   time.Minute,
		"rate(90 minutes)": 90 * time.Minute,
		"rate(1 hour)":     time.Hour,
		"rate(3 hours)":    3 * time.Hour,
		"rate(1 day)":      24 * time.Hour,
		"rate(2 days)":     48 * time.Hour,
	} {
		s, err := Parse(expr)
		if err != nil {
			t.Fatalf("Parse(%q): %v", expr, err)
		}
		r, ok := s.(Rate)
		if !ok {
			t.Fatalf("Parse(%q) = %T, want Rate", expr, s)
		}
		if r.Interval() != want {
			t.Errorf("%s interval = %v, want %v", expr, r.Interval(), want)
		}
	}
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad test instant %q: %v", s, err)
	}
	return v
}

func TestNextFiresAtTheDocumentedTimes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		expr, after, want string
	}{
		// A rate rule first fires one interval after its anchor.
		{"rate(5 minutes)", "2026-10-08T10:02:30Z", "2026-10-08T10:07:30Z"},
		{"rate(1 day)", "2026-10-08T10:00:00Z", "2026-10-09T10:00:00Z"},
		// Cron fires strictly after the given instant, on minute boundaries, in UTC.
		{"cron(0 10 * * ? *)", "2026-10-08T10:00:00Z", "2026-10-09T10:00:00Z"},
		{"cron(0 10 * * ? *)", "2026-10-08T09:59:30Z", "2026-10-08T10:00:00Z"},
		{"cron(0 10 * * ? *)", "2026-10-08T12:00:00+02:00", "2026-10-09T10:00:00Z"},
		{"cron(0/15 * * * ? *)", "2026-10-08T10:07:00Z", "2026-10-08T10:15:00Z"},
		{"cron(1/10 * * * ? *)", "2026-10-08T10:01:00Z", "2026-10-08T10:11:00Z"},
		{"cron(5,35 14 * * ? *)", "2026-10-08T14:05:00Z", "2026-10-08T14:35:00Z"},
		{"cron(0 18 ? * MON-FRI *)", "2026-10-09T18:00:00Z", "2026-10-12T18:00:00Z"},
		{"cron(0 2 ? * SAT#3 *)", "2026-10-08T00:00:00Z", "2026-10-17T02:00:00Z"},
		{"cron(0 0 ? * 3#2 *)", "2026-10-01T00:00:00Z", "2026-10-13T00:00:00Z"},
		{"cron(15 10 ? * 6L 2002-2005)", "2002-01-01T00:00:00Z", "2002-01-25T10:15:00Z"},
		{"cron(15 10 ? * 6L 2002-2005)", "1999-06-01T00:00:00Z", "2002-01-25T10:15:00Z"},
		{"cron(0 8 L * ? *)", "2026-02-01T00:00:00Z", "2026-02-28T08:00:00Z"},
		{"cron(0 8 LW * ? *)", "2026-02-01T00:00:00Z", "2026-02-27T08:00:00Z"},
		{"cron(0 8 15W * ? *)", "2026-11-01T00:00:00Z", "2026-11-16T08:00:00Z"},
		{"cron(0 8 1W * ? *)", "2026-08-01T00:00:00Z", "2026-08-03T08:00:00Z"},
		{"cron(0 8 ? * L *)", "2026-10-08T00:00:00Z", "2026-10-10T08:00:00Z"},
		{"cron(0/30 20-2 ? * MON-FRI *)", "2026-10-08T23:45:00Z", "2026-10-09T00:00:00Z"},
		{"cron(0/30 20-2 ? * MON-FRI *)", "2026-10-08T02:30:00Z", "2026-10-08T20:00:00Z"},
		{"cron(0 12 29 FEB ? *)", "2026-01-01T00:00:00Z", "2028-02-29T12:00:00Z"},
		{"cron(0 0 1 NOV-JAN ? *)", "2026-02-01T00:00:00Z", "2026-11-01T00:00:00Z"},
		{"cron(0 0 ? * sun *)", "2026-10-08T00:00:00Z", "2026-10-11T00:00:00Z"},
	} {
		s, err := Parse(c.expr)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.expr, err)
		}
		got, ok := Next(s, at(t, c.after))
		if !ok || !got.Equal(at(t, c.want)) || got.Location() != time.UTC {
			t.Errorf("Next(%s, %s) = %v, %v; want %s", c.expr, c.after, got, ok, c.want)
		}
	}
}

func TestNextReportsSchedulesThatNeverFire(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ expr, after string }{
		{"cron(0 0 30 FEB ? *)", "2026-01-01T00:00:00Z"},
		{"cron(0 0 1 1 ? 2199)", "2199-01-01T00:00:00Z"},
		{"cron(0 0 * * ? 2020)", "2026-01-01T00:00:00Z"},
	} {
		s, err := Parse(c.expr)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.expr, err)
		}
		if got, ok := Next(s, at(t, c.after)); ok {
			t.Errorf("Next(%s, %s) = %v, want no fire", c.expr, c.after, got)
		}
	}
}

// An instant long before the year field's range starts the search at 1970.
func TestNextFromTheFarPastFiresInTheFirstYear(t *testing.T) {
	t.Parallel()
	s, err := Parse("cron(0 0 1 1 ? *)")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	after := time.Date(-100000000, time.March, 1, 0, 0, 0, 0, time.UTC)
	want := time.Date(minYear, time.January, 1, 0, 0, 0, 0, time.UTC)
	if got, ok := Next(s, after); !ok || !got.Equal(want) {
		t.Errorf("Next(%v) = %v, %v; want %v", after, got, ok, want)
	}
}

// A rate value too large to parse is reported as too large, not as below 1.
func TestParseNamesAnOverflowingRateValue(t *testing.T) {
	t.Parallel()
	_, err := Parse("rate(99999999999999999999 minutes)")
	if err == nil {
		t.Fatal("Parse accepted an overflowing rate value")
	}
	if want := "rate value is too large"; err.Detail() != want {
		t.Errorf("detail = %q, want %q", err.Detail(), want)
	}
}

// The zero Rate has no interval, so it can never fire strictly later.
func TestNextNeverFiresTheZeroRate(t *testing.T) {
	t.Parallel()
	after := at(t, "2026-10-08T10:00:00Z")
	if got, ok := Next(Rate{}, after); ok {
		t.Errorf("Next(Rate{}, %v) = %v, want no fire", after, got)
	}
}
