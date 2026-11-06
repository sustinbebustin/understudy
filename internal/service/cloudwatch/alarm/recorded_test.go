package alarm_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch/alarm"
)

// The evaluation range AWS used, read from when a point left it: R = N + 5
// for a 60-second period (N = 1 and N = 3) and N + 2 for a 300-second one
// (N = 1 recorded, N = 3 documented) (cwa 3.4; CWA-01, recorded
// 2026-11-03: testdata/parity/cloudwatch/CWA-01).
func TestRangeRecorded(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.evaluation-range", "n-plus-5-for-60s,n-plus-2-otherwise")
	for _, tc := range []struct {
		period    int32
		n, wantR  int
		recording string
	}{
		{60, 1, 6, "CWA-01 r1"},
		{60, 3, 8, "CWA-01 r3"},
		{300, 1, 3, "CWA-01 r5"},
		{300, 3, 5, "cwa 3.4, aws-doc"},
	} {
		if got := (alarm.Config{Period: tc.period, N: tc.n, M: tc.n}).Range(); got != tc.wantR {
			t.Errorf("Range() for Period %d, N = %d = %d, want %d (%s)", tc.period, tc.n, got, tc.wantR, tc.recording)
		}
	}
}

// recordedBins reads marks, oldest first and space-separated ("-" a missing
// bin, otherwise the bin's Maximum of one sample), into bins newest first
// ending at t0, padded with missing bins to r.
func recordedBins(t *testing.T, t0 time.Time, period time.Duration, r int, marks string) []alarm.Bin {
	t.Helper()
	fields := strings.Fields(marks)
	if len(fields) > r {
		t.Fatalf("%d marks in %q exceed the range %d", len(fields), marks, r)
	}
	bins := make([]alarm.Bin, r)
	for k := range bins {
		bins[k] = alarm.Missing{Start: t0.Add(-time.Duration(k+1) * period)}
	}
	for i, f := range fields {
		k := len(fields) - 1 - i
		if f == "-" {
			continue
		}
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			t.Fatalf("mark %q in %q is neither - nor a number", f, marks)
		}
		bins[k] = alarm.Value{Start: t0.Add(-time.Duration(k+1) * period), Value: v, SampleCount: 1}
	}
	return bins
}

// recordedCase is one transition AWS recorded: the alarm (the recording
// kit's cwalarm: Maximum, GreaterThanThreshold 3), the bins it evaluated
// (point timestamps and recentDatapoints place them), the state it left,
// and the StateReason and StateReasonData of its first StateUpdate item,
// verbatim.
type recordedCase struct {
	fixture    string
	cfg        alarm.Config
	bins       string // marks, oldest first
	evaluation string // queryDate
	from, to   cloudwatchapi.StateValue
	reason     string
	data       string
}

func recordedConfig(period int32, n, m int, mset bool, treatment alarm.Treatment) alarm.Config {
	return alarm.Config{
		Period: period, N: n, M: m, MSet: mset, Comparison: alarm.GreaterThan, Threshold: 3,
		Treatment: treatment, Statistic: cloudwatchapi.StatisticMaximum,
	}
}

