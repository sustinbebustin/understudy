package kernel_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The kernel side of iam.enforce: an Authorizer that cannot decide, actions scoped
// to the resources they apply to (iam-enforcement-surfaces.md 12.6),
// authorization moved after a handler resolves its resource by name,
// request condition keys a binding supplies, the aws:SourceAccount a
// delivery sends (8.1, 12.4), and the pairs the access record keeps. A fake
// Authorizer stands in for the evaluator, as in authorize_test.go.

// An Authorizer that cannot decide, such as for a stored policy it cannot
// read, ends the call Internal with its cause, never as if no policy
// existed (iam-enforcement-surfaces.md 12.10); the handler does not run.
func TestAnUnevaluableVerdictIsInternal(t *testing.T) {
	g := newGuard(guardDenial())
	unreadable := errors.New("the queue policy of orders does not parse")
	a := &fakeAuthorizer{decide: func(kernel.AuthzQuery) kernel.Verdict { return kernel.Unevaluable{Cause: unreadable} }}
	runGuard(t, g, a, func(k *kerneltest.K) {
		ran := false
		bindGuard(k, g.run, func(context.Context, *kernel.Call, *guardInput) (*guardOutput, *awserr.Error) {
			ran = true
			return &guardOutput{}, nil
		})
		_, err := k.Invoke(k.Context(), g.run, &guardInput{})
		if cause := internalCause(k.T, err); !errors.Is(cause, unreadable) {
			k.Errorf("cause = %v, want the Authorizer's cause %v", cause, unreadable)
		}
		if ran {
			k.Error("the handler ran when the Authorizer could not decide")
		}
	})
}

var (
	srcObject = mustARN("arn:aws:s3:::src-bucket/in/a.txt")
	dstObject = mustARN("arn:aws:s3:::dst-bucket/out/a.txt")
)

// fixed resolves to arns whatever the input.
func fixed(arns ...aws.ARN) func(*kernel.Call, *guardInput) []aws.ARN {
	return func(*kernel.Call, *guardInput) []aws.ARN { return arns }
}

