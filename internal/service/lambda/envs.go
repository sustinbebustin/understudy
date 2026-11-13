package lambda

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/netip"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/lambda/codestore"
	"github.com/sustinbebustin/understudy/internal/service/lambda/launcher"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool"
)

// Execution environments as the Lambda service sees them: the credentials
// each one runs with, the container spec built from the function record,
// and the link from a call made with those credentials to the invocation
// that runs it.

// credentialLifetime is how long an environment's credentials stay valid.
// SDKs read them from the environment and never refresh them, so they must
// outlive any environment: 12 hours, Lambda's own session length.
const credentialLifetime = 12 * time.Hour

// fnVersion names one function version: the unit of generations, warm
// environments and credentials.
type fnVersion struct {
	scope   aws.Scope
	name    string
	version string
}

// arn is the function's unqualified ARN, as launcher.Function.ARN writes it.
func (v fnVersion) arn() aws.ARN {
	return launcher.Function{Name: v.name, Account: v.scope.Account, Region: v.scope.Region}.ARN()
}

// generations is what a function version's execution generation is made
// of: the function's code generation and its hot-reload overlay's. exec
// rises whenever either changes, so the pool drains environments of the
// code they no longer run (ARCHITECTURE.md 14.2).
type generations struct {
	code, overlay, exec uint64
}

// slot is one execution environment's identity: credentials minted for it
// alone, which make its pool key, and the invocation it runs. A slot runs
// one invocation at a time, so the access key on a request a function
// makes names the invocation (ARCHITECTURE.md 10).
type slot struct {
	id    identity.EnvID
	fn    fnVersion
	creds launcher.Credentials
	// trees are the code store trees the slot's environments mount, held
	// from mint to revocation: the extracted package (none for an overlay
	// or an image) and the merged tree of the version's layers (none
	// without layers).
	trees []treeKey
	// running is the cause in-function calls link to while an invocation
	// runs; nil while the slot is free.
	running *ref.Cause
	// retired slots belong to an older generation: their credentials are
	// revoked once no invocation runs.
	retired bool
	// env is the pool environment the slot's last invocation ran in, and
	// stream its log stream; a lease of another environment is a cold start.
	env    *pool.Env
	stream *logStream
	// addr is the address byAddr holds the slot under, guarded by the
	// environments' lock; the zero Addr when it holds none.
	addr netip.Addr
}

// located is the slot whose environment runs in the container at an
// address, and that environment, whose state tells whether the container
// still holds the address.
type located struct {
	slot *slot
	env  *pool.Env
}

// versionSlots are the slots of one function version's current generation.
// The entry outlives a drop: the pool remembers the highest generation it
// saw of every function and drains environments of lower ones on release,
// so exec must never go back.
type versionSlots struct {
	gen generations
	// trees are the trees the generation's environments mount, incarnation
	// the function's Incarnation it was started for.
	trees       []treeKey
	incarnation string
	// live is false before the first lease and after a drop: the next
	// lease starts a new execution generation.
	live  bool
	slots []*slot
}

// leaseFor is what an invocation asks of its function version's slots.
type leaseFor struct {
	code, overlay uint64
	trees         []treeKey
	incarnation   string
	role          aws.ARN
	cause         ref.Cause
}

// environments keeps the slots of every function version. Its lock is a
// leaf lock apart from calls into the identity store and the code trees,
// whose locks are leaves.
type environments struct {
	identity *identity.Store
	clock    *clock.Clock
	trees    *codeTrees

	mu       sync.Mutex
	versions map[fnVersion]*versionSlots
	byID     map[identity.EnvID]*slot
	// byAddr finds a slot by its container's address, on the topologies
	// where that is the peer address of the container's calls.
	byAddr map[netip.Addr]located
	serial uint64
}

func newEnvironments(store *identity.Store, clk *clock.Clock, trees *codeTrees) *environments {
	return &environments{
		identity: store, clock: clk, trees: trees,
		versions: map[fnVersion]*versionSlots{}, byID: map[identity.EnvID]*slot{}, byAddr: map[netip.Addr]located{},
	}
}

