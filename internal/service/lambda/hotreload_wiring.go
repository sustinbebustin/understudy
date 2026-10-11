package lambda

import (
	"context"
	"sync"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/lambda/hotreload"
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
// Overlays are bound from the configuration and the admin API only. The
// LocalStack-compatible magic bucket (S3Bucket "hot-reload") is not
// implemented: CreateFunction and UpdateFunctionCode fetch it as any other
// bucket and fail.

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

// overlays are the active hot-reload overlays by function.
type overlays struct {
	mu     sync.Mutex
	active map[fnKey]*overlay
	// generations rises on every start, change and end of a function's
	// overlay, so an execution generation never repeats.
	generations map[fnKey]uint64
}

// overlay is one function's active overlay.
type overlay struct {
	dir    string
	origin overlayOrigin
	stop   context.CancelFunc
}

func newOverlays() *overlays {
	return &overlays{active: map[fnKey]*overlay{}, generations: map[fnKey]uint64{}}
}

// HotReload binds dir as the code of the zip function named function in
// scope until a reset of scope; binding another directory replaces it. It
// is the admin API's hot-reload endpoint.
func (x *Execution) HotReload(_ context.Context, scope aws.Scope, function, dir string) *awserr.Error {
	_, err := view(x.svc, scope, func(st *State) (struct{}, *awserr.Error) {
		f, ok := st.Functions[function]
		if !ok {
			return struct{}{}, errFunctionNotFound(functionARN(scope, function))
		}
		if _, ok := f.Latest.Code.(ZipCode); !ok {
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
