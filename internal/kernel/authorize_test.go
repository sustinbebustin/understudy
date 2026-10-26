package kernel_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The kernel side of IAM enforcement (iam-enforcement-surfaces.md 12.1):
// pipeline step 8 asks the Authorizer after validation and before the
// request-phase fault rules, and kernel.Authorize asks it for a resource a
// handler resolved itself. A fake Authorizer stands in for 5b's evaluator,
// so these tests pin what the kernel asks and what it does with the
// answer, never a policy decision.

// guardRefused is the guard service's denial identity. Its fields are
// arbitrary: the kernel copies a service's identity, whatever it holds
// (iam-enforcement-surfaces.md 12.3 gives each service its own).
var guardRefused = awserr.Identity{
	Shape: "dev.understudy.test#Refused", JSONType: "Refused", QueryCode: "Refused",
	Status: http.StatusForbidden, Fault: awserr.Sender, MessageKey: awserr.MessageLower,
}

var (
	guardRun   = registry.IAMAction{Prefix: "guard", Name: "Run"}
	guardTag   = registry.IAMAction{Prefix: "guard", Name: "TagResource"}
	guardProbe = registry.IAMAction{Prefix: "guard", Name: "Probe"}
	guardFetch = registry.IAMAction{Prefix: "guard", Name: "Fetch"}
	guardSweep = registry.IAMAction{Prefix: "guard", Name: "Sweep"}
	passRole   = registry.IAMAction{Prefix: "iam", Name: "PassRole"}
	assumeRole = registry.IAMAction{Prefix: "sts", Name: "AssumeRole"}
	getObject  = registry.IAMAction{Prefix: "s3", Name: "GetObject"}
	olRun      = registry.IAMAction{Prefix: "s3-object-lambda", Name: "Run"}

	guardRole   = mustARN("arn:aws:iam::000000000000:role/runner")
	guardThing1 = mustARN("arn:aws:guard:us-east-1:000000000000:thing/one")
	guardThing2 = mustARN("arn:aws:guard:us-east-1:000000000000:thing/two")
	guardObject = mustARN("arn:aws:s3:::bucket/in/key.txt")
)

func mustARN(s string) aws.ARN {
	a, err := aws.ParseARN(s)
	if err != nil {
		panic(err)
	}
	return a
}

// guardInput names what a call touches and which actions its binding
// declares, so one binding serves every pairing case.
type guardInput struct {
	Touch   []aws.ARN
	Declare []registry.IAMAction
	Invalid bool
}

type guardOutput struct{}

// guard is a test service with a denial identity. Run lists a primary
// action, a same-service secondary, an IAM action, an STS action, an S3
// action and one of a service no call touches; Probe is what Run invokes;
// Fetch is bound passthrough (BindRaw); Stream is raw HTTP; Sweep is
// internal and lists nothing.
type guard struct {
	svc          *registry.Service
	run, probe   kernel.Op[guardInput, guardOutput]
	sweep        kernel.Op[guardInput, guardOutput]
	fetch        kernel.Op[guardInput, kernel.RawOutput]
	stream       *registry.Operation
	runDesc      *registry.Operation
	fetchDesc    *registry.Operation
	probeDesc    *registry.Operation
	sweepDesc    *registry.Operation
	streamAction registry.IAMAction
}

func newGuard(denial registry.Denial) guard {
	svc := &registry.Service{ID: "guard", SigningName: "guard", IAMPrefix: "guard", Denial: denial}
	id := func(name string) ref.OperationID { return ref.OperationID{Service: "guard", Name: name} }
	stream := registry.IAMAction{Prefix: "guard", Name: "Stream"}
	fetch := &registry.Operation{Service: svc, ID: id("Fetch"), Ordinal: 0, Kind: registry.Modeled, IAM: []registry.IAMAction{guardFetch}}
	probe := &registry.Operation{Service: svc, ID: id("Probe"), Ordinal: 1, Kind: registry.Modeled, IAM: []registry.IAMAction{guardProbe}}
	run := &registry.Operation{Service: svc, ID: id("Run"), Ordinal: 2, Kind: registry.Modeled, IAM: []registry.IAMAction{
		guardRun, guardTag, passRole, assumeRole, getObject, olRun,
	}}
	raw := &registry.Operation{Service: svc, ID: id("Stream"), Ordinal: 3, Kind: registry.Raw, IAM: []registry.IAMAction{stream}}
	sweep := &registry.Operation{Service: svc, ID: id("Sweep"), Ordinal: 4, Kind: registry.Internal}
	svc.Operations = []*registry.Operation{fetch, probe, run, raw, sweep}
	return guard{
		svc: svc,
		run: kernel.NewOp[guardInput, guardOutput](run), probe: kernel.NewOp[guardInput, guardOutput](probe),
		sweep: kernel.NewOp[guardInput, guardOutput](sweep), fetch: kernel.NewOp[guardInput, kernel.RawOutput](fetch),
		stream: raw, runDesc: run, fetchDesc: fetch, probeDesc: probe, sweepDesc: sweep, streamAction: stream,
	}
}

