package kernel_test

import (
	"context"
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// WithResourcesFor beyond the blind tests (iam-enforcement-surfaces.md
// 12.6): options naming one action add up, an AuthorizedAfterResolve
// binding is asked with the same scoping, and an invalid input resolves
// nothing.

// Two options naming the same action pair it with the resources of both.
func TestWithResourcesForOptionsNamingOneActionAddUp(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.run, guardDone, kernel.WithActions(declared),
			kernel.WithResourcesFor([]registry.IAMAction{guardTag}, fixed(guardThing1)),
			kernel.WithResourcesFor([]registry.IAMAction{guardTag}, fixed(guardThing2, guardThing1)))
		if _, err := k.Invoke(k.Context(), g.run, &guardInput{Declare: []registry.IAMAction{guardTag}}); err != nil {
			k.Fatalf("call = %s", err.Message)
		}
		want := []kernel.Pair{
			{Action: guardRun, Resource: kernel.ResourceARN{ARN: guardThing1}},
			{Action: guardRun, Resource: kernel.ResourceARN{ARN: guardThing2}},
			{Action: guardTag, Resource: kernel.ResourceARN{ARN: guardThing1}},
			{Action: guardTag, Resource: kernel.ResourceARN{ARN: guardThing2}},
		}
		if got := a.about(g.runDesc); len(got) != 1 || !samePairs(got[0].Query.Pairs, want) {
			k.Errorf("the Authorizer was asked %+v, want once about %+v", got, want)
		}
	})
}

// An AuthorizedAfterResolve binding's deferred question keeps the scoping:
// the named action stays on its resolved resource while the primary takes
// what the handler touched too.
func TestAuthorizedAfterResolveKeepsTheScoping(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.run, func(_ context.Context, c *kernel.Call, in *guardInput) (*guardOutput, *awserr.Error) {
			c.Touch(in.Touch...)
			return &guardOutput{}, nil
		}, kernel.WithActions(declared), kernel.AuthorizedAfterResolve(),
			kernel.WithResourcesFor([]registry.IAMAction{guardTag}, fixed(guardThing2)))
		if _, err := k.Invoke(k.Context(), g.run, &guardInput{Touch: []aws.ARN{guardThing1}, Declare: []registry.IAMAction{guardTag}}); err != nil {
			k.Fatalf("call = %s", err.Message)
		}
		want := []kernel.Pair{
			{Action: guardRun, Resource: kernel.ResourceARN{ARN: guardThing2}},
			{Action: guardRun, Resource: kernel.ResourceARN{ARN: guardThing1}},
			{Action: guardTag, Resource: kernel.ResourceARN{ARN: guardThing2}},
		}
		if got := a.about(g.runDesc); len(got) != 1 || !samePairs(got[0].Query.Pairs, want) {
			k.Errorf("the Authorizer was asked %+v, want once about %+v", got, want)
		}
	})
}

// An input step 7 refuses reaches no resolver, so WithResourcesFor touches
// nothing and the Authorizer is not asked.
func TestAnInvalidInputResolvesNoScopedResource(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{}
	runGuard(t, g, a, func(k *kerneltest.K) {
		resolved := false
		bindGuard(k, g.run, guardDone, kernel.WithValidator(guardValid),
			kernel.WithResourcesFor([]registry.IAMAction{guardTag}, func(*kernel.Call, *guardInput) []aws.ARN {
				resolved = true
				return []aws.ARN{guardThing1}
			}))
		call := k.Call(g.runDesc)
		if _, err := kerneltest.Dispatch[guardInput, guardOutput](k.Context(), k, call, &guardInput{Invalid: true}); err == nil {
			k.Fatal("an invalid input succeeded")
		}
		if resolved || len(callEvent(k, call).Resources) != 0 || len(a.about(g.runDesc)) != 0 {
			k.Errorf("an invalid input resolved %t, touched %v and asked %d times; want none", resolved, callEvent(k, call).Resources, len(a.about(g.runDesc)))
		}
	})
}
