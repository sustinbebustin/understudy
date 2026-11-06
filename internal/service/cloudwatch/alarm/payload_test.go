package alarm_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch/alarm"
)

const (
	alarmARN = "arn:aws:cloudwatch:us-east-1:123456789012:alarm:cpu-high"
	topicARN = "arn:aws:sns:us-east-1:123456789012:alerts"
)

func mustARN(t *testing.T, s string) aws.ARN {
	t.Helper()
	a, err := aws.ParseARN(s)
	if err != nil {
		t.Fatalf("ParseARN(%q): %v", s, err)
	}
	return a
}

// recordedSubject is the alarm of AWS's recorded notification
// (localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L6297-L6335,
// cwa 5.1): Average over 10 seconds in Seconds, dimension InstanceId abc,
// breaching above 21, TreatMissingData ignore, no DatapointsToAlarm.
func recordedSubject(t *testing.T) alarm.Subject {
	t.Helper()
	return alarm.Subject{
		ARN: mustARN(t, alarmARN),
		Config: &cloudwatchapi.PutMetricAlarmInput{
			AlarmName:          "cpu-high",
			AlarmActions:       []string{topicARN},
			MetricName:         new("CPUUtilization"),
			Namespace:          new("tst"),
			Statistic:          new(cloudwatchapi.StatisticAverage),
			Dimensions:         []cloudwatchapi.Dimension{{Name: "InstanceId", Value: "abc"}},
			Period:             new(int32(10)),
			Unit:               new(cloudwatchapi.StandardUnitSeconds),
			EvaluationPeriods:  new(int32(1)),
			Threshold:          new(21.0),
			ComparisonOperator: new(cloudwatchapi.ComparisonOperatorGreaterThanThreshold),
			TreatMissingData:   new("ignore"),
		},
		ActionsEnabled: true,
		ConfigUpdated:  time.Date(2026, 2, 26, 10, 0, 0, 0, time.UTC),
		MetricID:       "6a8f1e52-0d0b-4f0e-9e7b-2f2b3b1c9d01",
	}
}

const breachReason = "Threshold Crossed: 1 datapoint [21.5 (26/02/26 10:00:40)] was greater than the threshold (21.0)."

func recordedChange() alarm.Change {
	return alarm.Change{
		Old: alarm.StateRecord{Value: stateInsufficient, Reason: "Unchecked: Initial alarm creation", Timestamp: time.Date(2026, 2, 26, 10, 0, 0, 0, time.UTC)},
		New: alarm.StateRecord{Value: stateAlarm, Reason: breachReason, Timestamp: time.Date(2026, 2, 26, 10, 0, 50, 123_000_000, time.UTC)},
	}
}

// The notification body has the schema's fields in the schema's order
// (cwa 5.1, aws-doc alarm-sns-schema) with the values AWS recorded for
// this alarm: Statistic AVERAGE, StatisticType Statistic, Unit Seconds,
// lower-case dimension keys, Threshold 21.0, EvaluateLowSampleCountPercentile
// "", no DatapointsToAlarm, ActionExecutedAfterMuteWindow false, and no
// reason data (cwa 11). AlarmDescription null, the value-then-name order
// inside a dimension and the place of ActionExecutedAfterMuteWindow follow
// cloudwatch.alarm.sns.unset-fields (CWA-11); Region's long name follows
// cloudwatch.alarm.sns.subject (CWA-12).
func TestSNSMessageMatchesTheRecording(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.sns.unset-fields", "description-null,treat-missing-data-empty,unit-null,low-sample-empty,datapoints-to-alarm-omitted,mute-window-false")
	followsSwitch(t, "cloudwatch.alarm.sns.subject", "state-quoted-name-in-region-long-name,100-chars")
	got := alarm.SNSMessage(recordedSubject(t), recordedChange())
	want := `{"AlarmName":"cpu-high","AlarmDescription":null,"AWSAccountId":"123456789012",` +
		`"AlarmConfigurationUpdatedTimestamp":"2026-02-26T10:00:00.000+0000","NewStateValue":"ALARM",` +
		`"NewStateReason":"` + breachReason + `","StateChangeTime":"2026-02-26T10:00:50.123+0000",` +
		`"Region":"US East (N. Virginia)","AlarmArn":"` + alarmARN + `","OldStateValue":"INSUFFICIENT_DATA",` +
		`"ActionExecutedAfterMuteWindow":false,"OKActions":[],"AlarmActions":["` + topicARN + `"],"InsufficientDataActions":[],` +
		`"Trigger":{"MetricName":"CPUUtilization","Namespace":"tst","StatisticType":"Statistic","Statistic":"AVERAGE",` +
		`"Unit":"Seconds","Dimensions":[{"value":"abc","name":"InstanceId"}],"Period":10,"EvaluationPeriods":1,` +
		`"ComparisonOperator":"GreaterThanThreshold","Threshold":21.0,"TreatMissingData":"ignore",` +
		`"EvaluateLowSampleCountPercentile":""}}`
	if got != want {
		t.Errorf("SNSMessage =\n%s\nwant\n%s", got, want)
	}
}