// guardDenial is the guard service's descriptor entry: the identity and an
// evidence label, as generated descriptors carry them.
func guardDenial() registry.Denial {
	return registry.Denial{Identity: guardRefused, Label: evidence.Inferred}
}

// touched names the resources an input lists.
func touched(_ *kernel.Call, in *guardInput) []aws.ARN { return in.Touch }

// declared declares the actions an input lists.
func declared(_ *kernel.Call, in *guardInput) []registry.IAMAction { return in.Declare }

// guardValid refuses an input marked invalid, at validation (step 7).
func guardValid(in *guardInput) []awserr.Violation {
	if in.Invalid {
		return []awserr.Violation{{Path: awserr.Path{"invalid"}, Value: awserr.RenderedText{Text: "true"}, Constraint: awserr.RangeMax{Max: "0"}}}
	}
	return nil
}

func guardDone(context.Context, *kernel.Call, *guardInput) (*guardOutput, *awserr.Error) {
	return &guardOutput{}, nil
}

// asked is one question the fake Authorizer received.
type asked struct {
	Op    ref.OperationID
	Mode  kernel.PrincipalMode
	Query kernel.AuthzQuery
}

// fakeAuthorizer records every question and answers with decide, Allowed
// when decide is nil.
type fakeAuthorizer struct {
	mu     sync.Mutex
	asked  []asked
	decide func(q kernel.AuthzQuery) kernel.Verdict
}

func (f *fakeAuthorizer) Authorize(_ context.Context, c *kernel.Call, q kernel.AuthzQuery) kernel.Verdict {
	f.mu.Lock()
	f.asked = append(f.asked, asked{Op: c.Op().ID, Mode: c.Mode(), Query: q})
	decide := f.decide
	f.mu.Unlock()
	if decide == nil {
		return kernel.Allowed{}
	}
	return decide(q)
}

