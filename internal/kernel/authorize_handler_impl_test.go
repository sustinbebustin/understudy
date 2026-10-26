package kernel_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// kernel.Authorize behavior the specification tests leave open, on the same
// guard service (authorize_test.go).

// A platform call's Authorize touches the resource on the platform call's
// own event (TestAuthorizeAllowsAPlatformCallWithoutAsking), not on its
// parent's.
func TestAuthorizeLeavesAPlatformCallsParentUntouched(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{decide: denyResource(guardRole, "denied")}
	runGuard(t, g, a, func(k *kerneltest.K) {
		var parent *kernel.Call
		bindGuard(k, g.probe, authorizeRole(nil))
		bindGuard(k, g.run, func(ctx context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
			parent = c
			return kernel.Invoke(ctx, k.Kernel, c, g.probe, kernel.Target{Scope: c.Scope(), As: kernel.AsPlatform{Name: "guard.amazonaws.com"}, Via: ref.SNSDelivery}, &guardInput{})
		})
		if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
			k.Fatalf("a platform call's Authorize = %s, want nil", err.Message)
		}
		if got := callEvent(k, parent).Resources; slices.Contains(got, guardRole) {
			k.Errorf("the parent's journaled resources = %v, want the role only on the platform call", got)
		}
	})
}

// Only platform calls skip the Authorizer: a delivery made as the service
// principal is asked, and the Authorizer sees the call's mode.
func TestAuthorizeAsksAboutAServicePrincipalsCallWithItsMode(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{decide: denyResource(guardRole, "role denied")}
	as := kernel.AsService{Name: "guard.amazonaws.com", Source: guardThing1}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.probe, authorizeRole(nil))
		bindGuard(k, g.run, func(ctx context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
			return kernel.Invoke(ctx, k.Kernel, c, g.probe, kernel.Target{Scope: c.Scope(), As: as, Via: ref.SNSDelivery}, &guardInput{})
		})
		_, err := k.Invoke(k.Context(), g.run, &guardInput{})
		wantDenial(k, err, "role denied")
		want := asked{Op: g.probeDesc.ID, Mode: as, Query: kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: passRole, Resource: kernel.ResourceARN{ARN: guardRole}}}}}
		if got := a.about(g.probeDesc); len(got) != 2 || !reflect.DeepEqual(got[1], want) {
			k.Errorf("the Authorizer was asked %+v about the delivery, want step 8 then %+v", got, want)
		}
	})
}

// An Authorizer that answers Authorize with no verdict is the same bug as
// at step 8: Internal with a VerdictError naming the operation.
func TestAuthorizeNilVerdictIsInternal(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{decide: func(q kernel.AuthzQuery) kernel.Verdict {
		if q.Pairs[0].Action == passRole {
			return nil
		}
		return kernel.Allowed{}
	}}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.run, authorizeRole(nil))
		_, err := k.Invoke(k.Context(), g.run, &guardInput{})
		if v, ok := errors.AsType[*kernel.VerdictError](internalCause(k.T, err)); !ok || v.Op != g.runDesc.ID {
			k.Errorf("cause = %v, want a VerdictError of %s", v, g.runDesc.ID)
		}
	})
}

// The zero ARN is refused with an AuthorizeError naming the action and the
// operation that asked, and nothing is touched.
func TestAuthorizeOfTheZeroARNNamesTheActionAndOperation(t *testing.T) {
	g := newGuard(guardDenial())
	runGuard(t, g, &fakeAuthorizer{}, func(k *kerneltest.K) {
		bindGuard(k, g.run, func(ctx context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
			return nil, kernel.Authorize(ctx, c, passRole, aws.ARN{}, nil)
		})
		c := k.Call(g.runDesc)
		_, err := kerneltest.Dispatch[guardInput, guardOutput](k.Context(), k, c, &guardInput{})
		if e, ok := errors.AsType[*kernel.AuthorizeError](internalCause(k.T, err)); !ok || e.Op != g.runDesc.ID || e.Action != passRole {
			k.Errorf("cause = %v, want an AuthorizeError of iam:PassRole from %s", e, g.runDesc.ID)
		}
		if got := c.Touched(); len(got) != 0 {
			k.Errorf("touched %v, want nothing", got)
		}
	})
}
