package apigatewayv2_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/server"
	"github.com/sustinbebustin/understudy/internal/service/apigatewayv2"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
)

// harness is a kernel with API Gateway's HTTP APIs, the Lambda control
// plane (functions and their resource policies) and a fake Lambda Invoke
// that records what API Gateway sends, behind an edge on the main listener
// that serves the execute-api host and path forms.
type harness struct {
	*kerneltest.K
	svc    *apigatewayv2.Service
	lambda *lambda.Service
	fn     *fakeFunction
	edge   *edge.Handler
}

// run runs fn against the services in a kernel built with opts.
func run(t *testing.T, fn func(h *harness), opts ...kerneltest.Option) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		blobs, err := blob.NewMemory("", 0)
		if err != nil {
			k.Fatal(err)
		}
		defer func() {
			if err := blobs.Close(); err != nil {
				k.Error(err)
			}
		}()
		deps := k.Deps(lambdaapi.Service())
		deps.Blob = blobs
		functions := lambda.New(deps, lambda.Options{})
		fake := &fakeFunction{}
		k.Stub(lambdaapi.OpInvoke, fake.invoke)
		svc := apigatewayv2.New(k.Deps(apigatewayv2api.Service()), apigatewayv2.Options{FunctionPolicies: functions, Regions: aws.KnownRegions()})
		admin := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			k.Errorf("the admin API received %s %s", r.Method, r.URL)
		})
		e, err := edge.New(edge.Options{
			Kernel: k.Kernel, Registry: k.Kernel.Registry(), Identity: k.Identity, Regions: aws.KnownRegions(),
			Codecs: server.Codecs(), S3BaseDomains: []string{"localhost"}, Listener: ref.ListenerMain, Admin: admin,
		})
		if err != nil {
			k.Fatal(err)
		}
		h := &harness{K: k, svc: svc, lambda: functions, fn: fake, edge: e}
		defer fake.releaseAll()
		fn(h)
	}, append(opts, kerneltest.Services(apigatewayv2api.Service(), lambdaapi.Service()))...)
}

// must runs op as a client request and fails the test on an error.
func must[In, Out any](h *harness, op kernel.Op[In, Out], in *In) *Out {
	h.Helper()
	out, err := h.Invoke(h.Context(), op, in)
	if err != nil {
		h.Fatalf("%s: %s %s", op.Descriptor().ID.Name, err.ID.JSONType, err.Message)
	}
	return out
}

// fail runs op as a client request that must fail and returns the error.
func fail[In, Out any](h *harness, op kernel.Op[In, Out], in *In) *awserr.Error {
	h.Helper()
	out, err := h.Invoke(h.Context(), op, in)
	if err == nil {
		h.Fatalf("%s = %+v, want an error", op.Descriptor().ID.Name, out)
	}
	return err
}

// wantError checks an error's identity and HTTP status and that it says
// something. API Gateway v2's error texts are UNVERIFIED (apigateway-http.md
// 2.3, APIGW2-01), so no text is asserted.
func wantError(tb testing.TB, err *awserr.Error, want awserr.Identity) {
	tb.Helper()
	if err == nil {
		tb.Fatalf("no error, want %s %d", want.JSONType, want.Status)
	}
	if err.ID.Shape != want.Shape || err.ID.Status != want.Status || err.Message == "" {
		tb.Errorf("error = %s %d %q, want %s %d with a message", err.ID.JSONType, err.ID.Status, err.Message, want.JSONType, want.Status)
	}
}

// The identities the control plane answers with (apigateway-http.md 2.3,
// the model's httpError traits; the constraint-failure identity is the
// overlay's inferred BadRequestException, conflict row 5 of the unit).
var (
	badRequest = apigatewayv2api.IdentityBadRequestException()
	conflict   = apigatewayv2api.IdentityConflictException()
	notFound   = apigatewayv2api.IdentityNotFoundException()
)