func (f *fakeAuthorizer) questions() []asked {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

// about returns the questions asked about op.
func (f *fakeAuthorizer) about(op *registry.Operation) []asked {
	var out []asked
	for _, a := range f.questions() {
		if a.Op == op.ID {
			out = append(out, a)
		}
	}
	return out
}

// denyAll denies every question with message.
func denyAll(message string) func(kernel.AuthzQuery) kernel.Verdict {
	return func(kernel.AuthzQuery) kernel.Verdict { return kernel.Denied{Message: message} }
}

// denyAction denies a question holding a pair of action.
func denyAction(action registry.IAMAction, message string) func(kernel.AuthzQuery) kernel.Verdict {
	return func(q kernel.AuthzQuery) kernel.Verdict {
		for _, p := range q.Pairs {
			if p.Action == action {
				return kernel.Denied{Message: message}
			}
		}
		return kernel.Allowed{}
	}
}

// denyResource denies a question holding a pair on resource.
func denyResource(resource aws.ARN, message string) func(kernel.AuthzQuery) kernel.Verdict {
	return func(q kernel.AuthzQuery) kernel.Verdict {
		for _, p := range q.Pairs {
			if r, ok := p.Resource.(kernel.ResourceARN); ok && r.ARN == resource {
				return kernel.Denied{Message: message}
			}
		}
		return kernel.Allowed{}
	}
}

// runGuard runs fn against a kernel serving g, with a as its Authorizer
// unless a is nil.
func runGuard(t *testing.T, g guard, a kernel.Authorizer, fn func(k *kerneltest.K)) {
	t.Helper()
	opts := []kerneltest.Option{kerneltest.Services(g.svc)}
	if a != nil {
		opts = append(opts, kerneltest.Authorizer(a))
	}
	kerneltest.Run(t, fn, opts...)
}

func bindGuard[Out any](k *kerneltest.K, op kernel.Op[guardInput, Out], h kernel.Handler[guardInput, Out], opts ...kernel.BindOption) {
	k.Helper()
	kernel.Bind(k.Kernel.Binder(op.Descriptor().Service), op, h, registry.Full{}, opts...)
	if err := k.Kernel.BindErr(); err != nil {
		k.Fatalf("bind %s: %v", op.Descriptor().ID, err)
	}
}

// callEvent returns the journaled API call of c. Trace roots its tree at
// the trace's root event, so an internal call's own event is found among
// the root's descendants.
func callEvent(k *kerneltest.K, c *kernel.Call) journal.APICall {
	k.Helper()
	tree, ok := k.Journal.Trace(c.Event())
	if !ok {
		k.Fatalf("call %d is not journaled", c.Event())
	}
	for pending := []*journal.Tree{tree}; len(pending) > 0; {
		t := pending[0]
		pending = append(pending[1:], t.Children...)
		if t.Event.ID != c.Event() {
			continue
		}
		call, ok := t.Event.Detail.(journal.APICall)
		if !ok {
			k.Fatalf("event of call %d = %T, want an APICall", c.Event(), t.Event.Detail)
		}
		return call
	}
	k.Fatalf("call %d is not in its own trace", c.Event())
	return journal.APICall{}
}

// useOf returns the access record of action on scope.
func useOf(k *kerneltest.K, action registry.IAMAction, scope access.Scope) (access.Use, bool) {
	uses := k.Access.Usage(access.Filter{})
	for i := range uses {
		if uses[i].Action == action && reflect.DeepEqual(uses[i].Resource, scope) {
			return uses[i], true
		}
	}
	return access.Use{}, false
}

// wantDenial checks err is the denial of a call of g's service with message.
func wantDenial(k *kerneltest.K, err *awserr.Error, message string) {
	k.Helper()
	if err == nil {
		k.Fatal("the call succeeded, want the denial")
	}
	if _, denial := err.Origin.(awserr.Denial); !denial || err.ID != guardRefused || err.Message != message {
		k.Fatalf("error = %+v (origin %T), want the service's denial identity with message %q and origin awserr.Denial", err.ID, err.Origin, message)
	}
}

// A denied call answers its service's denial identity with the
// Authorizer's message, before its handler and before the request-phase
// fault rules (iam-enforcement-surfaces.md 12.1: "after validation, before
// the handler"; 12.3: each service's own identity). It is journaled and
// access-recorded as a failed call, not as an injected fault.
func TestADeniedCallAnswersTheServicesDenialIdentity(t *testing.T) {
	g := newGuard(guardDenial())
	const message = "User: arn:aws:iam::000000000000:user/u is not authorized to perform: guard:Run"
	a := &fakeAuthorizer{decide: denyAll(message)}
	runGuard(t, g, a, func(k *kerneltest.K) {
		rule, ferr := k.Faults.Add(fault.Rule{
			Match: fault.Match{Operations: []ref.OperationID{g.runDesc.ID}}, Phase: fault.Request,
			Action: fault.Throttle{}, Trigger: fault.Always{},
		})
		if ferr != nil {
			k.Fatal(ferr)
		}
		ran := false
		bindGuard(k, g.run, func(context.Context, *kernel.Call, *guardInput) (*guardOutput, *awserr.Error) {
			ran = true
			return &guardOutput{}, nil
		})
		c := k.Call(g.runDesc)
		_, err := kerneltest.Dispatch[guardInput, guardOutput](k.Context(), k, c, &guardInput{})
		wantDenial(k, err, message)
		if ran {
			k.Error("the handler ran for a denied call")
		}

		want := []asked{{Op: g.runDesc.ID, Mode: nil, Query: kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: guardRun, Resource: kernel.AnyResource{}}}}}}
		if got := a.questions(); !reflect.DeepEqual(got, want) {
			k.Errorf("the Authorizer was asked %+v, want %+v", got, want)
		}

		failed := journal.Failed{Identity: journal.ErrorIdentity{Shape: string(guardRefused.Shape)}, Status: guardRefused.Status, Message: message}
		if call := callEvent(k, c); call.Outcome != failed {
			k.Errorf("journaled outcome = %+v, want %+v", call.Outcome, failed)
		}
		if hits := k.Journal.Query(journal.Filter{Kind: journal.KindFaultInjected}).Events; len(hits) != 0 {
			k.Errorf("journaled %d fault hits for a denied call, want none", len(hits))
		}
		for _, r := range k.Faults.List() {
			if r.Rule.ID == rule && r.Hits != 0 {
				k.Errorf("the request-phase rule took %d hits from a denied call, want 0", r.Hits)
			}
		}
		if u, ok := useOf(k, guardRun, access.AnyResource{}); !ok || u.Calls != 1 || u.Failures != 1 {
			k.Errorf("access record of guard:Run = %+v (found %t), want one failed call", u, ok)
		}
	})
}

// An allowed call goes on to the request-phase fault rules and the
// handler: authorization runs before fault.Before, not instead of it
// (iam-enforcement-surfaces.md 12.1).
func TestAnAllowedCallGoesOnToTheFaultRulesAndTheHandler(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{}
	runGuard(t, g, a, func(k *kerneltest.K) {
		if _, ferr := k.Faults.Add(fault.Rule{
			Match: fault.Match{Operations: []ref.OperationID{g.runDesc.ID}}, Phase: fault.Request,
			Action: fault.Throttle{}, Trigger: fault.NextN{N: 1},
		}); ferr != nil {
			k.Fatal(ferr)
		}
		bindGuard(k, g.run, guardDone)
		_, err := k.Invoke(k.Context(), g.run, &guardInput{})
		if err == nil {
			k.Fatal("the first call succeeded, want the injected throttle")
		}
		if _, injected := err.Origin.(awserr.Injected); !injected {
			k.Fatalf("first call = %+v (origin %T), want the injected throttle", err.ID, err.Origin)
		}
		if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
			k.Fatalf("second call = %s, want the handler's output", err.Message)
		}
		if got := len(a.about(g.runDesc)); got != 2 {
			k.Errorf("the Authorizer was asked %d times, want once per call (2)", got)
		}
	})
}