// lease takes a free slot of v's current generation for an invocation,
// minting a new one when every slot is busy. A code or overlay generation,
// trees or incarnation other than the last one seen, or the first lease
// after a drop, starts a new execution generation, whose number lease
// returns, and retires the older slots. The incarnation tells a function
// deleted and created again apart, whose generations start over.
func (e *environments) lease(v fnVersion, w leaseFor) (*slot, uint64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	vs, ok := e.versions[v]
	if !ok {
		vs = &versionSlots{}
		e.versions[v] = vs
	}
	if !vs.live || vs.gen.code != w.code || vs.gen.overlay != w.overlay || !slices.Equal(vs.trees, w.trees) || vs.incarnation != w.incarnation {
		e.retireLocked(vs)
		vs.gen = generations{code: w.code, overlay: w.overlay, exec: vs.gen.exec + 1}
		vs.trees, vs.incarnation, vs.live = w.trees, w.incarnation, true
	}
	for _, s := range vs.slots {
		if s.running == nil {
			s.running = &w.cause
			return s, vs.gen.exec, nil
		}
	}
	s, err := e.mintLocked(v, w.role, vs.trees)
	if err != nil {
		return nil, 0, err
	}
	s.running = &w.cause
	vs.slots = append(vs.slots, s)
	return s, vs.gen.exec, nil
}

// release frees s after its invocation; a retired slot's credentials are
// revoked now.
func (e *environments) release(s *slot) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s.running = nil
	if s.retired {
		e.revokeLocked(s)
	}
}

// link links a call made with the credentials of the environment p
// resolved from to the invocation it is running, and to nothing between
// invocations. A call with credentials no environment minted from the
// address of an environment's live container is Unminted, naming the
// environment's function; an unsigned call carries no credentials, so it
// is Unlinked wherever it comes from.
func (e *environments) link(p identity.Principal, remote netip.AddrPort) kernel.Link {
	if src, ok := mintedFor(p); ok {
		e.mu.Lock()
		defer e.mu.Unlock()
		s, ok := e.byID[src.Env]
		if !ok || s.running == nil {
			return kernel.Unlinked{}
		}
		return kernel.Linked{Cause: *s.running}
	}
	if _, unsigned := p.(identity.Anonymous); unsigned {
		return kernel.Unlinked{}
	}
	e.mu.Lock()
	at, ok := e.byAddr[remote.Addr().Unmap()]
	e.mu.Unlock()
	if !ok {
		return kernel.Unlinked{}
	}
	// A removed container gave its address back to the daemon, which may
	// have handed it to a container that is no function's.
	if _, gone := at.env.State().(pool.Gone); gone {
		return kernel.Unlinked{}
	}
	return kernel.Unminted{Scope: at.slot.fn.scope, Function: at.slot.fn.arn()}
}

// mintedFor reports the environment whose minted credentials p resolved
// from.
func mintedFor(p identity.Principal) (identity.FromExecutionEnvironment, bool) {
	role, ok := p.(identity.AssumedRole)
	if !ok {
		return identity.FromExecutionEnvironment{}, false
	}
	src, ok := role.Source.(identity.FromExecutionEnvironment)
	return src, ok
}

// locate records that s's environment runs in env's container, so a call
// from the container's address finds s; a container without an address
// is found by nothing.
func (e *environments) locate(s *slot, env *pool.Env) {
	addr := env.Container().Addr
	e.mu.Lock()
	defer e.mu.Unlock()
	e.unlocateLocked(s)
	if !addr.IsValid() {
		return
	}
	s.addr = addr
	e.byAddr[addr] = located{slot: s, env: env}
}

// unlocateLocked forgets s's container address, unless a newer container
// of another slot holds it now. Callers hold e.mu.
func (e *environments) unlocateLocked(s *slot) {
	if at, ok := e.byAddr[s.addr]; ok && at.slot == s {
		delete(e.byAddr, s.addr)
	}
	s.addr = netip.Addr{}
}