// WithResourcesFor pairs each named action with exactly the resources it
// resolved, so CopyObject asks about s3:GetObject on its source alone, not
// also on its destination (iam-enforcement-surfaces.md 12.6; 8.7 documents
// the source and destination split). An action of the operation's own
// service that resolved none is asked about on any resource; another
// service's action that resolved none is not asked about. Every other
// action keeps the pairing with every touched resource of its service.
func TestWithResourcesForScopesActionsToTheirResources(t *testing.T) {
	cases := []struct {
		name     string
		in       guardInput
		opts     []kernel.BindOption
		wantPair []kernel.Pair
	}{
		{
			name: "a named action pairs with its resources only",
			in:   guardInput{Touch: []aws.ARN{srcObject, dstObject}, Declare: []registry.IAMAction{getObject}},
			opts: []kernel.BindOption{kernel.WithResourcesFor([]registry.IAMAction{getObject}, fixed(srcObject))},
			wantPair: []kernel.Pair{
				{Action: guardRun, Resource: kernel.AnyResource{}},
				{Action: getObject, Resource: kernel.ResourceARN{ARN: srcObject}},
			},
		},
		{
			name: "an own-service action that resolved none is on any resource",
			in:   guardInput{Touch: []aws.ARN{guardThing1}, Declare: []registry.IAMAction{guardTag}},
			opts: []kernel.BindOption{kernel.WithResourcesFor([]registry.IAMAction{guardTag}, fixed())},
			wantPair: []kernel.Pair{
				{Action: guardRun, Resource: kernel.ResourceARN{ARN: guardThing1}},
				{Action: guardTag, Resource: kernel.AnyResource{}},
			},
		},
		{
			name: "another service's action that resolved none is not asked about",
			in:   guardInput{Touch: []aws.ARN{guardRole}, Declare: []registry.IAMAction{passRole}},
			opts: []kernel.BindOption{kernel.WithResourcesFor([]registry.IAMAction{passRole}, fixed())},
			wantPair: []kernel.Pair{
				{Action: guardRun, Resource: kernel.AnyResource{}},
			},
		},
		{
			name: "a resolved resource is touched and paired even if nothing else names it",
			in:   guardInput{Touch: []aws.ARN{guardThing1}, Declare: []registry.IAMAction{guardTag}},
			opts: []kernel.BindOption{kernel.WithResourcesFor([]registry.IAMAction{guardTag}, fixed(guardThing2))},
			wantPair: []kernel.Pair{
				{Action: guardRun, Resource: kernel.ResourceARN{ARN: guardThing1}},
				{Action: guardRun, Resource: kernel.ResourceARN{ARN: guardThing2}},
				{Action: guardTag, Resource: kernel.ResourceARN{ARN: guardThing2}},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newGuard(guardDenial())
			a := &fakeAuthorizer{}
			runGuard(t, g, a, func(k *kerneltest.K) {
				opts := append([]kernel.BindOption{kernel.WithResources(touched), kernel.WithActions(declared)}, c.opts...)
				bindGuard(k, g.run, guardDone, opts...)
				call := k.Call(g.runDesc)
				if _, err := kerneltest.Dispatch[guardInput, guardOutput](k.Context(), k, call, &c.in); err != nil {
					k.Fatalf("an allowed call = %s", err.Message)
				}
				got := a.about(g.runDesc)
				if len(got) != 1 || !samePairs(got[0].Query.Pairs, c.wantPair) {
					k.Errorf("the Authorizer was asked %+v, want once about %+v", got, c.wantPair)
				}
				journaled := callEvent(k, call).Resources
				for _, p := range c.wantPair {
					if r, ok := p.Resource.(kernel.ResourceARN); ok && !slices.Contains(journaled, r.ARN) {
						k.Errorf("journaled resources %v, want %s touched", journaled, r.ARN)
					}
				}
			})
		})
	}
}

// samePairs compares as sets: the order of the scoped pairs is not part of
// what step 8 promises.
func samePairs(got, want []kernel.Pair) bool {
	if len(got) != len(want) {
		return false
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			return false
		}
	}
	return true
}

// The access record pairs as step 8 does, so a policy rendered from it
// grants s3:GetObject on the source's objects and not the destination's
// (iam-enforcement-surfaces.md 12.6; ARCHITECTURE.md 17 folds an object
// into its bucket's objects).
func TestTheAccessRecordKeepsTheScopedPairs(t *testing.T) {
	g := newGuard(guardDenial())
	runGuard(t, g, nil, func(k *kerneltest.K) {
		bindGuard(k, g.run, guardDone, kernel.WithResources(touched), kernel.WithActions(declared),
			kernel.WithResourcesFor([]registry.IAMAction{getObject}, fixed(srcObject)))
		if _, err := k.Invoke(k.Context(), g.run, &guardInput{Touch: []aws.ARN{srcObject, dstObject}, Declare: []registry.IAMAction{getObject}}); err != nil {
			k.Fatalf("call = %s", err.Message)
		}
		if _, ok := useOf(k, getObject, access.BucketObjects{Bucket: mustARN("arn:aws:s3:::src-bucket")}); !ok {
			k.Error("the access record lacks s3:GetObject on the source bucket's objects")
		}
		if u, ok := useOf(k, getObject, access.BucketObjects{Bucket: mustARN("arn:aws:s3:::dst-bucket")}); ok {
			k.Errorf("the access record holds s3:GetObject on the destination bucket's objects: %+v", u)
		}
	})
}