// Fields an alarm may leave unset or set: Unit null without one and
// Statistic SAMPLE_COUNT (localstack@8b9a79f
// tests/aws/services/cloudwatch/test_cloudwatch_metrics.snapshot.json:L183, L187);
// SUM, MAXIMUM captured and MINIMUM by the same rule (cwa 5.1);
// DatapointsToAlarm an integer after EvaluationPeriods when set,
// TreatMissingData "" when unset, a description as sent, dimensions sorted
// by name (cloudwatch.alarm.sns.unset-fields, CWA-11).
func TestSNSMessageFields(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.sns.unset-fields", "description-null,treat-missing-data-empty,unit-null,low-sample-empty,datapoints-to-alarm-omitted,mute-window-false")
	s := recordedSubject(t)
	s.Config.Statistic = new(cloudwatchapi.StatisticSampleCount)
	s.Config.Unit = nil
	s.Config.TreatMissingData = nil
	s.Config.AlarmDescription = new("queue depth")
	s.Config.EvaluationPeriods = new(int32(3))
	s.Config.DatapointsToAlarm = new(int32(2))
	s.Config.Dimensions = []cloudwatchapi.Dimension{{Name: "Zone", Value: "b"}, {Name: "App", Value: "a"}}
	got := alarm.SNSMessage(s, recordedChange())
	for _, part := range []string{
		`"AlarmDescription":"queue depth",`,
		`"Statistic":"SAMPLE_COUNT","Unit":null,`,
		`"Dimensions":[{"value":"a","name":"App"},{"value":"b","name":"Zone"}]`,
		`"EvaluationPeriods":3,"DatapointsToAlarm":2,"ComparisonOperator":`,
		`"TreatMissingData":"",`,
	} {
		if !strings.Contains(got, part) {
			t.Errorf("SNSMessage lacks %s:\n%s", part, got)
		}
	}
	for stat, want := range map[cloudwatchapi.Statistic]string{
		cloudwatchapi.StatisticSum: "SUM", cloudwatchapi.StatisticMaximum: "MAXIMUM", cloudwatchapi.StatisticMinimum: "MINIMUM", cloudwatchapi.StatisticAverage: "AVERAGE",
	} {
		s.Config.Statistic = new(stat)
		if got := alarm.SNSMessage(s, recordedChange()); !strings.Contains(got, `"Statistic":"`+want+`"`) {
			t.Errorf("Statistic %s: SNSMessage lacks %q:\n%s", stat, want, got)
		}
	}
}

