package lambda_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
	"github.com/sustinbebustin/understudy/internal/service/lambda/furl"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool/pooltest"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
)

// urlHarness is the Execution with its function URLs bound, behind the
// edge.
type urlHarness struct {
	*execHarness
	edge *edge.Handler
}

func runURLs(t *testing.T, handler pooltest.Handler, o lambda.FunctionURLOptions, fn func(h *urlHarness)) {
	t.Helper()
	runExec(t, handler, func(h *execHarness) {
		h.x.BindFunctionURLs(h.Deps(lambdaapi.Service()), o)
		// The admin API shares the /_understudy/ prefix with the path
		// fallback, which must never reach it.
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
}

// urlFunction is the function the URL tests serve.
const urlFunction = "fn"

// createURL creates urlFunction and its function URL with authType and
// returns the URL's host, port included.
func (h *urlHarness) createURL(authType lambdaapi.FunctionUrlAuthType) string {
	h.Helper()
	h.createFunction(urlFunction)
	out := call(h.harness, lambdaapi.OpCreateFunctionUrlConfig, &lambdaapi.CreateFunctionUrlConfigInput{FunctionName: urlFunction, AuthType: authType})
	host, ok := strings.CutPrefix(strings.TrimSuffix(out.FunctionUrl, "/"), "http://")
	if !ok {
		h.Fatalf("FunctionUrl = %q, want http://<host>/", out.FunctionUrl)
	}
	return host
}

// allowPublic adds the two statements that open urlFunction's AuthType
// NONE URL to everyone, as Terraform's aws_lambda_function_url does
// (lambda-api.md section 11).
func (h *urlHarness) allowPublic() {
	h.Helper()
	h.allowPublicURLAction()
	h.allowPublicInvokeViaURL()
}

// allowPublicURLAction adds the lambda:InvokeFunctionUrl statement, the
// only one AWS required before October 2025.
func (h *urlHarness) allowPublicURLAction() {
	h.Helper()
	call(h.harness, lambdaapi.OpAddPermission, &lambdaapi.AddPermissionInput{
		FunctionName: urlFunction, StatementId: "FunctionURLAllowPublicAccess", Action: "lambda:InvokeFunctionUrl", Principal: "*",
		FunctionUrlAuthType: new(lambdaapi.FunctionUrlAuthTypeNone),
	})
}

// allowPublicInvokeViaURL adds the lambda:InvokeFunction statement limited
// to invocations through a function URL.
func (h *urlHarness) allowPublicInvokeViaURL() {
	h.Helper()
	call(h.harness, lambdaapi.OpAddPermission, &lambdaapi.AddPermissionInput{
		FunctionName: urlFunction, StatementId: "FunctionURLAllowInvokeAction", Action: "lambda:InvokeFunction", Principal: "*",
		InvokedViaFunctionUrl: new(true),
	})
}

func (h *urlHarness) request(method, url, body string) *http.Request {
	h.Helper()
	return httptest.NewRequestWithContext(h.Context(), method, url, strings.NewReader(body))
}

// send runs r through the edge.
func (h *urlHarness) send(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.edge.ServeHTTP(rec, r)
	return rec
}

// expect checks a response's status and body.
func expect(tb testing.TB, rec *httptest.ResponseRecorder, status int, body string) {
	tb.Helper()
	if rec.Code != status || rec.Body.String() != body {
		tb.Fatalf("response %d %s\nwant     %d %s", rec.Code, rec.Body, status, body)
	}
}

const forbidden = `{"Message":"Forbidden. For troubleshooting Function URL authorization issues, see: https://docs.aws.amazon.com/lambda/latest/dg/urls-auth.html"}`

// TestFunctionURLRequestEventGolden sends the Lambda guide's example
// request (lambda-events.md 7.3) through the edge to a function that
// echoes its event, which the URL answers as the JSON body. The request
// carries no trace, so x-amzn-trace-id is the trace the function ran
// under.
func TestFunctionURLRequestEventGolden(t *testing.T) {
	var mu sync.Mutex
	var trace string
	echo := func(ctx context.Context, inv runtimeapi.Invocation) (runtimeapi.Outcome, error) {
		mu.Lock()
		trace = inv.TraceID
		mu.Unlock()
		return pooltest.Echo(ctx, inv)
	}
	runURLs(t, echo, lambda.FunctionURLOptions{}, func(h *urlHarness) {
		host := h.createURL(lambdaapi.FunctionUrlAuthTypeNone)
		h.allowPublic()
		r := h.request(http.MethodPost, "http://"+host+"/my/path?parameter1=value1&parameter1=value2&parameter2=value", "Hello from client!")
		r.Header.Set("Content-Type", "text/plain")
		r.Header.Set("Header1", "value1")
		r.Header.Add("Header2", "value1")
		r.Header.Add("Header2", "value2")
		r.Header.Add("Cookie", "cookie1")
		r.Header.Add("Cookie", "cookie2")
		r.Header.Set("User-Agent", "agent")
		rec := h.send(r)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("response %d %q %s, want 200 application/json", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
		}
		raw, err := os.ReadFile("furl/testdata/request-edge.json")
		if err != nil {
			t.Fatal(err)
		}
		id, _, _ := strings.Cut(host, ".")
		requestID := rec.Header().Get("X-Amzn-Requestid")
		mu.Lock()
		ran := trace
		mu.Unlock()
		if ran == "" {
			t.Fatal("the function ran without a trace id")
		}
		want := strings.NewReplacer("{{URL_ID}}", id, "{{REQUEST_ID}}", requestID, "{{TRACE_ID}}", ran).Replace(string(raw))
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(want)); err != nil {
			t.Fatal(err)
		}
		if rec.Body.String() != compact.String() {
			t.Errorf("event\n%s\nwant\n%s", rec.Body, compact.String())
		}
		if inv := h.invocations(); len(inv) != 1 || requestID == "" {
			t.Errorf("Invocation events = %d with request id %q, want one invocation answering with its id", len(inv), requestID)
		}
	})
}

// A client's X-Amzn-Trace-Id is the trace the event reports and the one
// the function runs under, as on AWS.
func TestFunctionURLRunsUnderTheClientsTrace(t *testing.T) {
	const clientTrace = "Root=1-67891233-abcdef012345678912345678;Parent=463ac35c9f6413ad;Sampled=1"
	var mu sync.Mutex
	var ran string
	echo := func(ctx context.Context, inv runtimeapi.Invocation) (runtimeapi.Outcome, error) {
		mu.Lock()
		ran = inv.TraceID
		mu.Unlock()
		return pooltest.Echo(ctx, inv)
	}
	runURLs(t, echo, lambda.FunctionURLOptions{}, func(h *urlHarness) {
		host := h.createURL(lambdaapi.FunctionUrlAuthTypeNone)
		h.allowPublic()
		r := h.request(http.MethodGet, "http://"+host+"/", "")
		r.Header.Set("X-Amzn-Trace-Id", clientTrace)
		rec := h.send(r)
		var ev struct {
			Headers map[string]string `json:"headers"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &ev); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("response %d %s, want 200 echoing the event", rec.Code, rec.Body)
		}
		mu.Lock()
		defer mu.Unlock()
		if ev.Headers["x-amzn-trace-id"] != clientTrace || ran != clientTrace {
			t.Errorf("event x-amzn-trace-id %q, function trace %q; want both %q", ev.Headers["x-amzn-trace-id"], ran, clientTrace)
		}
	})
}

func TestFunctionURLWithAuthTypeNoneNeedsAPublicPolicyStatement(t *testing.T) {
	runURLs(t, pooltest.Echo, lambda.FunctionURLOptions{}, func(h *urlHarness) {
		host := h.createURL(lambdaapi.FunctionUrlAuthTypeNone)
		rec := h.send(h.request(http.MethodGet, "http://"+host+"/", ""))
		expect(t, rec, http.StatusForbidden, forbidden)
		if rec.Header().Get("X-Amzn-Errortype") != "AccessDeniedException" || rec.Header().Get("X-Amzn-Requestid") == "" {
			t.Errorf("headers %v, want x-amzn-ErrorType AccessDeniedException and a request id", rec.Header())
		}
		if n := len(h.started()); n != 0 {
			t.Errorf("a refused request started %d containers", n)
		}
		// Since October 2025 the lambda:InvokeFunctionUrl statement alone
		// no longer opens the URL.
		h.allowPublicURLAction()
		expect(t, h.send(h.request(http.MethodGet, "http://"+host+"/", "")), http.StatusForbidden, forbidden)
		h.allowPublicInvokeViaURL()
		rec = h.send(h.request(http.MethodGet, "http://"+host+"/", ""))
		var ev struct {
			RawPath string `json:"rawPath"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &ev); rec.Code != http.StatusOK || err != nil || ev.RawPath != "/" {
			t.Errorf("with the statement: %d %s, want 200 echoing the event", rec.Code, rec.Body)
		}
	})
}

func TestFunctionURLOfAnUnknownIDIsForbiddenWithANullMessage(t *testing.T) {
	runURLs(t, pooltest.Echo, lambda.FunctionURLOptions{}, func(h *urlHarness) {
		rec := h.send(h.request(http.MethodGet, "http://nosuchurl.lambda-url.us-east-1.localhost:4566/", ""))
		expect(t, rec, http.StatusForbidden, `{"Message":null}`)
		if rec.Header().Get("X-Amzn-Errortype") != "AccessDeniedException" || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("headers %v, want x-amzn-ErrorType AccessDeniedException, application/json", rec.Header())
		}
		host := h.createURL(lambdaapi.FunctionUrlAuthTypeNone)
		h.allowPublic()
		other := strings.Replace(host, "us-east-1", "eu-west-1", 1)
		expect(t, h.send(h.request(http.MethodGet, "http://"+other+"/", "")), http.StatusForbidden, `{"Message":null}`)
	})
}

