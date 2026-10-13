package admin

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// The hooks below are implemented by the services that own the behavior;
// wire.go assigns each hook from the service that implements it. A nil
// hook (as in tests) makes its endpoint answer unavailable.

// Drainer stops the execution environments and pollers of the state a
// reset or snapshot load replaces. The Lambda service implements it.
type Drainer interface {
	// Name is the service whose reset needs the drain.
	Name() ref.ServiceID
	// Drain returns once the environments and pollers of scope are gone.
	Drain(ctx context.Context, scope kernel.ResetScope) error
}

// LateResetter drops a Late service's data in the accounts and regions of
// scope, as a reset by account or region needs. ImportLate replaces the
// data whole, so a Late service without it refuses such a reset. Reset
// checks every Options.Late value for it.
type LateResetter interface {
	ResetLate(ctx context.Context, scope kernel.ResetScope) error
}

// Scoper names the accounts and regions a service holds state in, without
// serializing it; an account-keyed service (IAM, STS) reports a zero region.
type Scoper interface {
	Scopes() []aws.Scope
}

// Service is a Stateful the admin API serves: reset, resources and
// snapshot load reach its state, and GET /resources asks Scopes where that
// state lives, since kernel.Stateful lists the resources of a scope it is
// given but names none.
type Service interface {
	kernel.Stateful
	Scoper
}

// PollerRearmer reconciles the event source mapping pollers with the state
// after a reset or snapshot load has rearmed every service. The Lambda
// execution layer implements it.
type PollerRearmer interface {
	RearmPollers()
}

// Components reports the state of what services run on, for health.
type Components interface {
	Components(ctx context.Context) ComponentStatus
}

// ComponentStatus is the health of the components services run on.
type ComponentStatus struct {
	Docker        admin.ServiceStatus
	DynamoDBLocal admin.ServiceStatus
	// Topology names how containers reach understudy.
	Topology admin.Topology
	// Services replaces the status of the services that cannot serve
	// without a component, such as Lambda and DynamoDB without Docker.
	Services map[ref.ServiceID]admin.ServiceStatus
}

// HotReloader binds a host directory as a function's code.
type HotReloader interface {
	HotReload(ctx context.Context, scope aws.Scope, function, dir string) *awserr.Error
}

// ImagePuller pre-pulls the pinned runtime images, returning the images
// pulled; empty runtimes means every pinned runtime. An error the request
// caused, such as a runtime with no pinned image, implements RequestFault;
// any other means the images cannot be pulled now.
type ImagePuller interface {
	PullImages(ctx context.Context, runtimes []string) ([]string, error)
}

// RequestFault is a hook error the request caused rather than the
// emulator's state; it answers invalid_request.
type RequestFault interface {
	error
	RequestFault()
}

// TTLSweeper runs the DynamoDB TTL sweep now, returning the items deleted.
type TTLSweeper interface {
	SweepTTL(ctx context.Context) (int, error)
}

// SubscriptionTokens reads the confirmation token of a pending SNS
// subscription.
type SubscriptionTokens interface {
	SubscriptionToken(subscription aws.ARN) (string, *awserr.Error)
}

// notInBuild answers an endpoint whose service hook is not wired.
func notInBuild(what string) *admin.Error {
	return &admin.Error{
		Code:    admin.Unavailable,
		Message: what + " is not available: the service that provides it is not implemented in this build",
		Hint:    "check `understudy coverage` for the services this build implements",
	}
}

// fromAWS maps a service's AWS-shaped error onto the admin vocabulary. A
// 501 is a hook the service has but does not implement yet: unavailable,
// as for a hook the build lacks, not an internal failure.
func fromAWS(e *awserr.Error) *admin.Error {
	switch {
	case e.ID.Status == http.StatusNotImplemented:
		return &admin.Error{Code: admin.Unavailable, Message: e.ID.Shape.Name() + ": " + e.Message, Hint: "check `understudy coverage` for what this build implements"}
	case e.ID.Status == http.StatusNotFound:
		return &admin.Error{Code: admin.NotFound, Message: e.ID.Shape.Name() + ": " + e.Message}
	case e.ID.Status >= 400 && e.ID.Status < 500:
		return &admin.Error{Code: admin.InvalidRequest, Message: e.ID.Shape.Name() + ": " + e.Message}
	}
	return &admin.Error{Code: admin.Internal, Message: e.ID.Shape.Name() + ": " + e.Message}
}

func (h *Handler) hotReload(w http.ResponseWriter, r *http.Request, args []string) {
	if h.o.HotReloader == nil {
		h.fail(w, r, notInBuild("hot reload"))
		return
	}
	var in admin.HotReloadRequest
	if err := readJSON(w, r, &in, false); err != nil {
		h.fail(w, r, err)
		return
	}
	if strings.TrimSpace(in.Dir) == "" {
		h.fail(w, r, &admin.Error{Code: admin.InvalidRequest, Field: "dir", Message: "hot reload needs the host directory to bind", Hint: `send {"dir": "/path/to/build"}`})
		return
	}
	scope, err := h.scope(in.Account, in.Region)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	dir, err := overlayDir(in.Dir)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.o.HotReloader.HotReload(r.Context(), scope, args[0], dir); err != nil {
		h.fail(w, r, fromAWS(err))
		return
	}
	h.noContent(w)
}

