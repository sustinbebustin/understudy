package server

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/outbox"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	serveradmin "github.com/sustinbebustin/understudy/internal/server/admin"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb/ddblocal"
	"github.com/sustinbebustin/understudy/internal/service/events"
	"github.com/sustinbebustin/understudy/internal/service/iam"
	"github.com/sustinbebustin/understudy/internal/service/kms"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/lambda/codestore"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool"
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

// defaultS3BaseDomains are the hosts virtual-hosted S3 addresses end in
// when the configuration names none.
func defaultS3BaseDomains() []string { return []string{"localhost", "s3.localhost"} }

// wiring is the emulator constructed: every part in the order the
// dependencies demand, with nothing registered in init().
type wiring struct {
	kernel  *kernel.Kernel
	journal *journal.Journal
	faults  *fault.Store
	blobs   *blob.Store
	// blobRoot is the blob directory a disk store is rooted in; nil in
	// memory.
	blobRoot *os.Root
	// data is the locked persistence directory and persist its loader and
	// checkpointer; both nil when ephemeral.
	data    *dataDir
	persist *persister
	host    *containerHost
	local   *lazyLocal
	licence ddblocal.Licence
	// code holds extracted function code; codeTemp is its directory when
	// it is temporary.
	code     *codestore.Store
	codeTemp string
	pool     *pool.Pool
	lambda   *lambda.Execution
	admin    *serveradmin.Handler
	edge     *edge.Handler
	// containerEdge serves the container listener: the AWS API and only
	// health and clock from the admin prefix.
	containerEdge *edge.Handler
	switches      []fidelity.Entry
	stopped       sync.Once
}

