package scheduler_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/schedulerapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/scheduler"
)

// flexible is a FLEXIBLE window of minutes.
func flexible(minutes int32) schedulerapi.FlexibleTimeWindow {
	return schedulerapi.FlexibleTimeWindow{Mode: schedulerapi.FlexibleTimeWindowModeFlexible, MaximumWindowInMinutes: new(minutes)}
}

// DELETE removes the schedule in the write that hands its last invocation
// over, journaled as a state change whose parent is the fire's timer event
// (scheduler.md 8, 14.6). UNVERIFIED (SCH-07): the timing is understudy's.
func TestDeleteIsJournaledAsAChildOfTheFire(t *testing.T) {
	run(t, func(h *harness) {
		in := schedule("once", atExpr(h.now().Add(10*time.Minute)), h.queueTarget("once", new("x")))
		in.ActionAfterCompletion = new(schedulerapi.ActionAfterCompletionDelete)
		arn := h.create(in)
		h.advance(15 * time.Minute)
		var changes []journal.Event
		var c journal.StateChange
		for _, ev := range h.Journal.Query(journal.Filter{Kind: journal.KindStateChange}).Events {
			if sc, ok := ev.Detail.(journal.StateChange); ok && sc.Resource.String() == arn {
				changes, c = append(changes, ev), sc
			}
		}
		if len(changes) != 1 {
			h.Fatalf("state changes of %s = %+v, want one", arn, changes)
		}
		if c.From != "ENABLED" || c.To != "DELETED" {
			h.Errorf("state change = %s -> %s, want ENABLED -> DELETED", c.From, c.To)
		}
		parent := changes[0].Cause.Parent
		fired := false
		for _, ev := range h.Journal.Query(journal.Filter{Kind: journal.KindTimerFired}).Events {
			if tf, ok := ev.Detail.(journal.TimerFired); ok && tf.Key.ID == arn && (ev.ID == parent || ev.ID == changes[0].Cause.Trace) {
				fired = true
			}
		}
		if parent == ref.EventID(0) || !fired {
			h.Errorf("deletion cause = %+v, want it under the schedule's timer-fired event", changes[0].Cause)
		}
	})
}

// The schedule is gone before the invocation's retries run: they still
// deliver, as the outbox holds them (scheduler.md 14.6). UNVERIFIED
// (SCH-07).
func TestDeleteComesBeforeTheRetries(t *testing.T) {
	run(t, func(h *harness) {
		h.answerQueues(func(queue string, n int) *awserr.Error {
			if queue == "busy" && n == 0 {
				return sqsapi.ErrRequestThrottled("Request is throttled.")
			}
			return nil
		})
		in := schedule("once", atExpr(h.now().Add(10*time.Minute)), h.queueTarget("busy", new("x")))
		in.ActionAfterCompletion = new(schedulerapi.ActionAfterCompletionDelete)
		h.create(in)
		h.advance(10 * time.Minute)
		if n := len(h.sentTo("busy")); n != 1 {
			h.Fatalf("attempts at the fire = %d, want 1", n)
		}
		wantError(h, errOf(call(h, schedulerapi.OpGetSchedule, &schedulerapi.GetScheduleInput{Name: "once"})), "ResourceNotFoundException", 404, "")
		h.advanceBy(2*time.Hour, time.Minute)
		if n := len(h.sentTo("busy")); n != 2 {
			h.Errorf("attempts after the retry = %d, want 2", n)
		}
	})
}

// A fired at() with NONE stays, State unchanged; DELETE on a rate without
// EndDate never deletes (scheduler.md 8, 14.6). UNVERIFIED (SCH-07): the
// completed at()'s State is understudy's.
func TestNoneKeepsACompletedSchedule(t *testing.T) {
	run(t, func(h *harness) {
		h.create(schedule("once", atExpr(h.now().Add(10*time.Minute)), h.queueTarget("once", new("x"))))
		h.advance(2 * time.Hour)
		got := h.get("", "once")
		if len(h.sentTo("once")) != 1 || got.State == nil || *got.State != schedulerapi.ScheduleStateEnabled {
			h.Errorf("after its fire: %d sends, State %v; want 1 and ENABLED", len(h.sentTo("once")), got.State)
		}
	})
}