// The Subject is <NEW STATE>: "<name>" in <region long name> in every
// captured notification, and a name too long for 100 characters is cut so
// that the name and "..." make it exactly 100 (cwa 5.2), under
// cloudwatch.alarm.sns.subject (UNVERIFIED, CWA-12).
func TestSNSSubject(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.sns.subject", "state-quoted-name-in-region-long-name,100-chars")
	subject := func(name, region string) alarm.Subject {
		return alarm.Subject{ARN: mustARN(t, "arn:aws:cloudwatch:"+region+":123456789012:alarm:"+name), Config: &cloudwatchapi.PutMetricAlarmInput{AlarmName: name}}
	}
	for _, tc := range []struct {
		name, region string
		to           cloudwatchapi.StateValue
		want         string
	}{
		// widdix/cloudwatch-alarm-to-slack fb66af9 handler.js:L58
		{"cw-to-slack-Alarm-9THDKWBS1876", "us-east-1", stateAlarm, `ALARM: "cw-to-slack-Alarm-9THDKWBS1876" in US East (N. Virginia)`},
		// linuxacademy/la-aws-security_specialty 9e1e27c .../sns_event.json:L11
		{
			"cfst-716-ac22d3f1c22bd4cb8c2a626d6879e183-InvalidSSHLoginAlarm-NNNFH7H1BW17", "us-east-1", stateAlarm,
			`ALARM: "cfst-716-ac22d3f1c22bd4cb8c2a626d6879e183-InvalidSSHLoginAlarm-..." in US East (N. Virginia)`,
		},
		// alerta/alerta 4ac0106 tests/test_webhooks.py:L84, with today's
		// long name of eu-west-1.
		{"NotificationCount", "eu-west-1", stateInsufficient, `INSUFFICIENT_DATA: "NotificationCount" in Europe (Ireland)`},
		{"fine", "us-west-2", stateOK, `OK: "fine" in US West (Oregon)`},
		// A code the table does not hold prints as itself.
		{"fine", "xx-test-9", stateOK, `OK: "fine" in xx-test-9`},
	} {
		got := alarm.SNSSubject(subject(tc.name, tc.region), tc.to)
		if got != tc.want {
			t.Errorf("SNSSubject(%s in %s) =\n%q\nwant\n%q", tc.name, tc.region, got, tc.want)
		}
		if len(got) > 100 {
			t.Errorf("SNSSubject is %d characters, want at most 100 (cwa 5.2)", len(got))
		}
	}
}

// RegionName holds the General Reference long names
// (https://docs.aws.amazon.com/general/latest/gr/rande.html), seeded as
// cloudwatch.alarm.sns.subject says (cwa 5.2, CWA-12).
func TestRegionName(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.sns.subject", "state-quoted-name-in-region-long-name,100-chars")
	for code, want := range map[string]string{
		"us-east-1": "US East (N. Virginia)", "us-east-2": "US East (Ohio)", "us-west-1": "US West (N. California)",
		"us-west-2": "US West (Oregon)", "eu-west-1": "Europe (Ireland)", "eu-central-1": "Europe (Frankfurt)",
		"ap-southeast-2": "Asia Pacific (Sydney)",
	} {
		if got, ok := alarm.RegionName(code); !ok || got != want {
			t.Errorf("RegionName(%s) = %q %v, want %q", code, got, ok, want)
		}
	}
	if got, ok := alarm.RegionName("xx-test-9"); ok || got != "xx-test-9" {
		t.Errorf("RegionName(xx-test-9) = %q %v, want the code and false", got, ok)
	}
}

// A FIFO topic's message group id is a hash of the alarm ARN (cwa 5.3,
// aws-doc): lower-case hex SHA-256 (cloudwatch.alarm.sns.fifo-group,
// CWA-13).
func TestFIFOGroupID(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.sns.fifo-group", "sha256-hex-of-alarm-arn,uuid-dedup-id")
	h := sha256.Sum256([]byte(alarmARN))
	if got, want := alarm.FIFOGroupID(alarmARN), hex.EncodeToString(h[:]); got != want {
		t.Errorf("FIFOGroupID = %q, want %q", got, want)
	}
}

func parseJSON(t *testing.T, what string, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s is not a JSON object: %v: %s", what, err, b)
	}
	return m
}

func mustJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	return parseJSON(t, "expectation", []byte(s))
}

