package cloudwatch_test

import (
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/delivery"
)

// The golden cases below replay five of LocalStack's AWS-validated alarm
// snapshots (third-party-recorded, cloudwatch-alarms.md 0) through the
// kernel, on the emulator clock. Each cites localstack@8b9a79f <path>:L<n>
// and the test's last_validated_date in the sibling *.validation.json. The
// snapshots mask names, dates and ids; a masked value is computed here
// from the emulator clock and the alarm, and every unmasked value is
// asserted as recorded. The JSON-string values (StateReasonData,
// HistoryData) keep their recorded key order and number forms (cwa 4.2),
// so they are compared as strings; message bodies and events are compared
// parsed. Cases the existing tests already replay are left out:
// test_get_metric_statistics and test_multiple_dimensions_statistics
// (alarmreads_test.go), test_describe_minimal_metric_alarm, test_delete_alarm
// and test_set_alarm_invalid_input (alarms_test.go). TestSQSMetrics'
// test_alarm_number_of_messages_sent needs a vended AWS/SQS metric, which
// understudy never publishes (cwa 12.7).

// goldenTopic is the ARN of an SNS topic of the harness's scope.
func (h *harness) goldenTopic(name string) string {
	return "arn:aws:sns:" + h.Scope.Region.String() + ":" + h.Scope.Account.String() + ":" + name
}

// jsonDiff compares the JSON text got with want, both parsed.
func jsonDiff(h *harness, what string, got []byte, want any) {
	h.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		h.Fatalf("%s is not JSON: %v\n%s", what, err, got)
	}
	b, err := json.Marshal(want)
	if err != nil {
		h.Fatal(err)
	}
	if err := json.Unmarshal(b, &w); err != nil {
		h.Fatal(err)
	}
	if d := cmp.Diff(w, g); d != "" {
		h.Errorf("%s (-recorded +got):\n%s", what, d)
	}
}

// notification is one SNS action an alarm transition delivered.
type notification struct {
	topic string
	body  []byte
}

func notifications(h *harness, fx *effects) []notification {
	h.Helper()
	all := fx.sink.all()
	out := make([]notification, 0, len(all))
	for i := range all {
		d := &all[i]
		topic, ok := d.Request.Target.(delivery.SNSTopic)
		if !ok || d.Request.Mechanism != delivery.CloudWatchToSNS {
			h.Fatalf("delivery %+v, want a CloudWatchToSNS notification", d.Request)
		}
		out = append(out, notification{topic: topic.ARN.String(), body: []byte(topic.Message)})
	}
	return out
}

// goldenDims is the dimension LocalStack's alarm tests put on their alarms.
var goldenDims = []cloudwatchapi.Dimension{{Name: "InstanceId", Value: "abc"}}

// goldenDescribed checks the DescribeAlarms fields every replayed snapshot
// records for its alarm.
func goldenDescribed(h *harness, step string, got cloudwatchapi.MetricAlarm, state cloudwatchapi.StateValue, reason string, actionsEnabled bool) {
	h.Helper()
	if *got.StateValue != state || *got.StateReason != reason || *got.ActionsEnabled != actionsEnabled ||
		got.InsufficientDataActions == nil || len(got.InsufficientDataActions) != 0 {
		h.Errorf("%s: %s %q ActionsEnabled %v InsufficientDataActions %v; recorded %s %q ActionsEnabled %v []",
			step, *got.StateValue, *got.StateReason, *got.ActionsEnabled, got.InsufficientDataActions, state, reason, actionsEnabled)
	}
}

