package rpcv2cbor

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/cbor"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Codec is the Smithy RPC v2 CBOR codec. It has no settings; the zero
// value is ready to use.
type Codec struct{}

// Protocol returns the protocol id the codec registers under.
func (Codec) Protocol() ref.ProtocolID { return ref.RPCv2CBOR }

// DefaultMaxRequestBytes is the rpcv2Cbor body cap for an operation whose
// MaxRequestBytes is unset: the awsJson codec's 16 MiB, so CloudWatch
// takes the same bodies over either protocol.
const DefaultMaxRequestBytes int64 = 16 << 20

// MaxRequestBytes is op's body cap: its own MaxRequestBytes, else
// DefaultMaxRequestBytes.
func MaxRequestBytes(op *registry.Operation) int64 {
	if op.MaxRequestBytes > 0 {
		return op.MaxRequestBytes
	}
	return DefaultMaxRequestBytes
}

// Decode reads r's body into in through the generated Deserialize method.
// An empty body, bfff and a0 are all an input with no member set; unknown
// members and null members are skipped; a body with Content-Encoding:
// gzip is decompressed, up to MaxRequestBytes(op). A body that is not
// CBOR, or does not fit the input shape, is the service's malformedRequest
// answer; a body that cannot be read is RequestAborted, or the edge's own
// error when its body cap stopped the read.
func (Codec) Decode(r *http.Request, op *registry.Operation, in smithy.Deserializable) *awserr.Error {
	body, aerr := readBody(r, op)
	if aerr != nil {
		return aerr
	}
	d := newDeserializer(body)
	err := in.Deserialize(d)
	if err == nil {
		err = d.end()
	}
	if err == nil {
		return nil
	}
	var misuse *misuseError
	var usage *cbor.UsageError
	if errors.As(err, &misuse) || errors.As(err, &usage) {
		return awserr.Internalf(fmt.Errorf("decoding the %s input: %w", op.ID, err))
	}
	return malformedRequest(op.Service, fmt.Sprintf("The %s request body is not a valid rpcv2Cbor body for its input: %s.", op.ID.Name, describe(err)))
}

// describe is the client-facing text of a decoding failure: the cbor and
// codec errors without their package prefix.
func describe(err error) string {
	return strings.TrimPrefix(err.Error(), "cbor: ")
}

// readBody returns the request body, decompressed when the client gzipped
// it (the requestCompression trait: CloudWatch PutMetricData, wire-cbor.md
// 3.2).
func readBody(r *http.Request, op *registry.Operation) ([]byte, *awserr.Error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, readFailure(err)
	}
	switch encoding := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); encoding {
	case "", "identity":
		return body, nil
	case "gzip":
		return gunzip(body, op)
	default:
		return nil, malformedRequest(op.Service, fmt.Sprintf("Content-Encoding %s is not supported; send the body uncompressed or gzip-compressed.", encoding))
	}
}

// gunzip decompresses body, refusing output past op's cap so that a small
// compressed body cannot expand without bound.
func gunzip(body []byte, op *registry.Operation) ([]byte, *awserr.Error) {
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, malformedRequest(op.Service, "The request body is not valid gzip data: "+err.Error())
	}
	limit := MaxRequestBytes(op)
	out, err := io.ReadAll(io.LimitReader(zr, limit+1))
	if err != nil {
		return nil, malformedRequest(op.Service, "The request body is not valid gzip data: "+err.Error())
	}
	if int64(len(out)) > limit {
		return nil, &awserr.Error{
			ID:      awserr.RequestEntityTooLarge(),
			Message: fmt.Sprintf("The decompressed request body exceeds the %d-byte limit of %s; send a smaller body.", limit, op.ID),
			Origin:  awserr.Generic{},
		}
	}
	return out, nil
}

// readFailure maps a failed body read: the edge's body cap reports its
// error through awserr.BoundaryError; anything else means the client went
// away mid-body.
func readFailure(err error) *awserr.Error {
	if boundary, ok := errors.AsType[*awserr.BoundaryError](err); ok && boundary.Err != nil {
		return boundary.Err
	}
	return awserr.RequestAborted()
}

