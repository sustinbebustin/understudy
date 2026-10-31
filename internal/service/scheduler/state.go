package scheduler

import (
	"fmt"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/schedexpr"
	"github.com/sustinbebustin/understudy/internal/store"
)

// State is the Scheduler state of one account and region: its schedule
// groups by name, and its schedules by group, then name (scheduler.md
// 14.2). Records are values, replaced on change.
type State struct {
	// Groups are the scope's created groups, and the default group once a
	// tag write materializes it (14.2.2).
	Groups    map[string]scheduleGroup       `json:"groups,omitempty"`
	Schedules map[string]map[string]schedule `json:"schedules"`
}

func newState(aws.Scope) State {
	return State{Groups: map[string]scheduleGroup{}, Schedules: map[string]map[string]schedule{}}
}

// IsZero reports a scope without groups or schedules, which snapshots
// skip.
func (s *State) IsZero() bool {
	if len(s.Groups) > 0 {
		return false
	}
	for _, g := range s.Schedules {
		if len(g) > 0 {
			return false
		}
	}
	return true
}

// Partitioned finds IsZero by a type assertion; this keeps a rename from
// silently dropping the skip.
var _ store.Zeroer = (*State)(nil)

// schedule is one schedule: its members as accepted, the plan parsed from
// them (rebuilt on load, never saved), where it is in the plan, and its
// creation and last modification instants.
type schedule struct {
	Echo     echo          `json:"echo"`
	Plan     plan          `json:"-"`
	Run      run           `json:"run"`
	Created  clock.Instant `json:"created"`
	Modified clock.Instant `json:"modified"`
}

func (s *State) get(r scheduleRef) (schedule, bool) {
	sc, ok := s.Schedules[r.group][r.name]
	return sc, ok
}

func (s *State) put(r scheduleRef, sc schedule) {
	g := s.Schedules[r.group]
	if g == nil {
		g = map[string]schedule{}
		s.Schedules[r.group] = g
	}
	g[r.name] = sc
}

func (s *State) remove(r scheduleRef) {
	delete(s.Schedules[r.group], r.name)
	if len(s.Schedules[r.group]) == 0 {
		delete(s.Schedules, r.group)
	}
}

// StoredScheduleError is a snapshot schedule whose members do not parse
// into a plan: the snapshot was edited or written by another build.
type StoredScheduleError struct {
	Group, Name string
	Reason      string
}

func (e *StoredScheduleError) Error() string {
	return fmt.Sprintf("scheduler: the snapshot's schedule %s/%s does not parse: %s; the snapshot was edited or written by another build, and the state was not loaded",
		e.Group, e.Name, e.Reason)
}

// load rebuilds every schedule's plan from its members, as its write
// parsed them. Rate units are read under AnyUnitAgreement, which accepts
// every expression either setting of scheduler.rate.unit-agreement did.
// The codec is handed no scope, and a target in the schedule's Region was
// checked by the write, so each target is parsed in its own ARN's scope.
func (s *State) load() error {
	if s.Groups == nil {
		s.Groups = map[string]scheduleGroup{}
	}
	if s.Schedules == nil {
		s.Schedules = map[string]map[string]schedule{}
	}
	for group, g := range s.Schedules {
		for name := range g {
			sc := g[name]
			a, aerr := aws.ParseARN(sc.Echo.Target.Arn)
			scope, serr := aws.ScopeOf(a)
			if aerr != nil || serr != nil {
				return &StoredScheduleError{Group: group, Name: name, Reason: "its target " + sc.Echo.Target.Arn + " is not an ARN of a served Region"}
			}
			m := sc.Echo.members()
			p, err := accept(scope, &m, schedexpr.AnyUnitAgreement, atLoad)
			if err != nil {
				return &StoredScheduleError{Group: group, Name: name, Reason: err.Message}
			}
			if sc.Run == nil {
				return &StoredScheduleError{Group: group, Name: name, Reason: "it has no run"}
			}
			sc.Plan = p
			g[name] = sc
		}
	}
	return nil
}

// codec is the snapshot codec of State; a loaded section is checked by
// rebuilding its plans (State.load).
func codec() store.Codec {
	runs, err := jsonx.NewUnion("scheduler.run",
		jsonx.Variant[run, scheduled]("scheduled"), jsonx.Variant[run, disabled]("disabled"),
		jsonx.Variant[run, completed]("completed"), jsonx.Variant[run, ended]("ended"))
	if err != nil {
		return store.Codec{
			Marshal:   func(any) ([]byte, error) { return nil, err },
			Unmarshal: func([]byte, any) error { return err },
		}
	}
	c := jsonx.NewCodec(runs)
	unmarshal := func(data []byte, v any) error {
		if err := c.Unmarshal(data, v); err != nil {
			return err
		}
		if st, ok := v.(*State); ok {
			return st.load()
		}
		return nil
	}
	return store.Codec{Marshal: c.Marshal, Unmarshal: unmarshal}
}