// Every action a handler asks about through kernel.Authorize is recorded
// on the resource it named, whatever the verdict and whether or not an
// Authorizer runs, even an action the operation does not list: a policy
// rendered from the record then grants what the handler asked about, such
// as s3:ListBucket on a missing key (iam-enforcement-surfaces.md 5.1, 12.6).
func TestAuthorizedPairsAreRecorded(t *testing.T) {
	inspect := registry.IAMAction{Prefix: "guard", Name: "Inspect"}
	authorizers := map[string]kernel.Authorizer{"off": nil, "denying": &fakeAuthorizer{decide: denyAction(inspect, "denied")}}
	for name, a := range authorizers {
		t.Run(name, func(t *testing.T) {
			g := newGuard(guardDenial())
			runGuard(t, g, a, func(k *kerneltest.K) {
				bindGuard(k, g.run, func(ctx context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
					if err := kernel.Authorize(ctx, c, inspect, guardThing1, nil); err != nil {
						return nil, err
					}
					return &guardOutput{}, nil
				})
				_, _ = k.Invoke(k.Context(), g.run, &guardInput{})
				if u, ok := useOf(k, inspect, access.OnResource{ARN: guardThing1}); !ok || u.Calls != 1 {
					k.Errorf("access record of guard:Inspect on the thing = %+v (found %t), want one call", u, ok)
				}
			})
		})
	}
}

// AuthorizedAfterResolve: step 8 asks nothing before the handler; the
// kernel asks about the binding's pairs over the resources the call
// touched at its first Write, and a denial is what that Write returns,
// before its closure runs. It asks once per call.
func TestAuthorizedAfterResolveAsksAtTheFirstWrite(t *testing.T) {
	g := newGuard(guardDenial())
	const message = "User: u is not authorized to perform: guard:Run on resource: thing/one"
	a := &fakeAuthorizer{decide: denyResource(guardThing1, message)}
	runGuard(t, g, a, func(k *kerneltest.K) {
		p := cells(k)
		wrote, askedBefore := 0, -1
		bindGuard(k, g.run, func(_ context.Context, c *kernel.Call, in *guardInput) (*guardOutput, *awserr.Error) {
			askedBefore = len(a.questions())
			c.Touch(in.Touch...)
			for range 2 {
				if _, err := kernel.Write(c, p, c.Scope(), func(*int, clock.Instant, *kernel.Tx) (int, *awserr.Error) {
					wrote++
					return 0, nil
				}); err != nil {
					return nil, err
				}
			}
			return &guardOutput{}, nil
		}, kernel.AuthorizedAfterResolve())

		_, err := k.Invoke(k.Context(), g.run, &guardInput{Touch: []aws.ARN{guardThing1}})
		wantDenial(k, err, message)
		if askedBefore != 0 {
			k.Errorf("the Authorizer was asked %d times before the handler, want none", askedBefore)
		}
		if wrote != 0 {
			k.Errorf("the denied Write ran its closure %d times, want none", wrote)
		}
		want := []kernel.Pair{{Action: guardRun, Resource: kernel.ResourceARN{ARN: guardThing1}}}
		if got := a.about(g.runDesc); len(got) != 1 || !reflect.DeepEqual(got[0].Query.Pairs, want) {
			k.Errorf("the Authorizer was asked %+v, want once about %+v", got, want)
		}

		// Allowed: both writes run, and the kernel asked once.
		_, err = k.Invoke(k.Context(), g.run, &guardInput{Touch: []aws.ARN{guardThing2}})
		if err != nil {
			k.Fatalf("an allowed call = %s", err.Message)
		}
		if wrote != 2 {
			k.Errorf("an allowed call wrote %d times, want 2", wrote)
		}
		if got := len(a.about(g.runDesc)); got != 2 {
			k.Errorf("the Authorizer was asked %d times over two calls, want once per call", got)
		}
	})
}

