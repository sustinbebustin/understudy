package kernel_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Step 8 behavior the specification tests leave open, on the same guard
// service (authorize_test.go).

// An Authorizer that answers no verdict is a bug in it: the call answers
// 500 with a VerdictError naming the operation, and the handler does not
// run, since the kernel cannot tell whether the call may go on.
func TestANilVerdictIsInternal(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{decide: func(kernel.AuthzQuery) kernel.Verdict { return nil }}
	runGuard(t, g, a, func(k *kerneltest.K) {
		ran := false
		bindGuard(k, g.run, func(context.Context, *kernel.Call, *guardInput) (*guardOutput, *awserr.Error) {
			ran = true
			return &guardOutput{}, nil
		})
		_, err := k.Invoke(k.Context(), g.run, &guardInput{})
		if v, ok := errors.AsType[*kernel.VerdictError](internalCause(k.T, err)); !ok || v.Op != g.runDesc.ID {
			k.Errorf("cause = %v, want a VerdictError of %s", v, g.runDesc.ID)
		}
		if ran {
			k.Error("the handler ran without a verdict")
		}
	})
}

// A denied call never reaches its handler, so an after-commit rule, which
// fires only after a handler succeeded, takes no hit from it either.
func TestADeniedCallSpendsNoAfterCommitBudget(t *testing.T) {
	g := newGuard(guardDenial())
	broken := awserr.Identity{Shape: "com.example.guard#Broken", JSONType: "Broken", QueryCode: "Broken", Status: 500, Fault: awserr.Receiver}
	g.runDesc.Errors = []awserr.Identity{broken}
	a := &fakeAuthorizer{decide: denyAll("denied")}
	runGuard(t, g, a, func(k *kerneltest.K) {
		rule, ferr := k.Faults.Add(fault.Rule{
			Match: fault.Match{Operations: []ref.OperationID{g.runDesc.ID}}, Phase: fault.Request,
			Action: fault.AfterCommit{Shape: broken.Shape}, Trigger: fault.Always{},
		})
		if ferr != nil {
			k.Fatal(ferr)
		}
		bindGuard(k, g.run, guardDone)
		_, err := k.Invoke(k.Context(), g.run, &guardInput{})
		wantDenial(k, err, "denied")
		for _, r := range k.Faults.List() {
			if r.Rule.ID == rule && r.Hits != 0 {
				k.Errorf("the after-commit rule took %d hits from a denied call, want 0", r.Hits)
			}
		}
	})
}

// An unbound operation answers 501 before step 8: there is no binding to
// say what the call needs, and AWS has no such operation to authorize.
func TestStep8DoesNotAskAboutAnUnboundOperation(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{decide: denyAll("denied")}
	runGuard(t, g, a, func(k *kerneltest.K) {
		_, err := k.Invoke(k.Context(), g.run, &guardInput{})
		if err == nil || err.ID.Status != 501 {
			k.Fatalf("an unbound call = %+v, want 501", err)
		}
		if got := a.questions(); len(got) != 0 {
			k.Errorf("the Authorizer was asked %+v about an unbound operation", got)
		}
	})
}

