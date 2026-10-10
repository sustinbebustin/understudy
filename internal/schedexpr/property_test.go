package schedexpr

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// cronCase is a generated cron expression with an oracle written
// independently of the parser: fires reports whether a UTC minute satisfies
// every field.
type cronCase struct {
	expr  string
	fires func(time.Time) bool
}

// cronField is the value range and syntax of one generated cyclic field.
type cronField struct {
	label  string
	lo, hi int
	// name is the field's three-letter name of a value, nil when it has none.
	name func(int) string
	// steps reports whether the field takes the / increment; day of week
	// does not.
	steps bool
}

// draw generates the field as one item or, one time in four, a list of two
// or three items, and returns its text and the values it allows: the union
// of the items' values.
func (f cronField) draw(t *rapid.T) (text string, values []int) {
	if rapid.IntRange(0, 3).Draw(t, f.label+"-shape") != 0 {
		return f.drawItem(t)
	}
	parts := make([]string, rapid.IntRange(2, 3).Draw(t, f.label+"-items"))
	for i := range parts {
		var vs []int
		parts[i], vs = f.drawItem(t)
		values = append(values, vs...)
	}
	return strings.Join(parts, ","), values
}

// drawItem generates one list item: *, a value, a range, a value with an
// increment, a range with an increment or * with an increment. A range may
// wrap past hi back to lo.
func (f cronField) drawItem(t *rapid.T) (text string, values []int) {
	val := func(v int) string {
		if f.name != nil && rapid.Bool().Draw(t, f.label+"-named") {
			return f.name(v)
		}
		return strconv.Itoa(v)
	}
	value := func(suffix string) int { return rapid.IntRange(f.lo, f.hi).Draw(t, f.label+suffix) }
	step := func() int { return rapid.IntRange(1, f.hi-f.lo+1).Draw(t, f.label+"-step") }
	kinds := 2
	if f.steps {
		kinds = 5
	}
	switch rapid.IntRange(0, kinds).Draw(t, f.label+"-kind") {
	case 0:
		return "*", span(f.lo, f.hi, 1)
	case 1:
		v := value("")
		return val(v), []int{v}
	case 2:
		a, b := value("-from"), value("-to")
		return val(a) + "-" + val(b), f.cycle(a, b)
	case 3:
		a, n := value("-from"), step()
		return strconv.Itoa(a) + "/" + strconv.Itoa(n), span(a, f.hi, n)
	case 4:
		a, b, n := value("-from"), value("-to"), step()
		return val(a) + "-" + val(b) + "/" + strconv.Itoa(n), every(f.cycle(a, b), n)
	}
	n := step()
	return "*/" + strconv.Itoa(n), span(f.lo, f.hi, n)
}

// cycle lists a to b in order, running past hi back to lo when b < a.
func (f cronField) cycle(a, b int) []int {
	if a <= b {
		return span(a, b, 1)
	}
	return append(span(a, f.hi, 1), span(f.lo, b, 1)...)
}

// drawYear generates the year field as *, a range, a value with an
// increment or a range with an increment. Years do not wrap, and the start
// stays inside the instants instantGen draws.
func drawYear(t *rapid.T) (text string, values []int) {
	kind := rapid.IntRange(0, 3).Draw(t, "year-kind")
	if kind == 0 {
		return "*", span(minYear, maxYear, 1)
	}
	a := rapid.IntRange(2000, 2100).Draw(t, "year-from")
	from := strconv.Itoa(a)
	switch kind {
	case 1:
		b := rapid.IntRange(a, a+5).Draw(t, "year-to")
		return from + "-" + strconv.Itoa(b), span(a, b, 1)
	case 2:
		n := rapid.IntRange(1, 10).Draw(t, "year-step")
		return from + "/" + strconv.Itoa(n), span(a, maxYear, n)
	}
	b := rapid.IntRange(a, a+20).Draw(t, "year-to")
	n := rapid.IntRange(1, 5).Draw(t, "year-step")
	return from + "-" + strconv.Itoa(b) + "/" + strconv.Itoa(n), span(a, b, n)
}

func span(from, to, step int) []int {
	var out []int
	for v := from; v <= to; v += step {
		out = append(out, v)
	}
	return out
}

