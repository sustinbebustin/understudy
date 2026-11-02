package kernel_test

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
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

// A forward-access call knows the service whose call made it, which
// kms:ViaService names; a delivery and an external call know none.
func TestForwardedByNamesTheForwardingService(t *testing.T) {
	g := newGuard(guardDenial())
	runGuard(t, g, nil, func(k *kerneltest.K) {
		type seen struct {
			name string
			ok   bool
		}
		var got []seen
		bindGuard(k, g.probe, func(_ context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
			svc, ok := c.ForwardedBy()
			name := ""
			if ok {
				name = string(svc.ID)
			}
			got = append(got, seen{name, ok})
			return &guardOutput{}, nil
		})
		bindGuard(k, g.run, func(ctx context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
			if _, ok := c.ForwardedBy(); ok {
				k.Error("an external call names a forwarding service")
			}
			for _, as := range []kernel.PrincipalMode{kernel.AsCaller{}, kernel.AsService{Name: "guard.amazonaws.com", Source: guardThing1}} {
				if _, err := kernel.Invoke(ctx, k.Kernel, c, g.probe, kernel.Target{Scope: c.Scope(), As: as, Via: ref.KMSForward}, &guardInput{}); err != nil {
					return nil, err
				}
			}
			return &guardOutput{}, nil
		})
		if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
			k.Fatalf("call = %s", err.Message)
		}
		want := []seen{{"guard", true}, {"", false}}
		if !reflect.DeepEqual(got, want) {
			k.Errorf("ForwardedBy of the AsCaller and AsService calls = %+v, want %+v", got, want)
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

// AuthorizedAfterResolveForDeliveries asks a delivery existence first: a
// delivery to a missing target answers the target's not-found error
// unasked (sns.md 7.1), and one to an existing target is still asked
// before its first Write. Every other call is asked at step 8, deny-first
// (iam-enforcement-surfaces.md 5.3, iam.enforce.missing-resource).
func TestAuthorizedAfterResolveForDeliveriesDefersDeliveriesOnly(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{decide: denyAction(guardProbe, "probe denied")}
	missing := &awserr.Error{ID: awserr.Identity{Shape: "dev.understudy.test#NoSuchThing", QueryCode: "NoSuchThing", JSONType: "NoSuchThing", Status: http.StatusNotFound, Fault: awserr.Sender}, Message: "no such thing"}
	runGuard(t, g, a, func(k *kerneltest.K) {
		p := cells(k)
		ran, wrote := 0, 0
		bindGuard(k, g.probe, func(_ context.Context, c *kernel.Call, in *guardInput) (*guardOutput, *awserr.Error) {
			ran++
			if in.Invalid {
				return nil, missing
			}
			c.Touch(guardThing1)
			if _, err := kernel.Write(c, p, c.Scope(), func(*int, clock.Instant, *kernel.Tx) (int, *awserr.Error) {
				wrote++
				return 0, nil
			}); err != nil {
				return nil, err
			}
			return &guardOutput{}, nil
		}, kernel.AuthorizedAfterResolveForDeliveries())
		results := map[string]*awserr.Error{}
		bindGuard(k, g.run, func(ctx context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
			modes := map[string]kernel.PrincipalMode{
				"service": kernel.AsService{Name: "guard.amazonaws.com", Source: guardThing2},
				"caller":  kernel.AsCaller{},
			}
			for name, as := range modes {
				for _, invalid := range []bool{true, false} {
					key := name + map[bool]string{true: " missing", false: " existing"}[invalid]
					_, results[key] = kernel.Invoke(ctx, k.Kernel, c, g.probe, kernel.Target{Scope: c.Scope(), As: as, Via: ref.SNSDelivery}, &guardInput{Invalid: invalid})
				}
			}
			return &guardOutput{}, nil
		})
		if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
			k.Fatalf("call = %s", err.Message)
		}
		if err := results["service missing"]; err == nil || err.ID != missing.ID {
			k.Errorf("a delivery to a missing target = %+v, want the handler's not-found error", err)
		}
		for _, key := range []string{"service existing", "caller missing", "caller existing"} {
			wantDenial(k, results[key], "probe denied")
		}
		if ran != 2 {
			k.Errorf("the probe handler ran %d times, want twice (the deliveries only)", ran)
		}
		if wrote != 0 {
			k.Errorf("a denied Write ran its closure %d times, want none", wrote)
		}
		if got := len(a.about(g.probeDesc)); got != 3 {
			k.Errorf("the Authorizer was asked about the probe %d times, want 3 (not for the missing delivery target)", got)
		}

		_, err := k.Invoke(k.Context(), g.probe, &guardInput{Invalid: true})
		wantDenial(k, err, "probe denied")
	})
}

// AuthorizeResolved asks a deferred authorization at once, so a handler
// whose next work is neither a Write nor an Invoke, such as running a
// function, does no work for a call it is denied; the call is asked once,
// and a call that owes no deferred question is asked nothing more.
func TestAuthorizeResolvedAsksTheDeferredQuestionNow(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{decide: denyAction(guardProbe, "probe denied")}
	runGuard(t, g, a, func(k *kerneltest.K) {
		worked := 0
		bindGuard(k, g.probe, func(_ context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
			c.Touch(guardThing1)
			if err := kernel.AuthorizeResolved(c); err != nil {
				return nil, err
			}
			worked++
			return &guardOutput{}, nil
		}, kernel.AuthorizedAfterResolve())
		_, err := k.Invoke(k.Context(), g.probe, &guardInput{})
		wantDenial(k, err, "probe denied")
		if worked != 0 {
			k.Errorf("the handler worked %d times after its denial, want none", worked)
		}
		if got := len(a.about(g.probeDesc)); got != 1 {
			k.Errorf("the Authorizer was asked about the probe %d times, want once", got)
		}

		bindGuard(k, g.run, func(_ context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
			if err := kernel.AuthorizeResolved(c); err != nil {
				return nil, err
			}
			return &guardOutput{}, nil
		})
		if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
			k.Errorf("a step-8 call's AuthorizeResolved = %s, want nil", err.Message)
		}
		if got := len(a.about(g.runDesc)); got != 1 {
			k.Errorf("the Authorizer was asked about the step-8 call %d times, want once, at step 8", got)
		}
	})
}
