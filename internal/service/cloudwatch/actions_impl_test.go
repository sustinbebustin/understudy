package cloudwatch_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch"
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
			notRun[1].Text != "unrecognized action "+queue+" is not run: it is not an SNS topic or Lambda function in a valid account and Region, nor another documented action form (cloudwatch-alarms.md 7.2)" {
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

// DisableAlarmActions is a configuration update: AWS moved
// AlarmConfigurationUpdatedTimestamp and sent an "update" configuration
// change event whose configuration has actionsEnabled false, the previous
// one true, and both the same metric id (cloudwatch-alarms.md 6.2; CWA-14,
// recorded 2026-11-03: testdata/parity/sqs/CWA-14/retry-events-*.json).
func TestDisableAlarmActionsIsAConfigurationUpdate(t *testing.T) {
	runAlarms(t, func(h *harness, fx *effects) {
		followsSwitch(h, "cloudwatch.alarm.events.metric-id", "one-per-configuration")
		must(h, cloudwatchapi.OpPutMetricAlarm, evalAlarm("cpu"))
		created := h.now()
		h.advance(time.Second)
		must(h, cloudwatchapi.OpDisableAlarmActions, &cloudwatchapi.DisableAlarmActionsInput{AlarmNames: []string{"cpu"}})
		synctest.Wait()
		if got := h.one("cpu"); !got.AlarmConfigurationUpdatedTimestamp.Equal(created.Add(time.Second)) || *got.ActionsEnabled {
			h.Errorf("after DisableAlarmActions: AlarmConfigurationUpdatedTimestamp %v, ActionsEnabled %v; want %v, false",
				*got.AlarmConfigurationUpdatedTimestamp, *got.ActionsEnabled, created.Add(time.Second))
		}
		type config struct {
			ActionsEnabled bool   `json:"actionsEnabled"`
			Timestamp      string `json:"timestamp"`
			Metrics        []struct {
				ID string `json:"id"`
			} `json:"metrics"`
		}
		var d struct {
			Operation             string  `json:"operation"`
			Configuration         config  `json:"configuration"`
			PreviousConfiguration *config `json:"previousConfiguration"`
		}
		events := fx.bus.of("CloudWatch Alarm Configuration Change")
		if len(events) != 2 {
			h.Fatalf("configuration change events = %d, want the create and the update", len(events))
		}
		if err := json.Unmarshal([]byte(events[1].in.Detail), &d); err != nil {
			h.Fatalf("detail is not JSON: %v", err)
		}
		p := d.PreviousConfiguration
		if d.Operation != "update" || d.Configuration.ActionsEnabled || p == nil || !p.ActionsEnabled ||
			d.Configuration.Timestamp != stamp(created.Add(time.Second)) || p.Timestamp != stamp(created) ||
			len(d.Configuration.Metrics) != 1 || len(p.Metrics) != 1 || d.Configuration.Metrics[0].ID != p.Metrics[0].ID {
			h.Errorf("DisableAlarmActions event = %s", events[1].in.Detail)
		}
	})
}

// A topic ARN in no account understudy serves cannot be delivered to: it is
// journaled as not run, and the transition still answers, writes its
// history item, runs the actions after it and sends its event
// (cloudwatch-alarms.md 5.3: CloudWatch "doesn't test or validate the
// actions").
func TestUnservableActionIsNotRunAndTheRestRun(t *testing.T) {
	runAlarms(t, func(h *harness, fx *effects) {
		bad := "arn:aws:sns:us-east-1:111:t"
		in := evalAlarm("cpu")
		in.TreatMissingData = new("ignore")
		in.AlarmActions = []string{bad, onAlarmFn}
		must(h, cloudwatchapi.OpPutMetricAlarm, in)
		must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "cpu", StateValue: cloudwatchapi.StateValueAlarm, StateReason: "set"})
		synctest.Wait()
		if d := fx.sink.all(); len(d) != 1 || d[0].Request.Mechanism != delivery.CloudWatchToLambda {
			h.Errorf("deliveries = %+v, want the Lambda action alone", d)
		}
		if notRun := h.advisories(journal.AdvisoryAlarmActionNotRun); len(notRun) != 1 || !strings.Contains(notRun[0].Text, bad) {
			h.Errorf("not-run advisories = %+v, want the unservable topic", notRun)
		}
		if s := summaries(h.history(&cloudwatchapi.DescribeAlarmHistoryInput{AlarmName: new("cpu")})); len(s) != 1 {
			h.Errorf("history = %v, want the transition", s)
		}
		if ev := fx.bus.of("CloudWatch Alarm State Change"); len(ev) != 1 {
			h.Errorf("state change events = %d, want one", len(ev))
		}
	})
}

// Two transitions in one clock advance reach the default bus in the order
// the clock ran them, whatever the goroutine schedule (ADR 0018, hardening
// round 3).
func TestTransitionEventsReachTheBusInClockOrder(t *testing.T) {
	runAlarms(t, func(h *harness, fx *effects) {
		h.put("Load", cloudwatchapi.StandardUnitCount, 20)
		must(h, cloudwatchapi.OpPutMetricAlarm, evalAlarm("cpu"))
		h.settle(10 * time.Minute)
		events := fx.bus.of("CloudWatch Alarm State Change")
		got := make([]string, 0, len(events))
		for _, e := range events {
			var d struct {
				State struct {
					Value string `json:"value"`
				} `json:"state"`
			}
			if err := json.Unmarshal([]byte(e.in.Detail), &d); err != nil {
				h.Fatalf("detail is not JSON: %v", err)
			}
			got = append(got, d.State.Value)
		}
		if !slices.Equal(got, []string{"ALARM", "INSUFFICIENT_DATA"}) {
			h.Errorf("state change events = %v, want ALARM then INSUFFICIENT_DATA", got)
		}
	})
}

// historySink is a DeliverSink that reads the alarm's history as each
// delivery arrives.
type historySink struct {
	h     *harness
	items []int
}

func (s *historySink) Deliver(kernel.Delivery) error {
	s.items = append(s.items, len(s.h.history(&cloudwatchapi.DescribeAlarmHistoryInput{AlarmName: new("cpu")})))
	return nil
}

// A transition's actions are delivered once its history item exists, so a
// subscriber that reads the history on notification finds it
// (cloudwatch-alarms.md 12.4 item 5).
func TestActionsAreDeliveredAfterTheHistoryItem(t *testing.T) {
	hs := &historySink{}
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := &harness{K: k, svc: cloudwatch.New(k.Deps(cloudwatchapi.Service()), cloudwatch.Options{})}
		hs.h = h
		in := evalAlarm("cpu")
		in.TreatMissingData = new("ignore")
		in.AlarmActions = []string{alertsTopic}
		must(h, cloudwatchapi.OpPutMetricAlarm, in)
		must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "cpu", StateValue: cloudwatchapi.StateValueAlarm, StateReason: "set"})
		synctest.Wait()
		if !slices.Equal(hs.items, []int{1}) {
			h.Errorf("history items seen at each delivery = %v, want [1]", hs.items)
		}
	}, kerneltest.Services(cloudwatchapi.Service()), kerneltest.Deliver(hs))
}