// A deleted function URL, directly or with its alias or function, answers
// the Forbidden help message, unlike an id never minted
// (test_lambda_url_persists_after_alias_delete).
func TestFunctionURLOfADeletedConfigurationIsForbidden(t *testing.T) {
	runURLs(t, pooltest.Echo, lambda.FunctionURLOptions{}, func(h *urlHarness) {
		host := h.createURL(lambdaapi.FunctionUrlAuthTypeNone)
		h.allowPublic()
		call(h.harness, lambdaapi.OpPublishVersion, &lambdaapi.PublishVersionInput{FunctionName: urlFunction})
		call(h.harness, lambdaapi.OpCreateAlias, &lambdaapi.CreateAliasInput{FunctionName: urlFunction, Name: "live", FunctionVersion: "1"})
		aliased := call(h.harness, lambdaapi.OpCreateFunctionUrlConfig, &lambdaapi.CreateFunctionUrlConfigInput{FunctionName: urlFunction, Qualifier: new("live"), AuthType: lambdaapi.FunctionUrlAuthTypeNone}).FunctionUrl

		call(h.harness, lambdaapi.OpDeleteAlias, &lambdaapi.DeleteAliasInput{FunctionName: urlFunction, Name: "live"})
		expect(t, h.send(h.request(http.MethodGet, aliased, "")), http.StatusForbidden, forbidden)
		call(h.harness, lambdaapi.OpDeleteFunctionUrlConfig, &lambdaapi.DeleteFunctionUrlConfigInput{FunctionName: urlFunction})
		expect(t, h.send(h.request(http.MethodGet, "http://"+host+"/", "")), http.StatusForbidden, forbidden)

		host = strings.TrimSuffix(strings.TrimPrefix(call(h.harness, lambdaapi.OpCreateFunctionUrlConfig, &lambdaapi.CreateFunctionUrlConfigInput{FunctionName: urlFunction, AuthType: lambdaapi.FunctionUrlAuthTypeNone}).FunctionUrl, "http://"), "/")
		call(h.harness, lambdaapi.OpDeleteFunction, &lambdaapi.DeleteFunctionInput{FunctionName: urlFunction})
		expect(t, h.send(h.request(http.MethodGet, "http://"+host+"/", "")), http.StatusForbidden, forbidden)
		if n := len(h.started()); n != 0 {
			t.Errorf("requests to deleted URLs started %d containers", n)
		}
	})
}

