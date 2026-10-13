package events

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// Name returns "eventbridge", the service's registry id; its ARNs use the
// "events" namespace.
func (*Service) Name() ref.ServiceID { return "eventbridge" }

// SchemaVersion returns the version of State.
func (*Service) SchemaVersion() int { return stateVersion }

// Export serializes every non-empty scope to one section.
func (s *Service) Export() ([]store.Section, error) {
	return s.cells.Export(store.ScopeKeys(), stateVersion)
}

// Import loads sections written by Export of this schema version.
func (s *Service) Import(ss []store.Section) error {
	return s.cells.Import(store.ScopeKeys(), stateVersion, ss)
}

// Reset drops the scopes rs covers.
func (s *Service) Reset(rs kernel.ResetScope) {
	s.cells.Reset(func(k aws.Scope) bool { return !rs.Covers(k) })
}

// Rearm arms the schedule timer of every rule with a next fire; timers are
// never saved. It arms under the cell lock, where kernel.Write arms, so a
// concurrent write either commits first and its next fire is read here, or
// commits after and its own arm replaces this one.
//
// A next fire before now came due while understudy was stopped, or before
// a loaded snapshot's time. Such fires are skipped, and journaled as one
// schedule-fires-skipped Advisory per rule, rather than delivered in a
// burst the moment the rule is armed: the rule's next fire moves to its
// first fire at or after now, in the schedule's own phase. Only an
// explicit clock advance delivers every fire it passes (ADR 0006).
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
					slog.String("rule", a.Resource.String()), slog.String("error", err.Error()))
			}
		}
	}
}

// rearmScope arms the rules of one scope's state, moving each next fire
// before now to its catch-up fire; it returns an advisory per moved rule
// and whether it changed st.
func (s *Service) rearmScope(st *State, scope aws.Scope, now clock.Instant) ([]journal.Advisory, bool) {
	sched := s.k.Timers().Scheduler()
	var skipped []journal.Advisory
	for _, name := range slices.Sorted(maps.Keys(st.Rules)) {
		r := st.Rules[name]
		if r.NextFire.IsZero() {
			continue
		}
		rule := ruleRef{scope: scope, name: name}
		if r.NextFire.Before(now) {
			missed := r.NextFire
			r.NextFire = catchUp(r, now)
			st.Rules[name] = r
			skipped = append(skipped, journal.Advisory{
				Kind: journal.AdvisoryScheduleFiresSkipped, Resource: rule.arn(),
				Text: "skipped the fires due from " + missed.String() + " until " + now.String() +
					", which came due while understudy was stopped or before the loaded state's time; the next fire is " + r.NextFire.String(),
			})
		}
		if !r.NextFire.IsZero() {
			sched.Arm(s.schedule.Key(rule.arn().String()), r.NextFire)
		}
	}
	return skipped, len(skipped) > 0
}

// maxCatchUpSteps bounds the walk catchUp takes through a long downtime;
// past it the next fire is the first strictly after now.
const maxCatchUpSteps = 100_000

// catchUp is r's first fire at or after now, walked from its stored next
// fire so a rate keeps its phase; zero when the schedule never fires again.
func catchUp(r Rule, now clock.Instant) clock.Instant {
	sched, ok := r.Trigger.schedule()
	if !ok {
		return clock.Instant{}
	}
	next := r.NextFire
	for range maxCatchUpSteps {
		if next.IsZero() || !next.Before(now) {
			return next
		}
		next = sched.next(next)
	}
	return sched.next(now)
}

// Resources lists the rules of scope with their state and target count.
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return nil
	}
	return store.Read(cell, func(st *State) []kernel.Resource {
		out := make([]kernel.Resource, 0, len(st.Rules))
		for _, name := range slices.Sorted(maps.Keys(st.Rules)) {
			r := st.Rules[name]
			out = append(out, kernel.Resource{
				ARN:     ruleRef{scope: scope, name: name}.arn(),
				Summary: string(r.State) + ", " + strconv.Itoa(len(r.Targets)) + " targets",
			})
		}
		return out
	})
}

// Scopes returns the accounts and regions that hold a state cell.
func (s *Service) Scopes() []aws.Scope { return s.cells.Keys(store.ScopeKeys()) }