// Actions a service declares apart from the bind site (kernel.Declare) are
// asked about like those of the bind site; WithoutSecondaryActions leaves
// the primary alone. Each action of the guard service pairs with the thing
// the call touches.
func TestStep8AsksAboutActionsDeclaredApartFromTheBindSite(t *testing.T) {
	cases := []struct {
		name    string
		declare kernel.BindOption
		want    []kernel.Pair
	}{
		{
			name: "WithActions", declare: kernel.WithActions(declared),
			want: []kernel.Pair{
				{Action: guardRun, Resource: kernel.ResourceARN{ARN: guardThing1}},
				{Action: guardTag, Resource: kernel.ResourceARN{ARN: guardThing1}},
			},
		},
		{
			name: "WithoutSecondaryActions", declare: kernel.WithoutSecondaryActions(),
			want: []kernel.Pair{{Action: guardRun, Resource: kernel.ResourceARN{ARN: guardThing1}}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newGuard(guardDenial())
			a := &fakeAuthorizer{}
			runGuard(t, g, a, func(k *kerneltest.K) {
				bindGuard(k, g.run, guardDone, kernel.WithResources(touched))
				kernel.Declare(k.Kernel.Binder(g.svc), g.run, c.declare)
				if err := k.Kernel.BindErr(); err != nil {
					k.Fatal(err)
				}
				in := &guardInput{Touch: []aws.ARN{guardThing1}, Declare: []registry.IAMAction{guardTag}}
				if _, err := k.Invoke(k.Context(), g.run, in); err != nil {
					k.Fatalf("an allowed call = %s", err.Message)
				}
				if got := a.about(g.runDesc); len(got) != 1 || !reflect.DeepEqual(got[0].Query.Pairs, c.want) {
					k.Errorf("the Authorizer was asked %+v, want once about %+v", got, c.want)
				}
			})
		})
	}
}

// A modeled operation whose generated list is empty, with nothing
// declared, needs no pair, so step 8 asks nothing and the call goes on.
func TestStep8DoesNotAskAboutACallWithNoPair(t *testing.T) {
	svc := &registry.Service{ID: "bare", SigningName: "bare", IAMPrefix: "bare", Denial: guardDenial()}
	desc := &registry.Operation{Service: svc, ID: ref.OperationID{Service: "bare", Name: "Ping"}, Kind: registry.Modeled}
	svc.Operations = []*registry.Operation{desc}
	ping := kernel.NewOp[guardInput, guardOutput](desc)
	a := &fakeAuthorizer{decide: denyAll("denied")}
	kerneltest.Run(t, func(k *kerneltest.K) {
		bindGuard(k, ping, guardDone)
		if _, err := k.Invoke(k.Context(), ping, &guardInput{}); err != nil {
			k.Errorf("a call with no pair = %s, want it never asked about", err.Message)
		}
		if got := a.questions(); len(got) != 0 {
			k.Errorf("the Authorizer was asked %+v about a call with no pair", got)
		}
	}, kerneltest.Services(svc), kerneltest.Authorizer(a))
}

// A denied delivery is access-recorded like any failed call: under the
// service principal, in AsService mode with its source, as a failure with
// the denial's code (ARCHITECTURE.md 17).
func TestADeniedDeliveryIsRecordedAsAFailedUseOfTheServicePrincipal(t *testing.T) {
	source := mustARN("arn:aws:sns:us-east-1:000000000000:topic")
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{decide: denyAction(guardProbe, "probe denied")}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.probe, guardDone, kernel.WithResources(touched))
		bindGuard(k, g.run, func(ctx context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
			as := kernel.AsService{Name: "sns.amazonaws.com", Source: source}
			return kernel.Invoke(ctx, k.Kernel, c, g.probe, kernel.Target{Scope: c.Scope(), As: as, Via: ref.SNSDelivery}, &guardInput{Touch: []aws.ARN{guardThing1}})
		})
		_, err := k.Invoke(k.Context(), g.run, &guardInput{})
		wantDenial(k, err, "probe denied")

		var got []access.Use
		for _, u := range k.Access.Usage(access.Filter{}) {
			if u.Action == guardProbe {
				got = append(got, u)
			}
		}
		want := access.Use{
			Principal: access.ServicePrincipal{Name: "sns.amazonaws.com"}, Mode: access.AsService{Source: source},
			Action: guardProbe, Resource: access.OnResource{ARN: guardThing1}, Calls: 1, Failures: 1,
		}
		if len(got) != 1 {
			k.Fatalf("access records of guard:Probe = %+v, want one", got)
		}
		got[0].First, got[0].Last = want.First, want.Last
		if !reflect.DeepEqual(got[0], want) {
			k.Errorf("access record of guard:Probe = %+v, want %+v", got[0], want)
		}
	})
}
