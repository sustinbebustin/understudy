package apigatewayv2_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2"
)

// respondWith makes the function answer through respond.
func (f *fakeFunction) respondWith(respond func(ctx context.Context, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.respond = respond
}

// An integration naming a function alias invokes that alias: the
// qualifier stays on the function name Lambda gets, and the alias's own
// resource policy is the one checked, since a permission added with a
// qualifier is a statement on the qualified ARN (lambda-api.md 7.3:
// "If qualified, the invoker must use the qualified ARN").
func TestQualifiedIntegrationKeepsItsQualifier(t *testing.T) {
	run(t, func(h *harness) {
		fnARN := h.createFunction("fn")
		v := must(h, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: "fn"})
		must(h, lambdaapi.OpCreateAlias, &lambdaapi.CreateAliasInput{FunctionName: "fn", Name: "live", FunctionVersion: deref(v.Version)})
		id := h.apiID("t")
		h.createRoute(id, "GET /live", h.lambdaIntegration(id, fnARN+":live", "2.0"))
		h.autoStage(id)
		wantAnswer(t, h.get(id, "/live"), apigatewayv2.PermissionSwitch().Value.Denial, "an alias without a permission")
		h.notInvoked("an alias without a permission")
		must(h, lambdaapi.OpAddPermission, &lambdaapi.AddPermissionInput{
			FunctionName: "fn", Qualifier: new("live"), StatementId: "apigateway", Action: "lambda:InvokeFunction",
			Principal: "apigateway.amazonaws.com", SourceArn: new(h.executionARN(id) + "/*/*"),
		})
		if rec := h.get(id, "/live"); rec.Code != http.StatusOK {
			t.Fatalf("GET /live with the alias's permission = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		if name := h.only().in.FunctionName; name != fnARN+":live" {
			t.Errorf("FunctionName = %q, want the alias %s:live", name, fnARN)
		}
	})
}

// The integration timeout is the integration's TimeoutInMillis of wall
// time (apigateway-http.md 13.3 item 6, ARCHITECTURE.md 9): a function that
// answers within it is served even when the emulator clock moves an hour
// meanwhile, and one that answers after it gets the
// apigatewayv2.lambda.failure-answers switch's Timeout although the frozen
// emulator clock never moves.
func TestIntegrationTimeoutIsWallTime(t *testing.T) {
	run(t, func(h *harness) {
		id, integration := h.lambdaAPI("2.0", "GET /work")
		must(h, apigatewayv2api.OpUpdateIntegration, &apigatewayv2api.UpdateIntegrationInput{ApiId: id, IntegrationId: integration, TimeoutInMillis: new(int32(50))})
		clk := h.Kernel.Clock()
		takes := func(d, advance time.Duration) {
			h.fn.respondWith(func(ctx context.Context, _ *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
				if advance > 0 {
					if _, err := clk.Advance(advance); err != nil {
						h.Errorf("advancing the emulator clock: %v", err)
					}
				}
				<-clk.Until(ctx, clk.WallDeadline(clk.Wall().Add(d)))
				return payload(`{"statusCode":200,"body":"done"}`), nil
			})
		}
		takes(40*time.Millisecond, time.Hour)
		if rec := h.get(id, "/work"); rec.Code != http.StatusOK || rec.Body.String() != "done" {
			t.Errorf("a function answering after 40 ms of wall time, with the emulator clock an hour on = %d %s, want the function's 200", rec.Code, rec.Body)
		}
		takes(60*time.Millisecond, 0)
		wantAnswer(t, h.get(id, "/work"), apigatewayv2.FailureAnswersSwitch().Value.Timeout, "a function answering after 60 ms of wall time")
		// Let the timed-out invocation finish before the kernel stops.
		if _, err := h.Kernel.Settle(h.Context(), time.Second); err != nil {
			t.Errorf("settling after the timed-out invocation: %v", err)
		}
	})
}

// An invocation past the integration's timeout keeps running as tracked
// work, so settling waits for it, and when it ends its answer is dropped
// (inferred, apigateway-http.md 5.6, APIGW2-10); its journal event is
// still the InvokeApi call's child (ARCHITECTURE.md 9).
func TestTimedOutInvocationRunsOnAsTrackedWork(t *testing.T) {
	run(t, func(h *harness) {
		id, integration := h.lambdaAPI("2.0", "GET /slow")
		must(h, apigatewayv2api.OpUpdateIntegration, &apigatewayv2api.UpdateIntegrationInput{ApiId: id, IntegrationId: integration, TimeoutInMillis: new(int32(50))})
		h.fn.hang()
		wantAnswer(t, h.get(id, "/slow"), apigatewayv2.FailureAnswersSwitch().Value.Timeout, "an invocation past its timeout")
		pending, err := h.Kernel.Settle(h.Context(), time.Second)
		var settle *kernel.SettleError
		if !errors.As(err, &settle) || pending[kernel.InvocationRunning] != 1 {
			t.Errorf("Settle while the function runs = %v, %v; want one invocation-running", pending, err)
		}
		h.fn.releaseAll()
		if _, err := h.Kernel.Settle(h.Context(), time.Second); err != nil {
			t.Fatalf("Settle after the function ended = %v, want settled", err)
		}
		var front, invoke *journal.Event
		events := h.Journal.Query(journal.Filter{Kind: journal.KindAPICall, Limit: 1000}).Events
		for i := range events {
			call, ok := events[i].Detail.(journal.APICall)
			switch {
			case !ok:
			case call.Op == invokeAPI:
				front = &events[i]
			case call.Op == lambdaapi.OpInvoke.Descriptor().ID:
				invoke = &events[i]
			}
		}
		if front == nil || invoke == nil || invoke.Cause.Parent != front.ID {
			t.Errorf("journal holds InvokeApi %v and Invoke %v, want Invoke as InvokeApi's child", front, invoke)
		}
	})
}

// A client that goes away while the function runs gets no gateway answer:
// InvokeApi stops waiting and answers RequestAborted, as the body read
// does, and the function runs on.
func TestClientGoneWhileTheFunctionRuns(t *testing.T) {
	run(t, func(h *harness) {
		id, _ := h.lambdaAPI("2.0", "GET /hello")
		ctx, cancel := context.WithCancel(h.Context())
		defer cancel()
		release := make(chan struct{})
		h.fn.respondWith(func(fctx context.Context, _ *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
			cancel()
			select {
			case <-release:
			case <-fctx.Done():
			}
			return payload(`{"statusCode":200,"body":"late"}`), nil
		})
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, pathURL(id, "/hello"), http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		rec := h.send(r)
		close(release)
		if aborted := awserr.RequestAbortedException(); rec.Code != aborted.Status {
			t.Errorf("a client gone before the function answered = %d %s, want RequestAborted's %d", rec.Code, rec.Body, aborted.Status)
		}
		if _, err := h.Kernel.Settle(h.Context(), time.Second); err != nil {
			t.Errorf("Settle after the function ended = %v, want settled", err)
		}
		if n := len(h.fn.invocations()); n != 1 {
			t.Errorf("%d invocations, want the one the client left", n)
		}
	})
}
