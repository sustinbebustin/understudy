package lambda

import (
	"context"
	"log/slog"
	"sync"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/lambda/hotreload"
	"github.com/sustinbebustin/understudy/internal/store"
)

// Hot reload (ARCHITECTURE.md 14.4): a host directory overlaid on a zip
// function's code at the execution layer. The control plane never sees
// it: GetFunction keeps reporting the deployed CodeSha256 and
// LastModified, so a second terraform plan stays empty. Every change the
// watcher reports bumps the overlay's generation, so the next invocation
// starts cold with the new code. An active overlay wins over
// UpdateFunctionCode, which records journal.AdvisoryShadowedCodeUpdate on
// its own call.
//
// Overlays are bound from the configuration and the admin API. A function
// created or updated with code in the LocalStack-compatible hot-reload
// bucket (S3Bucket "hot-reload", S3Key the directory) stores the
// directory as its code (DirCode), which runs under a code binding: a
// watcher of the directory as an overlay has, which the Execution keeps in
// step with the state after every call that changes code and once a reset
// or snapshot load has replaced the state (RearmPollers). An overlay wins
// over a code binding as it does over a package.

// HotReloadBinding binds a host directory as a function's code, from the
// configuration (lambda.hotReload).
type HotReloadBinding struct {
	Scope    aws.Scope
	Function string
	// Dir is the absolute path of the directory as understudy sees it,
	// which the watcher polls: the launcher bind-mounts it, or copies it in
	// when understudy runs in a container. Inside understudy's container
	// that is the path the host directory is mounted at; host paths are not
	// translated through the container's mounts.
	Dir string
}

// overlayOrigin says who bound an overlay.
type overlayOrigin uint8

const (
	// configured overlays come from the configuration and outlive resets,
	// like the configuration itself.
	configured overlayOrigin = iota + 1
	// requested overlays come from the admin API and end with a reset of
	// their scope, like the function they were bound to.
	requested
)

// overlays are the active hot-reload overlays and code bindings by
// function.
type overlays struct {
	mu     sync.Mutex
	active map[fnKey]*overlay
	// code are the bindings of the directories functions' versions store
	// as their code.
	code map[codeBinding]*overlay
	// generations rises on every start, change and end of a function's
	// overlay and on every change of a directory bound as its code, so an
	// execution generation never repeats.
	generations map[fnKey]uint64
}

// overlay is one function's active overlay, or one code binding.
type overlay struct {
	dir    string
	origin overlayOrigin
	stop   context.CancelFunc
}

// codeBinding names a directory a version of a function stores as its
// code.
type codeBinding struct {
	fn  fnKey
	dir string
}

func newOverlays() *overlays {
	return &overlays{active: map[fnKey]*overlay{}, code: map[codeBinding]*overlay{}, generations: map[fnKey]uint64{}}
}

// HotReload binds dir as the code of the zip function named function in
// scope until a reset of scope; binding another directory replaces it. It
// is the admin API's hot-reload endpoint.
func (x *Execution) HotReload(_ context.Context, scope aws.Scope, function, dir string) *awserr.Error {
	_, err := store.View2(x.svc.cells, scope, func(st *State) (struct{}, *awserr.Error) {
		f, ok := st.Functions[function]
		if !ok {
			return struct{}{}, errFunctionNotFound(functionARN(scope, function))
		}
		if _, image := f.Latest.Code.(ImageCode); image {
			return struct{}{}, errHotReloadImage(function)
		}
		return struct{}{}, nil
	})
	if err != nil {
		return err
	}
	//nolint:contextcheck // the watcher outlives the admin request; it ends with a reset or Close
	if err := x.startOverlay(scope, function, dir, requested); err != nil {
		return user(lambdaapi.ErrInvalidParameterValueException(err.Error()))
	}
	return nil
}

// startOverlay watches dir and makes it function's code, replacing an
// overlay already bound.
func (x *Execution) startOverlay(scope aws.Scope, function, dir string, origin overlayOrigin) error {
	w, err := hotreload.New(dir, x.watch)
	if err != nil {
		return err
	}
	ctx, stop := context.WithCancel(x.ctx)
	ov := &overlay{dir: dir, origin: origin, stop: stop}
	k := fnKey{scope: scope, name: function}
	o := x.overlays
	o.mu.Lock()
	old := o.active[k]
	o.active[k] = ov
	o.generations[k]++
	o.mu.Unlock()
	if old != nil {
		old.stop()
	}
	x.wg.Go(func() { w.Run(ctx, func() { o.changed(k, ov) }) })
	return nil
}

// changed bumps the generation of ov's function while ov is its overlay.
func (o *overlays) changed(k fnKey, ov *overlay) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.active[k] == ov {
		o.generations[k]++
	}
}

