package events

import (
	"context"
	"log/slog"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/events/envelope"
)

// onSchedule fires a scheduled rule at due: it sends the scheduled event to
// every target of the rule in creation order and arms the next fire. A
// timer whose rule is gone, disabled or rescheduled since it was armed does
// nothing.
func (s *Service) onSchedule(c *kernel.Call, key clock.TimerKey, due clock.Instant) {
	ctx := context.Background()
	r, ok := parseRuleARN(key.ID)
	if !ok {
		s.k.Log().LogAttrs(ctx, slog.LevelError, "an EventBridge schedule timer names no rule; it was dropped",
			slog.String("timer", key.ID))
		return
	}
	c.Touch(r.arn())
	var f fanOut
	_, err := kernel.Write(c, s.cells, r.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		f = fanOut{}
		rule, ok := st.Rules[r.name]
		if !ok || rule.NextFire.Compare(due) != 0 || !rule.enabled() {
			tx.Unchanged()
			return struct{}{}, nil
		}
		sched, scheduled := rule.Trigger.schedule()
		if !scheduled {
			// Unreachable while setSchedule clears NextFire for a rule
			// without a schedule and State.check refuses a snapshot whose
			// rule has one; kept so a broken invariant never fires a
			// pattern-only rule.
			tx.Unchanged()
			return struct{}{}, nil
		}
		if len(rule.Targets) > 0 {
			ev := envelope.Scheduled(s.ids.UUID(ids.KindEventID).String(), r.scope.Account.String(), r.scope.Region.String(), r.arn().String(), due.Wire())
			for i := range rule.Targets {
				if dt, ok := f.resolve(r, &rule.Targets[i]); ok {
					f.send(r, &rule.Targets[i], dt, ev, due)
				}
			}
			for i := range f.requests {
				tx.Deliver(f.requests[i])
			}
		}
		rule.NextFire = sched.next(due)
		s.armSchedule(tx, r, rule.NextFire)
		st.Rules[r.name] = rule
		return struct{}{}, nil
	})
	if err != nil {
		s.k.Log().LogAttrs(ctx, slog.LevelError, "an EventBridge schedule fire failed; its deliveries may be lost and the rule's schedule may have stopped",
			slog.String("rule", r.arn().String()), slog.String("error", err.Message))
		return
	}
	s.report(ctx, c, r.scope, &f)
}
