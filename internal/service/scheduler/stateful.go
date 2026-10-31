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

// Import loads sections written by Export of this schema version; a
// default group no write materialized is dated at the load.
func (s *Service) Import(ss []store.Section) error {
	if err := s.cells.Import(store.ScopeKeys(), stateVersion, ss); err != nil {
		return err
	}
	s.restart()
	return nil
}

// Reset drops the scopes rs covers. Their armed fires find no schedule
// when they come due and do nothing. A default group no write
// materialized is dated at the reset.
func (s *Service) Reset(rs kernel.ResetScope) {
	s.cells.Reset(func(k aws.Scope) bool { return !rs.Covers(k) })
	s.restart()
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
// 14.3.8; as internal/service/events/stateful.go).
func (s *Service) Rearm(now clock.Instant) {
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		cell, ok := s.cells.Lookup(scope)
		if !ok {
			continue
		}
		skipped := store.Update(cell, func(st *State) ([]journal.Advisory, bool) {
			return s.rearmScope(st, scope, now)
		})
		for _, a := range skipped {
			if _, err := s.k.Journal().Append(journal.Event{At: now, Scope: scope, Detail: a}); err != nil {
				s.k.Log().LogAttrs(context.Background(), slog.LevelWarn, "journal refused a skipped-schedule advisory",
					slog.String("schedule", a.Resource.String()), slog.String("error", err.Error()))
			}
		}
	}
}

// rearmScope arms the schedules of one scope, moving each invocation due
// before now to its catch-up invocation; it returns an advisory per moved
// schedule and whether it changed st.
func (s *Service) rearmScope(st *State, scope aws.Scope, now clock.Instant) ([]journal.Advisory, bool) {
	sched := s.k.Timers().Scheduler()
	var skipped []journal.Advisory
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
				next = offset(&sc.Plan, catchUp(sc.Plan, at, now), s.opts.FlexibleOffset, s.ids)
				sc.Run = next
				st.put(r, sc)
				skipped = append(skipped, journal.Advisory{
					Kind: journal.AdvisoryScheduleFiresSkipped, Resource: r.arn(),
					Text: "skipped the invocations due from " + at.Due.String() + " until " + now.String() +
						", which came due while understudy was stopped or before the loaded state's time; " + nextText(next),
				})
			}
			if n, ok := next.(scheduled); ok {
				sched.Arm(s.fire.Key(r.arn().String()), n.Due)
			}
		}
	}
	return skipped, len(skipped) > 0
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
