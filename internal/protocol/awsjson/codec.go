package awsjson

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
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Version is the awsJson protocol version a Codec speaks.
type Version uint8

// Version values.
const (
	V10 Version = iota + 1 // awsJson1_0: SQS, DynamoDB, CloudWatch
	V11                    // awsJson1_1: Logs, Secrets Manager, KMS, EventBridge
)

// String returns "awsJson1_0" or "awsJson1_1".
func (v Version) String() string {
	switch v {
	case V10:
		return "awsJson1_0"
	case V11:
		return "awsJson1_1"
	default:
		return fmt.Sprintf("Version(%d)", uint8(v))
	}
}

func (v Version) mediaType() string {
	switch v {
	case V10:
		return "application/x-amz-json-1.0"
	case V11:
		return "application/x-amz-json-1.1"
	default:
		return ""
	}
}

// Codec is the awsJson codec of one version. The zero Codec has no version
// and claims nothing; use Codec{Version: V10} or Codec{Version: V11}.
type Codec struct {
	Version Version
}

// Protocol returns the protocol id the codec registers under.
func (c Codec) Protocol() ref.ProtocolID {
	switch c.Version {
	case V10:
		return ref.AWSJSON10
	case V11:
		return ref.AWSJSON11
	default:
		return ""
	}
}

// Decode reads r's body into in through the generated Deserialize method.
// An empty body is {}; unknown and null members are skipped; a body with
// Content-Encoding: gzip is decompressed, up to MaxRequestBytes(op). A body
// that is not JSON, or does not fit the input shape, is the service's
// malformedRequest answer; a body that cannot be read is
// RequestAborted, or the edge's own error when its body cap stopped the
// read.
func (c Codec) Decode(r *http.Request, op *registry.Operation, in smithy.Deserializable) *awserr.Error {
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
	var derr *decodeError
	var serr *jsonx.SyntaxError
	switch {
	case errors.As(err, &derr):
		return malformedRequest(op.Service, derr.Message)
	case errors.As(err, &serr):
		// Coral sends SerializationException with no message for a body
		// that is not JSON (error-matrix.md 5.5 T6).
		return malformedRequest(op.Service, "")
	default:
		// The deserializer checks kinds before jsonx does, so a
		// jsonx.KindError lands here as the codec bug it is.
		return awserr.Internalf(fmt.Errorf("decoding the %s input: %w", op.ID, err))
	}
}

// readBody returns the request body, decompressed when the client gzipped
// it (the requestCompression trait, which wire-json-query.md 3.2 finds
// only on CloudWatch PutMetricData).
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
		return nil, malformedRequest(op.Service, fmt.Sprintf("Content-Encoding %s is not supported; send the body uncompressed or gzip-compressed", encoding))
	}
}

// DefaultMaxRequestBytes is the awsJson body cap for an operation whose
// MaxRequestBytes is unset: DynamoDB's 16 MiB, past which it answers 413
// (error-matrix.md 5.5 T9).
const DefaultMaxRequestBytes int64 = 16 << 20

// MaxRequestBytes is op's body cap: its own MaxRequestBytes, else
// DefaultMaxRequestBytes.
func MaxRequestBytes(op *registry.Operation) int64 {
	if op.MaxRequestBytes > 0 {
		return op.MaxRequestBytes
	}
	return DefaultMaxRequestBytes
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
// error through awserr.BoundaryError; anything else, a BoundaryError built
// without its Err included, means the client went away mid-body.
func readFailure(err error) *awserr.Error {
	if boundary, ok := errors.AsType[*awserr.BoundaryError](err); ok && boundary.Err != nil {
		return boundary.Err
	}
	return awserr.RequestAborted()
}

// malformedRequest is svc's answer to a body that does not decode: its
// malformedRequestError, else SerializationException, the awsJson default
// Coral sends. message is Coral's text for the condition, empty where Coral
// sends none.
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
// response: status, which the pipeline chose (the codec does not derive
// it), Content-Type of the version, x-amzn-RequestId, and a JSON object of
// the set members, {} when none is set. An operation without modeled
// output (smithy.api#Unit) gets an empty body, as SQS answers
// DeleteMessage (sqs.md 2.1). A serializer failure is an Internal error,
// returned before anything is written.
func (c Codec) Encode(op *registry.Operation, requestID string, out smithy.Serializable, status int) (Response, *awserr.Error) {
	var body []byte
	if op.Output != nil && op.Output.Type != smithy.TypeUnit {
		s := newSerializer()
		out.Serialize(s)
		b, err := s.bytes()
		if err != nil {
			return Response{}, awserr.Internalf(fmt.Errorf("encoding the %s output: %w", op.ID, err))
		}
		body = b
	}
	return Response{Status: status, Header: c.header(requestID, len(body)), Body: body}, nil
}

// EncodeError renders e for svc as AWS's JSON front end does
// (wire-json-query.md 5 and 6, error-matrix.md 7): status from the
// identity; a body with __type (the identity's namespaced JSONType for
// awsJson1_0, the bare shape name for awsJson1_1), the message under the
// shape's own member spelling, and the error's extra fields; and for a
// query-compatible service, x-amzn-query-error: <QueryCode>;<Sender or
// Receiver> on every error, whether or not the request sent
// x-amzn-query-mode. When it did send x-amzn-query-mode: true, the
// identity's QueryModeStatus, if set, replaces the status (CloudWatch's
// 404 for ResourceNotFound). svc may be nil for a request no service
// claimed.
func (c Codec) EncodeError(r *http.Request, svc *registry.Service, requestID string, e *awserr.Error) Response {
	body := []byte(`{"__type":`)
	body = jsonx.AppendString(body, c.errorType(e.ID))
	member := func(name, value string) {
		body = append(body, ',')
		body = jsonx.AppendString(body, name)
		body = append(body, ':')
		body = jsonx.AppendString(body, value)
	}
	if msg := e.MessageFor(c.Protocol()); msg != "" {
		switch e.ID.MessageKey {
		case awserr.MessageLower:
			member("message", msg)
		case awserr.MessageUpper:
			member("Message", msg)
		case awserr.NoMessage:
		}
	}
	for _, f := range e.Fields {
		member(f.Name, f.Value)
	}
	body = append(body, '}')
	h := c.header(requestID, len(body))
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

// errorType is __type for the identity: verbatim for awsJson1_0, whose
// JSONType carries the overlay's namespace, and the bare shape name for
// awsJson1_1.
func (c Codec) errorType(id awserr.Identity) string {
	if c.Version == V11 {
		if _, name, ok := strings.Cut(id.JSONType, "#"); ok {
			return name
		}
	}
	return id.JSONType
}

func (c Codec) header(requestID string, n int) http.Header {
	return http.Header{
		"Content-Type":     {c.Version.mediaType()},
		"X-Amzn-Requestid": {requestID},
		"Content-Length":   {strconv.Itoa(n)},
	}
}
