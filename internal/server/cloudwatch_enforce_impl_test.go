package server_test

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/server"
)

// An alarm notification to a topic whose policy grants only the account
// root, so cloudwatch.amazonaws.com has no SNS:Publish (CWA-23's topic,
// cloudwatch-alarms.md 13.24), is delivered under off; delivered under
// advise with an access-would-deny advisory naming the principal; and
// dropped after its one attempt under enforce
// (cloudwatch.alarm.actions.denied dropped, cloudwatch-alarms.md 12.4 item
// 4).
func TestAlarmNotificationToATopicThatRefusesCloudWatch(t *testing.T) {
	for _, c := range []struct {
		name      string
		mode      server.IAMEnforce
		delivered bool
		advised   bool
	}{
		{"off", server.IAMEnforceOff, true, false},
		{"advise", server.IAMEnforceAdvise, true, true},
		{"enforce", server.IAMEnforceEnforce, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			client, endpoint, _ := enforced(t, c.mode)
			topic := element(t, mustSNS(t, endpoint, url.Values{"Action": {"CreateTopic"}, "Name": {"alerts"}}), "TopicArn")
			mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"inbox"}`)
			setQueuePolicy(t, endpoint, "inbox", accountPolicy("sns.amazonaws.com", "inbox", "000000000000"))
			mustSNS(t, endpoint, url.Values{
				"Action": {"Subscribe"}, "TopicArn": {topic}, "Protocol": {"sqs"}, "Endpoint": {"arn:aws:sqs:us-east-1:000000000000:inbox"},
				"Attributes.entry.1.key": {"RawMessageDelivery"}, "Attributes.entry.1.value": {"true"},
			})
			cloudwatch := func(form url.Values) {
				t.Helper()
				form.Set("Version", "2010-08-01")
				if w := queryCall(t, dummy, endpoint, "monitoring", form); w.status != http.StatusOK {
					t.Fatalf("%s = %d %s", form.Get("Action"), w.status, w.body)
				}
			}
			cloudwatch(url.Values{
				"Action": {"PutMetricAlarm"}, "AlarmName": {"cpu"}, "Namespace": {"App"}, "MetricName": {"Load"},
				"Statistic": {"Sum"}, "Period": {"60"}, "EvaluationPeriods": {"1"}, "Threshold": {"10"},
				"ComparisonOperator": {"GreaterThanThreshold"}, "TreatMissingData": {"ignore"}, "AlarmActions.member.1": {topic},
			})
			cloudwatch(url.Values{"Action": {"SetAlarmState"}, "AlarmName": {"cpu"}, "StateValue": {"ALARM"}, "StateReason": {"test"}})

			bodies := queueBodies(t, client, endpoint, "inbox")
			if got := len(bodies) == 1 && strings.Contains(bodies[0], `"NewStateValue":"ALARM"`); got != c.delivered {
				t.Errorf("queue holds %q; want the notification delivered = %v", bodies, c.delivered)
			}
			if !c.delivered {
				if got := outcomesTo(t, client, topic); len(got) != 1 || got[0] != "dropped" {
					t.Errorf("delivery outcomes to the topic = %q, want one dropped attempt", got)
				}
			}
			advisories := enforcementAdvisories(t, client, "access-would-deny")
			named := slices.ContainsFunc(advisories, func(text string) bool {
				return strings.Contains(text, "cloudwatch.amazonaws.com") && strings.Contains(text, "sns:Publish")
			})
			if named != c.advised {
				t.Errorf("access-would-deny advisories = %q; want one naming cloudwatch.amazonaws.com and sns:Publish = %v", advisories, c.advised)
			}
		})
	}
}
