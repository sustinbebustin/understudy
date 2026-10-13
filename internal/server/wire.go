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
	"github.com/sustinbebustin/understudy/internal/config"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/fault"
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
	"github.com/sustinbebustin/understudy/internal/service/lambda/hotreload"
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
	// runs is this process's identity, nil where there is no procfs.
	runs *runs
	// temp is the run's temporary directory without persistence, holding
	// the blob spill and the extracted function code; empty with
	// persistence, where both live under the data directory.
	temp string
	// code holds extracted function code.
	code   *codestore.Store
	pool   *pool.Pool
	lambda *lambda.Execution
	admin  *serveradmin.Handler
	edge   *edge.Handler
	// containerEdge serves the container listener: the AWS API and only
	// health and clock from the admin prefix.
	containerEdge *edge.Handler
	// clockFrom is what set the clock serve starts with, which the banner
	// names.
	clockFrom clockOrigin
	stopped   sync.Once
}

// wire builds the registry, the kernel with its clock, journal, faults,
// access recorder, identity store and outbox, the services, the snapshot
// coordinator, the admin API and the front door, then loads the persisted
// state. Each service constructor gets kernel.ServiceDeps and its Options;
// this file is the only place that names every service and codec.
func wire(cfg Config, log *slog.Logger) (w *wiring, err error) {
	w = &wiring{clockFrom: clockSetting{}}
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
	instance := rand.Text()[:12]
	w.identify(log)
	if err := w.storage(cfg, instance); err != nil {
		return w, err
	}
	if err := w.containers(cfg, instance, clk, log); err != nil {
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
	stateful, late := collect(box, sqsSvc, snsSvc, s3Svc, dynamoSvc, lambdaSvc, w.lambda.AsyncQueue(), logsSvc, secretsSvc, kmsSvc, iamSvc, stsSvc, eventsSvc, cloudwatchSvc)
	if err := checkCollected(reg, stateful); err != nil {
		return w, err
	}
	rt := &runtime{clock: clk, ids: src, seed: cfg.Seed, journal: w.journal, faults: w.faults}
	snapshots, err := coordinator(k, w.blobs, rt, idStore, stateful, late, "understudy "+cfg.Version)
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
	if err := w.snapshotDirs(&opts); err != nil {
		return w, err
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

// snapshotDirs sets the admin API's snapshot directories: with
// persistence, the path form's snapshots/ and the download spool tmp/
// under the data directory, emptied now; else only the spool, the run's
// temporary directory. A later start removes either spool, so an archive
// a crash leaves mid-download does not stay behind.
func (w *wiring) snapshotDirs(o *serveradmin.Options) error {
	if w.data == nil {
		o.TempDir = w.temp
		return nil
	}
	o.DataDir = w.data.snapshots
	var err error
	o.TempDir, err = w.data.spool()
	return err
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
// data directory with persistence, in memory otherwise, spilling into the
// run's temporary directory.
func (w *wiring) storage(cfg Config, instance string) error {
	var err error
	dir, ok := cfg.Persist.(PersistDir)
	if !ok {
		if w.temp, err = w.makeTemp(instance); err != nil {
			return err
		}
		w.blobs, err = blob.NewMemory(w.temp, 0)
		return err
	}
	if w.data, err = openDataDir(dir.Path); err != nil {
		return err
	}
	w.blobs, w.blobRoot, err = w.data.blobStore()
	return err
}

// containers builds the Docker host, the DynamoDB Local behind its
// licence, the code store and, when Docker is there, the Lambda pool.
// Nothing is dialed: the host detects the topology at first use. The
// containers' owner is the data directory's with persistence, else the
// run's identity, which a later run sweeps once this process is gone.
func (w *wiring) containers(cfg Config, instance string, clk *clock.Clock, log *slog.Logger) error {
	ownership := docker.Ownership{Instance: instance}
	var storage ddblocal.Storage
	var codeDir string
	switch {
	case w.data != nil:
		ownership.Owner = w.data.owner()
		ddb, err := w.data.dynamoDBDir()
		if err != nil {
			return err
		}
		storage = ddblocal.DBPath{Dir: ddb}
		codeDir = filepath.Join(w.data.path, "code")
	case w.runs != nil:
		ownership.Owner = w.runs.newRun(instance).String()
		storage, codeDir = ddblocal.InMemory{}, filepath.Join(w.temp, "code")
	default:
		ownership.Owner = runPrefix + instance
		storage, codeDir = ddblocal.InMemory{}, filepath.Join(w.temp, "code")
	}
	var err error
	if w.host, err = newContainerHost(cfg.Docker, cfg.LambdaNetwork, ownership, clk, log); err != nil {
		return err
	}
	if w.licence, err = licence(cfg.DynamoDB, w.data); err != nil {
		return err
	}
	w.local = &lazyLocal{host: w.host, licence: w.licence, storage: storage, memory: cfg.DynamoDBMemory, log: log}
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
	if w.host.client != nil {
		// Named even without a pool, so Invoke's error names the socket
		// that is missing.
		o.Docker = "unix://" + w.host.socket
	}
	if w.pool != nil {
		o.Pool, o.Images, o.Code, o.Logs = w.pool, images, w.code, w.host
	} else if w.host.client == nil {
		// Disabled by configuration: starting Docker would not help.
		o.NoPool = w.host.missing()
	}
	if w.lambda, err = lambda.NewExecution(svc, d, o); err != nil {
		return unwatchable(cfg.HotReload, err)
	}
	w.lambda.BindFunctionURLs(d, lambda.FunctionURLOptions{Regions: regions})
	return nil
}

// hotReloadSetting names the hot-reload binding at index i.
func hotReloadSetting(i int) string { return fmt.Sprintf("lambda.hotReload[%d]", i) }

// hotReloads parses the configured hot-reload bindings; one that names no
// account binds in account. A binding whose account or region does not
// parse is a *SettingError.
func hotReloads(in []HotReload, account aws.AccountID, regions aws.Regions) ([]lambda.HotReloadBinding, error) {
	out := make([]lambda.HotReloadBinding, 0, len(in))
	defaults := aws.Scope{Partition: aws.PartitionAWS(), Account: account, Region: aws.DefaultRegion()}
	for i, h := range in {
		scope, err := aws.ParseScope(h.Account, h.Region, defaults, regions)
		if err != nil {
			return nil, &SettingError{Setting: hotReloadSetting(i), Reason: "the function " + h.Function + ": " + err.Error(), Err: err}
		}
		out = append(out, lambda.HotReloadBinding{Scope: scope, Function: h.Function, Dir: h.Dir})
	}
	return out, nil
}

// unwatchable is err from building the Lambda execution layer, as a
// *SettingError naming the binding when it is a hot-reload directory the
// layer cannot watch. The bindings start in order and the first that
// fails stops the rest, so the first binding of the directory is the one
// that failed.
func unwatchable(bindings []HotReload, err error) error {
	dirErr, ok := errors.AsType[*hotreload.DirError](err)
	if !ok {
		return err
	}
	for i, h := range bindings {
		if h.Dir == dirErr.Dir {
			return &SettingError{
				Setting: hotReloadSetting(i) + ".dir",
				Reason:  fmt.Sprintf("the function %s cannot read its code from %q: %s", h.Function, h.Dir, dirErr.Reason),
				Hint:    "create the directory, point dir at the function's build output, or remove the binding",
				Err:     err,
			}
		}
	}
	return err
}

// edges builds the main listener's front door and the container
// listener's. A main listener beyond loopback answers every Host from a
// client that is not a browser, since its clients name it by whatever
// their network calls it, such as a compose service name; checkExposure
// has already required what guards it. The container listener keeps
// LocalHosts: function containers address it by IP literal. Both share one
// ServedPorts, so a page understudy served on any listener's port is
// refused on every other one.
func (w *wiring) edges(cfg Config, k *kernel.Kernel, reg *registry.Registry, idStore *identity.Store, regions aws.Regions, cat catalogue, bucketExists func(string) bool, log *slog.Logger) error {
	domains := cfg.S3BaseDomains
	if domains == nil {
		domains = config.DefaultBaseDomains()
	}
	var hosts edge.HostPolicy = edge.LocalHosts{}
	if !cfg.Listen.Addr().IsLoopback() {
		hosts = edge.NetworkHosts{}
	}
	o := edge.Options{
		Hosts:                 hosts,
		Kernel:                k,
		Registry:              reg,
		Identity:              idStore,
		Regions:               regions,
		Codecs:                Codecs(),
		BucketExists:          bucketExists,
		S3BaseDomains:         domains,
		S3Control:             S3ControlCodec(),
		Listener:              ref.ListenerMain,
		Served:                &edge.ServedPorts{},
		Admin:                 w.admin,
		Unclaimed:             ref.AWSJSON10,
		UnknownOperationError: cat.unknownOperationError,
		Log:                   log,
	}
	var err error
	if w.edge, err = edge.New(o); err != nil {
		return err
	}
	o.Listener, o.Admin, o.Hosts = ref.ListenerContainer, w.admin.Container(), edge.LocalHosts{}
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
		w.persist = &persister{dir: w.data, coord: snapshots, blobs: w.blobs, clock: w.kernel.Clock(), log: log, every: every}
		restored, err := w.persist.load(context.Background())
		if err != nil {
			return err
		}
		if restored {
			w.clockFrom = clockRestored{dir: w.data.path}
		}
	}
	if cfg.LoadSnapshot != "" {
		if err := loadSnapshot(cfg.LoadSnapshot, w.kernel, snapshots, log); err != nil {
			return err
		}
		w.clockFrom = clockLoaded{path: cfg.LoadSnapshot}
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

// preload activates the configured fault rules. A rule the fault store
// refuses is a *SettingError naming the rule's field at fault.
func preload(faults *fault.Store, rules []fault.Rule) error {
	for i := range rules {
		if _, err := faults.Add(rules[i]); err != nil {
			return ruleSettingError(i, err)
		}
	}
	return nil
}

// ruleSettingError is the refusal of the configured fault rule at index i.
func ruleSettingError(i int, err *fault.RuleError) *SettingError {
	setting := fmt.Sprintf("faults[%d]", i)
	if err.Field != "" {
		setting += "." + err.Field
	}
	reason := err.Reason
	if err.Rule != "" {
		reason = fmt.Sprintf("rule %q: %s", err.Rule, reason)
	}
	out := &SettingError{Setting: setting, Reason: reason, Err: err}
	if len(err.Known) > 0 {
		names := make([]string, len(err.Known))
		for j, s := range err.Known {
			names[j] = string(s)
		}
		out.Hint = "use one of the errors it sends: " + strings.Join(names, ", ")
	}
	return out
}

// collect gathers the Stateful views, with the outbox first, and the Late
// services.
func collect(box *outbox.Outbox, services ...serveradmin.Service) ([]serveradmin.Service, []snapshot.Late) {
	stateful := make([]serveradmin.Service, 0, 1+len(services))
	stateful = append(stateful, box)
	var late []snapshot.Late
	for _, s := range services {
		stateful = append(stateful, s)
		if l, ok := s.(snapshot.Late); ok {
			late = append(late, l)
		}
	}
	return stateful, late
}

// UncollectedServiceError names registered services wire passed no
// Stateful view of to collect: their state would silently be left out of
// snapshots, persistence, resets and the resource listing. It is a bug in
// wire.go, which names each service in apis(), its constructor block and
// its collect call; nothing is served.
type UncollectedServiceError struct{ Services []ref.ServiceID }

func (e *UncollectedServiceError) Error() string {
	names := make([]string, len(e.Services))
	for i, s := range e.Services {
		names[i] = string(s)
	}
	return "server: the registered services " + strings.Join(names, ", ") + " are not collected for snapshots, resets and resources; " +
		"this is a bug in understudy's wiring: pass each one to collect in wire.go"
}

// checkCollected refuses a registry service without a collected Stateful
// view of the same name.
func checkCollected(reg *registry.Registry, stateful []serveradmin.Service) error {
	collected := make(map[ref.ServiceID]bool, len(stateful))
	for _, s := range stateful {
		collected[s.Name()] = true
	}
	var missing []ref.ServiceID
	for _, svc := range reg.Services() {
		if !collected[svc.ID] {
			missing = append(missing, svc.ID)
		}
	}
	if len(missing) > 0 {
		return &UncollectedServiceError{Services: missing}
	}
	return nil
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

// removeOwnTimeout bounds the last sweep of this instance's containers
// after the drain.
const removeOwnTimeout = 10 * time.Second

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
		if w.host != nil {
			// The orderly stop may have run out of time; this instance's
			// labels find what it left, with a deadline of its own.
			last, cancelLast := context.WithTimeout(context.WithoutCancel(ctx), removeOwnTimeout)
			errs = append(errs, w.host.removeOwn(last))
			cancelLast()
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
	if w.blobs != nil {
		errs = append(errs, w.blobs.Close())
	}
	if w.temp != "" {
		errs = append(errs, os.RemoveAll(w.temp))
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
