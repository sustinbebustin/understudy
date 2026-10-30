package gosdk_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	sqsapi "github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// The alarm notification end to end: an admin clock advance evaluates the
// alarm, which publishes through SNS to a raw SQS subscription exactly one
// notification in the cwa 5.1 schema; the later transition to
// INSUFFICIENT_DATA sends nothing, since the alarm has no
// InsufficientDataActions (cwa 7.1). The topic and queue policies are the
// documented grants (cwa 5.3, Notify_Users_Alarm_Changes; sns.md). Not
// parallel: it moves the clock every test shares.
func TestCloudWatchAlarmNotifiesThroughSNSOnClockAdvance(t *testing.T) {
	frozenClock(t)
	acct, rgn := account(t), region()
	alarmName := name(t, "notify")
	alarmARN := "arn:aws:cloudwatch:" + rgn + ":" + acct + ":alarm:" + alarmName
	topicARN := createTopic(t, name(t, "alarm-topic"), nil)
	_, err := sns.NewFromConfig(awsCfg).SetTopicAttributes(t.Context(), &sns.SetTopicAttributesInput{
		TopicArn: aws.String(topicARN), AttributeName: aws.String("Policy"),
		AttributeValue: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"cloudwatch.amazonaws.com"},` +
			`"Action":"SNS:Publish","Resource":"` + topicARN + `","Condition":{"ArnLike":{"aws:SourceArn":"` + alarmARN + `"}}}]}`),
	})
	ok(t, err, "SetTopicAttributes Policy")
	queueURL, _ := subscribeQueue(t, topicARN, name(t, "alarm-inbox"), nil, map[string]string{"RawMessageDelivery": "true"})
	_, err = sqsapi.NewFromConfig(awsCfg).SetQueueAttributes(t.Context(), &sqsapi.SetQueueAttributesInput{
		QueueUrl: aws.String(queueURL),
		Attributes: map[string]string{string(sqstypes.QueueAttributeNamePolicy): `{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
			`"Principal":{"Service":"sns.amazonaws.com"},"Action":"sqs:SendMessage","Resource":"` + queueArn(t, queueURL) + `",` +
			`"Condition":{"ArnEquals":{"aws:SourceArn":"` + topicARN + `"}}}]}`},
	})
	ok(t, err, "SetQueueAttributes Policy")

	client := cloudwatch.NewFromConfig(awsCfg)
	metric := name(t, "load")
	// Data put without a Timestamp lines up with alarm windows (cwa 12.3).
	_, err = client.PutMetricData(t.Context(), &cloudwatch.PutMetricDataInput{
		Namespace: aws.String("Conformance"), MetricData: []types.MetricDatum{{MetricName: aws.String(metric), Value: aws.Float64(20)}},
	})
	ok(t, err, "PutMetricData")
	in := alarmInput(alarmName)
	in.MetricName, in.Threshold, in.AlarmActions = aws.String(metric), aws.Float64(10), []string{topicARN}
	putAlarm(t, in)

	advanceClock(t, time.Minute)
	msgs := drain(t, queueURL, 3*time.Second)
	if len(msgs) != 1 {
		t.Fatalf("notifications after the ALARM transition = %q, want one", bodies(msgs))
	}
	var n struct {
		AlarmName, AlarmArn, NewStateValue, OldStateValue, AWSAccountId string
		Trigger                                                         struct{ MetricName, Namespace string }
	}
	if err := json.Unmarshal([]byte(aws.ToString(msgs[0].Body)), &n); err != nil {
		t.Fatalf("raw notification is not the cwa 5.1 JSON: %v: %s", err, aws.ToString(msgs[0].Body))
	}
	if n.AlarmName != alarmName || n.AlarmArn != alarmARN || n.NewStateValue != "ALARM" || n.OldStateValue != "INSUFFICIENT_DATA" ||
		n.AWSAccountId != acct || n.Trigger.MetricName != metric || n.Trigger.Namespace != "Conformance" {
		t.Errorf("notification = %+v (cwa 5.1)", n)
	}

	// cwa 3.4, 3.5: once the point has left the range the default missing
	// treatment goes to INSUFFICIENT_DATA, which runs no action here.
	advanceClock(t, 10*time.Minute)
	if a := describeAlarm(t, client, alarmName); a.StateValue != types.StateValueInsufficientData {
		t.Fatalf("after the point left the range: %s", a.StateValue)
	}
	if more := drain(t, queueURL, 3*time.Second); len(more) != 0 {
		t.Errorf("notifications after the INSUFFICIENT_DATA transition = %q, want none", bodies(more))
	}
}