// TestGoldenPutMetricAlarm replays test_put_metric_alarm (localstack@8b9a79f
// tests/aws/services/cloudwatch/test_cloudwatch.py:L1361; last_validated_date
// 2026-02-26T10:14:12Z): two points of 21 and 22 seconds against an Average
// alarm over 10 seconds above 21 go to ALARM on the next evaluation, which
// notifies the topic, writes a StateUpdate item, and leaves the alarm
// findable by DescribeAlarmsForMetric with its reason data.
func TestGoldenPutMetricAlarm(t *testing.T) {
	runAlarms(t, func(h *harness, fx *effects) {
		followsSwitch(h, "cloudwatch.alarm.evaluation-cadence", "minute,10s-for-high-resolution,hourly-past-one-day")
		topic := h.goldenTopic("alarm-topic")
		t0 := h.now()
		must(h, cloudwatchapi.OpPutMetricAlarm, &cloudwatchapi.PutMetricAlarmInput{
			AlarmName: "golden", AlarmDescription: new("testing cloudwatch alarms"), MetricName: new("my-metric1"), Namespace: new("golden-ns"),
			ActionsEnabled: new(true), Period: new(int32(10)), Threshold: new(21.0), Dimensions: goldenDims, Unit: new(cloudwatchapi.StandardUnitSeconds),
			Statistic: new(cloudwatchapi.StatisticAverage), OKActions: []string{topic}, AlarmActions: []string{topic}, EvaluationPeriods: new(int32(1)),
			ComparisonOperator: new(cloudwatchapi.ComparisonOperatorGreaterThanThreshold), TreatMissingData: new("ignore"),
		})
		arn := h.alarmARN("golden")
		// test_cloudwatch.snapshot.json:L6255-L6296
		created := h.one("golden")
		goldenDescribed(h, "describe-alarm", created, cloudwatchapi.StateValueInsufficientData, "Unchecked: Initial alarm creation", true)
		if created.StateReasonData != nil {
			h.Errorf("describe-alarm StateReasonData = %s, recorded none", *created.StateReasonData)
		}

		data := make([]cloudwatchapi.MetricDatum, 0, 2)
		for _, v := range []float64{21, 22} {
			data = append(data, cloudwatchapi.MetricDatum{
				MetricName: "my-metric1", Dimensions: goldenDims, Value: new(v), Timestamp: new(t0), Unit: new(cloudwatchapi.StandardUnitSeconds),
			})
		}
		must(h, cloudwatchapi.OpPutMetricData, &cloudwatchapi.PutMetricDataInput{Namespace: "golden-ns", MetricData: data})
		at := t0.Add(10 * time.Second)
		h.settle(10 * time.Second)

		reason := "Threshold Crossed: 1 datapoint [21.5 (" + t0.Format(reasonDate) + ")] was greater than the threshold (21.0)."
		// test_cloudwatch.snapshot.json:L6297-L6335
		sent := notifications(h, fx)
		if len(sent) != 1 || sent[0].topic != topic {
			h.Fatalf("notifications = %+v, recorded one to %s", sent, topic)
		}
		jsonDiff(h, "alarm-triggered-sqs-msg", sent[0].body, map[string]any{
			"AWSAccountId": h.Scope.Account.String(), "ActionExecutedAfterMuteWindow": false, "AlarmActions": []string{topic}, "AlarmArn": arn,
			"AlarmConfigurationUpdatedTimestamp": stamp(t0), "AlarmDescription": "testing cloudwatch alarms", "AlarmName": "golden",
			"InsufficientDataActions": []string{}, "NewStateReason": reason, "NewStateValue": "ALARM", "OKActions": []string{topic},
			"OldStateValue": "INSUFFICIENT_DATA", "Region": "US East (N. Virginia)", "StateChangeTime": stamp(at),
			"Trigger": map[string]any{
				"ComparisonOperator": "GreaterThanThreshold", "Dimensions": []map[string]string{{"name": "InstanceId", "value": "abc"}},
				"EvaluateLowSampleCountPercentile": "", "EvaluationPeriods": 1, "MetricName": "my-metric1", "Namespace": "golden-ns", "Period": 10,
				"Statistic": "AVERAGE", "StatisticType": "Statistic", "Threshold": 21.0, "TreatMissingData": "ignore", "Unit": "Seconds",
			},
		})

		reasonData := `{"version":"1.0","queryDate":"` + stamp(at) + `","startDate":"` + stamp(t0) +
			`","unit":"Seconds","statistic":"Average","period":10,"recentDatapoints":[21.5],"threshold":21.0,` +
			`"evaluatedDatapoints":[{"timestamp":"` + stamp(t0) + `","sampleCount":2.0,"value":21.5}]}`
		// test_cloudwatch.snapshot.json:L6336-L6380
		items := h.history(&cloudwatchapi.DescribeAlarmHistoryInput{AlarmName: new("golden"), HistoryItemType: new(cloudwatchapi.HistoryItemTypeStateUpdate)})
		wantHistory := `{"version":"1.0","oldState":{"stateValue":"INSUFFICIENT_DATA","stateReason":"Unchecked: Initial alarm creation"},` +
			`"newState":{"stateValue":"ALARM","stateReason":"` + reason + `","stateReasonData":` + reasonData + `}}`
		if len(items) != 1 {
			h.Fatalf("describe-alarm-history = %v, recorded one item", summaries(items))
		}
		it := items[0]
		if *it.AlarmType != cloudwatchapi.AlarmTypeMetricAlarm || *it.HistoryItemType != cloudwatchapi.HistoryItemTypeStateUpdate ||
			*it.HistorySummary != "Alarm updated from INSUFFICIENT_DATA to ALARM" || *it.HistoryData != wantHistory || !it.Timestamp.Equal(at) {
			h.Errorf("describe-alarm-history item = %s %s %q at %v\n%s\nrecorded\n%s", *it.AlarmType, *it.HistoryItemType, *it.HistorySummary, *it.Timestamp, *it.HistoryData, wantHistory)
		}

		// test_cloudwatch.snapshot.json:L6381-L6435
		out := must(h, cloudwatchapi.OpDescribeAlarmsForMetric, &cloudwatchapi.DescribeAlarmsForMetricInput{
			MetricName: "my-metric1", Namespace: "golden-ns", Dimensions: goldenDims, Statistic: new(cloudwatchapi.StatisticAverage),
		})
		if len(out.MetricAlarms) != 1 {
			h.Fatalf("describe-alarms-for-metric found %d alarms, recorded one", len(out.MetricAlarms))
		}
		got := out.MetricAlarms[0]
		goldenDescribed(h, "describe-alarms-for-metric", got, cloudwatchapi.StateValueAlarm, reason, true)
		if got.StateReasonData == nil || *got.StateReasonData != reasonData {
			h.Errorf("describe-alarms-for-metric StateReasonData = %v\nrecorded %s", got.StateReasonData, reasonData)
		}
		if !got.StateUpdatedTimestamp.Equal(at) || !got.StateTransitionedTimestamp.Equal(at) || !got.AlarmConfigurationUpdatedTimestamp.Equal(t0) {
			h.Errorf("timestamps updated %v transitioned %v configuration %v; want %v %v %v (cwa 2.2, 12.6)",
				*got.StateUpdatedTimestamp, *got.StateTransitionedTimestamp, *got.AlarmConfigurationUpdatedTimestamp, at, at, t0)
		}
	})
}

