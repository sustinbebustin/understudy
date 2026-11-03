package cloudwatch

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/store"
)

// maxStatisticsDatapoints is the most datapoints one GetMetricStatistics
// call may ask for (cloudwatch-alarms.md 9.3).
const maxStatisticsDatapoints = 1440

// GetMetricStatistics' refusals of the statistics and the period, as AWS
// answered them (cloudwatch-alarms.md 9.3; CWA-19, recorded 2026-11-03).
const (
	textNoStatistics     = "At least one of the parameters Statistics and ExtendedStatistics must be specified."
	textSubMinutePeriod  = "The parameter Period must be a value in the set [1, 5, 10, 20, 30]."
	textMinutePeriodStep = "The parameter Period must be a multiple of 60."
)

// checkGetMetricStatistics checks, in order, that Statistics or
// ExtendedStatistics is sent, the period, the time range, the dimensions
// and the 1440-datapoint limit, counted over the request before any data is
// read (cloudwatch-alarms.md 9.1, 9.3). AWS accepts both statistics lists
// together (CWA-19). A valid ExtendedStatistics, alone or with Statistics,
// answers 501: understudy keeps statistic sets, not samples, and cannot
// compute percentiles (cloudwatch-alarms.md 12.7).
func checkGetMetricStatistics(in *cloudwatchapi.GetMetricStatisticsInput) *awserr.Error {
	switch {
	case len(in.Statistics) == 0 && len(in.ExtendedStatistics) == 0:
		return cloudwatchapi.ErrInvalidParameterCombinationException(textNoStatistics)
	case !validQueryPeriod(in.Period) && in.Period < 60:
		return cloudwatchapi.ErrInvalidParameterValueException(textSubMinutePeriod)
	case !validQueryPeriod(in.Period):
		return cloudwatchapi.ErrInvalidParameterValueException(textMinutePeriodStep)
	case !in.StartTime.Before(in.EndTime):
		return cloudwatchapi.ErrInvalidParameterValueException("The parameter StartTime must be less than the parameter EndTime.") // recorded
	}
	if err := checkDimensions("Dimensions", in.Dimensions); err != nil {
		return err
	}
	if n := statisticsDatapoints(in.StartTime, in.EndTime, in.Period); n > maxStatisticsDatapoints {
		return cloudwatchapi.ErrInvalidParameterCombinationException("You have requested up to " + strconv.FormatInt(n, 10) +
			" datapoints, which exceeds the limit of 1440. You may reduce the datapoints requested by increasing Period, or decreasing the time range.") // recorded
	}
	for i, e := range in.ExtendedStatistics {
		if !validExtendedStat(e) {
			return errInvalidValue(e, "ExtendedStatistics.member."+strconv.Itoa(i+1))
		}
	}
	if len(in.ExtendedStatistics) > 0 {
		return errExtendedStatisticsNotImplemented()
	}
	return nil
}

// statisticsDatapoints is the number of periods a request spans: the
// floor of (EndTime - StartTime) / Period over the StartTime as sent, not
// as rounded (cloudwatch.get-metric-statistics.datapoint-limit, CWA-20).
// The caller has checked StartTime < EndTime and a positive Period.
func statisticsDatapoints(start, end time.Time, period int32) int64 {
	return int64(end.Sub(start) / (time.Duration(period) * time.Second))
}

// getMetricStatistics answers one datapoint per period with data, and per
// unit within the period when Unit is not sent (cloudwatch-alarms.md 9.2),
// with the statistics asked for. Periods start at StartTime rounded by its
// age, a period too fine for that age reads nothing, and EndTime is
// exclusive, as GetMetricData reads (cloudwatch-alarms.md 9.1).
func (s *Service) getMetricStatistics(_ context.Context, c *kernel.Call, in *cloudwatchapi.GetMetricStatisticsInput) (*cloudwatchapi.GetMetricStatisticsOutput, *awserr.Error) {
	out := &cloudwatchapi.GetMetricStatisticsOutput{Label: new(in.MetricName), Datapoints: []cloudwatchapi.Datapoint{}}
	start, end, now := clock.FromWire(in.StartTime), clock.FromWire(in.EndTime), c.Admitted()
	if !periodServes(start, now, in.Period) {
		return out, nil
	}
	if cell, ok := s.cells.Lookup(c.Scope()); ok {
		start = roundStart(start, now, in.Period)
		out.Datapoints = append(out.Datapoints, store.Read(cell, func(st *State) []cloudwatchapi.Datapoint {
			return statistics(st, in, start, end)
		})...)
	}
	return out, nil
}

