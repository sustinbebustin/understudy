package dynamodb

import (
	"context"
	"net/http"
	"net/url"

	"github.com/sustinbebustin/understudy/internal/service/dynamodb/ddblocal"
)

// Options are the DynamoDB settings wire.go fills from the configuration.
// The zero value has no DynamoDB Local: every forwarded operation answers
// 501 with the licence notice, as an unaccepted licence does.
type Options struct {
	// Local is the DynamoDB Local container the service forwards to; a
	// *ddblocal.Manager in production, a fake in tests.
	Local Local
}

// Local is a DynamoDB Local the proxy reaches over HTTP.
type Local interface {
	// Start makes Local ready, starting it when it is not running. A
	// *ddblocal.LicenceError means the licence does not let understudy run
	// it.
	Start(ctx context.Context) error
	// Endpoint is the base URL of the running Local; false when it stopped.
	Endpoint() (*url.URL, bool)
	// Transport reaches Endpoint.
	Transport() http.RoundTripper
}

// The manager is the production Local.
var _ Local = (*ddblocal.Manager)(nil)

// unlicensed is the Local of a service built without one: nobody accepted
// the licence, so nothing may be pulled.
type unlicensed struct{}

func (unlicensed) Start(context.Context) error {
	return &ddblocal.LicenceError{Licence: ddblocal.AwaitingLicence{}}
}

func (unlicensed) Endpoint() (*url.URL, bool) { return nil, false }

func (unlicensed) Transport() http.RoundTripper { return http.DefaultTransport }
