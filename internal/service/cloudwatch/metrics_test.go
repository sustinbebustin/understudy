package cloudwatch_test

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

const day = 24 * time.Hour

// bubbleTB hides *testing.T from rapid, which would otherwise call
// T.Deadline, forbidden inside a synctest bubble.
type bubbleTB struct{ *testing.T }

// put stores values of one metric of namespace "ns", stamped now.
func (h *harness) put(metric string, unit cloudwatchapi.StandardUnit, values ...float64) {
	h.Helper()
	data := make([]cloudwatchapi.MetricDatum, len(values))
	for i, v := range values {
		data[i] = cloudwatchapi.MetricDatum{MetricName: metric, Value: new(v), Unit: new(unit)}
	}
	must(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{Namespace: "ns", MetricData: data})
}

// stat is a MetricStat query of metric "ns"/name.
func stat(id, name, statistic string, period int32) cloudwatchapi.MetricDataQuery {
	return cloudwatchapi.MetricDataQuery{Id: id, MetricStat: &cloudwatchapi.MetricStat{
		Metric: cloudwatchapi.Metric{Namespace: new("ns"), MetricName: new(name)}, Period: period, Stat: statistic,
	}}
}

// around queries the minute before and after now, as the recorded tests do.
func (h *harness) around(queries ...cloudwatchapi.MetricDataQuery) *cloudwatchapi.GetMetricDataOutput {
	h.Helper()
	return must(h, cloudwatchapi.OpGetMetricData, &cloudwatchapi.GetMetricDataInput{
		MetricDataQueries: queries, StartTime: h.now().Add(-time.Minute), EndTime: h.now().Add(time.Minute),
	})
}

// labelled is a result's id, label and values.
type labelled struct {
	id, label string
	values    []float64
}

func results(out *cloudwatchapi.GetMetricDataOutput) []labelled {
	got := make([]labelled, len(out.MetricDataResults))
	for i, r := range out.MetricDataResults {
		got[i] = labelled{id: *r.Id, label: *r.Label, values: r.Values}
	}
	return got
}

func equalResults(a, b []labelled) bool {
	return slices.EqualFunc(a, b, func(x, y labelled) bool {
		return x.id == y.id && x.label == y.label && slices.Equal(x.values, y.values)
	})
}

// PutMetricData then GetMetricData aggregates each statistic over the
// period, labelled by statistic when the queries differ in it (AWS-recorded
// test_get_metric_data_with_zero_and_labels).
func TestGetMetricDataAggregatesOverAPeriod(t *testing.T) {
	run(t, func(h *harness) {
		h.put("metric1", cloudwatchapi.StandardUnitSeconds, 0, 2, 4, 3.5, 7, 100)
		out := h.around(
			stat("result_Average", "metric1", "Average", 60), stat("result_Sum", "metric1", "Sum", 60),
			stat("result_Minimum", "metric1", "Minimum", 60), stat("result_Maximum", "metric1", "Maximum", 60),
			stat("result_SampleCount", "metric1", "SampleCount", 60),
		)
		want := []labelled{
			{"result_Average", "metric1 Average", []float64{19.416666666666668}},
			{"result_Sum", "metric1 Sum", []float64{116.5}},
			{"result_Minimum", "metric1 Minimum", []float64{0}},
			{"result_Maximum", "metric1 Maximum", []float64{100}},
			{"result_SampleCount", "metric1 SampleCount", []float64{6}},
		}
		if got := results(out); !equalResults(got, want) {
			h.Errorf("results = %+v\nwant %+v", got, want)
		}
		if out.Messages == nil || len(out.Messages) != 0 || out.NextToken != nil {
			h.Errorf("Messages = %v, NextToken = %v; want an empty list and none", out.Messages, out.NextToken)
		}
		for _, r := range out.MetricDataResults {
			if *r.StatusCode != cloudwatchapi.StatusCodeComplete || len(r.Timestamps) != 1 || !r.Timestamps[0].Equal(h.now()) {
				h.Errorf("%s: status %s timestamps %v", *r.Id, *r.StatusCode, r.Timestamps)
			}
		}
	})
}

// Values and Counts fold into one set, as do StatisticValues.
func TestPutMetricDataValueForms(t *testing.T) {
	run(t, func(h *harness) {
		must(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{Namespace: "ns", MetricData: []cloudwatchapi.MetricDatum{
			{MetricName: "m", Values: []float64{1, 2}, Counts: []float64{3, 1}},
			{MetricName: "m", StatisticValues: &cloudwatchapi.StatisticSet{SampleCount: 2, Sum: 10, Minimum: 4, Maximum: 6}},
			{MetricName: "m", Values: []float64{-1}},
		}})
		got := results(h.around(stat("n", "m", "SampleCount", 60), stat("s", "m", "Sum", 60), stat("lo", "m", "Minimum", 60), stat("hi", "m", "Maximum", 60)))
		want := []labelled{{"n", "m SampleCount", []float64{7}}, {"s", "m Sum", []float64{14}}, {"lo", "m Minimum", []float64{-1}}, {"hi", "m Maximum", []float64{6}}}
		if !equalResults(got, want) {
			h.Errorf("results = %+v\nwant %+v", got, want)
		}
	})
}

