package server

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/api/stsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/outbox"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	serveradmin "github.com/sustinbebustin/understudy/internal/server/admin"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb"
	"github.com/sustinbebustin/understudy/internal/service/events"
	"github.com/sustinbebustin/understudy/internal/service/iam"
	"github.com/sustinbebustin/understudy/internal/service/kms"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/logs"
	"github.com/sustinbebustin/understudy/internal/service/s3"
	"github.com/sustinbebustin/understudy/internal/service/secretsmanager"
	"github.com/sustinbebustin/understudy/internal/service/sns"
	"github.com/sustinbebustin/understudy/internal/service/sqs"
	"github.com/sustinbebustin/understudy/internal/service/sts"
	"github.com/sustinbebustin/understudy/internal/snapshot"
)

// modelPin is the vendored Smithy model commit the generated packages come
// from (third_party/api-models-aws/UPSTREAM.json; a test keeps them equal).
const modelPin = "api-models-aws@1cfdfb4bb9ea858e65623e66d73af04bec353201"

// wiring is the emulator constructed: every part in the order the
// dependencies demand, with nothing registered in init().
type wiring struct {
	kernel   *kernel.Kernel
	journal  *journal.Journal
	blobs    *blob.Store
	dataDir  *os.Root
	admin    *serveradmin.Handler
	edge     *edge.Handler
	switches []fidelity.Entry
}

// wire builds the registry, the kernel with its clock, journal, faults,
// access recorder, identity store and outbox, the services, the snapshot
// coordinator, the admin API and the front door. Each service constructor
// gets kernel.ServiceDeps and its Options; this file is the only place that
// names every service and codec.
func wire(cfg Config, log *slog.Logger) (w *wiring, err error) {
	w = &wiring{}
	defer func() {
		if err != nil {
			err = errors.Join(err, w.close())
		}
	}()
	src := ids.NewRandom()
	if cfg.Seed != nil {
		src = ids.NewSeeded(*cfg.Seed)
	}
	timers := kernel.NewTimers()
	clk, err := clock.New(clockOptions(cfg.ClockStart, timers))
	if err != nil {
		return w, err
	}
	if w.journal, err = journal.New(journal.Options{Clock: clk}); err != nil {
		return w, err
	}
	cat := newCatalogue()
	reg, err := registry.New(cat.descs...)
	if err != nil {
		return w, err
	}
	idStore := identity.New(src, aws.DefaultAccount())
	faults := fault.New(cfg.Seed, src, kernel.NewCatalogue(reg, cat.throttles))
	recorder := &access.Recorder{}
	box := outbox.New()
	k, err := kernel.New(kernel.Options{
		Registry: reg,
		Clock:    clk,
		Timers:   timers,
		IDs:      src,
		Journal:  w.journal,
		Faults:   faults,
		Access:   recorder,
		Identity: idStore,
		Deliver:  box,
		Log:      log,
	})
	if err != nil {
		return w, err
	}
	w.kernel = k
	if err := box.Bind(k, outbox.Options{Faults: faults}); err != nil {
		return w, err
	}
	if w.blobs, err = blob.NewMemory("", 0); err != nil {
		return w, err
	}
	if dir, ok := cfg.Persist.(PersistDir); ok {
		if w.dataDir, err = openDataDir(dir.Path); err != nil {
			return w, err
		}
	}
	deps := func(desc *registry.Service) kernel.ServiceDeps {
		return kernel.ServiceDeps{Kernel: k, Binder: k.Binder(desc), Clock: clk, IDs: src, Blob: w.blobs, Identity: idStore, Log: log}
	}
	sqsSvc := sqs.New(deps(sqsapi.Service()), sqs.Options{})
	snsSvc := sns.New(deps(snsapi.Service()), sns.Options{})
	s3Svc := s3.New(deps(s3api.Service()), s3.Options{})
	dynamoSvc := dynamodb.New(deps(dynamodbapi.Service()), dynamodb.Options{})
	lambdaSvc := lambda.New(deps(lambdaapi.Service()), lambda.Options{})
	logsSvc := logs.New(deps(logsapi.Service()), logs.Options{})
	secretsSvc := secretsmanager.New(deps(secretsmanagerapi.Service()), secretsmanager.Options{})
	kmsSvc := kms.New(deps(kmsapi.Service()), kms.Options{})
	iamSvc := iam.New(deps(iamapi.Service()), iam.Options{})
	stsSvc := sts.New(deps(stsapi.Service()), sts.Options{})
	eventsSvc := events.New(deps(eventbridgeapi.Service()), events.Options{})
	cloudwatchSvc := cloudwatch.New(deps(cloudwatchapi.Service()), cloudwatch.Options{})
	if err := k.BindErr(); err != nil {
		return w, err
	}
	stateful, late := collect(w, box, sqsSvc, snsSvc, s3Svc, dynamoSvc, lambdaSvc, logsSvc, secretsSvc, kmsSvc, iamSvc, stsSvc, eventsSvc, cloudwatchSvc)
	rt := &runtime{clock: clk, ids: src, seed: cfg.Seed, journal: w.journal, faults: faults}
	snapshots, err := coordinator(k.Gate(), w.blobs, rt, stateful, late, "understudy "+cfg.Version)
	if err != nil {
		return w, err
	}
	opts := serveradmin.Options{
		Kernel:     k,
		Faults:     faults,
		Access:     recorder,
		Snapshots:  snapshots,
		Blobs:      w.blobs,
		Services:   stateful,
		Late:       late,
		Regions:    aws.KnownRegions(),
		ClockStart: clockStart(cfg.ClockStart, clk),
		DataDir:    w.dataDir,
		Token:      cfg.AdminToken,
		Version:    cfg.Version,
		ModelPin:   modelPin,
		Uptime:     clk.Stopwatch(),
		Log:        log,
	}
	hook(lambdaSvc, &opts.Drainer)
	hook(lambdaSvc, &opts.HotReloader)
	hook(lambdaSvc, &opts.ImagePuller)
	hook(dynamoSvc, &opts.TTLSweeper)
	hook(snsSvc, &opts.Subscriptions)
	if w.admin, err = serveradmin.New(opts); err != nil {
		return w, err
	}
	w.edge, err = edge.New(edge.Options{
		Kernel:                k,
		Registry:              reg,
		Identity:              idStore,
		Regions:               aws.KnownRegions(),
		Codecs:                Codecs(),
		S3BaseDomains:         []string{"localhost", "s3.localhost"},
		Listener:              ref.ListenerMain,
		Admin:                 w.admin,
		Unclaimed:             ref.AWSJSON10,
		UnknownOperationError: cat.unknownOperationError,
		Log:                   log,
	})
	return w, err
}