// drop retires the slots of the function versions in scope and returns
// their functions' ARNs, whose environments the caller drains. The next
// lease of each starts a new execution generation.
func (e *environments) drop(scope kernel.ResetScope) []aws.ARN {
	e.mu.Lock()
	defer e.mu.Unlock()
	seen := map[aws.ARN]bool{}
	var arns []aws.ARN
	for v, vs := range e.versions {
		if !scope.Covers(v.scope) || !vs.live {
			continue
		}
		e.retireLocked(vs)
		vs.live = false
		if a := v.arn(); !seen[a] {
			seen[a] = true
			arns = append(arns, a)
		}
	}
	return arns
}

// retireLocked retires every slot of vs. Callers hold e.mu.
func (e *environments) retireLocked(vs *versionSlots) {
	for _, s := range vs.slots {
		s.retired = true
		if s.running == nil {
			e.revokeLocked(s)
		}
	}
	vs.slots = nil
}

func (e *environments) revokeLocked(s *slot) {
	e.identity.Revoke(string(s.creds.AccessKeyID))
	delete(e.byID, s.id)
	e.unlocateLocked(s)
	for _, t := range s.trees {
		e.trees.unhold(t)
	}
}

// mintLocked mints a new slot's credentials: a session of the function's
// execution role whose source names the environment, so a call made with
// them resolves to it. The slot holds trees. Callers hold e.mu.
func (e *environments) mintLocked(v fnVersion, role aws.ARN, trees []treeKey) (*slot, error) {
	roleRef, account, err := parseRole(role)
	if err != nil {
		return nil, err
	}
	e.serial++
	id := identity.EnvID(fmt.Sprintf("%016x", e.serial))
	session := v.name
	if !aws.ValidRoleSessionName(session) {
		session = "lambda-" + v.name
	}
	p := identity.AssumedRole{Account: account, Role: roleRef, Session: session, Source: identity.FromExecutionEnvironment{Function: v.arn(), Env: id}}
	creds, err := e.identity.MintTemporary(p, e.clock.Wall().Add(credentialLifetime))
	if err != nil {
		return nil, fmt.Errorf("mint the execution role credentials of %s: %w", v.arn(), err)
	}
	var token ids.SessionToken
	if tmp, ok := creds.Lifetime.(identity.Temporary); ok {
		token = tmp.SessionToken
	}
	s := &slot{id: id, fn: v, trees: trees, creds: launcher.Credentials{AccessKeyID: creds.AccessKeyID, SecretAccessKey: creds.SecretAccessKey, SessionToken: token}}
	e.byID[id] = s
	for _, t := range trees {
		e.trees.hold(t)
	}
	return s, nil
}

// revokeAll revokes every slot's credentials, at shutdown.
func (e *environments) revokeAll() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range e.byID {
		e.identity.Revoke(string(s.creds.AccessKeyID))
	}
	clear(e.byID)
	clear(e.byAddr)
	clear(e.versions)
}

// parseRole reads an execution role ARN, arn:aws:iam::<account>:role<path><name>.
func parseRole(role aws.ARN) (identity.RoleRef, aws.AccountID, error) {
	account, err := aws.ParseAccountID(role.Account)
	rest, ok := strings.CutPrefix(role.Resource, "role/")
	if err != nil || !ok || rest == "" {
		return identity.RoleRef{}, aws.AccountID{}, fmt.Errorf("the execution role %s does not name an IAM role", role)
	}
	path, name := "/", rest
	if i := strings.LastIndexByte(rest, '/'); i >= 0 {
		path, name = "/"+rest[:i+1], rest[i+1:]
	}
	return identity.RoleRef{Name: name, Path: path}, account, nil
}