// Validation comes first (iam-enforcement-surfaces.md 12.1, switch
// iam.enforce.order validate-first until IAM-ENF-13): an invalid input and
// a rejected decode answer their own error, and the Authorizer is not
// asked about them.
func TestValidationRunsBeforeAuthorization(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{decide: denyAll("denied")}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.run, guardDone, kernel.WithValidator(guardValid))
		_, err := k.Invoke(k.Context(), g.run, &guardInput{Invalid: true})
		if err == nil {
			k.Fatal("an invalid input succeeded, want its validation error")
		}
		if _, denial := err.Origin.(awserr.Denial); denial {
			k.Errorf("an invalid input answered the denial %+v, want its validation error", err.ID)
		}
		rejected := &awserr.Error{ID: awserr.SerializationException(), Origin: awserr.Generic{}}
		if res, ok := k.Kernel.Dispatch(k.Context(), k.Call(g.runDesc), kernel.Rejected{Err: rejected}).(kernel.Failure); !ok || res.Err != rejected {
			k.Errorf("a rejected decode = %#v, want the rejection", res)
		}
		if got := a.questions(); len(got) != 0 {
			k.Errorf("the Authorizer was asked %+v before validation passed, want nothing", got)
		}
		_, err = k.Invoke(k.Context(), g.run, &guardInput{})
		wantDenial(k, err, "denied")
	})
}

// The pairs step 8 asks about: the primary action, then the actions the
// binding declared that the operation lists, in declaration order without
// duplicates, never the whole generated list; each paired with every
// touched resource of its service in first-touch order (an sts action with
// IAM roles), else with any resource when it is of the operation's own
// service, else not at all. An S3 object stays the object. Keys are nil.
// The pairing is the access recorder's (ARCHITECTURE.md 17) without its
// bucket folding; a call needs every pair allowed (iam-evaluation.md 2.1).
func TestStep8AsksAboutTheDeclaredActionsOnTheirTouchedResources(t *testing.T) {
	bogus := registry.IAMAction{Prefix: "guard", Name: "Undeploy"}
	cases := []struct {
		name     string
		undecl   bool // bind without WithActions
		in       guardInput
		wantPair []kernel.Pair
	}{
		{
			name: "an undeclared binding asks about the primary only", undecl: true,
			in:       guardInput{Touch: []aws.ARN{guardRole}},
			wantPair: []kernel.Pair{{Action: guardRun, Resource: kernel.AnyResource{}}},
		},
		{
			name:     "a binding that declares nothing for the input asks about the primary only",
			in:       guardInput{Touch: []aws.ARN{guardRole}},
			wantPair: []kernel.Pair{{Action: guardRun, Resource: kernel.AnyResource{}}},
		},
		{
			name: "declared actions follow the primary in declaration order",
			in:   guardInput{Touch: []aws.ARN{guardRole}, Declare: []registry.IAMAction{passRole, guardTag}},
			wantPair: []kernel.Pair{
				{Action: guardRun, Resource: kernel.AnyResource{}},
				{Action: passRole, Resource: kernel.ResourceARN{ARN: guardRole}},
				{Action: guardTag, Resource: kernel.AnyResource{}},
			},
		},
		{
			name: "duplicates are dropped",
			in:   guardInput{Declare: []registry.IAMAction{guardTag, guardRun, guardTag}},
			wantPair: []kernel.Pair{
				{Action: guardRun, Resource: kernel.AnyResource{}},
				{Action: guardTag, Resource: kernel.AnyResource{}},
			},
		},
		{
			name:     "a declared action the operation does not list is dropped",
			in:       guardInput{Declare: []registry.IAMAction{bogus}},
			wantPair: []kernel.Pair{{Action: guardRun, Resource: kernel.AnyResource{}}},
		},
		{
			name: "own-service resources are paired in first-touch order",
			in:   guardInput{Touch: []aws.ARN{guardThing2, guardRole, guardThing1}},
			wantPair: []kernel.Pair{
				{Action: guardRun, Resource: kernel.ResourceARN{ARN: guardThing2}},
				{Action: guardRun, Resource: kernel.ResourceARN{ARN: guardThing1}},
			},
		},
		{
			name: "an sts action pairs with an IAM role",
			in:   guardInput{Touch: []aws.ARN{guardRole}, Declare: []registry.IAMAction{assumeRole}},
			wantPair: []kernel.Pair{
				{Action: guardRun, Resource: kernel.AnyResource{}},
				{Action: assumeRole, Resource: kernel.ResourceARN{ARN: guardRole}},
			},
		},
		{
			name: "an S3 object stays the object",
			in:   guardInput{Touch: []aws.ARN{guardObject}, Declare: []registry.IAMAction{getObject}},
			wantPair: []kernel.Pair{
				{Action: guardRun, Resource: kernel.AnyResource{}},
				{Action: getObject, Resource: kernel.ResourceARN{ARN: guardObject}},
			},
		},
		{
			name:     "another service's action without a resource of that service is not asked about",
			in:       guardInput{Declare: []registry.IAMAction{passRole, olRun}},
			wantPair: []kernel.Pair{{Action: guardRun, Resource: kernel.AnyResource{}}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newGuard(guardDenial())
			a := &fakeAuthorizer{}
			runGuard(t, g, a, func(k *kerneltest.K) {
				opts := []kernel.BindOption{kernel.WithResources(touched)}
				if !c.undecl {
					opts = append(opts, kernel.WithActions(declared))
				}
				bindGuard(k, g.run, guardDone, opts...)
				if _, err := k.Invoke(k.Context(), g.run, &c.in); err != nil {
					k.Fatalf("an allowed call = %s", err.Message)
				}
				want := []asked{{Op: g.runDesc.ID, Query: kernel.AuthzQuery{Pairs: c.wantPair}}}
				if got := a.questions(); !reflect.DeepEqual(got, want) {
					k.Errorf("the Authorizer was asked %+v, want %+v", got, want)
				}
			})
		})
	}
}

