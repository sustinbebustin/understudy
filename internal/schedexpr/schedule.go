package schedexpr

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// message is AWS's one ValidationException text for every invalid rate and
// cron expression (recorded).
const message = "Parameter ScheduleExpression is not valid."

// Error is an expression AWS would reject, which the events service returns
// as a ValidationException. Message is the wire text; Detail names the
// offending part for logs and never reaches the wire.
type Error struct {
	detail string
}

// Message returns the ValidationException text.
func (*Error) Message() string { return message }

// Detail names the offending part of the expression, for logs only.
func (e *Error) Detail() string { return e.detail }

// Error returns the wire message.
func (*Error) Error() string { return message }

func invalid(detail string) *Error { return &Error{detail: detail} }

// Schedule is a parsed rate or cron expression.
//
//sumtype:decl
type Schedule interface{ isSchedule() }

// Rate fires every Interval, counted from an anchor such as the rule's
// creation time.
type Rate struct{ every time.Duration }

// Interval is the time between two fires.
func (r Rate) Interval() time.Duration { return r.every }

func (Rate) isSchedule() {}

// Parse parses rate(value unit) or cron(minutes hours day-of-month month
// day-of-week year). Whitespace is not trimmed: AWS rejects it around the
// expression and inside the parentheses.
func Parse(expr string) (Schedule, *Error) {
	if inner, ok := enclosed(expr, "rate("); ok {
		return parseRate(inner)
	}
	if inner, ok := enclosed(expr, "cron("); ok {
		return parseCron(inner)
	}
	return nil, invalid("expected rate(...) or cron(...)")
}

func enclosed(expr, open string) (string, bool) {
	rest, ok := strings.CutPrefix(expr, open)
	if !ok {
		return "", false
	}
	return strings.CutSuffix(rest, ")")
}

// parseRate reads "value unit": value is a positive decimal integer, unit is
// singular exactly when value is 1, and units are case-sensitive.
func parseRate(inner string) (Rate, *Error) {
	num, unit, ok := strings.Cut(inner, " ")
	if !ok || num == "" || strings.Trim(num, "0123456789") != "" {
		return Rate{}, invalid("rate needs a positive integer and a unit separated by one space")
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return Rate{}, invalid("rate value is too large")
	}
	if n < 1 {
		return Rate{}, invalid("rate value must be at least 1")
	}
	singular := n == 1
	var per time.Duration
	switch {
	case singular && unit == "minute", !singular && unit == "minutes":
		per = time.Minute
	case singular && unit == "hour", !singular && unit == "hours":
		per = time.Hour
	case singular && unit == "day", !singular && unit == "days":
		per = 24 * time.Hour
	default:
		return Rate{}, invalid("rate unit must be minute, hour or day, plural unless the value is 1")
	}
	if n > math.MaxInt64/int64(per) {
		return Rate{}, invalid("rate interval is too long")
	}
	return Rate{every: time.Duration(n) * per}, nil
}

// Next returns the first fire strictly after after, in UTC. For a Rate,
// after is the anchor or the previous fire. ok is false when the schedule
// never fires again, which includes the zero Rate: only Parse builds a Rate
// with a positive interval.
func Next(s Schedule, after time.Time) (time.Time, bool) {
	after = after.UTC()
	switch s := s.(type) {
	case Rate:
		if s.every <= 0 {
			return time.Time{}, false
		}
		return after.Add(s.every), true
	case Cron:
		return s.next(after)
	}
	return time.Time{}, false
}