// A period of no samples has no Average: the result leaves it out rather
// than answer NaN, while its other statistics remain.
func TestGetMetricDataAverageOfNoSamples(t *testing.T) {
	run(t, func(h *harness) {
		must(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{Namespace: "ns", MetricData: []cloudwatchapi.MetricDatum{
			{MetricName: "m", StatisticValues: &cloudwatchapi.StatisticSet{SampleCount: 0, Sum: 0, Minimum: 0, Maximum: 0}},
			{MetricName: "m", Values: []float64{5}, Counts: []float64{0}},
		}})
		got := results(h.around(stat("avg", "m", "Average", 60), stat("n", "m", "SampleCount", 60)))
		want := []labelled{{"avg", "m Average", []float64{}}, {"n", "m SampleCount", []float64{0}}}
		if !equalResults(got, want) {
			h.Errorf("results = %+v\nwant %+v", got, want)
		}
	})
}

// The default label adds the statistic and the period only when the
// request's queries differ in them; the unit never (AWS-recorded
// test_label_generation, all seven cases).
func TestGetMetricDataDefaultLabels(t *testing.T) {
	type q struct {
		stat   string
		period int32
		unit   cloudwatchapi.StandardUnit
	}
	for i, tc := range []struct {
		queries [2]q
		labels  [2]string
		values  [2][]float64
	}{
		{[2]q{{"Sum", 60, "Seconds"}, {"Minimum", 30, "Seconds"}}, [2]string{"metric1 Sum 60", "metric1 Minimum 30"}, [2][]float64{{109}, {0}}},
		{[2]q{{"Sum", 60, "Seconds"}, {"Minimum", 60, "Seconds"}}, [2]string{"metric1 Sum", "metric1 Minimum"}, [2][]float64{{109}, {0}}},
		{[2]q{{"Sum", 60, "Seconds"}, {"Sum", 30, "Seconds"}}, [2]string{"metric1 60", "metric1 30"}, [2][]float64{{109}, {109}}},
		{[2]q{{"Sum", 60, "Seconds"}, {"Minimum", 30, "Milliseconds"}}, [2]string{"metric1 Sum 60", "metric1 Minimum 30"}, [2][]float64{{109}, {}}},
		{[2]q{{"Sum", 60, "Seconds"}, {"Minimum", 60, "Milliseconds"}}, [2]string{"metric1 Sum", "metric1 Minimum"}, [2][]float64{{109}, {}}},
		{[2]q{{"Sum", 60, "Seconds"}, {"Sum", 30, "Milliseconds"}}, [2]string{"metric1 60", "metric1 30"}, [2][]float64{{109}, {}}},
		{[2]q{{"Sum", 60, "Seconds"}, {"Sum", 60, "Milliseconds"}}, [2]string{"metric1", "metric1"}, [2][]float64{{109}, {}}},
	} {
		t.Run(fmt.Sprintf("input_pairs%d", i), func(t *testing.T) {
			run(t, func(h *harness) {
				h.put("metric1", cloudwatchapi.StandardUnitSeconds, 0, 2, 7, 100)
				queries := make([]cloudwatchapi.MetricDataQuery, 2)
				for j, p := range tc.queries {
					queries[j] = stat(fmt.Sprintf("result_%s_%d_%s", p.stat, p.period, p.unit), "metric1", p.stat, p.period)
					queries[j].MetricStat.Unit = new(p.unit)
				}
				for j, r := range h.around(queries...).MetricDataResults {
					if *r.Label != tc.labels[j] || !slices.Equal(r.Values, tc.values[j]) || r.Values == nil {
						h.Errorf("%s = %q %v, want %q %v", *r.Id, *r.Label, r.Values, tc.labels[j], tc.values[j])
					}
				}
			})
		})
	}
}

// A query without a Unit over data of several units answers with the
// first unit by name and says so (AWS-recorded
// test_get_metric_data_different_units_no_unit_in_query).
func TestGetMetricDataWithSeveralUnits(t *testing.T) {
	for i, tc := range []struct {
		put     func(h *harness)
		value   float64
		message string
	}{
		{func(h *harness) {
			h.put("m-test", cloudwatchapi.StandardUnitMilliseconds, 60000)
			h.put("m-test", cloudwatchapi.StandardUnitSeconds, 60)
		}, 60000, "Multiple units returned: '[Milliseconds, Seconds]'"},
		{func(h *harness) {
			for range 2 {
				h.put("m-test", cloudwatchapi.StandardUnitMilliseconds, 60000)
				h.put("m-test", cloudwatchapi.StandardUnitSeconds, 60)
			}
		}, 120000, "Multiple units returned: '[Milliseconds, Seconds]'"},
		{func(h *harness) {
			for range 2 {
				h.put("m-test", cloudwatchapi.StandardUnitMilliseconds, 60000)
				h.put("m-test", cloudwatchapi.StandardUnitSeconds, 60)
			}
			h.put("m-test", cloudwatchapi.StandardUnitCount, 5)
		}, 5, "Multiple units returned: '[Count, Milliseconds, Seconds]'"},
	} {
		t.Run(fmt.Sprintf("metric_data%d", i), func(t *testing.T) {
			run(t, func(h *harness) {
				tc.put(h)
				out := must(h, cloudwatchapi.OpGetMetricData, &cloudwatchapi.GetMetricDataInput{
					MetricDataQueries: []cloudwatchapi.MetricDataQuery{stat("m1", "m-test", "Sum", 60)},
					StartTime:         h.now(), EndTime: h.now().Add(time.Minute),
				})
				r := out.MetricDataResults[0]
				want := []cloudwatchapi.MessageData{{Code: new("MultipleUnits"), Value: new(tc.message)}}
				if !slices.Equal(r.Values, []float64{tc.value}) || !reflect.DeepEqual(r.Messages, want) {
					h.Errorf("values %v messages %+v, want %v %s", r.Values, r.Messages, tc.value, tc.message)
				}
			})
		})
	}
}

