package cloudwatch_test

import (
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
)

// quietAlarm is an evaluable alarm whose evaluations, with no data under
// ignore, keep whatever state SetAlarmState gives it (cwa 2.3, 3.5).
func quietAlarm(name string) *cloudwatchapi.PutMetricAlarmInput {
	in := evalAlarm(name)
	in.TreatMissingData = new("ignore")
	return in
}

func (h *harness) setState(name string, v cloudwatchapi.StateValue) {
	h.Helper()
	must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: name, StateValue: v, StateReason: "set to " + string(v)})
}

// DescribeAlarmHistory returns StateUpdate items of metric alarms, newest
// first by default (cwa 8.1: the API reference and AWS's CLI example),
// filtered by alarm name, type, item type and date, and nothing for item
// types and contributors understudy never records (cwa 12.1, contract 3.5).
func TestDescribeAlarmHistoryFilters(t *testing.T) {
	run(t, func(h *harness) {
		must(h, cloudwatchapi.OpPutMetricAlarm, quietAlarm("a"))
		must(h, cloudwatchapi.OpPutMetricAlarm, quietAlarm("b"))
		t0 := h.now()
		h.setState("a", cloudwatchapi.StateValueAlarm)
		h.advance(time.Minute)
		h.setState("b", cloudwatchapi.StateValueAlarm)
		h.advance(time.Minute)
		h.setState("a", cloudwatchapi.StateValueOk)

		all := h.history(&cloudwatchapi.DescribeAlarmHistoryInput{})
		want := []string{
			"a: Alarm updated from ALARM to OK",
			"b: Alarm updated from INSUFFICIENT_DATA to ALARM",
			"a: Alarm updated from INSUFFICIENT_DATA to ALARM",
		}
		if got := summaries(all); !slices.Equal(got, want) {
			h.Fatalf("history = %v\nwant %v", got, want)
		}
		for i, it := range all {
			if *it.AlarmType != cloudwatchapi.AlarmTypeMetricAlarm || *it.HistoryItemType != cloudwatchapi.HistoryItemTypeStateUpdate ||
				!it.Timestamp.Equal(t0.Add(time.Duration(2-i)*time.Minute)) || it.HistoryData == nil {
				h.Errorf("item %d = %+v (cwa 8.2)", i, it)
			}
		}

		for _, tc := range []struct {
			name string
			in   cloudwatchapi.DescribeAlarmHistoryInput
			want []string
		}{
			{"by name", cloudwatchapi.DescribeAlarmHistoryInput{AlarmName: new("a")}, []string{want[0], want[2]}},
			{"ascending", cloudwatchapi.DescribeAlarmHistoryInput{ScanBy: new(cloudwatchapi.ScanByTimestampAscending)}, []string{want[2], want[1], want[0]}},
			{"metric alarms", cloudwatchapi.DescribeAlarmHistoryInput{AlarmTypes: []cloudwatchapi.AlarmType{cloudwatchapi.AlarmTypeCompositeAlarm, cloudwatchapi.AlarmTypeMetricAlarm}}, want},
			{"composite alarms", cloudwatchapi.DescribeAlarmHistoryInput{AlarmTypes: []cloudwatchapi.AlarmType{cloudwatchapi.AlarmTypeCompositeAlarm}}, nil},
			{"state updates", cloudwatchapi.DescribeAlarmHistoryInput{HistoryItemType: new(cloudwatchapi.HistoryItemTypeStateUpdate)}, want},
			{"configuration updates", cloudwatchapi.DescribeAlarmHistoryInput{HistoryItemType: new(cloudwatchapi.HistoryItemTypeConfigurationUpdate)}, nil},
			{"actions", cloudwatchapi.DescribeAlarmHistoryInput{HistoryItemType: new(cloudwatchapi.HistoryItemTypeAction)}, nil},
			{"contributor", cloudwatchapi.DescribeAlarmHistoryInput{AlarmContributorId: new("c1")}, nil},
			{"dates", cloudwatchapi.DescribeAlarmHistoryInput{StartDate: new(t0.Add(30 * time.Second)), EndDate: new(t0.Add(90 * time.Second))}, []string{want[1]}},
			{"unknown alarm", cloudwatchapi.DescribeAlarmHistoryInput{AlarmName: new("nope")}, nil},
		} {
			if got := summaries(h.history(&tc.in)); !slices.Equal(got, tc.want) {
				h.Errorf("%s: history = %v, want %v", tc.name, got, tc.want)
			}
		}
	})
}

