package cloudwatch

import (
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch/alarm"
)

// alarmBins are the bins d1..dR, newest first, of an evaluation at of
// the alarm cfg was read from (alarm.ConfigOf): the window cfg.Window(at)
// over the series with exactly the alarm's namespace, name and dimension
// set (cloudwatch-alarms.md 3.2, 3.3). The points are of the alarm's Unit
// when it has one, else of the first unit by name
// (cloudwatch.alarm.mixed-units), bucketed as GetMetricData buckets them
// from the window's start (cloudwatch-alarms.md 12.3); a bin with no point,
// or whose statistic has no value, is Missing. No vended AWS/* metric is
// invented: a namespace nothing was put to reads only Missing bins
// (cloudwatch-alarms.md 12.7).
func alarmBins(st *State, in *cloudwatchapi.PutMetricAlarmInput, cfg alarm.Config, at clock.Instant) []alarm.Bin {
	start, end := cfg.Window(at.Wire())
	period := time.Duration(cfg.Period) * time.Second
	bins := make([]alarm.Bin, cfg.Range())
	for k := range bins {
		bins[k] = alarm.Missing{Start: end.Add(-time.Duration(k+1) * period)}
	}
	if in.Namespace == nil || in.MetricName == nil {
		return bins
	}
	from := clock.FromWire(start)
	points := seriesPoints(st, *in.Namespace, *in.MetricName, in.Dimensions, cfg.Unit, from, clock.FromWire(end))
	if cfg.Unit == nil {
		points, _ = oneUnit(points)
	}
	s, _ := statOf(string(cfg.Statistic))
	for _, b := range bucketed(points, from, period) {
		v, ok := s.of(b.stats)
		if !ok {
			continue
		}
		k := len(bins) - 1 - int(b.start.Sub(from)/period)
		bins[k] = alarm.Value{Start: b.start.Wire(), Value: v, SampleCount: b.stats.SampleCount}
	}
	return bins
}
