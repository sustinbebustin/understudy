package cloudwatch

import (
	"context"
	"encoding/base64"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/store"
)

// defaultMaxDatapoints is GetMetricData's page size when MaxDatapoints is
// absent (secrets-logs-events-alarms.md 5.3).
const defaultMaxDatapoints = 100_800

// The ages of StartTime past which GetMetricData needs a period of whole
// minutes, and past which it rounds StartTime down to five minutes and to
// the hour instead of the minute and needs periods of those multiples.
const (
	minuteAge     = 3 * time.Hour
	fiveMinuteAge = 15 * 24 * time.Hour
	hourAge       = 63 * 24 * time.Hour
)

// stat is one of the statistics GetMetricData computes over a period.
type stat uint8

const (
	statSampleCount stat = iota + 1
	statAverage
	statSum
	statMinimum
	statMaximum
)

// stats lists the statistics in the order statNames names them.
func stats() []stat {
	return []stat{statSampleCount, statAverage, statSum, statMinimum, statMaximum}
}

func (s stat) String() string {
	switch s {
	case statSampleCount:
		return "SampleCount"
	case statAverage:
		return "Average"
	case statSum:
		return "Sum"
	case statMinimum:
		return "Minimum"
	case statMaximum:
		return "Maximum"
	}
	return "stat(" + strconv.Itoa(int(s)) + ")"
}

// statOf reads a MetricStat's Stat; ok is false for anything else AWS
// accepts (percentiles, trimmed and windowed statistics) or rejects.
func statOf(name string) (stat, bool) {
	for _, s := range stats() {
		if s.String() == name {
			return s, true
		}
	}
	return 0, false
}

// statNames are the names of the statistics understudy computes.
func statNames() []string {
	out := make([]string, 0, len(stats()))
	for _, s := range stats() {
		out = append(out, s.String())
	}
	return out
}

// of is the statistic's value over a period's combined set; ok is false for
// the Average of a set of no samples, which has none.
func (s stat) of(x Stats) (v float64, ok bool) {
	switch s {
	case statSampleCount:
		return x.SampleCount, true
	case statAverage:
		return x.Sum / x.SampleCount, x.SampleCount != 0
	case statSum:
		return x.Sum, true
	case statMinimum:
		return x.Minimum, true
	case statMaximum:
		return x.Maximum, true
	}
	return 0, false
}

// result is one query's answer before paging: its periods with data, in
// ascending time.
type result struct {
	id, label  string
	timestamps []time.Time
	values     []float64
	messages   []cloudwatchapi.MessageData
}

// getMetricData answers each MetricStat query with one value per period
// that holds data, newest first unless ScanBy says otherwise. Periods start
// at StartTime rounded down by its age on the emulator clock and the
// query's period, and a period too fine for that age reads no data;
// EndTime is exclusive. MaxDatapoints bounds the values of one page across
// all results, and NextToken continues where the page stopped.
func (s *Service) getMetricData(_ context.Context, c *kernel.Call, in *cloudwatchapi.GetMetricDataInput) (*cloudwatchapi.GetMetricDataOutput, *awserr.Error) {
	offset := 0
	if in.NextToken != nil {
		n, ok := parseOffset(*in.NextToken)
		if !ok {
			return nil, errInvalidNextToken()
		}
		offset = n
	}
	start, end := clock.FromWire(in.StartTime), clock.FromWire(in.EndTime)
	labels := defaultLabels(in.MetricDataQueries)
	answer := func(st *State) []result {
		var out []result
		for i := range in.MetricDataQueries {
			q := &in.MetricDataQueries[i]
			if q.ReturnData != nil && !*q.ReturnData {
				continue
			}
			r := read(st, q.MetricStat, start, c.Admitted(), end)
			r.id, r.label = q.Id, labels[i]
			if q.Label != nil {
				r.label = *q.Label
			}
			out = append(out, r)
		}
		return out
	}
	var results []result
	if cell, ok := s.cells.Lookup(c.Scope()); ok {
		results = store.Read(cell, answer)
	} else {
		results = answer(&State{})
	}
	if in.ScanBy == nil || *in.ScanBy == cloudwatchapi.ScanByTimestampDescending {
		for i := range results {
			slices.Reverse(results[i].timestamps)
			slices.Reverse(results[i].values)
		}
	}
	limit := defaultMaxDatapoints
	if in.MaxDatapoints != nil {
		limit = int(*in.MaxDatapoints)
	}
	return paged(results, offset, limit), nil
}

// read answers one MetricStat from StartTime at now: nothing when its
// period is too fine for StartTime's age, its periods from the rounded
// StartTime otherwise.
func read(st *State, ms *cloudwatchapi.MetricStat, start, now, end clock.Instant) result {
	if !periodServes(start, now, ms.Period) {
		return result{}
	}
	return query(st, ms, roundStart(start, now, ms.Period), end)
}