// The Lambda action's event has the shape AWS recorded for a SetAlarmState
// change (localstack@8b9a79f tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L5-L42,
// cwa 6.1): the region as a code, time with milliseconds and +0000, the
// single metric as a one-element metrics array with the generated id.
func TestLambdaEventMatchesTheRecording(t *testing.T) {
	s := recordedSubject(t)
	s.Config.Dimensions = []cloudwatchapi.Dimension{}
	s.Config.Unit = nil
	s.Config.AlarmDescription = new("testing")
	ch := recordedChange()
	ch.New.Reason = "testing alarm"
	got := parseJSON(t, "LambdaEvent", alarm.LambdaEvent(s, ch))
	want := mustJSON(t, `{"source":"aws.cloudwatch","alarmArn":"`+alarmARN+`","accountId":"123456789012",
		"time":"2026-02-26T10:00:50.123+0000","region":"us-east-1",
		"alarmData":{"alarmName":"cpu-high",
			"state":{"value":"ALARM","reason":"testing alarm","timestamp":"2026-02-26T10:00:50.123+0000"},
			"previousState":{"value":"INSUFFICIENT_DATA","reason":"Unchecked: Initial alarm creation","timestamp":"2026-02-26T10:00:00.000+0000"},
			"configuration":{"description":"testing","metrics":[{"id":"6a8f1e52-0d0b-4f0e-9e7b-2f2b3b1c9d01",
				"metricStat":{"metric":{"namespace":"tst","name":"CPUUtilization","dimensions":{}},"period":10,"stat":"Average"},
				"returnData":true}]}}}`)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LambdaEvent =\n%v\nwant\n%v", got, want)
	}
}

