package lambda

import (
	"maps"
	"slices"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// TimerSettle ends a provisioning state under TransitionsClock: a
// function version's Creating or Updating, an event source mapping's
// transient state. Its id is the version's qualified ARN or the mapping's
// ARN.
const TimerSettle kernel.TimerKind = "lambda.settle"

// TimerImage starts making a version's image present. It fires at once:
// the timer only moves the pull out of the call that asked for it, and
// Rearm restarts the pulls a snapshot load interrupted. Its id is the
// version's qualified ARN.
const TimerImage kernel.TimerKind = "lambda.image"

// settleDelay is how long a provisioning state lasts under
// TransitionsClock (ARCHITECTURE.md 14.7).
const settleDelay = 10 * time.Millisecond

// Service is AWS Lambda: one state cell per account and region.
type Service struct {
	k     *kernel.Kernel
	ids   *ids.Source
	blobs *blob.Store
	cells *store.Partitioned[aws.Scope, State]
	opts  Options
	// settle and image are TimerSettle and TimerImage as registered.
	settle kernel.HandledKind
	image  kernel.HandledKind
}

// The service is the snapshot and reset view of its state.
var _ kernel.Stateful = (*Service)(nil)

// New binds the Lambda control plane and registers its timers. Invoke,
// the function URL data plane and every operation outside phase 1 stay
// unbound and answer 501.
func New(d kernel.ServiceDeps, o Options) *Service {
	s := &Service{
		k:     d.Kernel,
		ids:   d.IDs,
		blobs: d.Blob,
		cells: store.NewPartitioned(d.Kernel.Gate(), stateCodec(), newState),
		opts:  o,
	}
	s.settle = d.Kernel.Timers().Handle(TimerSettle, s.onSettle)
	s.image = d.Kernel.Timers().Handle(TimerImage, s.onImage)
	b := lambdaapi.NewBinder(d.Binder)
	s.bindFunctions(b)
	s.bindVersions(b)
	s.bindAliases(b)
	s.bindPermissions(b)
	s.bindConcurrency(b)
	s.bindTags(b)
	s.bindURLConfigs(b)
	s.bindEventInvokeConfigs(b)
	s.bindMappings(b)
	return s
}

// Switches lists the behaviors chosen without AWS-recorded evidence.
func (s *Service) Switches() []fidelity.Entry {
	return []fidelity.Entry{fidelity.Switch[string]{
		Name:  "lambda.stateTransitions",
		Value: s.opts.StateTransitions.String(),
		Label: evidence.AWSDoc,
		Ref:   "docs/design/ARCHITECTURE.md#9-the-clock",
	}.Entry()}
}

// Name returns "lambda".
func (*Service) Name() ref.ServiceID { return "lambda" }

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

// Reset drops the scopes rs covers. It releases no blob reference: the
// admin reset releases those the dropped scopes' sections listed.
func (s *Service) Reset(rs kernel.ResetScope) {
	s.cells.Reset(func(k aws.Scope) bool { return !rs.Covers(k) })
}

// Rearm arms the timers the state implies, which are never saved: the
// settle timer of every version and mapping in a provisioning state under
// TransitionsClock, and the image timer of every version waiting for its
// image, whose pull a load interrupted.
func (s *Service) Rearm(now clock.Instant) {
	sched := s.k.Timers().Scheduler()
	for _, scope := range s.cells.Keys(store.ScopeKeys()) {
		cell, ok := s.cells.Lookup(scope)
		if !ok {
			continue
		}
		for _, a := range store.Read(cell, func(st *State) []clock.Armed { return s.armed(scope, st, now) }) {
			sched.Arm(a.Key, a.Due)
		}
	}
}

// Resources lists the functions and event source mappings of scope.
func (s *Service) Resources(scope aws.Scope) []kernel.Resource {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		return nil
	}
	return store.Read(cell, func(st *State) []kernel.Resource {
		var out []kernel.Resource
		for _, name := range slices.Sorted(maps.Keys(st.Functions)) {
			if a, err := aws.ParseARN(functionARN(scope, name)); err == nil {
				out = append(out, kernel.Resource{ARN: a, Summary: string(renderStatus(st.Functions[name].Latest.Status).State)})
			}
		}
		for _, uuid := range slices.Sorted(maps.Keys(st.Mappings)) {
			if a, err := aws.ParseARN(mappingARN(scope, uuid)); err == nil {
				out = append(out, kernel.Resource{ARN: a, Summary: string(st.Mappings[uuid].state())})
			}
		}
		return out
	})
}