// collect gathers the Stateful views, with the outbox first, the Late
// services and the fidelity switches of the services.
func collect(w *wiring, box *outbox.Outbox, services ...serviceView) ([]serveradmin.Service, []snapshot.Late) {
	stateful := make([]serveradmin.Service, 0, 1+len(services))
	stateful = append(stateful, box)
	var late []snapshot.Late
	for _, s := range services {
		stateful = append(stateful, s)
		w.switches = append(w.switches, s.Switches()...)
		if l, ok := s.(snapshot.Late); ok {
			late = append(late, l)
		}
	}
	return stateful, late
}

// api is one service's generated descriptor with the overlay identities the
// descriptor does not carry: its throttleError and, for the services with
// an RPC protocol, its unknownOperationError per protocol family.
type api struct {
	desc     *registry.Service
	throttle awserr.Identity
	unknown  func(awserr.Family) (awserr.Identity, bool)
}

// catalogue indexes the generated services' descriptors and identities.
type catalogue struct {
	descs     []*registry.Service
	throttles map[ref.ServiceID]awserr.Identity
	unknown   map[ref.ServiceID]func(awserr.Family) (awserr.Identity, bool)
}

func newCatalogue() catalogue {
	c := catalogue{throttles: map[ref.ServiceID]awserr.Identity{}, unknown: map[ref.ServiceID]func(awserr.Family) (awserr.Identity, bool){}}
	for _, a := range apis() {
		c.descs = append(c.descs, a.desc)
		c.throttles[a.desc.ID] = a.throttle
		if a.unknown != nil {
			c.unknown[a.desc.ID] = a.unknown
		}
	}
	return c
}

