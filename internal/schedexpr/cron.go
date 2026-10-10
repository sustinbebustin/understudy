package schedexpr

import (
	"strconv"
	"strings"
	"time"
)

// Year bounds of the cron year field.
const (
	minYear = 1970
	maxYear = 2199
)

// Cron fires at every UTC minute that satisfies all six fields. Day of month
// and day of week are one field here because exactly one of them may be
// specified.
type Cron struct {
	minutes, hours, months, years bitset
	days                          daySpec
}

func (Cron) isSchedule() {}

// bitset holds small non-negative integers; years are stored as year-1970.
type bitset [4]uint64

func (b bitset) with(i int) bitset {
	b[i/64] |= 1 << (i % 64)
	return b
}

func (b bitset) has(i int) bool { return i >= 0 && i < 256 && b[i/64]&(1<<(i%64)) != 0 }

// field describes the values one cron field takes.
type field struct {
	name     string
	min, max int
	// names lists the three-letter names of min, min+1, ... concatenated.
	names string
	// wraps allows a range whose start is after its end, such as 20-2.
	wraps bool
}

func minuteField() field { return field{name: "minutes", min: 0, max: 59, wraps: true} }
func hourField() field   { return field{name: "hours", min: 0, max: 23, wraps: true} }
func domField() field    { return field{name: "day-of-month", min: 1, max: 31, wraps: true} }
func yearField() field   { return field{name: "year", min: minYear, max: maxYear} }

func monthField() field {
	return field{name: "month", min: 1, max: 12, names: "JANFEBMARAPRMAYJUNJULAUGSEPOCTNOVDEC", wraps: true}
}

func dowField() field {
	return field{name: "day-of-week", min: 1, max: 7, names: "SUNMONTUEWEDTHUFRISAT", wraps: true}
}

func parseCron(inner string) (Cron, *Error) {
	if strings.TrimSpace(inner) != inner {
		return Cron{}, invalid("cron fields must not be padded inside the parentheses")
	}
	f := strings.FieldsFunc(inner, func(r rune) bool { return r == ' ' || r == '\t' })
	if len(f) != 6 {
		return Cron{}, invalid("cron needs exactly six fields")
	}
	var (
		c   Cron
		err *Error
	)
	if c.minutes, err = minuteField().set(f[0], 0); err != nil {
		return Cron{}, err
	}
	if c.hours, err = hourField().set(f[1], 0); err != nil {
		return Cron{}, err
	}
	if c.months, err = monthField().set(f[3], 0); err != nil {
		return Cron{}, err
	}
	if c.years, err = yearField().set(f[5], minYear); err != nil {
		return Cron{}, err
	}
	if c.days, err = parseDays(f[2], f[4]); err != nil {
		return Cron{}, err
	}
	return c, nil
}

// set parses a list of *, */step, v, v/step, a-b and a-b/step items. Each
// value v is stored as v-offset.
func (f field) set(s string, offset int) (bitset, *Error) {
	var b bitset
	for item := range strings.SplitSeq(s, ",") {
		base, stepText, stepped := strings.Cut(item, "/")
		step := 1
		if stepped {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 || strings.Trim(stepText, "0123456789") != "" {
				return bitset{}, invalid(f.name + ": increment must be a positive integer")
			}
			step = n
		}
		lo, hi, err := f.bounds(base, stepped)
		if err != nil {
			return bitset{}, err
		}
		span := f.max - f.min + 1
		length := hi - lo
		if length < 0 {
			if !f.wraps {
				return bitset{}, invalid(f.name + ": range start is after its end")
			}
			length += span
		}
		for k := 0; k <= length; k += step {
			b = b.with(f.min + (lo-f.min+k)%span - offset)
		}
	}
	return b, nil
}

// bounds reads *, v or a-b. A lone value with an increment runs to the end
// of the field, so 0/15 is 0, 15, 30, 45.
func (f field) bounds(base string, stepped bool) (lo, hi int, err *Error) {
	if base == "*" {
		return f.min, f.max, nil
	}
	if a, b, isRange := strings.Cut(base, "-"); isRange {
		if lo, err = f.value(a); err != nil {
			return 0, 0, err
		}
		hi, err = f.value(b)
		return lo, hi, err
	}
	if lo, err = f.value(base); err != nil {
		return 0, 0, err
	}
	if stepped {
		return lo, f.max, nil
	}
	return lo, lo, nil
}

// value reads a number in range or, for month and day of week, a
// three-letter name in any case.
func (f field) value(s string) (int, *Error) {
	if len(s) == 3 && f.names != "" {
		if i := strings.Index(f.names, strings.ToUpper(s)); i >= 0 && i%3 == 0 {
			return f.min + i/3, nil
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || strings.Trim(s, "0123456789") != "" || n < f.min || n > f.max {
		return 0, invalid(f.name + ": " + strconv.Quote(s) + " is not a value from " +
			strconv.Itoa(f.min) + " to " + strconv.Itoa(f.max))
	}
	return n, nil
}

// next walks forward field by field, jumping to the start of the next year,
// month, day, hour or minute whenever a field rejects the candidate. A start
// before minYear moves to minYear, so the walk is bounded by the year range.
func (c Cron) next(after time.Time) (time.Time, bool) {
	t := after.Truncate(time.Minute).Add(time.Minute)
	if t.Year() < minYear {
		t = time.Date(minYear, time.January, 1, 0, 0, 0, 0, time.UTC)
	}
	for t.Year() <= maxYear {
		y, m, d := t.Date()
		switch {
		case !c.years.has(y - minYear):
			t = time.Date(y+1, time.January, 1, 0, 0, 0, 0, time.UTC)
		case !c.months.has(int(m)):
			t = time.Date(y, m+1, 1, 0, 0, 0, 0, time.UTC)
		case !c.days.matches(y, m, d):
			t = time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC)
		case !c.hours.has(t.Hour()):
			t = t.Truncate(time.Hour).Add(time.Hour)
		case !c.minutes.has(t.Minute()):
			t = t.Add(time.Minute)
		default:
			return t, true
		}
	}
	return time.Time{}, false
}
