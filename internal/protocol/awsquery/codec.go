package awsquery

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/form"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/xmlw"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Codec is the awsQuery codec. The zero Codec is ready to use.
type Codec struct{}

// Protocol returns the protocol id the codec registers under.
func (Codec) Protocol() ref.ProtocolID { return ref.AWSQuery }

// DefaultMaxRequestBytes is the awsQuery body cap for an operation whose
// MaxRequestBytes is unset, the same 16 MiB as awsJson's.
const DefaultMaxRequestBytes int64 = 16 << 20

// MaxRequestBytes is op's body cap: its own MaxRequestBytes, else
// DefaultMaxRequestBytes.
func MaxRequestBytes(op *registry.Operation) int64 {
	if op.MaxRequestBytes > 0 {
		return op.MaxRequestBytes
	}
	return DefaultMaxRequestBytes
}

// Decode reads r's form, the query string of a GET or else the body, into
// in through the generated Deserialize method. Unknown parameters, Action
// and Version among them, are skipped. A legacy SQS request on a queue
// path gets that queue's URL as its QueueUrl unless the form names one. A
// body whose Content-Encoding cannot be undone, a form Parse refuses and a
// value that does not fit its member are the service's malformed-request
// answer; a body that cannot be read is RequestAborted, or the edge's own
// error when its body cap stopped the read.
func (Codec) Decode(r *http.Request, op *registry.Operation, in smithy.Deserializable) *awserr.Error {
	text, aerr := formText(r, op)
	if aerr != nil {
		return aerr
	}
	v, perr := form.Parse(text)
	if perr != nil {
		return malformedRequest(op.Service, perr.Message())
	}
	if path, ok := queuePath(r, op.Service); ok {
		v.Add("QueueUrl", queueURL(r, path))
	}
	err := in.Deserialize(newDeserializer(v))
	if err == nil {
		return nil
	}
	if derr, ok := errors.AsType[*decodeError](err); ok {
		return malformedRequest(op.Service, derr.Message)
	}
	return awserr.Internalf(fmt.Errorf("decoding the %s input: %w", op.ID, err))
}