// An internal operation lists no actions: a call is asked about only the
// actions its binding declares, and a call with no pair is not asked about
// at all.
func TestStep8AsksAboutAnInternalOperationsDeclaredActionsOnly(t *testing.T) {
	t.Run("undeclared", func(t *testing.T) {
		g := newGuard(guardDenial())
		a := &fakeAuthorizer{decide: denyAll("denied")}
		runGuard(t, g, a, func(k *kerneltest.K) {
			bindGuard(k, g.sweep, guardDone)
			if _, err := k.Invoke(k.Context(), g.sweep, &guardInput{}); err != nil {
				k.Errorf("an internal call with no declared action = %s, want it never asked about", err.Message)
			}
			if got := a.questions(); len(got) != 0 {
				k.Errorf("the Authorizer was asked %+v about a call with no pair", got)
			}
		})
	})
	t.Run("declared", func(t *testing.T) {
		g := newGuard(guardDenial())
		a := &fakeAuthorizer{decide: denyAll("denied")}
		runGuard(t, g, a, func(k *kerneltest.K) {
			bindGuard(k, g.sweep, guardDone, kernel.WithActions(func(*kernel.Call, *guardInput) []registry.IAMAction {
				return []registry.IAMAction{guardSweep}
			}))
			_, err := k.Invoke(k.Context(), g.sweep, &guardInput{})
			wantDenial(k, err, "denied")
			want := []kernel.Pair{{Action: guardSweep, Resource: kernel.AnyResource{}}}
			if got := a.about(g.sweepDesc); len(got) != 1 || !reflect.DeepEqual(got[0].Query.Pairs, want) {
				k.Errorf("the Authorizer was asked %+v, want once about %+v", got, want)
			}
		})
	})
}

// A raw HTTP binding authorizes itself (function URLs answer from the
// resource policy alone), so step 8 leaves it alone; a passthrough binding
// (BindRaw, the DynamoDB proxy) is decoded and validated like any other
// and is asked, since enforcement must decide before forwarding
// (iam-enforcement-surfaces.md 12.1).
func TestStep8SkipsRawHTTPButNotPassthroughBindings(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{decide: denyAll("denied")}
	runGuard(t, g, a, func(k *kerneltest.K) {
		streamed := false
		kernel.BindRawHTTP(k.Kernel.Binder(g.svc), g.stream, func(_ context.Context, _ *kernel.Call, _ *http.Request, w http.ResponseWriter) *awserr.Error {
			streamed = true
			w.WriteHeader(http.StatusOK)
			return nil
		}, registry.Full{})
		forwarded := false
		kernel.BindRaw(k.Kernel.Binder(g.svc), g.fetch, func(context.Context, *kernel.Call, *guardInput, kernel.Wire) (kernel.RawResponse, *awserr.Error) {
			forwarded = true
			return kernel.RawResponse{Status: http.StatusOK}, nil
		}, registry.Full{})
		if err := k.Kernel.BindErr(); err != nil {
			k.Fatal(err)
		}

		req := httptest.NewRequestWithContext(k.Context(), http.MethodGet, "http://localhost:4566/stream", http.NoBody)
		res := k.Kernel.Dispatch(k.Context(), k.Call(g.stream), kernel.Ok{In: kernel.RawExchange{Request: req, Writer: httptest.NewRecorder()}})
		if f, failed := res.(kernel.Failure); failed || !streamed {
			k.Errorf("raw HTTP call = %#v (handler ran %t), want it handled without authorization", f, streamed)
		}
		if got := a.about(g.stream); len(got) != 0 {
			k.Errorf("the Authorizer was asked %+v about a raw HTTP call", got)
		}

		_, err := k.Invoke(k.Context(), g.fetch, &guardInput{})
		wantDenial(k, err, "denied")
		if forwarded {
			k.Error("the passthrough handler forwarded a denied call")
		}
		want := []kernel.Pair{{Action: guardFetch, Resource: kernel.AnyResource{}}}
		if got := a.about(g.fetchDesc); len(got) != 1 || !reflect.DeepEqual(got[0].Query.Pairs, want) {
			k.Errorf("the Authorizer was asked %+v, want once about %+v", got, want)
		}
	})
}