// reasonData appears when the state has one and metricStat.unit when the
// alarm has a unit (aws-doc alarms-and-actions-Lambda, cwa 6.1); description
// is absent without an AlarmDescription (UNVERIFIED, CWA-15,
// cloudwatch.alarm.events.metric-id).
func TestLambdaEventOptionalFields(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.events.metric-id", "one-per-configuration")
	ch := recordedChange()
	ch.New.ReasonData = new(`{"version":"1.0"}`)
	raw := alarm.LambdaEvent(recordedSubject(t), ch)
	var got struct {
		AlarmData struct {
			State         map[string]any `json:"state"`
			PreviousState map[string]any `json:"previousState"`
			Configuration struct {
				Description *string `json:"description"`
				Metrics     []struct {
					MetricStat struct {
						Metric struct {
							Dimensions map[string]string `json:"dimensions"`
						} `json:"metric"`
						Unit *string `json:"unit"`
					} `json:"metricStat"`
				} `json:"metrics"`
			} `json:"configuration"`
		} `json:"alarmData"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("LambdaEvent is not JSON: %v: %s", err, raw)
	}
	data := got.AlarmData
	if data.State["reasonData"] != `{"version":"1.0"}` {
		t.Errorf("state.reasonData = %v, want the StateReasonData string", data.State["reasonData"])
	}
	if _, ok := data.PreviousState["reasonData"]; ok {
		t.Errorf("previousState carries reasonData without one: %v", data.PreviousState)
	}
	if data.Configuration.Description != nil {
		t.Errorf("configuration carries description %q without an AlarmDescription", *data.Configuration.Description)
	}
	if len(data.Configuration.Metrics) != 1 {
		t.Fatalf("metrics = %+v, want one", data.Configuration.Metrics)
	}
	stat := data.Configuration.Metrics[0].MetricStat
	if stat.Unit == nil || *stat.Unit != "Seconds" || !reflect.DeepEqual(stat.Metric.Dimensions, map[string]string{"InstanceId": "abc"}) {
		t.Errorf("metricStat = %+v, want unit Seconds and dimensions {InstanceId: abc}", stat)
	}
}

// The "CloudWatch Alarm State Change" detail has AWS's documented shape,
// with reasonData as the StateReasonData string and never muteDetail or
// alarmTags (cwa 6.2, aws-doc cloudwatch-and-eventbridge).
func TestStateChangeDetail(t *testing.T) {
	ch := recordedChange()
	ch.New.ReasonData = new(`{"version":"1.0","threshold":21.0}`)
	got := parseJSON(t, "StateChangeDetail", []byte(alarm.StateChangeDetail(recordedSubject(t), ch)))
	want := mustJSON(t, `{"alarmName":"cpu-high",
		"configuration":{"metrics":[{"id":"6a8f1e52-0d0b-4f0e-9e7b-2f2b3b1c9d01",
			"metricStat":{"metric":{"namespace":"tst","name":"CPUUtilization","dimensions":{"InstanceId":"abc"}},"period":10,"stat":"Average","unit":"Seconds"},
			"returnData":true}]},
		"previousState":{"reason":"Unchecked: Initial alarm creation","timestamp":"2026-02-26T10:00:00.000+0000","value":"INSUFFICIENT_DATA"},
		"state":{"reason":"`+breachReason+`","reasonData":"{\"version\":\"1.0\",\"threshold\":21.0}","timestamp":"2026-02-26T10:00:50.123+0000","value":"ALARM"}}`)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("StateChangeDetail =\n%v\nwant\n%v", got, want)
	}
}

// configChangeSubject is the metric alarm of AWS's update example (cwa 6.2),
// at threshold.
func configChangeSubject(t *testing.T, threshold float64, metricID string) alarm.Subject {
	t.Helper()
	s := recordedSubject(t)
	s.Config.Threshold = new(threshold)
	s.Config.AlarmDescription = new("cpu")
	s.MetricID = metricID
	return s
}

// The "CloudWatch Alarm Configuration Change" detail of an update carries
// the configuration and the previous one, each with its own metric id
// (AWS's update example, cwa 6.2, aws-doc); numbers print as Java doubles
// ("threshold":3.0 recorded, where the documented example prints 80), under
// cloudwatch.alarm.number-format and cloudwatch.alarm.events.metric-id
// (CWA-15).
func TestConfigurationChangeDetail(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.events.metric-id", "one-per-configuration")
	followsSwitch(t, "cloudwatch.alarm.number-format", "java-double")
	cur := configChangeSubject(t, 80, "86bfa85f-b14c-ebf7-8916-7da014ce23c0")
	prev := configChangeSubject(t, 70, "d6bfa85f-893e-b052-a58b-4f9295c9111a")
	state := alarm.StateRecord{Value: stateAlarm, Timestamp: time.Date(2026, 2, 26, 10, 0, 50, 123_000_000, time.UTC)}
	raw := alarm.ConfigurationChangeDetail(alarm.ConfigUpdate, cur, &prev, state)
	got := parseJSON(t, "ConfigurationChangeDetail", []byte(raw))
	config := func(threshold int, id string) string {
		return `{"evaluationPeriods":1,"threshold":` + map[int]string{80: "80.0", 70: "70.0"}[threshold] + `,
			"comparisonOperator":"GreaterThanThreshold","treatMissingData":"ignore",
			"metrics":[{"id":"` + id + `","metricStat":{"metric":{"namespace":"tst","name":"CPUUtilization","dimensions":{"InstanceId":"abc"}},"period":10,"stat":"Average","unit":"Seconds"},"returnData":true}],
			"alarmName":"cpu-high","description":"cpu","actionsEnabled":true,"timestamp":"2026-02-26T10:00:00.000+0000",
			"okActions":[],"alarmActions":["` + topicARN + `"],"insufficientDataActions":[]}`
	}
	want := mustJSON(t, `{"alarmName":"cpu-high","operation":"update",
		"state":{"value":"ALARM","timestamp":"2026-02-26T10:00:50.123+0000"},
		"configuration":`+config(80, cur.MetricID)+`,"previousConfiguration":`+config(70, prev.MetricID)+`}`)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ConfigurationChangeDetail =\n%v\nwant\n%v", got, want)
	}
	if !regexp.MustCompile(`"threshold":80\.0[,}]`).MatchString(raw) || !regexp.MustCompile(`"threshold":70\.0[,}]`).MatchString(raw) {
		t.Errorf("thresholds do not print as Java doubles: %s", raw)
	}

	for op, name := range map[alarm.ConfigOp]string{alarm.ConfigCreate: "create", alarm.ConfigDelete: "delete"} {
		if op.String() != name {
			t.Errorf("ConfigOp(%d).String() = %q, want %q", op, op.String(), name)
		}
		m := parseJSON(t, "ConfigurationChangeDetail", []byte(alarm.ConfigurationChangeDetail(op, cur, nil, state)))
		if m["operation"] != name {
			t.Errorf("%s: operation = %v", name, m["operation"])
		}
		if _, ok := m["previousConfiguration"]; ok {
			t.Errorf("%s carries previousConfiguration, which only an update has", name)
		}
	}
	if alarm.ConfigUpdate.String() != "update" {
		t.Errorf("ConfigUpdate.String() = %q, want update", alarm.ConfigUpdate.String())
	}
}

// A StateUpdate history item: HistorySummary "Alarm updated from <old> to
// <new>" (recorded, localstack@8b9a79f
// tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L6336-L6380)
// and HistoryData with the reason data nested as an object, its bytes kept
// ("threshold":70.0 inside AWS's example, aws/aws-cli e9993cf
// awscli/examples/cloudwatch/describe-alarm-history.rst:L16), absent when
// the state has none (cwa 8.2).
func TestStateUpdate(t *testing.T) {
	ch := recordedChange()
	ch.New.ReasonData = new(`{"version":"1.0","statistic":"Average","period":10,"recentDatapoints":[21.5],"threshold":21.0}`)
	summary, data := alarm.StateUpdate(ch)
	if summary != "Alarm updated from INSUFFICIENT_DATA to ALARM" {
		t.Errorf("summary = %q", summary)
	}
	want := `{"version":"1.0","oldState":{"stateValue":"INSUFFICIENT_DATA","stateReason":"Unchecked: Initial alarm creation"},` +
		`"newState":{"stateValue":"ALARM","stateReason":"` + breachReason + `",` +
		`"stateReasonData":{"version":"1.0","statistic":"Average","period":10,"recentDatapoints":[21.5],"threshold":21.0}}}`
	if data != want {
		t.Errorf("data =\n%s\nwant\n%s", data, want)
	}

	set := alarm.Change{
		Old: alarm.StateRecord{Value: stateAlarm, Reason: "testing purposes"},
		New: alarm.StateRecord{Value: stateOK, Reason: "fine"},
	}
	summary, data = alarm.StateUpdate(set)
	if summary != "Alarm updated from ALARM to OK" ||
		data != `{"version":"1.0","oldState":{"stateValue":"ALARM","stateReason":"testing purposes"},"newState":{"stateValue":"OK","stateReason":"fine"}}` {
		t.Errorf("StateUpdate = %q %s", summary, data)
	}
}

// HistorySummary and HistoryData fit the model's 255 and 4095 characters
// even for the longest reason and reason data (model
// com.amazonaws.cloudwatch#HistorySummary, #HistoryData; cwa 12.5).
func TestStateUpdateRespectsTheLimits(t *testing.T) {
	longData := `{"version":"1.0","pad":"` + strings.Repeat("x", alarm.MaxReasonData-26) + `"}`
	if len(longData) != alarm.MaxReasonData {
		t.Fatalf("test data is %d characters", len(longData))
	}
	ch := alarm.Change{
		Old: alarm.StateRecord{Value: stateOK, Reason: strings.Repeat("o", alarm.MaxReason), ReasonData: &longData},
		New: alarm.StateRecord{Value: stateAlarm, Reason: strings.Repeat("n", alarm.MaxReason), ReasonData: &longData},
	}
	summary, data := alarm.StateUpdate(ch)
	if len(summary) > 255 || len(data) > 4095 {
		t.Errorf("summary %d, data %d characters; want at most 255 and 4095", len(summary), len(data))
	}
	if !json.Valid([]byte(data)) {
		t.Errorf("HistoryData is not JSON: %.200s", data)
	}
}

// Action ARNs are classified by the forms of the model doc of
// PutMetricAlarmInput$AlarmActions (cwa 6.1, 7.2): SNS and Lambda run,
// every other documented kind is not run, and anything else is
// unrecognized.
func TestParseAction(t *testing.T) {
	fn := "arn:aws:lambda:us-east-1:123456789012:function:on-alarm"
	for _, tc := range []struct {
		arn  string
		want alarm.Action
	}{
		{topicARN, alarm.SNSAction{Topic: mustARN(t, topicARN)}},
		{"arn:aws:sns:us-east-1:123456789012:alerts.fifo", alarm.SNSAction{Topic: mustARN(t, "arn:aws:sns:us-east-1:123456789012:alerts.fifo")}},
		{fn, alarm.LambdaAction{Function: mustARN(t, fn)}},
		{fn + ":prod", alarm.LambdaAction{Function: mustARN(t, fn), Qualifier: "prod"}},
		{fn + ":7", alarm.LambdaAction{Function: mustARN(t, fn), Qualifier: "7"}},
		{"arn:aws:automate:us-east-1:ec2:stop", alarm.NotRunAction{ARN: "arn:aws:automate:us-east-1:ec2:stop", Kind: alarm.NotRunEC2}},
		{"arn:aws:automate:us-east-1:ec2:terminate", alarm.NotRunAction{ARN: "arn:aws:automate:us-east-1:ec2:terminate", Kind: alarm.NotRunEC2}},
		{"arn:aws:automate:us-east-1:ec2:reboot", alarm.NotRunAction{ARN: "arn:aws:automate:us-east-1:ec2:reboot", Kind: alarm.NotRunEC2}},
		{"arn:aws:automate:us-east-1:ec2:recover", alarm.NotRunAction{ARN: "arn:aws:automate:us-east-1:ec2:recover", Kind: alarm.NotRunEC2}},
		{
			"arn:aws:swf:us-east-1:123456789012:action/actions/AWS_EC2.InstanceId.Stop/1.0",
			alarm.NotRunAction{ARN: "arn:aws:swf:us-east-1:123456789012:action/actions/AWS_EC2.InstanceId.Stop/1.0", Kind: alarm.NotRunEC2},
		},
		{
			"arn:aws:autoscaling:us-east-1:123456789012:scalingPolicy:1a2b:autoScalingGroupName/web:policyName/up",
			alarm.NotRunAction{ARN: "arn:aws:autoscaling:us-east-1:123456789012:scalingPolicy:1a2b:autoScalingGroupName/web:policyName/up", Kind: alarm.NotRunAutoScaling},
		},
		{
			"arn:aws:ssm:us-east-1:123456789012:opsitem:3#CATEGORY=Availability",
			alarm.NotRunAction{ARN: "arn:aws:ssm:us-east-1:123456789012:opsitem:3#CATEGORY=Availability", Kind: alarm.NotRunSystemsManager},
		},
		{
			"arn:aws:ssm-incidents::123456789012:responseplan/oncall",
			alarm.NotRunAction{ARN: "arn:aws:ssm-incidents::123456789012:responseplan/oncall", Kind: alarm.NotRunSystemsManager},
		},
		{
			"arn:aws:aiops:us-east-1:123456789012:investigation-group:abc123",
			alarm.NotRunAction{ARN: "arn:aws:aiops:us-east-1:123456789012:investigation-group:abc123", Kind: alarm.NotRunInvestigation},
		},
		{"arn:aws:sqs:us-east-1:123456789012:jobs", alarm.NotRunAction{ARN: "arn:aws:sqs:us-east-1:123456789012:jobs", Kind: alarm.NotRunUnrecognized}},
		{"not an arn", alarm.NotRunAction{ARN: "not an arn", Kind: alarm.NotRunUnrecognized}},
		// A topic or function outside any account and Region understudy
		// serves cannot be delivered to.
		{"arn:aws:sns:us-east-1:111:alerts", alarm.NotRunAction{ARN: "arn:aws:sns:us-east-1:111:alerts", Kind: alarm.NotRunUnrecognized}},
		{"arn:aws:sns::123456789012:alerts", alarm.NotRunAction{ARN: "arn:aws:sns::123456789012:alerts", Kind: alarm.NotRunUnrecognized}},
		{
			"arn:aws:lambda:us-east-1:12345:function:on-alarm",
			alarm.NotRunAction{ARN: "arn:aws:lambda:us-east-1:12345:function:on-alarm", Kind: alarm.NotRunUnrecognized},
		},
	} {
		if got := alarm.ParseAction(tc.arn); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ParseAction(%q) = %#v\nwant %#v", tc.arn, got, tc.want)
		}
	}
	for kind, name := range map[alarm.NotRunKind]string{
		alarm.NotRunEC2: "ec2", alarm.NotRunAutoScaling: "autoscaling", alarm.NotRunSystemsManager: "ssm",
		alarm.NotRunInvestigation: "investigation", alarm.NotRunUnrecognized: "unrecognized",
	} {
		if kind.String() != name {
			t.Errorf("NotRunKind(%d).String() = %q, want %q", kind, kind.String(), name)
		}
	}
}

// The EventBridge source and detail types (cwa 6.2, aws-doc).
func TestEventNames(t *testing.T) {
	if alarm.EventSource != "aws.cloudwatch" || alarm.DetailTypeStateChange != "CloudWatch Alarm State Change" ||
		alarm.DetailTypeConfigChange != "CloudWatch Alarm Configuration Change" {
		t.Errorf("event names = %q %q %q", alarm.EventSource, alarm.DetailTypeStateChange, alarm.DetailTypeConfigChange)
	}
}
