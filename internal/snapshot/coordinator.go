package snapshot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Exporter is the save side of a service whose state lives in cells.
// kernel.Stateful implements it. Export does no I/O: the Coordinator calls
// it under the gate's exclusive side.
type Exporter interface {
	Name() ref.ServiceID
	SchemaVersion() int
	// Export returns the service's sections with paths relative to its
	// directory; the Coordinator adds "services/<name>/".
	Export() ([]store.Section, error)
}

// LateExporter is the save side of a service whose data is not cell state,
// such as DynamoDB items exported through the proxy. The Coordinator calls
// it after the cut, outside the gate. Late data holds no blobs: the
// sections ExportLate returns pin none, or the save is refused, since no
// cut pins them and no load retains them.
type LateExporter interface {
	Name() ref.ServiceID
	SchemaVersion() int
	ExportLate(ctx context.Context) ([]store.Section, error)
}

// Stateful is a service the Coordinator saves and loads with the cut.
type Stateful interface {
	Exporter
	// ResetAll drops every partition's state. Load calls it under the gate
	// before Import, after an Export that tells it which blob references the
	// dropped state held; the Coordinator releases those.
	ResetAll()
	// Import loads sections with paths relative to the service's directory,
	// under the gate. It refuses a section of another schema version.
	Import(sections []store.Section) error
	// Rearm rebuilds the service's timers and pollers from its state once
	// the gate is released.
	Rearm(now clock.Instant)
}

// Late is a Stateful's counterpart for data that is not cell state. A save
// with WithLateData exports it after the cut and lists the service in
// Manifest.LateServices; a load runs ImportLate before the cut for each
// listed service, even with no sections, and ImportLate replaces all of the
// service's late data. A service the archive does not list keeps its data.
// The sections ImportLate receives pin no blobs.
type Late interface {
	LateExporter
	ImportLate(ctx context.Context, sections []store.Section) error
}

// Runtime is the state around the cells that a snapshot records: the clock,
// the id streams, the journal counter and the fault rules.
type Runtime interface {
	// Head reads that state. Cut calls it under the gate, so it must do no
	// I/O and must not change the clock. The returned Positions map and
	// Faults bytes belong to the caller, which writes them out after the
	// gate is released: Head returns copies (ids.Source.Positions returns
	// a fresh map) and never changes them afterwards.
	Head() (Head, error)
	// RestoreClock sets the clock from a loaded archive. Load calls it
	// outside the gate, before the cells are imported: the clock's step
	// lock, which timer handlers hold while they wait on the gate, comes
	// before the gate in the lock order.
	RestoreClock(c Clock) error
	// RestoreUnderGate sets the id streams, the journal counter and the
	// fault rules from a loaded archive; h.Clock is RestoreClock's and is
	// not read. Load calls it under the gate's exclusive side, just before
	// the cells are reset, so no call that can write a cell draws an id or
	// a fault decision from the restored state while the old cells stand.
	// It must do no I/O and take only leaf locks.
	RestoreUnderGate(h Head) error
	// Now is emulator time, which Rearm receives after a load.
	Now() clock.Instant
}

// Effects counts the effects of committed writes that have not run yet: a
// write commits its cell under the gate's shared side and hands its
// deliveries to the outbox after releasing it. A cut that landed in between
// would hold the write's state change without its delivery. *kernel.Kernel
// implements it.
type Effects interface {
	// PendingEffects returns the count and a channel closed when it may
	// have changed. The count rises only while a write holds the gate's
	// shared side, so under the exclusive side it can only fall. It takes
	// only leaf locks.
	PendingEffects() (n int, changed <-chan struct{})
}

// EffectsWait is how long, in wall time, a cut waits for pending effects to
// run before it cuts anyway and reports them (ADR 0009).
const EffectsWait = 5 * time.Second

// Head is what Runtime records and restores.
type Head struct {
	Clock       Clock
	Seed        *uint64
	Positions   map[ids.Kind]uint64
	JournalNext ref.EventID
	// Faults is the fault store's export.
	Faults []byte
}

