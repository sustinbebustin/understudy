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
)

// onEvaluation evaluates the alarm a TimerAlarmEvaluation names at its due
// instant and arms the next evaluation (cloudwatch-alarms.md 12.3). An
// alarm that is gone or no longer evaluable is left alone and its timer
// dies. An evaluation that keeps the state writes nothing and journals
// nothing (cloudwatch.alarm.unchanged-state); a transition writes the
// state, journals a StateChange and appends a history item.
func (s *Service) onEvaluation(c *kernel.Call, key clock.TimerKey, due clock.Instant) {
	ref, ok := parseAlarmARN(key.ID)
	if !ok {
		s.k.Log().LogAttrs(context.Background(), slog.LevelError, "alarm evaluation timer names no alarm ARN; the timer is dropped",
			slog.String("timer", key.ID))
		return
	}
	item, err := kernel.Write(c, s.cells, ref.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (*HistoryItem, *awserr.Error) {
		a, ok := st.Alarms[ref.name]
		if !ok {
			tx.Unchanged()
			return nil, nil
		}
		cfg, notEvaluable := alarm.ConfigOf(&a.Config)
		if notEvaluable != nil {
			tx.Unchanged()
			return nil, nil
		}
		tx.Arm(s.evaluation, key.ID, clock.FromWire(cfg.Next(due.Wire())))
		bins := alarmBins(st, &a.Config, cfg, due)
		warmedNow := false
		if cfg.WarmUp != nil && !a.WarmedUp && !a.Created.IsZero() {
			// During warm-up the alarm stays as it is and runs no action
			// (cloudwatch-alarms.md 2.4).
			if !cfg.WarmUp.Over(due.Sub(a.Created), bins, cfg.N) {
				tx.Unchanged()
				return nil, nil
			}
			a.WarmedUp, warmedNow = true, true
			st.Alarms[ref.name] = a
		}
		tr, ok := alarm.Evaluate(cfg, bins, a.State.Value).(alarm.Transition)
		if !ok {
			if !warmedNow {
				tx.Unchanged()
			}
			return nil, nil
		}
		data := alarm.ReasonData(cfg, tr, due.Wire())
		next := AlarmState{Value: tr.To, Reason: alarm.Reason(cfg, tr), ReasonData: &data, Updated: due, Transitioned: due}
		it := transition(tx, ref, &a, next)
		st.Alarms[ref.name] = a
		return &it, nil
	})
	if err == nil && item != nil {
		err = s.appendHistory(c, ref.scope, ref.name, *item)
	}
	if err != nil {
		s.k.Log().LogAttrs(context.Background(), slog.LevelError, "alarm evaluation failed; the alarm keeps its state and the next evaluation is not armed until a snapshot load re-arms it",
			slog.String("alarm", key.ID), slog.String("error", err.Message))
	}
}

// transition moves a to next, a state of a different value, journaling the
// StateChange in the write tx (cloudwatch-alarms.md 3.8), and returns the
// StateUpdate history item the change writes (cloudwatch-alarms.md 8.2).
func transition(tx *kernel.Tx, ref alarmRef, a *Alarm, next AlarmState) HistoryItem {
	old := a.State
	a.State = next
	tx.Record(ref.scope, journal.StateChange{Resource: ref.arn(), From: journal.ResourceState(old.Value), To: journal.ResourceState(next.Value)})
	summary, data := alarm.StateUpdate(alarm.Change{Old: old.record(), New: next.record()})
	return HistoryItem{At: next.Updated, Summary: summary, Data: data}
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
