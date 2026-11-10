package cloudwatch

import (
	"reflect"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch/alarm"
)

// binsAlarm is an evaluable alarm on ns/Load with dimensions B=2, A=1:
// period seconds, N = 3, so a range of 5 (cloudwatch.alarm.evaluation-range).
func binsAlarm(period int32, stat cloudwatchapi.Statistic, unit *cloudwatchapi.StandardUnit) *cloudwatchapi.PutMetricAlarmInput {
	return &cloudwatchapi.PutMetricAlarmInput{
		AlarmName: "a", Namespace: new("ns"), MetricName: new("Load"),
		Dimensions: []cloudwatchapi.Dimension{{Name: "B", Value: "2"}, {Name: "A", Value: "1"}},
		Statistic:  new(stat), Period: new(period), Unit: unit, EvaluationPeriods: new(int32(3)),
		Threshold: new(10.0), ComparisonOperator: new(cloudwatchapi.ComparisonOperatorGreaterThanThreshold),
	}
}

func binsState(points ...Point) *State {
	st := newState(aws.Scope{})
	dims := []Dimension{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}}
	st.Series[seriesKey("ns", "Load", dims)] = Series{Namespace: "ns", Name: "Load", Dimensions: dims, Points: points}
	return &st
}

func at(t *testing.T, s string) clock.Instant {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return clock.FromWire(v)
}

// pt is a point of one value v at ts.
func pt(t *testing.T, ts, unit string, v float64) Point {
	t.Helper()
	return Point{At: at(t, ts), Unit: unit, Stats: Stats{SampleCount: 1, Sum: v, Minimum: v, Maximum: v}}
}

func binsOf(t *testing.T, in *cloudwatchapi.PutMetricAlarmInput, st *State, evalAt string) []alarm.Bin {
	t.Helper()
	cfg, err := alarm.ConfigOf(in)
	if err != nil {
		t.Fatal(err)
	}
	return alarmBins(st, in, cfg, at(t, evalAt))
}

