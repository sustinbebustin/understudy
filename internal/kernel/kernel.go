package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"sync/atomic"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/store"
)

// FaultStage evaluates fault-injection rules at the pipeline's hooks;
// *fault.Store implements it.
type FaultStage interface {
	Evaluate(h fault.Hook, in fault.MatchInput) fault.Decision
}

// AccessRecorder records the IAM actions a call used; *access.Recorder
// implements it.
type AccessRecorder interface {
	Record(e access.Entry) error
}

// DeliverSink accepts the cross-service deliveries Tx.Deliver produces. The
// outbox implements it: Deliver inserts the delivery into its own state
// before the source call returns, so an accepted delivery is state.
type DeliverSink interface {
	Deliver(d Delivery) error
}

// Linker links an external call made by function code to the invocation
// that runs it, from the principal its minted credentials resolve to and,
// on Linux bridge, the remote address (ARCHITECTURE.md 10). The Lambda
// service implements it.
type Linker interface {
	Link(p identity.Principal, remote netip.AddrPort) (ref.Cause, bool)
}

// Options configures New. Registry, Clock, Timers, IDs and Journal are
// required; the interfaces are optional and a nil one disables its stage.
type Options struct {
	Registry *registry.Registry
	// Clock is the emulator clock. It must have been built with
	// Timers.Scheduler() as its scheduler, so due timers reach the kernel.
	Clock  *clock.Clock
	Timers *Timers
	IDs    *ids.Source
	// Journal records every call; its clock is Clock.
	Journal  *journal.Journal
	Faults   FaultStage
	Access   AccessRecorder
	Identity *identity.Store
	// Gate is shared by every cell of every service; nil means a new one.
	Gate *store.Gate
	// Deliver accepts Tx.Deliver effects; nil makes a Deliver an Internal
	// error of the write that produced it.
	Deliver DeliverSink
	Linker  Linker
	// Log receives kernel diagnostics; nil discards them.
	Log *slog.Logger
	// Strict turns a zero-valued required timestamp in an output into an
	// Internal error, so a forgotten field fails a test instead of a client
	// (ARCHITECTURE.md 3.2). Tests and conformance runs set it.
	Strict bool
}

// OptionsError reports Options New cannot use. Nothing was created.
type OptionsError struct {
	Field string
}

func (e *OptionsError) Error() string {
	return fmt.Sprintf("kernel: Options.%s is required; build it before the kernel (internal/server/wire.go shows the order)", e.Field)
}

// Kernel is the one pipeline every call runs through, with the state gate,
// the waiters, the timer table, the work tracker and the per-kernel
// bindings. Several kernels coexist in one process.
type Kernel struct {
	reg      *registry.Registry
	clock    *clock.Clock
	timers   *Timers
	ids      *ids.Source
	journal  *journal.Journal
	faults   FaultStage
	access   AccessRecorder
	identity *identity.Store
	gate     *store.Gate
	waiters  *store.Waiters
	deliver  DeliverSink
	linker   Linker
	log      *slog.Logger
	strict   bool

	bindings *Bindings
	work     *tracker

	wg      sync.WaitGroup
	running atomic.Bool

	mu       sync.Mutex
	stopped  bool
	cancels  map[*context.CancelFunc]struct{}
	bindErrs []error
}

// New returns a kernel with no bindings. Services bind their handlers
// through Binder before Run; BindErr reports what they got wrong.
func New(o Options) (*Kernel, error) {
	switch {
	case o.Registry == nil:
		return nil, &OptionsError{Field: "Registry"}
	case o.Clock == nil:
		return nil, &OptionsError{Field: "Clock"}
	case o.Timers == nil:
		return nil, &OptionsError{Field: "Timers"}
	case o.IDs == nil:
		return nil, &OptionsError{Field: "IDs"}
	case o.Journal == nil:
		return nil, &OptionsError{Field: "Journal"}
	}
	gate := o.Gate
	if gate == nil {
		gate = &store.Gate{}
	}
	log := o.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	k := &Kernel{
		reg:      o.Registry,
		clock:    o.Clock,
		timers:   o.Timers,
		ids:      o.IDs,
		journal:  o.Journal,
		faults:   o.Faults,
		access:   o.Access,
		identity: o.Identity,
		gate:     gate,
		waiters:  &store.Waiters{},
		deliver:  o.Deliver,
		linker:   o.Linker,
		log:      log,
		strict:   o.Strict,
		bindings: newBindings(o.Registry),
		work:     newTracker(),
		cancels:  map[*context.CancelFunc]struct{}{},
	}
	if err := o.Timers.attach(k); err != nil {
		return nil, err
	}
	return k, nil
}

// Registry returns the registry the kernel dispatches over.
func (k *Kernel) Registry() *registry.Registry { return k.reg }

// Clock returns the emulator clock.
func (k *Kernel) Clock() *clock.Clock { return k.clock }

// IDs returns the identifier source.
func (k *Kernel) IDs() *ids.Source { return k.ids }

// Journal returns the journal every call is recorded in.
func (k *Kernel) Journal() *journal.Journal { return k.journal }

// Gate returns the gate every cell shares; snapshot, reset and load take
// its exclusive side.
func (k *Kernel) Gate() *store.Gate { return k.gate }

// Waiters returns the wait registry blocking operations subscribe to.
func (k *Kernel) Waiters() *store.Waiters { return k.waiters }

// Timers returns the timer table services register their kinds with.
func (k *Kernel) Timers() *Timers { return k.timers }

// Identity returns the credential store, or nil when none was configured.
func (k *Kernel) Identity() *identity.Store { return k.identity }

// Log returns the kernel's logger.
func (k *Kernel) Log() *slog.Logger { return k.log }

// Bindings returns the kernel's binding table.
func (k *Kernel) Bindings() *Bindings { return k.bindings }

// Binder returns the binder for svc's operations. Binding through it an
// operation of another service is a bind error.
func (k *Kernel) Binder(svc *registry.Service) *Binder {
	return &Binder{k: k, svc: svc}
}

// BindErr reports every bind mistake so far, joined: an operation bound
// twice, an operation outside its binder's service or the registry, and a
// timer kind registered twice. wire.go checks it once every service is
// constructed, and Run refuses to start while it is non-nil.
func (k *Kernel) BindErr() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	return errors.Join(append(k.bindErrs, k.timers.err())...)
}

// RunningError reports a second Run of the same kernel.
type RunningError struct{}

func (*RunningError) Error() string {
	return "kernel: Run was called on a kernel that is already running or has stopped; build a new kernel for each run"
}

// Run drives the kernel until ctx ends: the clock's scheduler driver, which
// fires timers into the handlers services registered, and every goroutine
// started with Go. When ctx ends it cancels the context of every tracked
// goroutine and returns once all of them have. It refuses to start while
// BindErr reports a mistake.
func (k *Kernel) Run(ctx context.Context) error {
	if err := k.BindErr(); err != nil {
		return err
	}
	if !k.running.CompareAndSwap(false, true) {
		return &RunningError{}
	}
	k.wg.Go(func() { k.clock.Run(ctx) })
	<-ctx.Done()
	k.mu.Lock()
	k.stopped = true
	for cancel := range k.cancels {
		(*cancel)()
	}
	k.mu.Unlock()
	k.wg.Wait()
	return nil
}

// recordBindErr keeps a bind mistake for BindErr.
func (k *Kernel) recordBindErr(err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.bindErrs = append(k.bindErrs, err)
}