// The path fallback and the host form reach the same function through the
// edge, with the same URL and path (ARCHITECTURE.md 4.1 rules 1 and 2).
func TestFunctionURLPathFallbackAndHostFormReachTheSameFunction(t *testing.T) {
	runURLs(t, pooltest.Echo, lambda.FunctionURLOptions{}, func(h *urlHarness) {
		host := h.createURL(lambdaapi.FunctionUrlAuthTypeNone)
		h.allowPublic()
		id, _, _ := strings.Cut(host, ".")
		viaHost := h.send(h.request(http.MethodGet, "http://"+host+"/a/b?x=1", ""))
		viaPath := h.send(h.request(http.MethodGet, "http://127.0.0.1:4566"+furl.PathPrefix+id+"/a/b?x=1", ""))
		type seen struct {
			RawPath        string `json:"rawPath"`
			RawQuery       string `json:"rawQueryString"`
			RequestContext struct {
				APIID      string `json:"apiId"`
				DomainName string `json:"domainName"`
			} `json:"requestContext"`
		}
		var a, b seen
		if err := json.Unmarshal(viaHost.Body.Bytes(), &a); err != nil {
			t.Fatalf("host form: %d %s", viaHost.Code, viaHost.Body)
		}
		if err := json.Unmarshal(viaPath.Body.Bytes(), &b); err != nil {
			t.Fatalf("path fallback: %d %s", viaPath.Code, viaPath.Body)
		}
		if a != b || a.RawPath != "/a/b" || a.RawQuery != "x=1" || a.RequestContext.APIID != id || a.RequestContext.DomainName != id+".lambda-url.us-east-1.localhost" {
			t.Errorf("host form saw %+v, path fallback %+v; want the same URL and path", a, b)
		}
		events := h.invocations()
		if len(events) != 2 || invocationOf(t, events[0]).Function != invocationOf(t, events[1]).Function {
			t.Errorf("invocations = %d, want both forms to run fn", len(events))
		}
	})
}

