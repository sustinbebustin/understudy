package schedexpr

import (
	"strconv"
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
type At struct{}

// In returns the instant the civil time names in loc. Not implemented yet:
// it returns the zero time.
func (At) In(*time.Location) time.Time { return time.Time{} }

func (At) isExpression()   {}
func (Rate) isExpression() {}
func (Cron) isExpression() {}

// ParseScheduler parses an EventBridge Scheduler expression: at(...),
// rate(value unit) with units per units, or cron(...) with the rules'
// grammar (scheduler.md 5.1-5.3). A refusal's Message is "Invalid Schedule
// Expression <expr>." with expr verbatim (scheduler.md 2.4). Not
// implemented yet: at() and cron() are refused.
func ParseScheduler(expr string, units RateUnits) (Expression, *Error) {
	if inner, ok := enclosed(expr, "rate("); ok {
		r, err := parseRateUnits(inner, units)
		if err != nil {
			return nil, &Error{detail: err.detail, text: schedulerMessage(expr)}
		}
		return r, nil
	}
	return nil, &Error{detail: "at() and cron() are not implemented yet", text: schedulerMessage(expr)}
}

// schedulerMessage is Scheduler's text for a malformed expression,
// third-party-recorded byte for byte, a leading space included
// (scheduler.md 2.4).
func schedulerMessage(expr string) string { return "Invalid Schedule Expression " + expr + "." }

// NextIn is the first fire of c strictly after after, evaluated in loc: the
// first instant whose civil minute in loc matches c, so a civil minute
// repeated by a fall-back transition fires once and one skipped by a spring
// forward never fires (scheduler.md 5.4, 14.3.3). NextIn(c, t, time.UTC)
// equals Next(c, t). ok is false when c never fires again. Not implemented
// yet: it reports no fire.
func NextIn(Cron, time.Time, *time.Location) (time.Time, bool) { return time.Time{}, false }
