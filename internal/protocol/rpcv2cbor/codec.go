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
// error's absolute shape id, then the message, the members of the value e
// carries as the output serializer writes them (smithy-protocol-tests.md
// 11.2) and the error's extra fields; and for a query-compatible service,
// x-amzn-query-error: <QueryCode>;<Sender or Receiver>, unless the
// identity sends it only in query mode (awserr.Identity.QueryErrorHeader)
// and r did not send x-amzn-query-mode: true. The message goes
// under the shape's own spelling when the shape is in svc's namespace,
// which the client's error registry knows; any other shape (the generic
// catalogue, understudy's NotImplemented) reaches the client as an unknown
// type, whose message smithy-go reads from message, so it goes there. When
// r sent x-amzn-query-mode: true to a query-compatible service, the
// identity's QueryModeStatus, if set, replaces the status (CloudWatch's
// 404 for ResourceNotFound). A value that does not serialize is answered
// as InternalFailure. svc is nil for a request no service claimed.
func (c Codec) EncodeError(r *http.Request, svc *registry.Service, requestID string, e *awserr.Error) Response {
	body, err := errorBody(svc, e)
	if err != nil {
		return c.EncodeError(r, svc, requestID, awserr.Internalf(fmt.Errorf("encoding the %s error: %w", e.ID.Shape, err)))
	}
	h := header(requestID, len(body))
	h.Set("Content-Type", mediaType)
	queryCompatible := svc != nil && svc.QueryCompatible
	queryMode := strings.EqualFold(r.Header.Get("X-Amzn-Query-Mode"), "true")
	if v, ok := e.ID.QueryErrorHeader(queryMode); queryCompatible && ok {
		h.Set("X-Amzn-Query-Error", v)
	}
	for _, x := range e.Headers {
		h.Add(x.Name, x.Value)
	}
	status := e.ID.Status
	if queryCompatible && e.ID.QueryModeStatus != 0 && queryMode {
		status = e.ID.QueryModeStatus
	}
	return Response{Status: status, Header: h, Body: body}
}

// errorBody is e's map: __type, the message, the members of the value e
// carries, then e's fields. Every key and text goes through the writer as
// cbor.AppendText writes it, and the map's head is definite.
func errorBody(svc *registry.Service, e *awserr.Error) ([]byte, error) {
	s := newSerializer()
	s.w.BeginMap()
	text := func(key, value string) {
		s.w.Key(key)
		s.w.Text(value)
	}
	text("__type", string(e.ID.Shape))
	if msg := e.MessageFor(ref.RPCv2CBOR); msg != "" {
		if key, ok := messageKey(svc, e.ID); ok {
			text(key, msg)
		}
	}
	if v, ok := e.WireMembers(); ok {
		s.open = append(s.open, writeFrame{kind: structFrame})
		v.Serialize(&inlined{serializer: s})
		s.open = s.open[:len(s.open)-1]
	}
	for _, f := range e.Fields {
		text(f.Name, f.Value)
	}
	s.w.EndMap()
	return s.bytes()
}

// inlined writes a structure's members into the map s already has open:
// it drops the structure's own WriteStruct and CloseStruct, so an error
// value's members join the error map.
type inlined struct {
	*serializer
	// structs counts the structures open in the value; 1 is the value.
	structs int
}

func (i *inlined) WriteStruct(m *smithy.Member, sch *smithy.Schema) {
	i.structs++
	if i.structs > 1 {
		i.serializer.WriteStruct(m, sch)
	}
}

func (i *inlined) CloseStruct() {
	if i.structs > 1 {
		i.serializer.CloseStruct()
	}
	i.structs--
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
