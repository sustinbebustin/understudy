package furl

import (
	"time"

	"github.com/sustinbebustin/understudy/internal/httpevent"
)

// Request is the HTTP request a function URL received, as the request
// event reports it, with httpevent.Request's fields; RawPath is without
// the path fallback's prefix.
type Request httpevent.Request

// Context is what the event's requestContext reports beyond the request.
type Context struct {
	// URLID is the url id, reported as apiId and domainPrefix.
	URLID      string
	DomainName string
	// RequestID is the invocation's request id.
	RequestID string
	// At is the request time on the emulator clock.
	At time.Time
	// TraceID is the request's X-Ray trace: the X-Amzn-Trace-Id it carries
	// or, without one, a trace derived from the request id and the
	// admission time; the function runs under the same trace. The
	// x-amzn-trace-id header reports it when it is not empty.
	TraceID string
	Caller  Caller
}

// Caller is who the request event says made the request.
//
//sumtype:decl
type Caller interface{ isCaller() }

// Public is the caller of a URL with AuthType NONE: accountId is
// "anonymous" and there is no authorizer.
type Public struct{}

// IAMCaller is the SigV4 signer of a request to a URL with AuthType
// AWS_IAM, reported under requestContext.authorizer.iam.
type IAMCaller struct {
	AccessKey string
	AccountID string
	// ID is the principal's unique id, reported as both callerId and
	// userId: a user's AIDA id, a role session's AROA id with its session
	// name, or a root's account.
	ID      string
	UserARN string
}

func (Public) isCaller()    {}
func (IAMCaller) isCaller() {}

// defaultRoute is the placeholder routeKey and stage of a function URL.
const defaultRoute = "$default"

// Event builds the payload format version 2.0 event for r
// (lambda-events.md 7.3) with httpevent.EventV2: routeKey and stage are
// $default, apiId and domainPrefix the url id, and accountId "anonymous"
// for a Public caller or the signer's account for an IAMCaller, which
// authorizer.iam reports. pathParameters, stageVariables and
// requestContext.authentication are left out, as the guide allows and
// LocalStack's recorded echoes show. As AWS recorded (R13
// 01-logged-events), headers keep cookie beside cookies and
// x-amzn-trace-id carries the Root= part alone.
func Event(r Request, c Context) ([]byte, error) {
	hc := httpevent.Context{
		AccountID: "anonymous", APIID: c.URLID, DomainName: c.DomainName, DomainPrefix: c.URLID,
		RouteKey: defaultRoute, Stage: defaultRoute, RequestID: c.RequestID, At: c.At, TraceID: c.TraceID,
		Authorizer: httpevent.NoAuthorizer{},
	}
	switch caller := c.Caller.(type) {
	case IAMCaller:
		hc.AccountID = caller.AccountID
		hc.Authorizer = httpevent.IAMAuthorizer{
			AccessKey: caller.AccessKey, AccountID: caller.AccountID, CallerID: caller.ID,
			UserARN: caller.UserARN, UserID: caller.ID,
		}
	case Public:
	}
	return httpevent.EventV2For(httpevent.Request(r), hc, httpevent.FunctionURL{CookieHeader: true, RootOnlyTrace: true})
}
