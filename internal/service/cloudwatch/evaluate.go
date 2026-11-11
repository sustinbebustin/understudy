package cloudwatch

import (
	"context"
	"log/slog"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch/alarm"
	"github.com/sustinbebustin/understudy/internal/serviceevent"
)

// onEvaluation evaluates the alarm a TimerAlarmEvaluation names at its due
// instant and arms the next evaluation (cloudwatch-alarms.md 12.3). An
// alarm that is gone or no longer evaluable is left alone and its timer
// dies. Every evaluation is the kernel's TimerFired journal event and one
// seeded request id; one that keeps the state writes nothing and journals
// nothing more (cloudwatch.alarm.unchanged-state), and a transition writes
// the state and journals a StateChange under it, then finishes as
// finishTransition says.
func (s *Service) onEvaluation(c *kernel.Call, key clock.TimerKey, due clock.Instant) {
	ref, ok := parseAlarmARN(key.ID)
	if !ok {
		s.k.Log().LogAttrs(context.Background(), slog.LevelError, "alarm evaluation timer names no alarm ARN; the timer is dropped",
			slog.String("timer", key.ID))
		return
	}
	// Set in the write, as Write answers its zero result when an effect
	// fails after the commit, and a committed transition is finished
	// either way.
	var done *changed
	_, err := kernel.Write(c, s.cells, ref.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		a, ok := st.Alarms[ref.name]
		if !ok {
			tx.Unchanged()
			return struct{}{}, nil
		}
		cfg, notEvaluable := alarm.ConfigOf(&a.Config)
		if notEvaluable != nil {
			tx.Unchanged()
			return struct{}{}, nil
		}
		tx.Arm(s.evaluation, key.ID, clock.FromWire(cfg.Next(due.Wire())))
		bins := alarmBins(st, &a.Config, cfg, due)
		warmedNow := false
		if cfg.WarmUp != nil && !a.WarmedUp && !a.Created.IsZero() {
			// During warm-up the alarm stays as it is and runs no action
			// (cloudwatch-alarms.md 2.4).
			if !cfg.WarmUp.Over(due.Sub(a.Created), bins, cfg.N) {
				tx.Unchanged()
				return struct{}{}, nil
			}
			a.WarmedUp, warmedNow = true, true
			st.Alarms[ref.name] = a
		}
		tr, ok := alarm.Evaluate(cfg, bins, a.State.Value).(alarm.Transition)
		if !ok {
			if !warmedNow {
				tx.Unchanged()
			}
			return struct{}{}, nil
		}
		data := alarm.ReasonData(cfg, tr, due.Wire())
		next := AlarmState{Value: tr.To, Reason: alarm.Reason(cfg, tr), ReasonData: &data, Updated: due, Transitioned: due}
		done = new(s.transition(tx, ref, &a, next, c, warmedNow))
		st.Alarms[ref.name] = a
		return struct{}{}, nil
	})
	if err != nil {
		// The write function never fails, so this is an effect that
		// failed after the commit: the evaluation's state and its next
		// evaluation stand.
		s.k.Log().LogAttrs(context.Background(), slog.LevelError, "an effect of an alarm evaluation failed after its write committed; the alarm's state and its next evaluation stand",
			slog.String("alarm", key.ID), slog.String("error", err.Message))
	}
	if done == nil {
		return
	}
	s.finishTransition(context.Background(), c, ref, done, due)
	s.events.FlushLater()
}

// changed is what a transition leaves to do once its write commits: the
// StateUpdate history item to append, the actions to run and the state
// change event to send.
type changed struct {
	item    HistoryItem
	actions []func(tx *kernel.Tx)
	event   serviceevent.Event
}

// transition moves a to next, a state of a different value, in the write
// tx of call c, the SetAlarmState call or the evaluation: the state and
// the StateChange journal event (cloudwatch-alarms.md 3.8). endsWarmUp is
// an evaluation's transition out of warm-up, whose old state was
// IN_WARM_UP (CWA-22). It returns the rest of the transition for
// finishTransition.
func (s *Service) transition(tx *kernel.Tx, ref alarmRef, a *Alarm, next AlarmState, c *kernel.Call, endsWarmUp bool) changed {
	old := a.State
	a.State = next
	tx.Record(ref.scope, journal.StateChange{Resource: ref.arn(), From: journal.ResourceState(old.Value), To: journal.ResourceState(next.Value)})
	ch := alarm.Change{Old: old.record(), New: next.record()}
	ch.Old.InWarmUp = endsWarmUp
	summary, data := alarm.StateUpdate(ch)
	return changed{
		item:    HistoryItem{At: next.Updated, Summary: summary, Data: data},
		actions: actionRuns(ref, a, ch, string(c.ID())),
		event:   stateChangeEvent(ref, a, ch),
	}
}

// finishTransition completes a committed transition in the order
// cloudwatch-alarms.md 12.4 item 5 gives after the state write: the
// StateUpdate history item (cloudwatch-alarms.md 8.2), then the new
// state's actions, as the effects of the history write, so no delivery
// starts before the item exists; then the state change event, queued
// behind every earlier alarm event and admitted at (zero: the call's
// admission). It returns the queued event for the caller to flush.
//
// A failed history write is logged rather than returned: the transition
// has committed, and the event is queued either way so EventBridge sees
// every transition.
func (s *Service) finishTransition(ctx context.Context, c *kernel.Call, ref alarmRef, ch *changed, at clock.Instant) *serviceevent.Queued {
	if err := s.appendHistory(c, ref.scope, ref.name, ch.item, ch.actions); err != nil {
		s.k.Log().LogAttrs(ctx, slog.LevelError, "the history write of an alarm transition failed; the transition stands, and its history item or actions may be lost",
			slog.String("alarm", ref.arn().String()), slog.String("error", err.Message))
	}
	return s.events.Push(c, at, ch.event)
}

// record is the state as one side of an alarm.Change.
func (st AlarmState) record() alarm.StateRecord {
	return alarm.StateRecord{Value: st.Value, Reason: st.Reason, ReasonData: st.ReasonData, Timestamp: st.Updated.Wire()}
}

// armEvaluation arms the alarm's next evaluation after now in tx, or, for an
// alarm understudy cannot evaluate, disarms it and journals why
// (cloudwatch-alarms.md 12.7).
func (s *Service) armEvaluation(tx *kernel.Tx, ref alarmRef, cfg *alarm.Config, notEvaluable *alarm.NotEvaluableError, now clock.Instant) {
	arn := ref.arn()
	if notEvaluable != nil {
		tx.Disarm(s.evaluation, arn.String())
		tx.Record(ref.scope, journal.Advisory{Kind: journal.AdvisoryAlarmNotEvaluated, Resource: arn, Text: notEvaluable.Error()})
		return
	}
	tx.Arm(s.evaluation, arn.String(), clock.FromWire(cfg.Next(now.Wire())))
}

// parseAlarmARN is the alarm an evaluation timer's id names.
func parseAlarmARN(s string) (alarmRef, bool) {
	arn, err := aws.ParseARN(s)
	if err != nil || arn.Service != "cloudwatch" {
		return alarmRef{}, false
	}
	scope, err := aws.ScopeOf(arn)
	if err != nil {
		return alarmRef{}, false
	}
	name, ok := strings.CutPrefix(arn.Resource, "alarm:")
	return alarmRef{scope: scope, name: name}, ok && name != ""
}