// With no Write or Invoke, the kernel asks at the handler's successful
// return, and a denial replaces the output. A handler error before any of
// them is answered as the handler returned it, unauthorized: existence
// first (switch iam.enforce.missing-resource).
func TestAuthorizedAfterResolveAsksAtReturnAndAnswersExistenceFirst(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{decide: denyAll("denied")}
	missing := &awserr.Error{ID: awserr.Identity{Shape: "dev.understudy.test#NoSuchThing", QueryCode: "NoSuchThing", JSONType: "NoSuchThing", Status: http.StatusNotFound, Fault: awserr.Sender}, Message: "no such thing"}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.run, func(_ context.Context, c *kernel.Call, in *guardInput) (*guardOutput, *awserr.Error) {
			if in.Invalid {
				return nil, missing
			}
			c.Touch(in.Touch...)
			return &guardOutput{}, nil
		}, kernel.AuthorizedAfterResolve())

		_, err := k.Invoke(k.Context(), g.run, &guardInput{Touch: []aws.ARN{guardThing1}})
		wantDenial(k, err, "denied")

		_, err = k.Invoke(k.Context(), g.run, &guardInput{Invalid: true})
		if err == nil || err.ID != missing.ID {
			k.Errorf("a handler error before authorization = %+v, want the handler's own error", err)
		}
		if got := len(a.about(g.runDesc)); got != 1 {
			k.Errorf("the Authorizer was asked %d times, want once (not for the handler error)", got)
		}
	})
}

// ResolveFirst answers a call whose resource does not exist with the
// check's not-found error before step 8 asks anything, and the handler
// does not run; a call whose check passes is asked at step 8 as usual. With
// authorization off the check does not run and the handler answers
// (iam-enforcement-surfaces.md 5.3, IAM-ENF-14).
func TestResolveFirstAnswersNotFoundBeforeAuthorization(t *testing.T) {
	g := newGuard(guardDenial())
	missing := &awserr.Error{ID: awserr.Identity{Shape: "dev.understudy.test#NoSuchThing", QueryCode: "NoSuchThing", JSONType: "NoSuchThing", Status: http.StatusNotFound, Fault: awserr.Sender}, Message: "no such thing"}
	exists := kernel.ResolveFirst(func(_ context.Context, _ *kernel.Call, in *guardInput) *awserr.Error {
		if in.Member == "missing" {
			return missing
		}
		return nil
	})
	t.Run("enforced", func(t *testing.T) {
		a := &fakeAuthorizer{decide: denyAll("denied")}
		runGuard(t, g, a, func(k *kerneltest.K) {
			ran := 0
			bindGuard(k, g.run, func(context.Context, *kernel.Call, *guardInput) (*guardOutput, *awserr.Error) {
				ran++
				return &guardOutput{}, nil
			}, kernel.WithResources(touched), exists)

			_, err := k.Invoke(k.Context(), g.run, &guardInput{Member: "missing", Touch: []aws.ARN{guardThing1}})
			if err == nil || err.ID != missing.ID {
				k.Errorf("a call on a missing resource = %+v, want the check's not-found error", err)
			}
			if got := len(a.about(g.runDesc)); got != 0 {
				k.Errorf("the Authorizer was asked %d times about a missing resource, want never", got)
			}

			_, err = k.Invoke(k.Context(), g.run, &guardInput{Touch: []aws.ARN{guardThing1}})
			wantDenial(k, err, "denied")
			if ran != 0 {
				k.Errorf("the handler ran %d times, want never", ran)
			}
			if got := k.Kernel.Bindings().ResolvedFirstOps(g.svc); !slices.Equal(got, []string{g.runDesc.ID.String()}) {
				k.Errorf("ResolvedFirstOps = %v, want the ResolveFirst binding", got)
			}
		})
	})
	t.Run("off", func(t *testing.T) {
		runGuard(t, g, nil, func(k *kerneltest.K) {
			checked := false
			bindGuard(k, g.run, guardDone, kernel.ResolveFirst(func(context.Context, *kernel.Call, *guardInput) *awserr.Error {
				checked = true
				return missing
			}))
			if _, err := k.Invoke(k.Context(), g.run, &guardInput{Member: "missing"}); err != nil {
				k.Errorf("a call with authorization off = %s, want the handler's answer", err.Message)
			}
			if checked {
				k.Error("the check ran with authorization off")
			}
		})
	})
}

