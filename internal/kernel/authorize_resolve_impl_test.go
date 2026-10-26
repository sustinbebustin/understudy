package kernel_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// AuthorizedAfterResolve and WithConditionKeys beyond the blind tests in
// authorize_resolve_test.go: the deferred question carries the binding's
// condition keys, Declare keeps both options, the last authorization
// option applies, and with no Authorizer nothing is deferred.

// touchThing1 is a handler that resolves thing/one and succeeds.
func touchThing1(_ context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
	c.Touch(guardThing1)
	return &guardOutput{}, nil
}

func externalID(*kernel.Call, *guardInput) kernel.ConditionKeys {
	return kernel.ConditionKeys{"sts:ExternalId": {"ext-1"}}
}

// The deferred question asks about the resolved resource with the keys the
// binding supplies, as step 8 would have.
func TestAuthorizedAfterResolveCarriesTheConditionKeys(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.run, touchThing1, kernel.AuthorizedAfterResolve(), kernel.WithConditionKeys(externalID))
		if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
			k.Fatalf("call = %s", err.Message)
		}
		got := a.about(g.runDesc)
		wantPairs := []kernel.Pair{{Action: guardRun, Resource: kernel.ResourceARN{ARN: guardThing1}}}
		if len(got) != 1 || !reflect.DeepEqual(got[0].Query.Pairs, wantPairs) || !reflect.DeepEqual(got[0].Query.Keys, externalID(nil, nil)) {
			k.Errorf("the Authorizer was asked %+v, want once about %+v with %v", got, wantPairs, externalID(nil, nil))
		}
	})
}

// Declare after the bind site keeps the binding's AuthorizedAfterResolve
// and adds its condition keys, as WithActions are added.
func TestDeclareKeepsAfterResolveAndAddsConditionKeys(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.run, touchThing1, kernel.AuthorizedAfterResolve())
		kernel.Declare(k.Kernel.Binder(g.svc), g.run, kernel.WithConditionKeys(externalID))
		if err := k.Kernel.BindErr(); err != nil {
			k.Fatal(err)
		}
		if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
			k.Fatalf("call = %s", err.Message)
		}
		got := a.about(g.runDesc)
		if len(got) != 1 || !reflect.DeepEqual(got[0].Query.Keys, externalID(nil, nil)) {
			k.Fatalf("the Authorizer was asked %+v, want once with the declared keys", got)
		}
		if r, ok := got[0].Query.Pairs[0].Resource.(kernel.ResourceARN); !ok || r.ARN != guardThing1 {
			k.Errorf("pairs = %+v, want the resolved thing (asked after the handler)", got[0].Query.Pairs)
		}
	})
}

// With AuthorizedAfterResolve then AuthorizedInHandler the last applies:
// step 8 asks about the other actions before the handler and nothing is
// deferred.
func TestTheLastAuthorizationOptionApplies(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.run, touchThing1, kernel.AuthorizedAfterResolve(), kernel.AuthorizedInHandler())
		if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
			k.Fatalf("call = %s", err.Message)
		}
		for _, q := range a.about(g.runDesc) {
			for _, p := range q.Query.Pairs {
				if p.Action == guardRun {
					k.Errorf("asked about the primary action %+v, want it left to the handler (AuthorizedInHandler applies)", p)
				}
			}
		}
	})
}

// With authorization off, an AuthorizedAfterResolve call writes and returns
// as any other call, and the journal lists what it resolved.
func TestAuthorizedAfterResolveWithoutAnAuthorizer(t *testing.T) {
	g := newGuard(guardDenial())
	runGuard(t, g, nil, func(k *kerneltest.K) {
		p := cells(k)
		wrote := 0
		bindGuard(k, g.run, func(_ context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
			c.Touch(guardThing1)
			if _, err := kernel.Write(c, p, c.Scope(), func(*int, clock.Instant, *kernel.Tx) (int, *awserr.Error) {
				wrote++
				return 0, nil
			}); err != nil {
				return nil, err
			}
			return &guardOutput{}, nil
		}, kernel.AuthorizedAfterResolve())
		call := k.Call(g.runDesc)
		if _, err := kerneltest.Dispatch[guardInput, guardOutput](k.Context(), k, call, &guardInput{}); err != nil {
			k.Fatalf("call with authorization off = %s", err.Message)
		}
		if wrote != 1 {
			k.Errorf("the Write ran %d times, want once", wrote)
		}
		if got := callEvent(k, call).Resources; !reflect.DeepEqual(got, []aws.ARN{guardThing1}) {
			k.Errorf("journaled resources = %v, want the resolved thing", got)
		}
	})
}
