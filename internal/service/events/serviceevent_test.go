package events_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

const alarmARN = "arn:aws:cloudwatch:us-east-1:000000000000:alarm:cpu"

// putServiceEvent calls PutServiceEventInternal as CloudWatch does: on its
// own behalf as cloudwatch.amazonaws.com, via the alarm (contract 3.6).
func (h *harness) putServiceEvent(in *eventbridgeapi.PutServiceEventInternalInput) {
	h.Helper()
	parent := h.Call(eventbridgeapi.OpPutEvents.Descriptor())
	_, err := kernel.Invoke(h.Context(), h.Kernel, parent, eventbridgeapi.OpPutServiceEventInternal,
		kernel.Target{Scope: h.Scope, As: kernel.AsPlatform{Name: "cloudwatch.amazonaws.com"}, Via: ref.CloudWatchAlarm}, in)
	if err != nil {
		h.Fatalf("PutServiceEventInternal = %s: %s", err.ID.JSONType, err.Message)
	}
}

// An AWS service event goes to the default bus, "which automatically
// receives events from AWS services" (cloudwatch-alarms.md 12.4, aws-doc),
// in the envelope AWS documents for an alarm state change: version 0, a new
// id, the source, detail-type, resources and detail as sent, the account
// and region of the scope, and the time to the second with Z
// (cloudwatch-alarms.md 6.2). Rules match it as they match PutEvents
// events, and the reserved aws. prefix is not refused (contract 3.6).
func TestPutServiceEventInternalGoesToTheDefaultBus(t *testing.T) {
	run(t, func(h *harness) {
		const pattern = `{"source": ["aws.cloudwatch"], "detail-type": ["CloudWatch Alarm State Change"]}`
		h.patternRule("alarms", pattern)
		h.target("alarms", eventbridgeapi.Target{Id: "q", Arn: queueARN})
		h.createBus("custom")
		h.putRuleOn("custom", "custom-alarms", pattern)
		h.targetOn("custom", "custom-alarms", eventbridgeapi.Target{Id: "q", Arn: "arn:aws:sqs:us-east-1:000000000000:custom"})
		h.advance(1500 * time.Millisecond)
		now := h.Clock.Now().Wire()

		detail := `{"alarmName":"cpu","state":{"value":"ALARM"}}`
		h.putServiceEvent(&eventbridgeapi.PutServiceEventInternalInput{
			Source: "aws.cloudwatch", DetailType: "CloudWatch Alarm State Change", Detail: detail, Resources: []string{alarmARN},
		})
		h.putServiceEvent(&eventbridgeapi.PutServiceEventInternalInput{
			Source: "aws.cloudwatch", DetailType: "CloudWatch Alarm Configuration Change", Detail: `{"alarmName":"cpu","operation":"create"}`, Resources: []string{alarmARN},
		})
		got := h.sink.take()
		if want := []string{queueARN}; !slices.Equal(targetARNs(got), want) {
			t.Fatalf("delivered to %v, want %v: the default bus's matching rule only", targetARNs(got), want)
		}
		e := decode(t, got[0])
		var gotDetail, wantDetail any
		if err := json.Unmarshal(e.Detail, &gotDetail); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(detail), &wantDetail); err != nil {
			t.Fatal(err)
		}
		if e.Version != "0" || e.ID == "" || e.Source != "aws.cloudwatch" || e.DetailType != "CloudWatch Alarm State Change" ||
			e.Account != account || e.Region != "us-east-1" || !slices.Equal(e.Resources, []string{alarmARN}) ||
			e.Time != now.Truncate(time.Second).Format(time.RFC3339) || !jsonEqual(gotDetail, wantDetail) {
			t.Errorf("event = %+v", e)
		}
	})
}

func jsonEqual(a, b any) bool {
	x, errX := json.Marshal(a)
	y, errY := json.Marshal(b)
	return errX == nil && errY == nil && bytes.Equal(x, y)
}

// PutServiceEventInternal is bound Full (contract 3.6).
func TestPutServiceEventInternalCoverage(t *testing.T) {
	run(t, func(h *harness) {
		if got := h.Kernel.Bindings().Status(eventbridgeapi.OpPutServiceEventInternal.Descriptor()); got != (kernel.Bound{Coverage: registry.Full{}}) {
			t.Errorf("PutServiceEventInternal = %#v, want bound Full", got)
		}
	})
}