// every keeps the first value and each nth one after it.
func every(vs []int, n int) []int {
	var out []int
	for i := 0; i < len(vs); i += n {
		out = append(out, vs[i])
	}
	return out
}

func monthName(m int) string { return strings.ToUpper(time.Month(m).String()[:3]) }

func dayName(d int) string { return strings.ToUpper(time.Weekday(d - 1).String()[:3]) }

func lastOfMonth(t time.Time) bool { return t.AddDate(0, 0, 1).Month() != t.Month() }

func isWeekday(t time.Time) bool { return t.Weekday() != time.Saturday && t.Weekday() != time.Sunday }

func midnight(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// nearestWeekdayOracle is the weekday of t's month closest to day n.
func nearestWeekdayOracle(t time.Time, n int) bool {
	if n > daysIn(t.Year(), t.Month()) {
		return false
	}
	first := midnight(t).AddDate(0, 0, 1-t.Day())
	best, bestDist := 0, 99
	for d := first; d.Month() == t.Month(); d = d.AddDate(0, 0, 1) {
		if dist := max(d.Day()-n, n-d.Day()); isWeekday(d) && dist < bestDist {
			best, bestDist = d.Day(), dist
		}
	}
	return t.Day() == best
}

// dayField draws the day-of-month and day-of-week pair, exactly one of which
// is ?, and its oracle.
func dayField(t *rapid.T) (dom, dow string, fires func(time.Time) bool) {
	switch rapid.IntRange(0, 6).Draw(t, "day-kind") {
	case 0:
		text, days := cronField{label: "dom", lo: 1, hi: 31, steps: true}.draw(t)
		return text, "?", func(x time.Time) bool { return slices.Contains(days, x.Day()) }
	case 1:
		return "L", "?", lastOfMonth
	case 2:
		return "LW", "?", func(x time.Time) bool {
			if !isWeekday(x) {
				return false
			}
			for d := midnight(x).AddDate(0, 0, 1); d.Month() == x.Month(); d = d.AddDate(0, 0, 1) {
				if isWeekday(d) {
					return false
				}
			}
			return true
		}
	case 3:
		n := rapid.IntRange(1, 31).Draw(t, "nearest")
		return strconv.Itoa(n) + "W", "?", func(x time.Time) bool { return nearestWeekdayOracle(x, n) }
	case 4:
		text, days := cronField{label: "dow", lo: 1, hi: 7, name: dayName}.draw(t)
		return "?", text, func(x time.Time) bool { return slices.Contains(days, int(x.Weekday())+1) }
	case 5:
		w := rapid.IntRange(1, 7).Draw(t, "last-weekday")
		return "?", strconv.Itoa(w) + "L", func(x time.Time) bool {
			return int(x.Weekday())+1 == w && x.AddDate(0, 0, 7).Month() != x.Month()
		}
	}
	w := rapid.IntRange(1, 7).Draw(t, "nth-weekday")
	n := rapid.IntRange(1, 5).Draw(t, "nth")
	return "?", dayName(w) + "#" + strconv.Itoa(n), func(x time.Time) bool {
		return int(x.Weekday())+1 == w && (x.Day()+6)/7 == n
	}
}

func cronCaseGen() *rapid.Generator[cronCase] {
	return rapid.Custom(func(t *rapid.T) cronCase {
		minText, minutes := cronField{label: "minute", lo: 0, hi: 59, steps: true}.draw(t)
		hourText, hours := cronField{label: "hour", lo: 0, hi: 23, steps: true}.draw(t)
		monthText, months := cronField{label: "month", lo: 1, hi: 12, name: monthName, steps: true}.draw(t)
		dom, dow, dayFires := dayField(t)
		yearText, years := drawYear(t)
		return cronCase{
			expr: "cron(" + strings.Join([]string{minText, hourText, dom, monthText, dow, yearText}, " ") + ")",
			fires: func(x time.Time) bool {
				return slices.Contains(minutes, x.Minute()) && slices.Contains(hours, x.Hour()) &&
					slices.Contains(months, int(x.Month())) && slices.Contains(years, x.Year()) && dayFires(x)
			},
		}
	})
}

func instantGen() *rapid.Generator[time.Time] {
	return rapid.Custom(func(t *rapid.T) time.Time {
		start := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
		end := time.Date(2100, 12, 31, 0, 0, 0, 0, time.UTC).Unix()
		return time.Unix(rapid.Int64Range(start, end).Draw(t, "after"), 0).UTC()
	})
}

// bruteWindow bounds the minute-by-minute search that checks Next skipped
// no matching minute.
const bruteWindow = 2 * 24 * time.Hour

func firstFireByBruteForce(c cronCase, after, limit time.Time) (time.Time, bool) {
	for x := after.Truncate(time.Minute).Add(time.Minute); !x.After(limit); x = x.Add(time.Minute) {
		if c.fires(x) {
			return x, true
		}
	}
	return time.Time{}, false
}

func TestPropertyCronFiresSatisfyEveryField(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		c := cronCaseGen().Draw(t, "cron")
		s, err := Parse(c.expr)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.expr, err)
		}
		prev := instantGen().Draw(t, "after")
		for range 4 {
			next, ok := Next(s, prev)
			want, found := firstFireByBruteForce(c, prev, prev.Add(bruteWindow))
			switch {
			case !ok && found:
				t.Fatalf("%s after %v: no fire, but %v matches", c.expr, prev, want)
			case !ok:
				return
			case !next.After(prev) || next.Second() != 0 || next.Nanosecond() != 0:
				t.Fatalf("%s after %v: %v is not a later whole minute", c.expr, prev, next)
			case !c.fires(next):
				t.Fatalf("%s after %v: %v violates a field", c.expr, prev, next)
			case found && !next.Equal(want):
				t.Fatalf("%s after %v: %v skipped %v", c.expr, prev, next, want)
			}
			prev = next
		}
	})
}

