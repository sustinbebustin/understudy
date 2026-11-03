package scheduler

import (
	"context"
	"log/slog"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Name returns "scheduler".
func (*Service) Name() ref.ServiceID { return "scheduler" }

// SchemaVersion returns the version of the service's snapshot section.
func (*Service) SchemaVersion() int { return stateVersion }

// Export serializes every non-empty scope to one section.
func (s *Service) Export() ([]store.Section, error) {
	return s.cells.Export(store.ScopeKeys(), stateVersion)
}

// Import loads sections written by Export of this schema version.
func (s *Service) Import(ss []store.Section) error {
	return s.cells.Import(store.ScopeKeys(), stateVersion, ss)
}

// Reset drops the scopes rs covers. Their armed fires find no schedule
// when they come due and do nothing.
func (s *Service) Reset(rs kernel.ResetScope) {
	s.cells.Reset(func(k aws.Scope) bool { return !rs.Covers(k) })
}

// Rearm arms the fire timer of every schedule with a next invocation;
// timers are never saved. It arms under the cell lock, where kernel.Write
// arms, so a concurrent write either commits first and is read here, or
// commits after and its own arm replaces this one.
//
// An invocation due before now came due while understudy was stopped, or
// before a loaded snapshot's time. It is skipped, journaled as one
// schedule-fires-skipped Advisory per schedule, rather than delivered in a
// burst: the plan moves to its first invocation at or after now, in the
// schedule's own phase, and a past-due at() is completed (scheduler.md
// 14.3.8; as internal/service/events/stateful.go). A schedule the catch-up
// leaves with no invocation under ActionAfterCompletion DELETE (an at(), or
// a rate or cron past its EndDate) is deleted, as its last fire would have
// deleted it, and journaled DELETED (scheduler.md 8, 14.6).
func (s *Service) Rearm(now clock.Instant) {
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		cell, ok := s.cells.Lookup(scope)
		if !ok {
			continue
		}
		skipped := store.Update(cell, func(st *State) ([]journal.Detail, bool) {
			return s.rearmScope(st, scope, now)
		})
		for _, d := range skipped {
			if _, err := s.k.Journal().Append(journal.Event{At: now, Scope: scope, Detail: d}); err != nil {
				s.k.Log().LogAttrs(context.Background(), slog.LevelWarn, "journal refused a skipped-schedule event; the schedule was rearmed or deleted regardless",
					slog.String("account", scope.Account.String()), slog.String("region", scope.Region.String()), slog.String("error", err.Error()))
			}
		}
	}
}

// rearmScope arms the schedules of one scope, moving each invocation due
// before now to its catch-up invocation; it returns an advisory per moved
// schedule, and a DELETED state change per schedule deleted because the
// catch-up left it no invocation under ActionAfterCompletion DELETE
// (scheduler.md 8, 14.6), and whether it changed st.
func (s *Service) rearmScope(st *State, scope aws.Scope, now clock.Instant) ([]journal.Detail, bool) {
	sched := s.k.Timers().Scheduler()
	var skipped []journal.Detail
	for _, group := range slices.Sorted(maps.Keys(st.Schedules)) {
		for _, name := range slices.Sorted(maps.Keys(st.Schedules[group])) {
			r := scheduleRef{scope: scope, group: group, name: name}
			sc := st.Schedules[group][name]
			at, ok := sc.Run.(scheduled)
			if !ok {
				continue
			}
			var next run = at
			if at.Due.Before(now) {
				var events []journal.Detail
				next, events = s.catchUpOne(st, r, sc, at, now)
				skipped = append(skipped, events...)
			}
			if n, ok := next.(scheduled); ok {
				sched.Arm(s.fire.Key(r.arn().String()), n.Due)
			}
		}
	}
	return skipped, len(skipped) > 0
}

// catchUpOne moves sc, whose invocation at came due before now, to its
// catch-up run, deleting it when that leaves no invocation under
// ActionAfterCompletion DELETE; it returns the run and what to journal.
func (s *Service) catchUpOne(st *State, r scheduleRef, sc schedule, at scheduled, now clock.Instant) (run, []journal.Detail) {
	next := offset(&sc.Plan, catchUp(sc.Plan, at, now), s.opts.FlexibleOffset, s.ids)
	sc.Run = next
	_, left := next.(scheduled)
	gone := !left && sc.Plan.deletesWhenDone()
	text := nextText(next)
	if gone {
		st.remove(r)
		text += "; ActionAfterCompletion DELETE deleted it"
	} else {
		st.put(r, sc)
	}
	events := []journal.Detail{journal.Advisory{
		Kind: journal.AdvisoryScheduleFiresSkipped, Resource: r.arn(),
		Text: "skipped the invocations due from " + at.Due.String() + " until " + now.String() +
			", which came due while understudy was stopped or before the loaded state's time; " + text,
	}}
	if gone {
		events = append(events, journal.StateChange{Resource: r.arn(), From: journal.ResourceState(sc.Echo.state()), To: scheduleDeleted})
	}
	return next, events
}

// nextText says what a skipped schedule does next.
func nextText(next run) string {
	switch n := next.(type) {
	case scheduled:
		return "the next is due " + n.Due.String()
	case completed:
		return "it was a one-time at() schedule and is completed"
	case ended:
		return "it has no invocation left before its EndDate"
	case disabled:
		return "it is disabled"
	}
	return ""
}

// Resources lists the groups scope holds (default once a write
// materialized it) by name, then its schedules by group and name with
// their expression.
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return nil
	}
	return store.Read(cell, func(st *State) []kernel.Resource {
		var out []kernel.Resource
		for _, name := range slices.Sorted(maps.Keys(st.Groups)) {
			out = append(out, kernel.Resource{ARN: groupARN(scope, name), Summary: "schedule group"})
		}
		for _, group := range slices.Sorted(maps.Keys(st.Schedules)) {
			for _, name := range slices.Sorted(maps.Keys(st.Schedules[group])) {
				out = append(out, kernel.Resource{
					ARN:     scheduleRef{scope: scope, group: group, name: name}.arn(),
					Summary: st.Schedules[group][name].Echo.Expression,
				})
			}
		}
		return out
	})
}

// Scopes returns the accounts and regions that hold a state cell.
func (s *Service) Scopes() []aws.Scope { return s.cells.Keys(store.ScopeKeys()) }