// query reads one MetricStat's periods from the series it names exactly:
// the same namespace, name and dimension set. Without a Unit, data of
// several units answers with the first unit by name and a MultipleUnits
// message listing them all, as AWS records.
func query(st *State, ms *cloudwatchapi.MetricStat, start, end clock.Instant) result {
	var r result
	m := ms.Metric
	if m.Namespace == nil || m.MetricName == nil {
		return r
	}
	series, ok := st.Series[seriesKey(*m.Namespace, *m.MetricName, sortedDimensions(m.Dimensions))]
	if !ok {
		return r
	}
	var points []Point
	for _, p := range series.Points {
		if p.At.Before(start) || !p.At.Before(end) || ms.Unit != nil && p.Unit != string(*ms.Unit) {
			continue
		}
		points = append(points, p)
	}
	if ms.Unit == nil {
		points, r.messages = oneUnit(points)
	}
	s, _ := statOf(ms.Stat)
	period := time.Duration(ms.Period) * time.Second
	buckets := map[int64]Stats{}
	for _, p := range points {
		i := int64(p.At.Sub(start) / period)
		if b, ok := buckets[i]; ok {
			buckets[i] = b.add(p.Stats)
		} else {
			buckets[i] = p.Stats
		}
	}
	for _, i := range slices.Sorted(maps.Keys(buckets)) {
		v, ok := s.of(buckets[i])
		if !ok {
			continue
		}
		r.timestamps = append(r.timestamps, start.Add(time.Duration(i)*period).Wire())
		r.values = append(r.values, v)
	}
	return r
}

// oneUnit keeps the points of the first unit by name when points carry
// several, with the MultipleUnits message AWS sends.
func oneUnit(points []Point) ([]Point, []cloudwatchapi.MessageData) {
	var units []string
	for _, p := range points {
		if !slices.Contains(units, p.Unit) {
			units = append(units, p.Unit)
		}
	}
	if len(units) < 2 {
		return points, nil
	}
	slices.Sort(units)
	kept := slices.DeleteFunc(points, func(p Point) bool { return p.Unit != units[0] })
	return kept, []cloudwatchapi.MessageData{{
		Code:  new("MultipleUnits"),
		Value: new("Multiple units returned: '[" + strings.Join(units, ", ") + "]'"),
	}}
}

// roundStart rounds StartTime down as AWS documents: to the minute when it
// is less than 15 days old, or to the query's period when that is under a
// minute, so 01:05:13 with a period of 10 starts at 01:05:10; to five
// minutes up to 63 days; to the hour beyond. Every sub-minute period
// divides the minute, so truncating to it lands on the minute's divisions.
func roundStart(start, now clock.Instant, period int32) clock.Instant {
	unit := time.Minute
	switch age := now.Sub(start); {
	case age >= hourAge:
		unit = time.Hour
	case age >= fiveMinuteAge:
		unit = 5 * time.Minute
	case time.Duration(period)*time.Second < time.Minute:
		unit = time.Duration(period) * time.Second
	}
	return clock.FromWire(start.Wire().Truncate(unit))
}

// periodServes reports whether a query's period reads data from a
// StartTime of its age: over three hours old the period must be a multiple
// of 60 seconds, from 15 days of 300 and from 63 days of 3600, or AWS
// returns no data points (the MetricStat documentation).
func periodServes(start, now clock.Instant, period int32) bool {
	var unit time.Duration
	switch age := now.Sub(start); {
	case age >= hourAge:
		unit = time.Hour
	case age >= fiveMinuteAge:
		unit = 5 * time.Minute
	case age > minuteAge:
		unit = time.Minute
	default:
		return true
	}
	return (time.Duration(period)*time.Second)%unit == 0
}

// defaultLabels are the labels of queries without one: the metric name,
// then the statistic when the request's queries use more than one, then
// the period when they use more than one (AWS-recorded label generation).
func defaultLabels(qs []cloudwatchapi.MetricDataQuery) []string {
	stats, periods := map[string]bool{}, map[int32]bool{}
	for i := range qs {
		if ms := qs[i].MetricStat; ms != nil {
			stats[ms.Stat], periods[ms.Period] = true, true
		}
	}
	out := make([]string, len(qs))
	for i := range qs {
		ms := qs[i].MetricStat
		if ms == nil || ms.Metric.MetricName == nil {
			continue
		}
		parts := []string{*ms.Metric.MetricName}
		if len(stats) > 1 {
			parts = append(parts, ms.Stat)
		}
		if len(periods) > 1 {
			parts = append(parts, strconv.Itoa(int(ms.Period)))
		}
		out[i] = strings.Join(parts, " ")
	}
	return out
}

// paged cuts one page of at most limit values across the results in order,
// skipping the offset values earlier pages returned. A result cut short is
// PartialData; the page then carries the token of the next offset.
func paged(results []result, offset, limit int) *cloudwatchapi.GetMetricDataOutput {
	out := &cloudwatchapi.GetMetricDataOutput{
		MetricDataResults: make([]cloudwatchapi.MetricDataResult, len(results)),
		Messages:          []cloudwatchapi.MessageData{},
	}
	skip, room, more := offset, limit, false
	for i, r := range results {
		from := min(skip, len(r.values))
		skip -= from
		to := from + min(room, len(r.values)-from)
		room -= to - from
		status := cloudwatchapi.StatusCodeComplete
		if to < len(r.values) {
			status, more = cloudwatchapi.StatusCodePartialData, true
		}
		// Both lists are always sent, empty when the page holds no value
		// of the result (AWS-recorded).
		out.MetricDataResults[i] = cloudwatchapi.MetricDataResult{
			Id:         new(r.id),
			Label:      new(r.label),
			Timestamps: append([]time.Time{}, r.timestamps[from:to]...),
			Values:     append([]float64{}, r.values[from:to]...),
			StatusCode: new(status),
			Messages:   r.messages,
		}
	}
	if more {
		out.NextToken = new(offsetToken(offset + limit))
	}
	return out
}

// offsetToken and parseOffset are GetMetricData's NextToken: the number of
// values earlier pages returned.
func offsetToken(n int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(n)))
}

func parseOffset(token string) (int, bool) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(string(b))
	return n, err == nil && n >= 0
}