// unknownOperationError is a service's overlay unknownOperationError for a
// protocol family; false means the generic identity.
func (c catalogue) unknownOperationError(svc ref.ServiceID, f awserr.Family) (awserr.Identity, bool) {
	if fn, ok := c.unknown[svc]; ok {
		return fn(f)
	}
	var none awserr.Identity
	return none, false
}

// apis lists every generated service. S3 routes through its restXml route
// table only, so it has no unknown-operation identity.
func apis() []api {
	return []api{
		{sqsapi.Service(), sqsapi.ThrottleError(), sqsapi.UnknownOperationError},
		{snsapi.Service(), snsapi.ThrottleError(), snsapi.UnknownOperationError},
		{s3api.Service(), s3api.ThrottleError(), nil},
		{dynamodbapi.Service(), dynamodbapi.ThrottleError(), dynamodbapi.UnknownOperationError},
		{lambdaapi.Service(), lambdaapi.ThrottleError(), lambdaapi.UnknownOperationError},
		{logsapi.Service(), logsapi.ThrottleError(), logsapi.UnknownOperationError},
		{secretsmanagerapi.Service(), secretsmanagerapi.ThrottleError(), secretsmanagerapi.UnknownOperationError},
		{kmsapi.Service(), kmsapi.ThrottleError(), kmsapi.UnknownOperationError},
		{iamapi.Service(), iamapi.ThrottleError(), iamapi.UnknownOperationError},
		{stsapi.Service(), stsapi.ThrottleError(), stsapi.UnknownOperationError},
		{eventbridgeapi.Service(), eventbridgeapi.ThrottleError(), eventbridgeapi.UnknownOperationError},
		{cloudwatchapi.Service(), cloudwatchapi.ThrottleError(), cloudwatchapi.UnknownOperationError},
	}
}

// serviceView is what wire.go collects from every service: its Stateful
// view with its scopes, and its fidelity switches.
type serviceView interface {
	serveradmin.Service
	Switches() []fidelity.Entry
}

// hook sets *dst to svc when the service implements the admin hook, so a
// service task that adds the method is picked up without editing wire.go.
func hook[H any](svc any, dst *H) {
	if h, ok := svc.(H); ok {
		*dst = h
	}
}

// close releases what wire opened, after a failed wiring or at shutdown.
func (w *wiring) close() error {
	var errs []error
	if w.blobs != nil {
		errs = append(errs, w.blobs.Close())
	}
	if w.dataDir != nil {
		errs = append(errs, w.dataDir.Close())
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("server: releasing the blob store and data directory: %w", err)
	}
	return nil
}

// openDataDir opens the persistence directory, creating it when missing.
func openDataDir(path string) (*os.Root, error) {
	if err := os.MkdirAll(path, 0o750); err != nil {
		return nil, fmt.Errorf("server: creating the data directory %s: %w", path, err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("server: opening the data directory %s: %w", path, err)
	}
	return root, nil
}

// clockOptions maps the start setting onto the clock.
func clockOptions(start ClockStart, timers *kernel.Timers) clock.Options {
	o := clock.Options{Mode: clock.Running{}, Timers: timers.Scheduler()}
	if f, ok := start.(ClockFrozen); ok {
		o.Mode = clock.Frozen{}
		if !f.At.IsZero() {
			o.Start = clock.FromWire(f.At)
		}
	}
	return o
}

// clockStart is how a reset with Clock restarts the clock: a running clock
// follows wall time again; a frozen one returns to the instant serve froze
// it at.
func clockStart(start ClockStart, clk *clock.Clock) func() (clock.Instant, clock.Mode) {
	if _, ok := start.(ClockFrozen); ok {
		at := clk.Now()
		return func() (clock.Instant, clock.Mode) { return at, clock.Frozen{} }
	}
	return func() (clock.Instant, clock.Mode) { return clock.FromWire(clk.Wall()), clock.Running{} }
}
