package scheduler_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// A rate schedule without StartDate first fires at the write instant, then
// every interval, each invocation carrying attempt 1. UNVERIFIED (SCH-09),
// scheduler.md 5.2, 14.3.4: the write-instant fire is understudy's choice.
func TestRateFirstFiresAtTheWriteInstant(t *testing.T) {
	run(t, func(h *harness) {
		t0 := h.now()
		h.create(schedule("tracer", "rate(1 hour)", h.queueTarget("tracer", new("hello <aws.scheduler.attempt-number>"))))
		h.settled()
		got := h.sentTo("tracer")
		if len(got) != 1 || !got[0].at.Wire().Equal(t0) || got[0].sqs.MessageBody != "hello 1" {
			h.Fatalf("after create sent %+v, want hello 1 at the write instant %v", got, t0)
		}
		h.advance(time.Hour)
		got = h.sentTo("tracer")
		if len(got) != 2 || !got[1].at.Wire().Equal(t0.Add(time.Hour)) || got[1].sqs.MessageBody != "hello 1" {
			h.Fatalf("after an hour sent %+v, want a second hello 1 at %v", got, t0.Add(time.Hour))
		}
		if got[0].principal == got[1].principal {
			h.Errorf("both invocations ran as %v, want a fresh role session each (scheduler.md 9)", got[0].principal)
		}
	})
}

// A deleted schedule's armed fire delivers nothing (scheduler.md 4.4).
func TestDeleteScheduleDisarmsItsFire(t *testing.T) {
	run(t, func(h *harness) {
		h.create(schedule("s", "rate(1 hour)", h.queueTarget("s", nil)))
		h.settled()
		must(h, schedulerapi.OpDeleteSchedule, &schedulerapi.DeleteScheduleInput{Name: "s"})
		h.advance(3 * time.Hour)
		if got := h.sentTo("s"); len(got) != 1 {
			h.Errorf("sent %d, want only the write-instant fire before the delete", len(got))
		}
	})
}

// A second CreateSchedule of a name the group holds is ConflictException
// 409 (model; scheduler.md 2.4); the text is understudy's, UNVERIFIED
// (SCH-02).
func TestCreateScheduleConflict(t *testing.T) {
	run(t, func(h *harness) {
		h.create(schedule("s", "rate(1 hour)", h.queueTarget("q", nil)))
		err := errOf(call(h, schedulerapi.OpCreateSchedule, schedule("s", "rate(2 hours)", h.queueTarget("q", nil))))
		wantError(h, err, "ConflictException", 409, "Schedule default/s already exists.")
		if got := val(h.get("", "s").ScheduleExpression); got != "rate(1 hour)" {
			h.Errorf("expression after the refused create = %s, want rate(1 hour)", got)
		}
	})
}

// What tickets 2 to 4 do not build answers 501 and stores nothing
// (CLAUDE.md invariant 3; spec "Tracer bullet").
func TestUnbuiltMembersAnswerNotImplemented(t *testing.T) {
	cases := map[string]func(h *harness, in *schedulerapi.CreateScheduleInput){
		"group": func(_ *harness, in *schedulerapi.CreateScheduleInput) { in.GroupName = new("g") },
		"delete after completion": func(_ *harness, in *schedulerapi.CreateScheduleInput) {
			in.ActionAfterCompletion = new(schedulerapi.ActionAfterCompletionDelete)
		},
		"lambda": func(h *harness, in *schedulerapi.CreateScheduleInput) { in.Target.Arn = h.functionArn("f") },
		"other region": func(_ *harness, in *schedulerapi.CreateScheduleInput) {
			in.Target.Arn = "arn:aws:sqs:eu-west-1:000000000000:q"
		},
		"flexible": func(_ *harness, in *schedulerapi.CreateScheduleInput) {
			in.FlexibleTimeWindow = schedulerapi.FlexibleTimeWindow{Mode: schedulerapi.FlexibleTimeWindowModeFlexible, MaximumWindowInMinutes: new(int32(5))}
		},
		"dead-letter queue": func(h *harness, in *schedulerapi.CreateScheduleInput) {
			in.Target.DeadLetterConfig = &schedulerapi.DeadLetterConfig{Arn: new(h.queueArn("dlq"))}
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			run(t, func(h *harness) {
				in := schedule("s", "rate(1 hour)", h.queueTarget("q", nil))
				edit(h, in)
				wantError(h, errOf(call(h, schedulerapi.OpCreateSchedule, in)), "NotImplemented", 501, "")
				wantError(h, errOf(call(h, schedulerapi.OpGetSchedule, &schedulerapi.GetScheduleInput{Name: "s", GroupName: in.GroupName})), "ResourceNotFoundException", 404, "")
			})
		})
	}
}

// A schedule survives a snapshot: Export, then Import into an emptied
// service, reads back the same, and Rearm skips the invocations that came
// due meanwhile with one schedule-fires-skipped Advisory, resuming in the
// rate's phase (scheduler.md 14.3.8; ADR 0009).
func TestSnapshotKeepsTheRateAndSkipsMissedFires(t *testing.T) {
	run(t, func(h *harness) {
		t0 := h.now()
		arn := h.create(schedule("s", "rate(1 hour)", h.queueTarget("s", new("x"))))
		h.settled()
		before := fmt.Sprintf("%+v", deref(h.get("", "s")))
		sections, err := h.svc.Export()
		if err != nil || len(sections) == 0 {
			h.Fatalf("Export = %d sections, %v", len(sections), err)
		}
		h.svc.Reset(kernel.ResetScope{})
		h.Kernel.Timers().Scheduler().Clear()
		h.advance(150 * time.Minute)
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		h.svc.Rearm(h.Clock.Now())
		h.settled()
		if after := fmt.Sprintf("%+v", deref(h.get("", "s"))); after != before {
			h.Errorf("after import:\n%s\nbefore:\n%s", after, before)
		}
		if got := h.sentTo("s"); len(got) != 1 {
			h.Fatalf("sent %d after Rearm, want only the write-instant fire: missed fires were delivered", len(got))
		}
		h.advance(30 * time.Minute)
		got := h.sentTo("s")
		if len(got) != 2 || !got[1].at.Wire().Equal(t0.Add(3*time.Hour)) {
			h.Errorf("sent %+v, want the next fire in phase at %v", got, t0.Add(3*time.Hour))
		}
		skipped := 0
		for _, ev := range h.Journal.Query(journal.Filter{Kind: journal.KindAdvisory}).Events {
			if a, ok := ev.Detail.(journal.Advisory); ok && a.Kind == journal.AdvisoryScheduleFiresSkipped && a.Resource.String() == arn {
				skipped++
			}
		}
		if skipped != 1 {
			h.Errorf("schedule-fires-skipped advisories for %s = %d, want 1", arn, skipped)
		}
	})
}
