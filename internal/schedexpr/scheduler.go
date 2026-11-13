package schedexpr

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

// RateUnits is which rate units EventBridge Scheduler accepts with which
// values: switch scheduler.rate.unit-agreement (scheduler.md 5.2, 14.12).
type RateUnits uint8

// RateUnits values; the zero value is AnyUnitAgreement.
const (
	// AnyUnitAgreement accepts each of minute, minutes, hour, hours, day
	// and days with any positive value.
	AnyUnitAgreement RateUnits = iota
	// SingularIffOne is the rules grammar: a singular unit exactly when the
	// value is 1.
	SingularIffOne
)

// String returns the switch value: "any" or "singular-iff-one".
func (u RateUnits) String() string {
	switch u {
	case AnyUnitAgreement:
		return "any"
	case SingularIffOne:
		return "singular-iff-one"
	}
	return "RateUnits(" + strconv.Itoa(int(u)) + ")"
}

// Expression is a parsed EventBridge Scheduler expression: At, Rate or
// Cron (scheduler.md 5).
//
//sumtype:decl
type Expression interface{ isExpression() }

// At is a one-time at(yyyy-mm-ddThh:mm:ss) expression: a civil time to the
// second, read in the schedule's time zone (scheduler.md 5.1).
type At struct {
	year                      int
	month                     time.Month
	day, hour, minute, second int
}

// atLayout and atMinuteLayout are the forms at() takes: no zone suffix, no
// fraction, every field zero-padded, the seconds optional (scheduler.md
// 5.1; G3 L271-L302; SCH-03 accepted at(2030-01-01T00:00), recorded
// 2026-11-03).
const (
	atLayout       = "2006-01-02T15:04:05"
	atMinuteLayout = "2006-01-02T15:04"
)

// In returns the instant the civil time names in loc. A civil time that
// loc skips (a spring-forward gap) is normalized as time.Date does;
// AWS's answer there is unrecorded (scheduler.md 5.1).
func (a At) In(loc *time.Location) time.Time {
	return time.Date(a.year, a.month, a.day, a.hour, a.minute, a.second, 0, loc)
}

func (At) isExpression()   {}
func (Rate) isExpression() {}
func (Cron) isExpression() {}

// parseAt reads yyyy-mm-ddThh:mm:ss or yyyy-mm-ddThh:mm. The length check
// refuses the fractional seconds time.Parse would accept after the
// seconds field.
func parseAt(inner string) (At, *Error) {
	layout := atLayout
	switch len(inner) {
	case len(atLayout):
	case len(atMinuteLayout):
		layout = atMinuteLayout
	default:
		return At{}, invalid("at() needs yyyy-mm-ddThh:mm:ss or yyyy-mm-ddThh:mm")
	}
	t, err := time.Parse(layout, inner)
	if err != nil {
		return At{}, invalid("at() needs a valid yyyy-mm-ddThh:mm:ss")
	}
	return At{year: t.Year(), month: t.Month(), day: t.Day(), hour: t.Hour(), minute: t.Minute(), second: t.Second()}, nil
}

// ParseScheduler parses an EventBridge Scheduler expression: at(...),
// rate(value unit) with units per units and the unit in any case
// (rate(1 MINUTE) was accepted, SCH-03, recorded 2026-11-03), or cron(...)
// with the rules' grammar except that runs of spaces and tabs separate its
// fields (scheduler.md 5.1-5.3). A refusal's Message is
// "Invalid Schedule Expression <expr>." with expr verbatim (scheduler.md
// 2.4).
func ParseScheduler(expr string, units RateUnits) (Expression, *Error) {
	var (
		ex  Expression
		err *Error
	)
	if inner, ok := enclosed(expr, "rate("); ok {
		ex, err = parseRateUnits(lowerUnit(inner), units)
	} else if inner, ok := enclosed(expr, "cron("); ok {
		ex, err = parseCron(inner, spaceAndTabRuns)
	} else if inner, ok := enclosed(expr, "at("); ok {
		ex, err = parseAt(inner)
	} else {
		err = invalid("expected at(...), rate(...) or cron(...)")
	}
	if err != nil {
		return nil, &Error{detail: err.detail, text: schedulerMessage(expr)}
	}
	return ex, nil
}

// lowerUnit is "value unit" with the unit lower-cased; anything without
// one space is returned as is for parseRateUnits to refuse.
func lowerUnit(inner string) string {
	num, unit, ok := strings.Cut(inner, " ")
	if !ok {
		return inner
	}
	return num + " " + strings.ToLower(unit)
}

// schedulerMessage is Scheduler's text for a malformed expression,
// third-party-recorded byte for byte, a leading space included
// (scheduler.md 2.4).
func schedulerMessage(expr string) string { return "Invalid Schedule Expression " + expr + "." }

// NextIn is the first fire of c strictly after after, evaluated in loc: the
// first instant whose civil minute in loc matches c, so a civil minute
// repeated by a fall-back transition fires once and one skipped by a spring
// forward never fires (scheduler.md 5.4, 14.3.3). NextIn(c, t, time.UTC)
// equals Next(c, t). ok is false when c never fires again.
//
// It walks c's matching civil minutes from the civil time of after; each
// fires at the first instant that shows it, unless that instant is not
// later than after (the minute was passed before a fall back repeated
// it) or no instant shows it (a spring-forward gap).
func NextIn(c Cron, after time.Time, loc *time.Location) (time.Time, bool) {
	l := after.In(loc)
	civil := time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), l.Nanosecond(), time.UTC)
	for {
		m, ok := c.next(civil)
		if !ok {
			return time.Time{}, false
		}
		if x, shown := firstShowing(m, loc); shown && x.After(after) {
			return x, true
		}
		civil = m
	}
}

// firstShowing is the earliest instant whose civil minute in loc is m (m's
// fields read as UTC), and false when loc skips m. The candidates are m
// less each offset loc uses within three hours of the instant time.Date
// picks for m, which covers every transition in the zone database.
func firstShowing(m time.Time, loc *time.Location) (time.Time, bool) {
	near := time.Date(m.Year(), m.Month(), m.Day(), m.Hour(), m.Minute(), 0, 0, loc)
	var shows []time.Time
	for _, probe := range []time.Duration{-3 * time.Hour, 0, 3 * time.Hour} {
		_, offset := near.Add(probe).Zone()
		x := m.Add(-time.Duration(offset) * time.Second)
		if l := x.In(loc); l.Year() == m.Year() && l.YearDay() == m.YearDay() && l.Hour() == m.Hour() && l.Minute() == m.Minute() {
			shows = append(shows, x)
		}
	}
	if len(shows) == 0 {
		return time.Time{}, false
	}
	return slices.MinFunc(shows, time.Time.Compare).UTC(), true
}