// DescribeAlarmHistory pages by MaxRecords with an opaque NextToken, by 50
// without it (cloudwatch.alarm.history.page-size, CWA-18), and refuses a
// token it did not issue with the existing InvalidNextToken error.
func TestDescribeAlarmHistoryPages(t *testing.T) {
	run(t, func(h *harness) {
		pageSize := switchCase(h, "cloudwatch.alarm.history.page-size", map[string]int{"50": 50})
		must(h, cloudwatchapi.OpPutMetricAlarm, quietAlarm("a"))
		states := []cloudwatchapi.StateValue{cloudwatchapi.StateValueAlarm, cloudwatchapi.StateValueOk}
		for i := range pageSize + 1 {
			h.setState("a", states[i%2])
		}
		out := must(h, cloudwatchapi.OpDescribeAlarmHistory, &cloudwatchapi.DescribeAlarmHistoryInput{})
		if len(out.AlarmHistoryItems) != pageSize || out.NextToken == nil {
			h.Fatalf("first page = %d items, NextToken %v; want %d and a token", len(out.AlarmHistoryItems), out.NextToken, pageSize)
		}
		next := must(h, cloudwatchapi.OpDescribeAlarmHistory, &cloudwatchapi.DescribeAlarmHistoryInput{NextToken: out.NextToken})
		if len(next.AlarmHistoryItems) != 1 || next.NextToken != nil {
			h.Errorf("second page = %d items, NextToken %v; want 1 and none", len(next.AlarmHistoryItems), next.NextToken)
		}
		// Ties keep the newest written first (contract 3.5): the first
		// item written, an INSUFFICIENT_DATA -> ALARM, is last.
		if got := *next.AlarmHistoryItems[0].HistorySummary; got != "Alarm updated from INSUFFICIENT_DATA to ALARM" {
			h.Errorf("last item = %q", got)
		}

		var seen []string
		in := &cloudwatchapi.DescribeAlarmHistoryInput{MaxRecords: new(int32(7))}
		for range 20 {
			page := must(h, cloudwatchapi.OpDescribeAlarmHistory, in)
			if len(page.AlarmHistoryItems) > 7 {
				h.Fatalf("page of %d items, MaxRecords 7", len(page.AlarmHistoryItems))
			}
			for _, it := range page.AlarmHistoryItems {
				seen = append(seen, *it.HistoryData)
			}
			if page.NextToken == nil {
				break
			}
			in.NextToken = page.NextToken
		}
		if len(seen) != pageSize+1 {
			h.Errorf("pages hold %d items, want %d", len(seen), pageSize+1)
		}

		_, err := call(h, cloudwatchapi.OpDescribeAlarmHistory, &cloudwatchapi.DescribeAlarmHistoryInput{NextToken: new("bogus")})
		wantError(h.T, err, "InvalidNextToken", "The specified NextToken is invalid.")
	})
}