// BlobStore is the blob store a Coordinator pins, reads and loads blobs
// through. *blob.Store implements it.
type BlobStore interface {
	BlobSource
	Pin(refs []blob.Ref) (unpin func() error, err error)
	Import(ctx context.Context, src fs.FS) error
	Retain(ref blob.Ref) error
	Release(ref blob.Ref) error
	Prune() error
}

// Config configures a Coordinator. Every field but Late and ModelPin is
// required.
type Config struct {
	Gate     *store.Gate
	Blobs    BlobStore
	Runtime  Runtime
	Effects  Effects
	Services []Stateful
	Late     []Late
	// Writer names this build in the manifests it writes.
	Writer string
	// ModelPin is the vendored Smithy model commit.
	ModelPin string
}

// Coordinator runs the two-phase save and the mirrored load of ARCHITECTURE.md
// 13 against one store.Gate. A save is Cut then WriteAfterCut; a load is Load.
// Saves and loads are serialized: a Cut holds the Coordinator until it is
// released, so no load lands between a cut and its late exports. A Cut or
// Load waiting its turn gives up when its context ends.
type Coordinator struct {
	cfg     Config
	schemas map[ref.ServiceID]int
	// turn holds a token while a Cut is outstanding or a Load runs.
	turn chan struct{}
}

// NewCoordinator checks cfg and returns a Coordinator. It refuses a missing
// gate, blob store, runtime, effects counter or writer, a service name that
// is not one clean path element, two services of one kind with the same
// name, and a service that is both Stateful and Late with two schema
// versions.
func NewCoordinator(cfg Config) (*Coordinator, error) {
	if cfg.Gate == nil || cfg.Blobs == nil || cfg.Runtime == nil || cfg.Effects == nil || cfg.Writer == "" {
		return nil, &ConfigError{Reason: "a Coordinator needs a Gate, a BlobStore, a Runtime, an Effects counter and a Writer"}
	}
	schemas := map[ref.ServiceID]int{}
	add := func(kind string, name ref.ServiceID, version int, seen map[ref.ServiceID]bool) error {
		if !validServiceName(name) {
			return &ConfigError{Reason: fmt.Sprintf("%s service name %q is not one clean path element", kind, name)}
		}
		if seen[name] {
			return &ConfigError{Reason: fmt.Sprintf("two %s services are named %q", kind, name)}
		}
		seen[name] = true
		if v, ok := schemas[name]; ok && v != version {
			return &ConfigError{Reason: fmt.Sprintf("service %q reports schema version %d to Stateful and %d to Late", name, v, version)}
		}
		schemas[name] = version
		return nil
	}
	seen := map[ref.ServiceID]bool{}
	for _, s := range cfg.Services {
		if err := add("stateful", s.Name(), s.SchemaVersion(), seen); err != nil {
			return nil, err
		}
	}
	seen = map[ref.ServiceID]bool{}
	for _, l := range cfg.Late {
		if err := add("late", l.Name(), l.SchemaVersion(), seen); err != nil {
			return nil, err
		}
	}
	return &Coordinator{cfg: cfg, schemas: schemas, turn: make(chan struct{}, 1)}, nil
}

// Schemas returns the running schema version of every service, for
// ReadOptions.Schemas.
func (c *Coordinator) Schemas() map[ref.ServiceID]int {
	return maps.Clone(c.schemas)
}

// Cut is the first phase of a save: the cell sections and runtime state of
// one consistent instant, with their blobs pinned.
type Cut struct {
	owner   *Coordinator
	archive Archive
	// pendingEffects is the Effects count when the cut was taken.
	pendingEffects int
	// claimed is set by the first Release or WriteAfterCut, which alone runs
	// free.
	claimed atomic.Bool
	// free unpins the blobs and unlocks owner.
	free func() error
}

// Archive returns the cut's archive, without the late sections and journal
// that WriteAfterCut adds.
func (c *Cut) Archive() Archive { return c.archive }

// PendingEffects is how many effects of committed writes had still not run
// when the cut was taken, after Cut waited EffectsWait for them. Zero means
// the archive holds every committed write's deliveries; otherwise it holds
// those writes' state changes without their deliveries, and a save reports
// the count so the user knows what the archive misses.
func (c *Cut) PendingEffects() int { return c.pendingEffects }