// Scopes returns the accounts and regions that hold a state cell.
func (s *Service) Scopes() []aws.Scope { return s.cells.Keys(store.ScopeKeys()) }

// armed lists the timers st implies.
func (s *Service) armed(scope aws.Scope, st *State, now clock.Instant) []clock.Armed {
	var out []clock.Armed
	for _, name := range slices.Sorted(maps.Keys(st.Functions)) {
		versions := st.Functions[name].all()
		for i := range versions {
			if kind, due, ok := s.provisioning(&versions[i], now); ok {
				out = append(out, clock.Armed{Key: kind.Key(versionID(scope, name, versions[i].Number)), Due: due})
			}
		}
	}
	for _, uuid := range slices.Sorted(maps.Keys(st.Mappings)) {
		if t := st.Mappings[uuid].Transition; t != nil {
			out = append(out, clock.Armed{Key: s.settle.Key(mappingARN(scope, uuid)), Due: t.Settles})
		}
	}
	return out
}

// provisioning is the timer v's status waits on: the settle timer of
// Creating and Updating, the image timer, due now, of a version waiting
// for its image. false when v waits on nothing.
func (s *Service) provisioning(v *Version, now clock.Instant) (kernel.HandledKind, clock.Instant, bool) {
	switch st := v.Status.(type) {
	case Creating:
		return s.settle, st.Settles, true
	case PullingImage:
		return s.image, now, true
	case Active:
		switch u := st.Update.(type) {
		case Updating:
			return s.settle, u.Settles, true
		case UpdatingImage:
			return s.image, now, true
		case Successful, UpdateFailed:
		}
	case Failed:
	}
	return kernel.HandledKind{}, clock.Instant{}, false
}

// armProvisioning arms the timer v's status waits on, if any.
func (s *Service) armProvisioning(tx *kernel.Tx, scope aws.Scope, name string, v *Version, now clock.Instant) {
	if kind, due, ok := s.provisioning(v, now); ok {
		tx.Arm(kind, versionID(scope, name, v.Number), due)
	}
}

// disarmProvisioning removes the timers of a deleted version.
func (s *Service) disarmProvisioning(tx *kernel.Tx, scope aws.Scope, name, number string) {
	tx.Disarm(s.settle, versionID(scope, name, number))
	tx.Disarm(s.image, versionID(scope, name, number))
}

// versionID is a version's timer id: its qualified ARN.
func versionID(scope aws.Scope, name, number string) string {
	return functionARN(scope, name) + ":" + number
}

// clockTransitions reports the lambda.stateTransitions: clock switch.
func (s *Service) clockTransitions() bool { return s.opts.StateTransitions == TransitionsClock }

// view runs fn on the state of scope under the cell's lock; a scope
// without a cell reads as empty. fn's result must not alias the state.
func view[R any](s *Service, scope aws.Scope, fn func(st *State) (R, *awserr.Error)) (R, *awserr.Error) {
	cell, ok := s.cells.Lookup(scope)
	if !ok {
		st := newState(scope)
		return fn(&st)
	}
	type result struct {
		r   R
		err *awserr.Error
	}
	out := store.Read(cell, func(st *State) result {
		r, err := fn(st)
		return result{r, err}
	})
	return out.r, out.err
}

// write runs fn on the state of scope through kernel.Write.
func write[R any](s *Service, c *kernel.Call, scope aws.Scope, fn func(st *State, now clock.Instant, tx *kernel.Tx) (R, *awserr.Error)) (R, *awserr.Error) {
	return kernel.Write(c, s.cells, scope, fn)
}

// touch records the resources a call addressed, by the ARNs this service
// minted for them.
func touch(c *kernel.Call, arns ...string) {
	for _, s := range arns {
		if a, err := aws.ParseARN(s); err == nil {
			c.Touch(a)
		}
	}
}

// full, stored and partial are the coverage of the operations this
// package binds.
func full() registry.Full { return registry.Full{} }

func stored(note string) registry.StoredOnly { return registry.StoredOnly{Note: note} }

func partial(note string) registry.Partial { return registry.Partial{Note: note} }
