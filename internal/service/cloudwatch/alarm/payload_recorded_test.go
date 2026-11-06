package alarm_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch/alarm"
)

// The recording's topic, function and namespace (masked account).
const (
	recTopic = "arn:aws:sns:us-east-2:123456789012:us1103-cwa"
	recFn    = "arn:aws:lambda:us-east-2:123456789012:function:us1103-cwa"
)

func recTime(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// The notification bodies AWS published, byte for byte: ActionExecutedAfterMuteWindow
// after OldStateValue, Trigger.Dimensions sorted by name whatever order
// they were sent in, DatapointsToAlarm after EvaluationPeriods, and an
// ExtendedStatistic alarm's Trigger without Statistic
// (cloudwatch.alarm.sns.unset-fields; cwa 5.1; CWA-11, recorded
// 2026-11-03: testdata/parity/sqs/CWA-11/receive-*.json).
func TestSNSMessageRecorded(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.sns.unset-fields", "description-null,treat-missing-data-empty,unit-null,low-sample-empty,datapoints-to-alarm-omitted,mute-window-false")
	full := alarm.Subject{
		ARN: mustARN(t, "arn:aws:cloudwatch:us-east-2:123456789012:alarm:us1103-sns-full"),
		Config: &cloudwatchapi.PutMetricAlarmInput{
			AlarmName: "us1103-sns-full", AlarmDescription: new(`full "quoted" description`),
			Namespace: new("us1103/cwa"), MetricName: new("sns-none"),
			Dimensions: []cloudwatchapi.Dimension{{Name: "b", Value: "2"}, {Name: "a", Value: "1"}},
			Statistic:  new(cloudwatchapi.StatisticMaximum), Unit: new(cloudwatchapi.StandardUnitSeconds),
			Period: new(int32(60)), EvaluationPeriods: new(int32(2)), DatapointsToAlarm: new(int32(1)), Threshold: new(3.0),
			ComparisonOperator: new(cloudwatchapi.ComparisonOperatorGreaterThanThreshold), TreatMissingData: new("ignore"),
			AlarmActions: []string{recTopic}, OKActions: []string{recTopic},
		},
		ConfigUpdated: recTime(t, "2026-11-03T03:15:36.700Z"),
	}
	p90 := alarm.Subject{
		ARN: mustARN(t, "arn:aws:cloudwatch:us-east-2:123456789012:alarm:us1103-sns-p90"),
		Config: &cloudwatchapi.PutMetricAlarmInput{
			AlarmName: "us1103-sns-p90", Namespace: new("us1103/cwa"), MetricName: new("sns-none"),
			ExtendedStatistic: new("p90"), EvaluateLowSampleCountPercentile: new("ignore"),
			Period: new(int32(60)), EvaluationPeriods: new(int32(1)), Threshold: new(3.0),
			ComparisonOperator: new(cloudwatchapi.ComparisonOperatorGreaterThanThreshold), TreatMissingData: new("ignore"),
			AlarmActions: []string{recTopic},
		},
		ConfigUpdated: recTime(t, "2026-11-03T03:15:37.218Z"),
	}
	set := func(reason, at string) alarm.Change {
		return alarm.Change{
			Old: alarm.StateRecord{Value: stateInsufficient, Reason: "Unchecked: Initial alarm creation"},
			New: alarm.StateRecord{Value: stateAlarm, Reason: reason, Timestamp: recTime(t, at)},
		}
	}
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{
			"sns-full", alarm.SNSMessage(full, set("cwa sns-full", "2026-11-03T03:15:39.316Z")),
			`{"AlarmName":"us1103-sns-full","AlarmDescription":"full \"quoted\" description","AWSAccountId":"123456789012","AlarmConfigurationUpdatedTimestamp":"2026-11-03T03:15:36.700+0000","NewStateValue":"ALARM","NewStateReason":"cwa sns-full","StateChangeTime":"2026-11-03T03:15:39.316+0000","Region":"US East (Ohio)","AlarmArn":"arn:aws:cloudwatch:us-east-2:123456789012:alarm:us1103-sns-full","OldStateValue":"INSUFFICIENT_DATA","ActionExecutedAfterMuteWindow":false,"OKActions":["arn:aws:sns:us-east-2:123456789012:us1103-cwa"],"AlarmActions":["arn:aws:sns:us-east-2:123456789012:us1103-cwa"],"InsufficientDataActions":[],"Trigger":{"MetricName":"sns-none","Namespace":"us1103/cwa","StatisticType":"Statistic","Statistic":"MAXIMUM","Unit":"Seconds","Dimensions":[{"value":"1","name":"a"},{"value":"2","name":"b"}],"Period":60,"EvaluationPeriods":2,"DatapointsToAlarm":1,"ComparisonOperator":"GreaterThanThreshold","Threshold":3.0,"TreatMissingData":"ignore","EvaluateLowSampleCountPercentile":""}}`,
		},
		{
			"sns-p90", alarm.SNSMessage(p90, set("cwa sns-p90", "2026-11-03T03:15:39.821Z")),
			`{"AlarmName":"us1103-sns-p90","AlarmDescription":null,"AWSAccountId":"123456789012","AlarmConfigurationUpdatedTimestamp":"2026-11-03T03:15:37.218+0000","NewStateValue":"ALARM","NewStateReason":"cwa sns-p90","StateChangeTime":"2026-11-03T03:15:39.821+0000","Region":"US East (Ohio)","AlarmArn":"arn:aws:cloudwatch:us-east-2:123456789012:alarm:us1103-sns-p90","OldStateValue":"INSUFFICIENT_DATA","ActionExecutedAfterMuteWindow":false,"OKActions":[],"AlarmActions":["arn:aws:sns:us-east-2:123456789012:us1103-cwa"],"InsufficientDataActions":[],"Trigger":{"MetricName":"sns-none","Namespace":"us1103/cwa","StatisticType":"ExtendedStatistic","ExtendedStatistic":"p90","Unit":null,"Dimensions":[],"Period":60,"EvaluationPeriods":1,"ComparisonOperator":"GreaterThanThreshold","Threshold":3.0,"TreatMissingData":"ignore","EvaluateLowSampleCountPercentile":"ignore"}}`,
		},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: SNSMessage =\n%s\nwant\n%s", tc.name, tc.got, tc.want)
		}
	}
}

