package alarm_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch/alarm"
)

// Numbers print as Java's Double.toString: a whole number keeps ".0",
// others the shortest round-trip digits; every value here is in a verbatim
// AWS sample (cwa 4.1, 4.2, 4.3: aws-doc, third-party-recorded, captured).
func TestFormatNumber(t *testing.T) {
	for _, tc := range []struct {
		v    float64
		want string
	}{
		{21, "21.0"},
		{0, "0.0"},
		{1062305, "1062305.0"},
		{50, "50.0"},
		{2, "2.0"},
		{21.5, "21.5"},
		{3.22, "3.22"},
		{32.6, "32.6"},
		{99.50160229693434, "99.50160229693434"},
		{0.0666851903306472, "0.0666851903306472"},
	} {
		if got := alarm.FormatNumber(tc.v); got != tc.want {
			t.Errorf("FormatNumber(%v) = %q, want %q", tc.v, got, tc.want)
		}
	}
}

// At or above 10^7 and below 10^-3 Java switches to its E notation with at
// least one fractional digit and no "+"
// (https://docs.oracle.com/en/java/javase/21/docs/api/java.base/java/lang/Double.html#toString(double));
// CloudWatch printed 1.23456789E7, 1.23E-4 and 5.0E-4 so
// (cloudwatch.alarm.number-format; CWA-09, recorded 2026-11-03:
// testdata/parity/cloudwatch/CWA-09).
func TestFormatNumberJavaExponent(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.number-format", "java-double")
	for _, tc := range []struct {
		v    float64
		want string
	}{
		{1.23456789e7, "1.23456789E7"},
		{0.000123, "1.23E-4"},
		{0.0005, "5.0E-4"},
		{1e7, "1.0E7"},
		{1e-4, "1.0E-4"},
		{-2.5e-5, "-2.5E-5"},
		{9999999, "9999999.0"},
		{0.001, "0.001"},
	} {
		if got := alarm.FormatNumber(tc.v); got != tc.want {
			t.Errorf("FormatNumber(%v) = %q, want %q", tc.v, got, tc.want)
		}
	}
}

// Dates inside a StateReason are dd/MM/yy HH:mm:ss in UTC, day first; JSON
// timestamps yyyy-MM-ddTHH:mm:ss.SSS+0000 (cwa 4.3, AWS's examples).
func TestFormatDates(t *testing.T) {
	eastern := time.FixedZone("UTC-4", -4*3600)
	start := time.Date(2019, 10, 2, 16, 59, 0, 0, time.UTC)
	for _, in := range []time.Time{start, start.In(eastern)} {
		if got := alarm.FormatReasonDate(in); got != "02/10/19 16:59:00" {
			t.Errorf("FormatReasonDate(%v) = %q, want 02/10/19 16:59:00", in, got)
		}
		if got := alarm.FormatTimestamp(in); got != "2019-10-02T16:59:00.000+0000" {
			t.Errorf("FormatTimestamp(%v) = %q, want 2019-10-02T16:59:00.000+0000", in, got)
		}
	}
	query := time.Date(2019, 10, 2, 17, 4, 40, 985_000_000, time.UTC)
	if got := alarm.FormatTimestamp(query.In(eastern)); got != "2019-10-02T17:04:40.985+0000" {
		t.Errorf("FormatTimestamp = %q, want 2019-10-02T17:04:40.985+0000", got)
	}
}

// used builds the Value bins of a reason sample: values newest first at
// the given starts.
func used(values []float64, starts ...time.Time) []alarm.Value {
	out := make([]alarm.Value, len(values))
	for i, v := range values {
		out[i] = alarm.Value{Start: starts[i], Value: v, SampleCount: 1}
	}
	return out
}

func binsOf(vs []alarm.Value) []alarm.Bin {
	out := make([]alarm.Bin, len(vs))
	for i, v := range vs {
		out[i] = v
	}
	return out
}