// spec is the container spec of t's environments for slot s in execution
// generation gen: the function's configuration, its runtime's pinned base
// image or the image digest its last deployment recorded, its code (the
// hot-reload overlay's directory when one is active), the merged tree of
// its layers and the slot's credentials.
func (x *Execution) spec(ctx context.Context, t *target, s *slot, gen uint64, overlayDir string) (launcher.Spec, error) {
	cfg := t.version.Config
	arch, err := launcher.ParseArchitecture(cfg.Architecture)
	if err != nil {
		return launcher.Spec{}, err
	}
	spec := launcher.Spec{
		Function:           launcher.Function{Name: t.ref.name, Version: t.version.Number, Account: t.ref.scope.Account, Region: t.ref.scope.Region},
		Generation:         gen,
		Architecture:       arch,
		MemoryMB:           int(cfg.MemorySize),
		EphemeralStorageMB: int(cfg.EphemeralStorage),
		Environment:        functionEnvironment(cfg),
		Credentials:        s.creds,
		LogGroup:           logGroup(t.ref.name, cfg.Logging),
	}
	switch code := t.version.Code.(type) {
	case ZipCode:
		img, ok := launcher.ImageForRuntime(cfg.Runtime)
		if !ok {
			return launcher.Spec{}, fmt.Errorf("understudy pins no base image for the runtime %s; the runtimes it runs are listed by `understudy images pull --help`", cfg.Runtime)
		}
		if spec.Image, err = x.resolveImage(ctx, arch, img.Ref()); err != nil {
			return launcher.Spec{}, err
		}
		if spec.Code, err = x.zipCode(ctx, code, cfg, overlayDir); err != nil {
			return launcher.Spec{}, err
		}
	case ImageCode:
		if spec.Image, err = x.pinnedImage(ctx, code, arch); err != nil {
			return launcher.Spec{}, err
		}
		image := launcher.ImageCode{}
		if settings := cfg.Image; settings != nil {
			image.EntryPoint, image.Command, image.WorkingDirectory = settings.EntryPoint, settings.Command, smithy.Deref(settings.WorkingDirectory)
		}
		spec.Code = image
	}
	return spec, nil
}

// zipCode is how a zip function's code and layers reach its container:
// the overlay's directory or the package's extracted tree at /var/task,
// and the merged tree of cfg's layers, in order, at /opt
// (lambda-destinations-layers.md 8.1, 11.5).
func (x *Execution) zipCode(ctx context.Context, code ZipCode, cfg Config, overlayDir string) (launcher.ZipCode, error) {
	out := launcher.ZipCode{Dir: overlayDir, Handler: cfg.Handler}
	var err error
	if out.Dir == "" {
		if out.Dir, err = x.trees.dir(ctx, code); err != nil {
			return launcher.ZipCode{}, err
		}
	}
	if len(cfg.Layers) > 0 {
		if out.Opt, err = x.trees.opt(ctx, cfg.Layers); err != nil {
			return launcher.ZipCode{}, err
		}
	}
	return out, nil
}

// functionEnvironment is the function's Environment.Variables over the
// variables its LoggingConfig sets (containers.md 7): the runtime
// interface clients read AWS_LAMBDA_LOG_FORMAT and AWS_LAMBDA_LOG_LEVEL,
// the application log level, which the JSON format defaults to INFO.
func functionEnvironment(cfg Config) map[string]string {
	env := map[string]string{}
	if cfg.Logging.Format != "" {
		env["AWS_LAMBDA_LOG_FORMAT"] = cfg.Logging.Format
	}
	switch {
	case cfg.Logging.ApplicationLevel != "":
		env["AWS_LAMBDA_LOG_LEVEL"] = cfg.Logging.ApplicationLevel
	case cfg.Logging.Format == string(lambdaapi.LogFormatJson):
		env["AWS_LAMBDA_LOG_LEVEL"] = string(lambdaapi.ApplicationLogLevelInfo)
	}
	maps.Copy(env, cfg.Environment)
	return env
}