// An internal call is asked about with its principal mode (Call.Mode is
// Target.As): a delivery, a forward-access call and an execution-role call
// are authorized; a platform call, which no policy grants, is not
// (ARCHITECTURE.md 16; the access recorder leaves it out for the same
// reason). An external call's mode is nil.
func TestStep8AsksAboutInternalCallsWithTheirModeExceptPlatformCalls(t *testing.T) {
	source := mustARN("arn:aws:sns:us-east-1:000000000000:topic")
	modes := []struct {
		name  string
		mode  kernel.PrincipalMode
		asked bool
	}{
		{"service", kernel.AsService{Name: "guard.amazonaws.com", Source: source}, true},
		{"caller", kernel.AsCaller{}, true},
		{"execution role", kernel.AsExecutionRole{Role: guardRole}, true},
		{"platform", kernel.AsPlatform{Name: "guard.amazonaws.com"}, false},
	}
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			g := newGuard(guardDenial())
			a := &fakeAuthorizer{decide: denyAction(guardProbe, "probe denied")}
			runGuard(t, g, a, func(k *kerneltest.K) {
				probed := false
				var childMode kernel.PrincipalMode
				bindGuard(k, g.probe, func(_ context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
					probed = true
					childMode = c.Mode()
					return &guardOutput{}, nil
				})
				var parentMode kernel.PrincipalMode = kernel.AsCaller{}
				bindGuard(k, g.run, func(ctx context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
					parentMode = c.Mode()
					return kernel.Invoke(ctx, k.Kernel, c, g.probe, kernel.Target{Scope: c.Scope(), As: m.mode, Via: ref.SNSDelivery}, &guardInput{})
				})
				_, err := k.Invoke(k.Context(), g.run, &guardInput{})
				if parentMode != nil {
					k.Errorf("an external call's Mode = %#v, want nil", parentMode)
				}
				got := a.about(g.probeDesc)
				if !m.asked {
					if err != nil || !probed || len(got) != 0 {
						k.Errorf("platform call: err %v, handler ran %t, asked %+v; want it run without authorization", err.AsError(), probed, got)
					}
					if !reflect.DeepEqual(childMode, m.mode) {
						k.Errorf("the child call's Mode = %#v, want %#v", childMode, m.mode)
					}
					return
				}
				wantDenial(k, err, "probe denied")
				if probed {
					k.Error("the invoked handler ran for a denied internal call")
				}
				if len(got) != 1 || !reflect.DeepEqual(got[0].Mode, m.mode) {
					k.Errorf("the Authorizer was asked %+v about the internal call, want once with mode %#v", got, m.mode)
				}
			})
		})
	}
}

// A denial of a call whose service descriptor names no denial identity is
// a 500 whose cause says how to add one; nothing falls back to a generic
// identity (CLAUDE.md invariant 3: nothing silently faked).
func TestADenialWithoutADenialIdentityIsInternal(t *testing.T) {
	g := newGuard(registry.Denial{})
	const message = "User: u is not authorized"
	a := &fakeAuthorizer{decide: denyAll(message)}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.run, guardDone)
		_, err := k.Invoke(k.Context(), g.run, &guardInput{})
		cause := internalCause(k.T, err)
		missing, ok := errors.AsType[*kernel.DenialIdentityError](cause)
		if !ok || missing.Service != "guard" || missing.Message != message {
			k.Fatalf("cause = %v, want a DenialIdentityError of guard with the message", cause)
		}
		text := missing.Error()
		for _, want := range []string{"guard", "dev.understudy#denialError", `"` + message + `"`, "codegen/overlays/guard.json", "just generate"} {
			if !strings.Contains(text, want) {
				t.Errorf("DenialIdentityError text %q does not contain %q", text, want)
			}
		}
	})
}

