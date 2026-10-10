package edge

import (
	"io"
	"net/http"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Codec is one wire protocol as the edge drives it. The edge never imports
// a protocol package: server/wire.go adapts each codec to this interface
// and registers it under its ref.ProtocolID.
type Codec interface {
	// Protocol is the id the codec registers under.
	Protocol() ref.ProtocolID
	// Claim decides whether r is a request of this protocol and which
	// operation it names.
	Claim(r *http.Request, reg *registry.Registry) Claim
	// BodyLimit is op's request body cap: the operation's own
	// MaxRequestBytes, or the protocol default.
	BodyLimit(op *registry.Operation) int64
	// DecodeHead binds the input members the request line and headers carry
	// (REST labels, query and header bindings). RPC protocols bind nothing
	// there.
	DecodeHead(r *http.Request, op *registry.Operation, in smithy.Deserializable) *awserr.Error
	// DecodeBody binds the input members the body carries. body is the
	// edge's view of the request body, capped at BodyLimit by a counting
	// reader: a codec reads a whole body through body.Open, and binds a
	// @streaming payload member to body itself, unread, so the handler pulls
	// it after its own checks.
	DecodeBody(r *http.Request, op *registry.Operation, body smithy.Body, in smithy.Deserializable) *awserr.Error
	// Encode renders a handler's output with the HTTP status the pipeline
	// chose. An error means nothing can be sent but that error.
	Encode(r *http.Request, op *registry.Operation, requestID string, out smithy.Serializable, status int) (Response, *awserr.Error)
	// EncodeError renders e in the protocol's error envelope for svc; svc
	// is nil for a request no service claimed.
	EncodeError(r *http.Request, svc *registry.Service, requestID string, e *awserr.Error) Response
}

// Response is an encoded answer the edge writes: status, headers and a
// body it copies to the client, so a streaming output never sits in
// memory whole.
type Response struct {
	Status int
	Header http.Header
	Body   io.Reader
}

// Claim is a codec's answer to whether a request is its protocol's.
//
//sumtype:decl
type Claim interface{ isClaim() }

// Claimed names the modeled operation the request calls.
type Claimed struct{ Op *registry.Operation }

// UnknownOperation is a request of the protocol that names a service but no
// modeled operation of it; the service's unknownOperationError answers it.
type UnknownOperation struct {
	Service *registry.Service
	// Name is the operation name the request carried, possibly empty.
	Name string
}

// Declined is a request the codec does not serve; Reason says why, for the
// Unclaimed journal event.
type Declined struct{ Reason string }

func (Claimed) isClaim()          {}
func (UnknownOperation) isClaim() {}
func (Declined) isClaim()         {}