func utc(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

// reasonCase is a StateReason sample: the alarm, the transition and the
// text AWS wrote for it.
type reasonCase struct {
	name string
	cfg  alarm.Config
	tr   alarm.Transition
	want string
}

func reasonConfig(n, m int, mset bool, cmp alarm.Comparison, threshold float64) alarm.Config {
	return alarm.Config{Period: 300, N: n, M: m, MSet: mset, Comparison: cmp, Threshold: threshold, Treatment: alarm.TreatMissing, Statistic: cloudwatchapi.StatisticAverage}
}

func transition(to cloudwatchapi.StateValue, u []alarm.Value, filled, breaching int) alarm.Transition {
	return alarm.Transition{To: to, Evidence: alarm.Evidence{Bins: binsOf(u), Used: u, Filled: filled, Breaching: breaching}}
}

func checkReasons(t *testing.T, cases []reasonCase) {
	t.Helper()
	for i := range cases {
		tc := &cases[i]
		if got := alarm.Reason(tc.cfg, tc.tr); got != tc.want {
			t.Errorf("%s: Reason =\n%q\nwant\n%q", tc.name, got, tc.want)
		}
	}
}

// The StateReason texts AWS wrote, verbatim (cwa 4.1). The count form
// without DatapointsToAlarm and the M out of N form with it are the
// research's reading of which form AWS picks, cloudwatch.alarm.reason-format
// (UNVERIFIED, CWA-06); each text is a recorded, captured or documented
// sample of its form.
func TestReasonSamples(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.reason-format", "count-form-unless-datapoints-to-alarm")
	missing := func(n int) alarm.Transition {
		return alarm.Transition{To: stateInsufficient, Evidence: alarm.Evidence{Filled: n}}
	}
	breachingNoData := alarm.Transition{To: stateAlarm, Evidence: alarm.Evidence{Filled: 2}}
	threeOfThree := used([]float64{32.6, 33.8, 41.0}, utc(2025, 7, 3, 8, 29), utc(2025, 7, 3, 8, 24), utc(2025, 7, 3, 8, 19))
	checkReasons(t, []reasonCase{
		{
			"captured count form", // widdix/cloudwatch-alarm-to-slack fb66af9 handler.js:L59
			reasonConfig(1, 1, false, alarm.GreaterThan, 1),
			transition(stateAlarm, used([]float64{3.22}, utc(2017, 10, 29, 13, 20)), 0, 1),
			"Threshold Crossed: 1 datapoint [3.22 (29/10/17 13:20:00)] was greater than the threshold (1.0).",
		},
		{
			"captured greater than or equal to", // alerta/alerta 4ac0106 tests/test_webhooks.py:L70
			reasonConfig(1, 1, false, alarm.GreaterThanOrEqual, 0),
			transition(stateAlarm, used([]float64{1062305}, utc(2019, 2, 14, 23, 53)), 0, 1),
			"Threshold Crossed: 1 datapoint [1062305.0 (14/02/19 23:53:00)] was greater than or equal to the threshold (0.0).",
		},
		{
			"captured not breaching", // terraform-aws-notify-slack a7765ac functions/messages/cloudwatch_alarm.json:L11
			reasonConfig(1, 1, false, alarm.LessThan, 1),
			transition(stateOK, used([]float64{1}, utc(2019, 2, 12, 15, 44)), 0, 0),
			"Threshold Crossed: 1 datapoint [1.0 (12/02/19 15:44:00)] was not less than the threshold (1.0).",
		},
		{
			"aws-doc M out of N breaching", // cloudwatch-and-eventbridge
			reasonConfig(1, 1, true, alarm.GreaterThan, 50),
			transition(stateAlarm, used([]float64{99.50160229693434}, utc(2019, 10, 2, 16, 59)), 0, 1),
			"Threshold Crossed: 1 out of the last 1 datapoints [99.50160229693434 (02/10/19 16:59:00)] was greater than the threshold (50.0) (minimum 1 datapoint for OK -> ALARM transition).",
		},
		{
			"aws-doc M out of N not breaching", // cloudwatch-and-eventbridge
			reasonConfig(1, 1, true, alarm.GreaterThan, 50),
			transition(stateOK, used([]float64{0.0666851903306472}, utc(2019, 10, 1, 13, 46)), 0, 0),
			"Threshold Crossed: 1 out of the last 1 datapoints [0.0666851903306472 (01/10/19 13:46:00)] was not greater than the threshold (50.0) (minimum 1 datapoint for ALARM -> OK transition).",
		},
		{
			// alarm-actions prints the head; the plural minimum is the
			// alarms-and-actions-Lambda log alarm's wording.
			"aws-doc 3 out of 3",
			reasonConfig(3, 3, true, alarm.GreaterThan, 31),
			transition(stateAlarm, threeOfThree, 0, 3),
			"Threshold Crossed: 3 out of the last 3 datapoints [32.6 (03/07/25 08:29:00), 33.8 (03/07/25 08:24:00), 41.0 (03/07/25 08:19:00)] were greater than the threshold (31.0) (minimum 3 datapoints for OK -> ALARM transition).",
		},
		{
			// localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L1993
			"recorded no data breaching",
			alarm.Config{Period: 60, N: 2, M: 2, Comparison: alarm.GreaterThan, Threshold: 1, Treatment: alarm.TreatBreaching, Statistic: cloudwatchapi.StatisticAverage},
			breachingNoData,
			"Threshold Crossed: no datapoints were received for 2 periods and 2 missing datapoints were treated as [Breaching].",
		},
		{
			"aws-doc insufficient data", // alarms-and-actions-Lambda
			reasonConfig(5, 5, false, alarm.GreaterThan, 5),
			missing(5),
			"Insufficient Data: 5 datapoints were unknown.",
		},
		{
			"captured insufficient data", // alerta/alerta 4ac0106 tests/test_webhooks.py:L85
			reasonConfig(10, 10, false, alarm.GreaterThan, 5),
			missing(10),
			"Insufficient Data: 10 datapoints were unknown.",
		},
	})
}

// Forms the samples imply by the rules cwa 4.1 reads from them (plural
// "datapoints" and "were", "not " before the phrase, the minimum
// N - M + 1 for ALARM -> OK) and the no-data and filled texts CWA-04 and
// CWA-08 recorded, all under cloudwatch.alarm.reason-format.
func TestReasonForms(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.reason-format", "count-form-unless-datapoints-to-alarm")
	d := func(k int) time.Time { return t0.Add(-time.Duration(k) * 5 * time.Minute) }
	checkReasons(t, []reasonCase{
		{
			"count form plural",
			reasonConfig(3, 3, false, alarm.GreaterThan, 31),
			transition(stateAlarm, used([]float64{32.6, 33.8, 41.0}, d(1), d(2), d(3)), 0, 3),
			"Threshold Crossed: 3 datapoints [32.6 (03/07/25 08:29:00), 33.8 (03/07/25 08:24:00), 41.0 (03/07/25 08:19:00)] were greater than the threshold (31.0).",
		},
		{
			// Of three counted, the two not breaching are listed.
			"M out of N to OK lists the non-breaching",
			reasonConfig(3, 2, true, alarm.GreaterThan, 1),
			transition(stateOK, used([]float64{0, 2, 0.5}, d(1), d(2), d(3)), 0, 1),
			"Threshold Crossed: 2 out of the last 3 datapoints [0.0 (03/07/25 08:29:00), 0.5 (03/07/25 08:19:00)] were not greater than the threshold (1.0) (minimum 2 datapoints for ALARM -> OK transition).",
		},
		{
			"singular no data breaching",
			alarm.Config{Period: 60, N: 1, M: 1, Comparison: alarm.GreaterThan, Threshold: 1, Treatment: alarm.TreatBreaching, Statistic: cloudwatchapi.StatisticAverage},
			alarm.Transition{To: stateAlarm, Evidence: alarm.Evidence{Filled: 1}},
			"Threshold Crossed: no datapoints were received for 1 period and 1 missing datapoint was treated as [Breaching].",
		},
		{
			"no data notBreaching",
			alarm.Config{Period: 60, N: 2, M: 2, Comparison: alarm.GreaterThan, Threshold: 1, Treatment: alarm.TreatNotBreaching, Statistic: cloudwatchapi.StatisticAverage},
			alarm.Transition{To: stateOK, Evidence: alarm.Evidence{Filled: 2}},
			"Threshold Crossed: no datapoints were received for 2 periods and 2 missing datapoints were treated as [NonBreaching].",
		},
		{
			"singular insufficient data",
			reasonConfig(1, 1, false, alarm.GreaterThan, 1),
			alarm.Transition{To: stateInsufficient, Evidence: alarm.Evidence{Filled: 1}},
			"Insufficient Data: 1 datapoint was unknown.",
		},
		{
			// cwa 3.6 table 2 row 3 under breaching: the real bin is
			// listed and the filled one counted after the threshold
			// (testdata/parity/cloudwatch/CWA-04/t2r3-breaching-history.json).
			"case C lists real bins and counts filled ones",
			alarm.Config{Period: 300, N: 3, M: 2, MSet: true, Comparison: alarm.GreaterThan, Threshold: 1, Treatment: alarm.TreatBreaching, Statistic: cloudwatchapi.StatisticAverage},
			transition(stateAlarm, used([]float64{2, 0}, d(3), d(5)), 1, 1),
			"Threshold Crossed: 1 out of the last 3 datapoints [2.0 (03/07/25 08:19:00)] was greater than the threshold (1.0) and 1 missing datapoint was treated as [Breaching] (minimum 2 datapoints for OK -> ALARM transition).",
		},
		{
			// cwa 3.6 table 2 row 4 under breaching: no real bin breaches,
			// so the text counts the received ones and the filled points
			// (testdata/parity/cloudwatch/CWA-04/t2r4-breaching-history.json).
			"case C with nothing to list",
			alarm.Config{Period: 300, N: 3, M: 2, MSet: true, Comparison: alarm.GreaterThan, Threshold: 1, Treatment: alarm.TreatBreaching, Statistic: cloudwatchapi.StatisticAverage},
			transition(stateAlarm, used([]float64{0}, d(1)), 2, 0),
			"Threshold Crossed: 1 datapoint was received for 3 periods and 2 missing datapoints were treated as [Breaching].",
		},
	})
}

// StateReasonData for an evaluation with no data is AWS's Lambda example
// verbatim: no startDate or unit, recentDatapoints empty and the N newest
// bins as bare timestamps (cwa 4.2, aws-doc alarms-and-actions-Lambda).
func TestReasonDataWithoutData(t *testing.T) {
	cfg := alarm.Config{Period: 60, N: 5, M: 5, Comparison: alarm.GreaterThan, Threshold: 5, Treatment: alarm.TreatMissing, Statistic: cloudwatchapi.StatisticAverage}
	anchor := time.Date(2023, 8, 4, 12, 31, 0, 0, time.UTC)
	bins := make([]alarm.Bin, cfg.Range())
	for k := range bins {
		bins[k] = alarm.Missing{Start: anchor.Add(-time.Duration(k+1) * time.Minute)}
	}
	tr := alarm.Transition{To: stateInsufficient, Evidence: alarm.Evidence{Bins: bins, Filled: 5}}
	got := alarm.ReasonData(cfg, tr, time.Date(2023, 8, 4, 12, 31, 29, 591_000_000, time.UTC))
	want := `{"version":"1.0","queryDate":"2023-08-04T12:31:29.591+0000","statistic":"Average","period":60,"recentDatapoints":[],"threshold":5.0,` +
		`"evaluatedDatapoints":[{"timestamp":"2023-08-04T12:30:00.000+0000"},{"timestamp":"2023-08-04T12:29:00.000+0000"},{"timestamp":"2023-08-04T12:28:00.000+0000"},{"timestamp":"2023-08-04T12:27:00.000+0000"},{"timestamp":"2023-08-04T12:26:00.000+0000"}]}`
	if got != want {
		t.Errorf("ReasonData =\n%s\nwant\n%s", got, want)
	}
}

// StateReasonData for an evaluation with data has the keys, order and
// number forms AWS recorded (localstack@8b9a79f
// tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L6408-L6426,
// cwa 4.2); the recording masks dates, so these are chosen.
func TestReasonDataWithData(t *testing.T) {
	unit := cloudwatchapi.StandardUnitSeconds
	cfg := alarm.Config{Period: 10, N: 1, M: 1, Comparison: alarm.GreaterThan, Threshold: 21, Treatment: alarm.TreatIgnore, Statistic: cloudwatchapi.StatisticAverage, Unit: &unit}
	start := time.Date(2026, 2, 26, 9, 59, 50, 0, time.UTC)
	d1 := alarm.Value{Start: start, Value: 21.5, SampleCount: 2}
	bins := []alarm.Bin{d1, alarm.Missing{Start: start.Add(-10 * time.Second)}, alarm.Missing{Start: start.Add(-20 * time.Second)}}
	tr := alarm.Transition{To: stateAlarm, Evidence: alarm.Evidence{Bins: bins, Used: []alarm.Value{d1}, Breaching: 1}}
	got := alarm.ReasonData(cfg, tr, time.Date(2026, 2, 26, 10, 0, 0, 120_000_000, time.UTC))
	want := `{"version":"1.0","queryDate":"2026-02-26T10:00:00.120+0000","startDate":"2026-02-26T09:59:50.000+0000","unit":"Seconds","statistic":"Average","period":10,` +
		`"recentDatapoints":[21.5],"threshold":21.0,"evaluatedDatapoints":[{"timestamp":"2026-02-26T09:59:50.000+0000","sampleCount":2.0,"value":21.5}]}`
	if got != want {
		t.Errorf("ReasonData =\n%s\nwant\n%s", got, want)
	}
}

// recentDatapoints lists the used values oldest first and startDate is the
// oldest one's start: AWS's alarm-actions example pairs the reason
// [32.6 (08:29), 33.8 (08:24), 41.0 (08:19)] with recentDatapoints
// [41, 33.8, 32.6] and startDate 08:19 (cwa 4.2, aws-doc).
func TestReasonDataOrder(t *testing.T) {
	cfg := reasonConfig(3, 3, true, alarm.GreaterThan, 31)
	u := used([]float64{32.6, 33.8, 41.0}, utc(2025, 7, 3, 8, 29), utc(2025, 7, 3, 8, 24), utc(2025, 7, 3, 8, 19))
	bins := append(binsOf(u), alarm.Missing{Start: utc(2025, 7, 3, 8, 14)}, alarm.Missing{Start: utc(2025, 7, 3, 8, 9)})
	tr := alarm.Transition{To: stateAlarm, Evidence: alarm.Evidence{Bins: bins, Used: u, Breaching: 3}}
	var got struct {
		StartDate        string    `json:"startDate"`
		RecentDatapoints []float64 `json:"recentDatapoints"`
		Unit             *string   `json:"unit"`
	}
	data := alarm.ReasonData(cfg, tr, time.Date(2025, 7, 3, 8, 34, 6, 300_000_000, time.UTC))
	if err := json.Unmarshal([]byte(data), &got); err != nil {
		t.Fatalf("ReasonData is not JSON: %v: %s", err, data)
	}
	if got.StartDate != "2025-07-03T08:19:00.000+0000" || !reflect.DeepEqual(got.RecentDatapoints, []float64{41, 33.8, 32.6}) {
		t.Errorf("startDate %s, recentDatapoints %v; want 2025-07-03T08:19:00.000+0000, [41 33.8 32.6]", got.StartDate, got.RecentDatapoints)
	}
	// unit is absent for an alarm without one (localstack@8b9a79f
	// tests/aws/services/cloudwatch/test_cloudwatch_metrics.snapshot.json:L123-L138).
	if got.Unit != nil || strings.Contains(data, `"unit"`) {
		t.Errorf("ReasonData of an alarm without a Unit carries unit: %s", data)
	}
}
