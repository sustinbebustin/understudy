package restjson

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/restbind"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// MediaTypeErrors are the identities a service answers a request with
// when its Content-Type is not what the operation reads (415) or its
// Accept excludes what the operation writes (406).
type MediaTypeErrors struct {
	UnsupportedMediaType awserr.Identity
	NotAcceptable        awserr.Identity
}

// Codec is the restJson1 codec (Lambda). Its zero value is usable,
// checks no media types and adds no fields to the errors it writes.
type Codec struct {
	// MediaTypeErrors returns a service's 415 and 406 identities, which
	// neither the descriptors nor the generic catalogue carry. nil, or
	// false for a service, skips the Content-Type and Accept checks for it.
	MediaTypeErrors func(svc ref.ServiceID) (MediaTypeErrors, bool)
	// ClientFaultFields returns the body fields a service puts on every
	// client error, written ahead of the message on the errors the codec
	// raises itself: the malformed-request, 415 and 406 answers, which no
	// handler sees. Lambda's are Type User (error-matrix.md 5.6 T6). nil
	// adds none.
	ClientFaultFields func(svc ref.ServiceID) []awserr.Field
}

// Response is an encoded answer: status, the complete header set and a
// body the edge copies to the client.
type Response struct {
	Status int
	Header http.Header
	Body   io.Reader
}

// mediaJSON is restJson1's document media type.
const mediaJSON = "application/json"

// Protocol returns ref.RESTJSON1.
func (Codec) Protocol() ref.ProtocolID { return ref.RESTJSON1 }

// DefaultMaxRequestBytes caps a request body when the operation sets no
// MaxRequestBytes: Lambda's frontend request cap, 70167211 bytes
// (lambda-api.md 2.1), which admits UpdateFunctionCode with a 50 MB zip
// as base64. Invoke's 6 MB payload limit is its handler's
// RequestTooLargeException (lambda-api.md 8.2).
const DefaultMaxRequestBytes int64 = 70167211

// BodyLimit is op's body cap: its MaxRequestBytes, else
// DefaultMaxRequestBytes.
func (Codec) BodyLimit(op *registry.Operation) int64 {
	if op.MaxRequestBytes > 0 {
		return op.MaxRequestBytes
	}
	return DefaultMaxRequestBytes
}

// Decode binds r onto in: the head through restbind, the unbound members
// from a JSON object body (empty means none), a blob payload as raw bytes.
// When the service has MediaTypeErrors, a non-empty body whose
// Content-Type is not what the operation reads is 415 and an Accept that
// excludes what it writes is 406 (the corpus's malformed-content-type and
// malformed-accept cases). A blob payload without a mediaType trait is
// never checked: Lambda's Invoke payload is one, and SDKs send it with
// application/octet-stream or no Content-Type at all (wire-rest.md 4.4).
// A body or header that does not decode is the service's
// malformedRequestError (Lambda's InvalidRequestContentException), else
// SerializationException.
func (c Codec) Decode(r *http.Request, op *registry.Operation, body smithy.Body, in smithy.Deserializable) *awserr.Error {
	if err := c.checkMediaTypes(r, op); err != nil {
		return err
	}
	err := restbind.Decode(r, op, body, in, newDocument)
	if err == nil {
		return nil
	}
	var read *restbind.ReadError
	var bind *restbind.BindError
	switch {
	case errors.As(err, &read):
		return read.AWSError()
	case errors.As(err, &bind):
		return c.malformedRequest(op.Service, "The request's "+bind.Location.String()+" is invalid: "+bind.Error())
	case bodyProblem(err):
		return c.malformedRequest(op.Service, "Could not parse request body into json: "+reason(err))
	default:
		return awserr.Internalf(fmt.Errorf("decoding the %s input: %w", op.ID, err))
	}
}

// reason is the client-facing part of a body error.
func reason(err error) string {
	if decode, ok := errors.AsType[*decodeError](err); ok {
		return decode.Message
	}
	if syntax, ok := errors.AsType[*jsonx.SyntaxError](err); ok {
		return fmt.Sprintf("the body is not valid JSON at byte %d", syntax.Offset)
	}
	return err.Error()
}

func describe(want string) string {
	if want == "" {
		return "no body"
	}
	return want
}

// expectedMedia is the media type a body of shape s travels as: "" for
// no body, and checked false for a blob payload without a mediaType trait,
// which is not checked. hasDocument is the document rule for the side s is
// on.
func expectedMedia(s *smithy.Schema, hasDocument func(*smithy.Schema) bool) (want string, checked bool) {
	if p, ok := restbind.PayloadMember(s); ok {
		switch t := p.Target; {
		case t.MediaType != "":
			return t.MediaType, true
		case t.Type == smithy.TypeBlob:
			return "", false
		case t.Type == smithy.TypeString || t.Type == smithy.TypeEnum:
			return "text/plain", true
		default:
			return mediaJSON, true
		}
	}
	if hasDocument(s) {
		return mediaJSON, true
	}
	return "", true
}

// hasResponseDocument is restJson1's response rule: every structure
// output without a payload member is a JSON object, {} when all its
// members are bound to headers or the status. "Server implementations are
// expected to respond with a JSON object regardless of if the output
// parameters are empty" (RestJsonGreetingWithErrors; also
// RestJsonHttpResponseCode). Requests keep restbind.HasRequestDocument.
func hasResponseDocument(s *smithy.Schema) bool {
	if s == nil || s.Type != smithy.TypeStructure {
		return false
	}
	_, ok := restbind.PayloadMember(s)
	return !ok
}