// failing ends each invocation as its event's rawPath asks: a function
// error, a timeout or a malformed response.
func failing(_ context.Context, inv runtimeapi.Invocation) (runtimeapi.Outcome, error) {
	var ev struct {
		RawPath string `json:"rawPath"`
	}
	if err := json.Unmarshal(inv.Payload, &ev); err != nil {
		return nil, err
	}
	switch ev.RawPath {
	case "/error":
		return runtimeapi.FunctionError{Type: "Runtime.Unknown", Payload: []byte(`{"errorMessage":"boom"}`)}, nil
	case "/timeout":
		return runtimeapi.Timeout{After: inv.Timeout}, nil
	}
	return runtimeapi.Success{Payload: []byte(`{"statusCode":"teapot"}`)}, nil
}

func TestFunctionURLAnswersFunctionErrorsAndMalformedResponsesWith502(t *testing.T) {
	runURLs(t, failing, lambda.FunctionURLOptions{}, func(h *urlHarness) {
		host := h.createURL(lambdaapi.FunctionUrlAuthTypeNone)
		h.allowPublic()
		for _, path := range []string{"/error", "/timeout", "/malformed"} {
			rec := h.send(h.request(http.MethodGet, "http://"+host+path, ""))
			expect(t, rec, http.StatusBadGateway, "Internal Server Error")
			if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
				t.Errorf("%s: Content-Type = %q, want text/plain", path, ct)
			}
		}
	})
}

// The function's structured answer becomes the HTTP response.
func TestFunctionURLSendsTheFunctionsStructuredResponse(t *testing.T) {
	answer := func(context.Context, runtimeapi.Invocation) (runtimeapi.Outcome, error) {
		return runtimeapi.Success{Payload: []byte(`{"statusCode":418,"headers":{"X-Tea":"pot"},"cookies":["a=1"],"body":"i'm a teapot"}`)}, nil
	}
	runURLs(t, answer, lambda.FunctionURLOptions{}, func(h *urlHarness) {
		host := h.createURL(lambdaapi.FunctionUrlAuthTypeNone)
		h.allowPublic()
		rec := h.send(h.request(http.MethodGet, "http://"+host+"/", ""))
		expect(t, rec, http.StatusTeapot, "i'm a teapot")
		if rec.Header().Get("X-Tea") != "pot" || rec.Header().Get("Set-Cookie") != "a=1" || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("headers %v, want X-Tea, Set-Cookie and the default application/json", rec.Header())
		}
	})
}

