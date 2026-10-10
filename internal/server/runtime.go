package server

import (
	"fmt"

	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/snapshot"
	"github.com/sustinbebustin/understudy/internal/store"
)

// runtime is the state around the cells that a snapshot records: the
// clock, the id streams, the journal counter and the fault rules.
type runtime struct {
	clock   *clock.Clock
	ids     *ids.Source
	seed    *uint64
	journal *journal.Journal
	faults  *fault.Store
}

var _ snapshot.Runtime = (*runtime)(nil)

// Head reads the runtime state for a cut. Positions exist only for a
// seeded source.
func (r *runtime) Head() (snapshot.Head, error) {
	h := snapshot.Head{
		Clock:       snapshot.Clock{Mode: r.clock.Mode(), At: r.clock.Now()},
		Seed:        r.seed,
		JournalNext: r.journal.NextID(),
	}
	if r.seed != nil {
		positions, err := r.ids.Positions()
		if err != nil {
			return snapshot.Head{}, fmt.Errorf("reading the id stream positions: %w", err)
		}
		h.Positions = positions
	}
	faults, err := r.faults.Export()
	if err != nil {
		return snapshot.Head{}, fmt.Errorf("exporting the fault rules: %w", err)
	}
	h.Faults = faults
	return h, nil
}

// Restore sets the runtime state from a loaded archive. The id streams
// move to the recorded positions only when both runs are seeded; the
// journal counter only moves forward, so an id is never issued twice; an
// archive without a faults entry clears the rules.
func (r *runtime) Restore(h snapshot.Head) error {
	if _, err := r.clock.Restore(h.Clock.At, h.Clock.Mode); err != nil {
		return fmt.Errorf("restoring the clock: %w", err)
	}
	if r.seed != nil && h.Positions != nil {
		if err := r.ids.Seek(h.Positions); err != nil {
			return fmt.Errorf("restoring the id stream positions: %w", err)
		}
	}
	if h.JournalNext > r.journal.NextID() {
		if err := r.journal.Seek(h.JournalNext); err != nil {
			return fmt.Errorf("restoring the journal counter: %w", err)
		}
	}
	if h.Faults == nil {
		r.faults.Clear()
		return nil
	}
	if err := r.faults.Import(h.Faults); err != nil {
		return fmt.Errorf("restoring the fault rules: %w", err)
	}
	return nil
}

// Now is emulator time.
func (r *runtime) Now() clock.Instant { return r.clock.Now() }

// stateful adapts a kernel.Stateful to the snapshot coordinator, whose
// ResetAll is a reset of every scope.
type stateful struct{ kernel.Stateful }

func (s stateful) ResetAll() { s.Reset(kernel.ResetScope{}) }

// coordinator builds the snapshot coordinator over the services.
func coordinator[S kernel.Stateful](gate *store.Gate, blobs snapshot.BlobStore, rt *runtime, services []S, late []snapshot.Late, writer string) (*snapshot.Coordinator, error) {
	adapted := make([]snapshot.Stateful, len(services))
	for i, s := range services {
		adapted[i] = stateful{s}
	}
	c, err := snapshot.NewCoordinator(snapshot.Config{
		Gate: gate, Blobs: blobs, Runtime: rt, Services: adapted, Late: late, Writer: writer, ModelPin: modelPin,
	})
	if err != nil {
		return nil, fmt.Errorf("building the snapshot coordinator: %w", err)
	}
	return c, nil
}