// wire builds the registry, the kernel with its clock, journal, faults,
// access recorder, identity store and outbox, the services, the snapshot
// coordinator, the admin API and the front door, then loads the persisted
// state. Each service constructor gets kernel.ServiceDeps and its Options;
// this file is the only place that names every service and codec.
func wire(cfg Config, log *slog.Logger) (w *wiring, err error) {
	w = &wiring{}
	defer func() {
		if err != nil {
			err = errors.Join(err, w.close(context.Background()))
		}
	}()
	regions, err := aws.KnownRegions().WithExtra(cfg.Regions...)
	if err != nil {
		return w, err
	}
	src := ids.NewRandom()
	if cfg.Seed != nil {
		src = ids.NewSeeded(*cfg.Seed)
	}
	timers := kernel.NewTimers()
	clk, err := clock.New(clockOptions(cfg.ClockStart, timers))
	if err != nil {
		return w, err
	}
	if w.journal, err = journal.New(journalOptions(cfg.Journal, clk)); err != nil {
		return w, err
	}
	cat := newCatalogue()
	reg, err := registry.New(cat.descs...)
	if err != nil {
		return w, err
	}
	account := defaultAccount(cfg)
	idStore := identity.New(src, account)
	w.faults = fault.New(cfg.Seed, src, kernel.NewCatalogue(reg, cat.throttles))
	recorder := &access.Recorder{}
	box := outbox.New()
	link := &deferredLinker{}
	k, err := kernel.New(kernel.Options{
		Registry: reg,
		Clock:    clk,
		Timers:   timers,
		IDs:      src,
		Journal:  w.journal,
		Faults:   w.faults,
		Access:   recorder,
		Identity: idStore,
		Deliver:  box,
		Linker:   link,
		Log:      log,
	})
	if err != nil {
		return w, err
	}
	w.kernel = k
	if err := w.storage(cfg); err != nil {
		return w, err
	}
	if err := w.containers(cfg, clk, log); err != nil {
		return w, err
	}
	deps := func(desc *registry.Service) kernel.ServiceDeps {
		return kernel.ServiceDeps{Kernel: k, Binder: k.Binder(desc), Clock: clk, IDs: src, Blob: w.blobs, Identity: idStore, Docker: w.host.client, Log: log}
	}
	var images lambda.ImageSource
	if w.pool != nil {
		images = w.host
	}
	sw := cfg.Switches
	sqsSvc := sqs.New(deps(sqsapi.Service()), sqs.Options{DeletedRecently: sw.SQSDeletedRecently})
	snsSvc := sns.New(deps(snsapi.Service()), sns.Options{})
	s3Svc := s3.New(deps(s3api.Service()), s3.Options{CrossRegion: sw.S3CrossRegion, Regions: regions})
	dynamoSvc := dynamodb.New(deps(dynamodbapi.Service()), dynamodb.Options{Local: w.local})
	lambdaDeps := deps(lambdaapi.Service())
	lambdaSvc := lambda.New(lambdaDeps, lambda.Options{StateTransitions: sw.LambdaTransitions, Images: images})
	logsSvc := logs.New(deps(logsapi.Service()), logs.Options{})
	secretsSvc := secretsmanager.New(deps(secretsmanagerapi.Service()), secretsmanager.Options{})
	kmsSvc := kms.New(deps(kmsapi.Service()), kms.Options{})
	iamSvc := iam.New(deps(iamapi.Service()), iam.Options{})
	stsSvc := sts.New(deps(stsapi.Service()), sts.Options{AssumeRole: sw.STSAssumeRole})
	eventsSvc := events.New(deps(eventbridgeapi.Service()), events.Options{})
	cloudwatchSvc := cloudwatch.New(deps(cloudwatchapi.Service()), cloudwatch.Options{})
	if err := w.execution(cfg, account, regions, lambdaSvc, lambdaDeps, images); err != nil {
		return w, err
	}
	link.set(w.lambda)
	if err := k.BindErr(); err != nil {
		return w, err
	}
	policies := policySources{sqs: sqsSvc, sns: snsSvc, lambda: lambdaSvc}
	if err := box.Bind(k, outbox.Options{Faults: w.faults, Policies: policies}); err != nil {
		return w, err
	}
	stateful, late := collect(w, box, sqsSvc, snsSvc, s3Svc, dynamoSvc, lambdaSvc, w.lambda.AsyncQueue(), logsSvc, secretsSvc, kmsSvc, iamSvc, stsSvc, eventsSvc, cloudwatchSvc)
	rt := &runtime{clock: clk, ids: src, seed: cfg.Seed, journal: w.journal, faults: w.faults}
	snapshots, err := coordinator(k.Gate(), w.blobs, rt, stateful, late, "understudy "+cfg.Version)
	if err != nil {
		return w, err
	}
	opts := serveradmin.Options{
		Kernel:         k,
		Faults:         w.faults,
		Access:         recorder,
		Snapshots:      snapshots,
		Blobs:          w.blobs,
		Services:       stateful,
		Late:           late,
		Regions:        regions,
		ClockStart:     clockStart(cfg.ClockStart, clk),
		DefaultAccount: account,
		Token:          cfg.AdminToken,
		Version:        cfg.Version,
		ModelPin:       modelPin,
		Uptime:         clk.Stopwatch(),
		Components:     components{host: w.host, local: w.local},
		Log:            log,

		Drainer:       w.lambda,
		Pollers:       w.lambda,
		HotReloader:   w.lambda,
		ImagePuller:   w.lambda,
		TTLSweeper:    dynamoSvc,
		Subscriptions: snsSvc,
	}
	if w.data != nil {
		opts.DataDir = w.data.snapshots
	}
	if w.admin, err = serveradmin.New(opts); err != nil {
		return w, err
	}
	if err := w.edges(cfg, k, reg, idStore, regions, cat, s3Svc.BucketExists, log); err != nil {
		return w, err
	}
	if err := w.load(cfg, snapshots, log); err != nil {
		return w, err
	}
	return w, preload(w.faults, cfg.Faults)
}

// policySources routes the outbox's access advisory to the service that
// stores the delivery target's resource policy: the queue's Policy, the
// topic's Policy, the function's permissions.
type policySources struct {
	sqs, sns, lambda outbox.PolicySource
}

func (p policySources) ResourcePolicy(target aws.ARN) (*policydoc.Document, bool) {
	switch target.Service {
	case "sqs":
		return p.sqs.ResourcePolicy(target)
	case "sns":
		return p.sns.ResourcePolicy(target)
	case "lambda":
		return p.lambda.ResourcePolicy(target)
	}
	return nil, false
}

// journalOptions maps the journal setting onto the journal.
func journalOptions(j Journal, clk *clock.Clock) journal.Options {
	o := journal.Options{Capacity: j.Capacity, MaxBytes: j.MaxBytes, Clock: clk}
	if j.File != "" {
		o.File = &journal.FileOptions{Path: j.File}
	}
	return o
}

// storage opens the data directory and the blob store: on disk under the
// data directory with persistence, in memory otherwise.
func (w *wiring) storage(cfg Config) error {
	dir, ok := cfg.Persist.(PersistDir)
	if !ok {
		var err error
		w.blobs, err = blob.NewMemory("", 0)
		return err
	}
	var err error
	if w.data, err = openDataDir(dir.Path); err != nil {
		return err
	}
	w.blobs, w.blobRoot, err = w.data.blobStore()
	return err
}

