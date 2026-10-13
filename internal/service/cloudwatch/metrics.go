package cloudwatch

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// PutMetricData's timestamp windows, which are also the span past which
// ListMetrics stops listing a metric, and ListMetrics' page size and
// RecentlyActive span (secrets-logs-events-alarms.md 5.2; the ListMetrics
// documentation).
const (
	pastWindow     = 14 * 24 * time.Hour
	futureWindow   = 2 * time.Hour
	listMetricsMax = 500
	recentlyActive = 3 * time.Hour
)

// bindMetrics binds the metric data operations.
func (s *Service) bindMetrics(b cloudwatchapi.Binder) {
	b.PutMetricData(s.putMetricData,
		registry.Partial{Note: "entities are not kept; with StrictEntityValidation false an entity error stores the data without the X-Amzn-Failure-Message header; points are kept until reset, not aged out by resolution"},
		kernel.WithCheck(checkPutMetricData))
	b.GetMetricData(s.getMetricData,
		registry.Partial{Note: "MetricStat queries with SampleCount, Average, Sum, Minimum or Maximum; metric math, Metrics Insights and percentiles answer 501; LabelOptions is ignored"},
		kernel.WithCheck(checkGetMetricData))
	b.ListMetrics(s.listMetrics,
		registry.Partial{Note: "no linked accounts: IncludeLinkedAccounts and OwningAccount select nothing more"})
}

// putMetricData stores each datum as one point of its series. A request
// whose datum falls outside the two-week and two-hour windows of both the
// wall clock and the emulator clock stores nothing.
func (s *Service) putMetricData(_ context.Context, c *kernel.Call, in *cloudwatchapi.PutMetricDataInput) (*cloudwatchapi.PutMetricDataOutput, *awserr.Error) {
	ds := datums(in)
	wall := clock.FromWire(s.k.Clock().Wall())
	_, err := kernel.Write(c, s.cells, c.Scope(), func(st *State, now clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		points := make([]Point, len(ds))
		for i, d := range ds {
			at := now
			if d.d.Timestamp != nil {
				at = clock.FromWire(*d.d.Timestamp)
				if err := checkWindow(at, now, wall, d.path); err != nil {
					return struct{}{}, err
				}
			}
			unit := cloudwatchapi.StandardUnitNone
			if d.d.Unit != nil {
				unit = *d.d.Unit
			}
			points[i] = Point{At: storedAt(at, d.d.StorageResolution), Unit: string(unit), Stats: statsOf(d.d)}
		}
		for i, d := range ds {
			dims := sortedDimensions(d.d.Dimensions)
			key := seriesKey(in.Namespace, d.d.MetricName, dims)
			series, ok := st.Series[key]
			if !ok {
				series = Series{Namespace: in.Namespace, Name: d.d.MetricName, Dimensions: dims}
			}
			// Appending never writes an element an earlier slice header
			// of the series can read, so a value read before stays intact.
			series.Points = append(series.Points, points[i])
			series.LastPut = now
			st.Series[key] = series
		}
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &cloudwatchapi.PutMetricDataOutput{}, nil
}

// checkWindow accepts a timestamp no more than two weeks in the past and
// two hours in the future by either clock (inferred texts). Each clock's
// window is checked on its own, so a timestamp between the windows of
// diverged clocks is valid by neither; a rejection names the emulator
// clock's verdict.
func checkWindow(at, now, wall clock.Instant, path string) *awserr.Error {
	inWindow := func(ref clock.Instant) bool {
		return !at.Before(ref.Add(-pastWindow)) && !ref.Add(futureWindow).Before(at)
	}
	switch {
	case inWindow(now) || inWindow(wall):
		return nil
	case now.Add(futureWindow).Before(at):
		return cloudwatchapi.ErrInvalidParameterValueException("The parameter " + path + ".Timestamp must specify a time no more than two hours in the future.")
	}
	return cloudwatchapi.ErrInvalidParameterValueException("The parameter " + path + ".Timestamp must specify a time no more than two weeks in the past.")
}

// storedAt is a datum's timestamp at the resolution CloudWatch stores it
// at: the second for a high-resolution metric (StorageResolution 1), the
// minute for a regular one (60, the default), so a sub-minute period sees
// a regular metric's data only in the period that starts its minute (the
// MetricDatum documentation).
func storedAt(at clock.Instant, storageResolution *int32) clock.Instant {
	unit := time.Minute
	if storageResolution != nil && *storageResolution == 1 {
		unit = time.Second
	}
	return clock.FromWire(at.Wire().Truncate(unit))
}

// statsOf folds a datum's value form into one statistic set; Counts
// default to 1 per value.
func statsOf(d *cloudwatchapi.MetricDatum) Stats {
	switch {
	case d.Value != nil:
		v := *d.Value
		return Stats{SampleCount: 1, Sum: v, Minimum: v, Maximum: v}
	case d.StatisticValues != nil:
		s := d.StatisticValues
		return Stats{SampleCount: s.SampleCount, Sum: s.Sum, Minimum: s.Minimum, Maximum: s.Maximum}
	}
	out := Stats{Minimum: d.Values[0], Maximum: d.Values[0]}
	for i, v := range d.Values {
		count := 1.0
		if len(d.Counts) > 0 {
			count = d.Counts[i]
		}
		out = out.add(Stats{SampleCount: count, Sum: v * count, Minimum: v, Maximum: v})
	}
	return out
}

// listMetrics lists the scope's metrics by namespace, name and dimensions,
// 500 to a page.
func (s *Service) listMetrics(_ context.Context, c *kernel.Call, in *cloudwatchapi.ListMetricsInput) (*cloudwatchapi.ListMetricsOutput, *awserr.Error) {
	type listed struct {
		key    string
		metric cloudwatchapi.Metric
	}
	var all []listed
	if cell, ok := s.cells.Lookup(c.Scope()); ok {
		now := c.Admitted()
		all = store.Read(cell, func(st *State) []listed {
			var out []listed
			for _, key := range slices.Sorted(maps.Keys(st.Series)) {
				series := st.Series[key]
				if !lists(in, &series, now) {
					continue
				}
				out = append(out, listed{key: key, metric: cloudwatchapi.Metric{
					Namespace: new(series.Namespace), MetricName: new(series.Name), Dimensions: wireDimensions(series.Dimensions),
				}})
			}
			return out
		})
	}
	page, next, ok := pageOf(all, func(l listed) string { return l.key }, in.NextToken, listMetricsMax)
	if !ok {
		return nil, errInvalidListToken()
	}
	out := &cloudwatchapi.ListMetricsOutput{Metrics: make([]cloudwatchapi.Metric, len(page)), NextToken: next}
	for i, l := range page {
		out.Metrics[i] = l.metric
	}
	return out, nil
}

// lists reports whether ListMetrics selects a series at now: one that
// reported data within the past two weeks, as AWS documents, and that its
// filters select, every dimension filter naming one of its dimensions with
// the given value when there is one.
func lists(in *cloudwatchapi.ListMetricsInput, s *Series, now clock.Instant) bool {
	switch {
	case s.LastPut.Before(now.Add(-pastWindow)):
		return false
	case in.Namespace != nil && *in.Namespace != s.Namespace:
		return false
	case in.MetricName != nil && *in.MetricName != s.Name:
		return false
	case in.RecentlyActive != nil && s.LastPut.Before(now.Add(-recentlyActive)):
		return false
	}
	for _, f := range in.Dimensions {
		if !slices.ContainsFunc(s.Dimensions, func(d Dimension) bool { return d.Name == f.Name && (f.Value == nil || *f.Value == d.Value) }) {
			return false
		}
	}
	return true
}