// A query names a series exactly: without dimensions it does not read data
// put with them (AWS-recorded test_get_metric_with_null_dimensions).
func TestGetMetricDataMatchesTheDimensionSetExactly(t *testing.T) {
	run(t, func(h *harness) {
		must(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{Namespace: "ns", MetricData: []cloudwatchapi.MetricDatum{
			{MetricName: "m", Value: new(3.0), Dimensions: []cloudwatchapi.Dimension{{Name: "b", Value: "2"}, {Name: "a", Value: "1"}}},
		}})
		bare := stat("bare", "m", "Sum", 60)
		dims := stat("dims", "m", "Sum", 60)
		dims.MetricStat.Metric.Dimensions = []cloudwatchapi.Dimension{{Name: "a", Value: "1"}, {Name: "b", Value: "2"}}
		got := results(h.around(bare, dims))
		if want := []labelled{{"bare", "m", []float64{}}, {"dims", "m", []float64{3}}}; !equalResults(got, want) {
			h.Errorf("results = %+v, want %+v", got, want)
		}
	})
}

// Periods without data are left out; the newest period comes first unless
// ScanBy asks for ascending order.
func TestGetMetricDataPeriodsAndOrder(t *testing.T) {
	run(t, func(h *harness) {
		start := h.now()
		h.put("m", cloudwatchapi.StandardUnitCount, 1, 2)
		h.advance(2 * time.Minute)
		h.put("m", cloudwatchapi.StandardUnitCount, 10)
		h.advance(30 * time.Second)
		h.put("m", cloudwatchapi.StandardUnitCount, 20)
		in := cloudwatchapi.GetMetricDataInput{
			MetricDataQueries: []cloudwatchapi.MetricDataQuery{stat("s", "m", "Sum", 60)},
			StartTime:         start, EndTime: start.Add(5 * time.Minute),
		}
		r := must(h, cloudwatchapi.OpGetMetricData, &in).MetricDataResults[0]
		if want := []time.Time{start.Add(2 * time.Minute), start}; !slices.Equal(r.Values, []float64{30, 3}) || !slices.EqualFunc(r.Timestamps, want, time.Time.Equal) {
			h.Errorf("descending = %v at %v", r.Values, r.Timestamps)
		}
		in.ScanBy = new(cloudwatchapi.ScanByTimestampAscending)
		if r := must(h, cloudwatchapi.OpGetMetricData, &in).MetricDataResults[0]; !slices.Equal(r.Values, []float64{3, 30}) {
			h.Errorf("ascending = %v", r.Values)
		}
		// EndTime is exclusive.
		in.EndTime = start.Add(2 * time.Minute)
		if r := must(h, cloudwatchapi.OpGetMetricData, &in).MetricDataResults[0]; !slices.Equal(r.Values, []float64{3}) {
			h.Errorf("before the third minute = %v", r.Values)
		}
	})
}

// StartTime rounds down by its age: to five minutes once it is fifteen
// days old, so a point minutes before it is read.
func TestGetMetricDataRoundsStartTimeByAge(t *testing.T) {
	run(t, func(h *harness) {
		at := h.now()
		h.put("m", cloudwatchapi.StandardUnitCount, 1)
		in := cloudwatchapi.GetMetricDataInput{
			MetricDataQueries: []cloudwatchapi.MetricDataQuery{stat("s", "m", "Sum", 300)},
			StartTime:         at.Add(3 * time.Minute), EndTime: at.Add(time.Hour),
		}
		if r := must(h, cloudwatchapi.OpGetMetricData, &in).MetricDataResults[0]; len(r.Values) != 0 {
			h.Errorf("a fresh StartTime rounds to the minute; read %v", r.Values)
		}
		h.advance(20 * day)
		if r := must(h, cloudwatchapi.OpGetMetricData, &in).MetricDataResults[0]; !slices.Equal(r.Values, []float64{1}) || !r.Timestamps[0].Equal(at) {
			h.Errorf("a 20-day-old StartTime rounds to five minutes; read %v at %v", r.Values, r.Timestamps)
		}
	})
}