// essence is a media type without parameters, lowercase; "" when it does
// not parse.
func essence(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil && !errors.Is(err, mime.ErrInvalidMediaParameter) {
		return ""
	}
	return mt
}

// accepts reports whether an Accept header admits want: a range equal to
// it, its type with a wildcard subtype, or */*.
func accepts(accept, want string) bool {
	kind, _, _ := strings.Cut(want, "/")
	for r := range strings.SplitSeq(accept, ",") {
		switch mt := essence(strings.TrimSpace(r)); mt {
		case want, "*/*", kind + "/*":
			return true
		}
	}
	return false
}

// Encode renders out, the handler's output for op, with the status the
// pipeline chose (an httpResponseCode member replaces it): header and
// prefix-header members as headers, the unbound members as a JSON object
// (application/json; {} when none is set, even when every member is bound),
// a payload as its bytes (application/octet-stream for a blob without a
// mediaType trait), and x-amzn-RequestId. A 1xx, 204 or 304 status sends
// no body (Lambda's DeleteFunction).
func (Codec) Encode(_ *http.Request, op *registry.Operation, requestID string, out smithy.Serializable, status int) (Response, *awserr.Error) {
	enc, err := restbind.Encode(op, out, status, hasResponseDocument, newWriter)
	if err != nil {
		return Response{}, awserr.Internalf(fmt.Errorf("encoding the %s output: %w", op.ID, err))
	}
	body, err := enc.Finish(mediaJSON)
	if err != nil {
		return Response{}, awserr.Internalf(fmt.Errorf("opening the %s output stream: %w", op.ID, err))
	}
	enc.Header.Set("X-Amzn-Requestid", requestID)
	return Response{Status: enc.Status, Header: enc.Header, Body: body}, nil
}

// EncodeError renders e as restJson1 errors are sent (wire-rest.md 4.2):
// status from the identity; X-Amzn-Errortype with the bare shape name; a
// JSON body with no __type, holding the error's fields (Lambda's Type and
// Reason) and then the message under the shape's own spelling (message or
// Message), {} when it has neither; the error's headers (Retry-After); and
// x-amzn-RequestId. It reads neither the request nor the service, which
// it takes to keep the signature the other REST codec's.
func (Codec) EncodeError(_ *http.Request, _ *registry.Service, requestID string, e *awserr.Error) Response {
	body := []byte{'{'}
	member := func(name, value string) {
		if len(body) > 1 {
			body = append(body, ',')
		}
		body = jsonx.AppendString(body, name)
		body = append(body, ':')
		body = jsonx.AppendString(body, value)
	}
	for _, f := range e.Fields {
		member(f.Name, f.Value)
	}
	if msg := e.MessageFor(ref.RESTJSON1); msg != "" {
		switch e.ID.MessageKey {
		case awserr.MessageLower:
			member("message", msg)
		case awserr.MessageUpper:
			member("Message", msg)
		case awserr.NoMessage:
		}
	}
	body = append(body, '}')
	h := http.Header{
		"Content-Type":     {mediaJSON},
		"X-Amzn-Errortype": {e.ID.Shape.Name()},
		"X-Amzn-Requestid": {requestID},
		"Content-Length":   {strconv.Itoa(len(body))},
	}
	for _, x := range e.Headers {
		h.Add(x.Name, x.Value)
	}
	return Response{Status: e.ID.Status, Header: h, Body: bytes.NewReader(body)}
}

// checkMediaTypes answers a wrong Content-Type with 415 and a wrong Accept
// with 406, when the service names the identities.
func (c Codec) checkMediaTypes(r *http.Request, op *registry.Operation) *awserr.Error {
	if c.MediaTypeErrors == nil {
		return nil
	}
	ids, ok := c.MediaTypeErrors(op.Service.ID)
	if !ok {
		return nil
	}
	contentType := r.Header.Get("Content-Type")
	if want, checked := expectedMedia(op.Input, restbind.HasRequestDocument); checked && r.ContentLength != 0 {
		if want == "" && contentType != "" || want != "" && essence(contentType) != want {
			return c.clientError(op.Service, ids.UnsupportedMediaType,
				fmt.Sprintf("The request's Content-Type %q is not the media type %s reads (%s).", contentType, op.ID.Name, describe(want)))
		}
	}
	accept := r.Header.Get("Accept")
	if want, checked := expectedMedia(op.Output, hasResponseDocument); checked && want != "" && accept != "" && !accepts(accept, want) {
		return c.clientError(op.Service, ids.NotAcceptable,
			fmt.Sprintf("The request's Accept %q excludes %s, the media type %s returns.", accept, want, op.ID.Name))
	}
	return nil
}

// malformedRequest is svc's answer to a request that does not decode.
func (c Codec) malformedRequest(svc *registry.Service, message string) *awserr.Error {
	id := svc.MalformedRequestError
	if id.Shape == "" {
		id = awserr.SerializationException()
	}
	return c.clientError(svc, id, message)
}

// clientError is a client fault the codec raises itself, with svc's
// ClientFaultFields.
func (c Codec) clientError(svc *registry.Service, id awserr.Identity, message string) *awserr.Error {
	e := &awserr.Error{ID: id, Message: message, Origin: awserr.Generic{}}
	if c.ClientFaultFields != nil {
		e.Fields = c.ClientFaultFields(svc.ID)
	}
	return e
}