// wantNotImplemented checks a valid but unemulated request member's
// refusal: 501 NotImplemented naming the member and saying nothing was
// changed (the repository's convention for an unimplemented parameter,
// CLAUDE.md invariant 3).
func wantNotImplemented(tb testing.TB, err *awserr.Error, member string) {
	tb.Helper()
	if err == nil {
		tb.Fatalf("no error, want NotImplemented naming %s", member)
	}
	if err.ID.Shape != apigatewayv2api.IdentityNotImplemented().Shape || err.ID.Status != http.StatusNotImplemented {
		tb.Errorf("error = %s %d %q, want NotImplemented 501", err.ID.JSONType, err.ID.Status, err.Message)
	}
	if !strings.Contains(strings.ToLower(err.Message), strings.ToLower(member)) || !strings.Contains(err.Message, "nothing was changed") {
		tb.Errorf("message = %q, want it to name %s and say nothing was changed", err.Message, member)
	}
}

const (
	account = "000000000000"
	region  = "us-east-1"
)

// apiARN is an API's resource ARN, with the empty account field of
// apigateway-http.md 2.5 (https://docs.aws.amazon.com/apigateway/latest/developerguide/arn-format-reference.html).
func apiARN(id string) string { return "arn:aws:apigateway:" + region + "::/apis/" + id }

// executionARN is the execute-api ARN of an API in the harness scope, as
// Terraform's execution_arn builds it (apigateway-http.md 2.5, 9.7).
func (h *harness) executionARN(id string) string {
	return "arn:aws:execute-api:" + h.Scope.Region.String() + ":" + h.Scope.Account.String() + ":" + id
}

// createAPI creates an HTTP API with only the required members.
func (h *harness) createAPI(name string) *apigatewayv2api.CreateApiOutput {
	h.Helper()
	out := must(h, apigatewayv2api.OpCreateApi, &apigatewayv2api.CreateApiInput{Name: name, ProtocolType: apigatewayv2api.ProtocolTypeHttp})
	if out.ApiId == nil || *out.ApiId == "" {
		h.Fatalf("CreateApi %s answered no ApiId", name)
	}
	return out
}

func (h *harness) apiID(name string) string {
	h.Helper()
	return *h.createAPI(name).ApiId
}

// zipOf builds a zip package holding a bootstrap file.
func zipOf(tb testing.TB, body string) []byte {
	tb.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, err := w.Create("bootstrap")
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := f.Write([]byte(body)); err != nil {
		tb.Fatal(err)
	}
	if err := w.Close(); err != nil {
		tb.Fatal(err)
	}
	return b.Bytes()
}

// createFunction creates a zip function in the harness scope and returns
// its ARN.
func (h *harness) createFunction(name string) string {
	h.Helper()
	out := must(h, lambdaapi.OpCreateFunction, &lambdaapi.CreateFunctionInput{
		FunctionName: name, Role: "arn:aws:iam::" + h.Scope.Account.String() + ":role/lambda-role",
		Runtime: new(lambdaapi.RuntimeProvidedal2023), Handler: new("bootstrap"),
		Code: lambdaapi.FunctionCode{ZipFile: zipOf(h, "#!/bin/sh\n")}, PackageType: new(lambdaapi.PackageTypeZip),
	})
	if out.FunctionArn == nil {
		h.Fatalf("CreateFunction %s answered no FunctionArn", name)
	}
	return *out.FunctionArn
}

// allow adds the statement the CLI, Terraform and the docs add so API
// Gateway may invoke fn: lambda:InvokeFunction for apigateway.amazonaws.com
// with the source ARN given (apigateway-http.md 5.5, 9.6).
func (h *harness) allow(fn, sid, sourceARN string) {
	h.Helper()
	must(h, lambdaapi.OpAddPermission, &lambdaapi.AddPermissionInput{
		FunctionName: fn, StatementId: sid, Action: "lambda:InvokeFunction", Principal: "apigateway.amazonaws.com", SourceArn: new(sourceARN),
	})
}