// Release unpins the cut's blobs and frees the Coordinator for the next save
// or load. WriteAfterCut releases the cut itself; a caller that abandons a
// cut calls Release instead. Once the cut is released or handed to
// WriteAfterCut, Release does nothing and returns nil.
func (c *Cut) Release() error {
	if !c.claimed.CompareAndSwap(false, true) {
		return nil
	}
	return c.free()
}

// Cut takes the gate's exclusive side, reads the runtime head, exports every
// Stateful service and pins the blobs the sections name, then releases the
// gate. Only Head, Export and Pin run under the gate; checking the sections
// and hashing them wait until it is released, so writers blocked by the cut
// proceed as soon as the exports and the pin return. Before cutting it waits
// up to EffectsWait of wall time, with the gate released, for the effects of
// committed writes to run, so the archive holds a write's deliveries along
// with its state change; Cut.PendingEffects reports what it cut without. The
// cut keeps the Coordinator until WriteAfterCut or Release. Its archive's
// Manifest.Blobs is unset: WriteOptions.Blobs places the blobs. While another
// cut is outstanding or a load runs, or while it waits for effects, Cut
// returns ctx's error with nothing saved if ctx ends first.
func (c *Coordinator) Cut(ctx context.Context) (*Cut, error) {
	if err := c.take(ctx); err != nil {
		return nil, fmt.Errorf("snapshot cut gave up waiting for the save or load in progress; nothing was saved: %w", err)
	}
	a, pending, unpin, err := c.cut(ctx)
	if err != nil {
		c.give()
		return nil, err
	}
	return &Cut{owner: c, archive: a, pendingEffects: pending, free: func() error {
		defer c.give()
		return unpin()
	}}, nil
}

// LateData says whether a save captures the data of the Late services.
type LateData uint8

// LateData values.
const (
	// WithLateData runs every LateExporter and lists every Late service in
	// Manifest.LateServices, so a load replaces their data: an explicit
	// `snapshot save`.
	WithLateData LateData = iota + 1
	// WithoutLateData runs no LateExporter and lists no Late service, so a
	// load leaves late data alone: the --persist checkpoint, where DynamoDB
	// Local owns the item data (ADR 0009). The checkpoint also writes with
	// ExternalBlobs, since <dir>/blobs keeps the blobs.
	WithoutLateData
)

// WriteAfterCut is the second phase of a save: outside the gate it runs
// every LateExporter when late is WithLateData, then writes the archive with
// journal (nil for none) to w, then releases cut. The blobs stay pinned
// until the archive is written. It refuses with *CutError, before running
// anything, a cut another Coordinator took or one already released or
// written; it then leaves the cut alone. A late section that pins blobs
// fails the save before anything is written.
func (c *Coordinator) WriteAfterCut(ctx context.Context, w io.Writer, cut *Cut, late LateData, journal []byte, opts WriteOptions) (err error) {
	if cut == nil || cut.owner != c {
		return &CutError{Defect: CutForeign}
	}
	if !cut.claimed.CompareAndSwap(false, true) {
		return &CutError{Defect: CutReleased}
	}
	defer func() { err = errors.Join(err, cut.free()) }()
	a := cut.archive
	a.Journal = journal
	switch late {
	case WithLateData:
		for _, l := range c.cfg.Late {
			ss, err := l.ExportLate(ctx)
			if err != nil {
				return fmt.Errorf("snapshot save failed exporting late data of service %q; nothing was written: %w", l.Name(), err)
			}
			for _, s := range ss {
				if len(s.Blobs) > 0 {
					return fmt.Errorf("snapshot save failed: late section %q of service %q pins %d blobs, but late data holds no blobs; nothing was written (the service's ExportLate is at fault)", s.Path, l.Name(), len(s.Blobs))
				}
			}
			a.Late = append(a.Late, prefixed(l.Name(), ss)...)
			a.Manifest.LateServices = append(a.Manifest.LateServices, l.Name())
		}
		slices.Sort(a.Manifest.LateServices)
	case WithoutLateData:
	default:
		return &ConfigError{Reason: fmt.Sprintf("LateData %d is neither WithLateData nor WithoutLateData; nothing was written", late)}
	}
	return Write(w, a, opts)
}