// The texts AWS wrote for the transitions the recording kit staged (cwa 4.1,
// 4.2; recorded 2026-11-03 under testdata/parity/cloudwatch): which bins
// evaluatedDatapoints lists, recentDatapoints' nulls, the "received for"
// form and the filled-points clause of the M out of N form.
func TestReasonRecorded(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.reason-format", "count-form-unless-datapoints-to-alarm")
	for _, tc := range []recordedCase{
		{
			"CWA-01/r3-history.json", recordedConfig(60, 3, 3, true, alarm.TreatNotBreaching),
			"5 5 - - - - - -", "2026-11-03T03:14:23.549Z", stateAlarm, stateOK,
			"Threshold Crossed: 2 datapoints were received for 3 periods and 1 missing datapoint was treated as [NonBreaching].",
			`{"version":"1.0","queryDate":"2026-11-03T03:14:23.549+0000","startDate":"2026-11-03T03:06:00.000+0000","statistic":"Maximum","period":60,"recentDatapoints":[5.0,5.0],"threshold":3.0,"evaluatedDatapoints":[{"timestamp":"2026-11-03T03:13:00.000+0000"}]}`,
		},
		{
			"CWA-01/r1-history.json", recordedConfig(60, 1, 1, false, alarm.TreatMissing),
			"- - - - - -", "2026-11-03T03:14:48.132Z", stateAlarm, stateInsufficient,
			"Insufficient Data: 1 datapoint was unknown.",
			`{"version":"1.0","queryDate":"2026-11-03T03:14:48.132+0000","statistic":"Maximum","period":60,"recentDatapoints":[],"threshold":3.0,"evaluatedDatapoints":[{"timestamp":"2026-11-03T03:13:00.000+0000"}]}`,
		},
		{
			"CWA-04/t2r3-missing-history.json", recordedConfig(300, 3, 2, true, alarm.TreatMissing),
			"1 - 5 - -", "2026-11-03T03:15:46.309Z", stateInsufficient, stateOK,
			"Threshold Crossed: 1 out of the last 3 datapoints [1.0 (03/11/26 02:50:00)] was not greater than the threshold (3.0) (minimum 2 datapoints for ALARM -> OK transition).",
			`{"version":"1.0","queryDate":"2026-11-03T03:15:46.309+0000","startDate":"2026-11-03T02:50:00.000+0000","statistic":"Maximum","period":300,"recentDatapoints":[1.0,null,5.0],"threshold":3.0,"evaluatedDatapoints":[{"timestamp":"2026-11-03T03:10:00.000+0000"},{"timestamp":"2026-11-03T02:50:00.000+0000","sampleCount":1.0,"value":1.0}]}`,
		},
		{
			"CWA-04/t1r2-breaching-history.json", recordedConfig(300, 3, 3, true, alarm.TreatBreaching),
			"1 - - - -", "2026-11-03T03:15:40.659Z", stateInsufficient, stateOK,
			"Threshold Crossed: 1 out of the last 3 datapoints [1.0 (03/11/26 02:50:00)] was not greater than the threshold (3.0) and 2 missing datapoints were treated as [Breaching] (minimum 1 datapoint for ALARM -> OK transition).",
			`{"version":"1.0","queryDate":"2026-11-03T03:15:40.659+0000","startDate":"2026-11-03T02:50:00.000+0000","statistic":"Maximum","period":300,"recentDatapoints":[1.0],"threshold":3.0,"evaluatedDatapoints":[{"timestamp":"2026-11-03T02:50:00.000+0000","sampleCount":1.0,"value":1.0}]}`,
		},
		{
			"CWA-04/t1r2-notBreaching-history.json", recordedConfig(300, 3, 3, true, alarm.TreatNotBreaching),
			"1 - - - -", "2026-11-03T03:14:56.140Z", stateInsufficient, stateOK,
			"Threshold Crossed: 1 out of the last 3 datapoints [1.0 (03/11/26 02:49:00)] was not greater than the threshold (3.0) and 2 missing datapoints were treated as [NonBreaching] (minimum 1 datapoint for ALARM -> OK transition).",
			`{"version":"1.0","queryDate":"2026-11-03T03:14:56.140+0000","startDate":"2026-11-03T02:49:00.000+0000","statistic":"Maximum","period":300,"recentDatapoints":[1.0],"threshold":3.0,"evaluatedDatapoints":[{"timestamp":"2026-11-03T02:49:00.000+0000","sampleCount":1.0,"value":1.0}]}`,
		},
		{
			"CWA-04/t2r4-breaching-history.json", recordedConfig(300, 3, 2, true, alarm.TreatBreaching),
			"- - - - 1", "2026-11-03T03:15:29.375Z", stateInsufficient, stateAlarm,
			"Threshold Crossed: 1 datapoint was received for 3 periods and 2 missing datapoints were treated as [Breaching].",
			`{"version":"1.0","queryDate":"2026-11-03T03:15:29.375+0000","startDate":"2026-11-03T03:10:00.000+0000","statistic":"Maximum","period":300,"recentDatapoints":[1.0],"threshold":3.0,"evaluatedDatapoints":[{"timestamp":"2026-11-03T03:05:00.000+0000"},{"timestamp":"2026-11-03T03:00:00.000+0000"}]}`,
		},
		{
			"CWA-06/f-m2of3-history.json", recordedConfig(60, 3, 2, true, alarm.TreatMissing),
			"5 1 6 -", "2026-11-03T03:15:05.386Z", stateInsufficient, stateAlarm,
			"Threshold Crossed: 2 out of the last 3 datapoints [6.0 (03/11/26 03:13:00), 5.0 (03/11/26 03:11:00)] were greater than the threshold (3.0) (minimum 2 datapoints for OK -> ALARM transition).",
			`{"version":"1.0","queryDate":"2026-11-03T03:15:05.386+0000","startDate":"2026-11-03T03:11:00.000+0000","statistic":"Maximum","period":60,"recentDatapoints":[5.0,1.0,6.0],"threshold":3.0,"evaluatedDatapoints":[{"timestamp":"2026-11-03T03:13:00.000+0000","sampleCount":1.0,"value":6.0},{"timestamp":"2026-11-03T03:11:00.000+0000","sampleCount":1.0,"value":5.0}]}`,
		},
		{
			"CWA-08/nd-p3-history.json", recordedConfig(60, 3, 3, false, alarm.TreatBreaching),
			"5 -", "2026-11-03T03:15:25.813Z", stateInsufficient, stateAlarm,
			"Threshold Crossed: 1 datapoint [5.0 (03/11/26 03:13:00)] was greater than the threshold (3.0) and 2 missing datapoints were treated as [Breaching].",
			`{"version":"1.0","queryDate":"2026-11-03T03:15:25.813+0000","startDate":"2026-11-03T03:13:00.000+0000","statistic":"Maximum","period":60,"recentDatapoints":[5.0],"threshold":3.0,"evaluatedDatapoints":[{"timestamp":"2026-11-03T03:14:00.000+0000"},{"timestamp":"2026-11-03T03:13:00.000+0000","sampleCount":1.0,"value":5.0},{"timestamp":"2026-11-03T03:12:00.000+0000"}]}`,
		},
		{
			"CWA-08/nd-n2-history.json", recordedConfig(60, 2, 2, false, alarm.TreatNotBreaching),
			"", "2026-11-03T03:15:40.453Z", stateInsufficient, stateOK,
			"Threshold Crossed: no datapoints were received for 2 periods and 2 missing datapoints were treated as [NonBreaching].",
			`{"version":"1.0","queryDate":"2026-11-03T03:15:40.453+0000","statistic":"Maximum","period":60,"recentDatapoints":[],"threshold":3.0,"evaluatedDatapoints":[{"timestamp":"2026-11-03T03:14:00.000+0000"}]}`,
		},
		{
			"CWA-10/gap3-history.json", recordedConfig(60, 3, 3, true, alarm.TreatMissing),
			"5 - 5 - 5 -", "2026-11-03T03:15:15.665Z", stateInsufficient, stateAlarm,
			"Threshold Crossed: 3 out of the last 3 datapoints [5.0 (03/11/26 03:13:00), 5.0 (03/11/26 03:11:00), 5.0 (03/11/26 03:09:00)] were greater than the threshold (3.0) (minimum 3 datapoints for OK -> ALARM transition).",
			`{"version":"1.0","queryDate":"2026-11-03T03:15:15.665+0000","startDate":"2026-11-03T03:09:00.000+0000","statistic":"Maximum","period":60,"recentDatapoints":[5.0,null,5.0,null,5.0],"threshold":3.0,"evaluatedDatapoints":[{"timestamp":"2026-11-03T03:13:00.000+0000","sampleCount":1.0,"value":5.0},{"timestamp":"2026-11-03T03:11:00.000+0000","sampleCount":1.0,"value":5.0},{"timestamp":"2026-11-03T03:09:00.000+0000","sampleCount":1.0,"value":5.0}]}`,
		},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			at, err := time.Parse(time.RFC3339Nano, tc.evaluation)
			if err != nil {
				t.Fatal(err)
			}
			period := time.Duration(tc.cfg.Period) * time.Second
			bins := recordedBins(t, tc.cfg.Anchor(at), period, tc.cfg.Range(), tc.bins)
			tr, ok := alarm.Evaluate(tc.cfg, bins, tc.from).(alarm.Transition)
			if !ok || tr.To != tc.to {
				t.Fatalf("Evaluate from %s = %#v, want a Transition to %s", tc.from, alarm.Evaluate(tc.cfg, bins, tc.from), tc.to)
			}
			if got := alarm.Reason(tc.cfg, tr); got != tc.reason {
				t.Errorf("Reason =\n%s\nwant\n%s", got, tc.reason)
			}
			if got := alarm.ReasonData(tc.cfg, tr, at); got != tc.data {
				t.Errorf("ReasonData =\n%s\nwant\n%s", got, tc.data)
			}
		})
	}
}