// overlayDir resolves dir, the directory a hot-reload call names, to the
// one the function container binds: an absolute path with every symbolic
// link resolved, to a directory the user understudy runs as owns. On
// loopback the admin API answers any local user without a token, and the
// Docker daemon binds the directory into a container that reads it as
// root, so without the owner check any local user could read a directory
// only root may, such as /root or /etc, through a function. The resolved
// path is what is bound, so Docker does not follow a link the caller
// controls. Bindings declared in the configuration file are the
// operator's and are not checked here.
func overlayDir(dir string) (string, *admin.Error) {
	const nothing = "; nothing was bound"
	refuse := func(msg, hint string) (string, *admin.Error) {
		return "", &admin.Error{Code: admin.InvalidRequest, Field: "dir", Message: msg + nothing, Hint: hint}
	}
	if !filepath.IsAbs(dir) {
		return refuse(fmt.Sprintf("%q is not an absolute path", dir), "give the directory's absolute host path, such as $PWD/build")
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return refuse(fmt.Sprintf("%s cannot be resolved on the host: %v", dir, err), "check that the directory exists and that the user understudy runs as can reach it")
	}
	named := resolved
	if resolved != filepath.Clean(dir) {
		named = fmt.Sprintf("%s (where %s leads)", resolved, dir)
	}
	info, err := os.Lstat(resolved)
	switch {
	case err != nil:
		return refuse(fmt.Sprintf("%s cannot be read on the host: %v", named, err), "check that the user understudy runs as can reach the directory")
	case !info.IsDir():
		return refuse(named+" is not a directory", "give the directory the function's build writes its code to")
	}
	if owner, ok := ownerUID(info); !ok || owner != os.Getuid() {
		return refuse(
			fmt.Sprintf("%s is not owned by the user understudy runs as (uid %d): the function container reads the directory as root, so the admin API binds only directories that user owns", named, os.Getuid()),
			"build into a directory the user running understudy owns, or declare the binding under lambda.hotReload in the configuration file",
		)
	}
	return resolved, nil
}

// ownerUID is the uid that owns the file info describes; false where the
// platform reports none.
func ownerUID(info fs.FileInfo) (int, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}

func (h *Handler) imagePull(w http.ResponseWriter, r *http.Request, _ []string) {
	if h.o.ImagePuller == nil {
		h.fail(w, r, notInBuild("pulling runtime images"))
		return
	}
	var in admin.ImagePullRequest
	if err := readJSON(w, r, &in, true); err != nil {
		h.fail(w, r, err)
		return
	}
	images, err := h.o.ImagePuller.PullImages(r.Context(), in.Runtimes)
	if fault, ok := errors.AsType[RequestFault](err); ok {
		h.fail(w, r, &admin.Error{Code: admin.InvalidRequest, Field: "runtimes", Message: fault.Error()})
		return
	}
	if err != nil {
		// Every cause names its own next step, such as starting Docker.
		h.fail(w, r, &admin.Error{Code: admin.Unavailable, Message: "pulling runtime images failed: " + err.Error()})
		return
	}
	h.ok(w, r, admin.ImagePullResult{Images: images})
}

func (h *Handler) ttlSweep(w http.ResponseWriter, r *http.Request, _ []string) {
	if h.o.TTLSweeper == nil {
		h.fail(w, r, notInBuild("the DynamoDB TTL sweep"))
		return
	}
	n, err := h.o.TTLSweeper.SweepTTL(r.Context())
	if err != nil {
		h.fail(w, r, &admin.Error{Code: admin.Unavailable, Message: "the TTL sweep failed: " + err.Error(), Hint: "check that DynamoDB Local is running with `understudy health`"})
		return
	}
	h.ok(w, r, admin.TTLSweepResult{Deleted: n})
}

func (h *Handler) subscriptionToken(w http.ResponseWriter, r *http.Request, args []string) {
	if h.o.Subscriptions == nil {
		h.fail(w, r, notInBuild("reading subscription tokens"))
		return
	}
	arn, err := aws.ParseARN(args[0])
	if err != nil {
		h.fail(w, r, &admin.Error{Code: admin.InvalidRequest, Message: err.Error(), Hint: "pass the subscription ARN Subscribe returned"})
		return
	}
	token, aerr := h.o.Subscriptions.SubscriptionToken(arn)
	if aerr != nil {
		h.fail(w, r, fromAWS(aerr))
		return
	}
	h.ok(w, r, admin.SubscriptionToken{Subscription: arn.String(), Token: token})
}

// scope parses an optional account and region, defaulting to the default
// account and region.
func (h *Handler) scope(account, region string) (aws.Scope, *admin.Error) {
	defaults := aws.Scope{Partition: aws.PartitionAWS(), Account: h.o.DefaultAccount, Region: aws.DefaultRegion()}
	s, err := aws.ParseScope(account, region, defaults, h.o.Regions)
	if err != nil {
		out := &admin.Error{Code: admin.InvalidRequest, Message: err.Error()}
		if se, ok := errors.AsType[*aws.ScopeError](err); ok {
			out.Field = se.Part.String()
		}
		return s, out
	}
	return s, nil
}
