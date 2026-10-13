package server

import (
	"bytes"
	"fmt"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
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

// RestoreClock sets the clock from a loaded archive.
func (r *runtime) RestoreClock(c snapshot.Clock) error {
	if _, err := r.clock.Restore(c.At, c.Mode); err != nil {
		return fmt.Errorf("restoring the clock: %w", err)
	}
	return nil
}

// RestoreUnderGate sets the rest of the runtime state from a loaded
// archive, taking only the leaf locks of the id source, the journal
// counter and the fault store. The id streams move to the recorded
// positions only when both runs are seeded; the journal counter only moves
// forward, so an id is never issued twice; an archive without a faults
// entry clears the rules.
func (r *runtime) RestoreUnderGate(h snapshot.Head) error {
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

// identityFile is the path of the identity section under
// services/identity/.
const identityFile = "keys.json"

// mintedKeys is the identity store as a snapshot section: every access key
// IAM and STS minted, with the principal it resolves to. Without it a
// restart or a load would leave IAM listing keys that resolve to the
// default account's root. A store holding no key writes no section.
type mintedKeys struct {
	id *identity.Store
	// blank is the export of a store holding no key, which Import loads
	// from an archive without the section.
	blank []byte
}

var _ snapshot.Stateful = mintedKeys{}

// newMintedKeys wraps id.
func newMintedKeys(id *identity.Store) (mintedKeys, error) {
	blank, err := identity.New(ids.NewRandom(), aws.DefaultAccount()).Export()
	if err != nil {
		return mintedKeys{}, fmt.Errorf("exporting an empty identity store: %w", err)
	}
	return mintedKeys{id: id, blank: blank}, nil
}

// Name returns "identity".
func (mintedKeys) Name() ref.ServiceID { return "identity" }

// SchemaVersion returns the version of the identity export.
func (mintedKeys) SchemaVersion() int { return identity.StateVersion }

// Export returns the minted keys as one section, or none when there are
// none.
func (k mintedKeys) Export() ([]store.Section, error) {
	data, err := k.id.Export()
	if err != nil {
		return nil, err
	}
	if bytes.Equal(data, k.blank) {
		return nil, nil
	}
	return []store.Section{{Path: identityFile, Version: identity.StateVersion, Data: data}}, nil
}

// ResetAll keeps the keys: Import, which a load always runs after it,
// replaces them as a whole and keeps them when it refuses the section.
func (mintedKeys) ResetAll() {}

// Import replaces the minted keys with the section's, or with none when
// there is no section.
func (k mintedKeys) Import(ss []store.Section) error {
	data := k.blank
	switch {
	case len(ss) == 0:
	case len(ss) > 1:
		return &store.SectionError{Path: ss[1].Path, Defect: store.DuplicateKey}
	case ss[0].Path != identityFile:
		return &store.SectionError{Path: ss[0].Path, Defect: store.UndecodablePath, Err: fmt.Errorf("the identity section is %s", identityFile)}
	case ss[0].Version != identity.StateVersion:
		return &store.VersionMismatchError{Path: ss[0].Path, Section: ss[0].Version, Running: identity.StateVersion}
	default:
		data = ss[0].Data
	}
	if err := k.id.Import(data); err != nil {
		return &store.SectionError{Path: identityFile, Defect: store.UndecodableState, Err: err}
	}
	return nil
}

// Rearm does nothing: keys arm no timers.
func (mintedKeys) Rearm(clock.Instant) {}

// coordinator builds the snapshot coordinator over the identity store and
// the services, cutting under the kernel's gate and waiting on its pending
// effects. The identity store comes first, so a load that fails importing a
// service has already restored the keys the loaded cells name.
func coordinator[S kernel.Stateful](k *kernel.Kernel, blobs snapshot.BlobStore, rt *runtime, idStore *identity.Store, services []S, late []snapshot.Late, writer string) (*snapshot.Coordinator, error) {
	minted, err := newMintedKeys(idStore)
	if err != nil {
		return nil, fmt.Errorf("building the snapshot coordinator: %w", err)
	}
	adapted := make([]snapshot.Stateful, 0, 1+len(services))
	adapted = append(adapted, minted)
	for _, s := range services {
		adapted = append(adapted, stateful{s})
	}
	c, err := snapshot.NewCoordinator(snapshot.Config{
		Gate: k.Gate(), Blobs: blobs, Runtime: rt, Effects: k, Services: adapted, Late: late, Writer: writer, ModelPin: modelPin,
	})
	if err != nil {
		return nil, fmt.Errorf("building the snapshot coordinator: %w", err)
	}
	return c, nil
}