// The first Invoke asks before it calls: a denial is what Invoke returns
// and the invoked handler does not run.
func TestAuthorizedAfterResolveAsksAtTheFirstInvoke(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{decide: denyAction(guardRun, "denied")}
	runGuard(t, g, a, func(k *kerneltest.K) {
		probed := false
		bindGuard(k, g.probe, func(context.Context, *kernel.Call, *guardInput) (*guardOutput, *awserr.Error) {
			probed = true
			return &guardOutput{}, nil
		})
		ran, askedBefore := false, -1
		bindGuard(k, g.run, func(ctx context.Context, c *kernel.Call, in *guardInput) (*guardOutput, *awserr.Error) {
			ran, askedBefore = true, len(a.questions())
			c.Touch(in.Touch...)
			return kernel.Invoke(ctx, k.Kernel, c, g.probe, kernel.Target{Scope: c.Scope(), As: kernel.AsCaller{}, Via: ref.SNSDelivery}, &guardInput{})
		}, kernel.AuthorizedAfterResolve())
		_, err := k.Invoke(k.Context(), g.run, &guardInput{Touch: []aws.ARN{guardThing1}})
		wantDenial(k, err, "denied")
		if !ran || askedBefore != 0 {
			k.Errorf("handler ran %t after %d questions, want it run with none asked before", ran, askedBefore)
		}
		if probed {
			k.Error("the invoked handler ran after the denial")
		}
	})
}

// WithConditionKeys: step 8 passes the union of every option's keys as
// AuthzQuery.Keys, a later option replacing an earlier one key by key; a
// key with no values is one the request does not carry.
func TestWithConditionKeysReachStep8(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.run, guardDone,
			kernel.WithConditionKeys(func(*kernel.Call, *guardInput) kernel.ConditionKeys {
				return kernel.ConditionKeys{"sts:ExternalId": {"ext-1"}, "aws:TagKeys": {"team"}}
			}),
			kernel.WithConditionKeys(func(*kernel.Call, *guardInput) kernel.ConditionKeys {
				return kernel.ConditionKeys{"aws:TagKeys": {"team", "env"}, "sts:SourceIdentity": {}}
			}))
		if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
			k.Fatalf("call = %s", err.Message)
		}
		want := kernel.ConditionKeys{"sts:ExternalId": {"ext-1"}, "aws:TagKeys": {"team", "env"}, "sts:SourceIdentity": {}}
		if got := a.about(g.runDesc); len(got) != 1 || !reflect.DeepEqual(got[0].Query.Keys, want) {
			k.Errorf("the Authorizer was asked %+v, want Keys %v", got, want)
		}
	})
}

// WithFixedConditionKeys fits a binding of any input type and joins the
// WithConditionKeys union in option order, a later option replacing an
// earlier one key by key.
func TestWithFixedConditionKeysJoinStep8sKeys(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.run, guardDone,
			kernel.WithFixedConditionKeys(kernel.ConditionKeys{"guard:ApiVersion": {"1"}, "guard:Shared": {"fixed"}}),
			kernel.WithConditionKeys(func(*kernel.Call, *guardInput) kernel.ConditionKeys {
				return kernel.ConditionKeys{"guard:Shared": {"input"}}
			}))
		if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
			k.Fatalf("call = %s", err.Message)
		}
		want := kernel.ConditionKeys{"guard:ApiVersion": {"1"}, "guard:Shared": {"input"}}
		if got := a.about(g.runDesc); len(got) != 1 || !reflect.DeepEqual(got[0].Query.Keys, want) {
			k.Errorf("the Authorizer was asked %+v, want Keys %v", got, want)
		}
	})
}