// malformedRequest is svc's answer to a body that does not decode: its
// malformedRequestError, else SerializationException.
func malformedRequest(svc *registry.Service, message string) *awserr.Error {
	id := svc.MalformedRequestError
	if id.Shape == "" {
		id = awserr.SerializationException()
	}
	return &awserr.Error{ID: id, Message: message, Origin: awserr.Generic{}}
}

// Response is an encoded response: the status, the complete header set and
// the body, for the edge to write.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Encode serializes out, the handler's output for op, into a success
// response with status, which the pipeline chose. An operation whose
// output is smithy.api#Unit gets no body and no Content-Type, whatever
// out holds: a Unit response with any member panics the Go client
// (wire-cbor.md 3.3 and 5.1, vectors G11 and G22). Any other output is a
// definite-length CBOR map of the set members, a0 when none is set. A
// serializer failure is an Internal error, returned before anything is
// written.
func (Codec) Encode(op *registry.Operation, requestID string, out smithy.Serializable, status int) (Response, *awserr.Error) {
	if op.Output == nil || op.Output.Type == smithy.TypeUnit {
		return Response{Status: status, Header: header(requestID, 0)}, nil
	}
	s := newSerializer()
	out.Serialize(s)
	body, err := s.bytes()
	if err != nil {
		return Response{}, awserr.Internalf(fmt.Errorf("encoding the %s output: %w", op.ID, err))
	}
	h := header(requestID, len(body))
	h.Set("Content-Type", mediaType)
	return Response{Status: status, Header: h, Body: body}, nil
}

// EncodeError renders e for svc (wire-cbor.md 3.4): the identity's status;
// a definite-length CBOR map that is never empty, holding __type, the
// error's absolute shape id, then the message and the error's extra
// fields; and for a query-compatible service, x-amzn-query-error:
// <QueryCode>;<Sender or Receiver>. The message goes under the shape's own
// spelling when the shape is in svc's namespace, which the client's error
// registry knows; any other shape (the generic catalogue, understudy's
// NotImplemented) reaches the client as an unknown type, whose message
// smithy-go reads from message, so it goes there. When r sent
// x-amzn-query-mode: true to a query-compatible service, the identity's
// QueryModeStatus, if set, replaces the status (CloudWatch's 404 for
// ResourceNotFound). svc is nil for a request no service claimed.
func (Codec) EncodeError(r *http.Request, svc *registry.Service, requestID string, e *awserr.Error) Response {
	members := []awserr.Field{{Name: "__type", Value: string(e.ID.Shape)}}
	if msg := e.MessageFor(ref.RPCv2CBOR); msg != "" {
		if key, ok := messageKey(svc, e.ID); ok {
			members = append(members, awserr.Field{Name: key, Value: msg})
		}
	}
	members = append(members, e.Fields...)
	body := cbor.AppendMapHead(nil, len(members))
	for _, m := range members {
		body = cbor.AppendText(cbor.AppendText(body, m.Name), m.Value)
	}
	h := header(requestID, len(body))
	h.Set("Content-Type", mediaType)
	queryCompatible := svc != nil && svc.QueryCompatible
	if queryCompatible {
		h.Set("X-Amzn-Query-Error", e.ID.QueryCode+";"+e.ID.Fault.String())
	}
	for _, x := range e.Headers {
		h.Add(x.Name, x.Value)
	}
	status := e.ID.Status
	if queryCompatible && e.ID.QueryModeStatus != 0 && strings.EqualFold(r.Header.Get("X-Amzn-Query-Mode"), "true") {
		status = e.ID.QueryModeStatus
	}
	return Response{Status: status, Header: h, Body: body}
}

// messageKey is the member that carries the message of an error with
// identity id, or false when the shape has none.
func messageKey(svc *registry.Service, id awserr.Identity) (string, bool) {
	if svc == nil || id.Shape.Namespace() != svc.Shape.Namespace() {
		return "message", true
	}
	switch id.MessageKey {
	case awserr.MessageLower:
		return "message", true
	case awserr.MessageUpper:
		return "Message", true
	case awserr.NoMessage:
		return "", false
	default:
		return "message", true
	}
}

// header is the header set of every response: Smithy-Protocol, which
// clients may check (wire-cbor.md 3.3), the request id and the length.
func header(requestID string, n int) http.Header {
	return http.Header{
		"Smithy-Protocol":  {protocolName},
		"X-Amzn-Requestid": {requestID},
		"Content-Length":   {strconv.Itoa(n)},
	}
}