// TestGoldenBreachingAlarmActions replays test_breaching_alarm_actions[json]
// (localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch.py:L1478;
// last_validated_date 2025-09-19T21:12:42Z): with no data at all, missing
// data treated as breaching takes the alarm to ALARM on its first
// evaluation and notifies the topic.
func TestGoldenBreachingAlarmActions(t *testing.T) {
	runAlarms(t, func(h *harness, fx *effects) {
		topic := h.goldenTopic("breaching-topic")
		t0 := h.now()
		must(h, cloudwatchapi.OpPutMetricAlarm, &cloudwatchapi.PutMetricAlarmInput{
			AlarmName: "golden", AlarmDescription: new("testing cloudwatch alarms"), MetricName: new("my-metric101"), Namespace: new("test/breaching-alarm"),
			Period: new(int32(10)), Threshold: new(2.0), Dimensions: goldenDims, Unit: new(cloudwatchapi.StandardUnitSeconds),
			Statistic: new(cloudwatchapi.StatisticAverage), OKActions: []string{topic}, AlarmActions: []string{topic}, EvaluationPeriods: new(int32(2)),
			ComparisonOperator: new(cloudwatchapi.ComparisonOperatorGreaterThanThreshold), TreatMissingData: new("breaching"),
		})
		arn := h.alarmARN("golden")
		if got := h.one("golden"); !*got.ActionsEnabled {
			h.Fatal("ActionsEnabled is false after a create without it, recorded true (test_cloudwatch.py:L1522)")
		}
		at := t0.Add(10 * time.Second)
		h.settle(10 * time.Second)

		reason := "Threshold Crossed: no datapoints were received for 2 periods and 2 missing datapoints were treated as [Breaching]."
		// test_cloudwatch.snapshot.json:L1983-L2020. This 2025 recording has
		// no ActionExecutedAfterMuteWindow; the 2026 one has it false
		// (TestGoldenPutMetricAlarm), which understudy follows (cwa 5.1).
		sent := notifications(h, fx)
		if len(sent) != 1 || sent[0].topic != topic {
			h.Fatalf("notifications = %+v, recorded one to %s", sent, topic)
		}
		var body map[string]any
		if err := json.Unmarshal(sent[0].body, &body); err != nil {
			h.Fatalf("notification is not JSON: %v", err)
		}
		delete(body, "ActionExecutedAfterMuteWindow")
		b, err := json.Marshal(body)
		if err != nil {
			h.Fatal(err)
		}
		jsonDiff(h, "alarm-1-sqs-msg", b, map[string]any{
			"AWSAccountId": h.Scope.Account.String(), "AlarmActions": []string{topic}, "AlarmArn": arn,
			"AlarmConfigurationUpdatedTimestamp": stamp(t0), "AlarmDescription": "testing cloudwatch alarms", "AlarmName": "golden",
			"InsufficientDataActions": []string{}, "NewStateReason": reason, "NewStateValue": "ALARM", "OKActions": []string{topic},
			"OldStateValue": "INSUFFICIENT_DATA", "Region": "US East (N. Virginia)", "StateChangeTime": stamp(at),
			"Trigger": map[string]any{
				"ComparisonOperator": "GreaterThanThreshold", "Dimensions": []map[string]string{{"name": "InstanceId", "value": "abc"}},
				"EvaluateLowSampleCountPercentile": "", "EvaluationPeriods": 2, "MetricName": "my-metric101", "Namespace": "test/breaching-alarm", "Period": 10,
				"Statistic": "AVERAGE", "StatisticType": "Statistic", "Threshold": 2.0, "TreatMissingData": "breaching", "Unit": "Seconds",
			},
		})

		// test_cloudwatch.snapshot.json:L2021-L2079. The recording masks the
		// two evaluated timestamps; they are the starts of d1 and d2 of the
		// window anchored at the evaluation (cwa 3.2).
		got := h.one("golden")
		goldenDescribed(h, "alarm-1-describe", got, cloudwatchapi.StateValueAlarm, reason, true)
		wantData := `{"version":"1.0","queryDate":"` + stamp(at) + `","unit":"Seconds","statistic":"Average","period":10,"recentDatapoints":[],"threshold":2.0,` +
			`"evaluatedDatapoints":[{"timestamp":"` + stamp(t0) + `"},{"timestamp":"` + stamp(t0.Add(-10*time.Second)) + `"}]}`
		if got.StateReasonData == nil || *got.StateReasonData != wantData {
			h.Errorf("alarm-1-describe StateReasonData = %v\nrecorded %s", got.StateReasonData, wantData)
		}
	})
}