// Load replaces the emulator state with a's, mirroring the save. Before
// changing anything it checks the manifest, every section against the
// running schema versions and every pinned blob: in a.Blobs when the
// archive embeds them, in the Coordinator's BlobStore when they are
// external (the --persist startup, where NewDisk indexed <dir>/blobs with no
// references; a.Blobs is then ignored). It imports embedded blobs and pins
// every pinned blob, so a concurrent delete cannot remove one the load
// needs; runs ImportLate for each service in Manifest.LateServices, leaving
// the other Late services' data alone; and restores the clock. Under the
// gate it exports every Stateful service to learn the blob references the
// state it replaces holds, restores the id streams, the journal counter and
// the fault rules, resets every service, imports its sections and retains
// each blob reference. With the gate released it rearms every service, then
// releases the replaced state's references and the pin and prunes
// unreferenced blobs. The caller drains Lambda environments, stops pollers
// and clears the scheduler first, and lifts the drain's hold through
// LoadWith's LoadOptions.BeforeRearm.
//
// A failure after the blob import still releases what the load holds and
// prunes. A failure after the first Late import is a *LoadError: the state
// is partially loaded. A *BlobReleaseError means the load succeeded and
// rearmed every service, but freeing the replaced state's blobs failed.
// While a cut is outstanding or another load runs, Load
// waits, and returns ctx's error with the state unchanged if ctx ends first.
//
// Load is LoadWith with no options: for a load nothing was drained for, such
// as the --persist startup.
func (c *Coordinator) Load(ctx context.Context, a *Archive) error {
	return c.LoadWith(ctx, a, LoadOptions{})
}

// LoadOptions are the hooks of a load.
type LoadOptions struct {
	// BeforeRearm, when set, runs once every service has imported the
	// archive and the gate is released, before the first Rearm; it does not
	// run when the load fails before that. A caller that drained work off
	// the replaced state lifts the drain's hold here: a service's Rearm can
	// fire work at once (a due asynchronous invocation, a wake of the queues
	// an event source mapping polls), and that work must run against the
	// loaded state, not be refused by a hold that outlives the load.
	BeforeRearm func()
}

// LoadWith is Load with opts.
func (c *Coordinator) LoadWith(ctx context.Context, a *Archive, opts LoadOptions) error {
	if err := c.take(ctx); err != nil {
		return fmt.Errorf("snapshot load gave up waiting for the save or load in progress; the state is unchanged: %w", err)
	}
	defer c.give()
	p, err := c.plan(a)
	if err != nil {
		return err
	}
	unpin, err := c.holdBlobs(ctx, p)
	if err != nil {
		return errors.Join(fmt.Errorf("snapshot load failed making the archive's blobs available in the blob store; the state is unchanged: %w", err), c.settleBlobs(nil, nil))
	}
	replaced, le := c.replace(ctx, a.Manifest, a.Faults, p)
	if le == nil {
		if opts.BeforeRearm != nil {
			opts.BeforeRearm()
		}
		now := c.cfg.Runtime.Now()
		for _, s := range c.cfg.Services {
			s.Rearm(now)
		}
	}
	settled := c.settleBlobs(unpin, replaced)
	if le != nil {
		le.Err = errors.Join(le.Err, settled)
		return le
	}
	if settled != nil {
		return &BlobReleaseError{Err: settled}
	}
	return nil
}