func TestFunctionURLAnswersCORSPreflightsWithoutRunningTheFunction(t *testing.T) {
	runURLs(t, pooltest.Echo, lambda.FunctionURLOptions{}, func(h *urlHarness) {
		h.createFunction("fn")
		out := call(h.harness, lambdaapi.OpCreateFunctionUrlConfig, &lambdaapi.CreateFunctionUrlConfigInput{
			FunctionName: "fn", AuthType: lambdaapi.FunctionUrlAuthTypeNone,
			Cors: &lambdaapi.Cors{AllowOrigins: []string{"https://example.com"}, AllowMethods: []string{"POST"}, ExposeHeaders: []string{"x-id"}},
		})
		h.allowPublic()
		url := out.FunctionUrl
		pre := h.request(http.MethodOptions, url, "")
		pre.Header.Set("Origin", "https://example.com")
		pre.Header.Set("Access-Control-Request-Method", "POST")
		rec := h.send(pre)
		if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "https://example.com" || rec.Header().Get("Access-Control-Allow-Methods") != "POST" {
			t.Errorf("preflight %d %v, want 200 with the configured CORS headers", rec.Code, rec.Header())
		}
		if n := len(h.invocations()); n != 0 {
			t.Errorf("the preflight ran the function %d times", n)
		}
		get := h.request(http.MethodGet, url, "")
		get.Header.Set("Origin", "https://example.com")
		rec = h.send(get)
		if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "https://example.com" || rec.Header().Get("Access-Control-Expose-Headers") != "x-id" {
			t.Errorf("GET %d %v, want 200 with the CORS headers", rec.Code, rec.Header())
		}
	})
}

// Browsers never sign preflights, so an AWS_IAM URL answers an unsigned
// one; the request it precedes still needs a signature.
func TestFunctionURLWithAuthTypeAWSIAMAnswersUnsignedCORSPreflights(t *testing.T) {
	runURLs(t, pooltest.Echo, lambda.FunctionURLOptions{}, func(h *urlHarness) {
		h.createFunction(urlFunction)
		out := call(h.harness, lambdaapi.OpCreateFunctionUrlConfig, &lambdaapi.CreateFunctionUrlConfigInput{
			FunctionName: urlFunction, AuthType: lambdaapi.FunctionUrlAuthTypeAwsIam,
			Cors: &lambdaapi.Cors{AllowOrigins: []string{"https://example.com"}, AllowMethods: []string{"POST"}},
		})
		pre := h.request(http.MethodOptions, out.FunctionUrl, "")
		pre.Header.Set("Origin", "https://example.com")
		pre.Header.Set("Access-Control-Request-Method", "POST")
		rec := h.send(pre)
		if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "https://example.com" {
			t.Errorf("unsigned preflight %d %v, want 200 with the configured CORS headers", rec.Code, rec.Header())
		}
		post := h.request(http.MethodPost, out.FunctionUrl, "{}")
		post.Header.Set("Origin", "https://example.com")
		expect(t, h.send(post), http.StatusForbidden, forbidden)
		if n := len(h.invocations()); n != 0 {
			t.Errorf("the preflight and the unsigned POST ran the function %d times", n)
		}
	})
}

// signedRequest is a GET of url signed for lambda in the default region
// with creds.
func (h *urlHarness) signedRequest(url string, creds identity.Credentials) *http.Request {
	h.Helper()
	return h.signedFor(url, creds, aws.DefaultRegion(), "lambda")
}

// signedFor is a GET of url signed with creds under the credential scope
// of region and service.
func (h *urlHarness) signedFor(url string, creds identity.Credentials, region aws.Region, service string) *http.Request {
	h.Helper()
	r := h.request(http.MethodGet, url, "")
	auth.Sign(r, nil, auth.Signer{
		AccessKeyID: string(creds.AccessKeyID), SecretAccessKey: string(creds.SecretAccessKey),
		Region: region, Service: service,
	}, h.Clock.Wall())
	return r
}