// TestGoldenEnableDisableAlarmActions replays
// test_enable_disable_alarm_actions[json] (localstack@8b9a79f
// tests/aws/services/cloudwatch/test_cloudwatch.py:L1542;
// last_validated_date 2025-09-19T21:17:46Z): SetAlarmState to ALARM notifies
// the topic; with actions disabled SetAlarmState to OK changes the state and
// notifies nothing; enabling the actions again runs nothing. Under ignore
// with no data the evaluations in between keep the set state (cwa 2.3).
func TestGoldenEnableDisableAlarmActions(t *testing.T) {
	runAlarms(t, func(h *harness, fx *effects) {
		topic := h.goldenTopic("enable-topic")
		must(h, cloudwatchapi.OpPutMetricAlarm, &cloudwatchapi.PutMetricAlarmInput{
			AlarmName: "golden", AlarmDescription: new("testing cloudwatch alarms"), MetricName: new("my-metric101"), Namespace: new("test/enable"),
			Period: new(int32(10)), Threshold: new(2.0), Dimensions: goldenDims, Unit: new(cloudwatchapi.StandardUnitSeconds),
			Statistic: new(cloudwatchapi.StatisticAverage), OKActions: []string{topic}, AlarmActions: []string{topic}, EvaluationPeriods: new(int32(2)),
			ComparisonOperator: new(cloudwatchapi.ComparisonOperatorGreaterThanThreshold), TreatMissingData: new("ignore"),
		})
		// test_cloudwatch.snapshot.json:L2440-L2481
		goldenDescribed(h, "describe_alarm", h.one("golden"), cloudwatchapi.StateValueInsufficientData, "Unchecked: Initial alarm creation", true)

		h.advance(5 * time.Second)
		set := h.now()
		must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "golden", StateValue: cloudwatchapi.StateValueAlarm, StateReason: "testing alarm"})
		h.settle(30 * time.Second)
		// test_cloudwatch.snapshot.json:L2482-L2519
		sent := notifications(h, fx)
		if len(sent) != 1 || sent[0].topic != topic {
			h.Fatalf("notifications = %+v, recorded one to %s", sent, topic)
		}
		var msg struct {
			NewStateReason, NewStateValue, OldStateValue, StateChangeTime string
			Trigger                                                       struct{ TreatMissingData, EvaluateLowSampleCountPercentile string }
		}
		if err := json.Unmarshal(sent[0].body, &msg); err != nil {
			h.Fatalf("notification is not JSON: %v", err)
		}
		if msg.NewStateReason != "testing alarm" || msg.NewStateValue != "ALARM" || msg.OldStateValue != "INSUFFICIENT_DATA" || msg.StateChangeTime != stamp(set) ||
			msg.Trigger.TreatMissingData != "ignore" || msg.Trigger.EvaluateLowSampleCountPercentile != "" {
			h.Errorf("alarm-state-sqs-msg = %s", sent[0].body)
		}
		// test_cloudwatch.snapshot.json:L2520-L2561: no StateReasonData.
		got := h.one("golden")
		goldenDescribed(h, "alarm-state-describe", got, cloudwatchapi.StateValueAlarm, "testing alarm", true)
		if got.StateReasonData != nil {
			h.Errorf("alarm-state-describe StateReasonData = %s, recorded none", *got.StateReasonData)
		}

		must(h, cloudwatchapi.OpDisableAlarmActions, &cloudwatchapi.DisableAlarmActionsInput{AlarmNames: []string{"golden"}})
		must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "golden", StateValue: cloudwatchapi.StateValueOk, StateReason: "testing OK state"})
		// test_cloudwatch.snapshot.json:L2562-L2603
		goldenDescribed(h, "describe_alarm_disabled", h.one("golden"), cloudwatchapi.StateValueOk, "testing OK state", false)
		h.settle(30 * time.Second)
		// test_cloudwatch.snapshot.json:L2604-L2645
		goldenDescribed(h, "ok-state-action-disabled-describe", h.one("golden"), cloudwatchapi.StateValueOk, "testing OK state", false)

		must(h, cloudwatchapi.OpEnableAlarmActions, &cloudwatchapi.EnableAlarmActionsInput{AlarmNames: []string{"golden"}})
		h.settle(0)
		// test_cloudwatch.snapshot.json:L2646-L2687
		goldenDescribed(h, "describe_alarm_enabled", h.one("golden"), cloudwatchapi.StateValueOk, "testing OK state", true)
		if sent := notifications(h, fx); len(sent) != 1 {
			h.Errorf("notifications = %d, want only the ALARM one: the OK change ran with actions disabled (cwa 7.1)", len(sent))
		}
	})
}

