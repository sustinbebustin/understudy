package lambda_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool/pooltest"
)

// invocationDenier stands in for an enforcing evaluator under which no
// principal may invoke a function: it denies every pair of
// lambda:InvokeFunctionUrl or lambda:InvokeFunction, allows the rest and
// keeps every pair it is asked about.
type invocationDenier struct {
	mu    sync.Mutex
	asked []kernel.Pair
}

func (a *invocationDenier) Authorize(_ context.Context, _ *kernel.Call, q kernel.AuthzQuery) kernel.Verdict {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asked = append(a.asked, q.Pairs...)
	for _, p := range q.Pairs {
		if isInvocation(p.Action) {
			return kernel.Denied{Message: "the test denies every invocation"}
		}
	}
	return kernel.Allowed{}
}

// isInvocation reports whether a invokes a function.
func isInvocation(a registry.IAMAction) bool {
	return a == registry.IAMAction{Prefix: "lambda", Name: "InvokeFunctionUrl"} || a == registry.IAMAction{Prefix: "lambda", Name: "InvokeFunction"}
}

// invocationsAsked returns the invocation pairs a was asked about.
func (a *invocationDenier) invocationsAsked() []kernel.Pair {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []kernel.Pair
	for _, p := range a.asked {
		if isInvocation(p.Action) {
			out = append(out, p)
		}
	}
	return out
}

// runAuthorizedURLs is runURLs on a kernel whose Authorizer is a.
func runAuthorizedURLs(t *testing.T, a kernel.Authorizer, fn func(h *urlHarness)) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		withExec(k, pooltest.Echo, nil, func(h *execHarness) {
			h.x.BindFunctionURLs(h.Deps(lambdaapi.Service()), lambda.FunctionURLOptions{})
			admin := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				t.Errorf("the admin API received %s %s", r.Method, r.URL)
			})
			e, err := edge.New(edge.Options{
				Kernel: h.Kernel, Registry: h.Kernel.Registry(), Identity: h.Identity, Regions: aws.KnownRegions(), Listener: ref.ListenerMain, Admin: admin,
			})
			if err != nil {
				t.Fatal(err)
			}
			fn(&urlHarness{execHarness: h, edge: e})
		})
	}, kerneltest.Services(lambdaapi.Service(), logsapi.Service()), kerneltest.Authorizer(a))
}

// A request to an AuthType NONE URL is anonymous, and only the function's
// resource policy can grant an anonymous caller (iam-evaluation.md 2.10;
// iam-enforcement-surfaces.md 7.4). So the URL answers exactly as it does
// with no Authorizer, here one that would deny every invocation, and the
// Authorizer is never asked about the request: the function URL's binding
// is raw and decides from the resource policy itself.
func TestFunctionURLWithAuthTypeNoneIsDecidedByItsResourcePolicyAlone(t *testing.T) {
	a := &invocationDenier{}
	runAuthorizedURLs(t, a, func(h *urlHarness) {
		host := h.createURL(lambdaapi.FunctionUrlAuthTypeNone)
		expect(t, h.send(h.request(http.MethodGet, "http://"+host+"/", "")), http.StatusForbidden, forbidden)
		h.allowPublicURLAction()
		expect(t, h.send(h.request(http.MethodGet, "http://"+host+"/", "")), http.StatusForbidden, forbidden)
		h.allowPublicInvokeViaURL()
		rec := h.send(h.request(http.MethodGet, "http://"+host+"/", ""))
		var ev struct {
			RawPath string `json:"rawPath"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &ev); rec.Code != http.StatusOK || err != nil || ev.RawPath != "/" {
			t.Errorf("with both statements: %d %s, want 200 echoing the event", rec.Code, rec.Body)
		}
	})
	if asked := a.invocationsAsked(); len(asked) != 0 {
		t.Errorf("the Authorizer was asked about %v, want no invocation pair", asked)
	}
	// The control-plane calls that set the URL up were asked about, so the
	// kernel did consult this Authorizer.
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.asked) == 0 {
		t.Error("the Authorizer was never asked, want CreateFunction, CreateFunctionUrlConfig and AddPermission asked at step 8")
	}
}