// presignedURL is url presigned for a GET under lambda in the default
// region with creds, valid for expires.
func (h *urlHarness) presignedURL(url string, creds identity.Credentials, expires time.Duration) string {
	h.Helper()
	r := h.request(http.MethodGet, url, "")
	auth.Presign(r, auth.Signer{
		AccessKeyID: string(creds.AccessKeyID), SecretAccessKey: string(creds.SecretAccessKey),
		Region: aws.DefaultRegion(), Service: "lambda",
	}, expires, h.Clock.Wall())
	return r.URL.String()
}

func TestFunctionURLWithAuthTypeAWSIAMNeedsASignature(t *testing.T) {
	runURLs(t, pooltest.Echo, lambda.FunctionURLOptions{}, func(h *urlHarness) {
		host := h.createURL(lambdaapi.FunctionUrlAuthTypeAwsIam)
		expect(t, h.send(h.request(http.MethodGet, "http://"+host+"/", "")), http.StatusForbidden, forbidden)
		creds, err := h.Identity.MintLongTerm(identity.Root{Account: aws.DefaultAccount()})
		if err != nil {
			t.Fatal(err)
		}
		rec := h.send(h.signedRequest("http://"+host+"/", creds))
		var ev struct {
			RequestContext struct {
				AccountID  string `json:"accountId"`
				Authorizer struct {
					IAM struct {
						AccessKey string `json:"accessKey"`
						UserARN   string `json:"userArn"`
					} `json:"iam"`
				} `json:"authorizer"`
			} `json:"requestContext"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &ev); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("signed request: %d %s, want 200", rec.Code, rec.Body)
		}
		iam := ev.RequestContext.Authorizer.IAM
		if iam.AccessKey != string(creds.AccessKeyID) || iam.UserARN != "arn:aws:iam::000000000000:root" || ev.RequestContext.AccountID != "000000000000" {
			t.Errorf("requestContext = %+v, want the signer under authorizer.iam", ev.RequestContext)
		}
		used := h.Access.Usage(access.Filter{Service: "lambda"})
		if !strings.Contains(useActions(used), "lambda:InvokeFunctionUrl") {
			t.Errorf("access record %s, want lambda:InvokeFunctionUrl", useActions(used))
		}
		calls := h.Journal.Query(journal.Filter{Operation: ref.OperationID{Service: "lambda", Name: "InvokeFunctionUrl"}, Limit: 10}).Events
		if len(calls) != 2 {
			t.Errorf("journaled InvokeFunctionUrl calls = %d, want 2", len(calls))
		}
	})
}

func TestFunctionURLVerifiesSignaturesUnderSigV4Verify(t *testing.T) {
	runURLs(t, pooltest.Echo, lambda.FunctionURLOptions{SigV4: lambda.SigV4Verify}, func(h *urlHarness) {
		host := h.createURL(lambdaapi.FunctionUrlAuthTypeAwsIam)
		creds, err := h.Identity.MintLongTerm(identity.Root{Account: aws.DefaultAccount()})
		if err != nil {
			t.Fatal(err)
		}
		if rec := h.send(h.signedRequest("http://"+host+"/", creds)); rec.Code != http.StatusOK {
			t.Fatalf("a valid signature: %d %s, want 200", rec.Code, rec.Body)
		}
		forged := creds
		forged.SecretAccessKey = "wrong" + forged.SecretAccessKey[5:]
		rec := h.send(h.signedRequest("http://"+host+"/", forged))
		if rec.Code != http.StatusForbidden || rec.Header().Get("X-Amzn-Errortype") != "InvalidSignatureException" {
			t.Errorf("a forged signature: %d %v %s, want 403 InvalidSignatureException", rec.Code, rec.Header(), rec.Body)
		}
		euWest1, err := aws.KnownRegions().Parse("eu-west-1")
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name, body string
			r          *http.Request
		}{
			{"signed for s3", `{"Message":"Credential should be scoped to correct service: 'lambda'. "}`, h.signedFor("http://"+host+"/", creds, aws.DefaultRegion(), "s3")},
			{"signed for eu-west-1", `{"Message":"Credential should be scoped to a valid region, not 'eu-west-1'. "}`, h.signedFor("http://"+host+"/", creds, euWest1, "lambda")},
		} {
			rec := h.send(tc.r)
			expect(t, rec, http.StatusForbidden, tc.body)
			if rec.Header().Get("X-Amzn-Errortype") != "InvalidSignatureException" {
				t.Errorf("%s: x-amzn-ErrorType %q, want InvalidSignatureException", tc.name, rec.Header().Get("X-Amzn-Errortype"))
			}
		}
		if n := len(h.invocations()); n != 1 {
			t.Errorf("invocations = %d, want only the valid signature's", n)
		}
	})
}

// Every key with a secret verifies, as at the edge: the dummy pair
// test/test and a 12-digit account key with the secret test, while an
// unknown key with no secret is refused.
func TestFunctionURLVerifiesEveryKeyWithASecret(t *testing.T) {
	runURLs(t, pooltest.Echo, lambda.FunctionURLOptions{SigV4: lambda.SigV4Verify}, func(h *urlHarness) {
		host := h.createURL(lambdaapi.FunctionUrlAuthTypeAwsIam)
		for _, key := range []ids.AccessKeyID{identity.DummyAccessKeyID, "111111111111"} {
			creds := identity.Credentials{AccessKeyID: key, SecretAccessKey: identity.DummySecret}
			if rec := h.send(h.signedRequest("http://"+host+"/", creds)); rec.Code != http.StatusOK {
				t.Errorf("signed with %s/test: %d %s, want 200", key, rec.Code, rec.Body)
			}
		}
		unknown := identity.Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"} //nolint:gosec // G101: AWS's published example key, not a credential
		rec := h.send(h.signedRequest("http://"+host+"/", unknown))
		if rec.Code != http.StatusForbidden || rec.Header().Get("X-Amzn-Errortype") != "UnrecognizedClientException" {
			t.Errorf("an unknown key: %d %v %s, want 403 UnrecognizedClientException", rec.Code, rec.Header(), rec.Body)
		}
	})
}

// A presigned URL runs the function until it expires on the emulator
// clock.
func TestFunctionURLVerifiesPresignedURLsUnderSigV4Verify(t *testing.T) {
	runURLs(t, pooltest.Echo, lambda.FunctionURLOptions{SigV4: lambda.SigV4Verify}, func(h *urlHarness) {
		host := h.createURL(lambdaapi.FunctionUrlAuthTypeAwsIam)
		creds, err := h.Identity.MintLongTerm(identity.Root{Account: aws.DefaultAccount()})
		if err != nil {
			t.Fatal(err)
		}
		url := h.presignedURL("http://"+host+"/", creds, time.Minute)
		if rec := h.send(h.request(http.MethodGet, url, "")); rec.Code != http.StatusOK {
			t.Fatalf("a valid presigned URL: %d %s, want 200", rec.Code, rec.Body)
		}
		if _, err := h.Clock.Advance(2 * time.Minute); err != nil {
			t.Fatal(err)
		}
		if rec := h.send(h.request(http.MethodGet, url, "")); rec.Code != http.StatusForbidden {
			t.Errorf("an expired presigned URL: %d %s, want 403", rec.Code, rec.Body)
		}
		if n := len(h.invocations()); n != 1 {
			t.Errorf("invocations = %d, want only the unexpired URL's", n)
		}
	})
}

func TestFunctionURLRefusesBodiesOverTheSynchronousLimit(t *testing.T) {
	runURLs(t, pooltest.Echo, lambda.FunctionURLOptions{}, func(h *urlHarness) {
		host := h.createURL(lambdaapi.FunctionUrlAuthTypeNone)
		h.allowPublic()
		r := httptest.NewRequestWithContext(h.Context(), http.MethodPost, "http://"+host+"/", io.LimitReader(zeros{}, 6_291_457))
		rec := h.send(r)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("a 6 MiB + 1 body: %d %s, want 413", rec.Code, rec.Body)
		}
		if n := len(h.invocations()); n != 0 {
			t.Errorf("the refused request ran the function %d times", n)
		}
	})
}

// zeros reads as an endless run of zero bytes.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func useActions(uses []access.Use) string {
	names := make([]string, len(uses))
	for i := range uses {
		names[i] = uses[i].Action.Prefix + ":" + uses[i].Action.Name
	}
	return strings.Join(names, ",")
}