// take waits for the Coordinator's turn or for ctx to end. The caller
// returns the turn with give.
func (c *Coordinator) take(ctx context.Context) error {
	select {
	case c.turn <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Coordinator) give() { <-c.turn }

// holdBlobs imports embedded blobs with no references, then pins every
// pinned blob until the load has retained them. Without the pin, a
// concurrent Release of a blob the current state also holds (an S3
// DeleteObject) could delete it before the retain pass, and a --persist
// startup's Prune would delete what it has not retained yet. A blob deleted
// in the instant between the check or import and the pin fails the load
// before anything changed.
func (c *Coordinator) holdBlobs(ctx context.Context, p loadPlan) (unpin func() error, err error) {
	if len(p.refs) == 0 {
		return func() error { return nil }, nil
	}
	if p.placement == EmbedBlobs {
		if err := c.cfg.Blobs.Import(ctx, blobFS{src: p.blobs, refs: p.refs}); err != nil {
			return nil, err
		}
	}
	unpin, err = c.cfg.Blobs.Pin(p.refs)
	if err != nil {
		return nil, fmt.Errorf("a concurrent delete removed a blob the archive needs before the load pinned it (load again): %w", err)
	}
	return unpin, nil
}

// settleBlobs drops the load's pin and the references the replaced state
// held, then prunes every blob nothing references: those only the replaced
// state used, and those a failed load imported. Either argument may be nil.
func (c *Coordinator) settleBlobs(unpin func() error, replaced []string) error {
	var errs []error
	if unpin != nil {
		if err := unpin(); err != nil {
			errs = append(errs, fmt.Errorf("unpinning the archive's blobs: %w", err))
		}
	}
	for _, h := range replaced {
		r, err := blob.ParseRef(h)
		if err == nil {
			err = c.cfg.Blobs.Release(r)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("releasing blob %s of the replaced state: %w", h, err))
		}
	}
	if err := c.cfg.Blobs.Prune(); err != nil {
		errs = append(errs, fmt.Errorf("pruning unreferenced blobs: %w", err))
	}
	return errors.Join(errs...)
}

// replace runs the late imports, restores the clock and swaps the rest of
// the runtime head and the cells. It returns the blob references the
// replaced cells held once they are reset, also when a later step fails.
func (c *Coordinator) replace(ctx context.Context, m Manifest, faults []byte, p loadPlan) (replaced []string, le *LoadError) {
	var loaded []ref.ServiceID
	for _, l := range c.cfg.Late {
		ss, captured := p.late[l.Name()]
		if !captured {
			continue
		}
		if err := l.ImportLate(ctx, ss); err != nil {
			return nil, &LoadError{Step: fmt.Sprintf("importing late data of service %q", l.Name()), Loaded: loaded, Err: err}
		}
		loaded = append(loaded, l.Name())
	}
	if err := c.cfg.Runtime.RestoreClock(m.Clock); err != nil {
		return nil, &LoadError{Step: "restoring the clock", Loaded: loaded, Err: err}
	}
	head := Head{Clock: m.Clock, Seed: m.Seed, Positions: m.Positions, JournalNext: m.JournalNext, Faults: faults}
	return c.importCut(head, p.cut, loaded)
}

func (c *Coordinator) cut(ctx context.Context) (a Archive, pending int, unpin func() error, err error) {
	head, sections, pending, unpin, err := c.exportUnderGate(ctx)
	if err != nil {
		return Archive{}, 0, nil, err
	}
	if err := distinctPaths(sections, func(s store.Section) string { return s.Path }); err != nil {
		return Archive{}, 0, nil, errors.Join(err, unpin())
	}
	return Archive{
		Manifest: Manifest{
			Format:      Format,
			Writer:      c.cfg.Writer,
			ModelPin:    c.cfg.ModelPin,
			Clock:       head.Clock,
			Seed:        head.Seed,
			Positions:   head.Positions,
			JournalNext: head.JournalNext,
		},
		Sections: sections,
		Faults:   head.Faults,
		Blobs:    c.cfg.Blobs,
	}, pending, unpin, nil
}

