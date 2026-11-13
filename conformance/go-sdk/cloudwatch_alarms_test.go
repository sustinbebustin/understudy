package gosdk_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

// Research: docs/research/cloudwatch-alarms.md (cited as cwa N.N).

// admin calls understudy's admin API on the plain endpoint and fails the
// test on a non-2xx answer.
func admin(t *testing.T, method, path string, body any) []byte {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		ok(t, err, "marshal admin body")
		r = bytes.NewReader(b)
	}
	// Not t.Context: cleanups call admin after it is canceled.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String()+"/_understudy/v1"+path, r)
	ok(t, err, "admin request")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	ok(t, err, method+" "+path)
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	ok(t, err, "read admin answer")
	if resp.StatusCode/100 != 2 {
		t.Fatalf("%s %s = %d: %s", method, path, resp.StatusCode, out)
	}
	return out
}

// frozenClock freezes the emulator clock for the test and puts it back as
// serve started it afterwards.
func frozenClock(t *testing.T) {
	t.Helper()
	admin(t, http.MethodPost, "/clock", map[string]string{"op": "freeze"})
	t.Cleanup(func() {
		admin(t, http.MethodPost, "/reset", map[string]any{"services": []string{"sts"}, "clock": true})
	})
}

// advanceClock moves the emulator clock forward, running the evaluations
// due on the way, and waits until the emulator has no queued work.
func advanceClock(t *testing.T, d time.Duration) {
	t.Helper()
	admin(t, http.MethodPost, "/clock", map[string]string{"op": "advance", "by": d.String()})
	admin(t, http.MethodGet, "/settle?timeout=30s", nil)
}

// An alarm on data put now goes to ALARM on the first evaluation an admin
// clock advance runs, records a StateUpdate history item, and goes to
// INSUFFICIENT_DATA once the point has left its evaluation range. Not
// parallel: it moves the clock every test shares.
func TestCloudWatchAlarmEvaluatesOnClockAdvance(t *testing.T) {
	frozenClock(t)
	client := cloudwatch.NewFromConfig(awsCfg)
	metric := name(t, "load")
	// Data put without a Timestamp lines up with alarm windows (cwa 12.3).
	_, err := client.PutMetricData(t.Context(), &cloudwatch.PutMetricDataInput{
		Namespace: aws.String("Conformance"), MetricData: []types.MetricDatum{{MetricName: aws.String(metric), Value: aws.Float64(20)}},
	})
	ok(t, err, "PutMetricData")
	alarmName := name(t, "eval")
	in := alarmInput(alarmName)
	in.MetricName, in.Threshold = aws.String(metric), aws.Float64(10)
	putAlarm(t, in)

	// cwa 3.1: no synchronous evaluation (third-party-recorded).
	if a := describeAlarm(t, client, alarmName); a.StateValue != types.StateValueInsufficientData || aws.ToString(a.StateReason) != "Unchecked: Initial alarm creation" {
		t.Fatalf("after create: %s %q", a.StateValue, aws.ToString(a.StateReason))
	}

	advanceClock(t, time.Minute)
	a := describeAlarm(t, client, alarmName)
	reason := aws.ToString(a.StateReason)
	// cwa 4.1, 4.3: the count form, values as Java doubles.
	if a.StateValue != types.StateValueAlarm || !strings.HasPrefix(reason, "Threshold Crossed: 1 datapoint [20.0 (") ||
		!strings.HasSuffix(reason, ")] was greater than the threshold (10.0).") {
		t.Fatalf("after a minute: %s %q", a.StateValue, reason)
	}
	var data struct {
		Version          string    `json:"version"`
		Statistic        string    `json:"statistic"`
		Period           int       `json:"period"`
		RecentDatapoints []float64 `json:"recentDatapoints"`
		Threshold        float64   `json:"threshold"`
	}
	if err := json.Unmarshal([]byte(aws.ToString(a.StateReasonData)), &data); err != nil || data.Version != "1.0" || data.Statistic != "Sum" ||
		data.Period != 60 || len(data.RecentDatapoints) != 1 || data.RecentDatapoints[0] != 20 || data.Threshold != 10 {
		t.Errorf("StateReasonData = %s (%v) (cwa 4.2)", aws.ToString(a.StateReasonData), err)
	}

	history := func() []string {
		t.Helper()
		out, err := client.DescribeAlarmHistory(t.Context(), &cloudwatch.DescribeAlarmHistoryInput{
			AlarmName: aws.String(alarmName), HistoryItemType: types.HistoryItemTypeStateUpdate,
		})
		ok(t, err, "DescribeAlarmHistory")
		var got []string
		for _, it := range out.AlarmHistoryItems {
			if it.HistoryItemType != types.HistoryItemTypeStateUpdate || it.AlarmType != types.AlarmTypeMetricAlarm {
				t.Errorf("history item %+v, want a metric alarm StateUpdate (cwa 8.2)", it)
			}
			got = append(got, aws.ToString(it.HistorySummary))
		}
		return got
	}
	if got := history(); len(got) != 1 || got[0] != "Alarm updated from INSUFFICIENT_DATA to ALARM" {
		t.Errorf("history = %q (cwa 8.2)", got)
	}

	// cwa 3.5: with every bin missing the default treatment is
	// INSUFFICIENT_DATA.
	advanceClock(t, 10*time.Minute)
	if a := describeAlarm(t, client, alarmName); a.StateValue != types.StateValueInsufficientData {
		t.Errorf("after the point left the range: %s", a.StateValue)
	}
	if got := history(); len(got) != 2 || got[0] != "Alarm updated from ALARM to INSUFFICIENT_DATA" {
		t.Errorf("history = %q, want the newest first (cwa 8.1)", got)
	}

	// DescribeAlarmsForMetric finds it by its metric (cwa 9.4).
	out, err := client.DescribeAlarmsForMetric(t.Context(), &cloudwatch.DescribeAlarmsForMetricInput{Namespace: aws.String("Conformance"), MetricName: aws.String(metric)})
	ok(t, err, "DescribeAlarmsForMetric")
	if len(out.MetricAlarms) != 1 || aws.ToString(out.MetricAlarms[0].AlarmName) != alarmName {
		t.Errorf("DescribeAlarmsForMetric = %+v", out.MetricAlarms)
	}
}

