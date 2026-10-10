package kerneltest

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// K is a kernel running in a synctest bubble, with the parts a test drives
// and inspects. It embeds the bubble's *testing.T, so a test body reports
// through k.Fatal and reads k.Context().
type K struct {
	*testing.T

	Kernel   *kernel.Kernel
	Clock    *clock.Clock
	Journal  *journal.Journal
	Faults   *fault.Store
	Access   *access.Recorder
	Identity *identity.Store
	// Scope is where Invoke runs client calls: 000000000000 in us-east-1
	// unless the test changes it.
	Scope aws.Scope
	// Endpoint is the host client calls address, which emulator-minted URLs
	// echo.
	Endpoint kernel.Endpoint
}

type config struct {
	services []*registry.Service
	mode     clock.Mode
	seed     uint64
}

// Option adjusts Run.
type Option func(*config)

// Services puts the given service descriptors in the kernel's registry.
func Services(svcs ...*registry.Service) Option {
	return func(c *config) { c.services = append(c.services, svcs...) }
}

// Mode starts the clock in m instead of frozen at the bubble's start.
func Mode(m clock.Mode) Option {
	return func(c *config) { c.mode = m }
}

// Seed seeds the identifier and fault streams; the default is 1.
func Seed(n uint64) Option {
	return func(c *config) { c.seed = n }
}

// Run runs fn inside a synctest bubble against a running kernel: a frozen
// clock by default, a seeded identifier source, a journal, a fault store, an
// access recorder and strict output checks. When fn returns, Run stops the
// kernel, waits for its goroutines and fails the test on any bind mistake.
// fn receives the bubble's test through K.
func Run(t *testing.T, fn func(k *K), opts ...Option) {
	t.Helper()
	cfg := config{mode: clock.Frozen{}, seed: 1}
	for _, o := range opts {
		o(&cfg)
	}
	synctest.Test(t, func(t *testing.T) {
		k := build(t, cfg)
		ctx, cancel := context.WithCancel(context.Background())
		stopped := make(chan error, 1)
		go func() { stopped <- k.Kernel.Run(ctx) }()
		// Deferred so a test that fails with t.Fatal still stops the kernel
		// before the bubble checks for leftover goroutines.
		defer func() {
			cancel()
			if err := <-stopped; err != nil {
				t.Errorf("kernel.Run = %v", err)
			}
			if err := k.Kernel.BindErr(); err != nil {
				t.Errorf("bind mistakes: %v", err)
			}
		}()
		fn(k)
	})
}

func build(t *testing.T, cfg config) *K {
	t.Helper()
	timers := kernel.NewTimers()
	clk, err := clock.New(clock.Options{Mode: cfg.mode, Timers: timers.Scheduler()})
	if err != nil {
		t.Fatal(err)
	}
	j, err := journal.New(journal.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(cfg.services...)
	if err != nil {
		t.Fatal(err)
	}
	src := ids.NewSeeded(cfg.seed)
	faults := fault.New(&cfg.seed, src, kernel.NewCatalogue(reg, nil))
	rec := &access.Recorder{}
	idStore := identity.New(src, aws.DefaultAccount())
	k, err := kernel.New(kernel.Options{
		Registry: reg, Clock: clk, Timers: timers, IDs: src, Journal: j,
		Faults: faults, Access: rec, Identity: idStore, Strict: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &K{
		T:      t,
		Kernel: k, Clock: clk, Journal: j, Faults: faults, Access: rec, Identity: idStore,
		Scope:    aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()},
		Endpoint: kernel.Endpoint{Scheme: "http", Host: "localhost:4566"},
	}
}

// Deps returns the dependencies a service constructor takes, with the
// binder of svc.
func (k *K) Deps(svc *registry.Service) kernel.ServiceDeps {
	return kernel.ServiceDeps{
		Kernel:   k.Kernel,
		Binder:   k.Kernel.Binder(svc),
		Clock:    k.Clock,
		IDs:      k.Kernel.IDs(),
		Identity: k.Identity,
		Log:      k.Kernel.Log(),
	}
}

// Stub binds h as op's handler, for a service the test does not construct.
func (k *K) Stub[In, Out any](op kernel.Op[In, Out], h kernel.Handler[In, Out]) {
	kernel.Bind(k.Kernel.Binder(op.Descriptor().Service), op, h, registry.Full{})
	if err := k.Kernel.BindErr(); err != nil {
		k.Fatalf("Stub(%s): %v", op.Descriptor().ID, err)
	}
}

// Call returns the call of a client request for op, as the edge builds it:
// External origin over awsJson1_0, the root of Scope's account, admitted now.
func (k *K) Call(op *registry.Operation) *kernel.Call {
	return k.Kernel.NewCall(kernel.ExternalCall{
		RequestID: k.Kernel.IDs().RequestID(),
		Operation: op.ID,
		Scope:     k.Scope,
		Principal: identity.Root{Account: k.Scope.Account},
		Origin:    ref.External{Protocol: ref.AWSJSON10, Listener: ref.ListenerMain},
		Admitted:  k.Clock.Now(),
		Endpoint:  k.Endpoint,
	})
}

// Invoke runs op with in as a client request in Scope through the full
// pipeline and returns its output or error.
func (k *K) Invoke[In, Out any](ctx context.Context, op kernel.Op[In, Out], in *In) (*Out, *awserr.Error) {
	return Dispatch[In, Out](ctx, k, k.Call(op.Descriptor()), in)
}

// OutputTypeError is a pipeline result that is not the operation's output.
type OutputTypeError struct {
	Got, Want string
}

func (e *OutputTypeError) Error() string {
	return "kerneltest: the pipeline returned " + e.Got + ", want " + e.Want
}

// Dispatch runs c through the pipeline with in and returns the typed
// output or the error.
func Dispatch[In, Out any](ctx context.Context, k *K, c *kernel.Call, in *In) (*Out, *awserr.Error) {
	want := fmt.Sprintf("%T", (*Out)(nil))
	switch r := k.Kernel.Dispatch(ctx, c, kernel.Ok{In: in}).(type) {
	case kernel.Output:
		out, ok := r.Out.(*Out)
		if !ok {
			return nil, awserr.Internalf(&OutputTypeError{Got: fmt.Sprintf("%T", r.Out), Want: want})
		}
		return out, nil
	case kernel.Failure:
		return nil, r.Err
	case kernel.StatusOKFailure:
		return nil, r.Err
	}
	return nil, awserr.Internalf(&OutputTypeError{Got: "no result", Want: want})
}
