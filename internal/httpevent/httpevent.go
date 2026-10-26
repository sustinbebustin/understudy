// Package httpevent builds the Lambda payload format 2.0 request event and
// reads the function's response, for Lambda function URLs and API Gateway
// HTTP APIs alike (lambda-events.md 7.3, 7.4; apigateway-http.md 5.1, 5.3).
// It is pure and imports nothing in the repository: the caller resolves
// the route, authorizes the request and invokes the function.
package httpevent

import (
	"errors"
	"net/http"
	"time"
)

// Request is the HTTP request the event reports.
type Request struct {
	Method string
	// RawPath is the escaped path as sent, without any path fallback's
	// prefix; RawQuery the query as sent, without its "?".
	RawPath  string
	RawQuery string
	// Protocol is the request's protocol, such as HTTP/1.1.
	Protocol string
	// Host is the Host header, which net/http keeps out of Header.
	Host   string
	Header http.Header
	// SourceIP is the address of the client's TCP connection.
	SourceIP string
	// Scheme and Port are what the x-forwarded-proto and x-forwarded-port
	// headers the front end adds report.
	Scheme string
	Port   string
	Body   []byte
}

// Context is what the event reports beyond the request.
type Context struct {
	// AccountID is requestContext.accountId.
	AccountID string
	// APIID, DomainName and DomainPrefix are requestContext's apiId,
	// domainName and domainPrefix.
	APIID        string
	DomainName   string
	DomainPrefix string
	// RouteKey is routeKey and requestContext.routeKey; Stage is
	// requestContext.stage.
	RouteKey string
	Stage    string
	// PathParameters and StageVariables are left out of the event when
	// empty.
	PathParameters map[string]string
	StageVariables map[string]string
	// RequestID is requestContext.requestId.
	RequestID string
	// At is the request time on the emulator clock.
	At time.Time
	// TraceID is reported as the x-amzn-trace-id header when not empty.
	TraceID    string
	Authorizer Authorizer
}

// Authorizer is what requestContext.authorizer reports.
//
//sumtype:decl
type Authorizer interface{ isAuthorizer() }

// NoAuthorizer leaves requestContext.authorizer out.
type NoAuthorizer struct{}

// IAMAuthorizer is requestContext.authorizer.iam.
type IAMAuthorizer struct {
	AccessKey string
	AccountID string
	CallerID  string
	UserARN   string
	UserID    string
}

func (NoAuthorizer) isAuthorizer()  {}
func (IAMAuthorizer) isAuthorizer() {}

// errNotImplemented is what the builders answer until they move here from
// internal/service/lambda/furl.
var errNotImplemented = errors.New("httpevent: payload format 2.0 is not implemented here yet; internal/service/lambda/furl still builds it")

// EventV2 builds the payload format 2.0 event for r. It is not implemented
// yet.
func EventV2(r Request, c Context) ([]byte, error) {
	_, _ = r, c
	return nil, errNotImplemented
}

// Response is what the HTTP front end answers.
type Response struct {
	Status int
	Header http.Header
	// Cookies are sent as one Set-Cookie header each.
	Cookies []string
	Body    []byte
}

// Write sends r on w, over any headers w already holds. It is not
// implemented yet and answers 501.
func (r Response) Write(w http.ResponseWriter) {
	_ = r
	w.WriteHeader(http.StatusNotImplemented)
}

// MalformedError is a function output the front end cannot turn into an
// HTTP response.
type MalformedError struct {
	// Problem says what is wrong, as a clause.
	Problem string
}

func (e *MalformedError) Error() string {
	return "the function's response cannot be sent as an HTTP response: " + e.Problem
}

// InferV2 turns a function's output into the HTTP response under payload
// format 2.0. It is not implemented yet.
func InferV2(payload []byte) (Response, error) {
	_ = payload
	return Response{}, errNotImplemented
}
