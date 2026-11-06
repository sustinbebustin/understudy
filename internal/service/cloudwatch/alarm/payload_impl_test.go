package alarm_test

import (
	"encoding/json"
	"testing"
	"time"
	"unicode/utf8"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch/alarm"
)

// drawSubject is an alarm with arbitrary text wherever a caller sets it.
func drawSubject(t *rapid.T) alarm.Subject {
	text := rapid.String()
	opt := func(label string) *string {
		if rapid.Bool().Draw(t, label+"-set") {
			return new(text.Draw(t, label))
		}
		return nil
	}
	name := rapid.StringN(1, 255, -1).Draw(t, "name")
	cfg := &cloudwatchapi.PutMetricAlarmInput{
		AlarmName:          name,
		AlarmDescription:   opt("description"),
		AlarmActions:       rapid.SliceOfN(text, 0, 5).Draw(t, "actions"),
		MetricName:         opt("metric"),
		Namespace:          opt("namespace"),
		Statistic:          new(cloudwatchapi.StatisticAverage),
		Dimensions:         []cloudwatchapi.Dimension{{Name: text.Draw(t, "dim-name"), Value: text.Draw(t, "dim-value")}},
		Period:             new(int32(60)),
		EvaluationPeriods:  new(int32(3)),
		Threshold:          new(rapid.Float64Range(-1e12, 1e12).Draw(t, "threshold")),
		ComparisonOperator: new(cloudwatchapi.ComparisonOperatorLessThanThreshold),
		TreatMissingData:   opt("treat"),
	}
	return alarm.Subject{
		ARN:      aws.ARN{Partition: "aws", Service: "cloudwatch", Region: "eu-west-1", Account: "123456789012", Resource: "alarm:" + name},
		Config:   cfg,
		MetricID: text.Draw(t, "metric-id"),
	}
}

func drawChange(t *rapid.T) alarm.Change {
	at := time.Date(2026, 2, 26, 10, 0, 0, 0, time.UTC)
	rec := func(label string, v cloudwatchapi.StateValue) alarm.StateRecord {
		r := alarm.StateRecord{Value: v, Reason: rapid.String().Draw(t, label+"-reason"), Timestamp: at}
		if rapid.Bool().Draw(t, label+"-data") {
			r.ReasonData = new(`{"k":` + rapid.SampledFrom([]string{"1", `"v"`, "[]"}).Draw(t, label+"-value") + `}`)
		}
		return r
	}
	return alarm.Change{Old: rec("old", cloudwatchapi.StateValueOk), New: rec("new", cloudwatchapi.StateValueAlarm)}
}

// Whatever text an alarm carries, every payload is one JSON object, and the
// notification Subject is valid UTF-8 of at most 100 bytes
// (cloudwatch-alarms.md 5.1, 5.2, 6.1, 6.2).
func TestPayloadsAreJSONForAnyText(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		s, ch := drawSubject(t), drawChange(t)
		prev := drawSubject(t)
		for what, b := range map[string][]byte{
			"SNSMessage":                []byte(alarm.SNSMessage(s, ch)),
			"LambdaEvent":               alarm.LambdaEvent(s, ch),
			"StateChangeDetail":         []byte(alarm.StateChangeDetail(s, ch)),
			"ConfigurationChangeDetail": []byte(alarm.ConfigurationChangeDetail(alarm.ConfigUpdate, s, &prev, ch.New)),
		} {
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatalf("%s is not a JSON object: %v: %s", what, err, b)
			}
		}
		subject := alarm.SNSSubject(s, ch.New.Value)
		if len(subject) > 100 || !utf8.ValidString(subject) && utf8.ValidString(s.Config.AlarmName) {
			t.Fatalf("SNSSubject = %q (%d bytes), want valid UTF-8 of at most 100", subject, len(subject))
		}
	})
}