// An Authorizer that panics is service code like a validator: the call
// answers 500 with a PanicError and the handler does not run.
func TestAnAuthorizerPanicIsInternal(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{decide: func(kernel.AuthzQuery) kernel.Verdict { panic("boom") }}
	runGuard(t, g, a, func(k *kerneltest.K) {
		ran := false
		bindGuard(k, g.run, func(context.Context, *kernel.Call, *guardInput) (*guardOutput, *awserr.Error) {
			ran = true
			return &guardOutput{}, nil
		})
		_, err := k.Invoke(k.Context(), g.run, &guardInput{})
		cause := internalCause(k.T, err)
		if p, ok := errors.AsType[*kernel.PanicError](cause); !ok || p.Value != "boom" {
			k.Errorf("cause = %v, want the PanicError of boom", cause)
		}
		if ran {
			k.Error("the handler ran after the Authorizer panicked")
		}
	})
}

// A denial's per-protocol messages ride on the error (SQS over Query names
// the queue URL, iam-enforcement-surfaces.md 4.3).
func TestADenialKeepsItsProtocolMessages(t *testing.T) {
	g := newGuard(guardDenial())
	query := []awserr.ProtocolMessage{{Protocol: ref.AWSQuery, Message: "Access to the resource http://localhost:4566/000000000000/q is denied."}}
	a := &fakeAuthorizer{decide: func(kernel.AuthzQuery) kernel.Verdict {
		return kernel.Denied{Message: "generic", Messages: query}
	}}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.run, guardDone)
		_, err := k.Invoke(k.Context(), g.run, &guardInput{})
		wantDenial(k, err, "generic")
		if !reflect.DeepEqual(err.Messages, query) || err.MessageFor(ref.AWSQuery) != query[0].Message || err.MessageFor(ref.AWSJSON10) != "generic" {
			k.Errorf("messages = %+v, want the Query override beside the generic text", err.Messages)
		}
	})
}

// authorizeRole is a handler that resolves guardRole itself and asks
// kernel.Authorize whether it may pass it.
func authorizeRole(keys kernel.ConditionKeys) kernel.Handler[guardInput, guardOutput] {
	return func(ctx context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
		if err := kernel.Authorize(ctx, c, passRole, guardRole, keys); err != nil {
			return nil, err
		}
		return &guardOutput{}, nil
	}
}

// kernel.Authorize asks about exactly the one pair the handler resolved,
// with the keys it supplies; a denial is the service's denial identity,
// which the handler returns and the pipeline journals and access-records
// as a failed call with the resource touched.
func TestAuthorizeAsksAboutTheResolvedResource(t *testing.T) {
	g := newGuard(guardDenial())
	const message = "not allowed to pass the role"
	keys := kernel.ConditionKeys{"iam:PassedToService": {"guard.amazonaws.com"}}
	a := &fakeAuthorizer{decide: denyResource(guardRole, message)}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.run, authorizeRole(keys))
		c := k.Call(g.runDesc)
		_, err := kerneltest.Dispatch[guardInput, guardOutput](k.Context(), k, c, &guardInput{})
		wantDenial(k, err, message)

		want := []asked{
			{Op: g.runDesc.ID, Query: kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: guardRun, Resource: kernel.AnyResource{}}}}},
			{Op: g.runDesc.ID, Query: kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: passRole, Resource: kernel.ResourceARN{ARN: guardRole}}}, Keys: keys}},
		}
		if got := a.questions(); !reflect.DeepEqual(got, want) {
			k.Errorf("the Authorizer was asked %+v, want step 8 then %+v", got, want[1])
		}
		call := callEvent(k, c)
		failed := journal.Failed{Identity: journal.ErrorIdentity{Shape: string(guardRefused.Shape)}, Status: guardRefused.Status, Message: message}
		if call.Outcome != failed || !slices.Contains(call.Resources, guardRole) {
			k.Errorf("journaled outcome %+v resources %v, want %+v touching %s", call.Outcome, call.Resources, failed, guardRole)
		}
		if u, ok := useOf(k, passRole, access.OnResource{ARN: guardRole}); !ok || u.Calls != 1 || u.Failures != 1 {
			k.Errorf("access record of iam:PassRole on the role = %+v (found %t), want one failed call", u, ok)
		}
	})
}

// An allowed Authorize returns nil and the call goes on; without an
// Authorizer every Authorize allows. Either way the resource is touched,
// so the journal and the access record list it whatever the verdict.
func TestAuthorizeAllowsAndTouchesTheResource(t *testing.T) {
	for _, withAuthorizer := range []bool{true, false} {
		t.Run("authorizer "+strconv.FormatBool(withAuthorizer), func(t *testing.T) {
			g := newGuard(guardDenial())
			var a kernel.Authorizer
			fake := &fakeAuthorizer{}
			if withAuthorizer {
				a = fake
			}
			runGuard(t, g, a, func(k *kerneltest.K) {
				bindGuard(k, g.run, authorizeRole(nil))
				c := k.Call(g.runDesc)
				if _, err := kerneltest.Dispatch[guardInput, guardOutput](k.Context(), k, c, &guardInput{}); err != nil {
					k.Fatalf("call = %s, want it allowed", err.Message)
				}
				if call := callEvent(k, c); !slices.Contains(call.Resources, guardRole) {
					k.Errorf("journaled resources %v, want %s", call.Resources, guardRole)
				}
				if withAuthorizer && len(fake.questions()) != 2 {
					k.Errorf("the Authorizer was asked %+v, want step 8 and the role", fake.questions())
				}
			})
		})
	}
}

