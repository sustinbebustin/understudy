package apigateway_test

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

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/server"
	"github.com/sustinbebustin/understudy/internal/service/apigateway"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
)

// invokeRestAPI is the raw data-plane operation (contract 1).
var invokeRestAPI = ref.OperationID{Service: "apigateway", Name: "InvokeRestApi"}

// harness is a kernel with API Gateway's REST APIs, the Lambda control
// plane (functions and their resource policies) and a fake Lambda Invoke
// that records what API Gateway sends, behind an edge on the main listener
// whose execute-api front is InvokeRestApi.
type harness struct {
	*kerneltest.K
	svc    *apigateway.Service
	lambda *lambda.Service
	fn     *fakeFunction
	edge   *edge.Handler
	// fnARN is the function proxyAPI created.
	fnARN string
}

// run runs fn with iam.enforce off.
func run(t *testing.T, fn func(h *harness), opts ...kerneltest.Option) {
	t.Helper()
	runMode(t, apigateway.EnforceOff, fn, opts...)
}

// modes are the three iam.enforce modes the enforce matrix tests run under.
var modes = map[string]apigateway.EnforceMode{"off": apigateway.EnforceOff, "advise": apigateway.EnforceAdvise, "enforce": apigateway.EnforceEnforce}

// runMode runs fn against the services in a kernel built with opts, the
// service seeing iam.enforce as mode (contract 7.1, Options.Enforce).
func runMode(t *testing.T, mode apigateway.EnforceMode, fn func(h *harness), opts ...kerneltest.Option) {
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
		svc := apigateway.New(k.Deps(apigatewayapi.Service()), apigateway.Options{FunctionPolicies: functions, Regions: aws.KnownRegions(), Enforce: mode})
		admin := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			k.Errorf("the admin API received %s %s", r.Method, r.URL)
		})
		e, err := edge.New(edge.Options{
			Kernel: k.Kernel, Registry: k.Kernel.Registry(), Identity: k.Identity, Regions: aws.KnownRegions(),
			Codecs: server.Codecs(), S3BaseDomains: []string{"localhost"}, Listener: ref.ListenerMain, Admin: admin,
			ExecuteAPI: []edge.ExecuteAPIService{{Op: invokeRestAPI, Owner: svc}},
		})
		if err != nil {
			k.Fatal(err)
		}
		h := &harness{K: k, svc: svc, lambda: functions, fn: fake, edge: e}
		defer fake.releaseAll()
		fn(h)
	}, append(opts, kerneltest.Services(apigatewayapi.Service(), lambdaapi.Service()))...)
}

