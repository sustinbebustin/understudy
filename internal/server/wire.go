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
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/authz"
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
	"github.com/sustinbebustin/understudy/internal/policycatalogue"
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
// this file is the only place that names every service.
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
	src := idSource(cfg.Seed)
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
	idStore, err := identityStore(cfg.StaticKeys, src, account)
	if err != nil {
		return w, err
	}
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
	c := &construction{switches: cfg.Switches, regions: regions, local: w.local, images: w.images()}
	built, err := c.build(deps)
	if err != nil {
		return w, err
	}
	if err := w.execution(cfg, account, regions, c.lambda, c.lambdaDeps, c.images); err != nil {
		return w, err
	}
	link.set(w.lambda)
	if err := k.BindErr(); err != nil {
		return w, err
	}
	if err := bindOutbox(box, k, w.faults, built); err != nil {
		return w, err
	}
	stateful, late := collect(box, built, w.lambda)
	if err := checkCollected(reg, stateful); err != nil {
		return w, err
	}
	rt := &runtime{clock: clk, ids: src, seed: cfg.Seed, journal: w.journal, faults: w.faults}
	snapshots, err := coordinator(k, w.blobs, rt, idStore, stateful, late, "understudy "+cfg.Version)
	if err != nil {
		return w, err
	}
	opts := serveradmin.Options{
		Kernel:           k,
		Faults:           w.faults,
		Access:           recorder,
		Snapshots:        snapshots,
		Blobs:            w.blobs,
		Services:         stateful,
		Late:             late,
		Regions:          regions,
		ClockStart:       clockStart(cfg.ClockStart, clk),
		DefaultAccount:   account,
		MaxSnapshotBytes: snapshotMaxBytes(cfg),
		Token:            cfg.AdminToken,
		Version:          cfg.Version,
		ModelPin:         modelPin,
		Uptime:           clk.Stopwatch(),
		Components:       components{host: w.host, local: w.local},
		Log:              log,

		Drainer:       w.lambda,
		Pollers:       w.lambda,
		HotReloader:   w.lambda,
		ImagePuller:   w.lambda,
		TTLSweeper:    c.dynamodb,
		Subscriptions: c.sns,
	}
	if err := w.snapshotDirs(&opts); err != nil {
		return w, err
	}
	if w.admin, err = serveradmin.New(opts); err != nil {
		return w, err
	}
	if err := w.edges(cfg, k, reg, idStore, regions, cat, c.s3.BucketExists, log); err != nil {
		return w, err
	}
	if err := w.load(cfg, snapshots, log); err != nil {
		return w, err
	}
	return w, preload(w.faults, cfg.Faults)
}

// idSource draws ids from seed when one is set, else from crypto/rand.
func idSource(seed *uint64) *ids.Source {
	if seed != nil {
		return ids.NewSeeded(*seed)
	}
	return ids.NewRandom()
}