// DescribeAlarmsForMetric returns the standard alarms on exactly the named
// metric and dimension set, whatever order the dimensions are listed in,
// narrowed by the filters sent; never alarms on Metrics (cwa 9.4, aws-doc).
// Each is rendered as DescribeAlarms renders it, and the answer has
// MetricAlarms and no CompositeAlarms (localstack@8b9a79f
// tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L6381-L6435).
func TestDescribeAlarmsForMetric(t *testing.T) {
	run(t, func(h *harness) {
		ab := []cloudwatchapi.Dimension{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}}
		ba := []cloudwatchapi.Dimension{{Name: "B", Value: "2"}, {Name: "A", Value: "1"}}
		sum := quietAlarm("m1-sum")
		sum.Dimensions = ab
		avg := quietAlarm("m2-avg")
		avg.Dimensions = ba
		avg.Statistic = new(cloudwatchapi.StatisticAverage)
		avg.Period = new(int32(300))
		avg.Unit = new(cloudwatchapi.StandardUnitCount)
		onlyA := quietAlarm("m3-a")
		onlyA.Dimensions = []cloudwatchapi.Dimension{{Name: "A", Value: "1"}}
		math := &cloudwatchapi.PutMetricAlarmInput{
			AlarmName: "m4-math", EvaluationPeriods: new(int32(1)), Threshold: new(1.0),
			ComparisonOperator: new(cloudwatchapi.ComparisonOperatorGreaterThanThreshold),
			Metrics: []cloudwatchapi.MetricDataQuery{{Id: "m", MetricStat: &cloudwatchapi.MetricStat{
				Metric: cloudwatchapi.Metric{Namespace: new("ns"), MetricName: new("Load"), Dimensions: ab}, Period: 60, Stat: "Sum",
			}}},
		}
		for _, in := range []*cloudwatchapi.PutMetricAlarmInput{avg, sum, onlyA, math} {
			must(h, cloudwatchapi.OpPutMetricAlarm, in)
		}
		names := func(in *cloudwatchapi.DescribeAlarmsForMetricInput) []string {
			h.Helper()
			out := must(h, cloudwatchapi.OpDescribeAlarmsForMetric, in)
			if out.MetricAlarms == nil {
				h.Fatal("MetricAlarms is nil, want a list")
			}
			got := make([]string, 0, len(out.MetricAlarms))
			for _, a := range out.MetricAlarms {
				got = append(got, *a.AlarmName)
			}
			return got
		}
		base := func() *cloudwatchapi.DescribeAlarmsForMetricInput {
			return &cloudwatchapi.DescribeAlarmsForMetricInput{Namespace: "ns", MetricName: "Load", Dimensions: ab}
		}
		for _, tc := range []struct {
			name  string
			alter func(in *cloudwatchapi.DescribeAlarmsForMetricInput)
			want  []string
		}{
			{"dimension set", func(*cloudwatchapi.DescribeAlarmsForMetricInput) {}, []string{"m1-sum", "m2-avg"}},
			{"dimensions in another order", func(in *cloudwatchapi.DescribeAlarmsForMetricInput) { in.Dimensions = ba }, []string{"m1-sum", "m2-avg"}},
			{"statistic", func(in *cloudwatchapi.DescribeAlarmsForMetricInput) { in.Statistic = new(cloudwatchapi.StatisticSum) }, []string{"m1-sum"}},
			{"period", func(in *cloudwatchapi.DescribeAlarmsForMetricInput) { in.Period = new(int32(300)) }, []string{"m2-avg"}},
			{"unit", func(in *cloudwatchapi.DescribeAlarmsForMetricInput) { in.Unit = new(cloudwatchapi.StandardUnitCount) }, []string{"m2-avg"}},
			{"extended statistic", func(in *cloudwatchapi.DescribeAlarmsForMetricInput) { in.ExtendedStatistic = new("p90") }, []string{}},
			{"subset of dimensions", func(in *cloudwatchapi.DescribeAlarmsForMetricInput) { in.Dimensions = in.Dimensions[:1] }, []string{"m3-a"}},
			{"no dimensions", func(in *cloudwatchapi.DescribeAlarmsForMetricInput) { in.Dimensions = nil }, []string{}},
			{"other metric", func(in *cloudwatchapi.DescribeAlarmsForMetricInput) { in.MetricName = "Other" }, []string{}},
		} {
			in := base()
			tc.alter(in)
			if got := names(in); !slices.Equal(got, tc.want) {
				h.Errorf("%s: alarms = %v, want %v", tc.name, got, tc.want)
			}
		}
		out := must(h, cloudwatchapi.OpDescribeAlarmsForMetric, &cloudwatchapi.DescribeAlarmsForMetricInput{Namespace: "ns", MetricName: "Load", Dimensions: ab, Statistic: new(cloudwatchapi.StatisticSum)})
		if want := h.one("m1-sum"); len(out.MetricAlarms) != 1 || !reflect.DeepEqual(out.MetricAlarms[0], want) {
			h.Errorf("DescribeAlarmsForMetric = %+v\nwant the DescribeAlarms rendering %+v", out.MetricAlarms, want)
		}
	})
}