func TestPutMetricDataValidation(t *testing.T) {
	run(t, func(h *harness) {
		for _, tc := range []struct {
			name          string
			datum         cloudwatchapi.MetricDatum
			code, message string
		}{
			{
				"value and values",
				cloudwatchapi.MetricDatum{MetricName: "m", Value: new(1.0), Values: []float64{1}},
				"InvalidParameterCombination", "The parameters MetricData.member.1.Value and MetricData.member.1.Values are mutually exclusive and you have specified both.",
			},
			{
				"value and statistic values",
				cloudwatchapi.MetricDatum{MetricName: "m", Value: new(1.0), StatisticValues: &cloudwatchapi.StatisticSet{SampleCount: 1}},
				"InvalidParameterCombination", "The parameters MetricData.member.1.Value and MetricData.member.1.StatisticValues are mutually exclusive and you have specified both.",
			},
			{
				"values and counts",
				cloudwatchapi.MetricDatum{MetricName: "m", Values: []float64{1, 2}, Counts: []float64{1}},
				"InvalidParameterValue", "The parameters MetricData.member.1.Values and MetricData.member.1.Counts must be of the same size.",
			},
			{
				"no value",
				cloudwatchapi.MetricDatum{MetricName: "m"},
				"MissingParameter", "One of the parameters MetricData.member.1.Value, MetricData.member.1.Values or MetricData.member.1.StatisticValues is required.",
			},
			{
				"infinite",
				cloudwatchapi.MetricDatum{MetricName: "m", Value: new(2e300)},
				"InvalidParameterValue", "The value 2e+300 for parameter MetricData.member.1.Value is invalid.",
			},
		} {
			_, err := call(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{Namespace: "ns", MetricData: []cloudwatchapi.MetricDatum{tc.datum}})
			if err == nil || err.ID.QueryCode != tc.code || err.Message != tc.message {
				h.Errorf("%s = %v, want %s %q", tc.name, err, tc.code, tc.message)
			}
		}
		_, err := call(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{Namespace: "AWS/EC2", MetricData: []cloudwatchapi.MetricDatum{{MetricName: "m", Value: new(1.0)}}})
		wantError(h.T, err, "InvalidParameterValue", "The value AWS/EC2 for parameter Namespace is invalid.")
		// The namespace is ASCII without control characters (the
		// PutMetricData documentation).
		for _, ns := range []string{"App\x01", "App\x7f", "Aplicación"} {
			_, err = call(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{Namespace: ns, MetricData: []cloudwatchapi.MetricDatum{{MetricName: "m", Value: new(1.0)}}})
			wantError(h.T, err, "InvalidParameterValue", "The value "+ns+" for parameter Namespace is invalid.")
		}
		entity := cloudwatchapi.EntityMetricData{MetricData: []cloudwatchapi.MetricDatum{{MetricName: "m", Value: new(1.0)}}}
		_, err = call(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{Namespace: "ns", EntityMetricData: []cloudwatchapi.EntityMetricData{entity}})
		wantError(h.T, err, "MissingParameter", "The parameter StrictEntityValidation is required.")
		_, err = call(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{
			Namespace: "ns", EntityMetricData: []cloudwatchapi.EntityMetricData{entity, entity, entity}, StrictEntityValidation: new(true),
		})
		wantError(h.T, err, "InvalidParameterValue", "The collection EntityMetricData must not have a size greater than 2.")
		if got := must(h, cloudwatchapi.OpListMetrics, &cloudwatchapi.ListMetricsInput{}).Metrics; len(got) != 0 {
			h.Errorf("a refused request stored %+v", got)
		}
	})
}

// Without strict entity validation an entity error does not refuse the
// request: its metric data is stored (the PutMetricData documentation).
func TestPutMetricDataStoresDataPastAnEntityErrorWhenNotStrict(t *testing.T) {
	run(t, func(h *harness) {
		entity := cloudwatchapi.EntityMetricData{MetricData: []cloudwatchapi.MetricDatum{{MetricName: "m", Value: new(1.0)}}}
		must(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{
			Namespace: "ns", EntityMetricData: []cloudwatchapi.EntityMetricData{entity, entity, entity}, StrictEntityValidation: new(false),
		})
		if got := results(h.around(stat("n", "m", "SampleCount", 60))); !equalResults(got, []labelled{{"n", "m", []float64{3}}}) {
			h.Errorf("results = %+v, want the three data points", got)
		}
	})
}

// A datum with both Values and StatisticValues is accepted, as AWS accepts
// it (AWS-recorded test_put_metric_data_validation), and stores its
// StatisticValues.
func TestPutMetricDataAcceptsValuesWithStatisticValues(t *testing.T) {
	run(t, func(h *harness) {
		must(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{Namespace: "ns", MetricData: []cloudwatchapi.MetricDatum{{
			MetricName: "m", Values: []float64{1, 10},
			StatisticValues: &cloudwatchapi.StatisticSet{SampleCount: 10, Sum: 55, Minimum: 1, Maximum: 10},
		}}})
		got := results(h.around(stat("n", "m", "SampleCount", 60), stat("s", "m", "Sum", 60)))
		if want := []labelled{{"n", "m SampleCount", []float64{10}}, {"s", "m Sum", []float64{55}}}; !equalResults(got, want) {
			h.Errorf("results = %+v\nwant %+v", got, want)
		}
	})
}