// identityStore is the credential store, holding the static keys; a key it
// refuses is a *SettingError naming the accounts.keys entry.
func identityStore(keys []StaticKey, src *ids.Source, account aws.AccountID) (*identity.Store, error) {
	store := identity.New(src, account)
	static := make([]identity.StaticKey, len(keys))
	for i, k := range keys {
		static[i] = identity.StaticKey{AccessKeyID: ids.AccessKeyID(k.AccessKeyID), Secret: ids.SecretAccessKey(k.Secret), Account: k.Account}
	}
	if err := store.SetStaticKeys(static); err != nil {
		setting := "accounts.keys"
		if keyErr, ok := errors.AsType[*identity.StaticKeyError](err); ok {
			setting = fmt.Sprintf("accounts.keys[%d]", keyErr.Index)
		}
		return nil, &SettingError{Setting: setting, Reason: err.Error(), Err: err}
	}
	return store, nil
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

// images is the Lambda image source: the container host when there is a
// pool to run images in, else none.
func (w *wiring) images() lambda.ImageSource {
	if w.pool == nil {
		return nil
	}
	return w.host
}

// bindOutbox binds the outbox to the kernel, with the access advisory
// asking the built services that store resource policies.
func bindOutbox(box *outbox.Outbox, k *kernel.Kernel, faults *fault.Store, built []wiredBuilt) error {
	policies, err := policyProviders(built)
	if err != nil {
		return err
	}
	return box.Bind(k, outbox.Options{Faults: faults, Policies: policies})
}

// policySources routes a resource-policy read to the service that stores
// the target's policy, by the target ARN's service namespace: the queue's
// and the topic's Policy, the bucket policy, the function's permissions,
// the key policy, the secret's resource policy and the role's trust policy
// (iam-enforcement-surfaces.md 12.2), and the event bus's policy
// (eventbridge-buses.md 10.2.5). The outbox's access advisory asks it about
// delivery targets; it never asks for an event bus's policy, since it skips
// events-to-events deliveries (ADR 0015 amendment 2026-10-26;
// eventbridge-buses.md 6.2, 6.3).
type policySources map[string]authz.PolicyProvider

// The router reads every namespace's policies through one provider.
var _ authz.PolicyProvider = policySources(nil)

// policyProviders indexes the built services that implement
// authz.PolicyProvider by their descriptor's ARNNamespace. Two providers
// for one namespace are a *PolicySourceError.
func policyProviders(built []wiredBuilt) (policySources, error) {
	out := policySources{}
	by := map[string]ref.ServiceID{}
	for _, b := range built {
		p, ok := b.svc.(authz.PolicyProvider)
		if !ok {
			continue
		}
		ns := b.desc.ARNNamespace
		if first, dup := by[ns]; dup {
			return nil, &PolicySourceError{Namespace: ns, Services: [2]ref.ServiceID{first, b.desc.ID}}
		}
		out[ns], by[ns] = p, b.desc.ID
	}
	return out, nil
}

// ResourcePolicy asks the provider of target's namespace; a namespace no
// service provides has no resource policy.
func (p policySources) ResourcePolicy(target aws.ARN) (*policydoc.Document, bool) {
	src, ok := p[target.Service]
	if !ok {
		return nil, false
	}
	return src.ResourcePolicy(target)
}

// PolicySourceError names an ARN namespace whose resource policies two
// wired services provide, so a resource-policy read could ask only one of
// them. It is a bug in wire.go; nothing is served.
type PolicySourceError struct {
	Namespace string
	Services  [2]ref.ServiceID
}

func (e *PolicySourceError) Error() string {
	return fmt.Sprintf("server: the wired services %s and %s both provide the resource policies of ARN namespace %q, so a resource-policy read cannot tell which to ask; "+
		"this is a bug in understudy's wiring: have one service in wire.go provide each namespace", e.Services[0], e.Services[1], e.Namespace)
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

// containers builds the Docker host, DynamoDB Local, the code store and,
// when Docker is there, the Lambda pool. Nothing is dialed: the host
// detects the topology at first use. The containers' owner is the data
// directory's with persistence, else the run's identity, which a later run
// sweeps once this process is gone.
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
	if w.host, err = newContainerHost(cfg.Docker, cfg.LambdaNetwork, ownership, cfg.DockerLabels, clk, log); err != nil {
		return err
	}
	w.local = &lazyLocal{host: w.host, mode: cfg.DynamoDB, storage: storage, memory: cfg.DynamoDBMemory, log: log}
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

// execution builds the Lambda data plane: Invoke, the asynchronous queue,
// function logs, hot reload, function URLs and, from Serve, the pollers.
func (w *wiring) execution(cfg Config, account aws.AccountID, regions aws.Regions, svc *lambda.Service, d kernel.ServiceDeps, images lambda.ImageSource) error {
	bindings, err := hotReloads(cfg.HotReload, account, regions)
	if err != nil {
		return err
	}
	o := lambda.ExecutionOptions{HotReload: bindings}
	if w.host.client != nil {
		// Named even without a pool, so Invoke's error names the daemon
		// that is missing.
		o.Docker = w.host.endpoint.String()
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
// refused on every other one, and one ClockTimeline, so a URL presigned
// after a clock change made on the main listener is placed after it on
// the container listener too.
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
		Timeline:              &edge.ClockTimeline{},
		Admin:                 w.admin,
		Unclaimed:             ref.AWSJSON10,
		UnknownOperationError: cat.unknownOperationError,
		Log:                   log,
	}
	vocabulary, refuse := s3.AuthDialect()
	o.ServiceAuth = map[ref.ServiceID]edge.ServiceAuth{s3api.Service().ID: {Vocabulary: vocabulary, ParseError: refuse, Payload: edge.ServiceChecksPayload}}
	o.OperationAuth = map[ref.OperationID]auth.Vocabulary{{Service: lambdaapi.Service().ID, Name: "InvokeFunctionUrl"}: lambda.FunctionURLAuth()}
	o.ProtocolAuth = map[edge.ServiceProtocol]auth.Vocabulary{{Service: sqsapi.Service().ID, Protocol: ref.AWSJSON10}: sqs.JSONAuth()}
	o.Verify = cfg.SigV4 == SigV4Verify
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
		w.persist = &persister{dir: w.data, coord: snapshots, blobs: w.blobs, clock: w.kernel.Clock(), log: log, every: every, maxBytes: snapshotMaxBytes(cfg)}
		restored, err := w.persist.load(context.Background())
		if err != nil {
			return err
		}
		if restored {
			w.clockFrom = clockRestored{dir: w.data.path}
		}
	}
	if cfg.LoadSnapshot != "" {
		if err := loadSnapshot(cfg.LoadSnapshot, snapshotMaxBytes(cfg), w.kernel, snapshots, log); err != nil {
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

// snapshotMaxBytes is the configured snapshot archive cap, or the admin
// API's default.
func snapshotMaxBytes(cfg Config) int64 {
	if cfg.SnapshotMaxBytes <= 0 {
		return serveradmin.DefaultMaxSnapshotBytes
	}
	return cfg.SnapshotMaxBytes
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
		out.Hint = "use one of the errors it sends: " + strings.Join(err.Choices(), ", ")
	}
	return out
}

// collect gathers the Stateful views: the outbox first, then each built
// service's in table order, with the Lambda execution layer's asynchronous
// queue beside Lambda's (ARCHITECTURE.md 14.7); and the Late services.
func collect(box *outbox.Outbox, built []wiredBuilt, x *lambda.Execution) ([]serveradmin.Service, []snapshot.Late) {
	views := make([]serveradmin.Service, 0, 2+len(built))
	views = append(views, box)
	for _, b := range built {
		views = append(views, b.svc)
		if b.svc.Name() == x.Name() {
			views = append(views, x.AsyncQueue())
		}
	}
	var late []snapshot.Late
	for _, v := range views {
		if l, ok := v.(snapshot.Late); ok {
			late = append(late, l)
		}
	}
	return views, late
}

// UncollectedServiceError names registered services wire collected no
// Stateful view of: their state would silently be left out of snapshots,
// persistence, resets and the resource listing. It is a bug in wire.go:
// its wiring table collects each row's service, so a row's service is
// named other than its descriptor; nothing is served.
type UncollectedServiceError struct{ Services []ref.ServiceID }

func (e *UncollectedServiceError) Error() string {
	names := make([]string, len(e.Services))
	for i, s := range e.Services {
		names[i] = string(s)
	}
	return "server: the registered services " + strings.Join(names, ", ") + " are not collected for snapshots, resets and resources; " +
		"this is a bug in understudy's wiring: each one's row in wiredServices in wire.go must build a service whose Name is the descriptor's ID"
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

// wiredService is one row of the wiring table: a generated API and the
// constructor of the service that serves it.
type wiredService struct {
	api api
	// build constructs the service from its kernel.ServiceDeps and the
	// run's settings in c, and records in c the services wire hands on by
	// type.
	build func(kernel.ServiceDeps, *construction) serveradmin.Service
}

// wiredServices is the wiring table: every service, in the order wire
// registers, builds and collects them. apis(), the collected Stateful
// views, checkCollected and the policy lookup all read it, so a new service
// is one row. S3 routes through its restXml route table only, so it has no
// unknown-operation identity.
func wiredServices() []wiredService {
	return []wiredService{
		{api{sqsapi.Service(), sqsapi.ThrottleError(), sqsapi.UnknownOperationError}, func(d kernel.ServiceDeps, c *construction) serveradmin.Service {
			return sqs.New(d, sqs.Options{DeletedRecently: c.switches.SQSDeletedRecently})
		}},
		{api{snsapi.Service(), snsapi.ThrottleError(), snsapi.UnknownOperationError}, func(d kernel.ServiceDeps, c *construction) serveradmin.Service {
			c.sns = sns.New(d, sns.Options{})
			return c.sns
		}},
		{api{s3api.Service(), s3api.ThrottleError(), nil}, func(d kernel.ServiceDeps, c *construction) serveradmin.Service {
			c.s3 = s3.New(d, s3.Options{CrossRegion: c.switches.S3CrossRegion, Regions: c.regions})
			return c.s3
		}},
		{api{dynamodbapi.Service(), dynamodbapi.ThrottleError(), dynamodbapi.UnknownOperationError}, func(d kernel.ServiceDeps, c *construction) serveradmin.Service {
			c.dynamodb = dynamodb.New(d, dynamodb.Options{Local: c.local})
			return c.dynamodb
		}},
		{api{lambdaapi.Service(), lambdaapi.ThrottleError(), lambdaapi.UnknownOperationError}, func(d kernel.ServiceDeps, c *construction) serveradmin.Service {
			c.lambda, c.lambdaDeps = lambda.New(d, lambda.Options{StateTransitions: c.switches.LambdaTransitions, Images: c.images}), d
			return c.lambda
		}},
		{api{logsapi.Service(), logsapi.ThrottleError(), logsapi.UnknownOperationError}, func(d kernel.ServiceDeps, _ *construction) serveradmin.Service {
			return logs.New(d, logs.Options{})
		}},
		{api{secretsmanagerapi.Service(), secretsmanagerapi.ThrottleError(), secretsmanagerapi.UnknownOperationError}, func(d kernel.ServiceDeps, _ *construction) serveradmin.Service {
			return secretsmanager.New(d, secretsmanager.Options{})
		}},
		{api{kmsapi.Service(), kmsapi.ThrottleError(), kmsapi.UnknownOperationError}, func(d kernel.ServiceDeps, _ *construction) serveradmin.Service {
			return kms.New(d, kms.Options{})
		}},
		{api{iamapi.Service(), iamapi.ThrottleError(), iamapi.UnknownOperationError}, func(d kernel.ServiceDeps, _ *construction) serveradmin.Service {
			return iam.New(d, iam.Options{Catalogue: policycatalogue.Embedded})
		}},
		{api{stsapi.Service(), stsapi.ThrottleError(), stsapi.UnknownOperationError}, func(d kernel.ServiceDeps, c *construction) serveradmin.Service {
			return sts.New(d, sts.Options{AssumeRole: c.switches.STSAssumeRole})
		}},
		{api{eventbridgeapi.Service(), eventbridgeapi.ThrottleError(), eventbridgeapi.UnknownOperationError}, func(d kernel.ServiceDeps, _ *construction) serveradmin.Service {
			return events.New(d, events.Options{})
		}},
		{api{cloudwatchapi.Service(), cloudwatchapi.ThrottleError(), cloudwatchapi.UnknownOperationError}, func(d kernel.ServiceDeps, _ *construction) serveradmin.Service {
			return cloudwatch.New(d, cloudwatch.Options{})
		}},
	}
}

// apis lists every wired service's generated API, in table order.
func apis() []api {
	rows := wiredServices()
	out := make([]api, len(rows))
	for i, r := range rows {
		out[i] = r.api
	}
	return out
}

// wiredBuilt is a built row: the service and its descriptor.
type wiredBuilt struct {
	desc *registry.Service
	svc  serveradmin.Service
}

// construction is what the constructors take beyond kernel.ServiceDeps,
// and the services wire hands on by type once every row is built: to the
// front door (S3's bucket lookup), the admin hooks (DynamoDB's TTL sweep,
// SNS's subscription tokens) and the Lambda execution layer, the named
// hook built after the table. Handing them on by type keeps the compiler
// checking that each implements its hook.
type construction struct {
	switches Switches
	regions  aws.Regions
	local    *lazyLocal
	images   lambda.ImageSource

	s3         *s3.Service
	dynamodb   *dynamodb.Service
	sns        *sns.Service
	lambda     *lambda.Service
	lambdaDeps kernel.ServiceDeps
}

// build constructs every row of the wiring table in order. A table whose
// rows leave a service wire hands on by type unbuilt is an
// *UnbuiltServiceError.
func (c *construction) build(deps func(*registry.Service) kernel.ServiceDeps) ([]wiredBuilt, error) {
	rows := wiredServices()
	built := make([]wiredBuilt, len(rows))
	for i, r := range rows {
		built[i] = wiredBuilt{desc: r.api.desc, svc: r.build(deps(r.api.desc), c)}
	}
	var missing []ref.ServiceID
	for _, m := range []struct {
		unbuilt bool
		id      ref.ServiceID
	}{
		{c.s3 == nil, s3api.Service().ID},
		{c.dynamodb == nil, dynamodbapi.Service().ID},
		{c.sns == nil, snsapi.Service().ID},
		{c.lambda == nil, lambdaapi.Service().ID},
	} {
		if m.unbuilt {
			missing = append(missing, m.id)
		}
	}
	if len(missing) > 0 {
		return nil, &UnbuiltServiceError{Services: missing}
	}
	return built, nil
}

// UnbuiltServiceError names services that the front door, an admin hook or
// the Lambda execution layer takes by type and no row of the wiring table
// built. It is a bug in wire.go; nothing is served.
type UnbuiltServiceError struct{ Services []ref.ServiceID }

func (e *UnbuiltServiceError) Error() string {
	names := make([]string, len(e.Services))
	for i, s := range e.Services {
		names[i] = string(s)
	}
	return "server: the services " + strings.Join(names, ", ") + " are not built, but the front door, the admin hooks or the Lambda execution layer need them; " +
		"this is a bug in understudy's wiring: keep each one's row in wiredServices in wire.go, recording the service in its construction field"
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
// store, the run's temporary directory, the data directory and, last, so it
// records the rest closing, the journal with its file.
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
