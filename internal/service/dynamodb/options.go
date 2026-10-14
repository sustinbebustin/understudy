package dynamodb

import (
	"context"
	"net/http"
	"net/url"

	"github.com/sustinbebustin/understudy/internal/service/dynamodb/ddblocal"
)

// Options are the DynamoDB settings wire.go fills from the configuration.
// The zero value has no DynamoDB Local: every forwarded operation answers
// 501, as a server with DynamoDB turned off does.
type Options struct {
	// Local is the DynamoDB Local container the service forwards to; a
	// *ddblocal.Manager in production, a fake in tests.
	Local Local
}

// Local is a DynamoDB Local the proxy reaches over HTTP.
type Local interface {
	// Start makes Local ready, starting it when it is not running. A
	// *DisabledError means Local cannot run here and nothing was pulled.
	Start(ctx context.Context) error
	// Endpoint is the base URL of the running Local; false when it stopped.
	Endpoint() (*url.URL, bool)
	// Transport reaches Endpoint.
	Transport() http.RoundTripper
	// Lost reports that a request to endpoint did not reach Local; when
	// its container is gone, the next Start launches a new one. An error
	// means whether it is gone could not be told.
	Lost(ctx context.Context, endpoint *url.URL) error
}

// The manager is the production Local.
var _ Local = (*ddblocal.Manager)(nil)

// DisabledError is a Start refused because DynamoDB Local cannot run here:
// the configuration turns DynamoDB off, or Docker is unavailable. Reason
// says which and how to change it; a DynamoDB call answers 501 with it
// while every other service keeps working.
type DisabledError struct{ Reason string }

func (e *DisabledError) Error() string { return e.Reason }

// noLocal is the Local of a service built without one.
type noLocal struct{}

func (noLocal) Start(context.Context) error {
	return &DisabledError{Reason: "The DynamoDB service has no DynamoDB Local to forward to in this understudy, so it serves no DynamoDB operation."}
}

func (noLocal) Endpoint() (*url.URL, bool) { return nil, false }

func (noLocal) Transport() http.RoundTripper { return http.DefaultTransport }

func (noLocal) Lost(context.Context, *url.URL) error { return nil }