// pinnedImage is the reference of the image code's last deployment
// recorded: its repository digest, so a local rebuild under the same tag
// changes nothing until UpdateFunctionCode, as on AWS. An image built
// locally under Docker's classic image store has no repository digest and
// was recorded by its image ID, the second reference tried.
func (x *Execution) pinnedImage(ctx context.Context, code ImageCode, arch launcher.Architecture) (string, error) {
	if code.Digest == "" {
		return "", fmt.Errorf("no digest is recorded for the image %s; deploy it again with UpdateFunctionCode", code.URI)
	}
	image, err := x.resolveImage(ctx, arch, launcher.Repository(code.URI)+"@"+code.Digest, code.Digest)
	if err != nil {
		return "", fmt.Errorf("the image %s as deployed (%s) is not present: %w; build or pull it again, or deploy the image now tagged %s with UpdateFunctionCode", code.URI, code.Digest, err, code.URI)
	}
	return image, nil
}

// resolveImage makes the first of refs that can be present for arch
// present and returns the reference the daemon resolved. The answer is
// remembered under the first reference, so warm invocations make no
// Docker round trip; forgetImage drops it when a container start fails.
func (x *Execution) resolveImage(ctx context.Context, arch launcher.Architecture, refs ...string) (string, error) {
	key := imageRequest{ref: refs[0], arch: arch}
	x.imagesMu.Lock()
	resolved, ok := x.resolved[key]
	x.imagesMu.Unlock()
	if ok {
		return resolved, nil
	}
	var errs []error
	for _, r := range refs {
		img, err := x.images.EnsureImage(ctx, r, arch, nil)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		x.imagesMu.Lock()
		x.resolved[key] = img.Ref
		x.imagesMu.Unlock()
		return img.Ref, nil
	}
	return "", errors.Join(errs...)
}

// forgetImage drops every remembered resolution to image.
func (x *Execution) forgetImage(image string) {
	x.imagesMu.Lock()
	defer x.imagesMu.Unlock()
	maps.DeleteFunc(x.resolved, func(_ imageRequest, ref string) bool { return ref == image })
}

// imageRequest is an image asked for an architecture.
type imageRequest struct {
	ref  string
	arch launcher.Architecture
}

// PullImages makes the pinned runtime base images present for the host's
// architecture, every pinned runtime when runtimes is empty, and returns
// the references made present. It is the admin API's image pull, behind
// `understudy images pull`, so CI pays for the pulls before the first
// invocation.
func (x *Execution) PullImages(ctx context.Context, runtimes []string) ([]string, error) {
	if x.images == nil {
		return nil, errors.New("no Docker daemon is reachable" + x.dockerAt() + "; start Docker, or run `understudy doctor` to diagnose")
	}
	pins := launcher.RuntimeImages()
	if len(runtimes) > 0 {
		pins = pins[:0:0]
		for _, r := range runtimes {
			img, ok := launcher.ImageForRuntime(r)
			if !ok {
				return nil, &UnpinnedRuntimeError{Runtime: r, Pinned: pinnedRuntimes()}
			}
			pins = append(pins, img)
		}
	}
	arch := launcher.X86
	if runtime.GOARCH == "arm64" {
		arch = launcher.ARM64
	}
	pulled := make([]string, 0, len(pins))
	for _, img := range pins {
		if _, err := x.images.EnsureImage(ctx, img.Ref(), arch, nil); err != nil {
			return pulled, fmt.Errorf("pull the %s image %s: %w", img.Runtime, img.Ref(), err)
		}
		pulled = append(pulled, img.Ref())
	}
	return pulled, nil
}

// UnpinnedRuntimeError is a PullImages request for a runtime understudy
// pins no image for: the caller's mistake, not an unavailable daemon.
type UnpinnedRuntimeError struct {
	Runtime string
	// Pinned are the runtimes with a pinned image.
	Pinned []string
}

func (e *UnpinnedRuntimeError) Error() string {
	return fmt.Sprintf("understudy pins no image for the runtime %q; pull one of %s", e.Runtime, strings.Join(e.Pinned, ", "))
}