// bindCode watches b's directory as code of b's function, unless it is
// bound already, and returns the function's generation. Watching starts
// from the directory as it is now; the next environment of the function
// starts on it anyway, since the code it stores changed or none ran it.
func (x *Execution) bindCode(b codeBinding) (uint64, error) {
	o := x.overlays
	o.mu.Lock()
	if _, ok := o.code[b]; ok {
		defer o.mu.Unlock()
		return o.generations[b.fn], nil
	}
	o.mu.Unlock()
	w, err := hotreload.New(b.dir, x.watch)
	if err != nil {
		return 0, err
	}
	ctx, stop := context.WithCancel(x.ctx)
	ov := &overlay{dir: b.dir, stop: stop}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.code[b]; ok {
		// Bound by another call meanwhile.
		stop()
		return o.generations[b.fn], nil
	}
	o.code[b] = ov
	x.wg.Go(func() { w.Run(ctx, func() { o.codeChanged(b, ov) }) })
	return o.generations[b.fn], nil
}

// codeChanged bumps the generation of b's function while ov binds b.
func (o *overlays) codeChanged(b codeBinding, ov *overlay) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.code[b] == ov {
		o.generations[b.fn]++
	}
}

// syncCode makes the code bindings of the scopes covers match those the
// state's versions name: it binds each directory a version stores as its
// code and ends every binding no version names any more. A directory that
// cannot be watched is logged and left unbound; the next invocation of the
// version tries again and answers why it cannot. Service.codeChanged calls
// it after calls that change code, and RearmPollers after a reset or
// snapshot load.
func (x *Execution) syncCode(covers func(aws.Scope) bool) {
	x.codeSync.Lock()
	defer x.codeSync.Unlock()
	want := map[codeBinding]bool{}
	for _, scope := range x.svc.cells.Keys(store.ScopeKeys()) {
		if cell, ok := x.svc.cells.Lookup(scope); ok && covers(scope) {
			store.Read(cell, func(st *State) struct{} {
				st.codeBindings(scope, want)
				return struct{}{}
			})
		}
	}
	o := x.overlays
	o.mu.Lock()
	for b, ov := range o.code {
		if covers(b.fn.scope) && !want[b] {
			ov.stop()
			delete(o.code, b)
		}
	}
	o.mu.Unlock()
	for b := range want {
		if _, err := x.bindCode(b); err != nil {
			x.k.Log().LogAttrs(x.ctx, slog.LevelWarn, "a hot-reload directory a Lambda function stores as its code cannot be watched; its next invocation tries again",
				slog.String("function", functionARN(b.fn.scope, b.fn.name)), slog.String("dir", b.dir), slog.String("error", err.Error()))
		}
	}
}

// codeBindings adds to want the directory every version in st, of scope,
// stores as its code.
func (st *State) codeBindings(scope aws.Scope, want map[codeBinding]bool) {
	for name := range st.Functions {
		versions := st.Functions[name].all()
		for i := range versions {
			if d, ok := versions[i].Code.(DirCode); ok {
				want[codeBinding{fn: fnKey{scope: scope, name: name}, dir: d.Dir}] = true
			}
		}
	}
}

// codeDir is the directory and generation an invocation of the function k
// names runs when its version stores code as the directory dir: an active
// overlay's, which wins, else dir under its code binding, bound now when it
// is not yet.
func (x *Execution) codeDir(k fnKey, dir string) (run string, generation uint64, err error) {
	if overlay, gen := x.overlays.current(k); overlay != "" {
		return overlay, gen, nil
	}
	generation, err = x.bindCode(codeBinding{fn: k, dir: dir})
	return dir, generation, err
}

// current returns the directory of k's active overlay, empty when none,
// and the overlay generation.
func (o *overlays) current(k fnKey) (dir string, generation uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if ov, ok := o.active[k]; ok {
		dir = ov.dir
	}
	return dir, o.generations[k]
}

// dirOf returns the directory of k's active overlay, false when none: the
// Service's overlayDir.
func (o *overlays) dirOf(k fnKey) (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	ov, ok := o.active[k]
	if !ok {
		return "", false
	}
	return ov.dir, true
}

// drop ends the requested overlays of scope.
func (o *overlays) drop(scope kernel.ResetScope) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for k, ov := range o.active {
		if ov.origin == requested && scope.Covers(k.scope) {
			ov.stop()
			delete(o.active, k)
			o.generations[k]++
		}
	}
}

// errHotReloadImage refuses an overlay on a container image function.
func errHotReloadImage(function string) *awserr.Error {
	return user(lambdaapi.ErrInvalidParameterValueException("Hot reload overlays the code of zip functions only; " + function +
		" is a container image function. Rebuild the image under the same tag and call UpdateFunctionCode instead."))
}