// lambdaIntegration creates a Lambda proxy integration of payload format
// version to the function fnARN and returns its id.
func (h *harness) lambdaIntegration(apiID, fnARN, version string) string {
	h.Helper()
	out := must(h, apigatewayv2api.OpCreateIntegration, &apigatewayv2api.CreateIntegrationInput{
		ApiId: apiID, IntegrationType: apigatewayv2api.IntegrationTypeAwsProxy, IntegrationUri: new(fnARN), PayloadFormatVersion: new(version),
	})
	if out.IntegrationId == nil {
		h.Fatal("CreateIntegration answered no IntegrationId")
	}
	return *out.IntegrationId
}

// createRoute creates the route key on apiID targeting integration (none
// when integration is empty) and returns the route id.
func (h *harness) createRoute(apiID, key, integration string) string {
	h.Helper()
	in := &apigatewayv2api.CreateRouteInput{ApiId: apiID, RouteKey: key}
	if integration != "" {
		in.Target = new("integrations/" + integration)
	}
	out := must(h, apigatewayv2api.OpCreateRoute, in)
	if out.RouteId == nil {
		h.Fatalf("CreateRoute %s answered no RouteId", key)
	}
	return *out.RouteId
}

// autoStage creates the $default stage with automatic deployment.
func (h *harness) autoStage(apiID string) *apigatewayv2api.CreateStageOutput {
	h.Helper()
	return must(h, apigatewayv2api.OpCreateStage, &apigatewayv2api.CreateStageInput{ApiId: apiID, StageName: "$default", AutoDeploy: new(true)})
}

// lambdaAPI builds the tracer bullet's API: function fn, a Lambda proxy
// integration of payload format version, the given routes targeting it, a
// $default stage that deploys automatically, and a permission for every
// stage, method and path of the API ("${execution_arn}/*/*",
// apigateway-http.md 5.5). It returns the API id and the integration id.
func (h *harness) lambdaAPI(version string, keys ...string) (apiID, integration string) {
	h.Helper()
	fnARN := h.createFunction("fn")
	apiID = h.apiID("t")
	integration = h.lambdaIntegration(apiID, fnARN, version)
	for _, k := range keys {
		h.createRoute(apiID, k, integration)
	}
	h.autoStage(apiID)
	h.allow("fn", "apigateway", h.executionARN(apiID)+"/*/*")
	return apiID, integration
}

// invocation is one Invoke API Gateway made, as the fake function saw it.
type invocation struct {
	in     lambdaapi.InvokeInput
	mode   kernel.PrincipalMode
	origin ref.Origin
	scope  aws.Scope
}

// event decodes the invocation's payload.
func (i invocation) event(tb testing.TB) map[string]any {
	tb.Helper()
	var ev map[string]any
	if err := json.Unmarshal(i.in.Payload, &ev); err != nil {
		tb.Fatalf("event %s: %v", i.in.Payload, err)
	}
	return ev
}

// fakeFunction is Lambda's Invoke as API Gateway calls it: it records each
// call and answers with respond, a 200 structured response by default.
type fakeFunction struct {
	mu      sync.Mutex
	calls   []invocation
	respond func(ctx context.Context, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error)
	release chan struct{}
}

func (f *fakeFunction) invoke(ctx context.Context, c *kernel.Call, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
	f.mu.Lock()
	f.calls = append(f.calls, invocation{in: *in, mode: c.Mode(), origin: c.Origin(), scope: c.Scope()})
	respond := f.respond
	f.mu.Unlock()
	if respond == nil {
		return payload(`{"statusCode":200,"body":"ok"}`), nil
	}
	return respond(ctx, in)
}

// answer makes the function answer with the payload p.
func (f *fakeFunction) answer(p string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.respond = func(context.Context, *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
		return payload(p), nil
	}
}

// answerWith makes the function answer with out and err.
func (f *fakeFunction) answerWith(out *lambdaapi.InvokeOutput, err *awserr.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.respond = func(context.Context, *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
		return out, err
	}
}

