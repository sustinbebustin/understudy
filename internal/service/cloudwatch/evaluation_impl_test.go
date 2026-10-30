package cloudwatch_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch"
	"github.com/sustinbebustin/understudy/internal/store"
)

// With OnlyStartEvaluatingAfterWarmUpPeriodEnds the alarm "waits the full
// duration": evaluations inside it leave the state alone, and the first
// one at or after its end evaluates as usual (cloudwatch-alarms.md 2.4).
func TestWarmUpWaitsTheFullDuration(t *testing.T) {
	run(t, func(h *harness) {
		t0 := h.now()
		h.put("Load", cloudwatchapi.StandardUnitCount, 20)
		in := evalAlarm("cpu")
		in.WarmUpConfiguration = &cloudwatchapi.WarmUpConfiguration{WarmUpPeriodDurationInMinutes: 3, OnlyStartEvaluatingAfterWarmUpPeriodEnds: new(true)}
		must(h, cloudwatchapi.OpPutMetricAlarm, in)
		h.advance(2 * time.Minute)
		if got := h.one("cpu"); *got.StateValue != cloudwatchapi.StateValueInsufficientData || *got.StateReason != "Unchecked: Initial alarm creation" {
			h.Fatalf("inside warm-up: %s %q, want the initial state", *got.StateValue, *got.StateReason)
		}
		h.advance(time.Minute)
		if got := h.one("cpu"); *got.StateValue != cloudwatchapi.StateValueAlarm || !got.StateTransitionedTimestamp.Equal(t0.Add(3*time.Minute)) {
			h.Errorf("at the end of warm-up: %s at %v, want ALARM at %v", *got.StateValue, *got.StateTransitionedTimestamp, t0.Add(3*time.Minute))
		}
	})
}

// Without the flag, warm-up ends as soon as every period of the window
// holds a datapoint (cloudwatch.alarm.warm-up-end, CWA-22).
func TestWarmUpEndsWhenTheWindowFills(t *testing.T) {
	run(t, func(h *harness) {
		followsSwitch(h, "cloudwatch.alarm.warm-up-end", "every-period-has-data")
		t0 := h.now()
		h.put("Load", cloudwatchapi.StandardUnitCount, 20)
		in := evalAlarm("cpu")
		in.WarmUpConfiguration = &cloudwatchapi.WarmUpConfiguration{WarmUpPeriodDurationInMinutes: 60}
		must(h, cloudwatchapi.OpPutMetricAlarm, in)
		h.advance(time.Minute)
		if got := h.one("cpu"); *got.StateValue != cloudwatchapi.StateValueAlarm || !got.StateTransitionedTimestamp.Equal(t0.Add(time.Minute)) {
			h.Errorf("after the first evaluation with its one period full: %s at %v, want ALARM at %v", *got.StateValue, *got.StateTransitionedTimestamp, t0.Add(time.Minute))
		}
	})
}

// historySections are the exported history sections of the service.
func historySections(h *harness) []store.Section {
	h.Helper()
	sections, err := h.svc.Export()
	if err != nil {
		h.Fatal(err)
	}
	return slices.DeleteFunc(sections, func(s store.Section) bool { return !strings.HasPrefix(s.Path, "history/") })
}

// Items older than 30 days are removed from the store on the next history
// write to the scope, not only hidden from DescribeAlarmHistory (contract
// 3.5; cloudwatch-alarms.md 8.1).
func TestHistoryWritePrunesExpiredItems(t *testing.T) {
	run(t, func(h *harness) {
		must(h, cloudwatchapi.OpPutMetricAlarm, quietAlarm("old"))
		h.setState("old", cloudwatchapi.StateValueAlarm)
		must(h, cloudwatchapi.OpDeleteAlarms, &cloudwatchapi.DeleteAlarmsInput{AlarmNames: []string{"old"}})
		h.advance(31 * day)
		must(h, cloudwatchapi.OpPutMetricAlarm, quietAlarm("new"))
		h.setState("new", cloudwatchapi.StateValueAlarm)

		sections := historySections(h)
		if len(sections) != 1 || sections[0].Path != "history/"+h.Scope.Account.String()+"/"+h.Scope.Region.String()+".json" {
			h.Fatalf("history sections = %v", sections)
		}
		var hist cloudwatch.History
		if err := json.Unmarshal(sections[0].Data, &hist); err != nil {
			h.Fatal(err)
		}
		if _, ok := hist.Items["old"]; ok || len(hist.Items["new"]) != 1 {
			h.Errorf("stored history = %+v, want the new alarm's one item and the expired one gone", hist.Items)
		}
	})
}

// A snapshot without history sections, such as one written before alarm
// history existed, loads its alarms with empty history (contract 3.4).
func TestSnapshotWithoutHistoryLoads(t *testing.T) {
	run(t, func(h *harness) {
		must(h, cloudwatchapi.OpPutMetricAlarm, quietAlarm("cpu"))
		h.setState("cpu", cloudwatchapi.StateValueAlarm)
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		state := slices.DeleteFunc(slices.Clone(sections), func(s store.Section) bool { return strings.HasPrefix(s.Path, "history/") })
		if len(state) == len(sections) {
			h.Fatalf("Export wrote no history section: %v", sections)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(state); err != nil {
			h.Fatal(err)
		}
		if got := h.one("cpu"); *got.StateValue != cloudwatchapi.StateValueAlarm {
			h.Errorf("state after Import = %s, want ALARM", *got.StateValue)
		}
		if items := h.history(&cloudwatchapi.DescribeAlarmHistoryInput{}); len(items) != 0 {
			h.Errorf("history after Import = %v, want none", summaries(items))
		}
	})
}

// A sub-minute alarm is evaluated every ten seconds (cloudwatch.alarm.
// evaluation-cadence, cloudwatch-alarms.md 3.1), so its next evaluation
// after create is ten seconds away.
func TestSubMinuteAlarmEvaluatesEveryTenSeconds(t *testing.T) {
	run(t, func(h *harness) {
		followsSwitch(h, "cloudwatch.alarm.evaluation-cadence", "minute,10s-for-high-resolution,hourly-past-one-day")
		in := evalAlarm("fast")
		in.Period = new(int32(10))
		must(h, cloudwatchapi.OpPutMetricAlarm, in)
		arn := h.alarmARN("fast")
		if due, ok := h.evaluationTimer(arn); !ok || !due.Equal(h.now().Add(10*time.Second)) {
			h.Fatalf("timer = %v %v, want %v", due, ok, h.now().Add(10*time.Second))
		}
		h.advance(10 * time.Second)
		if due, ok := h.evaluationTimer(arn); !ok || !due.Equal(h.now().Add(10*time.Second)) {
			h.Errorf("after one evaluation the timer = %v %v, want %v", due, ok, h.now().Add(10*time.Second))
		}
	})
}

// A Reset drops the alarms; a timer still armed for one finds nothing and
// arms no next evaluation (contract 3.4).
func TestStaleTimerAfterResetDoesNothing(t *testing.T) {
	run(t, func(h *harness) {
		must(h, cloudwatchapi.OpPutMetricAlarm, evalAlarm("cpu"))
		arn := h.alarmARN("cpu")
		h.svc.Reset(kernel.ResetScope{})
		h.advance(time.Minute)
		if due, ok := h.evaluationTimer(arn); ok {
			h.Errorf("a reset alarm's timer re-armed at %v", due)
		}
		if sc := h.stateChanges(arn); len(sc) != 0 {
			h.Errorf("state changes = %v", sc)
		}
	})
}