// containers builds the Docker host, the DynamoDB Local behind its
// licence, the code store and, when Docker is there, the Lambda pool.
// Nothing is dialed: the host detects the topology at first use.
func (w *wiring) containers(cfg Config, clk *clock.Clock, log *slog.Logger) error {
	instance := rand.Text()[:12]
	ownership := docker.Ownership{Instance: instance, Owner: "run-" + instance}
	storage := ddblocal.Storage(ddblocal.InMemory{})
	codeDir := ""
	if w.data != nil {
		ownership.Owner = w.data.owner()
		ddb, err := w.data.dynamoDBDir()
		if err != nil {
			return err
		}
		storage = ddblocal.DBPath{Dir: ddb}
		codeDir = filepath.Join(w.data.path, "code")
	}
	var err error
	if w.host, err = newContainerHost(cfg.Docker, cfg.LambdaNetwork, ownership, clk, log); err != nil {
		return err
	}
	if w.licence, err = licence(cfg.DynamoDB, w.data); err != nil {
		return err
	}
	w.local = &lazyLocal{host: w.host, licence: w.licence, storage: storage, memory: cfg.DynamoDBMemory, log: log}
	if codeDir == "" {
		if codeDir, err = os.MkdirTemp("", "understudy-code-"); err != nil {
			return fmt.Errorf("server: creating a directory for extracted function code: %w", err)
		}
		w.codeTemp = codeDir
	}
	if w.code, err = codestore.Open(codeDir); err != nil {
		return err
	}
	if w.host.missing() != "" {
		return nil
	}
	w.pool, err = pool.New(pool.Options{
		Launcher:         w.host,
		Runtime:          w.host,
		Clock:            clk,
		StartConcurrency: cfg.Lambda.StartConcurrency,
		IdleTimeout:      cfg.Lambda.IdleTimeout,
		GlobalCap:        cfg.Lambda.MaxEnvironments,
		Logger:           log,
	})
	return err
}

// licence reads the DynamoDB Local licence state, recording an acceptance
// in the data directory.
func licence(setting DynamoDB, data *dataDir) (ddblocal.Licence, error) {
	var root *os.Root
	if data != nil {
		root = data.root
	}
	switch setting.(type) {
	case DynamoDBDisabled:
		return ddblocal.Disabled{}, nil
	case DynamoDBAccepted:
		return ddblocal.LoadLicence(root, true)
	case DynamoDBAwaiting, nil:
	}
	return ddblocal.LoadLicence(root, false)
}

// execution builds the Lambda data plane: Invoke, the asynchronous queue,
// function logs, hot reload, function URLs and, from Serve, the pollers.
func (w *wiring) execution(cfg Config, account aws.AccountID, regions aws.Regions, svc *lambda.Service, d kernel.ServiceDeps, images lambda.ImageSource) error {
	bindings, err := hotReloads(cfg.HotReload, account, regions)
	if err != nil {
		return err
	}
	o := lambda.ExecutionOptions{HotReload: bindings}
	if w.pool != nil {
		o.Pool, o.Docker, o.Images, o.Code, o.Logs = w.pool, "unix://"+w.host.socket, images, w.code, w.host
	}
	if w.lambda, err = lambda.NewExecution(svc, d, o); err != nil {
		return err
	}
	w.lambda.BindFunctionURLs(d, lambda.FunctionURLOptions{Regions: regions})
	return nil
}

// HotReloadError is a configured hot-reload binding whose account or
// region does not parse.
type HotReloadError struct {
	Function string
	Err      error
}

func (e *HotReloadError) Error() string {
	return "server: hot reload of " + e.Function + ": " + e.Err.Error()
}

func (e *HotReloadError) Unwrap() error { return e.Err }

// hotReloads parses the configured hot-reload bindings; one that names no
// account binds in account.
func hotReloads(in []HotReload, account aws.AccountID, regions aws.Regions) ([]lambda.HotReloadBinding, error) {
	out := make([]lambda.HotReloadBinding, 0, len(in))
	for _, h := range in {
		scope := aws.Scope{Partition: aws.PartitionAWS(), Account: account, Region: aws.DefaultRegion()}
		if h.Account != "" {
			a, err := aws.ParseAccountID(h.Account)
			if err != nil {
				return nil, &HotReloadError{Function: h.Function, Err: err}
			}
			scope.Account = a
		}
		if h.Region != "" {
			r, err := regions.Parse(h.Region)
			if err != nil {
				return nil, &HotReloadError{Function: h.Function, Err: err}
			}
			scope.Region = r
		}
		out = append(out, lambda.HotReloadBinding{Scope: scope, Function: h.Function, Dir: h.Dir})
	}
	return out, nil
}

