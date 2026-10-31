package kernel_test

import (
	"context"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// The access record pairs each action with the resources step 8 pairs it
// with (ARCHITECTURE.md 17): a resource the handler creates and touches
// after step 8 asked, such as a key CreateKey mints, is not one the call
// was authorized on, so the record keeps the action on "*", where step 8
// asked it, under every iam.enforce mode (none: no Authorizer).
func TestTheAccessRecordPairsActionsWithStep8sResources(t *testing.T) {
	creates := func(_ context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
		c.Touch(guardThing1)
		return &guardOutput{}, nil
	}
	for _, tc := range []struct {
		name string
		a    kernel.Authorizer
	}{{"no authorizer", nil}, {"authorizer", &fakeAuthorizer{}}} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGuard(guardDenial())
			runGuard(t, g, tc.a, func(k *kerneltest.K) {
				bindGuard(k, g.run, creates)
				if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
					k.Fatalf("call = %s", err.Message)
				}
				if _, ok := useOf(k, guardRun, access.AnyResource{}); !ok {
					k.Errorf("the access record lacks %s on *, where step 8 asked it; it holds %+v", guardRun, k.Access.Usage(access.Filter{}))
				}
				if _, ok := useOf(k, guardRun, access.OnResource{ARN: guardThing1}); ok {
					k.Errorf("the access record holds %s on %s, which the handler touched after step 8", guardRun, guardThing1)
				}
			})
		})
	}
}

// An AuthorizedAfterResolve binding is asked at its first Write, Invoke or
// return, about what the call touched by then; the record pairs it the
// same way, with or without an Authorizer.
func TestTheAccessRecordPairsADeferredActionWithWhatItWasAskedOn(t *testing.T) {
	resolves := func(_ context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
		c.Touch(guardThing1)
		return &guardOutput{}, nil
	}
	for _, tc := range []struct {
		name string
		a    kernel.Authorizer
	}{{"no authorizer", nil}, {"authorizer", &fakeAuthorizer{}}} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGuard(guardDenial())
			runGuard(t, g, tc.a, func(k *kerneltest.K) {
				bindGuard(k, g.run, resolves, kernel.AuthorizedAfterResolve())
				if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
					k.Fatalf("call = %s", err.Message)
				}
				if _, ok := useOf(k, guardRun, access.OnResource{ARN: guardThing1}); !ok {
					k.Errorf("the access record lacks %s on %s, which the call touched before its deferred authorization; it holds %+v", guardRun, guardThing1, k.Access.Usage(access.Filter{}))
				}
			})
		})
	}
}