// TestGoldenSetAlarm replays test_set_alarm[json] (localstack@8b9a79f
// tests/aws/services/cloudwatch/test_cloudwatch.py:L991; last_validated_date
// 2025-09-19T20:56:55Z): SetAlarmState to ALARM notifies the ALARM topic
// and to OK the OK topic, each message carrying the caller's reason and
// exactly the Trigger the test expects (test_cloudwatch.py:L1021-L1034,
// compared whole by check_message, L3297-L3321).
func TestGoldenSetAlarm(t *testing.T) {
	runAlarms(t, func(h *harness, fx *effects) {
		alarmTopic, okTopic := h.goldenTopic("topic-alarm"), h.goldenTopic("topic-ok")
		must(h, cloudwatchapi.OpPutMetricAlarm, &cloudwatchapi.PutMetricAlarmInput{
			AlarmName: "test-alarm", AlarmDescription: new("Test Alarm when CPU exceeds 50 percent"), MetricName: new("CPUUtilization-3"),
			Namespace: new("AWS/EC2"), ActionsEnabled: new(true), Period: new(int32(300)), Threshold: new(50.0),
			Dimensions: []cloudwatchapi.Dimension{{Name: "InstanceId", Value: "i-0317828c84edbe100"}}, Unit: new(cloudwatchapi.StandardUnitPercent),
			Statistic: new(cloudwatchapi.StatisticAverage), OKActions: []string{okTopic}, AlarmActions: []string{alarmTopic}, EvaluationPeriods: new(int32(2)),
			ComparisonOperator: new(cloudwatchapi.ComparisonOperatorGreaterThanThreshold), TreatMissingData: new("ignore"),
		})
		trigger := map[string]any{
			"MetricName": "CPUUtilization-3", "Namespace": "AWS/EC2", "Unit": "Percent", "Period": 300, "EvaluationPeriods": 2,
			"ComparisonOperator": "GreaterThanThreshold", "Threshold": 50.0, "TreatMissingData": "ignore", "EvaluateLowSampleCountPercentile": "",
			"Dimensions": []map[string]string{{"value": "i-0317828c84edbe100", "name": "InstanceId"}}, "StatisticType": "Statistic", "Statistic": "AVERAGE",
		}
		check := func(step string, n notification, topic string, state cloudwatchapi.StateValue, reason string) {
			h.Helper()
			var msg struct {
				NewStateValue, NewStateReason, AlarmName, AlarmDescription string
				Trigger                                                    json.RawMessage
			}
			if err := json.Unmarshal(n.body, &msg); err != nil {
				h.Fatalf("%s: notification is not JSON: %v", step, err)
			}
			if n.topic != topic || msg.NewStateValue != string(state) || msg.NewStateReason != reason || msg.AlarmName != "test-alarm" ||
				msg.AlarmDescription != "Test Alarm when CPU exceeds 50 percent" {
				h.Errorf("%s: notification to %s = %s", step, n.topic, n.body)
			}
			jsonDiff(h, step+" Trigger", msg.Trigger, trigger)
		}

		must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "test-alarm", StateReason: "testing alarm", StateValue: cloudwatchapi.StateValueAlarm})
		h.settle(time.Minute)
		sent := notifications(h, fx)
		if len(sent) != 1 {
			h.Fatalf("notifications after ALARM = %d, recorded one", len(sent))
		}
		check("ALARM", sent[0], alarmTopic, cloudwatchapi.StateValueAlarm, "testing alarm")
		// test_cloudwatch.snapshot.json:L5993-L6034
		goldenDescribed(h, "triggered-alarm", h.one("test-alarm"), cloudwatchapi.StateValueAlarm, "testing alarm", true)

		must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "test-alarm", StateReason: "resetting alarm", StateValue: cloudwatchapi.StateValueOk})
		h.settle(time.Minute)
		sent = notifications(h, fx)
		if len(sent) != 2 {
			h.Fatalf("notifications after OK = %d, recorded two", len(sent))
		}
		check("OK", sent[1], okTopic, cloudwatchapi.StateValueOk, "resetting alarm")
		// test_cloudwatch.snapshot.json:L6035-L6076
		goldenDescribed(h, "reset-alarm", h.one("test-alarm"), cloudwatchapi.StateValueOk, "resetting alarm", true)
	})
}

