package kernel_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The bind options that change what step 8 asks: AuthorizedInHandler and
// NeverAuthorized, on the guard service (authorize_test.go).

// denyAnyResource denies a question holding a pair on AnyResource, as an
// evaluator does for an action a policy grants only on named resources.
func denyAnyResource(q kernel.AuthzQuery) kernel.Verdict {
	for _, p := range q.Pairs {
		if _, ok := p.Resource.(kernel.AnyResource); ok {
			return kernel.Denied{Message: "denied on *"}
		}
	}
	return kernel.Allowed{}
}

// authorizeRunOn is a handler that resolves thing and asks about the
// operation's primary action on it, as GetRole does on the role it reads.
func authorizeRunOn(thing aws.ARN) kernel.Handler[guardInput, guardOutput] {
	return func(ctx context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
		if err := kernel.Authorize(ctx, c, guardRun, thing, nil); err != nil {
			return nil, err
		}
		return &guardOutput{}, nil
	}
}

// A binding whose handler authorizes its primary action on the resource it
// resolves is not asked about that action at step 8, where nothing names
// the resource yet: AnyResource there would deny a caller whose policy
// grants the action only on that resource, which AWS allows
// (iam-evaluation.md 2.1). The handler's question is the only one.
func TestAuthorizedInHandlerLeavesThePrimaryActionToTheHandler(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{decide: denyAnyResource}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.run, authorizeRunOn(guardThing1), kernel.AuthorizedInHandler())
		if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
			k.Fatalf("call = %+v %q, want the handler's check to allow it", err.ID, err.Message)
		}
		want := []asked{{Op: g.runDesc.ID, Query: kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: guardRun, Resource: kernel.ResourceARN{ARN: guardThing1}}}}}}
		if got := a.questions(); !reflect.DeepEqual(got, want) {
			k.Errorf("the Authorizer was asked %+v, want only the handler's %+v", got, want)
		}
	})
}

// AuthorizedInHandler removes only the primary action: the other actions
// the binding declares are still asked about at step 8, the primary not
// even when declared, and the handler's denial is the call's answer.
func TestAuthorizedInHandlerKeepsTheDeclaredActionsAtStep8(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{decide: denyResource(guardThing2, "thing denied")}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.run, authorizeRunOn(guardThing2), kernel.AuthorizedInHandler(), kernel.WithActions(declared))
		_, err := k.Invoke(k.Context(), g.run, &guardInput{Declare: []registry.IAMAction{guardRun, guardTag}})
		wantDenial(k, err, "thing denied")
		want := []asked{
			{Op: g.runDesc.ID, Query: kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: guardTag, Resource: kernel.AnyResource{}}}}},
			{Op: g.runDesc.ID, Query: kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: guardRun, Resource: kernel.ResourceARN{ARN: guardThing2}}}}},
		}
		if got := a.questions(); !reflect.DeepEqual(got, want) {
			k.Errorf("the Authorizer was asked %+v, want step 8 about the declared action, then the handler's %+v", got, want[1])
		}
	})
}

// AuthorizedInHandler declared apart from the bind site (kernel.Declare)
// applies as at the bind site.
func TestAuthorizedInHandlerThroughDeclare(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{decide: denyAnyResource}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.run, authorizeRunOn(guardThing1))
		kernel.Declare(k.Kernel.Binder(g.svc), g.run, kernel.AuthorizedInHandler())
		if err := k.Kernel.BindErr(); err != nil {
			k.Fatal(err)
		}
		if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
			k.Fatalf("call = %+v %q, want step 8 to leave the primary action to the handler", err.ID, err.Message)
		}
		if got := a.questions(); len(got) != 1 {
			k.Errorf("the Authorizer was asked %+v, want only the handler's question", got)
		}
	})
}

// An operation AWS never evaluates, even under an explicit Deny
// (sts:GetCallerIdentity, iam-enforcement-surfaces.md 5.1), is never asked
// about at step 8, whatever it touches or declares.
func TestNeverAuthorizedIsNotAskedAboutAtStep8(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{decide: denyAll("denied")}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.run, guardDone, kernel.NeverAuthorized(), kernel.WithResources(touched), kernel.WithActions(declared))
		in := &guardInput{Touch: []aws.ARN{guardThing1}, Declare: []registry.IAMAction{guardTag}}
		if _, err := k.Invoke(k.Context(), g.run, in); err != nil {
			k.Fatalf("call = %+v %q, want it never asked about", err.ID, err.Message)
		}
		if got := a.questions(); len(got) != 0 {
			k.Errorf("the Authorizer was asked %+v, want nothing", got)
		}
	})
}