// A sub-minute period rounds StartTime down to its own division of the
// minute, not to the minute: a 10-second query from 01:05:13 starts at
// 01:05:10 (the GetMetricData documentation).
func TestGetMetricDataRoundsStartTimeToASubMinutePeriod(t *testing.T) {
	run(t, func(h *harness) {
		minute := h.now().Truncate(time.Minute)
		at := func(offset time.Duration, v float64) cloudwatchapi.MetricDatum {
			return cloudwatchapi.MetricDatum{MetricName: "m", Value: new(v), Timestamp: new(minute.Add(offset)), StorageResolution: new(int32(1))}
		}
		must(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{Namespace: "ns", MetricData: []cloudwatchapi.MetricDatum{
			at(5*time.Second, 1), at(15*time.Second, 2), at(25*time.Second, 4),
		}})
		for _, tc := range []struct {
			period int32
			start  time.Duration
			want   []float64
			first  time.Duration
		}{
			{10, 13 * time.Second, []float64{2, 4}, 10 * time.Second},
			{1, 15 * time.Second, []float64{2, 4}, 15 * time.Second},
			{60, 23 * time.Second, []float64{7}, 0},
		} {
			in := cloudwatchapi.GetMetricDataInput{
				MetricDataQueries: []cloudwatchapi.MetricDataQuery{stat("s", "m", "Sum", tc.period)},
				StartTime:         minute.Add(tc.start), EndTime: minute.Add(time.Minute),
				ScanBy: new(cloudwatchapi.ScanByTimestampAscending),
			}
			r := must(h, cloudwatchapi.OpGetMetricData, &in).MetricDataResults[0]
			if !slices.Equal(r.Values, tc.want) || len(r.Timestamps) == 0 || !r.Timestamps[0].Equal(minute.Add(tc.first)) {
				h.Errorf("period %d from +%v = %v at %v, want %v from +%v", tc.period, tc.start, r.Values, r.Timestamps, tc.want, tc.first)
			}
		}
	})
}

// A regular-resolution metric, StorageResolution 60 or absent, is stored at
// one-minute resolution, so a sub-minute period sees its data only in the
// period that starts the minute (the MetricDatum documentation).
func TestGetMetricDataReadsRegularResolutionByTheMinute(t *testing.T) {
	run(t, func(h *harness) {
		minute := h.now().Truncate(time.Minute)
		must(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{Namespace: "ns", MetricData: []cloudwatchapi.MetricDatum{
			{MetricName: "m", Value: new(1.0), Timestamp: new(minute.Add(5 * time.Second))},
			{MetricName: "m", Value: new(2.0), Timestamp: new(minute.Add(45 * time.Second)), StorageResolution: new(int32(60))},
		}})
		in := cloudwatchapi.GetMetricDataInput{
			MetricDataQueries: []cloudwatchapi.MetricDataQuery{stat("s", "m", "Sum", 10)},
			StartTime:         minute, EndTime: minute.Add(time.Minute),
		}
		r := must(h, cloudwatchapi.OpGetMetricData, &in).MetricDataResults[0]
		if !slices.Equal(r.Values, []float64{3}) || !r.Timestamps[0].Equal(minute) {
			h.Errorf("10-second periods = %v at %v, want [3] at the minute", r.Values, r.Timestamps)
		}
	})
}

// Past three hours of age, a StartTime needs a period of whole minutes,
// past 15 days of five minutes and past 63 days of hours; a finer period
// reads no data (the MetricStat documentation).
func TestGetMetricDataNeedsAPeriodForStartTimesAge(t *testing.T) {
	run(t, func(h *harness) {
		at := h.now()
		must(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{Namespace: "ns", MetricData: []cloudwatchapi.MetricDatum{
			{MetricName: "m", Value: new(1.0), StorageResolution: new(int32(1))},
		}})
		read := func(period int32) []float64 {
			h.Helper()
			return must(h, cloudwatchapi.OpGetMetricData, &cloudwatchapi.GetMetricDataInput{
				MetricDataQueries: []cloudwatchapi.MetricDataQuery{stat("s", "m", "Sum", period)},
				StartTime:         at, EndTime: at.Add(time.Hour),
			}).MetricDataResults[0].Values
		}
		for _, tc := range []struct {
			age             time.Duration
			served, refused int32
		}{
			{3 * time.Hour, 10, 0},
			{4 * time.Hour, 60, 10},
			{20 * day, 300, 60},
			{70 * day, 3600, 300},
		} {
			h.advance(at.Add(tc.age).Sub(h.now()))
			if got := read(tc.served); !slices.Equal(got, []float64{1}) {
				h.Errorf("age %v, period %d = %v, want [1]", tc.age, tc.served, got)
			}
			if tc.refused == 0 {
				continue
			}
			if got := read(tc.refused); len(got) != 0 {
				h.Errorf("age %v, period %d = %v, want no data", tc.age, tc.refused, got)
			}
		}
	})
}

// ListMetrics leaves out a metric that has not reported data for two
// weeks of emulator time (the ListMetrics documentation); its points stay
// readable.
func TestListMetricsLeavesOutMetricsQuietForTwoWeeks(t *testing.T) {
	run(t, func(h *harness) {
		h.put("old", cloudwatchapi.StandardUnitCount, 1)
		h.advance(14*day - time.Hour)
		h.put("new", cloudwatchapi.StandardUnitCount, 1)
		h.advance(2 * time.Hour)
		metrics := must(h, cloudwatchapi.OpListMetrics, &cloudwatchapi.ListMetricsInput{Namespace: new("ns")}).Metrics
		if len(metrics) != 1 || *metrics[0].MetricName != "new" {
			h.Errorf("ListMetrics = %+v, want only the metric reported within two weeks", metrics)
		}
	})
}