// DELETE without EndDate never deletes, even a cron bounded to one year
// that stops matching after its last fire: "If you configure a schedule
// with automatic deletion but do not specify a value for EndDate,
// EventBridge Scheduler does not automatically delete the schedule"
// (scheduler.md 8, 14.6).
func TestDeleteKeepsAYearBoundedCronWithoutEndDate(t *testing.T) {
	run(t, func(h *harness) {
		at := h.now().Add(10 * time.Minute).UTC()
		expr := fmt.Sprintf("cron(%d %d %d %d ? %d)", at.Minute(), at.Hour(), at.Day(), int(at.Month()), at.Year())
		in := schedule("yearly", expr, h.queueTarget("yearly", new("x")))
		in.ActionAfterCompletion = new(schedulerapi.ActionAfterCompletionDelete)
		h.create(in)
		h.advance(2 * time.Hour)
		if n := len(h.sentTo("yearly")); n != 1 {
			h.Fatalf("sends = %d, want the cron's one match", n)
		}
		h.get("", "yearly")
	})
}

// A past-due at() with DELETE that a snapshot load skips is deleted, as its
// fire would have deleted it, and journaled DELETED, rather than kept
// completed forever (scheduler.md 8, 14.3.8, 14.6). UNVERIFIED (SCH-07):
// AWS has no skipped fires.
func TestRearmDeletesASkippedDeleteAt(t *testing.T) {
	run(t, func(h *harness) {
		in := schedule("once", atExpr(h.now().Add(10*time.Minute)), h.queueTarget("once", new("x")))
		in.ActionAfterCompletion = new(schedulerapi.ActionAfterCompletionDelete)
		arn := h.create(in)
		sections, err := h.svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		h.Kernel.Timers().Scheduler().Clear()
		h.advance(time.Hour)
		if err := h.svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		h.svc.Rearm(h.Clock.Now())
		h.advance(time.Hour)
		if n := len(h.sentTo("once")); n != 0 {
			h.Errorf("the skipped at() sent %d, want 0", n)
		}
		wantError(h, errOf(call(h, schedulerapi.OpGetSchedule, &schedulerapi.GetScheduleInput{Name: "once"})), "ResourceNotFoundException", 404, "")
		deleted := 0
		for _, ev := range h.Journal.Query(journal.Filter{Kind: journal.KindStateChange}).Events {
			if sc, ok := ev.Detail.(journal.StateChange); ok && sc.Resource.String() == arn && sc.To == "DELETED" {
				deleted++
			}
		}
		if deleted != 1 {
			h.Errorf("DELETED state changes of %s = %d, want 1", arn, deleted)
		}
	})
}

// MaximumWindowInMinutes reads back under FLEXIBLE and is dropped under
// OFF (scheduler.md 4.2). UNVERIFIED (SCH-04): dropping it under OFF.
func TestFlexibleWindowEcho(t *testing.T) {
	run(t, func(h *harness) {
		in := schedule("flex", "rate(1 hour)", h.queueTarget("q", nil))
		in.FlexibleTimeWindow = flexible(15)
		h.create(in)
		if w := h.get("", "flex").FlexibleTimeWindow; w == nil || w.Mode != schedulerapi.FlexibleTimeWindowModeFlexible || val32(w.MaximumWindowInMinutes) != 15 {
			h.Errorf("FLEXIBLE window read back as %+v, want FLEXIBLE 15", w)
		}
		off := schedule("off", "rate(1 hour)", h.queueTarget("q", nil))
		off.FlexibleTimeWindow = schedulerapi.FlexibleTimeWindow{Mode: schedulerapi.FlexibleTimeWindowModeOff, MaximumWindowInMinutes: new(int32(15))}
		h.create(off)
		if w := h.get("", "off").FlexibleTimeWindow; w == nil || w.MaximumWindowInMinutes != nil {
			h.Errorf("OFF window read back as %+v, want no MaximumWindowInMinutes", w)
		}
	})
}

