package admin

import (
	"context"
	"net/http"
	"strings"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// The hooks below are implemented by the services that own the behavior;
// wire.go hands over the service when it implements one. A nil hook makes
// its endpoint answer unavailable.

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

// HotReloader binds a host directory as a function's code.
type HotReloader interface {
	HotReload(ctx context.Context, scope aws.Scope, function, dir string) *awserr.Error
}

// ImagePuller pre-pulls the pinned runtime images, returning the images
// pulled; empty runtimes means every pinned runtime.
type ImagePuller interface {
	PullImages(ctx context.Context, runtimes []string) ([]string, error)
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
	if err := h.o.HotReloader.HotReload(r.Context(), scope, args[0], in.Dir); err != nil {
		h.fail(w, r, fromAWS(err))
		return
	}
	h.noContent(w)
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
	if err != nil {
		h.fail(w, r, &admin.Error{Code: admin.Unavailable, Message: "pulling runtime images failed: " + err.Error(), Hint: "run `understudy doctor` to check Docker"})
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
	s := aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()}
	if account != "" {
		a, err := aws.ParseAccountID(account)
		if err != nil {
			return s, &admin.Error{Code: admin.InvalidRequest, Field: "account", Message: err.Error()}
		}
		s.Account = a
	}
	if region != "" {
		reg, err := h.o.Regions.Parse(region)
		if err != nil {
			return s, &admin.Error{Code: admin.InvalidRequest, Field: "region", Message: err.Error()}
		}
		s.Region = reg
	}
	return s, nil
}
