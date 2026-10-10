package awsquery

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"

	"github.com/sustinbebustin/understudy/internal/protocol/form"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Claim is the codec's answer to whether a request is an awsQuery request,
// and for which operation.
//
//sumtype:decl
type Claim interface{ isClaim() }

// Claimed names the modeled operation the request calls.
type Claimed struct {
	Op *registry.Operation
}

// UnknownOperation is a request whose Version names a service that speaks
// awsQuery but whose Action names no modeled operation of it, or names one
// the endpoint does not serve (CreateQueue on an SQS queue path). The
// service's unknownOperationError answers it.
type UnknownOperation struct {
	Service *registry.Service
	// Operation is the Action the request carried.
	Operation string
}

// Declined is a request this codec does not serve; Reason says why, for the
// journal's Unclaimed event.
type Declined struct {
	Reason string
}

func (Claimed) isClaim()          {}
func (UnknownOperation) isClaim() {}
func (Declined) isClaim()         {}

// formMediaType is the awsQuery request media type.
const formMediaType = "application/x-www-form-urlencoded"

// Claim decides whether r is an awsQuery request (ARCHITECTURE.md 4.1 rule
// 7): a body of the form media type, parameters such as charset ignored, or
// a GET whose query string carries Action. The service and operation come
// from the Action and Version parameters, read alone so that a bad value
// elsewhere in the form is the service's malformed-request answer from
// Decode; cross-checking the service against the SigV4 signing name is the
// edge's job. A form body is read to find them, gzip-decompressed when its
// Content-Encoding says so, and then put back as sent, so r.Body reads
// from the start again.
func (Codec) Claim(r *http.Request, reg *registry.Registry) Claim {
	text, reason := claimText(r)
	if reason != "" {
		return Declined{Reason: reason}
	}
	action, _, perr := form.Lookup(text, "Action")
	if perr != nil {
		return Declined{Reason: "the Action parameter cannot be decoded: " + perr.Error()}
	}
	if action == "" {
		return Declined{Reason: "the form has no Action parameter"}
	}
	version, _, perr := form.Lookup(text, "Version")
	if perr != nil {
		return Declined{Reason: "the Version parameter cannot be decoded: " + perr.Error()}
	}
	op, ok := reg.ByQueryAction(action, version)
	if !ok {
		svc, ok := serviceAtVersion(reg, version)
		if !ok {
			return Declined{Reason: fmt.Sprintf("Version %q names no service that speaks awsQuery, or more than one", version)}
		}
		return UnknownOperation{Service: svc, Operation: action}
	}
	if _, onQueue := queuePath(r, op.Service); onQueue && !servedOnQueuePath(op) {
		return UnknownOperation{Service: op.Service, Operation: action}
	}
	return Claimed{Op: op}
}

// claimText returns the form text of r, the query string of a GET or the
// body of a form request, or the reason r is not an awsQuery request.
func claimText(r *http.Request) (text, reason string) {
	if r.Method == http.MethodGet {
		return r.URL.RawQuery, ""
	}
	if !isForm(r.Header.Get("Content-Type")) {
		return "", fmt.Sprintf("Content-Type %q is not %s and the method is %s, not GET", r.Header.Get("Content-Type"), formMediaType, r.Method)
	}
	body, err := peekBody(r)
	if err != nil {
		return "", err.Error()
	}
	body, cerr := decodeContent(r.Header, body, DefaultMaxRequestBytes)
	if cerr != nil {
		return "", "the form body cannot be read to find its Action: " + cerr.Error()
	}
	return string(body), ""
}

// isForm reports whether the Content-Type essence is the form media type.
// A malformed parameter does not change the essence.
func isForm(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil && !errors.Is(err, mime.ErrInvalidMediaParameter) {
		return false
	}
	return mediaType == formMediaType
}

// peekBody reads r's body, up to DefaultMaxRequestBytes, and puts it back.
func peekBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	orig := r.Body
	body, err := io.ReadAll(io.LimitReader(orig, DefaultMaxRequestBytes+1))
	r.Body = replayBody{Reader: io.MultiReader(bytes.NewReader(body), orig), Closer: orig}
	if err != nil {
		return nil, fmt.Errorf("the form body could not be read to find its Action: %w", err)
	}
	if int64(len(body)) > DefaultMaxRequestBytes {
		return nil, fmt.Errorf("the form body exceeds %d bytes, so its Action is not read", DefaultMaxRequestBytes)
	}
	return body, nil
}

// replayBody is a request body read once by Claim and readable again.
type replayBody struct {
	io.Reader
	io.Closer
}

// serviceAtVersion finds the one service that speaks awsQuery at version.
func serviceAtVersion(reg *registry.Registry, version string) (*registry.Service, bool) {
	var found *registry.Service
	for _, svc := range reg.Services() {
		if svc.QueryVersion != version || !slices.Contains(svc.Protocols, ref.AWSQuery) {
			continue
		}
		if found != nil {
			return nil, false
		}
		found = svc
	}
	return found, found != nil
}
