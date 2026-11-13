package cloudwatch_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/outbox"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch"
)

// runActions runs fn against a kernel whose alarm actions go through the
// outbox, bound to report alarm notifications back to CloudWatch, to an
// SNS Publish stub that answers publish and a Lambda Invoke stub that
// accepts every invoke.
func runActions(t *testing.T, publish func(*snsapi.PublishInput) *awserr.Error, fn func(h *harness)) {
	t.Helper()
	box := outbox.New()
	kerneltest.Run(t, func(k *kerneltest.K) {
		k.Stub(snsapi.OpPublish, func(_ context.Context, _ *kernel.Call, in *snsapi.PublishInput) (*snsapi.PublishOutput, *awserr.Error) {
			if err := publish(in); err != nil {
				return nil, err
			}
			return &snsapi.PublishOutput{MessageId: new("m")}, nil
		})
		k.Stub(lambdaapi.OpInvoke, func(context.Context, *kernel.Call, *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
			return &lambdaapi.InvokeOutput{StatusCode: 202}, nil
		})
		svc := cloudwatch.New(k.Deps(cloudwatchapi.Service()), cloudwatch.Options{})
		if err := box.Bind(k.Kernel, outbox.Options{Faults: k.Faults, Alarms: svc}); err != nil {
			k.Fatal(err)
		}
		fn(&harness{K: k, svc: svc})
	}, kerneltest.Services(cloudwatchapi.Service(), snsapi.Service(), lambdaapi.Service()), kerneltest.Deliver(box))
}

// emailDate is the email text's timestamp form (CWA-16).
const emailDate = "Monday 02 January, 2006 15:04:05 UTC"

// Each SNS action an alarm runs on a state change adds an Action item once
// the publish has an outcome, after the change's StateUpdate item, with
// the summary and HistoryData AWS recorded: the published message on
// success (CWA-16 hist-ok), "Topic does not exist" for a missing topic
// (CWA-16 hist-miss) and CloudWatch's own text for a topic whose policy
// refuses it (CWA-23), stateUpdateTimestamp being the state change's
// instant (cloudwatch-alarms.md 5.3, 8.3; recorded 2026-11-03). Account,
// Region and timestamps are normalized.
func TestAnSNSActionListsItsActionItem(t *testing.T) {
	for _, c := range []struct {
		fixture, name, metric, topic, reason string
		answer                               *awserr.Error
		// configured, changed and changedMillis are the recording's
		// configuration and state change instants, as the item writes
		// them.
		configured, changed, changedMillis, changedEmail string
	}{
		{
			fixture: "CWA-16/hist-ok.json", name: "us1103-hist-ok", metric: "hist-none", topic: "us1103-cwa", reason: "delivered",
			configured: "2026-11-03T03:18:40.456+0000", changed: "2026-11-03T03:18:40.998+0000",
			changedMillis: "1793675920998", changedEmail: "Tuesday 03 November, 2026 03:18:40 UTC",
		},
		{
			fixture: "CWA-16/hist-miss.json", name: "us1103-hist-miss", metric: "hist-none", topic: "us1103-missing", reason: "missing topic",
			answer: snsapi.ErrNotFoundException("Topic does not exist"), changedMillis: "1793675919865",
		},
		{
			fixture: "CWA-23/history.json", name: "us1103-deny", metric: "deny-none", topic: "us1103-cwa-deny", reason: "denied",
			answer:        &awserr.Error{ID: snsapi.DenialError(), Message: "denied by the topic policy", Origin: awserr.Denial{}},
			changedMillis: "1793675626701",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			runActions(t, func(*snsapi.PublishInput) *awserr.Error { return c.answer }, func(h *harness) {
				created := h.now()
				must(h, cloudwatchapi.OpPutMetricAlarm, cwalarm(c.name, c.metric, "ignore", h.arnIn("sns", c.topic)))
				h.advance(542 * time.Millisecond)
				changed := h.now()
				must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: c.name, StateValue: cloudwatchapi.StateValueAlarm, StateReason: c.reason})
				synctest.Wait()

				got := h.history(&cloudwatchapi.DescribeAlarmHistoryInput{AlarmName: new(c.name), ScanBy: new(cloudwatchapi.ScanByTimestampAscending)})
				if len(got) != 3 || *got[1].HistoryItemType != cloudwatchapi.HistoryItemTypeStateUpdate || *got[2].HistoryItemType != cloudwatchapi.HistoryItemTypeAction {
					h.Fatalf("history = %v, want the create, the StateUpdate item, then the Action item", summaries(got))
				}
				replace := map[string]string{c.changedMillis: strconv.FormatInt(changed.UnixMilli(), 10)}
				if c.configured != "" {
					replace[c.configured] = stamp(created)
					replace[c.changed] = stamp(changed)
					replace[c.changedEmail] = changed.UTC().Format(emailDate)
				}
				want := recordedItems(h, c.fixture, cloudwatchapi.HistoryItemTypeAction, replace)
				wantConfigItems(h, got[2:], want, []time.Time{changed})

				var data struct {
					StateUpdateTimestamp int64 `json:"stateUpdateTimestamp"`
				}
				if err := json.Unmarshal([]byte(*got[2].HistoryData), &data); err != nil || data.StateUpdateTimestamp != got[1].Timestamp.UnixMilli() {
					h.Errorf("stateUpdateTimestamp = %d (%v), want the StateUpdate item's instant %d", data.StateUpdateTimestamp, err, got[1].Timestamp.UnixMilli())
				}
			})
		})
	}
}

// A Lambda action writes no Action item: every recorded Action item is an
// SNS action's, and no recording shows a Lambda action's form
// (cloudwatch-alarms.md 8.3).
func TestALambdaActionListsNoActionItem(t *testing.T) {
	runActions(t, func(*snsapi.PublishInput) *awserr.Error { return nil }, func(h *harness) {
		must(h, cloudwatchapi.OpPutMetricAlarm, cwalarm("cpu", "Load", "ignore", onAlarmFn))
		must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "cpu", StateValue: cloudwatchapi.StateValueAlarm, StateReason: "set"})
		synctest.Wait()
		got := h.history(&cloudwatchapi.DescribeAlarmHistoryInput{AlarmName: new("cpu"), HistoryItemType: new(cloudwatchapi.HistoryItemTypeAction)})
		if len(got) != 0 {
			h.Errorf("Action items = %v, want none", summaries(got))
		}
	})
}
