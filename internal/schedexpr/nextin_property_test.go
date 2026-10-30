package schedexpr

import (
	"testing"
	"time"

	"pgregory.net/rapid"
)

// zoneNames are the zones the NextIn properties run in: UTC, two with a
// one-hour DST shift on different dates, the census's legacy link name
// (scheduler.md 5.4, 12), and Lord Howe's 30-minute shift.
var zoneNames = []string{"UTC", "America/Los_Angeles", "Europe/Paris", "US/Eastern", "Australia/Lord_Howe"}

func loadZone(t *rapid.T, name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

// civil is the civil time of x in loc, as a UTC time with the same fields,
// so the UTC field oracle of cronCase can judge it.
func civil(x time.Time, loc *time.Location) time.Time {
	l := x.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), 0, 0, time.UTC)
}

// firstPass reports whether x is the first instant with its civil minute
// in loc: no instant up to two hours earlier shows the same civil minute.
// A fall-back transition repeats a civil minute 30 minutes to 2 hours
// later (Lord Howe shifts 30 minutes).
func firstPass(x time.Time, loc *time.Location) bool {
	c := civil(x, loc)
	for _, d := range []time.Duration{30 * time.Minute, time.Hour, 2 * time.Hour} {
		if civil(x.Add(-d), loc).Equal(c) {
			return false
		}
	}
	return true
}

// zoneFireByBruteForce walks the UTC minutes past after, up to limit, and returns
// the first whose civil minute in loc satisfies c and is the first pass of
// that civil minute (scheduler.md 14.3.3).
func zoneFireByBruteForce(c cronCase, after, limit time.Time, loc *time.Location) (time.Time, bool) {
	for x := after.Truncate(time.Minute).Add(time.Minute); !x.After(limit); x = x.Add(time.Minute) {
		if c.fires(civil(x, loc)) && firstPass(x, loc) {
			return x, true
		}
	}
	return time.Time{}, false
}

// nearTransitionGen draws an instant up to two days before the zone's next
// offset change after a random instant, so DST transitions are exercised;
// a zone without changes (UTC) gets the random instant itself.
func nearTransitionGen(loc *time.Location) *rapid.Generator[time.Time] {
	return rapid.Custom(func(t *rapid.T) time.Time {
		base := time.Unix(rapid.Int64Range(
			time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
			time.Date(2037, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
		).Draw(t, "base"), 0)
		_, end := base.In(loc).ZoneBounds()
		if end.IsZero() {
			return base.UTC()
		}
		back := time.Duration(rapid.Int64Range(0, int64(2*24*time.Hour/time.Second)).Draw(t, "before-transition")) * time.Second
		return end.Add(-back).UTC()
	})
}

// In UTC, NextIn is the rules' Next for every cron and instant
// (scheduler.md 14.3.3; contract section 2).
func TestPropertyNextInUTCEqualsNext(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		c := cronCaseGen().Draw(t, "cron")
		ex, err := ParseScheduler(c.expr, AnyUnitAgreement)
		if err != nil {
			t.Fatalf("ParseScheduler(%q): %v", c.expr, err)
		}
		cr, ok := ex.(Cron)
		if !ok {
			t.Fatalf("ParseScheduler(%q) = %T, want Cron", c.expr, ex)
		}
		after := instantGen().Draw(t, "after")
		got, gotOK := NextIn(cr, after, time.UTC)
		want, wantOK := Next(cr, after)
		if gotOK != wantOK || !got.Equal(want) {
			t.Fatalf("%s after %v: NextIn(UTC) = %v %v, Next = %v %v", c.expr, after, got, gotOK, want, wantOK)
		}
	})
}

// In any zone, NextIn returns the first instant later than after whose civil
// minute matches and has not been passed before: a later whole minute, no
// civil minute fired twice across a fall back, and no matching civil
// minute lost outside a spring-forward gap (aws-doc
// https://docs.aws.amazon.com/scheduler/latest/UserGuide/schedule-types.html;
// scheduler.md 5.4, 14.3.3).
func TestPropertyNextInFiresEachCivilMinuteOnce(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		loc := loadZone(t, rapid.SampledFrom(zoneNames).Draw(t, "zone"))
		c := cronCaseGen().Draw(t, "cron")
		ex, err := ParseScheduler(c.expr, AnyUnitAgreement)
		if err != nil {
			t.Fatalf("ParseScheduler(%q): %v", c.expr, err)
		}
		cr, ok := ex.(Cron)
		if !ok {
			t.Fatalf("ParseScheduler(%q) = %T, want Cron", c.expr, ex)
		}
		prev := nearTransitionGen(loc).Draw(t, "after")
		seen := map[time.Time]time.Time{}
		for range 4 {
			next, ok := NextIn(cr, prev, loc)
			want, found := zoneFireByBruteForce(c, prev, prev.Add(bruteWindow), loc)
			switch {
			case !ok && found:
				t.Fatalf("%s in %s after %v: no fire, but %v matches", c.expr, loc, prev, want)
			case !ok:
				return
			case !next.After(prev) || next.Second() != 0 || next.Nanosecond() != 0:
				t.Fatalf("%s in %s after %v: %v is not a later whole minute", c.expr, loc, prev, next)
			case !c.fires(civil(next, loc)):
				t.Fatalf("%s in %s after %v: %v (civil %v) violates a field", c.expr, loc, prev, next, civil(next, loc))
			case !firstPass(next, loc):
				t.Fatalf("%s in %s after %v: %v repeats civil minute %v", c.expr, loc, prev, next, civil(next, loc))
			case found && !next.Equal(want):
				t.Fatalf("%s in %s after %v: %v, want %v", c.expr, loc, prev, next, want)
			}
			if earlier, dup := seen[civil(next, loc)]; dup {
				t.Fatalf("%s in %s: civil minute %v fired at %v and %v", c.expr, loc, civil(next, loc), earlier, next)
			}
			seen[civil(next, loc)] = next
			prev = next
		}
	})
}