// The two-week and two-hour windows accept a timestamp valid by either the
// wall clock or the emulator clock.
func TestPutMetricDataWindowsAcceptEitherClock(t *testing.T) {
	run(t, func(h *harness) {
		wall := h.now()
		h.advance(30 * day)
		emu := h.now()
		at := func(ts time.Time) *cloudwatchapi.PutMetricDataInput {
			return &cloudwatchapi.PutMetricDataInput{Namespace: "ns", MetricData: []cloudwatchapi.MetricDatum{{MetricName: "m", Value: new(1.0), Timestamp: new(ts)}}}
		}
		must(h, cloudwatchapi.OpPutMetricData, at(emu))
		must(h, cloudwatchapi.OpPutMetricData, at(wall))
		_, err := call(h, cloudwatchapi.OpPutMetricData, at(emu.Add(3*time.Hour)))
		wantError(h.T, err, "InvalidParameterValue", "The parameter MetricData.member.1.Timestamp must specify a time no more than two hours in the future.")
		_, err = call(h, cloudwatchapi.OpPutMetricData, at(wall.Add(-15*day)))
		wantError(h.T, err, "InvalidParameterValue", "The parameter MetricData.member.1.Timestamp must specify a time no more than two weeks in the past.")
		// Between the two windows: too new by wall time, too old by the
		// emulator clock, so valid by neither.
		_, err = call(h, cloudwatchapi.OpPutMetricData, at(wall.Add(10*day)))
		wantError(h.T, err, "InvalidParameterValue", "The parameter MetricData.member.1.Timestamp must specify a time no more than two weeks in the past.")
	})
}

func TestGetMetricDataValidation(t *testing.T) {
	run(t, func(h *harness) {
		now := h.now()
		get := func(queries ...cloudwatchapi.MetricDataQuery) error {
			_, err := call(h, cloudwatchapi.OpGetMetricData, &cloudwatchapi.GetMetricDataInput{MetricDataQueries: queries, StartTime: now, EndTime: now.Add(time.Minute)})
			if err == nil {
				return nil
			}
			return err.AsError()
		}
		_, err := call(h, cloudwatchapi.OpGetMetricData, &cloudwatchapi.GetMetricDataInput{MetricDataQueries: []cloudwatchapi.MetricDataQuery{stat("a", "m", "Sum", 60)}, StartTime: now, EndTime: now})
		wantError(h.T, err, "InvalidParameterValue", "The parameter StartTime must be less than the parameter EndTime.")
		_, err = call(h, cloudwatchapi.OpGetMetricData, &cloudwatchapi.GetMetricDataInput{MetricDataQueries: []cloudwatchapi.MetricDataQuery{{Id: "e", Expression: new("SUM(METRICS())")}}, StartTime: now, EndTime: now.Add(time.Minute)})
		if err == nil || err.ID.Status != 501 {
			h.Errorf("an expression = %v, want 501 NotImplemented", err)
		}
		_, err = call(h, cloudwatchapi.OpGetMetricData, &cloudwatchapi.GetMetricDataInput{MetricDataQueries: []cloudwatchapi.MetricDataQuery{stat("p", "m", "p99", 60)}, StartTime: now, EndTime: now.Add(time.Minute)})
		if err == nil || err.ID.Status != 501 {
			h.Errorf("a percentile = %v, want 501 NotImplemented", err)
		}
		_, err = call(h, cloudwatchapi.OpGetMetricData, &cloudwatchapi.GetMetricDataInput{MetricDataQueries: []cloudwatchapi.MetricDataQuery{stat("a", "m", "Sum", 60), stat("a", "m", "Sum", 60)}, StartTime: now, EndTime: now.Add(time.Minute)})
		wantError(h.T, err, "InvalidParameterValue", "The value a for parameter MetricDataQueries.member.2.Id is not unique.")
		if err := get(stat("ok", "m", "Sum", 60)); err != nil {
			h.Errorf("a valid query = %v", err)
		}
		_, err = call(h, cloudwatchapi.OpGetMetricData, &cloudwatchapi.GetMetricDataInput{MetricDataQueries: []cloudwatchapi.MetricDataQuery{stat("a", "m", "Sum", 60)}, StartTime: now, EndTime: now.Add(time.Minute), NextToken: new("x")})
		wantError(h.T, err, "InvalidNextToken", "The specified NextToken is invalid.")
		// A page of no values could never advance.
		for _, n := range []int32{0, -1} {
			_, err = call(h, cloudwatchapi.OpGetMetricData, &cloudwatchapi.GetMetricDataInput{MetricDataQueries: []cloudwatchapi.MetricDataQuery{stat("a", "m", "Sum", 60)}, StartTime: now, EndTime: now.Add(time.Minute), MaxDatapoints: new(n)})
			wantError(h.T, err, "InvalidParameterValue", fmt.Sprintf("The value %d for parameter MaxDatapoints is invalid.", n))
		}
		for _, p := range []int32{1, 5, 10, 20, 30, 120} {
			if err := get(stat("ok", "m", "Sum", p)); err != nil {
				h.Errorf("period %d = %v", p, err)
			}
		}
		_, err = call(h, cloudwatchapi.OpGetMetricData, &cloudwatchapi.GetMetricDataInput{MetricDataQueries: []cloudwatchapi.MetricDataQuery{stat("a", "m", "Sum", 45)}, StartTime: now, EndTime: now.Add(time.Minute)})
		wantError(h.T, err, "InvalidParameterValue", "The value 45 for parameter MetricDataQueries.member.1.MetricStat.Period is invalid.")
		// A name that is no statistic is invalid, not unimplemented.
		_, err = call(h, cloudwatchapi.OpGetMetricData, &cloudwatchapi.GetMetricDataInput{MetricDataQueries: []cloudwatchapi.MetricDataQuery{stat("a", "m", "median", 60)}, StartTime: now, EndTime: now.Add(time.Minute)})
		wantError(h.T, err, "InvalidParameterValue", "The value median for parameter MetricDataQueries.member.1.MetricStat.Stat is invalid.")
		// An Id starts with a lowercase letter and holds letters, digits
		// and underscores (the MetricDataQuery documentation).
		for _, id := range []string{"Q1", "my-query", "1q", "_q"} {
			_, err = call(h, cloudwatchapi.OpGetMetricData, &cloudwatchapi.GetMetricDataInput{MetricDataQueries: []cloudwatchapi.MetricDataQuery{stat(id, "m", "Sum", 60)}, StartTime: now, EndTime: now.Add(time.Minute)})
			wantError(h.T, err, "InvalidParameterValue", "The value "+id+" for parameter MetricDataQueries.member.1.Id is invalid.")
		}
		if err := get(stat("q_1A", "m", "Sum", 60)); err != nil {
			h.Errorf("a valid Id = %v", err)
		}
		queries := make([]cloudwatchapi.MetricDataQuery, 501)
		for i := range queries {
			queries[i] = stat(fmt.Sprintf("q%d", i), "m", "Sum", 60)
		}
		_, err = call(h, cloudwatchapi.OpGetMetricData, &cloudwatchapi.GetMetricDataInput{MetricDataQueries: queries, StartTime: now, EndTime: now.Add(time.Minute)})
		wantError(h.T, err, "InvalidParameterValue", "The collection MetricDataQueries must not have a size greater than 500.")
		if err := get(queries[:500]...); err != nil {
			h.Errorf("500 queries = %v", err)
		}
		dimensioned := stat("a", "m", "Sum", 60)
		dimensioned.MetricStat.Metric.Dimensions = []cloudwatchapi.Dimension{{Name: "Env", Value: "prod"}, {Name: "Env\x01", Value: "prod"}}
		_, err = call(h, cloudwatchapi.OpGetMetricData, &cloudwatchapi.GetMetricDataInput{MetricDataQueries: []cloudwatchapi.MetricDataQuery{dimensioned}, StartTime: now, EndTime: now.Add(time.Minute)})
		wantError(h.T, err, "InvalidParameterValue", "The value Env\x01 for parameter MetricDataQueries.member.1.MetricStat.Metric.Dimensions.member.2.Name is invalid.")
	})
}

