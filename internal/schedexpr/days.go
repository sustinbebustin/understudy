package schedexpr

import (
	"strconv"
	"strings"
	"time"
)

// daySpec is the day rule of a cron expression: one of the day-of-month
// forms or one of the day-of-week forms.
//
//sumtype:decl
type daySpec interface {
	matches(y int, m time.Month, d int) bool
}

// monthDays is a day-of-month list, range or *.
type monthDays struct{ set bitset }

// lastDayOfMonth is L in day of month.
type lastDayOfMonth struct{}

// lastWeekdayOfMonth is LW: the last Monday to Friday of the month.
type lastWeekdayOfMonth struct{}

// nearestWeekday is nW: the Monday to Friday nearest day n without leaving
// the month.
type nearestWeekday struct{ day int }

// weekdays is a day-of-week list, range or *; bit 1 is Sunday.
type weekdays struct{ set bitset }

// lastWeekdayInMonth is nL: the last weekday n of the month.
type lastWeekdayInMonth struct{ weekday int }

// nthWeekday is w#n: the nth weekday w of the month.
type nthWeekday struct{ weekday, n int }

func (s monthDays) matches(_ int, _ time.Month, d int) bool { return s.set.has(d) }

func (lastDayOfMonth) matches(y int, m time.Month, d int) bool { return d == daysIn(y, m) }

func (lastWeekdayOfMonth) matches(y int, m time.Month, d int) bool {
	last := daysIn(y, m)
	switch weekday(y, m, last) {
	case time.Saturday:
		last--
	case time.Sunday:
		last -= 2
	case time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday:
	}
	return d == last
}

// matches uses the Quartz rule: a Saturday moves back to Friday unless that
// leaves the month, a Sunday moves forward to Monday unless that leaves the
// month. A day past the month's end never fires.
func (s nearestWeekday) matches(y int, m time.Month, d int) bool {
	last := daysIn(y, m)
	if s.day > last {
		return false
	}
	target := s.day
	switch weekday(y, m, s.day) {
	case time.Saturday:
		target = s.day - 1
		if target < 1 {
			target = s.day + 2
		}
	case time.Sunday:
		target = s.day + 1
		if target > last {
			target = s.day - 2
		}
	case time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday:
	}
	return d == target
}

func (s weekdays) matches(y int, m time.Month, d int) bool {
	return s.set.has(int(weekday(y, m, d)) + 1)
}

func (s lastWeekdayInMonth) matches(y int, m time.Month, d int) bool {
	return int(weekday(y, m, d))+1 == s.weekday && d+7 > daysIn(y, m)
}

func (s nthWeekday) matches(y int, m time.Month, d int) bool {
	return int(weekday(y, m, d))+1 == s.weekday && (d-1)/7+1 == s.n
}

func daysIn(y int, m time.Month) int { return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day() }

func weekday(y int, m time.Month, d int) time.Weekday {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Weekday()
}

// parseDays enforces that exactly one of the two day fields is ?.
func parseDays(dom, dow string) (daySpec, *Error) {
	switch {
	case dom == "?" && dow == "?":
		return nil, invalid("one of day-of-month and day-of-week must be specified")
	case dom == "?":
		return parseDayOfWeek(dow)
	case dow == "?":
		return parseDayOfMonth(dom)
	}
	return nil, invalid("day-of-month and day-of-week cannot both be specified; one must be ?")
}

// parseDayOfMonth reads L, LW and nW, which stand alone, or a list.
func parseDayOfMonth(s string) (daySpec, *Error) {
	switch s {
	case "L":
		return lastDayOfMonth{}, nil
	case "LW":
		return lastWeekdayOfMonth{}, nil
	}
	f := domField()
	if n, ok := strings.CutSuffix(s, "W"); ok {
		day, err := f.value(n)
		if err != nil {
			return nil, err
		}
		return nearestWeekday{day: day}, nil
	}
	set, err := f.set(s, 0)
	if err != nil {
		return nil, err
	}
	return monthDays{set: set}, nil
}

// parseDayOfWeek reads L (Saturday, the last day of the week), nL and w#n,
// which stand alone, or a list. The documented special characters of this
// field are , - * ? L #: unlike every other field it takes no / increment.
func parseDayOfWeek(s string) (daySpec, *Error) {
	if strings.ContainsRune(s, '/') {
		return nil, invalid("day-of-week: the / increment is not allowed in this field")
	}
	f := dowField()
	if s == "L" {
		return weekdays{set: bitset{}.with(int(time.Saturday) + 1)}, nil
	}
	if w, ok := strings.CutSuffix(s, "L"); ok {
		if strings.Trim(w, "0123456789") != "" {
			return nil, invalid("day-of-week: L takes a single day number from 1 to 7")
		}
		day, err := f.value(w)
		if err != nil {
			return nil, err
		}
		return lastWeekdayInMonth{weekday: day}, nil
	}
	if w, nth, ok := strings.Cut(s, "#"); ok {
		day, err := f.value(w)
		if err != nil {
			return nil, err
		}
		n, convErr := strconv.Atoi(nth)
		if convErr != nil || n < 1 || n > 5 || len(nth) != 1 {
			return nil, invalid("day-of-week: the # occurrence must be 1 to 5")
		}
		return nthWeekday{weekday: day, n: n}, nil
	}
	set, err := f.set(s, 0)
	if err != nil {
		return nil, err
	}
	return weekdays{set: set}, nil
}
