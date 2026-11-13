package kernel_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
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

// guardRunRaw is a primary action an input chooses in place of the
// generated one, as SES v2 SendEmail's Raw content is ses:SendRawEmail,
// an action outside the operation's generated list.
var guardRunRaw = registry.IAMAction{Prefix: "guard", Name: "RunRaw"}

// primaryOf chooses guardRunRaw for an input whose Member is "raw" and
// keeps the operation's primary otherwise.
func primaryOf(_ *kernel.Call, in *guardInput) registry.IAMAction {
	if in.Member == "raw" {
		return guardRunRaw
	}
	return registry.IAMAction{}
}

// A binding whose primary action depends on the input is asked about, and
// records, the chosen action in place of the generated primary, on the
// resources the call touches, with its denial resource text; an input
// that chooses none keeps the generated primary.
func TestWithPrimaryActionReplacesThePrimaryForTheInput(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{decide: denyAction(guardRunRaw, "raw denied")}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.run, guardDone, kernel.WithoutSecondaryActions(), kernel.WithResources(touched), kernel.WithPrimaryAction(primaryOf),
			kernel.WithDenialResource(func(*kernel.Call, *guardInput) string { return "thing one" }))
		_, err := k.Invoke(k.Context(), g.run, &guardInput{Touch: []aws.ARN{guardThing1}, Member: "raw"})
		wantDenial(k, err, "raw denied")
		if _, err := k.Invoke(k.Context(), g.run, &guardInput{Touch: []aws.ARN{guardThing1}}); err != nil {
			k.Fatalf("call keeping the primary = %+v %q, want it allowed", err.ID, err.Message)
		}
		want := []asked{
			{Op: g.runDesc.ID, Query: kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: guardRunRaw, Resource: kernel.ResourceARN{ARN: guardThing1}, DenialResource: "thing one"}}}},
			{Op: g.runDesc.ID, Query: kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: guardRun, Resource: kernel.ResourceARN{ARN: guardThing1}, DenialResource: "thing one"}}}},
		}
		if got := a.questions(); !reflect.DeepEqual(got, want) {
			k.Errorf("the Authorizer was asked %+v, want %+v", got, want)
		}
		if u, ok := useOf(k, guardRunRaw, access.OnResource{ARN: guardThing1}); !ok || u.Calls != 1 {
			k.Errorf("access record of %s on %s = %+v (found %t), want the one denied call", guardRunRaw, guardThing1, u, ok)
		}
		if u, ok := useOf(k, guardRun, access.OnResource{ARN: guardThing1}); !ok || u.Calls != 1 {
			k.Errorf("access record of %s on %s = %+v (found %t), want only the call that kept it", guardRun, guardThing1, u, ok)
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