// edges builds the main listener's front door and the container
// listener's.
func (w *wiring) edges(cfg Config, k *kernel.Kernel, reg *registry.Registry, idStore *identity.Store, regions aws.Regions, cat catalogue, bucketExists func(string) bool, log *slog.Logger) error {
	domains := cfg.S3BaseDomains
	if domains == nil {
		domains = defaultS3BaseDomains()
	}
	o := edge.Options{
		Kernel:                k,
		Registry:              reg,
		Identity:              idStore,
		Regions:               regions,
		Codecs:                Codecs(),
		BucketExists:          bucketExists,
		S3BaseDomains:         domains,
		S3Control:             S3ControlCodec(),
		Listener:              ref.ListenerMain,
		Admin:                 w.admin,
		Unclaimed:             ref.AWSJSON10,
		UnknownOperationError: cat.unknownOperationError,
		Log:                   log,
	}
	var err error
	if w.edge, err = edge.New(o); err != nil {
		return err
	}
	o.Listener, o.Admin = ref.ListenerContainer, w.admin.Container()
	w.containerEdge, err = edge.New(o)
	return err
}

// load replaces the empty state with the persisted one, then with the
// configured snapshot, and refuses a state that armed a timer nothing
// handles.
func (w *wiring) load(cfg Config, snapshots *snapshot.Coordinator, log *slog.Logger) error {
	if dir, ok := cfg.Persist.(PersistDir); ok {
		every := dir.SaveEvery
		if every <= 0 {
			every = DefaultSaveEvery
		}
		w.persist = &persister{dir: w.data, coord: snapshots, blobs: w.blobs, log: log, every: every}
		if err := w.persist.load(context.Background()); err != nil {
			return err
		}
	}
	if err := loadSnapshot(cfg.LoadSnapshot, w.kernel, snapshots, log); err != nil {
		return err
	}
	return checkTimers(w.kernel.Timers())
}

// UnhandledTimerError is an armed timer whose kind no handler is
// registered for. Stateful.Rearm arms timers straight from loaded state;
// one like this would never run, so the work it stands for (a message
// turning visible, a delivery attempt, a deletion window) would silently
// never happen. Nothing is served.
type UnhandledTimerError struct{ Keys []clock.TimerKey }

func (e *UnhandledTimerError) Error() string {
	named := make([]string, len(e.Keys))
	for i, k := range e.Keys {
		service, _, _ := strings.Cut(string(k.Kind), ".")
		named[i] = fmt.Sprintf("%s (service %s, timer %s)", k.Kind, service, k.ID)
	}
	return "server: timers are armed under kinds no handler is registered for: " + strings.Join(named, ", ") +
		". A service's Rearm armed a kind it never registered with Timers.Handle, which is a bug in understudy; " +
		"report it with these kinds. The saved state is unchanged on disk"
}

// checkTimers refuses a scheduler holding a timer no handler fires.
func checkTimers(t *kernel.Timers) error {
	if keys := t.Unhandled(); len(keys) > 0 {
		return &UnhandledTimerError{Keys: keys}
	}
	return nil
}

// defaultAccount is the configured default account, or 000000000000.
func defaultAccount(cfg Config) aws.AccountID {
	if cfg.DefaultAccount.IsZero() {
		return aws.DefaultAccount()
	}
	return cfg.DefaultAccount
}

// FaultRuleError is a configured fault rule the fault store refuses.
type FaultRuleError struct {
	Index int
	Err   *fault.RuleError
}

func (e *FaultRuleError) Error() string {
	return fmt.Sprintf("server: fault rule %d of the configuration: %s", e.Index, e.Err.Error())
}

func (e *FaultRuleError) Unwrap() error { return e.Err }

// preload activates the configured fault rules.
func preload(faults *fault.Store, rules []fault.Rule) error {
	for i := range rules {
		if _, err := faults.Add(rules[i]); err != nil {
			return &FaultRuleError{Index: i, Err: err}
		}
	}
	return nil
}