// must runs op as a client request and fails the test on an error.
func must[In, Out any](h *harness, op kernel.Op[In, Out], in *In) *Out {
	h.Helper()
	out, err := h.Invoke(h.Context(), op, in)
	if err != nil {
		h.Fatalf("%s: %s %d %s", op.Descriptor().ID.Name, err.ID.JSONType, err.ID.Status, err.Message)
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

func errOf[Out any](_ *Out, err *awserr.Error) *awserr.Error { return err }

// The control plane's identities (apigateway-rest.md 2.4: the modeled
// shapes with their httpError, and the unmodeled ValidationException and
// SerializationException seen on the wire).
var (
	badRequest = apigatewayapi.IdentityBadRequestException()
	conflict   = apigatewayapi.IdentityConflictException()
	notFound   = apigatewayapi.IdentityNotFoundException()
	validation = apigatewayapi.IdentityValidationException()
)

// wantError checks an error's identity and status and that it says
// something; used where the text is inferred (contract preamble: no
// inferred text asserted).
func wantError(tb testing.TB, err *awserr.Error, want awserr.Identity) {
	tb.Helper()
	if err == nil {
		tb.Fatalf("no error, want %s %d", want.JSONType, want.Status)
	}
	if err.ID.Shape != want.Shape || err.ID.Status != want.Status || err.Message == "" {
		tb.Errorf("error = %s %d %q, want %s %d with a message", err.ID.JSONType, err.ID.Status, err.Message, want.JSONType, want.Status)
	}
}

// wantText checks an error's identity, status and recorded text.
func wantText(tb testing.TB, err *awserr.Error, want awserr.Identity, text string) {
	tb.Helper()
	wantError(tb, err, want)
	if err != nil && err.Message != text {
		tb.Errorf("message = %q\nwant %q", err.Message, text)
	}
}

// wantNotImplemented checks a valid but unemulated member's refusal: 501
// NotImplemented naming the member and saying nothing was changed (the
// unit's conflict row 4; CLAUDE.md invariant 3).
func wantNotImplemented(tb testing.TB, err *awserr.Error, member string) {
	tb.Helper()
	if err == nil {
		tb.Fatalf("no error, want NotImplemented naming %s", member)
	}
	if err.ID.Shape != apigatewayapi.IdentityNotImplemented().Shape || err.ID.Status != http.StatusNotImplemented {
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

// apiARN is a REST API's resource ARN, with the empty account field
// (apigateway-rest.md 3.9, 3.10; aws-doc arn-format-reference).
func apiARN(id string) string { return "arn:aws:apigateway:" + region + "::/restapis/" + id }

// executionARN is the execute-api ARN of an API in the harness scope, as
// Terraform's execution_arn builds it (apigateway-rest.md 3.10).
func (h *harness) executionARN(id string) string {
	return "arn:aws:execute-api:" + h.Scope.Region.String() + ":" + h.Scope.Account.String() + ":" + id
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// createAPI creates a REST API with only the required member.
func (h *harness) createAPI(name string) *apigatewayapi.RestApi {
	h.Helper()
	out := must(h, apigatewayapi.OpCreateRestApi, &apigatewayapi.CreateRestApiInput{Name: name})
	if out.Id == nil || *out.Id == "" || out.RootResourceId == nil || *out.RootResourceId == "" {
		h.Fatalf("CreateRestApi %s answered id %v, root %v", name, out.Id, out.RootResourceId)
	}
	return out
}

// api creates a REST API and returns its id and root resource id.
func (h *harness) api(name string) (id, root string) {
	h.Helper()
	out := h.createAPI(name)
	return *out.Id, *out.RootResourceId
}

// resource creates the path part under parent and returns its id.
func (h *harness) resource(apiID, parent, part string) string {
	h.Helper()
	out := must(h, apigatewayapi.OpCreateResource, &apigatewayapi.CreateResourceInput{RestApiId: apiID, ParentId: parent, PathPart: part})
	if out.Id == nil || *out.Id == "" {
		h.Fatalf("CreateResource %s answered no id", part)
	}
	return *out.Id
}

// method puts an unauthenticated method.
func (h *harness) method(apiID, res, verb string) {
	h.Helper()
	must(h, apigatewayapi.OpPutMethod, &apigatewayapi.PutMethodInput{RestApiId: apiID, ResourceId: res, HttpMethod: verb, AuthorizationType: "NONE"})
}

// invokeURI is the Lambda proxy integration URI of a function
// (apigateway-rest.md 3.4: the form every recorded Lambda test uses).
func invokeURI(fnARN string) string {
	return "arn:aws:apigateway:" + region + ":lambda:path/2015-03-31/functions/" + fnARN + "/invocations"
}

// proxyInput is an AWS_PROXY integration to fnARN with integration method
// POST ("For Lambda integrations, you must set the integration method to
// POST", aws-doc API_PutIntegration, apigateway-rest.md 3.4).
func proxyInput(apiID, res, verb, fnARN string) *apigatewayapi.PutIntegrationInput {
	return &apigatewayapi.PutIntegrationInput{
		RestApiId: apiID, ResourceId: res, HttpMethod: verb, Type: apigatewayapi.IntegrationTypeAwsProxy,
		IntegrationHttpMethod: new("POST"), Uri: new(invokeURI(fnARN)),
	}
}

// proxy puts a method and its AWS_PROXY integration to fnARN.
func (h *harness) proxy(apiID, res, verb, fnARN string) {
	h.Helper()
	h.method(apiID, res, verb)
	must(h, apigatewayapi.OpPutIntegration, proxyInput(apiID, res, verb, fnARN))
}

// deploy deploys the API to stage and returns the deployment id.
func (h *harness) deploy(apiID, stage string) string {
	h.Helper()
	out := must(h, apigatewayapi.OpCreateDeployment, &apigatewayapi.CreateDeploymentInput{RestApiId: apiID, StageName: new(stage)})
	if out.Id == nil || *out.Id == "" {
		h.Fatal("CreateDeployment answered no id")
	}
	return *out.Id
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

// allow adds the statement the console and the docs add so API Gateway may
// invoke fn: lambda:InvokeFunction for apigateway.amazonaws.com with the
// source ARN given (apigateway-rest.md 5.4).
func (h *harness) allow(fn, sid, sourceARN string) {
	h.Helper()
	must(h, lambdaapi.OpAddPermission, &lambdaapi.AddPermissionInput{
		FunctionName: fn, StatementId: sid, Action: "lambda:InvokeFunction", Principal: "apigateway.amazonaws.com", SourceArn: new(sourceARN),
	})
}

// proxyAPI builds the tracer bullet's API (spec 6b "Tracer bullet"):
// function fn, a REST API whose root has GET by AWS_PROXY to it, the
// given extra resource paths (each with ANY by AWS_PROXY), a permission for
// every stage, method and path ("${execution_arn}/*/*", apigateway-rest.md
// 3.10), and a deployment to stage s. It returns the API id and the
// deployment id.
func (h *harness) proxyAPI(paths ...string) (apiID, deployment string) {
	h.Helper()
	fnARN := h.createFunction("fn")
	h.fnARN = fnARN
	apiID, root := h.api("t")
	h.proxy(apiID, root, http.MethodGet, fnARN)
	ids := map[string]string{"/": root}
	for _, p := range paths {
		parent := "/"
		for part := range strings.SplitSeq(strings.TrimPrefix(p, "/"), "/") {
			full := strings.TrimSuffix(parent, "/") + "/" + part
			if _, ok := ids[full]; !ok {
				ids[full] = h.resource(apiID, ids[parent], part)
			}
			parent = full
		}
		h.proxy(apiID, ids[p], "ANY", fnARN)
	}
	h.allow("fn", "apigateway", h.executionARN(apiID)+"/*/*")
	return apiID, h.deploy(apiID, "s")
}

// invocation is one Invoke API Gateway made, as the fake function saw it.
type invocation struct {
	in     lambdaapi.InvokeInput
	mode   kernel.PrincipalMode
	origin ref.Origin
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
// call and answers with respond, a 200 proxy response by default.
type fakeFunction struct {
	mu      sync.Mutex
	calls   []invocation
	respond func(ctx context.Context, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error)
	release chan struct{}
}

func (f *fakeFunction) invoke(ctx context.Context, c *kernel.Call, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
	f.mu.Lock()
	f.calls = append(f.calls, invocation{in: *in, mode: c.Mode(), origin: c.Origin()})
	respond := f.respond
	f.mu.Unlock()
	if respond == nil {
		return payload(`{"statusCode":200,"body":"ok"}`), nil
	}
	return respond(ctx, in)
}

// answer makes the function answer with the payload p.
func (f *fakeFunction) answer(p string) {
	f.answerWith(payload(p), nil)
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
// (contract 6: /_understudy/execute-api/<api-id>/<stage>/<rest>).
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

// wantRequestIDs checks the headers every data-plane response carries:
// x-amzn-RequestId and x-amz-apigw-id (aws-recorded,
// test_apigateway_lambda.snapshot.json:L1675-L1708, apigateway-rest.md 4.8).
func wantRequestIDs(tb testing.TB, rec *httptest.ResponseRecorder, what string) {
	tb.Helper()
	if rec.Header().Get("X-Amzn-Requestid") == "" || rec.Header().Get("X-Amz-Apigw-Id") == "" {
		tb.Errorf("%s: headers %v, want x-amzn-RequestId and x-amz-apigw-id", what, rec.Header())
	}
}

// wantAnswer checks a response API Gateway writes itself (contract 7.7):
// the answer's status and exact body, Content-Type application/json
// (apigateway-rest.md 4.8), x-amzn-ErrorType when the answer has one
// (4.7), and the request id headers.
func wantAnswer(tb testing.TB, rec *httptest.ResponseRecorder, want apigateway.GatewayAnswer, what string) {
	tb.Helper()
	if rec.Code != want.Status || rec.Body.String() != want.Body {
		tb.Errorf("%s: response %d %s, want %d %s", what, rec.Code, rec.Body, want.Status, want.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		tb.Errorf("%s: Content-Type = %q, want application/json", what, ct)
	}
	if want.ErrorType != "" && rec.Header().Get("X-Amzn-Errortype") != want.ErrorType {
		tb.Errorf("%s: x-amzn-ErrorType = %q, want %q", what, rec.Header().Get("X-Amzn-Errortype"), want.ErrorType)
	}
	wantRequestIDs(tb, rec, what)
}

// want501 checks a data-plane request understudy cannot serve as AWS
// would: 501 in API Gateway's {"message": ...} shape naming the setting
// (any one of its spellings, compared case-insensitively) and saying
// nothing was changed (the unit's conflict row 3: never an AWS-shaped 500;
// contract 7.7).
func want501(tb testing.TB, rec *httptest.ResponseRecorder, setting, what string, spellings ...string) {
	tb.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		tb.Errorf("%s: body %s is not {\"message\": ...}: %v", what, rec.Body, err)
	}
	if rec.Code != http.StatusNotImplemented || !strings.Contains(body["message"], "nothing was changed") {
		tb.Errorf("%s: response %d %s, want 501 saying nothing was changed", what, rec.Code, rec.Body)
	}
	msg := strings.ToLower(body["message"])
	for _, s := range append([]string{setting}, spellings...) {
		if strings.Contains(msg, strings.ToLower(s)) {
			return
		}
	}
	tb.Errorf("%s: message %q does not name %s", what, body["message"], setting)
}

// invokeCalls returns the journaled InvokeRestApi calls.
func (h *harness) invokeCalls() []journal.APICall {
	h.Helper()
	var out []journal.APICall
	for _, e := range h.Journal.Query(journal.Filter{Kind: journal.KindAPICall, Operation: invokeRestAPI, Limit: 1000}).Events {
		if c, ok := e.Detail.(journal.APICall); ok {
			out = append(out, c)
		}
	}
	return out
}

// wantJournaled501 checks that the last InvokeRestApi call is journaled
// with its 501 (spec 6b: each refusal "answers 501 and is journaled").
func (h *harness) wantJournaled501(what string) {
	h.Helper()
	calls := h.invokeCalls()
	if len(calls) == 0 {
		h.Errorf("%s: no InvokeRestApi call journaled", what)
		return
	}
	if f, ok := calls[len(calls)-1].Outcome.(journal.Failed); !ok || f.Status != http.StatusNotImplemented {
		h.Errorf("%s: journaled outcome %#v, want a 501 failure", what, calls[len(calls)-1].Outcome)
	}
}