// A dimension name and value hold only printable ASCII and at least one
// non-space character, and a name does not start with a colon (the
// Dimension documentation).
func TestPutMetricDataDimensionContent(t *testing.T) {
	run(t, func(h *harness) {
		put := func(d cloudwatchapi.Dimension) *awserr.Error {
			_, err := call(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{
				Namespace: "ns", MetricData: []cloudwatchapi.MetricDatum{{MetricName: "m", Value: new(1.0), Dimensions: []cloudwatchapi.Dimension{d}}},
			})
			return err
		}
		for _, tc := range []struct {
			d            cloudwatchapi.Dimension
			param, value string
		}{
			{cloudwatchapi.Dimension{Name: "Service", Value: "café"}, "Value", "café"},
			{cloudwatchapi.Dimension{Name: "Ser\tvice", Value: "api"}, "Name", "Ser\tvice"},
			{cloudwatchapi.Dimension{Name: "Service", Value: "api\x7f"}, "Value", "api\x7f"},
			{cloudwatchapi.Dimension{Name: ":shard", Value: "1"}, "Name", ":shard"},
			{cloudwatchapi.Dimension{Name: "Env", Value: "   "}, "Value", "   "},
			{cloudwatchapi.Dimension{Name: "  ", Value: "prod"}, "Name", "  "},
		} {
			wantError(h.T, put(tc.d), "InvalidParameterValue", "The value "+tc.value+" for parameter MetricData.member.1.Dimensions.member.1."+tc.param+" is invalid.")
		}
		if got := must(h, cloudwatchapi.OpListMetrics, &cloudwatchapi.ListMetricsInput{}).Metrics; len(got) != 0 {
			h.Errorf("a refused request stored %+v", got)
		}
		// Spaces inside, and a colon anywhere in a value or past a name's
		// first character, are allowed.
		for _, d := range []cloudwatchapi.Dimension{{Name: " a:b ", Value: ":x y~"}, {Name: "~", Value: " ! "}} {
			if err := put(d); err != nil {
				h.Errorf("%+v = %s %q, want it accepted", d, err.ID.QueryCode, err.Message)
			}
		}
		// Any character outside printable ASCII refuses a valid value.
		rapid.Check(bubbleTB{h.T}, func(t *rapid.T) {
			value := rapid.StringMatching(`[!-~][ -~]{0,8}`).Draw(t, "value")
			bad := rapid.Rune().Filter(func(r rune) bool { return r < ' ' || r > '~' }).Draw(t, "bad")
			at := rapid.IntRange(0, len(value)).Draw(t, "at")
			d := cloudwatchapi.Dimension{Name: "Env", Value: value[:at] + string(bad) + value[at:]}
			if err := put(d); err == nil || err.ID.QueryCode != "InvalidParameterValue" {
				t.Fatalf("%q = %v, want InvalidParameterValue", d.Value, err)
			}
		})
	})
}