// RequestFault marks the error as one the request caused, which the admin
// API answers as invalid_request.
func (*UnpinnedRuntimeError) RequestFault() {}

// pinnedRuntimes names the runtimes with a pinned image.
func pinnedRuntimes() []string {
	images := launcher.RuntimeImages()
	out := make([]string, len(images))
	for i, img := range images {
		out[i] = img.Runtime
	}
	return out
}

// treeKey names a code store tree environments mount.
//
//sumtype:decl
type treeKey interface{ isTreeKey() }

// packageTree is a zip package's extracted tree, the read-only /var/task,
// by the package's CodeSha256.
type packageTree string

// layersTree is the merged tree of a function's layers, the read-only
// /opt, by the layers' CodeSha256s in order joined by commas, which
// standard base64 never holds.
type layersTree string

func (packageTree) isTreeKey() {}
func (layersTree) isTreeKey()  {}

// layersKey names the merged tree of layers.
func layersKey(layers []LayerRef) layersTree {
	shas := make([]string, len(layers))
	for i, l := range layers {
		shas[i] = l.Sha256
	}
	return layersTree(strings.Join(shas, ","))
}

// codeTrees builds each tree environments mount once in the code store,
// a zip package's extraction or the merged tree of a function's layers,
// and gives a tree back once no slot holds it.
type codeTrees struct {
	store *codestore.Store
	blobs *blob.Store
	log   *slog.Logger

	mu    sync.Mutex
	trees map[treeKey]*codeTree
	// holds counts the slots whose environments mount a tree.
	holds map[treeKey]int
	// releases are the trees being given back. closed is set at Close
	// before they are waited for: an unhold after it, of an invocation
	// shutdown did not wait for, starts no release of its own.
	releases sync.WaitGroup
	closed   bool
}

// codeTree is one tree's build; done closes when it ends.
type codeTree struct {
	done chan struct{}
	// digest is the code store's name for the tree: a package's CodeSha256,
	// or a merged tree's Key.
	digest codestore.Digest
	dir    string
	err    error
}

func newCodeTrees(store *codestore.Store, blobs *blob.Store, log *slog.Logger) *codeTrees {
	return &codeTrees{store: store, blobs: blobs, log: log, trees: map[treeKey]*codeTree{}, holds: map[treeKey]int{}}
}

// hold counts one more slot mounting the tree key names.
func (c *codeTrees) hold(key treeKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.holds[key]++
}

// unhold counts one slot less mounting the tree key names and, with the
// last one, gives the tree back to the code store in the background.
func (c *codeTrees) unhold(key treeKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.holds[key]--
	if c.holds[key] > 0 {
		return
	}
	delete(c.holds, key)
	t, ok := c.trees[key]
	if !ok {
		return
	}
	select {
	case <-t.done:
	default:
		// Only a holder builds, so no build runs once the last one is
		// gone; were one running, release gives its tree back at Close.
		return
	}
	delete(c.trees, key)
	if t.err != nil {
		return
	}
	c.releases.Go(func() {
		if err := c.giveBack(key, t); err != nil {
			c.log.Warn("removing a Lambda code tree no function runs any more failed; it stays on disk until restart",
				slog.String("tree", t.dir), slog.String("error", err.Error()))
		}
	})
}

// giveBack drops the code store reference the build of t took.
func (c *codeTrees) giveBack(key treeKey, t *codeTree) error {
	switch key.(type) {
	case packageTree:
		return c.store.Release(t.digest)
	case layersTree:
		return c.store.ReleaseMerged(t.digest)
	}
	return fmt.Errorf("code tree %v has a key of no known kind", key)
}

// dir returns the extracted tree of z, extracting it on first use.
func (c *codeTrees) dir(ctx context.Context, z ZipCode) (string, error) {
	return c.get(ctx, packageTree(z.Sha256), func(ctx context.Context) (codestore.Digest, string, error) {
		code, err := c.extract(ctx, z.Sha256, z.Blob)
		return code.Sha256, code.Dir, err
	})
}