// exportUnderGate reads the runtime head, exports every Stateful service and
// pins the blobs the sections name, under the gate's exclusive side once
// settle has taken it. It does nothing else there: parsing the references is
// all the pin needs. pending is the Effects count at the cut.
func (c *Coordinator) exportUnderGate(ctx context.Context) (head Head, sections []store.Section, pending int, unpin func() error, err error) {
	release, pending, err := c.settle(ctx)
	if err != nil {
		return Head{}, nil, 0, nil, err
	}
	defer release()
	head, err = c.cfg.Runtime.Head()
	if err != nil {
		return Head{}, nil, 0, nil, fmt.Errorf("snapshot cut failed reading the clock, id streams, journal and faults; nothing was saved: %w", err)
	}
	for _, s := range c.cfg.Services {
		ss, err := s.Export()
		if err != nil {
			return Head{}, nil, 0, nil, fmt.Errorf("snapshot cut failed exporting service %q; nothing was saved: %w", s.Name(), err)
		}
		sections = append(sections, prefixed(s.Name(), ss)...)
	}
	refs, err := pinned(sections, func(s store.Section) []string { return s.Blobs })
	if err != nil {
		return Head{}, nil, 0, nil, fmt.Errorf("snapshot cut failed; nothing was saved: %w", err)
	}
	unpin, err = c.cfg.Blobs.Pin(refs)
	if err != nil {
		return Head{}, nil, 0, nil, fmt.Errorf("snapshot cut failed pinning the blobs its sections name; nothing was saved: %w", err)
	}
	return head, sections, pending, unpin, nil
}

// settle takes the gate's exclusive side once no effect of a committed write
// is pending, or once EffectsWait of wall time has passed, and returns its
// release with the count still pending. Under the exclusive side the count
// can only fall, so a zero read there holds for the whole cut. The effects
// themselves take the gate's shared side (an outbox enqueue is a cell
// write), so settle waits with the gate released. If ctx ends first it
// returns ctx's error with the gate released.
func (c *Coordinator) settle(ctx context.Context) (release func(), pending int, err error) {
	wait, cancel := context.WithTimeout(ctx, EffectsWait)
	defer cancel()
	for {
		release := c.cfg.Gate.Exclusive()
		n, changed := c.cfg.Effects.PendingEffects()
		switch {
		case n == 0:
			return release, 0, nil
		case ctx.Err() != nil:
			release()
			return nil, 0, fmt.Errorf("snapshot cut gave up waiting for %d pending effects of committed writes, such as deliveries, to run; nothing was saved: %w", n, ctx.Err())
		case wait.Err() != nil:
			return release, n, nil
		}
		release()
		select {
		case <-changed:
		case <-wait.Done():
		}
	}
}

// importCut, under the gate, exports every Stateful service to collect the
// blob references its current state holds, restores head through
// RestoreUnderGate, resets every service, imports its sections and retains each blob
// reference. Once the services are reset it returns the collected
// references, also with an error, for the caller to release.
func (c *Coordinator) importCut(head Head, cut map[ref.ServiceID][]store.Section, loaded []ref.ServiceID) (replaced []string, le *LoadError) {
	release := c.cfg.Gate.Exclusive()
	defer release()
	for _, s := range c.cfg.Services {
		ss, err := s.Export()
		if err != nil {
			return nil, &LoadError{Step: fmt.Sprintf("exporting service %q to find the blobs its replaced state holds", s.Name()), Loaded: loaded, Err: err}
		}
		for _, sec := range ss {
			replaced = append(replaced, sec.Blobs...)
		}
	}
	// The head moves with the cells: a call in flight before the gate was
	// taken drew its ids and fault decisions from the old head.
	if err := c.cfg.Runtime.RestoreUnderGate(head); err != nil {
		return nil, &LoadError{Step: "restoring the id streams, journal counter and fault rules", Loaded: loaded, Err: err}
	}
	for _, s := range c.cfg.Services {
		s.ResetAll()
	}
	for _, s := range c.cfg.Services {
		ss := cut[s.Name()]
		if err := s.Import(ss); err != nil {
			return replaced, &LoadError{Step: fmt.Sprintf("importing service %q", s.Name()), Loaded: loaded, Err: err}
		}
		if path, err := c.retain(ss); err != nil {
			return replaced, &LoadError{Step: fmt.Sprintf("retaining the blobs of section %q of service %q", path, s.Name()), Loaded: loaded, Err: err}
		}
		loaded = append(loaded, s.Name())
	}
	return replaced, nil
}

