package alarm

import "time"

// Next is the first evaluation boundary strictly after after: the next
// whole minute for a period of 60 or more, the next 10-second division of
// the minute for a period of 10, 20 or 30, and the next whole hour when
// N x Period exceeds one day (cloudwatch.alarm.evaluation-cadence,
// cloudwatch.alarm.evaluation-phase, cloudwatch.alarm.first-evaluation;
// cloudwatch-alarms.md 3.1).
func (c Config) Next(after time.Time) time.Time {
	step := c.cadence()
	return after.UTC().Truncate(step).Add(step)
}

// Anchor is T0 of an evaluation at at: at truncated to the minute, to the
// 10-second division for a sub-minute period, or to the hour when
// N x Period exceeds one day (cloudwatch.alarm.window-anchor;
// cloudwatch-alarms.md 3.2).
func (c Config) Anchor(at time.Time) time.Time {
	return at.UTC().Truncate(c.cadence())
}

// Window is the evaluation range of an evaluation at at:
// [T0 - Range x Period, T0) (cloudwatch-alarms.md 3.2, 12.3).
func (c Config) Window(at time.Time) (start, end time.Time) {
	end = c.Anchor(at)
	return end.Add(-time.Duration(c.Range()) * c.period()), end
}

// cadence is the interval between evaluations, which is also the
// granularity of T0 (cloudwatch-alarms.md 3.1).
func (c Config) cadence() time.Duration {
	switch {
	case time.Duration(c.N)*c.period() > 24*time.Hour:
		return time.Hour
	case c.Period == 10 || c.Period == 20 || c.Period == 30:
		return 10 * time.Second
	}
	return time.Minute
}

func (c Config) period() time.Duration {
	return time.Duration(c.Period) * time.Second
}
