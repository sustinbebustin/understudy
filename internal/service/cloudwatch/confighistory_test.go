package cloudwatch_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
)

// recordedAccount and recordedRegion are the account and Region of the
// CWA recordings (cloudwatch-alarms.md 13), replaced by the harness's own.
const (
	recordedAccount = "123456789012"
	recordedRegion  = "us-east-2"
)

// recordedConfigItems are the ConfigurationUpdate items of a
// DescribeAlarmHistory recording under testdata/parity/cloudwatch, in the
// recorded order, their HistoryData normalized to the harness: the account
// and Region replaced, and each recorded timestamp replaced by the harness
// instant at replaces names for it.
func recordedConfigItems(h *harness, fixture string, at map[string]time.Time) []cloudwatchapi.AlarmHistoryItem {
	h.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "parity", "cloudwatch", filepath.FromSlash(fixture)))
	if err != nil {
		h.Fatal(err)
	}
	var rec struct {
		Exchanges []struct {
			Response struct {
				Body string `json:"body"`
			} `json:"response"`
		} `json:"exchanges"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil || len(rec.Exchanges) != 1 {
		h.Fatalf("%s: %v (%d exchanges, want 1)", fixture, err, len(rec.Exchanges))
	}
	var body struct {
		AlarmHistoryItems []struct {
			AlarmName, AlarmType, HistoryData, HistoryItemType, HistorySummary string
		}
	}
	if err := json.Unmarshal([]byte(rec.Exchanges[0].Response.Body), &body); err != nil {
		h.Fatalf("%s: %v", fixture, err)
	}
	pairs := make([]string, 0, 4+2*len(at))
	pairs = append(pairs, recordedAccount, h.Scope.Account.String(), recordedRegion, h.Scope.Region.String())
	for recorded, instant := range at {
		pairs = append(pairs, recorded, stamp(instant))
	}
	normalize := strings.NewReplacer(pairs...)
	var out []cloudwatchapi.AlarmHistoryItem
	for _, it := range body.AlarmHistoryItems {
		if it.HistoryItemType != string(cloudwatchapi.HistoryItemTypeConfigurationUpdate) {
			continue
		}
		out = append(out, cloudwatchapi.AlarmHistoryItem{
			AlarmName:       new(it.AlarmName),
			AlarmType:       new(cloudwatchapi.AlarmType(it.AlarmType)),
			HistoryItemType: new(cloudwatchapi.HistoryItemType(it.HistoryItemType)),
			HistorySummary:  new(it.HistorySummary),
			HistoryData:     new(normalize.Replace(it.HistoryData)),
		})
	}
	if len(out) == 0 {
		h.Fatalf("%s holds no ConfigurationUpdate item", fixture)
	}
	return out
}

// wantConfigItems compares DescribeAlarmHistory's items field by field with
// the recorded ones, and each item's Timestamp with when, the instant of
// the call that wrote it.
func wantConfigItems(h *harness, got, want []cloudwatchapi.AlarmHistoryItem, when []time.Time) {
	h.Helper()
	if len(got) != len(want) || len(when) != len(want) {
		h.Fatalf("history = %v\nwant %d items, as recorded", summaries(got), len(want))
	}
	for i, at := range when {
		if !got[i].Timestamp.Equal(at) {
			h.Errorf("item %d Timestamp = %v, want %v", i, *got[i].Timestamp, at)
		}
	}
	for i, w := range want {
		g := got[i]
		switch {
		case *g.AlarmName != *w.AlarmName:
			h.Errorf("item %d AlarmName = %q, want %q", i, *g.AlarmName, *w.AlarmName)
		case *g.AlarmType != *w.AlarmType:
			h.Errorf("item %d AlarmType = %q, want %q", i, *g.AlarmType, *w.AlarmType)
		case *g.HistoryItemType != *w.HistoryItemType:
			h.Errorf("item %d HistoryItemType = %q, want %q", i, *g.HistoryItemType, *w.HistoryItemType)
		case *g.HistorySummary != *w.HistorySummary:
			h.Errorf("item %d HistorySummary = %q, want %q", i, *g.HistorySummary, *w.HistorySummary)
		case *g.HistoryData != *w.HistoryData:
			h.Errorf("item %d HistoryData =\n%s\nwant\n%s", i, *g.HistoryData, *w.HistoryData)
		}
	}
}

// cwalarm is the recording kit's alarm (cloudwatch-alarms.md 13.1): Maximum
// over 60 seconds on us1103/cwa, GreaterThanThreshold 3, one evaluation
// period, with the treatment and alarm action given.
func cwalarm(name, metric, treat, action string) *cloudwatchapi.PutMetricAlarmInput {
	return &cloudwatchapi.PutMetricAlarmInput{
		AlarmName:          name,
		Namespace:          new("us1103/cwa"),
		MetricName:         new(metric),
		Statistic:          new(cloudwatchapi.StatisticMaximum),
		Threshold:          new(3.0),
		ComparisonOperator: new(cloudwatchapi.ComparisonOperatorGreaterThanThreshold),
		Period:             new(int32(60)),
		EvaluationPeriods:  new(int32(1)),
		TreatMissingData:   new(treat),
		AlarmActions:       []string{action},
	}
}

func (h *harness) arnIn(service, resource string) string {
	return "arn:aws:" + service + ":" + h.Scope.Region.String() + ":" + h.Scope.Account.String() + ":" + resource
}

// PutMetricAlarm writes a ConfigurationUpdate item on create, with the
// summary and HistoryData AWS recorded (cloudwatch-alarms.md 8.3; CWA-16,
// recorded 2026-11-03: testdata/parity/cloudwatch/CWA-16/hist-miss.json).
func TestPutMetricAlarmCreateWritesAConfigurationUpdate(t *testing.T) {
	run(t, func(h *harness) {
		created := h.now()
		must(h, cloudwatchapi.OpPutMetricAlarm, cwalarm("us1103-hist-miss", "hist-none", "ignore", h.arnIn("sns", "us1103-missing")))
		h.advance(500 * time.Millisecond)
		h.setState("us1103-hist-miss", cloudwatchapi.StateValueAlarm)

		want := recordedConfigItems(h, "CWA-16/hist-miss.json", map[string]time.Time{"2026-11-03T03:18:39.309+0000": created})
		got := h.history(&cloudwatchapi.DescribeAlarmHistoryInput{
			AlarmName: new("us1103-hist-miss"), HistoryItemType: new(cloudwatchapi.HistoryItemTypeConfigurationUpdate),
		})
		wantConfigItems(h, got, want, []time.Time{created})
	})
}

// PutMetricAlarm on an existing alarm writes an "updated" item whose
// originalUpdatedFields holds the old values of the changed fields,
// alarmConfigurationUpdatedTimestamp among them (cloudwatch-alarms.md 8.3;
// CWA-16, recorded 2026-11-03: testdata/parity/cloudwatch/CWA-16/hist-ok.json).
func TestPutMetricAlarmUpdateWritesAConfigurationUpdate(t *testing.T) {
	run(t, func(h *harness) {
		topic := h.arnIn("sns", "us1103-cwa")
		created := h.now()
		must(h, cloudwatchapi.OpPutMetricAlarm, cwalarm("us1103-hist-ok", "hist-none", "ignore", topic))
		h.advance(500 * time.Millisecond)
		stateSet := h.now()
		must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "us1103-hist-ok", StateValue: cloudwatchapi.StateValueAlarm, StateReason: "delivered"})
		h.advance(500 * time.Millisecond)
		updated := h.now()
		must(h, cloudwatchapi.OpPutMetricAlarm, cwalarm("us1103-hist-ok", "hist-none", "breaching", topic))

		want := recordedConfigItems(h, "CWA-16/hist-ok.json", map[string]time.Time{
			"2026-11-03T03:18:40.456+0000": created,
			"2026-11-03T03:18:40.998+0000": stateSet,
			"2026-11-03T03:18:41.526+0000": updated,
		})
		got := h.history(&cloudwatchapi.DescribeAlarmHistoryInput{
			AlarmName: new("us1103-hist-ok"), ScanBy: new(cloudwatchapi.ScanByTimestampAscending),
			HistoryItemType: new(cloudwatchapi.HistoryItemTypeConfigurationUpdate),
		})
		wantConfigItems(h, got, want, []time.Time{created, updated})
	})
}

// DeleteAlarms writes a "deleted" item holding the alarm as it was, which
// outlives the alarm with the rest of its history; HistoryItemType selects
// the ConfigurationUpdate items alone (cloudwatch-alarms.md 8.1, 8.3;
// CWA-16, recorded 2026-11-03:
// testdata/parity/cloudwatch/CWA-16/hist-miss-after-delete.json).
func TestDeleteAlarmsWritesAConfigurationUpdate(t *testing.T) {
	run(t, func(h *harness) {
		created := h.now()
		must(h, cloudwatchapi.OpPutMetricAlarm, cwalarm("us1103-hist-miss", "hist-none", "ignore", h.arnIn("sns", "us1103-missing")))
		h.advance(500 * time.Millisecond)
		stateSet := h.now()
		must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "us1103-hist-miss", StateValue: cloudwatchapi.StateValueAlarm, StateReason: "missing topic"})
		h.advance(time.Minute)
		deleted := h.now()
		must(h, cloudwatchapi.OpDeleteAlarms, &cloudwatchapi.DeleteAlarmsInput{AlarmNames: []string{"us1103-hist-miss"}})

		want := recordedConfigItems(h, "CWA-16/hist-miss-after-delete.json", map[string]time.Time{
			"2026-11-03T03:18:39.309+0000": created,
			"2026-11-03T03:18:39.865+0000": stateSet,
		})
		got := h.history(&cloudwatchapi.DescribeAlarmHistoryInput{
			AlarmName: new("us1103-hist-miss"), HistoryItemType: new(cloudwatchapi.HistoryItemTypeConfigurationUpdate),
		})
		wantConfigItems(h, got, want, []time.Time{deleted, created})

		all := h.history(&cloudwatchapi.DescribeAlarmHistoryInput{AlarmName: new("us1103-hist-miss")})
		wantAll := []string{
			`us1103-hist-miss: Alarm "us1103-hist-miss" deleted`,
			"us1103-hist-miss: Alarm updated from INSUFFICIENT_DATA to ALARM",
			`us1103-hist-miss: Alarm "us1103-hist-miss" created`,
		}
		if got := summaries(all); !slices.Equal(got, wantAll) {
			h.Errorf("whole history = %v\nwant %v", got, wantAll)
		}
		states := h.history(&cloudwatchapi.DescribeAlarmHistoryInput{
			AlarmName: new("us1103-hist-miss"), HistoryItemType: new(cloudwatchapi.HistoryItemTypeStateUpdate),
		})
		if got := summaries(states); !slices.Equal(got, wantAll[1:2]) {
			h.Errorf("StateUpdate items = %v, want %v", got, wantAll[1:2])
		}
	})
}

// DisableAlarmActions writes an "updated" item with actionsEnabled false
// and its old value in originalUpdatedFields (cloudwatch-alarms.md 8.3;
// CWA-14, recorded 2026-11-03: testdata/parity/cloudwatch/CWA-14/history.json).
// EnableAlarmActions is inferred to write the mirror item.
func TestAlarmActionSwitchesWriteConfigurationUpdates(t *testing.T) {
	run(t, func(h *harness) {
		created := h.now()
		must(h, cloudwatchapi.OpPutMetricAlarm, cwalarm("us1103-ev-off", "ev-none", "ignore", h.arnIn("lambda", "function:us1103-cwa")))
		h.advance(500 * time.Millisecond)
		disabled := h.now()
		must(h, cloudwatchapi.OpDisableAlarmActions, &cloudwatchapi.DisableAlarmActionsInput{AlarmNames: []string{"us1103-ev-off"}})

		want := recordedConfigItems(h, "CWA-14/history.json", map[string]time.Time{
			"2026-11-03T03:14:19.737+0000": created,
			"2026-11-03T03:14:20.920+0000": disabled,
		})
		in := &cloudwatchapi.DescribeAlarmHistoryInput{
			AlarmName: new("us1103-ev-off"), ScanBy: new(cloudwatchapi.ScanByTimestampAscending),
			HistoryItemType: new(cloudwatchapi.HistoryItemTypeConfigurationUpdate),
		}
		wantConfigItems(h, h.history(in), want, []time.Time{created, disabled})

		h.advance(500 * time.Millisecond)
		enabled := h.now()
		must(h, cloudwatchapi.OpEnableAlarmActions, &cloudwatchapi.EnableAlarmActionsInput{AlarmNames: []string{"us1103-ev-off"}})
		items := h.history(in)
		if len(items) != 3 {
			h.Fatalf("history after EnableAlarmActions = %v, want a third item", summaries(items))
		}
		last := items[2]
		wantOriginal := `,"originalUpdatedFields":{"alarmConfigurationUpdatedTimestamp":"` + stamp(disabled) + `","actionsEnabled":false}}`
		if *last.HistorySummary != `Alarm "us1103-ev-off" updated` || !last.Timestamp.Equal(enabled) ||
			!strings.Contains(*last.HistoryData, `"actionsEnabled":true,`) || !strings.HasSuffix(*last.HistoryData, wantOriginal) {
			h.Errorf("EnableAlarmActions item = %q at %v\n%s\nwant it to end %s", *last.HistorySummary, *last.Timestamp, *last.HistoryData, wantOriginal)
		}
	})
}

// Members no recording sets appear in the alarm object only when set
// (inferred, cloudwatch-alarms.md 8.3), and an update that sets one
// reports its old value as null.
func TestConfigurationUpdateUnrecordedMembers(t *testing.T) {
	run(t, func(h *harness) {
		in := cwalarm("extra", "m", "ignore", h.arnIn("sns", "t"))
		created := h.now()
		must(h, cloudwatchapi.OpPutMetricAlarm, in)
		h.advance(time.Second)
		updated := h.now()
		in.AlarmDescription, in.Unit, in.DatapointsToAlarm = new("about"), new(cloudwatchapi.StandardUnitCount), new(int32(1))
		in.Dimensions = []cloudwatchapi.Dimension{{Name: "B", Value: "2"}, {Name: "A", Value: "1"}}
		must(h, cloudwatchapi.OpPutMetricAlarm, in)
		items := h.history(&cloudwatchapi.DescribeAlarmHistoryInput{AlarmName: new("extra")})
		if len(items) != 2 {
			h.Fatalf("history = %v", summaries(items))
		}
		ts := stamp(created)
		want := `{"version":"1.0","type":"Update","updatedAlarm":{"alarmName":"extra","alarmArn":"` + h.alarmARN("extra") +
			`","alarmDescription":"about","alarmConfigurationUpdatedTimestamp":"` + stamp(updated) + `","namespace":"us1103/cwa","metricName":"m",` +
			`"extendedStatistic":"MAXIMUM","period":60,"unit":"Count","dimensions":[{"name":"B","value":"2"},{"name":"A","value":"1"}],` +
			`"threshold":3.0,"comparisonOperator":"GreaterThanThreshold","evaluationPeriods":1,"datapointsToAlarm":1,"treatMissingData":"ignore",` +
			`"stateValue":"INSUFFICIENT_DATA","stateUpdatedTimestamp":"` + ts + `","actionsEnabled":true,"alarmActions":["` + h.arnIn("sns", "t") +
			`"],"insufficientDataActions":[],"okactions":[]},"originalUpdatedFields":{"alarmDescription":null,` +
			`"alarmConfigurationUpdatedTimestamp":"` + ts + `","unit":null,"dimensions":[],"datapointsToAlarm":null}}`
		if got := *items[0].HistoryData; got != want {
			h.Errorf("HistoryData =\n%s\nwant\n%s", got, want)
		}
	})
}