var uuidForm = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// TestGoldenAlarmLambdaTarget replays test_alarm_lambda_target
// (localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch.py:L2658;
// last_validated_date 2024-01-03T17:30:00Z): SetAlarmState to ALARM invokes
// the function of the ALARM action with the alarm event (cwa 6.1).
func TestGoldenAlarmLambdaTarget(t *testing.T) {
	runAlarms(t, func(h *harness, fx *effects) {
		fn := "arn:aws:lambda:" + h.Scope.Region.String() + ":" + h.Scope.Account.String() + ":function:fn-cw"
		t0 := h.now()
		must(h, cloudwatchapi.OpPutMetricAlarm, &cloudwatchapi.PutMetricAlarmInput{
			AlarmName: "golden", AlarmDescription: new("testing lambda alarm action"), MetricName: new("metric1"), Namespace: new("ns-golden"),
			Period: new(int32(10)), Threshold: new(2.0), Statistic: new(cloudwatchapi.StatisticAverage), OKActions: []string{}, AlarmActions: []string{fn},
			EvaluationPeriods: new(int32(2)), ComparisonOperator: new(cloudwatchapi.ComparisonOperatorGreaterThanThreshold), TreatMissingData: new("ignore"),
		})
		arn := h.alarmARN("golden")
		h.advance(5 * time.Second)
		set := h.now()
		must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "golden", StateValue: cloudwatchapi.StateValueAlarm, StateReason: "testing alarm"})
		h.settle(0)

		all := fx.sink.all()
		if len(all) != 1 {
			h.Fatalf("deliveries = %d, recorded one invocation", len(all))
		}
		target, ok := all[0].Request.Target.(delivery.LambdaFunction)
		if !ok || all[0].Request.Mechanism != delivery.CloudWatchToLambda || target.ARN.String() != fn || target.Invocation != delivery.InvocationEvent {
			h.Fatalf("delivery = %+v, want an asynchronous CloudWatchToLambda invoke of %s", all[0].Request, fn)
		}
		// The recording masks the generated metric id as <uuid:1>.
		var ids struct {
			AlarmData struct {
				Configuration struct {
					Metrics []struct {
						ID string `json:"id"`
					} `json:"metrics"`
				} `json:"configuration"`
			} `json:"alarmData"`
		}
		if err := json.Unmarshal(target.Event, &ids); err != nil {
			h.Fatalf("event is not JSON: %v", err)
		}
		metrics := ids.AlarmData.Configuration.Metrics
		if len(metrics) != 1 || !uuidForm.MatchString(metrics[0].ID) {
			h.Fatalf("event metrics = %+v, recorded one with a UUID id", metrics)
		}
		// test_cloudwatch.snapshot.json:L5-L42
		jsonDiff(h, "lambda-alarm-invocations", target.Event, map[string]any{
			"source": "aws.cloudwatch", "alarmArn": arn, "accountId": h.Scope.Account.String(), "time": stamp(set), "region": h.Scope.Region.String(),
			"alarmData": map[string]any{
				"alarmName":     "golden",
				"state":         map[string]any{"value": "ALARM", "reason": "testing alarm", "timestamp": stamp(set)},
				"previousState": map[string]any{"value": "INSUFFICIENT_DATA", "reason": "Unchecked: Initial alarm creation", "timestamp": stamp(t0)},
				"configuration": map[string]any{
					"description": "testing lambda alarm action",
					"metrics": []map[string]any{{
						"id": metrics[0].ID,
						"metricStat": map[string]any{
							"metric": map[string]any{"namespace": "ns-golden", "name": "metric1", "dimensions": map[string]string{}},
							"period": 10, "stat": "Average",
						},
						"returnData": true,
					}},
				},
			},
		})
	})
}
