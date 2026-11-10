package cloudwatch_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
)

// Periods are anchored at StartTime rounded down to the minute, one
// datapoint per period with data, ascending
// (cloudwatch.get-metric-statistics.order; cloudwatch-alarms.md 9.1, 9.2):
// data at t0 and t0 + 2m asked from t0 + 30s by the minute gives t0 and
// t0 + 2m, and nothing for the empty minute between.
func TestGetMetricStatisticsPeriods(t *testing.T) {
	run(t, func(h *harness) {
		followsSwitch(h, "cloudwatch.get-metric-statistics.order", "timestamp-ascending,then-unit")
		t0 := h.now()
		h.putRaw("periods", datum{v: 1}, datum{v: 3})
		h.advance(2 * time.Minute)
		h.putRaw("periods", datum{v: 5})
		h.advance(time.Minute)
		out := h.stats(&cloudwatchapi.GetMetricStatisticsInput{
			Namespace: "ns", MetricName: "periods", StartTime: t0.Add(30 * time.Second), EndTime: t0.Add(3 * time.Minute), Period: 60,
			Statistics: []cloudwatchapi.Statistic{cloudwatchapi.StatisticSum, cloudwatchapi.StatisticSampleCount},
		})
		none := new(cloudwatchapi.StandardUnitNone)
		want := []cloudwatchapi.Datapoint{
			{Timestamp: new(t0), Sum: new(4.0), SampleCount: new(2.0), Unit: none},
			{Timestamp: new(t0.Add(2 * time.Minute)), Sum: new(5.0), SampleCount: new(1.0), Unit: none},
		}
		if !reflect.DeepEqual(out.Datapoints, want) {
			h.Errorf("Datapoints = %+v\nwant %+v", out.Datapoints, want)
		}
	})
}

// Past three hours a period must be a multiple of 60 or no data points are
// returned (cloudwatch-alarms.md 9.1), as GetMetricData answers.
func TestGetMetricStatisticsOldStartNeedsMinutes(t *testing.T) {
	run(t, func(h *harness) {
		t0 := h.now()
		h.putRaw("m", datum{v: 1})
		h.advance(4 * time.Hour)
		in := &cloudwatchapi.GetMetricStatisticsInput{
			Namespace: "ns", MetricName: "m", StartTime: t0, EndTime: t0.Add(time.Minute), Period: 30,
			Statistics: []cloudwatchapi.Statistic{cloudwatchapi.StatisticSum},
		}
		if out := h.stats(in); len(out.Datapoints) != 0 {
			h.Errorf("period 30 four hours back = %+v, want none", out.Datapoints)
		}
		in.Period = 60
		if out := h.stats(in); len(out.Datapoints) != 1 {
			h.Errorf("period 60 four hours back = %+v, want one", out.Datapoints)
		}
	})
}

// An ExtendedStatistics name that is no statistic is a client error, not
// the 501 a valid percentile gets (cloudwatch-alarms.md 9.1: percentiles
// are p0.0 to p100).
func TestGetMetricStatisticsInvalidExtendedStatistic(t *testing.T) {
	run(t, func(h *harness) {
		now := h.now()
		_, err := call(h, cloudwatchapi.OpGetMetricStatistics, &cloudwatchapi.GetMetricStatisticsInput{
			Namespace: "ns", MetricName: "m", StartTime: now, EndTime: now.Add(time.Minute), Period: 60,
			ExtendedStatistics: []string{"p101"},
		})
		if err == nil || err.ID.Status != 400 {
			h.Errorf("ExtendedStatistics p101 = %v, want a 400", err)
		}
	})
}

// A period whose combined SampleCount is zero carries no Average, while the
// other statistics asked for stay (cwa 9.2).
func TestGetMetricStatisticsNoAverageOfNoSamples(t *testing.T) {
	run(t, func(h *harness) {
		t0 := h.now()
		must(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{Namespace: "ns", MetricData: []cloudwatchapi.MetricDatum{{
			MetricName: "zero", StatisticValues: &cloudwatchapi.StatisticSet{SampleCount: 0, Sum: 0, Minimum: 0, Maximum: 0},
		}}})
		out := h.stats(&cloudwatchapi.GetMetricStatisticsInput{
			Namespace: "ns", MetricName: "zero", StartTime: t0, EndTime: t0.Add(time.Minute), Period: 60,
			Statistics: []cloudwatchapi.Statistic{cloudwatchapi.StatisticAverage, cloudwatchapi.StatisticSampleCount},
		})
		want := []cloudwatchapi.Datapoint{{Timestamp: new(t0), SampleCount: new(0.0), Unit: new(cloudwatchapi.StandardUnitNone)}}
		if !reflect.DeepEqual(out.Datapoints, want) {
			h.Errorf("Datapoints = %+v\nwant %+v", out.Datapoints, want)
		}
	})
}

// A scope with no alarms answers an empty MetricAlarms list (cwa 9.4).
func TestDescribeAlarmsForMetricNoAlarms(t *testing.T) {
	run(t, func(h *harness) {
		out := must(h, cloudwatchapi.OpDescribeAlarmsForMetric, &cloudwatchapi.DescribeAlarmsForMetricInput{Namespace: "ns", MetricName: "Load"})
		if out.MetricAlarms == nil || len(out.MetricAlarms) != 0 {
			h.Errorf("MetricAlarms = %#v, want an empty list", out.MetricAlarms)
		}
	})
}