// collect gathers the Stateful views, with the outbox first, the Late
// services, the fidelity switches of the services and the service names.
func collect(w *wiring, box *outbox.Outbox, services ...serveradmin.Service) ([]serveradmin.Service, []snapshot.Late) {
	stateful := make([]serveradmin.Service, 0, 1+len(services))
	stateful = append(stateful, box)
	var late []snapshot.Late
	for _, s := range services {
		stateful = append(stateful, s)
		if sw, ok := s.(switched); ok {
			w.switches = append(w.switches, sw.Switches()...)
		}
		if l, ok := s.(snapshot.Late); ok {
			late = append(late, l)
		}
	}
	return stateful, late
}

// switched is a service with fidelity switches.
type switched interface {
	Switches() []fidelity.Entry
}

// deferredLinker links calls made by function code to their invocations
// once the Lambda execution layer exists: the kernel takes its Linker
// before the services that implement it are built.
type deferredLinker struct {
	x atomic.Pointer[lambda.Execution]
}

// Link links through the execution layer, and to nothing before it is
// set.
func (d *deferredLinker) Link(p identity.Principal, remote netip.AddrPort) (ref.Cause, bool) {
	x := d.x.Load()
	if x == nil {
		return ref.Cause{}, false
	}
	return x.Link(p, remote)
}

func (d *deferredLinker) set(x *lambda.Execution) { d.x.Store(x) }

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

// drainTimeout bounds how long closing waits for the Lambda environments
// and DynamoDB Local to go.
const drainTimeout = 30 * time.Second

// stopContainers removes the Lambda environments and DynamoDB Local, once:
// at shutdown while the kernel still runs, or when a wiring that never
// served is closed.
func (w *wiring) stopContainers(ctx context.Context) error {
	var err error
	w.stopped.Do(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
		defer cancel()
		var errs []error
		if w.lambda != nil {
			errs = append(errs, w.lambda.Close(ctx))
		}
		if w.local != nil {
			errs = append(errs, w.local.stop(ctx))
		}
		if err = errors.Join(errs...); err != nil {
			err = fmt.Errorf("server: removing the Lambda environments and DynamoDB Local; run understudy cleanup to remove what is left: %w", err)
		}
	})
	return err
}

// close releases what wire opened, after a failed wiring or at shutdown:
// the containers, the Runtime API listeners, the code store, the blob
// store, the data directory and, last, so it records the rest closing, the
// journal with its file.
func (w *wiring) close(ctx context.Context) error {
	errs := []error{w.stopContainers(ctx)}
	if w.host != nil {
		errs = append(errs, w.host.close())
	}
	if w.code != nil {
		errs = append(errs, w.code.Close())
	}
	if w.codeTemp != "" {
		errs = append(errs, os.RemoveAll(w.codeTemp))
	}
	if w.blobs != nil {
		errs = append(errs, w.blobs.Close())
	}
	if w.blobRoot != nil {
		errs = append(errs, w.blobRoot.Close())
	}
	if w.data != nil {
		errs = append(errs, w.data.close())
	}
	if w.journal != nil {
		errs = append(errs, w.journal.Close())
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("server: releasing the emulator's resources: %w", err)
	}
	return nil
}

// clockOptions maps the start setting onto the clock.
func clockOptions(start ClockStart, timers *kernel.Timers) clock.Options {
	o := clock.Options{Mode: clock.Running{}, Timers: timers.Scheduler()}
	switch s := start.(type) {
	case ClockFrozen:
		o.Mode = clock.Frozen{}
		if !s.At.IsZero() {
			o.Start = clock.FromWire(s.At)
		}
	case ClockScaled:
		o.Mode = clock.Scaled{Factor: s.Factor}
	case ClockRunning, nil:
	}
	return o
}

// clockStart is how a reset with Clock restarts the clock: a running or
// scaled clock runs from wall time again at its rate; a frozen one returns
// to the instant serve froze it at.
func clockStart(start ClockStart, clk *clock.Clock) func() (clock.Instant, clock.Mode) {
	switch s := start.(type) {
	case ClockFrozen:
		at := clk.Now()
		return func() (clock.Instant, clock.Mode) { return at, clock.Frozen{} }
	case ClockScaled:
		return func() (clock.Instant, clock.Mode) {
			return clock.FromWire(clk.Wall()), clock.Scaled{Factor: s.Factor}
		}
	case ClockRunning, nil:
	}
	return func() (clock.Instant, clock.Mode) { return clock.FromWire(clk.Wall()), clock.Running{} }
}