// opt returns the merged tree of layers, in order, building it on first
// use.
func (c *codeTrees) opt(ctx context.Context, layers []LayerRef) (string, error) {
	return c.get(ctx, layersKey(layers), func(ctx context.Context) (codestore.Digest, string, error) {
		m, err := c.merge(ctx, layers)
		return m.Key, m.Dir, err
	})
}

// get returns the tree key names, building it with build on first use;
// callers asking at once share one build, and a failed one is retried by
// the next caller. The build does not end with the request of the caller
// that started it, whose cancellation would fail the others sharing it;
// the package size caps bound it.
func (c *codeTrees) get(ctx context.Context, key treeKey, build func(context.Context) (codestore.Digest, string, error)) (string, error) {
	c.mu.Lock()
	t, ok := c.trees[key]
	if !ok {
		t = &codeTree{done: make(chan struct{})}
		c.trees[key] = t
	}
	c.mu.Unlock()
	if !ok {
		t.digest, t.dir, t.err = build(context.WithoutCancel(ctx))
		if t.err != nil {
			c.mu.Lock()
			delete(c.trees, key)
			c.mu.Unlock()
		}
		close(t.done)
	}
	select {
	case <-t.done:
		return t.dir, t.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (c *codeTrees) extract(ctx context.Context, sha string, archive blob.Ref) (codestore.Code, error) {
	if c.blobs == nil {
		return codestore.Code{}, errors.New("the Lambda service has no blob store holding function packages")
	}
	r, err := c.blobs.Open(archive)
	if err != nil {
		return codestore.Code{}, fmt.Errorf("open the package with CodeSha256 %s: %w", sha, err)
	}
	defer func() { _ = r.Close() }()
	code, err := c.store.PutZip(ctx, r, codestore.Options{})
	if err != nil {
		return codestore.Code{}, fmt.Errorf("extract the package with CodeSha256 %s: %w", sha, err)
	}
	return code, nil
}

// merge extracts each layer's archive, merges the trees in order into one
// (lambda-destinations-layers.md 8.1) and gives the extractions back: the
// merged tree is a copy.
func (c *codeTrees) merge(ctx context.Context, layers []LayerRef) (codestore.Merged, error) {
	digests := make([]codestore.Digest, 0, len(layers))
	var err error
	for _, l := range layers {
		code, xerr := c.extract(ctx, l.Sha256, l.Blob)
		if xerr != nil {
			err = fmt.Errorf("layer %s: %w", l.ARN, xerr)
			break
		}
		digests = append(digests, code.Sha256)
	}
	var m codestore.Merged
	if err == nil {
		m, err = c.store.Merge(ctx, digests)
		if conflict, ok := errors.AsType[*codestore.MergeConflictError](err); ok {
			err = fmt.Errorf("%w (layer %d is %s); what AWS does with such layers is unrecorded, so understudy runs no function with them: "+
				"change one of the layers so that the path is a file in both or a directory in both", err, conflict.Layer, layers[conflict.Layer].ARN)
		}
	}
	var released error
	for _, d := range digests {
		released = errors.Join(released, c.store.Release(d))
	}
	if err != nil {
		return codestore.Merged{}, errors.Join(err, released)
	}
	if released != nil {
		c.log.WarnContext(ctx, "removing the extracted layer archives of a merged /opt failed; they stay on disk until restart",
			slog.String("tree", m.Dir), slog.String("error", released.Error()))
	}
	return m, nil
}

// release gives every tree back to the code store, at Close.
func (c *codeTrees) release() error {
	// Every releases.Go ran under c.mu before closed was set, so it is
	// ordered before the Wait.
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.releases.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []error
	for key, t := range c.trees {
		select {
		case <-t.done:
			if t.err == nil {
				errs = append(errs, c.giveBack(key, t))
			}
		default:
			// A build still running releases nothing; its caller holds the
			// reference until the store closes.
		}
		delete(c.trees, key)
	}
	clear(c.holds)
	return errors.Join(errs...)
}