// The bins of an evaluation at 12:31:29 with a 60-second period and N = 3
// are the eight minutes (cloudwatch.alarm.evaluation-range, CWA-01) before
// T0 = 12:31, newest first, each starting at its bin's start
// (cloudwatch-alarms.md 3.2: the alarms-and-actions-Lambda row);
// points in one bin combine into the alarm's statistic, and points at or
// after T0 or before the range are not read.
func TestAlarmBinsWindow(t *testing.T) {
	st := binsState(
		pt(t, "2023-08-04T12:22:00Z", "None", 100), // before the range
		pt(t, "2023-08-04T12:28:00Z", "None", 1),
		pt(t, "2023-08-04T12:30:00Z", "None", 2),
		pt(t, "2023-08-04T12:30:00Z", "None", 3),
		pt(t, "2023-08-04T12:31:00Z", "None", 100), // at T0, outside
	)
	got := binsOf(t, binsAlarm(60, cloudwatchapi.StatisticSum, nil), st, "2023-08-04T12:31:29.591Z")
	start := func(s string) time.Time { return at(t, s).Wire() }
	want := []alarm.Bin{
		alarm.Value{Start: start("2023-08-04T12:30:00Z"), Value: 5, SampleCount: 2},
		alarm.Missing{Start: start("2023-08-04T12:29:00Z")},
		alarm.Value{Start: start("2023-08-04T12:28:00Z"), Value: 1, SampleCount: 1},
		alarm.Missing{Start: start("2023-08-04T12:27:00Z")},
		alarm.Missing{Start: start("2023-08-04T12:26:00Z")},
		alarm.Missing{Start: start("2023-08-04T12:25:00Z")},
		alarm.Missing{Start: start("2023-08-04T12:24:00Z")},
		alarm.Missing{Start: start("2023-08-04T12:23:00Z")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bins = %+v\nwant %+v", got, want)
	}
}

// A 300-second period is anchored at the evaluation minute, not at a
// multiple of five minutes: an evaluation at 08:34:06.300 reads d1 from
// 08:29, d2 from 08:24, d3 from 08:19 (cloudwatch-alarms.md 3.2, the
// alarm-actions row; cloudwatch.alarm.window-anchor).
func TestAlarmBinsAnchor(t *testing.T) {
	st := binsState(pt(t, "2025-07-03T08:33:00Z", "None", 7), pt(t, "2025-07-03T08:24:00Z", "None", 4))
	got := binsOf(t, binsAlarm(300, cloudwatchapi.StatisticMaximum, nil), st, "2025-07-03T08:34:06.300Z")
	starts := []string{"08:29", "08:24", "08:19", "08:14", "08:09"}
	if len(got) != len(starts) {
		t.Fatalf("%d bins, want %d", len(got), len(starts))
	}
	for k, s := range starts {
		var gotStart time.Time
		switch b := got[k].(type) {
		case alarm.Value:
			gotStart = b.Start
		case alarm.Missing:
			gotStart = b.Start
		}
		if want := at(t, "2025-07-03T"+s+":00Z").Wire(); !gotStart.Equal(want) {
			t.Errorf("d%d starts %s, want %s", k+1, gotStart, want)
		}
	}
	if v, ok := got[0].(alarm.Value); !ok || v.Value != 7 {
		t.Errorf("d1 = %+v, want Value 7", got[0])
	}
	if v, ok := got[1].(alarm.Value); !ok || v.Value != 4 {
		t.Errorf("d2 = %+v, want Value 4", got[1])
	}
}

// With a Unit the alarm reads that unit's points alone; without one, of
// points in several units it reads the first unit by name
// (cloudwatch-alarms.md 3.3, cloudwatch.alarm.mixed-units).
func TestAlarmBinsUnits(t *testing.T) {
	st := binsState(pt(t, "2023-08-04T12:30:00Z", "Seconds", 1), pt(t, "2023-08-04T12:30:00Z", "Count", 5))
	for _, tc := range []struct {
		name string
		unit *cloudwatchapi.StandardUnit
		want float64
	}{
		{"unit Seconds", new(cloudwatchapi.StandardUnitSeconds), 1},
		{"unit Count", new(cloudwatchapi.StandardUnitCount), 5},
		{"no unit: Count, the first by name", nil, 5},
	} {
		got := binsOf(t, binsAlarm(60, cloudwatchapi.StatisticSum, tc.unit), st, "2023-08-04T12:31:00Z")
		if v, ok := got[0].(alarm.Value); !ok || v.Value != tc.want || v.SampleCount != 1 {
			t.Errorf("%s: d1 = %+v, want Value %v of one sample", tc.name, got[0], tc.want)
		}
	}
	got := binsOf(t, binsAlarm(60, cloudwatchapi.StatisticSum, new(cloudwatchapi.StandardUnitBytes)), st, "2023-08-04T12:31:00Z")
	if _, ok := got[0].(alarm.Missing); !ok {
		t.Errorf("a unit never put: d1 = %+v, want Missing (cloudwatch-alarms.md 3.3)", got[0])
	}
}

// A bin whose statistic has no value, the Average of a statistic set of no
// samples, is Missing (as GetMetricData leaves it out); a
// metric nothing was put to reads only Missing bins (cloudwatch-alarms.md
// 12.7).
func TestAlarmBinsMissing(t *testing.T) {
	st := binsState(Point{At: at(t, "2023-08-04T12:30:00Z"), Unit: "None", Stats: Stats{}})
	got := binsOf(t, binsAlarm(60, cloudwatchapi.StatisticAverage, nil), st, "2023-08-04T12:31:00Z")
	if _, ok := got[0].(alarm.Missing); !ok {
		t.Errorf("Average of no samples: d1 = %+v, want Missing", got[0])
	}
	other := binsAlarm(60, cloudwatchapi.StatisticSum, nil)
	other.Namespace = new("AWS/SQS")
	for k, b := range binsOf(t, other, st, "2023-08-04T12:31:00Z") {
		if _, ok := b.(alarm.Missing); !ok {
			t.Errorf("AWS/SQS d%d = %+v, want Missing", k+1, b)
		}
	}
}