// WithConditionKeysFor: step 8 passes the keys as AuthzQuery.ActionKeys
// under each named action, so only that action's pairs see them (over
// Keys, which every pair sees), and an action it does not name sees Keys
// alone.
func TestWithConditionKeysForReachOnlyTheNamedActions(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.run, guardDone, kernel.WithActions(declared),
			kernel.WithConditionKeys(func(*kernel.Call, *guardInput) kernel.ConditionKeys {
				return kernel.ConditionKeys{"sts:ExternalId": {"ext-1"}, "guard:Shared": {"all"}}
			}),
			kernel.WithConditionKeysFor([]registry.IAMAction{guardTag}, func(*kernel.Call, *guardInput) kernel.ConditionKeys {
				return kernel.ConditionKeys{"guard:Shared": {"tag"}, "guard:Write": {}}
			}))
		if _, err := k.Invoke(k.Context(), g.run, &guardInput{Declare: []registry.IAMAction{guardTag}}); err != nil {
			k.Fatalf("call = %s", err.Message)
		}
		got := a.about(g.runDesc)
		if len(got) != 1 {
			k.Fatalf("the Authorizer was asked %+v, want once", got)
		}
		q := got[0].Query
		wantAll := kernel.ConditionKeys{"sts:ExternalId": {"ext-1"}, "guard:Shared": {"all"}}
		if !reflect.DeepEqual(q.Keys, wantAll) {
			k.Errorf("Keys = %v, want %v", q.Keys, wantAll)
		}
		if got := q.KeysFor(guardRun); !reflect.DeepEqual(got, wantAll) {
			k.Errorf("KeysFor(%s) = %v, want %v", guardRun, got, wantAll)
		}
		wantTag := kernel.ConditionKeys{"sts:ExternalId": {"ext-1"}, "guard:Shared": {"tag"}, "guard:Write": {}}
		if got := q.KeysFor(guardTag); !reflect.DeepEqual(got, wantTag) {
			k.Errorf("KeysFor(%s) = %v, want %v", guardTag, got, wantTag)
		}
	})
}

// A delivery's aws:SourceAccount (zero when it sends none) rides on the
// call's mode to the access record, which reports it beside the source
// (iam-enforcement-surfaces.md 8.1, 12.4, 12.6).
func TestADeliverysSourceAccountReachesTheAccessRecord(t *testing.T) {
	topic := mustARN("arn:aws:sns:us-east-1:000000000000:topic")
	acct, err := aws.ParseAccountID("000000000000")
	if err != nil {
		t.Fatal(err)
	}
	for _, sourceAccount := range []aws.AccountID{acct, {}} {
		t.Run("source account "+sourceAccount.String(), func(t *testing.T) {
			g := newGuard(guardDenial())
			runGuard(t, g, nil, func(k *kerneltest.K) {
				bindGuard(k, g.probe, guardDone, kernel.WithResources(touched))
				bindGuard(k, g.run, func(ctx context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
					as := kernel.AsService{Name: "guard.amazonaws.com", Source: topic, SourceAccount: sourceAccount}
					return kernel.Invoke(ctx, k.Kernel, c, g.probe, kernel.Target{Scope: c.Scope(), As: as, Via: ref.SNSDelivery}, &guardInput{Touch: []aws.ARN{guardThing1}})
				})
				if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
					k.Fatalf("call = %s", err.Message)
				}
				want := access.Requirement{
					Target: guardThing1, Principal: access.ServicePrincipal{Name: "guard.amazonaws.com"},
					Action: guardProbe, Source: topic, SourceAccount: sourceAccount,
				}
				if got := k.Access.ResourceRequirements(access.Filter{}); len(got) != 1 || got[0] != want {
					k.Errorf("ResourceRequirements = %+v, want exactly %+v", got, want)
				}
				uses := k.Access.Usage(access.Filter{Principal: access.ServicePrincipal{Name: "guard.amazonaws.com"}})
				if len(uses) != 1 || uses[0].Mode != (access.AsService{Source: topic, SourceAccount: sourceAccount}) {
					k.Errorf("delivery uses = %+v, want one AsService use carrying the source account", uses)
				}
			})
		})
	}
}