// Past what a StateReason holds, AWS names the count and lists the five
// newest crossing datapoints in a second sentence: a 100 of 100 alarm on
// 100 breaching minutes (CWA-10, recorded 2026-11-03:
// testdata/parity/cloudwatch/CWA-10/wide-describe.json). Where the form
// starts below 100 datapoints is UNVERIFIED; understudy switches to it when
// the listing would pass MaxReason.
func TestReasonRecordedWide(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.reason-format", "count-form-unless-datapoints-to-alarm")
	cfg := recordedConfig(60, 100, 100, true, alarm.TreatMissing)
	t0 := time.Date(2026, 11, 3, 3, 14, 0, 0, time.UTC)
	marks := strings.TrimSpace(strings.Repeat("5 ", 100))
	bins := recordedBins(t, t0, time.Minute, cfg.Range(), marks)
	tr, ok := alarm.Evaluate(cfg, bins, stateInsufficient).(alarm.Transition)
	if !ok || tr.To != stateAlarm {
		t.Fatalf("Evaluate = %#v, want a Transition to ALARM", alarm.Evaluate(cfg, bins, stateInsufficient))
	}
	want := "Threshold Crossed: 100 out of the last 100 datapoints were greater than the threshold (3.0). The most recent datapoints which crossed the threshold: " +
		"[5.0 (03/11/26 03:13:00), 5.0 (03/11/26 03:12:00), 5.0 (03/11/26 03:11:00), 5.0 (03/11/26 03:10:00), 5.0 (03/11/26 03:09:00)] (minimum 100 datapoints for OK -> ALARM transition)."
	if got := alarm.Reason(cfg, tr); got != want {
		t.Errorf("Reason =\n%s\nwant\n%s", got, want)
	}
}
