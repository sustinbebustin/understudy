package alarm

import "time"

// Next is the first evaluation boundary strictly after after: the next
// whole minute for a period of 60 or more, the next 10-second division of
// the minute for a period of 10, 20 or 30, and the next whole hour when
// N x Period exceeds one day (cloudwatch.alarm.evaluation-cadence,
// cloudwatch.alarm.evaluation-phase, cloudwatch.alarm.first-evaluation;
// cloudwatch-alarms.md 3.1).
func (Config) Next(_ time.Time) time.Time {
	// not implemented yet
	return time.Time{}
}

// Anchor is T0 of an evaluation at at: at truncated to the minute, to the
// 10-second division for a sub-minute period, or to the hour when
// N x Period exceeds one day (cloudwatch.alarm.window-anchor;
// cloudwatch-alarms.md 3.2).
func (Config) Anchor(_ time.Time) time.Time {
	// not implemented yet
	return time.Time{}
}

// Window is the evaluation range of an evaluation at at:
// [T0 - Range x Period, T0) (cloudwatch-alarms.md 3.2, 12.3).
func (Config) Window(_ time.Time) (start, end time.Time) {
	// not implemented yet
	return time.Time{}, time.Time{}
}