// statistics reads the datapoints of the series the input names from start
// to end: the points of each unit bucketed on their own, never narrowed to
// one unit as GetMetricData narrows them (cloudwatch-alarms.md 9.2, 12.3),
// sorted by timestamp, then unit (cloudwatch.get-metric-statistics.order).
func statistics(st *State, in *cloudwatchapi.GetMetricStatisticsInput, start, end clock.Instant) []cloudwatchapi.Datapoint {
	byUnit := map[string][]Point{}
	for _, p := range seriesPoints(st, in.Namespace, in.MetricName, in.Dimensions, in.Unit, start, end) {
		byUnit[p.Unit] = append(byUnit[p.Unit], p)
	}
	var out []cloudwatchapi.Datapoint
	for _, unit := range slices.Sorted(maps.Keys(byUnit)) {
		for _, b := range bucketed(byUnit[unit], start, time.Duration(in.Period)*time.Second) {
			out = append(out, datapoint(b, unit, in.Statistics))
		}
	}
	// Units were added in name order, so a stable sort by time keeps them
	// in that order within a period.
	slices.SortStableFunc(out, func(a, b cloudwatchapi.Datapoint) int { return a.Timestamp.Compare(*b.Timestamp) })
	return out
}

// datapoint is one period's datapoint of one unit: its start, its unit and
// the statistics asked for, without an Average when the set holds no
// samples (cloudwatch-alarms.md 9.2).
func datapoint(b bucket, unit string, want []cloudwatchapi.Statistic) cloudwatchapi.Datapoint {
	d := cloudwatchapi.Datapoint{Timestamp: new(b.start.Wire()), Unit: new(cloudwatchapi.StandardUnit(unit))}
	for _, w := range want {
		s, _ := statOf(string(w))
		v, ok := s.of(b.stats)
		if !ok {
			continue
		}
		switch s {
		case statSampleCount:
			d.SampleCount = new(v)
		case statAverage:
			d.Average = new(v)
		case statSum:
			d.Sum = new(v)
		case statMinimum:
			d.Minimum = new(v)
		case statMaximum:
			d.Maximum = new(v)
		}
	}
	return d
}

// statisticsSwitches are GetMetricStatistics' rows of cloudwatch-alarms.md
// 12.2: how the datapoint count rounds, and the order of the datapoints.
func statisticsSwitches() []fidelity.Entry {
	ops := []string{"cloudwatch.GetMetricStatistics"}
	return []fidelity.Entry{
		fidelity.Switch[string]{
			Name: "cloudwatch.get-metric-statistics.datapoint-limit", Value: "floor-unrounded",
			Label: evidence.AWSRecorded, Ref: alarmResearch + "#93-errors-and-limits", Record: "CWA-20", Ops: ops,
			Settles: fidelity.NoScenario{Reason: "CWA-20 recorded; cloudwatch/CWA-19 replays its steps, which match on status and message but differ on the JSON body's __type and member order, so none can settle it yet"},
		}.Entry(),
		fidelity.Switch[string]{
			Name: "cloudwatch.get-metric-statistics.order", Value: "timestamp-ascending,then-unit",
			Label: evidence.Inferred, Ref: alarmResearch + "#92-response", Ops: ops,
			Settles: fidelity.NoScenario{Reason: "AWS documents no order (\"Data points are not returned in chronological order\"), so no recording can pin one"},
		}.Entry(),
	}
}
