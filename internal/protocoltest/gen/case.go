package gen

import (
	"bytes"
	"io"
	"strings"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Suite is one synthetic service and its compliance cases, the ones a server
// runs: appliesTo absent or server (smithy-protocol-tests.md 2, 8).
type Suite struct {
	Service   *registry.Service
	Requests  []RequestCase
	Responses []ResponseCase
	Malformed []MalformedCase
}

// AppliesTo says which side of the wire a case was written for. Client-only
// cases are never generated.
type AppliesTo uint8

// AppliesTo values.
const (
	// Both is a case without appliesTo: clients and servers run it.
	Both AppliesTo = iota + 1
	// ServerOnly is a case with appliesTo: server.
	ServerOnly
)

// RequestCase is one smithy.test#httpRequestTests entry: the request a
// client sends and the input a server must decode from it.
type RequestCase struct {
	ID        string
	Protocol  ref.ProtocolID
	Operation *registry.Operation
	AppliesTo AppliesTo
	Method    string
	// URI is the request path without the query string.
	URI string
	// Host and ResolvedHost are the endpoint the client was given and the
	// host it sends to; empty when the case names none. A case with a host
	// tests the endpoint trait, a client concern.
	Host         string
	ResolvedHost string
	// QueryParams are key=value pieces, percent-encoded as on the wire.
	QueryParams        []string
	ForbidQueryParams  []string
	RequireQueryParams []string
	Headers            map[string]string
	ForbidHeaders      []string
	RequireHeaders     []string
	// Body is the request body; base64 when BodyMediaType is binary
	// (application/cbor). Empty with no BodyMediaType asserts nothing.
	Body          string
	BodyMediaType string
	// Params is the decoded input the server must produce: *<Op>Input.
	Params smithy.Deserializable
	Tags   []string
}

// ResponseCase is one smithy.test#httpResponseTests entry: the value a
// server serializes and the response a client must see.
type ResponseCase struct {
	ID        string
	Protocol  ref.ProtocolID
	Operation *registry.Operation
	AppliesTo AppliesTo
	Code      int
	Headers   map[string]string
	// ForbidHeaders and RequireHeaders name headers that must be absent or
	// present with any value.
	ForbidHeaders  []string
	RequireHeaders []string
	Body           string
	BodyMediaType  string
	Outcome        Outcome
	Tags           []string
}

// Outcome is what a response case's handler answers.
//
//sumtype:decl
type Outcome interface{ isOutcome() }

// Output is a successful response: *<Op>Output.
type Output struct{ Value smithy.Serializable }

// Error is an error response: the error shape's identity and its value,
// whose members the response carries. Code and Type are the case's
// ErrorCodeParams, the code and fault a client must report, empty when the
// case states none.
type Error struct {
	ID    awserr.Identity
	Value smithy.Serializable
	Code  string
	Type  string
}

func (Output) isOutcome() {}
func (Error) isOutcome()  {}

// MalformedCase is one expanded smithy.test#httpMalformedRequestTests entry:
// a request the server must reject before its handler runs, and the
// response it must send. A parameterized entry is one case per parameter
// index, its id suffixed _case<i>.
type MalformedCase struct {
	ID        string
	Protocol  ref.ProtocolID
	Operation *registry.Operation
	Request   MalformedRequest
	Response  MalformedResponse
	Tags      []string
}

// MalformedRequest is the request a malformed case sends.
type MalformedRequest struct {
	Method        string
	URI           string
	Host          string
	QueryParams   []string
	Headers       map[string]string
	Body          string
	BodyMediaType string
}

// MalformedResponse is what a malformed case expects: headers are
// subset-matched.
type MalformedResponse struct {
	Code    int
	Headers map[string]string
	Body    BodyAssertion
}

// BodyAssertion is what a malformed case asserts about the response body.
//
//sumtype:decl
type BodyAssertion interface{ isBodyAssertion() }

// NoBodyAssertion asserts nothing about the body.
type NoBodyAssertion struct{}

// BodyContents requires the body to equal Contents as MediaType compares.
type BodyContents struct{ MediaType, Contents string }

// BodyMessageRegex requires the body's message to match Pattern.
type BodyMessageRegex struct{ MediaType, Pattern string }

func (NoBodyAssertion) isBodyAssertion()  {}
func (BodyContents) isBodyAssertion()     {}
func (BodyMessageRegex) isBodyAssertion() {}

// TextBody is a case's input stream: the case's text is the body.
type TextBody string

// Open returns the body.
func (b TextBody) Open() (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(b))), nil
}

// Len returns the body's length.
func (b TextBody) Len() (int64, bool) { return int64(len(b)), true }

// Trailer reports no trailer: a case body is never aws-chunked.
func (TextBody) Trailer(string) (string, bool) { return "", false }

// TextSource is a case's output stream: the case's text is the body.
type TextSource string

// Size returns the body's length.
func (s TextSource) Size() int64 { return int64(len(s)) }

// Range returns n bytes of the body starting at off, cut to its end.
func (s TextSource) Range(off, n int64) (io.ReadCloser, error) {
	b := []byte(s)
	off = min(max(off, 0), int64(len(b)))
	end := min(off+max(n, 0), int64(len(b)))
	return io.NopCloser(bytes.NewReader(b[off:end])), nil
}
