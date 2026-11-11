// Package lambdaproxy is the Lambda proxy integration plumbing the API
// Gateway REST API (apigateway) and HTTP API (apigatewayv2) data planes
// share: the bounded body read, the request the event reports, the
// resource-policy check of the function, and the synchronous invocation
// bounded by the integration timeout (apigateway-rest.md 5;
// apigateway-http.md 5.5, 5.6, 13.3). Each service keeps what differs: its
// gateway answers, its payload formats and how it maps an invocation's end
// onto an answer.
package lambdaproxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/httpevent"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// ServicePrincipal is whom API Gateway invokes a Lambda proxy integration
// as (apigateway-rest.md 5.4, apigateway-http.md 5.5).
const ServicePrincipal = "apigateway.amazonaws.com"

// ReadBody reads a request's body, reporting one over limit bytes as too
// large without keeping it.
func ReadBody(r *http.Request, limit int) (body []byte, tooLarge bool, err *awserr.Error) {
	if r.Body == nil {
		return nil, false, nil
	}
	b, rerr := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
	if rerr != nil {
		return nil, false, awserr.RequestAborted()
	}
	if len(b) > limit {
		return nil, true, nil
	}
	return b, false, nil
}

// EventRequest is the request as the event reports it, rawPath being the
// path the API sees as the service's payload format reports it: escaped
// for a REST API, decoded for an HTTP API, a stage segment included.
func EventRequest(c *kernel.Call, r *http.Request, rawPath string, body []byte) httpevent.Request {
	e := c.Endpoint()
	scheme := e.Scheme
	if scheme == "" {
		scheme = "http"
	}
	port := "80"
	if scheme == "https" {
		port = "443"
	}
	host := e.Host
	if host == "" {
		host = r.Host
	}
	if _, p, err := net.SplitHostPort(host); err == nil && p != "" {
		port = p
	}
	source := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		source = h
	}
	return httpevent.Request{
		Method: r.Method, RawPath: rawPath, RawQuery: r.URL.RawQuery, Protocol: r.Proto,
		Host: r.Host, Header: r.Header, SourceIP: source, Scheme: scheme, Port: port, Body: body,
	}
}

// EscapedPath is the escaped path exactly as the client sent it: the
// request target up to its query.
func EscapedPath(r *http.Request) string {
	if strings.HasPrefix(r.RequestURI, "/") {
		path, _, _ := strings.Cut(r.RequestURI, "?")
		return path
	}
	return r.URL.EscapedPath()
}

// HostName is a Host header without its port, lowercase.
func HostName(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.Trim(host, "[]"))
}

// InvokeFunctionAction is the action the function's resource policy must
// allow API Gateway (apigateway-rest.md 5.4, apigateway-http.md 8.4).
func InvokeFunctionAction() registry.IAMAction {
	return registry.IAMAction{Prefix: "lambda", Name: "InvokeFunction"}
}

// errNoFunctionPolicies is a service built without a function policy
// provider.
var errNoFunctionPolicies = errors.New("lambdaproxy: no function policy provider was given, so no function's resource policy can be read; " +
	"wire.go passes the Lambda service as the API Gateway services' Options.FunctionPolicies")

// Permitted reports whether fn's resource policy, read from policies, lets
// API Gateway invoke it for source on behalf of the API's owner
// (apigateway-rest.md 5.4; apigateway-http.md finding 9, 13.3.1). It
// applies under every iam.enforce mode; a function that does not exist
// allows nothing.
func Permitted(policies authz.PolicyProvider, fn, source aws.ARN, owner aws.Scope) (bool, *awserr.Error) {
	if policies == nil {
		return false, awserr.Internalf(errNoFunctionPolicies)
	}
	doc, ok := policies.ResourcePolicy(fn)
	if !ok {
		return false, nil
	}
	_, allowed := authz.CheckResource(doc, authz.ResourceRequest{
		Caller:   policydoc.ServiceCaller{Name: ServicePrincipal},
		Action:   InvokeFunctionAction(),
		Resource: fn.String(),
		Keys:     map[string]string{"aws:SourceArn": source.String(), "aws:SourceAccount": owner.Account.String()},
	}).(policydoc.Allowed)
	return allowed, nil
}

// Invocation is a synchronous invocation of a Lambda proxy integration's
// function.
type Invocation struct {
	// Function is the function's ARN and Scope its account and region.
	Function aws.ARN
	Scope    aws.Scope
	// Source is the aws:SourceArn API Gateway presents for the request.
	Source aws.ARN
	// Event is the request event of the integration's payload format.
	Event []byte
}

// Waited is how the wait for an invocation ended.
//
//sumtype:decl
type Waited interface{ isWaited() }

// Output is Lambda's output for the invocation.
type Output struct{ Out *lambdaapi.InvokeOutput }

// Refusal is the error Lambda refused the invocation with.
type Refusal struct{ Err *awserr.Error }

// TimedOut is an invocation still running when the integration's timeout
// passed.
type TimedOut struct{}

// ClientGone is a client that went away before Lambda answered.
type ClientGone struct{}

func (Output) isWaited()     {}
func (Refusal) isWaited()    {}
func (TimedOut) isWaited()   {}
func (ClientGone) isWaited() {}

// InvokeWithin invokes the function as API Gateway and waits for Lambda's
// answer at most timeout of wall time. The integration timeout bounds real
// work in a container, as the function timeout does, so it runs on wall
// time and a frozen or scaled emulator clock neither stretches nor shortens
// it (apigateway-http.md 13.3 item 6, ARCHITECTURE.md 9). The invocation
// runs as tracked work of k, so settling waits for it, and it outlives the
// wait: past the timeout the function keeps running until it ends, its own
// timeout ends it or understudy stops, and its answer is dropped (inferred:
// what becomes of an invocation past the integration timeout is
// unrecorded, apigateway-http.md 5.6, APIGW1-10, APIGW2-10).
func InvokeWithin(ctx context.Context, k *kernel.Kernel, c *kernel.Call, timeout time.Duration, inv Invocation) Waited {
	t := kernel.Target{Scope: inv.Scope, As: kernel.AsService{Name: ServicePrincipal, Source: inv.Source}, Via: ref.ViaAPIGateway}
	in := &lambdaapi.InvokeInput{FunctionName: inv.Function.String(), InvocationType: new(lambdaapi.InvocationTypeRequestResponse), Payload: inv.Event}
	done := make(chan Waited, 1)
	k.Go(kernel.InvocationRunning, func(work context.Context) { //nolint:contextcheck // the invocation outlives the request as tracked work of the kernel
		out, err := kernel.Invoke(work, k, c, lambdaapi.OpInvoke, t, in)
		if err != nil {
			done <- Refusal{err}
			return
		}
		done <- Output{out}
	})
	wait, stop := context.WithCancel(ctx)
	defer stop()
	clk := k.Clock()
	expired := clk.Until(wait, clk.WallDeadline(clk.Wall().Add(timeout)))
	select {
	case r := <-done:
		return r
	case <-expired:
		if ctx.Err() != nil {
			return ClientGone{}
		}
		return TimedOut{}
	}
}