// formText returns the query string of a GET, else the whole body as its
// Content-Encoding says to read it, inflated to at most op's body cap.
func formText(r *http.Request, op *registry.Operation) (string, *awserr.Error) {
	if r.Method == http.MethodGet {
		return r.URL.RawQuery, nil
	}
	if r.Body == nil {
		return "", nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", readFailure(err)
	}
	body, cerr := decodeContent(r.Header, body, MaxRequestBytes(op))
	if cerr != nil {
		return "", malformedRequest(op.Service, cerr.Error())
	}
	return string(body), nil
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

// malformedRequest is svc's answer to a body it cannot decode, a form Parse
// refuses or a value that does not fit its member: its
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
// response with the status the pipeline chose: the envelope
// <OpResponse xmlns><OpResult/><ResponseMetadata><RequestId/></ResponseMetadata></OpResponse>
// (wire-json-query.md 8.1). The Result element is sent whenever the
// operation has an output shape, as <OpResult/> when no member is set, and
// the envelope even for a smithy.api#Unit output, since boto3 fails on a
// missing Result and on an empty body (8.2). The namespace is the
// service's xmlNamespace, absent when it has none. A serializer failure is
// an Internal error, returned before anything is written.
func (Codec) Encode(op *registry.Operation, requestID string, out smithy.Serializable, status int) (Response, *awserr.Error) {
	var buf bytes.Buffer
	s := newSerializer(xmlw.New(&buf))
	s.start(op.ID.Name+"Response", op.Service.XMLNamespace)
	if op.Output != nil && op.Output.Type != smithy.TypeUnit {
		s.start(op.ID.Name+"Result", "")
		out.Serialize(s)
		s.end()
	}
	err := s.done()
	if err == nil {
		s.start("ResponseMetadata", "")
		s.leaf("RequestId", requestID)
		s.end()
		s.end()
		err = s.w.Close()
	}
	if err != nil {
		return Response{}, awserr.Internalf(fmt.Errorf("encoding the %s output: %w", op.ID, err))
	}
	return Response{Status: status, Header: header(requestID, buf.Len()), Body: buf.Bytes()}, nil
}

// EncodeError renders e for svc as AWS's Query front end does
// (wire-json-query.md 9.1):
// <ErrorResponse><Error><Type/><Code/><Message/></Error><RequestId/></ErrorResponse>,
// Type Sender or Receiver, Code the identity's QueryCode (the awsQueryError
// code, else the shape name), the message for awsQuery (messageByProtocol
// over the default text) and omitted when empty, then the error's extra
// fields. The status is awsQueryError's httpResponseCode, which the
// identity carries as QueryModeStatus when it differs from httpError, else
// Status. Where a service's envelope differs from Smithy's, errorEnvelopeOf
// says how. svc may be nil for a request no service claimed.
func (Codec) EncodeError(svc *registry.Service, requestID string, e *awserr.Error) Response {
	body, err := errorBody(svc, requestID, e, e.Fields)
	if err != nil {
		// A field whose name is not an XML name: the error is still sent,
		// without its fields, rather than replaced.
		body, _ = errorBody(svc, requestID, e, nil) //nolint:errcheck // the fixed elements and escaped text always form a document
	}
	h := header(requestID, len(body))
	for _, x := range e.Headers {
		h.Add(x.Name, x.Value)
	}
	return Response{Status: errorStatus(e.ID), Header: h, Body: body}
}

func errorBody(svc *registry.Service, requestID string, e *awserr.Error, fields []awserr.Field) ([]byte, error) {
	var buf bytes.Buffer
	s := newSerializer(xmlw.New(&buf))
	env := errorEnvelopeOf(svc)
	s.start("ErrorResponse", env.namespace)
	s.start("Error", "")
	s.leaf("Type", e.ID.Fault.String())
	s.leaf("Code", cmp.Or(e.ID.QueryCode, e.ID.Shape.Name()))
	if msg := e.MessageFor(ref.AWSQuery); msg != "" {
		s.leaf("Message", msg)
	}
	if env.detail {
		s.leaf("Detail", "")
	}
	for _, f := range fields {
		s.leaf(f.Name, f.Value)
	}
	s.end()
	s.leaf("RequestId", requestID)
	s.end()
	if err := s.w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// errorStatus is the awsQuery status of an identity, falling back on the
// fault's default for an identity that carries none.
func errorStatus(id awserr.Identity) int {
	fallback := http.StatusBadRequest
	if id.Fault == awserr.Receiver {
		fallback = http.StatusInternalServerError
	}
	return cmp.Or(id.QueryModeStatus, id.Status, fallback)
}

// errorEnvelope is how a service's ErrorResponse differs from Smithy's.
type errorEnvelope struct {
	// namespace is the xmlns ErrorResponse declares, empty for none.
	namespace string
	// detail adds an empty <Detail/> after the message.
	detail bool
}

// errorEnvelopeOf returns svc's error envelope. CloudWatch declares its
// namespace on ErrorResponse and SNS does not (error-matrix.md 3, both
// recorded); IAM's recorded fixture declares it too (iam-sts-kms.md 1.2).
// SQS sends an empty Detail (error-matrix.md 3, recorded through boto3).
// Every other service follows Smithy's example: no namespace, no Detail.
func errorEnvelopeOf(svc *registry.Service) errorEnvelope {
	if svc == nil {
		return errorEnvelope{}
	}
	switch svc.ID {
	case "cloudwatch", "iam":
		return errorEnvelope{namespace: svc.XMLNamespace}
	case sqsService:
		return errorEnvelope{detail: true}
	default:
		return errorEnvelope{}
	}
}

func header(requestID string, n int) http.Header {
	return http.Header{
		"Content-Type":     {"text/xml"},
		"X-Amzn-Requestid": {requestID},
		"Content-Length":   {strconv.Itoa(n)},
	}
}