// GetMetricStatistics refuses more than 1440 datapoints with the recorded
// text, counted before any data is read (cwa 9.3, third-party-recorded),
// and answers an empty list labelled with the metric name (cwa 9.2).
func TestCloudWatchGetMetricStatisticsLimits(t *testing.T) {
	t.Parallel()
	client := cloudwatch.NewFromConfig(awsCfg)
	now := time.Now().UTC()
	in := &cloudwatch.GetMetricStatisticsInput{
		Namespace: aws.String("Conformance"), MetricName: aws.String("metric_name"), StartTime: aws.Time(now), EndTime: aws.Time(now.Add(24 * time.Hour)),
		Period: aws.Int32(1), Statistics: []types.Statistic{types.StatisticAverage},
	}
	_, err := client.GetMetricStatistics(t.Context(), in)
	const op = "GetMetricStatistics"
	apiErr(t, err, op, "InvalidParameterCombination")
	wantStatus(t, err, op, 400)
	wantMessage(t, err, op,
		"You have requested up to 86400 datapoints, which exceeds the limit of 1440. You may reduce the datapoints requested by increasing Period, or decreasing the time range.")
	in.Period = aws.Int32(60)
	out, err := client.GetMetricStatistics(t.Context(), in)
	ok(t, err, "GetMetricStatistics over a day by the minute")
	if aws.ToString(out.Label) != "metric_name" || len(out.Datapoints) != 0 {
		t.Errorf("GetMetricStatistics = %q %+v", aws.ToString(out.Label), out.Datapoints)
	}
	in.EndTime = in.StartTime
	_, err = client.GetMetricStatistics(t.Context(), in)
	apiErr(t, err, op, "InvalidParameterValue")
	wantStatus(t, err, op, 400)
	wantMessage(t, err, op, "The parameter StartTime must be less than the parameter EndTime.")
}