// putRaw stores data of ns/metric stamped now, each datum's unit as given
// (nil for none).
func (h *harness) putRaw(metric string, data ...struct {
	v    float64
	unit *cloudwatchapi.StandardUnit
},
) {
	h.Helper()
	in := &cloudwatchapi.PutMetricDataInput{Namespace: "ns"}
	for _, d := range data {
		in.MetricData = append(in.MetricData, cloudwatchapi.MetricDatum{MetricName: metric, Value: new(d.v), Unit: d.unit})
	}
	must(h, cloudwatchapi.OpPutMetricData, in)
}

type datum = struct {
	v    float64
	unit *cloudwatchapi.StandardUnit
}

func (h *harness) stats(in *cloudwatchapi.GetMetricStatisticsInput) *cloudwatchapi.GetMetricStatisticsOutput {
	h.Helper()
	out := must(h, cloudwatchapi.OpGetMetricStatistics, in)
	if out.Datapoints == nil {
		h.Fatal("Datapoints is nil, want a list (cwa 9.2)")
	}
	return out
}

// GetMetricStatistics answers one datapoint per period with data, at the
// period's start, with the statistics asked for, Timestamp and Unit, "None"
// for data put without one: all five recorded for the values 0 to 9 in one
// minute (localstack@8b9a79f
// tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L4225-L4236;
// cwa 9.2). Label is the metric name, and no data is an empty list
// (same file L5853-L5854).
func TestGetMetricStatisticsAggregates(t *testing.T) {
	run(t, func(h *harness) {
		t0 := h.now()
		data := make([]datum, 10)
		for i := range data {
			data[i] = datum{v: float64(i)}
		}
		h.putRaw("m", data...)
		all := []cloudwatchapi.Statistic{
			cloudwatchapi.StatisticSampleCount, cloudwatchapi.StatisticAverage, cloudwatchapi.StatisticSum,
			cloudwatchapi.StatisticMinimum, cloudwatchapi.StatisticMaximum,
		}
		out := h.stats(&cloudwatchapi.GetMetricStatisticsInput{Namespace: "ns", MetricName: "m", StartTime: t0, EndTime: t0.Add(time.Minute), Period: 60, Statistics: all})
		want := cloudwatchapi.Datapoint{
			Timestamp: new(t0), SampleCount: new(10.0), Average: new(4.5), Sum: new(45.0), Minimum: new(0.0), Maximum: new(9.0),
			Unit: new(cloudwatchapi.StandardUnitNone),
		}
		if out.Label == nil || *out.Label != "m" || len(out.Datapoints) != 1 || !reflect.DeepEqual(out.Datapoints[0], want) {
			h.Errorf("GetMetricStatistics = %+v %+v\nwant Label m and %+v", out.Label, out.Datapoints, want)
		}

		// Only the statistics asked for (aws-doc, aws/aws-cli e9993cf
		// awscli/examples/cloudwatch/get-metric-statistics.rst).
		out = h.stats(&cloudwatchapi.GetMetricStatisticsInput{Namespace: "ns", MetricName: "m", StartTime: t0, EndTime: t0.Add(time.Minute), Period: 60, Statistics: []cloudwatchapi.Statistic{cloudwatchapi.StatisticMaximum}})
		if len(out.Datapoints) != 1 || !reflect.DeepEqual(out.Datapoints[0], cloudwatchapi.Datapoint{Timestamp: new(t0), Maximum: new(9.0), Unit: new(cloudwatchapi.StandardUnitNone)}) {
			h.Errorf("Maximum only = %+v", out.Datapoints)
		}

		// EndTime is exclusive (cwa 9.1).
		out = h.stats(&cloudwatchapi.GetMetricStatisticsInput{Namespace: "ns", MetricName: "m", StartTime: t0.Add(-time.Minute), EndTime: t0, Period: 60, Statistics: all})
		if len(out.Datapoints) != 0 || *out.Label != "m" {
			h.Errorf("before the data = %+v", out.Datapoints)
		}
		// The dimension set must match exactly (cwa 9.1).
		out = h.stats(&cloudwatchapi.GetMetricStatisticsInput{
			Namespace: "ns", MetricName: "m", Dimensions: []cloudwatchapi.Dimension{{Name: "A", Value: "1"}},
			StartTime: t0, EndTime: t0.Add(time.Minute), Period: 60, Statistics: all,
		})
		if len(out.Datapoints) != 0 {
			h.Errorf("with a dimension the data lacks = %+v", out.Datapoints)
		}
	})
}