// retain adds one reference per blob entry of each section. On failure it
// names the section.
func (c *Coordinator) retain(ss []store.Section) (path string, err error) {
	for _, sec := range ss {
		for _, h := range sec.Blobs {
			r, err := blob.ParseRef(h)
			if err == nil {
				err = c.cfg.Blobs.Retain(r)
			}
			if err != nil {
				return sec.Path, err
			}
		}
	}
	return "", nil
}

// loadPlan is an archive checked for loading, its sections grouped by
// service with paths relative to the service.
type loadPlan struct {
	cut map[ref.ServiceID][]store.Section
	// late holds a key, possibly with no sections, for every service in
	// Manifest.LateServices.
	late map[ref.ServiceID][]store.Section
	// refs are the distinct pinned blobs.
	refs []blob.Ref
	// placement is the archive's; blobs serves refs, from the archive when
	// they are embedded and from the Coordinator's store when external.
	placement BlobPlacement
	blobs     BlobSource
}

// plan validates a for loading.
func (c *Coordinator) plan(a *Archive) (loadPlan, error) {
	metas, err := describe(a.Sections, a.Late)
	if err != nil {
		return loadPlan{}, err
	}
	m := a.Manifest
	m.Sections = metas
	if _, err := encodeManifest(m); err != nil {
		return loadPlan{}, err
	}
	if err := checkSchemas(m, c.schemas); err != nil {
		return loadPlan{}, err
	}
	stateful := map[ref.ServiceID]bool{}
	for _, s := range c.cfg.Services {
		stateful[s.Name()] = true
	}
	lateful := map[ref.ServiceID]bool{}
	for _, l := range c.cfg.Late {
		lateful[l.Name()] = true
	}
	for _, svc := range m.LateServices {
		if !lateful[svc] {
			return loadPlan{}, &ManifestError{Reason: fmt.Sprintf("the archive holds the late data of service %q, which has no Late side in this understudy", svc)}
		}
	}
	var p loadPlan
	if p.cut, err = group(a.Sections, stateful, "Stateful"); err != nil {
		return loadPlan{}, err
	}
	late, err := group(a.Late, lateful, "Late")
	if err != nil {
		return loadPlan{}, err
	}
	p.late = make(map[ref.ServiceID][]store.Section, len(m.LateServices))
	for _, svc := range m.LateServices {
		p.late[svc] = late[svc]
	}
	if p.refs, err = pinned(metas, metaBlobs); err != nil {
		return loadPlan{}, err
	}
	p.placement, p.blobs = m.Blobs, a.Blobs
	if m.Blobs == ExternalBlobs {
		p.blobs = c.cfg.Blobs
	}
	for _, r := range p.refs {
		if p.blobs == nil {
			return loadPlan{}, &EntryError{Name: blobsDir + r.String(), Defect: UnreadableBlob, Err: errors.New("the archive has no blob source")}
		}
		if _, err := p.blobs.Size(r); err != nil {
			return loadPlan{}, &EntryError{Name: blobsDir + r.String(), Defect: UnreadableBlob, Err: err}
		}
	}
	return p, nil
}

// group splits sections by service and strips "services/<svc>/". Every
// section must belong to a service in known.
func group(sections []store.Section, known map[ref.ServiceID]bool, kind string) (map[ref.ServiceID][]store.Section, error) {
	out := map[ref.ServiceID][]store.Section{}
	for _, s := range sections {
		svc, err := sectionService(s.Path)
		if err != nil {
			return nil, err
		}
		if !known[svc] {
			return nil, &ManifestError{Reason: fmt.Sprintf("section %q belongs to service %q, which has no %s side in this understudy", s.Path, svc, kind)}
		}
		s.Path = strings.TrimPrefix(s.Path, servicesDir+string(svc)+"/")
		out[svc] = append(out[svc], s)
	}
	return out, nil
}

// prefixed returns ss with paths under "services/<name>/".
func prefixed(name ref.ServiceID, ss []store.Section) []store.Section {
	out := make([]store.Section, len(ss))
	for i, s := range ss {
		s.Path = servicesDir + string(name) + "/" + s.Path
		out[i] = s
	}
	return out
}