// hang makes the function answer only after releaseAll, as a function
// that runs past the integration's timeout.
func (f *fakeFunction) hang() {
	f.mu.Lock()
	defer f.mu.Unlock()
	release := make(chan struct{})
	f.release = release
	f.respond = func(ctx context.Context, _ *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return payload(`{"statusCode":200,"body":"late"}`), nil
	}
}

// releaseAll lets every hanging invocation finish.
func (f *fakeFunction) releaseAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.release != nil {
		close(f.release)
		f.release = nil
	}
}

func (f *fakeFunction) invocations() []invocation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]invocation(nil), f.calls...)
}

// only returns the one invocation made, failing the test otherwise.
func (h *harness) only() invocation {
	h.Helper()
	calls := h.fn.invocations()
	if len(calls) != 1 {
		h.Fatalf("the function was invoked %d times, want once", len(calls))
	}
	return calls[0]
}

// notInvoked fails the test if the function ran.
func (h *harness) notInvoked(what string) {
	h.Helper()
	if calls := h.fn.invocations(); len(calls) != 0 {
		h.Errorf("%s: the function was invoked %d times, want never", what, len(calls))
	}
}

func payload(p string) *lambdaapi.InvokeOutput {
	return &lambdaapi.InvokeOutput{StatusCode: 200, Payload: []byte(p)}
}

// pathURL is the execute-api path fallback URL of path on the API id
// (apigateway-http.md 11.3: /_understudy/execute-api/<api-id>/<path>).
func pathURL(id, path string) string {
	return "http://localhost:4566/_understudy/execute-api/" + id + path
}

// request builds a client request to url.
func (h *harness) request(method, url, body string) *http.Request {
	h.Helper()
	var r io.Reader = http.NoBody
	if body != "" {
		r = strings.NewReader(body)
	}
	return httptest.NewRequestWithContext(h.Context(), method, url, r)
}

// send runs r through the edge.
func (h *harness) send(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.edge.ServeHTTP(rec, r)
	return rec
}

// get sends a GET of path to the API id through the path fallback.
func (h *harness) get(id, path string) *httptest.ResponseRecorder {
	h.Helper()
	return h.send(h.request(http.MethodGet, pathURL(id, path), ""))
}

// wantAnswer checks a response API Gateway writes itself: the answer's
// status, the body {"message":"<Message>"}, the Content-Type the
// apigatewayv2.front.answers switch names and an apigw-requestid header
// (apigateway-http.md 4.5, finding 12: the header name is from the JWT
// troubleshooting page).
func wantAnswer(tb testing.TB, rec *httptest.ResponseRecorder, want apigatewayv2.GatewayAnswer, what string) {
	tb.Helper()
	body, err := json.Marshal(map[string]string{"message": want.Message})
	if err != nil {
		tb.Fatal(err)
	}
	if rec.Code != want.Status || strings.TrimSpace(rec.Body.String()) != string(body) {
		tb.Errorf("%s: response %d %s, want %d %s", what, rec.Code, rec.Body, want.Status, body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != apigatewayv2.FrontAnswersSwitch().Value.ContentType {
		tb.Errorf("%s: Content-Type = %q, want %q (switch apigatewayv2.front.answers)", what, ct, apigatewayv2.FrontAnswersSwitch().Value.ContentType)
	}
	if rec.Header().Get("Apigw-Requestid") == "" {
		tb.Errorf("%s: no apigw-requestid header", what)
	}
}

// want501 checks a data-plane request understudy cannot serve as AWS
// would: 501 in API Gateway's {"message": ...} shape saying nothing was
// changed (the unit's conflict row 3: never an AWS-shaped 500).
func want501(tb testing.TB, rec *httptest.ResponseRecorder, what string) {
	tb.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		tb.Errorf("%s: body %s is not {\"message\": ...}: %v", what, rec.Body, err)
	}
	if rec.Code != http.StatusNotImplemented || !strings.Contains(body["message"], "nothing was changed") {
		tb.Errorf("%s: response %d %s, want 501 saying nothing was changed", what, rec.Code, rec.Body)
	}
}
