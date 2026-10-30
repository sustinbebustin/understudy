package cloudwatch_test

import (
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
)

// SetAlarmState leaves the evaluation timer where PutMetricAlarm armed it,
// so the next evaluation re-derives the state (cloudwatch-alarms.md 2.3).
// To the same value it journals no StateChange, and the sent values replace
// the reason and data whole: a set without StateReasonData drops the data
// a previous set left (cloudwatch.alarm.set-same-state, CWA-17).
func TestSetAlarmStateKeepsTheTimerAndJournalsTransitionsOnly(t *testing.T) {
	runAlarms(t, func(h *harness, _ *effects) {
		followsSwitch(h, "cloudwatch.alarm.set-same-state", "reason-and-data-only")
		in := evalAlarm("cpu")
		in.TreatMissingData = new("ignore")
		must(h, cloudwatchapi.OpPutMetricAlarm, in)
		arn := h.alarmARN("cpu")
		armed, ok := h.evaluationTimer(arn)
		if !ok {
			h.Fatal("PutMetricAlarm armed no evaluation timer")
		}

		h.advance(10 * time.Second)
		set := func(v cloudwatchapi.StateValue, reason string, data *string) {
			h.Helper()
			must(h, cloudwatchapi.OpSetAlarmState, &cloudwatchapi.SetAlarmStateInput{AlarmName: "cpu", StateValue: v, StateReason: reason, StateReasonData: data})
			synctest.Wait()
			if due, ok := h.evaluationTimer(arn); !ok || !due.Equal(armed) {
				h.Errorf("after SetAlarmState %s the timer = %v %v, want %v untouched", v, due, ok, armed)
			}
		}
		set(cloudwatchapi.StateValueAlarm, "first", new(`{"k":1}`))
		set(cloudwatchapi.StateValueAlarm, "second", nil)
		if got := h.one("cpu"); *got.StateReason != "second" || got.StateReasonData != nil {
			h.Errorf("after a same-value set without data: %q %v, want the data dropped", *got.StateReason, got.StateReasonData)
		}
		set(cloudwatchapi.StateValueOk, "third", nil)
		if sc := h.stateChanges(arn); !slices.Equal(sc, []string{"INSUFFICIENT_DATA->ALARM", "ALARM->OK"}) {
			h.Errorf("state changes = %v, want one per change of value", sc)
		}
	})
}
