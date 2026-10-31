package schedexpr

import (
	"testing"
	"time"
)

// at() takes exactly yyyy-mm-ddThh:mm:ss: no fraction, no zone suffix, no
// unpadded field, a real calendar date. Only the zone suffix and the
// date-only form are recorded (G3 L271-L302); the rest is UNVERIFIED
// (SCH-03), scheduler.md 5.1: understudy refuses every other form with
// Scheduler's text.
func TestParseSchedulerAtForms(t *testing.T) {
	t.Parallel()
	for _, expr := range []string{
		"at(2022-12-31T23:59:59.5)", "at(2022-12-31T23:59)", "at(2022-1-31T23:59:59)",
		"at(2022-02-30T00:00:00)", "at(2022-12-31 23:59:59)", "at(2022-12-31T24:00:00)",
		"at( 2022-12-31T23:59:59)", "at(2022-12-31T23:59:59+01:00)", "AT(2022-12-31T23:59:59)",
	} {
		_, err := ParseScheduler(expr, AnyUnitAgreement)
		if want := "Invalid Schedule Expression " + expr + "."; err == nil || err.Message() != want {
			t.Errorf("ParseScheduler(%q) = %v, want %q", expr, err, want)
		}
	}
	ex, err := ParseScheduler("at(2024-02-29T00:00:00)", AnyUnitAgreement)
	if err != nil {
		t.Fatalf("ParseScheduler(leap day) = %v", err)
	}
	at, ok := ex.(At)
	if !ok {
		t.Fatalf("ParseScheduler(leap day) = %T, want At", ex)
	}
	if got := at.In(time.UTC); !got.Equal(time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("At.In(UTC) = %v", got)
	}
}

// Scheduler's refusal text replaces the rules' one for every rate and cron
// refusal too, and the rules' Parse never takes at() (scheduler.md 2.4,
// 14.3.1).
func TestParseSchedulerTextOnly(t *testing.T) {
	t.Parallel()
	for _, expr := range []string{"cron(0 8 * * * *)", "rate(0 minutes)", "every(1 hour)", ""} {
		_, err := ParseScheduler(expr, SingularIffOne)
		if want := "Invalid Schedule Expression " + expr + "."; err == nil || err.Message() != want {
			t.Errorf("ParseScheduler(%q) = %v, want %q", expr, err, want)
		}
	}
}