func TestPropertyRateFiresEveryIntervalFromTheAnchor(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 10000).Draw(t, "minutes")
		unit := "minutes"
		if n == 1 {
			unit = "minute"
		}
		s, err := Parse("rate(" + strconv.Itoa(n) + " " + unit + ")")
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		anchor := instantGen().Draw(t, "anchor")
		fire := anchor
		for k := 1; k <= 20; k++ {
			next, ok := Next(s, fire)
			if !ok {
				t.Fatalf("rate stopped firing after %v", fire)
			}
			if want := anchor.Add(time.Duration(k*n) * time.Minute); !next.Equal(want) {
				t.Fatalf("fire %d = %v, want %v", k, next, want)
			}
			fire = next
		}
	})
}

func TestPropertyNextIsStrictlyIncreasing(t *testing.T) {
	t.Parallel()
	exprGen := rapid.OneOf(
		rapid.Map(cronCaseGen(), func(c cronCase) string { return c.expr }),
		rapid.Custom(func(t *rapid.T) string {
			n := rapid.IntRange(2, 500).Draw(t, "rate")
			unit := rapid.SampledFrom([]string{"minutes", "hours", "days"}).Draw(t, "unit")
			return "rate(" + strconv.Itoa(n) + " " + unit + ")"
		}),
	)
	rapid.Check(t, func(t *rapid.T) {
		expr := exprGen.Draw(t, "expr")
		s, err := Parse(expr)
		if err != nil {
			t.Fatalf("Parse(%q): %v", expr, err)
		}
		prev := instantGen().Draw(t, "after")
		for range 10 {
			next, ok := Next(s, prev)
			if !ok {
				return
			}
			if !next.After(prev) {
				t.Fatalf("%s: Next(%v) = %v is not later", expr, prev, next)
			}
			prev = next
		}
	})
}

func TestPropertyParseNeverPanics(t *testing.T) {
	t.Parallel()
	exprGen := rapid.OneOf(
		rapid.String(),
		rapid.Map(rapid.StringMatching(`[0-9A-Za-z*?/,#LW -]{0,30}`), func(s string) string { return "cron(" + s + ")" }),
		rapid.Map(rapid.StringMatching(`[0-9a-z+ -]{0,16}`), func(s string) string { return "rate(" + s + ")" }),
	)
	rapid.Check(t, func(t *rapid.T) {
		expr := exprGen.Draw(t, "expr")
		s, err := Parse(expr)
		if err != nil {
			if err.Message() != message {
				t.Fatalf("Parse(%q) message = %q", expr, err.Message())
			}
			return
		}
		Next(s, instantGen().Draw(t, "after"))
	})
}
