package outbox

import (
	"fmt"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Name returns "outbox", its directory in snapshot archives.
func (o *Outbox) Name() ref.ServiceID { return "outbox" }

// SchemaVersion returns the version of State.
func (o *Outbox) SchemaVersion() int { return stateVersion }

// Export serializes every scope with pending deliveries to one section.
func (o *Outbox) Export() ([]store.Section, error) {
	e := o.e.Load()
	if e == nil {
		return nil, &UnboundError{}
	}
	return e.cells.Export(store.ScopeKeys(), stateVersion)
}

// Import loads sections written by Export of this schema version and
// raises the Seq counter past every loaded entry's, so entries accepted
// after the load order after them. Rearm then arms their attempts.
func (o *Outbox) Import(ss []store.Section) error {
	e := o.e.Load()
	if e == nil {
		return &UnboundError{}
	}
	if err := e.cells.Import(store.ScopeKeys(), stateVersion, ss); err != nil {
		return err
	}
	var last uint64
	for _, scope := range e.cells.Keys(store.ScopeKeys()) {
		cell, ok := e.cells.Lookup(scope)
		if !ok {
			continue
		}
		last = max(last, store.Read(cell, (*State).lastSeq))
	}
	for {
		cur := e.seq.Load()
		if cur >= last || e.seq.CompareAndSwap(cur, last) {
			return nil
		}
	}
}

// Reset drops the pending deliveries into the scopes rs covers. Their armed
// attempts find nothing when they fire.
func (o *Outbox) Reset(rs kernel.ResetScope) {
	if e := o.e.Load(); e != nil {
		e.cells.Reset(func(k aws.Scope) bool { return !rs.Covers(k) })
	}
}

// Rearm arms the next attempt of every pending delivery outside a FIFO lane
// and of the head of each lane, in acceptance order; an
// attempt due before now fires at once, stamped with its own due instant.
func (o *Outbox) Rearm(clock.Instant) {
	e := o.e.Load()
	if e == nil {
		return
	}
	sched := e.k.Timers().Scheduler()
	for _, scope := range e.cells.Keys(store.ScopeKeys()) {
		cell, ok := e.cells.Lookup(scope)
		if !ok {
			continue
		}
		pending := store.Read(cell, func(st *State) []clock.Armed {
			armable := st.armable()
			out := make([]clock.Armed, 0, len(armable))
			for _, en := range armable {
				out = append(out, clock.Armed{Key: e.attempt.Key(timerID(scope, en.ID)), Due: en.NextAt})
			}
			return out
		})
		for _, a := range pending {
			sched.Arm(a.Key, a.Due)
		}
	}
}

// Scopes lists the scopes the outbox holds a cell for, whether or not any
// delivery is pending there; nil before Bind.
func (o *Outbox) Scopes() []aws.Scope {
	e := o.e.Load()
	if e == nil {
		return nil
	}
	return e.cells.Keys(store.ScopeKeys())
}

// Resources lists the deliveries pending into scope, oldest first, each by
// its target.
func (o *Outbox) Resources(scope aws.Scope) []kernel.Resource {
	e := o.e.Load()
	if e == nil {
		return nil
	}
	cell, ok := e.cells.Lookup(scope)
	if !ok {
		return nil
	}
	return store.Read(cell, func(st *State) []kernel.Resource {
		out := make([]kernel.Resource, 0, len(st.Entries))
		for _, en := range st.sorted() {
			target, _ := targetARN(en.Request.Target)
			out = append(out, kernel.Resource{
				ARN: target,
				Summary: fmt.Sprintf("pending %s delivery %s from %s: attempt %d due %s",
					en.Request.Mechanism, en.ID, en.Request.Source, en.Attempt+1, en.NextAt),
			})
		}
		return out
	})
}