// AWS sent a FIFO topic's notification with MessageGroupId the SHA-256 hex
// of the alarm ARN and a MessageDeduplicationId that is a v4 UUID matching
// no digest of the ARN (cloudwatch.alarm.sns.fifo-group; cwa 5.3; CWA-13,
// recorded 2026-11-03: testdata/parity/sqs/CWA-13/receive-1.json). One per
// transition: two transitions never share it.
func TestFIFODedupIDRecorded(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.sns.fifo-group", "sha256-hex-of-alarm-arn,uuid-dedup-id")
	v4 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	a, b := alarm.FIFODedupID(alarmARN, "req-1"), alarm.FIFODedupID(alarmARN, "req-2")
	if !v4.MatchString(a) || !v4.MatchString(b) {
		t.Errorf("FIFODedupID = %q, %q; want v4 UUIDs", a, b)
	}
	if a == b {
		t.Errorf("FIFODedupID is %q for two transitions", a)
	}
}

// evShape is CWA-15's alarm at one configuration: Maximum over 60 s, 2
// periods, 1 to alarm, ignore, the function for ALARM and OK.
func evShape(t *testing.T, threshold float64, description *string, id, configured string) alarm.Subject {
	t.Helper()
	return alarm.Subject{
		ARN: mustARN(t, "arn:aws:cloudwatch:us-east-2:123456789012:alarm:us1103-ev-shape"),
		Config: &cloudwatchapi.PutMetricAlarmInput{
			AlarmName: "us1103-ev-shape", AlarmDescription: description, Namespace: new("us1103/cwa"), MetricName: new("ev-none"),
			Statistic: new(cloudwatchapi.StatisticMaximum), Period: new(int32(60)), EvaluationPeriods: new(int32(2)),
			DatapointsToAlarm: new(int32(1)), Threshold: new(threshold),
			ComparisonOperator: new(cloudwatchapi.ComparisonOperatorGreaterThanThreshold), TreatMissingData: new("ignore"),
			AlarmActions: []string{recFn}, OKActions: []string{recFn},
		},
		ActionsEnabled: true, ConfigUpdated: recTime(t, configured), MetricID: id,
	}
}