// Without Unit, data of several units in one period answers one datapoint
// per unit: values 1 Seconds, 5 Count and 10 with no unit give Average 1.0,
// 5.0 and 10.0 in Seconds, Count and None (localstack@8b9a79f
// tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L4296-L4320,
// cwa 9.2); with Unit, that unit's alone (cwa 9.1). AWS documents no order
// ("Data points are not returned in chronological order", cwa 9.2):
// cloudwatch.get-metric-statistics.order sorts by timestamp, then unit.
func TestGetMetricStatisticsUnits(t *testing.T) {
	run(t, func(h *harness) {
		followsSwitch(h, "cloudwatch.get-metric-statistics.order", "timestamp-ascending,then-unit")
		t0 := h.now()
		h.putRaw("m", datum{1, new(cloudwatchapi.StandardUnitSeconds)}, datum{5, new(cloudwatchapi.StandardUnitCount)}, datum{10, nil})
		in := &cloudwatchapi.GetMetricStatisticsInput{
			Namespace: "ns", MetricName: "m", StartTime: t0, EndTime: t0.Add(time.Minute), Period: 60,
			Statistics: []cloudwatchapi.Statistic{cloudwatchapi.StatisticAverage},
		}
		out := h.stats(in)
		want := []cloudwatchapi.Datapoint{
			{Timestamp: new(t0), Average: new(5.0), Unit: new(cloudwatchapi.StandardUnitCount)},
			{Timestamp: new(t0), Average: new(10.0), Unit: new(cloudwatchapi.StandardUnitNone)},
			{Timestamp: new(t0), Average: new(1.0), Unit: new(cloudwatchapi.StandardUnitSeconds)},
		}
		if !reflect.DeepEqual(out.Datapoints, want) {
			h.Errorf("Datapoints = %+v\nwant %+v", out.Datapoints, want)
		}
		in.Unit = new(cloudwatchapi.StandardUnitCount)
		if out := h.stats(in); !reflect.DeepEqual(out.Datapoints, want[:1]) {
			h.Errorf("Unit Count = %+v", out.Datapoints)
		}
	})
}

// The 1440 limit is counted from the request before any data is read:
// StartTime now, EndTime a day later and Period 1 is refused with the
// recorded text, and Period 60 over a day from a StartTime off the minute
// is accepted, so the count is not taken over a rounded-down StartTime
// (localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L5813-L5852,
// cwa 9.3). StartTime not before EndTime is refused with the recorded
// text (same file L5841-L5849).
func TestGetMetricStatisticsLimits(t *testing.T) {
	run(t, func(h *harness) {
		h.advance(30 * time.Second)
		now := h.now()
		stat := []cloudwatchapi.Statistic{cloudwatchapi.StatisticAverage}
		_, err := call(h, cloudwatchapi.OpGetMetricStatistics, &cloudwatchapi.GetMetricStatisticsInput{
			Namespace: "ns", MetricName: "metric_name", StartTime: now, EndTime: now.Add(day), Period: 1, Statistics: stat,
		})
		wantError(h.T, err, "InvalidParameterCombination", "You have requested up to 86400 datapoints, which exceeds the limit of 1440. You may reduce the datapoints requested by increasing Period, or decreasing the time range.")
		if err != nil && err.ID.Status != 400 {
			h.Errorf("status = %d, want 400", err.ID.Status)
		}
		out := h.stats(&cloudwatchapi.GetMetricStatisticsInput{Namespace: "ns", MetricName: "metric_name", StartTime: now, EndTime: now.Add(day), Period: 60, Statistics: stat})
		if len(out.Datapoints) != 0 || out.Label == nil || *out.Label != "metric_name" {
			h.Errorf("a day by the minute = %+v %v, want {Datapoints: [], Label: metric_name}", out.Datapoints, out.Label)
		}
		_, err = call(h, cloudwatchapi.OpGetMetricStatistics, &cloudwatchapi.GetMetricStatisticsInput{
			Namespace: "ns", MetricName: "metric_name", StartTime: now, EndTime: now, Period: 60, Statistics: stat,
		})
		wantError(h.T, err, "InvalidParameterValue", "The parameter StartTime must be less than the parameter EndTime.")
	})
}

