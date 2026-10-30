package cloudwatch_test

import (
	"encoding/json"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
)

// SetAlarmState to a different value runs the new state's actions with the
// caller's reason (localstack@8b9a79f
// tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json:L2482-L2519)
// and sends one state change event (cloudwatch-alarms.md 6.2).
func TestSetAlarmStateRunsTheNewStatesActions(t *testing.T) {
	runAlarms(t, func(h *harness, fx *effects) {
		in := evalAlarm("cpu")
		in.TreatMissingData = new("ignore")
		in.AlarmActions = []string{alertsTopic}
		in.OKActions = []string{onAlarmFn}
		must(h, cloudwatchapi.OpPutMetricAlarm, in)
		must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "cpu", StateValue: cloudwatchapi.StateValueAlarm, StateReason: "testing alarm"})
		synctest.Wait()
		d := fx.sink.all()
		if len(d) != 1 {
			h.Fatalf("deliveries = %+v, want the one ALARM action", d)
		}
		topic, ok := d[0].Request.Target.(delivery.SNSTopic)
		if !ok || d[0].Request.Mechanism != delivery.CloudWatchToSNS || !strings.Contains(topic.Message, `"NewStateReason":"testing alarm"`) ||
			!strings.Contains(topic.Message, `"OldStateValue":"INSUFFICIENT_DATA"`) {
			h.Errorf("delivery = %+v, want the notification of the set state", d[0].Request)
		}
		if ev := fx.bus.of("CloudWatch Alarm State Change"); len(ev) != 1 {
			h.Errorf("state change events = %d, want one", len(ev))
		}
	})
}

// Every kind understudy cannot run is journaled as not run, with what it
// would need, and nothing is delivered for it (cloudwatch-alarms.md 7.2,
// 12.7).
func TestNotRunActionsAreJournaled(t *testing.T) {
	runAlarms(t, func(h *harness, fx *effects) {
		scaling := "arn:aws:autoscaling:us-east-1:000000000000:scalingPolicy:1a2b:autoScalingGroupName/web:policyName/up"
		queue := "arn:aws:sqs:us-east-1:000000000000:jobs"
		in := evalAlarm("cpu")
		in.AlarmActions = []string{scaling, queue}
		must(h, cloudwatchapi.OpPutMetricAlarm, in)
		must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "cpu", StateValue: cloudwatchapi.StateValueAlarm, StateReason: "set"})
		synctest.Wait()
		notRun := h.advisories(journal.AdvisoryAlarmActionNotRun)
		if len(notRun) != 2 ||
			notRun[0].Text != "autoscaling action "+scaling+" is not run: understudy does not emulate EC2 Auto Scaling" ||
			notRun[1].Text != "unrecognized action "+queue+" is not run: understudy does not emulate the service the ARN names" {
			h.Errorf("not-run advisories = %+v", notRun)
		}
		if d := fx.sink.all(); len(d) != 0 {
			h.Errorf("deliveries = %+v, want none", d)
		}
	})
}

// Each PutMetricAlarm gives the configuration a new metric id, which the
// configuration change events carry: the update's previousConfiguration
// has the create's id (cloudwatch.alarm.events.metric-id, cloudwatch-alarms.md
// 6.2).
func TestEachConfigurationHasItsOwnMetricID(t *testing.T) {
	runAlarms(t, func(h *harness, fx *effects) {
		followsSwitch(h, "cloudwatch.alarm.events.metric-id", "one-per-configuration")
		must(h, cloudwatchapi.OpPutMetricAlarm, evalAlarm("cpu"))
		must(h, cloudwatchapi.OpPutMetricAlarm, evalAlarm("cpu"))
		synctest.Wait()
		type config struct {
			Metrics []struct {
				ID string `json:"id"`
			} `json:"metrics"`
		}
		type detail struct {
			Configuration         config  `json:"configuration"`
			PreviousConfiguration *config `json:"previousConfiguration"`
		}
		events := fx.bus.of("CloudWatch Alarm Configuration Change")
		details := make([]detail, 0, len(events))
		for _, e := range events {
			var d detail
			if err := json.Unmarshal([]byte(e.in.Detail), &d); err != nil {
				h.Fatalf("detail is not JSON: %v", err)
			}
			details = append(details, d)
		}
		if len(details) != 2 || len(details[0].Configuration.Metrics) != 1 || len(details[1].Configuration.Metrics) != 1 ||
			details[1].PreviousConfiguration == nil || len(details[1].PreviousConfiguration.Metrics) != 1 {
			h.Fatalf("configuration change details = %+v, want a create and an update with one metric each", details)
		}
		created, updated := details[0].Configuration.Metrics[0].ID, details[1].Configuration.Metrics[0].ID
		if created == "" || created == updated || details[1].PreviousConfiguration.Metrics[0].ID != created {
			h.Errorf("metric ids: create %q, update %q, update's previous %q", created, updated, details[1].PreviousConfiguration.Metrics[0].ID)
		}
	})
}