// GetMetricData pages, followed until NextToken is absent, concatenate per
// result to the unpaged answer.
func TestGetMetricDataPagesConcatenateToTheAnswer(t *testing.T) {
	run(t, func(h *harness) {
		metrics := 0
		rapid.Check(bubbleTB{h.T}, func(t *rapid.T) {
			metrics++
			name := fmt.Sprintf("m%d", metrics)
			start := h.now().Truncate(time.Minute)
			minutes := rapid.SliceOfN(rapid.IntRange(0, 9), 0, 12).Draw(t, "minutes")
			data := make([]cloudwatchapi.MetricDatum, len(minutes))
			for i, m := range minutes {
				data[i] = cloudwatchapi.MetricDatum{MetricName: name, Value: new(float64(i)), Timestamp: new(start.Add(time.Duration(m) * time.Minute))}
			}
			if len(data) > 0 {
				must(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{Namespace: "ns", MetricData: data})
			}
			n := rapid.IntRange(1, 3).Draw(t, "queries")
			queries := make([]cloudwatchapi.MetricDataQuery, n)
			for i := range queries {
				queries[i] = stat(fmt.Sprintf("q%d", i), name, rapid.SampledFrom([]string{"Sum", "Average", "Maximum"}).Draw(t, "stat"), 60)
			}
			in := cloudwatchapi.GetMetricDataInput{MetricDataQueries: queries, StartTime: start, EndTime: start.Add(10 * time.Minute)}
			whole := must(h, cloudwatchapi.OpGetMetricData, &in)
			in.MaxDatapoints = new(rapid.Int32Range(1, 5).Draw(t, "max"))
			got := make([][]float64, n)
			calls, done := len(minutes)*n+2, false
			for range calls {
				page := must(h, cloudwatchapi.OpGetMetricData, &in)
				for i, r := range page.MetricDataResults {
					got[i] = append(got[i], r.Values...)
				}
				if page.NextToken == nil {
					done = true
					break
				}
				in.NextToken = page.NextToken
			}
			if !done {
				t.Fatalf("paging did not terminate within %d calls", calls)
			}
			for i, r := range whole.MetricDataResults {
				if !slices.Equal(got[i], r.Values) {
					t.Fatalf("%s: pages of %d = %v, unpaged %v", *r.Id, *in.MaxDatapoints, got[i], r.Values)
				}
			}
		})
	})
}

func TestListMetrics(t *testing.T) {
	run(t, func(h *harness) {
		route := func(v string) []cloudwatchapi.Dimension { return []cloudwatchapi.Dimension{{Name: "Route", Value: v}} }
		must(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{Namespace: "app", MetricData: []cloudwatchapi.MetricDatum{
			{MetricName: "Latency", Value: new(1.0), Dimensions: route("/a")},
			{MetricName: "Latency", Value: new(1.0), Dimensions: route("/b")},
			{MetricName: "Errors", Value: new(1.0)},
		}})
		h.advance(4 * time.Hour)
		must(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{Namespace: "jobs", MetricData: []cloudwatchapi.MetricDatum{{MetricName: "Depth", Value: new(1.0)}}})
		list := func(in *cloudwatchapi.ListMetricsInput) []string {
			h.Helper()
			metrics := must(h, cloudwatchapi.OpListMetrics, in).Metrics
			out := make([]string, len(metrics))
			for i, m := range metrics {
				out[i] = *m.Namespace + "/" + *m.MetricName
				for _, d := range m.Dimensions {
					out[i] += " " + d.Name + "=" + d.Value
				}
				if m.Dimensions == nil {
					h.Errorf("%s: Dimensions absent, want a list", out[i])
				}
			}
			return out
		}
		for _, tc := range []struct {
			name string
			in   cloudwatchapi.ListMetricsInput
			want []string
		}{
			{"all", cloudwatchapi.ListMetricsInput{}, []string{"app/Errors", "app/Latency Route=/a", "app/Latency Route=/b", "jobs/Depth"}},
			{"namespace", cloudwatchapi.ListMetricsInput{Namespace: new("jobs")}, []string{"jobs/Depth"}},
			{"name", cloudwatchapi.ListMetricsInput{MetricName: new("Latency")}, []string{"app/Latency Route=/a", "app/Latency Route=/b"}},
			{"dimension name", cloudwatchapi.ListMetricsInput{Dimensions: []cloudwatchapi.DimensionFilter{{Name: "Route"}}}, []string{"app/Latency Route=/a", "app/Latency Route=/b"}},
			{"dimension value", cloudwatchapi.ListMetricsInput{Dimensions: []cloudwatchapi.DimensionFilter{{Name: "Route", Value: new("/b")}}}, []string{"app/Latency Route=/b"}},
			{"recently active", cloudwatchapi.ListMetricsInput{RecentlyActive: new(cloudwatchapi.RecentlyActivePt3h)}, []string{"jobs/Depth"}},
		} {
			if got := list(&tc.in); !slices.Equal(got, tc.want) {
				h.Errorf("%s = %v, want %v", tc.name, got, tc.want)
			}
		}
		_, err := call(h, cloudwatchapi.OpListMetrics, &cloudwatchapi.ListMetricsInput{NextToken: new("%%")})
		wantError(h.T, err, "InvalidParameterValue", "The specified NextToken is invalid.")
	})
}