// A count that is not whole rounds up over the unrounded StartTime
// (cloudwatch.get-metric-statistics.datapoint-limit, UNVERIFIED, CWA-20).
func TestGetMetricStatisticsCountRounding(t *testing.T) {
	run(t, func(h *harness) {
		followsSwitch(h, "cloudwatch.get-metric-statistics.datapoint-limit", "ceil-unrounded")
		now := h.now()
		stat := []cloudwatchapi.Statistic{cloudwatchapi.StatisticSum}
		// 1440 periods and one second: 1441 by the ceiling.
		_, err := call(h, cloudwatchapi.OpGetMetricStatistics, &cloudwatchapi.GetMetricStatisticsInput{
			Namespace: "ns", MetricName: "m", StartTime: now, EndTime: now.Add(day + time.Second), Period: 60, Statistics: stat,
		})
		if err == nil || err.ID.QueryCode != "InvalidParameterCombination" ||
			err.Message != "You have requested up to "+strconv.Itoa(1441)+" datapoints, which exceeds the limit of 1440. You may reduce the datapoints requested by increasing Period, or decreasing the time range." {
			h.Errorf("1440 periods and a second = %v, want the 1441 datapoint refusal", err)
		}
		if _, err := call(h, cloudwatchapi.OpGetMetricStatistics, &cloudwatchapi.GetMetricStatisticsInput{
			Namespace: "ns", MetricName: "m", StartTime: now, EndTime: now.Add(day), Period: 60, Statistics: stat,
		}); err != nil {
			h.Errorf("exactly 1440 periods = %s %s, want accepted", err.ID.QueryCode, err.Message)
		}
	})
}

// Statistics and ExtendedStatistics together or neither, and a Period that
// is not 1, 5, 10, 20, 30 or a multiple of 60, are client errors (cwa 9.1,
// 9.3; every error the model lists for the operation but
// InternalServiceFault is 400). Their codes and texts are UNVERIFIED
// (CWA-19) and no switch governs them, so only the status is asserted.
// ExtendedStatistics alone answers 501: understudy keeps statistic sets and
// cannot compute percentiles (cwa 12.1, 12.7).
func TestGetMetricStatisticsRefusals(t *testing.T) {
	run(t, func(h *harness) {
		now := h.now()
		base := func() *cloudwatchapi.GetMetricStatisticsInput {
			return &cloudwatchapi.GetMetricStatisticsInput{
				Namespace: "ns", MetricName: "m", StartTime: now, EndTime: now.Add(time.Hour), Period: 60,
				Statistics: []cloudwatchapi.Statistic{cloudwatchapi.StatisticSum},
			}
		}
		for _, tc := range []struct {
			name  string
			alter func(in *cloudwatchapi.GetMetricStatisticsInput)
		}{
			{"both", func(in *cloudwatchapi.GetMetricStatisticsInput) { in.ExtendedStatistics = []string{"p90"} }},
			{"neither", func(in *cloudwatchapi.GetMetricStatisticsInput) { in.Statistics = nil }},
			{"period 7", func(in *cloudwatchapi.GetMetricStatisticsInput) { in.Period = 7 }},
			{"period 90", func(in *cloudwatchapi.GetMetricStatisticsInput) { in.Period = 90 }},
		} {
			in := base()
			tc.alter(in)
			if _, err := call(h, cloudwatchapi.OpGetMetricStatistics, in); err == nil || err.ID.Status != 400 {
				h.Errorf("%s: error = %v, want a 400", tc.name, err)
			}
		}
		for _, p := range []int32{1, 5, 10, 20, 30, 60, 120} {
			in := base()
			in.Period = p
			in.EndTime = now.Add(time.Minute)
			if _, err := call(h, cloudwatchapi.OpGetMetricStatistics, in); err != nil {
				h.Errorf("Period %d = %s %s, want accepted (cwa 9.1)", p, err.ID.QueryCode, err.Message)
			}
		}
		in := base()
		in.Statistics, in.ExtendedStatistics = nil, []string{"p90"}
		if _, err := call(h, cloudwatchapi.OpGetMetricStatistics, in); err == nil || err.ID.Status != 501 {
			h.Errorf("ExtendedStatistics alone = %v, want 501", err)
		}
	})
}