// The configuration and state change event details AWS sent, byte for
// byte: datapointsToAlarm after evaluationPeriods, numbers as Java doubles
// ("threshold":3.0), the state's reason on a delete only, and a state
// change's keys alarmName, state, previousState, configuration with
// description after metrics (cloudwatch.alarm.number-format,
// cloudwatch.alarm.events.metric-id; cwa 6.2; CWA-15, recorded 2026-11-03:
// testdata/parity/sqs/CWA-15/events-*.json).
func TestEventDetailsRecorded(t *testing.T) {
	followsSwitch(t, "cloudwatch.alarm.number-format", "java-double")
	followsSwitch(t, "cloudwatch.alarm.events.metric-id", "one-per-configuration")
	first := evShape(t, 3, nil, "c2d0cfb4-1516-36f2-5a1f-d2a9f14c031b", "2026-11-03T03:16:06.316Z")
	second := evShape(t, 4, new("described"), "88d0cfb4-194c-e040-2fb6-2645907f54cb", "2026-11-03T03:16:08.475Z")
	state := func(v cloudwatchapi.StateValue, reason, at string) alarm.StateRecord {
		return alarm.StateRecord{Value: v, Reason: reason, Timestamp: recTime(t, at)}
	}
	two := state(stateOK, "two", "2026-11-03T03:16:07.941Z")
	two.ReasonData = new(`{"k":"v"}`)
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{
			"create",
			alarm.ConfigurationChangeDetail(alarm.ConfigCreate, first, nil, state(stateInsufficient, "Unchecked: Initial alarm creation", "2026-11-03T03:16:06.316Z")),
			`{"alarmName":"us1103-ev-shape","operation":"create","state":{"value":"INSUFFICIENT_DATA","timestamp":"2026-11-03T03:16:06.316+0000"},"configuration":{"evaluationPeriods":2,"datapointsToAlarm":1,"threshold":3.0,"comparisonOperator":"GreaterThanThreshold","treatMissingData":"ignore","metrics":[{"id":"c2d0cfb4-1516-36f2-5a1f-d2a9f14c031b","metricStat":{"metric":{"namespace":"us1103/cwa","name":"ev-none","dimensions":{}},"period":60,"stat":"Maximum"},"returnData":true}],"alarmName":"us1103-ev-shape","actionsEnabled":true,"timestamp":"2026-11-03T03:16:06.316+0000","okActions":["arn:aws:lambda:us-east-2:123456789012:function:us1103-cwa"],"alarmActions":["arn:aws:lambda:us-east-2:123456789012:function:us1103-cwa"],"insufficientDataActions":[]}}`,
		},
		{
			"update",
			alarm.ConfigurationChangeDetail(alarm.ConfigUpdate, second, &first, two),
			`{"alarmName":"us1103-ev-shape","operation":"update","state":{"value":"OK","timestamp":"2026-11-03T03:16:07.941+0000"},"configuration":{"evaluationPeriods":2,"datapointsToAlarm":1,"threshold":4.0,"comparisonOperator":"GreaterThanThreshold","treatMissingData":"ignore","metrics":[{"id":"88d0cfb4-194c-e040-2fb6-2645907f54cb","metricStat":{"metric":{"namespace":"us1103/cwa","name":"ev-none","dimensions":{}},"period":60,"stat":"Maximum"},"returnData":true}],"alarmName":"us1103-ev-shape","description":"described","actionsEnabled":true,"timestamp":"2026-11-03T03:16:08.475+0000","okActions":["arn:aws:lambda:us-east-2:123456789012:function:us1103-cwa"],"alarmActions":["arn:aws:lambda:us-east-2:123456789012:function:us1103-cwa"],"insufficientDataActions":[]},"previousConfiguration":{"evaluationPeriods":2,"datapointsToAlarm":1,"threshold":3.0,"comparisonOperator":"GreaterThanThreshold","treatMissingData":"ignore","metrics":[{"id":"c2d0cfb4-1516-36f2-5a1f-d2a9f14c031b","metricStat":{"metric":{"namespace":"us1103/cwa","name":"ev-none","dimensions":{}},"period":60,"stat":"Maximum"},"returnData":true}],"alarmName":"us1103-ev-shape","actionsEnabled":true,"timestamp":"2026-11-03T03:16:06.316+0000","okActions":["arn:aws:lambda:us-east-2:123456789012:function:us1103-cwa"],"alarmActions":["arn:aws:lambda:us-east-2:123456789012:function:us1103-cwa"],"insufficientDataActions":[]}}`,
		},
		{
			"delete",
			alarm.ConfigurationChangeDetail(alarm.ConfigDelete, second, nil, state(stateAlarm, "three", "2026-11-03T03:16:09.013Z")),
			`{"alarmName":"us1103-ev-shape","operation":"delete","state":{"value":"ALARM","reason":"three","timestamp":"2026-11-03T03:16:09.013+0000"},"configuration":{"evaluationPeriods":2,"datapointsToAlarm":1,"threshold":4.0,"comparisonOperator":"GreaterThanThreshold","treatMissingData":"ignore","metrics":[{"id":"88d0cfb4-194c-e040-2fb6-2645907f54cb","metricStat":{"metric":{"namespace":"us1103/cwa","name":"ev-none","dimensions":{}},"period":60,"stat":"Maximum"},"returnData":true}],"alarmName":"us1103-ev-shape","description":"described","actionsEnabled":true,"timestamp":"2026-11-03T03:16:08.475+0000","okActions":["arn:aws:lambda:us-east-2:123456789012:function:us1103-cwa"],"alarmActions":["arn:aws:lambda:us-east-2:123456789012:function:us1103-cwa"],"insufficientDataActions":[]}}`,
		},
		{
			"state change",
			alarm.StateChangeDetail(second, alarm.Change{Old: two, New: state(stateAlarm, "three", "2026-11-03T03:16:09.013Z")}),
			`{"alarmName":"us1103-ev-shape","state":{"value":"ALARM","reason":"three","timestamp":"2026-11-03T03:16:09.013+0000"},"previousState":{"value":"OK","reason":"two","reasonData":"{\"k\":\"v\"}","timestamp":"2026-11-03T03:16:07.941+0000"},"configuration":{"metrics":[{"id":"88d0cfb4-194c-e040-2fb6-2645907f54cb","metricStat":{"metric":{"namespace":"us1103/cwa","name":"ev-none","dimensions":{}},"period":60,"stat":"Maximum"},"returnData":true}],"description":"described"}}`,
		},
	} {
		if tc.got != tc.want {
			t.Errorf("%s:\n%s\nwant\n%s", tc.name, tc.got, tc.want)
		}
	}
}