// firstFlexDelivery creates one FLEXIBLE 60 at() an hour out and returns
// how far after its scheduled instant it was delivered, optionally through
// a snapshot round trip before the fire.
func firstFlexDelivery(t *testing.T, roundTrip bool) time.Duration {
	t.Helper()
	var got time.Duration
	run(t, func(h *harness) {
		at := h.now().Add(time.Hour)
		in := schedule("flex", atExpr(at), h.queueTarget("flex", new("x")))
		in.FlexibleTimeWindow = flexible(60)
		h.create(in)
		if roundTrip {
			sections, err := h.svc.Export()
			if err != nil {
				h.Fatal(err)
			}
			h.svc.Reset(kernel.ResetScope{})
			h.Kernel.Timers().Scheduler().Clear()
			if err := h.svc.Import(sections); err != nil {
				h.Fatal(err)
			}
			h.svc.Rearm(h.Clock.Now())
		}
		h.advanceBy(2*time.Hour+time.Minute, time.Second*30)
		sent := h.sentTo("flex")
		if len(sent) != 1 {
			h.Fatalf("sends = %d, want 1", len(sent))
		}
		got = sent[0].at.Wire().Sub(at)
	})
	return got
}

// The offset is stored as the plan's due instant, so a snapshot round
// trip and Rearm re-arm the same instant (contract section 1; ADR 0009).
func TestFlexibleDueSurvivesASnapshot(t *testing.T) {
	plain, loaded := firstFlexDelivery(t, false), firstFlexDelivery(t, true)
	if plain != loaded || plain < 0 || plain >= time.Hour {
		t.Errorf("offset %v without a snapshot, %v through one; want the same instant inside the window", plain, loaded)
	}
}

// flexDeliveryAfter creates one FLEXIBLE 60 at() an hour out, applies
// then to it, and returns how far after its scheduled instant it was
// delivered.
func flexDeliveryAfter(t *testing.T, o scheduler.Options, then func(h *harness, in *schedulerapi.CreateScheduleInput)) time.Duration {
	t.Helper()
	var got time.Duration
	runWith(t, harnessOptions{scheduler: o, seed: 3}, func(h *harness) {
		at := h.now().Add(time.Hour)
		in := schedule("flex", atExpr(at), h.queueTarget("flex", new("x")))
		in.FlexibleTimeWindow = flexible(60)
		h.create(in)
		then(h, in)
		h.advanceBy(2*time.Hour+time.Minute, 30*time.Second)
		sent := h.sentTo("flex")
		if len(sent) != 1 {
			h.Fatalf("sends = %d, want 1", len(sent))
		}
		got = sent[0].at.Wire().Sub(at)
	})
	return got
}

// An update that keeps the timing keeps the drawn due instant
// (scheduler.md 14.3.10, 14.3.7). UNVERIFIED (SCH-09).
func TestUpdateKeepsTheDrawnOffset(t *testing.T) {
	none := flexDeliveryAfter(t, scheduler.Options{}, func(*harness, *schedulerapi.CreateScheduleInput) {})
	described := flexDeliveryAfter(t, scheduler.Options{}, func(h *harness, in *schedulerapi.CreateScheduleInput) {
		up := update(in)
		up.Description = new("changed")
		must(h, schedulerapi.OpUpdateSchedule, up)
	})
	if none != described {
		t.Errorf("offset %v without an update, %v after a description-only update; want the same", none, described)
	}
}

// Under window-start a FLEXIBLE invocation is made at its scheduled
// instant (spec switch table, scheduler.flexible-window.offset).
func TestWindowStartInvokesAtTheScheduledInstant(t *testing.T) {
	if got := flexDeliveryAfter(t, scheduler.Options{FlexibleOffset: scheduler.OffsetWindowStart}, func(*harness, *schedulerapi.CreateScheduleInput) {}); got >= 30*time.Second {
		t.Errorf("offset %v under window-start, want the scheduled instant", got)
	}
}