// A platform call is never asked about, in a handler as at step 8.
func TestAuthorizeAllowsAPlatformCallWithoutAsking(t *testing.T) {
	g := newGuard(guardDenial())
	a := &fakeAuthorizer{decide: denyResource(guardRole, "denied")}
	runGuard(t, g, a, func(k *kerneltest.K) {
		var child *kernel.Call
		bindGuard(k, g.probe, func(ctx context.Context, c *kernel.Call, in *guardInput) (*guardOutput, *awserr.Error) {
			child = c
			return authorizeRole(nil)(ctx, c, in)
		})
		bindGuard(k, g.run, func(ctx context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
			return kernel.Invoke(ctx, k.Kernel, c, g.probe, kernel.Target{Scope: c.Scope(), As: kernel.AsPlatform{Name: "guard.amazonaws.com"}, Via: ref.SNSDelivery}, &guardInput{})
		})
		if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
			k.Fatalf("a platform call's Authorize = %s, want nil", err.Message)
		}
		if got := a.about(g.probeDesc); len(got) != 0 {
			k.Errorf("the Authorizer was asked %+v about a platform call", got)
		}
		if call := callEvent(k, child); !slices.Contains(call.Resources, guardRole) {
			k.Errorf("journaled resources %v, want the role touched", call.Resources)
		}
	})
}

// Authorize while holding a cell lock is the same mistake as an Invoke
// there (ARCHITECTURE.md 8 rule 1), and a zero ARN names no resource: both
// are Internal, and neither reaches the Authorizer.
func TestAuthorizeRefusesHeldLocksAndTheZeroARN(t *testing.T) {
	t.Run("held lock", func(t *testing.T) {
		g := newGuard(guardDenial())
		a := &fakeAuthorizer{}
		runGuard(t, g, a, func(k *kerneltest.K) {
			p := cells(k)
			bindGuard(k, g.run, func(ctx context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
				return kernel.Write(c, p, c.Scope(), func(*int, clock.Instant, *kernel.Tx) (*guardOutput, *awserr.Error) {
					return nil, kernel.Authorize(ctx, c, passRole, guardRole, nil)
				})
			})
			_, err := k.Invoke(k.Context(), g.run, &guardInput{})
			var holding *kernel.HoldingError
			if cause := internalCause(k.T, err); !errors.As(cause, &holding) || holding.Attempted != "kernel.Authorize of iam:PassRole" || holding.Holder != "guard.Run" {
				k.Errorf("cause = %v, want a HoldingError for kernel.Authorize of iam:PassRole from guard.Run", cause)
			}
			if got := len(a.questions()); got != 1 {
				k.Errorf("the Authorizer was asked %d times, want only at step 8", got)
			}
		})
	})
	t.Run("zero ARN", func(t *testing.T) {
		g := newGuard(guardDenial())
		a := &fakeAuthorizer{}
		runGuard(t, g, a, func(k *kerneltest.K) {
			bindGuard(k, g.run, func(ctx context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
				if err := kernel.Authorize(ctx, c, passRole, aws.ARN{}, nil); err != nil {
					return nil, err
				}
				return &guardOutput{}, nil
			})
			_, err := k.Invoke(k.Context(), g.run, &guardInput{})
			if cause := internalCause(k.T, err); cause == nil {
				k.Error("the zero ARN answered Internal without a cause")
			}
			if got := len(a.questions()); got != 1 {
				k.Errorf("the Authorizer was asked %d times, want only at step 8", got)
			}
		})
	})
}

// An in-handler denial of a service without a denial identity is the same
// 500 as at step 8.
func TestAuthorizeDenialWithoutADenialIdentityIsInternal(t *testing.T) {
	g := newGuard(registry.Denial{})
	a := &fakeAuthorizer{decide: denyResource(guardRole, "denied")}
	runGuard(t, g, a, func(k *kerneltest.K) {
		bindGuard(k, g.run, authorizeRole(nil))
		_, err := k.Invoke(k.Context(), g.run, &guardInput{})
		if missing, ok := errors.AsType[*kernel.DenialIdentityError](internalCause(k.T, err)); !ok || missing.Service != "guard" || missing.Message != "denied" {
			k.Errorf("cause = %v, want a DenialIdentityError of guard", missing)
		}
	})
}
