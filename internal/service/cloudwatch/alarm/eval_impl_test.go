package alarm_test

import (
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch/alarm"
)

// PutMetricAlarm validation rejects a single-statistic alarm without these
// fields, so none is ever stored; ConfigOf still reports one as not
// evaluable rather than computing a state from a zero value, since no
// state may be computed for an alarm understudy cannot evaluate (cwa 12.7).
func TestConfigOfWithoutRequiredFields(t *testing.T) {
	for _, tc := range []struct {
		why   string
		alter func(in *cloudwatchapi.PutMetricAlarmInput)
	}{
		{"no ComparisonOperator", func(in *cloudwatchapi.PutMetricAlarmInput) { in.ComparisonOperator = nil }},
		{"no Statistic", func(in *cloudwatchapi.PutMetricAlarmInput) { in.Statistic = nil }},
		{"no Period", func(in *cloudwatchapi.PutMetricAlarmInput) { in.Period = nil }},
		{"no EvaluationPeriods", func(in *cloudwatchapi.PutMetricAlarmInput) { in.EvaluationPeriods = nil }},
		{"no Threshold", func(in *cloudwatchapi.PutMetricAlarmInput) { in.Threshold = nil }},
		{"TreatMissingData bogus", func(in *cloudwatchapi.PutMetricAlarmInput) { in.TreatMissingData = new("bogus") }},
	} {
		in := storedAlarm()
		tc.alter(in)
		if _, nerr := alarm.ConfigOf(in); nerr == nil || nerr.Why != tc.why {
			t.Errorf("ConfigOf = %v, want Why %q", nerr, tc.why)
		}
	}
}

// Each treatment is read back from its wire value as sent (cwa 3.5).
func TestConfigOfKeepsTheTreatment(t *testing.T) {
	for _, want := range treatments {
		in := storedAlarm()
		in.TreatMissingData = new(want.String())
		cfg, nerr := alarm.ConfigOf(in)
		if nerr != nil || cfg.Treatment != want {
			t.Errorf("ConfigOf with %s = %v %v, want %v", want, cfg.Treatment, nerr, want)
		}
	}
}

// Case A reads the N newest real bins whichever treatment is set, and a
// tie at the threshold breaches only under the OrEqual operators
// (cwa 3.3, 3.5 case 2).
func TestEvaluateTiesAtTheThreshold(t *testing.T) {
	bins := make([]alarm.Bin, 5)
	for k := range bins {
		bins[k] = alarm.Value{Start: t0.Add(-time.Duration(k+1) * 5 * time.Minute), Value: 1, SampleCount: 1}
	}
	for _, tc := range []struct {
		cmp  alarm.Comparison
		want cloudwatchapi.StateValue
	}{
		{alarm.GreaterThan, stateOK},
		{alarm.GreaterThanOrEqual, stateAlarm},
		{alarm.LessThan, stateOK},
		{alarm.LessThanOrEqual, stateAlarm},
	} {
		cfg := vectorConfig(3, 3, alarm.TreatBreaching)
		cfg.Comparison = tc.cmp
		got, ok := alarm.Evaluate(cfg, slices.Clone(bins), stateInsufficient).(alarm.Transition)
		if !ok || got.To != tc.want || got.Evidence.Filled != 0 || len(got.Evidence.Used) != 3 {
			t.Errorf("%v at the threshold: %#v, want a Transition to %s over d1..d3", tc.cmp, got, tc.want)
		}
	}
}

// A warm-up asked about more periods than the range holds cannot have
// filled them (cwa 2.4; cloudwatch.alarm.warm-up-end).
func TestWarmUpOverShortRange(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.warm-up-end", "every-period-has-data")
	full := vectorBins(t, "0 0 0")
	if (alarm.WarmUp{Duration: time.Hour}).Over(time.Minute, full, 4) {
		t.Error("Over with n beyond the bins = true, want false")
	}
}

// 20 and 30 second periods evaluate every 10 seconds like 10 second ones,
// and an input in another zone gives the same UTC boundary (cwa 3.1;
// cloudwatch.alarm.evaluation-cadence, cloudwatch.alarm.window-anchor).
func TestNextSubMinuteAndZones(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.evaluation-cadence", "minute,10s-for-high-resolution,hourly-past-one-day")
	followsSwitch(t, "cloudwatch.alarm.window-anchor", "evaluation-minute")
	after := at(t, "2025-07-03 12:00:05.000")
	for _, p := range []int32{20, 30} {
		if got, want := (alarm.Config{Period: p, N: 3, M: 3}).Next(after), at(t, "2025-07-03 12:00:10.000"); !got.Equal(want) {
			t.Errorf("Period %d: Next = %v, want %v", p, got, want)
		}
	}
	zone := time.FixedZone("UTC+0530", 5*3600+30*60)
	long := alarm.Config{Period: 86400, N: 2, M: 2}
	got := long.Anchor(at(t, "2025-07-03 10:03:20.000").In(zone))
	if want := at(t, "2025-07-03 10:00:00.000"); !got.Equal(want) || got.Location() != time.UTC {
		t.Errorf("Anchor in UTC+05:30 = %v, want %v in UTC", got, want)
	}
}
