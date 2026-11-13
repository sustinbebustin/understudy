package alarm_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch/alarm"
)

// recordedActionItem is one Action item of a DescribeAlarmHistory
// recording under testdata/parity/cloudwatch.
type recordedActionItem struct {
	Summary, Data string
}

// recordedActionItems are the Action items of fixture, oldest first.
func recordedActionItems(t *testing.T, fixture string) []recordedActionItem {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "testdata", "parity", "cloudwatch", filepath.FromSlash(fixture)))
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Exchanges []struct {
			Response struct {
				Body string `json:"body"`
			} `json:"response"`
		} `json:"exchanges"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil || len(rec.Exchanges) != 1 {
		t.Fatalf("%s: %v (%d exchanges, want 1)", fixture, err, len(rec.Exchanges))
	}
	var body struct {
		AlarmHistoryItems []struct {
			HistoryData, HistoryItemType, HistorySummary string
			Timestamp                                    float64
		}
	}
	if err := json.Unmarshal([]byte(rec.Exchanges[0].Response.Body), &body); err != nil {
		t.Fatalf("%s: %v", fixture, err)
	}
	var out []recordedActionItem
	for _, it := range body.AlarmHistoryItems {
		if it.HistoryItemType == string(cloudwatchapi.HistoryItemTypeAction) {
			out = append(out, recordedActionItem{Summary: it.HistorySummary, Data: it.HistoryData})
		}
	}
	return out
}

// recordedAlarm is the alarm of the CWA recordings' cwalarm helper
// (cloudwatch-alarms.md 13.1): Maximum of us1103/cwa <metric> over 60 s, 1
// period, GreaterThanThreshold 3.0.
func recordedAlarm(t *testing.T, name, metric, configured string, treatMissing *string, actions, insufficient []string) alarm.Subject {
	t.Helper()
	return alarm.Subject{
		ARN: mustARN(t, "arn:aws:cloudwatch:us-east-2:123456789012:alarm:"+name),
		Config: &cloudwatchapi.PutMetricAlarmInput{
			AlarmName: name, Namespace: new("us1103/cwa"), MetricName: new(metric),
			Statistic: new(cloudwatchapi.StatisticMaximum), Period: new(int32(60)), EvaluationPeriods: new(int32(1)), Threshold: new(3.0),
			ComparisonOperator: new(cloudwatchapi.ComparisonOperatorGreaterThanThreshold), TreatMissingData: treatMissing,
			AlarmActions: actions, InsufficientDataActions: insufficient,
		},
		ActionsEnabled: true, ConfigUpdated: recTime(t, configured),
	}
}

// The Action items AWS wrote for an alarm's SNS notifications, byte for
// byte: "Successfully executed action <topic>" with the published message,
// CloudWatch's JSON message structure of the notification body, its Subject
// as the sms text and the email text, or "Failed to execute action <topic>"
// with the error, "Topic does not exist" for a missing topic and
// CloudWatch's own text for a topic whose policy refuses it; both with the
// state change's instant in epoch milliseconds (cloudwatch-alarms.md 5.3,
// 8.3; recorded 2026-11-03: CWA-16 hist-ok and hist-miss, CWA-11
// sns-min-history, CWA-13 history, CWA-23 history).
func TestActionHistoryRecorded(t *testing.T) {
	const (
		topic   = "arn:aws:sns:us-east-2:123456789012:us1103-cwa"
		missing = "arn:aws:sns:us-east-2:123456789012:us1103-missing"
		denying = "arn:aws:sns:us-east-2:123456789012:us1103-cwa-deny"
		fifo    = "arn:aws:sns:us-east-2:123456789012:us1103-cwa.fifo"
		created = "Unchecked: Initial alarm creation"
	)
	change := func(from cloudwatchapi.StateValue, fromReason string, to cloudwatchapi.StateValue, reason, at string) alarm.Change {
		return alarm.Change{
			Old: alarm.StateRecord{Value: from, Reason: fromReason},
			New: alarm.StateRecord{Value: to, Reason: reason, Timestamp: recTime(t, at)},
		}
	}
	succeeded := func(s alarm.Subject, ch alarm.Change) alarm.ActionOutcome {
		return alarm.ActionSucceeded{Published: alarm.PublishedMessage(s, ch)}
	}
	minimal := recordedAlarm(t, "us1103-sns-min", "sns-none", "2026-11-03T03:15:36.145Z", nil, []string{topic}, []string{topic})
	toAlarm := change(stateInsufficient, created, stateAlarm, "cwa sns-min", "2026-11-03T03:15:38.764Z")
	toInsufficient := change(stateAlarm, "cwa sns-min", stateInsufficient, "Insufficient Data: 1 datapoint was unknown.", "2026-11-03T03:16:21.865Z")
	ok := recordedAlarm(t, "us1103-hist-ok", "hist-none", "2026-11-03T03:18:40.456Z", new("ignore"), []string{topic}, nil)
	okChange := change(stateInsufficient, created, stateAlarm, "delivered", "2026-11-03T03:18:40.998Z")
	ordered := recordedAlarm(t, "us1103-fifo", "fifo-none", "2026-11-03T03:14:18.568Z", new("ignore"), []string{fifo}, nil)
	orderedChange := change(stateInsufficient, created, stateAlarm, "fifo", "2026-11-03T03:14:19.129Z")
	for _, tc := range []struct {
		fixture string
		topic   string
		changes []alarm.Change
		outcome func(alarm.Change) alarm.ActionOutcome
	}{
		{"CWA-16/hist-ok.json", topic, []alarm.Change{okChange}, func(ch alarm.Change) alarm.ActionOutcome { return succeeded(ok, ch) }},
		{"CWA-11/sns-min-history.json", topic, []alarm.Change{toAlarm, toInsufficient}, func(ch alarm.Change) alarm.ActionOutcome { return succeeded(minimal, ch) }},
		{"CWA-13/history.json", fifo, []alarm.Change{orderedChange}, func(ch alarm.Change) alarm.ActionOutcome { return succeeded(ordered, ch) }},
		{
			"CWA-16/hist-miss.json", missing,
			[]alarm.Change{change(stateInsufficient, created, stateAlarm, "missing topic", "2026-11-03T03:18:39.865Z")},
			func(alarm.Change) alarm.ActionOutcome { return alarm.ActionFailed{Error: "Topic does not exist"} },
		},
		{
			"CWA-23/history.json", denying,
			[]alarm.Change{change(stateInsufficient, created, stateAlarm, "denied", "2026-11-03T03:13:46.701Z")},
			func(alarm.Change) alarm.ActionOutcome { return alarm.ActionFailed{Error: alarm.ActionDenied(denying)} },
		},
	} {
		want := recordedActionItems(t, tc.fixture)
		if len(want) != len(tc.changes) {
			t.Fatalf("%s: %d Action items, want %d", tc.fixture, len(want), len(tc.changes))
		}
		for i, ch := range tc.changes {
			summary, data := alarm.ActionHistory(tc.topic, ch.New.Timestamp, tc.outcome(ch))
			if summary != want[i].Summary {
				t.Errorf("%s item %d: summary = %q, want %q", tc.fixture, i, summary, want[i].Summary)
			}
			if data != want[i].Data {
				t.Errorf("%s item %d: HistoryData =\n%s\nwant\n%s", tc.fixture, i, data, want[i].Data)
			}
		}
	}
}
